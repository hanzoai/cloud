package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// stubSlackChannels answers conversations.list over two pages and records every
// conversations.join. "C-locked" refuses, so one channel's refusal can be shown
// not to end the walk.
func stubSlackChannels(t *testing.T) *[]string {
	t.Helper()
	joined := &[]string{}
	mux := http.NewServeMux()
	mux.HandleFunc("/conversations.list", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		page := map[string]any{"ok": true}
		if r.FormValue("cursor") == "" {
			page["channels"] = []map[string]any{
				{"id": "C-general", "name": "general", "is_member": false},
				{"id": "C-known", "name": "known", "is_member": true},
			}
			page["response_metadata"] = map[string]string{"next_cursor": "page2"}
		} else {
			page["channels"] = []map[string]any{
				{"id": "C-random", "name": "random", "is_member": false},
				{"id": "C-locked", "name": "locked", "is_member": false},
			}
			page["response_metadata"] = map[string]string{"next_cursor": ""}
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	mux.HandleFunc("/conversations.join", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		ch := r.FormValue("channel")
		if ch == "C-locked" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "is_archived"})
			return
		}
		*joined = append(*joined, ch)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	orig := slackWebAPIBase
	slackWebAPIBase = ts.URL
	t.Cleanup(func() { slackWebAPIBase = orig })
	return joined
}

// TestJoinWalksEveryPublicChannel proves the walk pages to the end, skips what it
// is already in, and reports a refusal per channel instead of abandoning the rest.
func TestJoinWalksEveryPublicChannel(t *testing.T) {
	joined := stubSlackChannels(t)

	out, err := slackJoinAll(context.Background(), "xoxb-test")
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if out.Listed != 4 {
		t.Errorf("listed = %d, want 4 across both pages", out.Listed)
	}
	if out.Already != 1 {
		t.Errorf("already = %d, want 1 (#known)", out.Already)
	}
	if !slices.Equal(out.Joined, []string{"general", "random"}) {
		t.Errorf("joined = %v, want [general random]", out.Joined)
	}
	// A channel it is already in is never asked for again — that skip is what
	// makes a second run cheap, which is how a rate-limited walk is finished.
	if slices.Contains(*joined, "C-known") {
		t.Error("a channel the bot is already in must not be joined again")
	}
	if len(out.Failed) != 1 || out.Failed[0].Name != "locked" || out.Failed[0].Error != "is_archived" {
		t.Errorf("failed = %+v, want one is_archived on #locked", out.Failed)
	}
}

// TestJoinRequiresOrgAdmin: joining every public channel changes what the whole
// workspace sees, so it is not a thing any member may do.
func TestJoinRequiresOrgAdmin(t *testing.T) {
	slackConfiguredEnv(t)
	stubSlackChannels(t)
	app := newApp(t, newKMS(t))
	res := req(t, app, http.MethodPost, "/v1/integration/slack/join", "acme", nil)
	if res.Code != http.StatusForbidden {
		t.Fatalf("a non-admin must be refused 403, got %d (%s)", res.Code, res.Body)
	}
	// The reason, not just the number: a 403 from a missing principal or an
	// unconnected workspace would pass a status-only check and prove nothing.
	if !strings.Contains(string(res.Body), "requires org admin") {
		t.Fatalf("the refusal must name the admin requirement, got %s", res.Body)
	}
}
