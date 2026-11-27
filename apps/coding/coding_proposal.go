package coding

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// ---- pure helpers ----

func codingTitle(repo, prompt string) string {
	p := headline(prompt)
	if p == "" {
		return "Agent changes to " + repo
	}
	if strings.HasPrefix(p, repo+": ") || conventional.MatchString(p) {
		return p
	}
	return repo + ": " + p
}

// conventional is a headline that already names the kind of change it is, the
// Conventional Commits way: "fix: …", "feat(api): …", "docs!: …". Prefixing one
// with the repository read "site: fix(header): …", which is two prefixes.
var conventional = regexp.MustCompile(`^(build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test|deps|security)(\([^()\s]+\))?!?: \S`)

// githubLogin is a GitHub login: what an assignee or a reviewer is. Anything
// else is refused before a request goes out, because a login is spliced into
// the request GitHub is sent.
var githubLogin = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9]){0,38}$`)

// maxPeople is how many assignees, or reviewers, one pull request is given:
// GitHub's own limit for assignees.
const maxPeople = 10

// people checks the assignees and reviewers a caller named and answers them
// trimmed, without blanks or repeats. A login that is not one is a 400.
func people(field string, logins []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, l := range logins {
		l = strings.TrimPrefix(strings.TrimSpace(l), "@")
		if l == "" || seen[strings.ToLower(l)] {
			continue
		}
		if !githubLogin.MatchString(l) {
			return nil, zip.ErrBadRequest(fmt.Sprintf("coding: %s: %q is not a GitHub login", field, l))
		}
		seen[strings.ToLower(l)] = true
		out = append(out, l)
	}
	if len(out) > maxPeople {
		return nil, zip.ErrBadRequest(fmt.Sprintf("coding: %s: at most %d logins", field, maxPeople))
	}
	return out, nil
}

// proposalOf is the Proposal a caller asked for, checked.
func proposalOf(draft bool, assignees, reviewers []string) (Proposal, error) {
	a, err := people("assignees", assignees)
	if err != nil {
		return Proposal{}, err
	}
	r, err := people("reviewers", reviewers)
	if err != nil {
		return Proposal{}, err
	}
	return Proposal{Draft: draft, Assignees: a, Reviewers: r}, nil
}

// prBody renders the PR description: who it is for, the task, the base..head,
// the tip, a diffstat, the session link so a reviewer can open the live run, and
// the issue it closes.
//
// The person is TEXT, never an @mention: on GitHub the App opens the proposal,
// and a forge login is not a GitHub login, so a mention would notify whoever
// holds that name there.
//
// `Closes #N` is the one line both hosts read as a link: the issue shows the
// proposal, and merging it closes the issue — which is what tells the thread
// the issue came from that the work landed. An issue in another repository is
// `Closes owner/name#N`, which both read the same way. issue is Closing's.
func prBody(prompt, base, branch, commit, diffstat, sessionID, person, issue string, commits ...client.CodingCommit) string {
	var b strings.Builder
	b.WriteString("Opened by the Hanzo coding agent")
	if p := strings.TrimSpace(person); p != "" {
		b.WriteString(" for `" + p + "`")
	}
	b.WriteString(".\n\n")
	b.WriteString("**Task:** " + headline(prompt) + "\n\n")
	b.WriteString("**Branch:** `" + branch + "` → base `" + baseOr(base) + "`\n")
	if commit != "" {
		b.WriteString("**Commit:** `" + commit + "`\n")
	}
	if len(commits) > 1 {
		b.WriteString("\n**Commits:**\n")
		for _, c := range slices.Backward(commits) {
			sha := strings.TrimSpace(c.SHA)
			if len(sha) > 10 {
				sha = sha[:10]
			}
			msg := firstLine(c.Message)
			if sha != "" && msg != "" {
				b.WriteString("- `" + sha + "` " + msg + "\n")
			}
		}
	}
	if strings.TrimSpace(diffstat) != "" {
		b.WriteString("\n```\n" + strings.TrimSpace(diffstat) + "\n```\n")
	}
	b.WriteString("\nSession: `/v1/agent/sessions/" + sessionID + "`\n")
	if issue != "" {
		b.WriteString("\nCloses " + issue + "\n")
	}
	return b.String()
}

// headline is a prompt's first line with leading Markdown heading markers stripped,
// cut to a title's length.
func headline(prompt string) string {
	p := strings.TrimSpace(strings.TrimLeft(firstLine(prompt), "#"))
	if len(p) > maxTitlePrompt {
		p = strings.TrimSpace(p[:maxTitlePrompt]) + "…"
	}
	return p
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if before, _, ok := strings.Cut(s, "\n"); ok {
		return strings.TrimSpace(before)
	}
	return s
}

// shortID returns the last 12 hex chars of a "sess_<hex>" id for a stable, unique
// branch suffix. Falls back to the whole id when it is short/unprefixed.
func shortID(id string) string {
	if i := strings.LastIndexByte(id, '_'); i >= 0 && i+1 < len(id) {
		id = id[i+1:]
	}
	if len(id) > 12 {
		return id[len(id)-12:]
	}
	return id
}

func baseOr(base string) string {
	if b := strings.TrimSpace(base); b != "" {
		return b
	}
	return "main"
}

// timeoutOr is a run's timeout in seconds: the caller's, else the default, and never more than a
// run may take (ceiling). It is budget (start.go) in seconds, so the run's context and the command
// inside it end together.
func timeoutOr(s int) int { return int(budget(s).Seconds()) }

// routedBudget is the longest a run sent to a machine of the org's own may be given: four hours, as
// every run was before a sandbox run's model credential was renewed. It rides to the machine and to
// the durable engine's activity deadline, so one that is not bounded would hold a slot of its org's
// routed concurrency — and a mailbox offer — for as long as a caller asked. The machine holds its own
// credentials, not a delegated one, so what bounds it is that slot and not a token. How long it
// should be is the owner's to say.
const routedBudget = 4 * time.Hour

func routedTimeoutOr(s int) int { return int(min(budget(s), routedBudget).Seconds()) }

func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
