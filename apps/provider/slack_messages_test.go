package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

func slackMessageApp(t *testing.T, handle http.HandlerFunc) *zip.App {
	t.Helper()
	slackConfiguredEnv(t)
	stubSlackBridge(t, make(chan string, 1))
	app := newApp(t, newKMS(t))
	for org, code := range map[string]string{"acme": "acmecode", "globex": "globexcode"} {
		if r := connectSlack(t, app, org, code); r.Code != http.StatusFound {
			t.Fatalf("connect %s: %d %s", org, r.Code, r.Body)
		}
	}
	srv := httptest.NewServer(handle)
	t.Cleanup(srv.Close)
	slackWebAPIBase = srv.URL // stubSlackBridge restores the original on cleanup.
	return app
}

func TestSlackMessagesReadNamedChannelAndThread(t *testing.T) {
	var paths []string
	app := slackMessageApp(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer xoxb-acme" {
			t.Errorf("wrong tenant token: %q", got)
		}
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/conversations.list":
			if r.Form.Get("types") != "public_channel" {
				t.Error("public lookup must not require private-channel scopes")
			}
			if r.Form.Get("cursor") == "" {
				// An empty page can still have a continuation cursor.
				_, _ = w.Write([]byte(`{"ok":true,"channels":[],"response_metadata":{"next_cursor":"page2"}}`))
			} else {
				_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C123","name":"hanzo-gtm","is_member":true}]}`))
			}
		case "/conversations.history":
			if r.Form.Get("channel") != "C123" || r.Form.Get("limit") != "15" {
				t.Errorf("history args: %v", r.Form)
			}
			_, _ = w.Write([]byte(`{"ok":true,"messages":[{"ts":"1710000000.000002","user":"U1","text":"Latest GTM update","reply_count":2}],"has_more":true,"response_metadata":{"next_cursor":"older"}}`))
		case "/conversations.replies":
			if r.Form.Get("channel") != "C123" || r.Form.Get("ts") != "1710000000.000002" || r.Form.Get("cursor") != "replies2" || r.Form.Get("oldest") != "1710000000.000003" {
				t.Errorf("thread args: %v", r.Form)
			}
			_, _ = w.Write([]byte(`{"ok":true,"messages":[{"ts":"1710000000.000004","thread_ts":"1710000000.000002","text":"A reply"}]}`))
		default:
			t.Errorf("unexpected Slack call: %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	r := req(t, app, http.MethodGet, "/v1/provider/slack/messages?channel=%23hanzo-gtm&org=globex", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("history: %d %s", r.Code, r.Body)
	}
	var out slackMessagesOut
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Channel != "C123" || len(out.Messages) != 1 || out.Messages[0].TS != "1710000000.000002" || out.Messages[0].ReplyCount != 2 || !out.HasMore || out.NextCursor != "older" {
		t.Fatalf("lost message or pagination data: %+v", out)
	}
	r = req(t, app, http.MethodGet, "/v1/provider/slack/messages?channel=C123&thread_ts=1710000000.000002&cursor=replies2&oldest=1710000000.000003", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("thread: %d %s", r.Code, r.Body)
	}
	if !slices.Equal(paths, []string{"/conversations.list", "/conversations.list", "/conversations.history", "/conversations.replies"}) {
		t.Fatalf("unexpected calls: %v", paths)
	}
	r = req(t, app, http.MethodPost, "/mcp", "acme", map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "get_provider_slack_messages", "arguments": map[string]any{"channel": "C123"}},
	})
	if r.Code != http.StatusOK || !strings.Contains(string(r.Body), "Latest GTM update") {
		t.Fatalf("MCP history failed: %d %s", r.Code, r.Body)
	}
}

func TestSlackMessagesMCPProjectionAndSend(t *testing.T) {
	app := slackMessageApp(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" || r.Header.Get("Authorization") != "Bearer xoxb-acme" {
			t.Errorf("unexpected send: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["channel"] != "C123" || body["thread_ts"] != "1710000000.000002" || body["text"] != "Here is the summary" {
			t.Errorf("send body: %v", body)
		}
		_, _ = w.Write([]byte(`{"ok":true,"ts":"1710000000.000005"}`))
	})
	for _, id := range []string{"get_provider_slack_channels", "get_provider_slack_messages", "post_provider_slack_messages"} {
		found := false
		for _, tool := range app.MCPTools() {
			if tool["name"] == id {
				found = true
				if tool["description"] == "" {
					t.Errorf("%s has no MCP description", id)
				}
			}
		}
		if !found {
			t.Errorf("missing MCP tool %s", id)
		}
	}
	// Real MCP transport with the identity forwarded by the host. Extra org/token
	// arguments cannot change credential custody.
	r := req(t, app, http.MethodPost, "/mcp", "acme", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "post_provider_slack_messages", "arguments": map[string]any{
			"channel": "<#C123|hanzo-gtm>", "thread_ts": "1710000000.000002",
			"text": "Here is the summary", "org": "globex", "token": "attacker",
		}},
	})
	if r.Code != http.StatusOK || !strings.Contains(string(r.Body), "1710000000.000005") {
		t.Fatalf("MCP send failed: %d %s", r.Code, r.Body)
	}
}

func TestSlackMessagesErrorsAndGates(t *testing.T) {
	calls := 0
	app := slackMessageApp(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		if r.Form.Get("channel") == "G123" {
			_, _ = w.Write([]byte(`{"ok":false,"error":"missing_scope","needed":"groups:history"}`))
		} else {
			_, _ = w.Write([]byte(`{"ok":false,"error":"not_in_channel"}`))
		}
	})
	for _, tc := range []struct {
		path, org string
		status    int
	}{
		{"/v1/provider/slack/messages?channel=C123", "", 401},
		{"/v1/provider/slack/channels", "", 401},
		{"/v1/provider/slack/messages?channel=C123", "unconnected", 400},
		{"/v1/provider/slack/messages?channel=C123&limit=101", "acme", 400},
		{"/v1/provider/slack/messages?channel=C123&thread_ts=invalid", "acme", 400},
		{"/v1/provider/slack/messages", "acme", 400},
	} {
		r := req(t, app, http.MethodGet, tc.path, tc.org, nil)
		if r.Code != tc.status {
			t.Errorf("%s (%s): %d %s", tc.path, tc.org, r.Code, r.Body)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid/unauthenticated requests called Slack %d times", calls)
	}
	for channel, hint := range map[string]string{"G123": "groups:history", "C123": "invite Hanzo"} {
		r := req(t, app, http.MethodGet, "/v1/provider/slack/messages?channel="+channel, "acme", nil)
		if r.Code != 502 || !strings.Contains(string(r.Body), hint) {
			t.Errorf("missing actionable error: %d %s", r.Code, r.Body)
		}
	}
}

func TestSlackMessagesPrivateChannelsAndGroupDMs(t *testing.T) {
	var kinds []string
	app := slackMessageApp(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/conversations.list":
			kind := r.Form.Get("types")
			kinds = append(kinds, kind)
			switch kind {
			case "public_channel":
				_, _ = w.Write([]byte(`{"ok":true,"channels":[]}`))
			case "private_channel":
				_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"G123","name":"private-gtm","is_private":true,"is_member":true}]}`))
			case "mpim":
				if r.Form.Get("cursor") != "page2" {
					t.Errorf("missing list cursor: %v", r.Form)
				}
				_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"G456","is_mpim":true}],"response_metadata":{"next_cursor":"page3"}}`))
			}
		case "/conversations.history":
			if r.Form.Get("channel") != "G123" && r.Form.Get("channel") != "G456" {
				t.Errorf("wrong channel: %v", r.Form)
			}
			_, _ = w.Write([]byte(`{"ok":true,"messages":[]}`))
		default:
			t.Errorf("read unexpectedly called %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	for _, path := range []string{
		"/v1/provider/slack/messages?channel=%23private-gtm",
		"/v1/provider/slack/messages?channel=G456",
	} {
		r := req(t, app, http.MethodGet, path, "acme", nil)
		if r.Code != http.StatusOK || !strings.Contains(string(r.Body), `"messages":[]`) {
			t.Fatalf("read: %d %s", r.Code, r.Body)
		}
	}
	r := req(t, app, http.MethodGet, "/v1/provider/slack/channels?types=mpim&cursor=page2", "acme", nil)
	if r.Code != http.StatusOK || !strings.Contains(string(r.Body), `"is_mpim":true`) || !strings.Contains(string(r.Body), `"next_cursor":"page3"`) {
		t.Fatalf("group DM listing: %d %s", r.Code, r.Body)
	}
	if !slices.Equal(kinds, []string{"public_channel", "private_channel", "mpim"}) {
		t.Fatalf("unexpected channel lookups: %v", kinds)
	}
}

func TestSlackManifestURLsAreRegistered(t *testing.T) {
	app := newApp(t, newKMS(t))
	var m struct {
		OAuth struct {
			Redirects []string `json:"redirect_urls"`
		} `json:"oauth_config"`
		Features struct {
			Commands []struct {
				URL string `json:"url"`
			} `json:"slash_commands"`
		} `json:"features"`
		Settings struct {
			Events struct {
				URL string `json:"request_url"`
			} `json:"event_subscriptions"`
			Interactivity struct {
				URL string `json:"request_url"`
			} `json:"interactivity"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(slackManifestJSON, &m); err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, route := range app.Fiber().GetRoutes(true) {
		paths[route.Path] = true
	}
	paths[callbackPath("slack")] = paths["/v1/provider/:provider/callback"]
	urls := append([]string{}, m.OAuth.Redirects...)
	urls = append(urls, m.Settings.Events.URL, m.Settings.Interactivity.URL)
	for _, cmd := range m.Features.Commands {
		urls = append(urls, cmd.URL)
	}
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || !paths[u.Path] {
			t.Errorf("manifest URL has no server route: %s", raw)
		}
	}
	if !slices.Contains(m.OAuth.Redirects, "https://api.hanzo.ai"+callbackPath("slack")) {
		t.Error("OAuth callback is missing from manifest redirects")
	}
}
