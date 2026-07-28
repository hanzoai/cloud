package cli

// agentpublish.go — `hanzo agent publish`: turn the agent session that built a
// repo into the READABLE BUILD of a project, so a visitor can follow how a
// template became a product and fork from any turn.
//
//	hanzo agent publish <project> [--transcript FILE] [--bind] [--dry-run]
//
// It reads a REAL session log — the harness's own JSONL, never a summary someone
// wrote afterwards — pairs each turn with the commit it produced AS RECORDED IN
// GIT, refuses locally if any turn carries a credential, and posts the result to
// /v1/agents/sessions. A fabricated transcript would be worthless the moment
// anyone diffed it against the commits, so the only thing this ships is the log
// that actually happened.
//
// THE GUARD RUNS HERE FIRST. The server refuses a leaking turn too (that is the
// real boundary), but scanning locally means the secret never crosses the wire
// at all, and the author is told which line of their own transcript to fix.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/agents"
	"github.com/hanzoai/cloud/clients/security/detect"
	"github.com/spf13/cobra"
)

// transcriptTurn is one turn as this command understands it: who spoke, what was
// said, when — plus the commit git says that turn produced.
type transcriptTurn struct {
	Role    string
	Text    string
	At      time.Time
	Commit  string
	Subject string
	Model   string
}

func newAgentPublishCmd(envOf func() *Env) *cobra.Command {
	var transcript, repoDir, title, agentLabel string
	var bind, dryRun, private bool
	c := &cobra.Command{
		Use:   "publish <project>",
		Short: "Publish this repo's agent session as the readable build of <project>",
		Long: "Read the agent session that built this repo and publish it as the build story\n" +
			"of <project>: every turn, paired with the commit it produced as recorded in git\n" +
			"(Hanzo-Session/Hanzo-Turn trailers, or notes under " + agents.NotesRef + ").\n\n" +
			"Turns are scanned for credentials before anything is sent and the command FAILS\n" +
			"on a hit — secrets belong in KMS and are referenced by name, so a truthful\n" +
			"transcript has nothing to hide. Use --bind to record the turn⇄commit binding as\n" +
			"git notes for commits that predate the trailer convention.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			project := args[0]
			if repoDir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				repoDir = wd
			}
			if transcript == "" {
				p, err := findTranscript(repoDir)
				if err != nil {
					return err
				}
				transcript = p
			}
			turns, err := readTranscript(transcript)
			if err != nil {
				return err
			}
			if len(turns) == 0 {
				return fmt.Errorf("%s holds no turns — nothing to publish", transcript)
			}

			// The guard, before the wire. Report EVERY offending turn at once so
			// the author fixes the transcript in one pass, then rotates.
			if bad := scanTurns(turns); len(bad) > 0 {
				for _, line := range bad {
					fmt.Fprintln(cmd.ErrOrStderr(), line)
				}
				return fmt.Errorf("refusing to publish: %d secret(s) found in the transcript; "+
					"rotate the exposed values and reference them from KMS by name", len(bad))
			}

			// Git states the binding. --bind records it for commits made during the
			// session that never carried a trailer.
			if bind {
				n, err := bindNotes(ctx, repoDir, sessionIDOf(transcript), turns)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "bound %d commit(s) via %s\n", n, agents.NotesRef)
			}
			links, err := readLinks(ctx, repoDir)
			if err != nil {
				return err
			}
			byTurn := map[int64]agents.Link{}
			for _, l := range links {
				byTurn[l.Turn] = l
			}
			for i := range turns {
				if l, ok := byTurn[int64(i+1)]; ok {
					turns[i].Commit = l.Commit
					turns[i].Subject = commitSubject(ctx, repoDir, l.Commit)
				}
			}

			if title == "" {
				title = turns[0].Text
				if len(title) > 120 {
					title = title[:120]
				}
			}
			if agentLabel == "" {
				agentLabel = "claude-code"
			}
			bound := 0
			for _, t := range turns {
				if t.Commit != "" {
					bound++
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %d turns, %d bound to commits (%s)\n",
				project, len(turns), bound, filepath.Base(transcript))
			if dryRun {
				return nil
			}

			e := envOf()
			var sess struct {
				ID string `json:"id"`
			}
			if err := cloudCall(ctx, e, "POST", "/v1/agents/sessions", map[string]any{
				"agent": agentLabel, "title": title, "project": project,
				"repo": originURL(ctx, repoDir), "cwd": repoDir,
				"provider": "claude", "status": "done", "published": !private,
			}, &sess); err != nil {
				return err
			}
			for i, t := range turns {
				body, _ := json.Marshal(map[string]any{
					"text": t.Text, "commit": t.Commit, "subject": t.Subject, "model": t.Model,
				})
				if err := cloudCall(ctx, e, "POST", "/v1/agents/sessions/"+sess.ID+"/events",
					map[string]any{"kind": "message", "actor": t.Role,
						"payload": json.RawMessage(body)}, nil); err != nil {
					return fmt.Errorf("turn %d: %w", i+1, err)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "published %s → %s/v1/agents/builds/<org>/%s\n",
				sess.ID, e.CloudURL, project)
			return nil
		},
	}
	c.Flags().StringVar(&transcript, "transcript", "", "session log to publish (default: this repo's newest)")
	c.Flags().StringVar(&repoDir, "repo", "", "repo whose git history holds the bindings (default: cwd)")
	c.Flags().StringVar(&title, "title", "", "build title (default: the opening prompt)")
	c.Flags().StringVar(&agentLabel, "agent", "", "agent label (default: claude-code)")
	c.Flags().BoolVar(&bind, "bind", false, "record turn⇄commit bindings as git notes for unbound commits")
	c.Flags().BoolVar(&private, "private", false, "keep the build unpublished (owner-only)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "read, guard and pair, but send nothing")
	return c
}

// ---- reading a real session log ----

// findTranscript locates the harness's own log for a working directory. Claude
// Code stores one JSONL per session under ~/.claude/projects/<path-with-slashes
// -as-dashes>/. The NEWEST is the session that just ran.
func findTranscript(dir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	key := strings.ReplaceAll(strings.ReplaceAll(abs, "/", "-"), ".", "-")
	glob := filepath.Join(home, ".claude", "projects", key, "*.jsonl")
	matches, _ := filepath.Glob(glob)
	if len(matches) == 0 {
		return "", fmt.Errorf("no session log for %s (looked in %s) — pass --transcript", abs, glob)
	}
	sort.Slice(matches, func(i, j int) bool {
		a, _ := os.Stat(matches[i])
		b, _ := os.Stat(matches[j])
		return a.ModTime().After(b.ModTime())
	})
	return matches[0], nil
}

func sessionIDOf(transcript string) string {
	return strings.TrimSuffix(filepath.Base(transcript), ".jsonl")
}

// jsonlRecord is the subset of a harness log line this command reads. Everything
// else (snapshots, mode changes, queue bookkeeping) is machinery, not a turn.
type jsonlRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

// readTranscript turns a session log into ordered turns. Assistant "thinking" is
// INCLUDED — the reasoning is the point of a readable build; a transcript with
// the thinking stripped out is a changelog, not a session. Tool calls are
// rendered as one line naming the tool, because the argument blobs are noise a
// reader skips and the diff they produced is already in the commit.
func readTranscript(path string) ([]transcriptTurn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []transcriptTurn
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var rec jsonlRecord
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if rec.Type != "user" && rec.Type != "assistant" {
			continue
		}
		var msg struct {
			Role    string          `json:"role"`
			Model   string          `json:"model"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(rec.Message, &msg) != nil {
			continue
		}
		text := renderContent(msg.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		at, _ := time.Parse(time.RFC3339, rec.Timestamp)
		out = append(out, transcriptTurn{Role: rec.Type, Text: text, At: at, Model: msg.Model})
	}
	return out, sc.Err()
}

// renderContent flattens a message body to readable text. Content is either a
// bare string or the block array (text / thinking / tool_use / tool_result).
func renderContent(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Thinking string          `json:"thinking"`
		Name     string          `json:"name"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, x := range blocks {
		switch x.Type {
		case "text":
			b.WriteString(x.Text)
		case "thinking":
			if t := strings.TrimSpace(x.Thinking); t != "" {
				b.WriteString("[thinking] " + t)
			}
		case "tool_use":
			b.WriteString("[" + x.Name + "]")
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// scanTurns runs the SAME engine the server runs, locally, and returns one human
// -readable line per finding — turn number, rule, and a masked preview.
func scanTurns(turns []transcriptTurn) []string {
	var out []string
	for i, t := range turns {
		for _, f := range detect.ScanContent("turn "+strconv.Itoa(i+1), t.Text) {
			out = append(out, fmt.Sprintf("turn %d: %s (%s) %s", i+1, f.RuleName, f.Severity, f.Preview))
		}
	}
	return out
}

// ---- git: the authority on which turn produced which commit ----

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// readLinks asks git for every commit⇄turn binding it holds, using the exact
// format the server's parser publishes. No notes ref yet ⇒ no bindings, not an
// error: a repo can be published before anything was bound.
func readLinks(ctx context.Context, dir string) ([]agents.Link, error) {
	out, err := git(ctx, dir, "log", "--format="+agents.ProvenanceLogFormat, "--notes="+agents.NotesRef)
	if err != nil {
		out, err = git(ctx, dir, "log", "--format="+agents.ProvenanceLogFormat)
		if err != nil {
			return nil, err
		}
	}
	return agents.ParseLinks(out), nil
}

func commitSubject(ctx context.Context, dir, sha string) string {
	out, err := git(ctx, dir, "log", "-1", "--format=%s", sha)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func originURL(ctx context.Context, dir string) string {
	out, err := git(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// bindNotes records the binding for commits that carry no trailer, by asking the
// one question a timestamp can honestly answer: which turn was in flight when
// this commit was authored? A commit authored between turn N and turn N+1 was
// produced during turn N.
//
// The note SAYS SO — it carries `Hanzo-Bind: time` — so a reader can tell a
// derived link from one the agent declared about itself in a trailer. A commit
// that already carries either is never touched: git's existing statement wins,
// and re-running this is idempotent.
func bindNotes(ctx context.Context, dir, session string, turns []transcriptTurn) (int, error) {
	existing := map[string]bool{}
	links, err := readLinks(ctx, dir)
	if err != nil {
		return 0, err
	}
	for _, l := range links {
		existing[l.Commit] = true
	}
	// Commits authored inside the session's window, oldest first.
	if len(turns) == 0 {
		return 0, nil
	}
	first, last := turns[0].At, turns[len(turns)-1].At
	if first.IsZero() {
		return 0, fmt.Errorf("transcript has no timestamps — cannot bind by time")
	}
	out, err := git(ctx, dir, "log", "--reverse", "--format=%H %aI",
		"--since="+first.Format(time.RFC3339), "--until="+last.Add(time.Minute).Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		sha, ts, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || existing[sha] {
			continue
		}
		at, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			continue
		}
		turn := turnInFlight(turns, at)
		if turn == 0 {
			continue
		}
		note := agents.TrailerSession + ": " + session + "\n" +
			agents.TrailerTurn + ": " + strconv.Itoa(turn) + "\n" +
			"Hanzo-Bind: time\n"
		if _, err := git(ctx, dir, "notes", "--ref="+agents.NotesRef, "add", "-f", "-m", note, sha); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// turnInFlight returns the 1-based turn that was running at t — the last turn
// that had started. 0 when t precedes the session.
func turnInFlight(turns []transcriptTurn, t time.Time) int {
	turn := 0
	for i, x := range turns {
		if x.At.IsZero() || x.At.After(t) {
			break
		}
		turn = i + 1
	}
	return turn
}
