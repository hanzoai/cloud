package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// issue_turn_test.go proves the issue-tracker turns: a comment that addresses
// the agent — and only such a comment — becomes the normalized inbound the
// channels transports read, a Bot's comment never does, and the GitHub reply is
// posted as one issue comment through the installation.

func TestStripMention(t *testing.T) {
	for in, want := range map[string]struct {
		text string
		ok   bool
	}{
		"@hanzo fix the tests":           {"fix the tests", true},
		"please @Hanzo, look":            {"please , look", true},
		"@hanzoai is someone else":       {"", false},
		"mail me at x@hanzo.example":     {"", false},
		"no mention here":                {"", false},
		"@hanzo":                         {"", true},
		"two @hanzo and @hanzo mentions": {"two  and @hanzo mentions", true},
	} {
		got, ok := stripMention(in, "@hanzo")
		if ok != want.ok || got != want.text {
			t.Errorf("stripMention(%q) = %q,%v want %q,%v", in, got, ok, want.text, want.ok)
		}
	}
}

func TestGitHubMentionInbound(t *testing.T) {
	t.Setenv(githubAppSlugEnv, "hanzo")
	ev := githubIssueEvent{Action: "created"}
	ev.Issue.Number = 7
	ev.Repository.FullName = "acme/widgets"
	ev.Installation.ID = 111
	ev.Comment.ID = 99
	ev.Comment.Body = "@hanzo why do the billing tests fail?"
	ev.Comment.User.ID = 42
	ev.Comment.User.Type = "User"
	in, ok := githubMentionInbound(ev)
	if !ok {
		t.Fatal("a mention by a person must be a turn")
	}
	if in.Provider != "github" || in.ExternalID != "111" || in.User != "42" || in.Channel != "acme/widgets#7" ||
		in.Text != "why do the billing tests fail?" || in.DedupeKey != "issue_comment:99" {
		t.Fatalf("inbound = %+v", in)
	}
	ev.Comment.User.Type = "Bot"
	if _, ok := githubMentionInbound(ev); ok {
		t.Fatal("a Bot's comment is never a turn")
	}
	ev.Comment.User.Type = "User"
	ev.Comment.Body = "no mention"
	if _, ok := githubMentionInbound(ev); ok {
		t.Fatal("a comment without the mention is not a turn")
	}
	ev.Comment.Body = "@hanzo hi"
	ev.Action = "edited"
	if _, ok := githubMentionInbound(ev); ok {
		t.Fatal("only a created comment is a turn")
	}
}

func TestLinearMentionInbound(t *testing.T) {
	data, _ := json.Marshal(map[string]any{"id": "c1", "body": "@hanzo take a look", "issueId": "iss-1", "userId": "u-9"})
	ev := linearEvent{Action: "create", Type: "Comment", OrganizationID: "org-1", Data: data}
	in, ok := linearMentionInbound(ev)
	if !ok || in.Provider != "linear" || in.ExternalID != "org-1" || in.User != "u-9" || in.Channel != "iss-1" ||
		in.Text != "take a look" || in.DedupeKey != "comment:c1" {
		t.Fatalf("inbound = %+v ok=%v", in, ok)
	}
	ev.Action = "update"
	if _, ok := linearMentionInbound(ev); ok {
		t.Fatal("only a created comment is a turn")
	}
}

func TestGitHubIssueCommentPostsThroughTheInstallation(t *testing.T) {
	var got struct {
		path string
		body map[string]string
		auth string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "ghs_installation_token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/7/comments":
			got.path = r.URL.Path
			got.auth = r.Header.Get("Authorization")
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &got.body)
			_, _ = io.WriteString(w, `{"id": 555}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	withGithubApp(t, srv)
	_ = newApp(t, newKMS(t))
	_ = mounted.State.store.Upsert(context.Background(), Connection{Org: "acme", Provider: "github", Label: "acme", ExternalID: "111", AccountLabel: "acme"})

	id, err := githubIssueComment(context.Background(), "acme", "acme/widgets#7", "on it")
	if err != nil {
		t.Fatal(err)
	}
	if id != "555" || got.body["body"] != "on it" || got.auth == "" {
		t.Fatalf("post = id %q path %q body %v auth-present %v", id, got.path, got.body, got.auth != "")
	}
	if _, err := githubIssueComment(context.Background(), "acme", "not-a-room", "x"); err == nil {
		t.Fatal("a room that is not owner/repo#N must be refused")
	}
}
