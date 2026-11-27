package provider

// github_thread.go is what happens to an issue, said where it was asked for.
//
// An issue filed from a chat thread is bound to that thread: its board row names
// the thread as its room (apps/task). Every event GitHub sends about it after
// that — a comment, a close, an assignment, a pull request that closes it — is
// one line in the thread, so the people who asked hear how it went without
// watching GitHub. An issue labelled `agent` is handed to the coding agent.
//
// THE APP'S OWN WRITES ARE NEVER SAID AGAIN. An issue filed, commented on or
// assigned from a thread comes back as an event signed by the App's bot, and the
// thread that asked already has the answer; saying it twice is the first half of
// a loop. A run the thread started posts its own progress, so the pull request
// it opens is not announced either — its merge, by a person, is.

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	agentspeer "github.com/hanzoai/cloud/client/agent"
	toolpeer "github.com/hanzoai/cloud/client/tool"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/plane"
	ansi "github.com/hanzoai/cloud/sdk/text"
	"github.com/zap-proto/zip"
)

const (
	// threadBudget bounds telling one event, from the mirror to the message.
	threadBudget = 20 * time.Second
	// handBudget bounds admitting a run, which resolves a credential and opens a
	// session before it answers.
	handBudget = 2 * time.Minute
	// agentLabel hands an issue to the coding agent. A GitHub App cannot be an
	// assignee, so the handing is a label — one anyone who can triage the
	// repository can put on, from GitHub or from a chat thread alike.
	agentLabel = "agent"
	// maxLine bounds one quoted value in a line.
	maxLine = 300
	// maxCloses bounds the issues one pull request is told to.
	maxCloses = 10
)

// self reports whether an event is the App's own write coming back. With the
// App's slug unset every bot is taken for it, which can only drop a line.
func self(a githubActor) bool {
	if a.Type != "Bot" {
		return false
	}
	slug := env.Or(githubAppSlugEnv, "")
	return slug == "" || strings.EqualFold(a.Login, slug+"[bot]")
}

// follow tells the issue's thread what one event did, and hands an issue
// labelled `agent` to the coding agent. Best-effort: the delivery answered for
// the mirror, and a thread that cannot hear costs a line.
func follow(s *cloud.Service[state], org string, ev githubIssueEvent, room string) {
	if self(ev.Sender) {
		return
	}
	if ev.Action == "labeled" && strings.EqualFold(ev.Label.Name, agentLabel) && !strings.EqualFold(ev.Issue.State, "closed") {
		hand(s, org, ev, room)
		return
	}
	if line := heard(ev); line != "" {
		ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), threadBudget)
		defer cancel()
		tell(ctx, s, org, room, line)
	}
}

// heard is the line an issue's thread hears for one event, "" for an event it
// does not. Pure, so what a thread will say is checkable without a Slack.
func heard(ev githubIssueEvent) string {
	ref := "`" + esc(ev.Repository.FullName+"#"+strconv.Itoa(ev.Issue.Number)) + "`"
	who := "*" + esc(ev.Sender.Login) + "*"
	switch {
	case ev.Comment.ID != 0:
		if ev.Action != "created" {
			return ""
		}
		return ":speech_balloon: " + who + " on " + ref + ": " + esc(ev.Comment.Body)
	case ev.Action == "closed":
		return ":white_check_mark: " + ref + " closed by " + who
	case ev.Action == "reopened":
		return ":leftwards_arrow_with_hook: " + ref + " reopened by " + who
	case ev.Action == "assigned":
		return ":bust_in_silhouette: " + ref + " assigned to *" + esc(ev.Assignee.Login) + "* by " + who
	case ev.Action == "unassigned":
		return ":bust_in_silhouette: " + ref + " no longer assigned to *" + esc(ev.Assignee.Login) + "*"
	}
	return ""
}

// hand starts the coding run an issue labelled `agent` asks for, as the person
// who labelled it: their linked Hanzo account, or the org's default subject for
// someone who is not linked, which is who a comment addressed to the App runs
// as too. Labelling takes triage rights on the repository, so an outsider
// cannot spend the org's runs. The run narrates in the issue's thread, or the
// org's issue channel when it has none (apps/coding), and a run that does not
// start says why on the issue, where the person who labelled it is looking.
func hand(s *cloud.Service[state], org string, ev githubIssueEvent, room string) {
	ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), handBudget)
	defer cancel()
	ref := ev.Repository.FullName + "#" + strconv.Itoa(ev.Issue.Number)
	link, say, _ := channelIdentity(s, org, "github",
		strconv.FormatInt(ev.Installation.ID, 10), strconv.FormatInt(ev.Sender.ID, 10))
	why := ""
	if say != "" || link.Subject == "" {
		why = ev.Sender.Login + " has no Hanzo account linked to their GitHub"
	} else {
		to, _ := client.ReplyTo(room)
		in := client.Work(issueRepo(org, ev.Repository.FullName), ev.Issue.Number, ev.Issue.Title, ev.Issue.Body, to)
		as := zip.WithCaller(ctx, zip.Caller{Org: org, User: link.Subject})
		if _, err := plane.Call[client.CodingStartIn, client.CodingStarted](as, agentspeer.App, client.AgentsCodingStart, &in); err != nil {
			why = err.Error()
		}
	}
	if why == "" {
		return
	}
	s.Log.Warn("github: an issue handed to the agent did not start a run", "org", org, "issue", ref, "why", why)
	tell(ctx, s, org, room, ":x: `"+esc(ref)+"` was handed to the agent, but no run started: "+esc(why))
	if _, err := githubIssueComment(ctx, org, ref, "The agent did not start: "+ansi.Plain(why)); err != nil {
		s.Log.Warn("github: could not say on the issue why no run started", "org", org, "issue", ref, "err", err)
	}
}

// issueRepo is how a run names the repository a GitHub issue is on. The estate
// reaches it through the forge, where it is mirrored under the bare name, as it
// always has. Every other org names it by GitHub's own address: a bare
// `owner/name` spelled like the org's workspace on the forge is the workspace's
// (forge.GitHub), and the issue is on GitHub whatever the workspace holds.
func issueRepo(org, full string) string {
	if _, err := forge.Owner(org); err == nil {
		return full
	}
	return "github.com/" + full
}

// tell posts one line in the thread a room names. A room that is not a Slack
// thread — a team room, or none — hears nothing from here.
func tell(ctx context.Context, s *cloud.Service[state], org, room, line string) {
	to, ok := client.ReplyTo(room)
	if !ok || to.Provider != "slack" {
		return
	}
	if _, err := SendSlackAt(ctx, org, to.Channel, to.Thread, "", line, client.Controls{}, client.Question{}); err != nil {
		s.Log.Warn("github: the issue's thread did not hear it", "org", org, "err", err)
	}
}

// esc makes GitHub's text safe to put in a Slack line: one line of it, bounded,
// with the three characters mrkdwn reads as markup written as text, and the
// Markdown link the edge would otherwise turn into one broken apart. Everything
// here was typed on GitHub by whoever could comment, so none of it may become a
// link under the App's name or a page to the whole workspace.
func esc(s string) string {
	s = strings.Join(strings.Fields(ansi.Plain(s)), " ")
	if len(s) > maxLine {
		s = strings.ToValidUTF8(s[:maxLine], "") + "…"
	}
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return strings.ReplaceAll(s, "](", "] (")
}

// ── pull requests ─────────────────────────────────────────────────────────────

// githubPullEvent is the slice of GitHub's `pull_request` payload a thread hears.
type githubPullEvent struct {
	Action string `json:"action"`
	Pull   struct {
		gatePull
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		Merged  bool   `json:"merged"`
	} `json:"pull_request"`
	Repository struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Sender githubActor `json:"sender"`
}

// closing is GitHub's own closing keywords, naming an issue on the same
// repository: the words that make a merge close it.
var closing = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s+#(\d+)\b`)

// closes is the issues a pull request's text closes, in order, each once.
func closes(text string) []int {
	var out []int
	for _, m := range closing.FindAllStringSubmatch(text, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 || slices.Contains(out, n) {
			continue
		}
		if out = append(out, n); len(out) == maxCloses {
			break
		}
	}
	return out
}

// handleGitHubPullEvent has the forge judge a pull request that opened or moved
// (gate.go), and tells the threads of the issues it closes that it was opened or
// merged. Answered at once and told off the delivery, for the reason follow is;
// anything it does not act on is a benign 200.
func handleGitHubPullEvent(s *cloud.Service[state], c *zip.Ctx, body []byte) error {
	var ev githubPullEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return zip.ErrBadRequest("invalid pull request payload")
	}
	if ev.Action != "opened" && ev.Action != "reopened" && ev.Action != "synchronize" && ev.Action != "closed" {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "pull request " + ev.Action})
	}
	if ev.Installation.ID == 0 || ev.Repository.FullName == "" {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "pull request"})
	}
	org, ok := OrgForExternalID("github", strconv.FormatInt(ev.Installation.ID, 10))
	if !ok {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "unknown installation"})
	}
	// Whoever opened it, the App included: an agent's pull request is judged like a
	// person's, and a status written here opens no pull request event.
	if owner, repo, _ := strings.Cut(ev.Repository.FullName, "/"); owner != "" && repo != "" {
		if ev.Action == "closed" {
			gateRun(s, owner, "closing the forge's pull request", func(ctx context.Context) error { return gateClosed(ctx, s, owner, repo, ev.Pull.gatePull) })
		} else {
			gateRun(s, owner, "opening the forge's pull request", func(ctx context.Context) error { return gateOpen(ctx, s, owner, repo, ev.Pull.gatePull) })
		}
	}
	if self(ev.Sender) || (ev.Action == "closed" && !ev.Pull.Merged) {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "pull request"})
	}
	var activatedSkills []string
	if ev.Action == "opened" || ev.Action == "reopened" || ev.Action == "synchronize" {
		// The tool plane is its own process: it switches the skills on, and this one asks.
		if out, err := toolpeer.ToolsReview(cloud.For(c.Context(), org), &client.ReviewIn{Project: ev.Repository.Name}); err != nil {
			s.Log.Warn("github: a pull request's review skills were not switched on", "org", org, "repo", ev.Repository.FullName, "err", err)
		} else {
			activatedSkills = out.Activated
		}
	}
	nums := closes(ev.Pull.Title + "\n" + ev.Pull.Body)
	if len(nums) > 0 && (ev.Action == "opened" || (ev.Action == "closed" && ev.Pull.Merged)) {
		go pulled(s, org, ev, nums)
	}
	return c.JSON(http.StatusOK, map[string]any{"closes": len(nums), "activatedSkills": activatedSkills})
}

// pulled tells each issue's thread about the pull request. The issue is read
// and mirrored again, because the event names only its number and the mirror
// is where its thread is written down.
func pulled(s *cloud.Service[state], org string, ev githubPullEvent, nums []int) {
	ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), threadBudget)
	defer cancel()
	owner, _, _ := strings.Cut(ev.Repository.FullName, "/")
	tok, err := InstallationToken(ctx, org, owner)
	if err != nil {
		s.Log.Warn("github: a pull request's issues were not told", "org", org, "repo", ev.Repository.FullName, "err", err)
		return
	}
	verb := "opened"
	if ev.Action == "closed" {
		verb = "merged"
	}
	for _, n := range nums {
		var is githubIssue
		if err := githubGet(ctx, tok, "/repos/"+ev.Repository.FullName+"/issues/"+strconv.Itoa(n), &is); err != nil || is.PullRequest != nil {
			continue
		}
		res, err := mirrorGitHubIssue(ctx, org, ev.Repository.Name, ev.Repository.FullName, is, "")
		if err != nil || res.Channel == "" {
			continue
		}
		tell(ctx, s, org, res.Channel, ":twisted_rightwards_arrows: *"+esc(ev.Sender.Login)+"* "+verb+" a pull request that closes `"+
			esc(ev.Repository.FullName+"#"+strconv.Itoa(n))+"`: "+esc(ev.Pull.Title)+" "+esc(ev.Pull.HTMLURL))
	}
}
