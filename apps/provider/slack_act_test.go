package provider

// slack_act.go is the four things an agent can DO to a workspace it can already
// read: search it, rewrite a message, take one back, react. These tests hold the
// three properties that separate an act from a guess — the RESOLVED target is
// what Slack is sent, a scope the install never granted comes back as the scope
// to grant rather than as a server fault, and an argument this package refuses
// costs no outbound request at all.
//
// The harness is slackMessageApp (slack_messages_test.go): a stub server assigned
// to slackWebAPIBase, two connected orgs, and the real connect → callback
// handshake behind them, so the token every act carries is the one KMS custodied
// for the validated org.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// slackCall is one request the stub Slack saw. The FORM is recorded, not just the
// method, because the fact worth pinning is what an act sent: chat.update must
// carry the resolved channel id, and reactions.add names its target `timestamp`
// where its siblings name it `ts`.
type slackCall struct {
	Method string
	Form   url.Values
	Bearer string
}

// slackActApp is slackMessageApp with the calls recorded and the reply chosen per
// Slack method, which is the whole of what these tests need to vary. The returned
// pointer is read AFTER the requests, so a test can assert on a count of zero —
// the only way to say "no outbound request was made".
func slackActApp(t *testing.T, reply func(method string) string) (*zip.App, *[]slackCall) {
	t.Helper()
	var calls []slackCall
	app := slackMessageApp(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		calls = append(calls, slackCall{Method: r.URL.Path, Form: r.Form, Bearer: r.Header.Get("Authorization")})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply(r.URL.Path)))
	})
	return app, &calls
}

const slackActTS = "1710000000.000002"

// slackActs is the four ops with arguments Slack would accept, addressed by
// channel ID so nothing but the act itself is on the wire. DELETE carries no body
// (zip's hasBody), so its arguments ride the query — which is the wire, not a
// convenience.
func slackActs() []struct {
	Name, Method, Path string
	Body               any
} {
	return []struct {
		Name, Method, Path string
		Body               any
	}{
		{"search", http.MethodPost, "/v1/provider/slack/search",
			map[string]any{"query": "pipeline"}},
		{"update", http.MethodPut, "/v1/provider/slack/messages",
			map[string]any{"channel": "C123", "ts": slackActTS, "text": "edited by Hanzo"}},
		{"delete", http.MethodDelete, "/v1/provider/slack/messages?channel=C123&ts=" + slackActTS, nil},
		{"react", http.MethodPost, "/v1/provider/slack/reactions",
			map[string]any{"channel": "C123", "ts": slackActTS, "name": "eyes"}},
	}
}

func TestSlackActSearchesEditsDeletesAndReacts(t *testing.T) {
	app, calls := slackActApp(t, func(method string) string {
		switch method {
		case "/conversations.list":
			return `{"ok":true,"channels":[{"id":"C123","name":"hanzo-gtm","is_member":true}]}`
		case "/search.messages":
			return `{"ok":true,"messages":{"total":42,"pagination":{"page":2,"page_count":9},"matches":[` +
				`{"text":"Q3 pipeline","ts":"` + slackActTS + `","user":"U1","username":"zeekay",` +
				`"permalink":"https://acme.slack.com/archives/C123/p1710000000000002",` +
				`"channel":{"id":"C123","name":"hanzo-gtm"}}]}}`
		case "/chat.update", "/chat.delete":
			return `{"ok":true,"channel":"C123","ts":"` + slackActTS + `"}`
		case "/reactions.add":
			return `{"ok":true}`
		}
		return `{"ok":false,"error":"unexpected_method"}`
	})

	r := req(t, app, http.MethodPost, "/v1/provider/slack/search", "acme",
		map[string]any{"query": "in:#hanzo-gtm pipeline", "count": 5, "page": 2})
	if r.Code != http.StatusOK {
		t.Fatalf("search: %d %s", r.Code, r.Body)
	}
	var found slackSearchOut
	if err := json.Unmarshal(r.Body, &found); err != nil {
		t.Fatal(err)
	}
	if found.Total != 42 || found.Page != 2 || found.Pages != 9 || len(found.Matches) != 1 {
		t.Fatalf("lost the page or the count: %+v", found)
	}
	m := found.Matches[0]
	if m.Channel != "C123" || m.ChannelName != "hanzo-gtm" || m.User != "U1" || m.Username != "zeekay" ||
		m.TS != slackActTS || m.Text != "Q3 pipeline" ||
		!strings.HasPrefix(m.Permalink, "https://acme.slack.com/archives/") {
		t.Fatalf("a hit lost the handle a reply needs: %+v", m)
	}

	// A NAME here, so the edit proves the resolved id is what Slack is sent.
	r = req(t, app, http.MethodPut, "/v1/provider/slack/messages", "acme",
		map[string]any{"channel": "#hanzo-gtm", "ts": slackActTS, "text": "edited by Hanzo"})
	if r.Code != http.StatusOK {
		t.Fatalf("update: %d %s", r.Code, r.Body)
	}
	var edited slackUpdateMessageOut
	if err := json.Unmarshal(r.Body, &edited); err != nil {
		t.Fatal(err)
	}
	if edited.Channel != "C123" || edited.TS != slackActTS {
		t.Fatalf("an edit must name the message it changed: %+v", edited)
	}

	r = req(t, app, http.MethodDelete, "/v1/provider/slack/messages?channel=C123&ts="+slackActTS, "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", r.Code, r.Body)
	}
	var gone slackDeleteMessageOut
	if err := json.Unmarshal(r.Body, &gone); err != nil {
		t.Fatal(err)
	}
	if gone.Channel != "C123" || gone.TS != slackActTS {
		t.Fatalf("a delete must name what is gone: %+v", gone)
	}

	// Slack names an emoji without colons, and the ones a caller types come off.
	r = req(t, app, http.MethodPost, "/v1/provider/slack/reactions", "acme",
		map[string]any{"channel": "C123", "ts": slackActTS, "name": ":eyes:"})
	if r.Code != http.StatusOK {
		t.Fatalf("react: %d %s", r.Code, r.Body)
	}
	var reacted slackReactOut
	if err := json.Unmarshal(r.Body, &reacted); err != nil {
		t.Fatal(err)
	}
	if reacted.Channel != "C123" || reacted.TS != slackActTS || reacted.Name != "eyes" {
		t.Fatalf("react lost its target or kept its colons: %+v", reacted)
	}

	var got []string
	form := map[string]url.Values{}
	for _, c := range *calls {
		got = append(got, c.Method)
		form[c.Method] = c.Form
		if c.Bearer != "Bearer xoxb-acme" {
			t.Errorf("%s carried the wrong tenant's token: %q", c.Method, c.Bearer)
		}
	}
	if !slices.Equal(got, []string{
		"/search.messages", "/conversations.list", "/chat.update", "/chat.delete", "/reactions.add",
	}) {
		t.Fatalf("unexpected Slack calls: %v", got)
	}
	if f := form["/search.messages"]; f.Get("query") != "in:#hanzo-gtm pipeline" || f.Get("count") != "5" || f.Get("page") != "2" {
		t.Errorf("search args: %v", f)
	}
	if f := form["/chat.update"]; f.Get("channel") != "C123" || f.Get("ts") != slackActTS || f.Get("text") != "edited by Hanzo" {
		t.Errorf("update args: %v", f)
	}
	if f := form["/chat.delete"]; f.Get("channel") != "C123" || f.Get("ts") != slackActTS {
		t.Errorf("delete args: %v", f)
	}
	// reactions.add names the message `timestamp`; its siblings name it `ts`.
	if f := form["/reactions.add"]; f.Get("channel") != "C123" || f.Get("timestamp") != slackActTS || f.Get("name") != "eyes" {
		t.Errorf("react args: %v", f)
	}
}

// TestSlackActSurfacesTheScopeSlackAsksFor holds the difference between an
// actionable refusal and an outage. ok=false is Slack answering, so the status is
// 502 and the body names the scope to grant — never a 500, which says the fault
// is ours and tells the reader nothing they can act on.
func TestSlackActSurfacesTheScopeSlackAsksFor(t *testing.T) {
	needed := map[string]string{
		"/search.messages": "search:read",
		"/chat.update":     "chat:write",
		"/chat.delete":     "chat:write",
		"/reactions.add":   "reactions:write",
	}
	app, calls := slackActApp(t, func(method string) string {
		return `{"ok":false,"error":"missing_scope","needed":"` + needed[method] + `"}`
	})
	scope := map[string]string{
		"search": "search:read", "update": "chat:write",
		"delete": "chat:write", "react": "reactions:write",
	}
	for _, a := range slackActs() {
		r := req(t, app, a.Method, a.Path, "acme", a.Body)
		if r.Code != http.StatusBadGateway {
			t.Errorf("%s: a Slack refusal is 502, got %d %s", a.Name, r.Code, r.Body)
		}
		body := string(r.Body)
		if !strings.Contains(body, "missing_scope") || !strings.Contains(body, scope[a.Name]) {
			t.Errorf("%s: refusal must name missing_scope and %q: %s", a.Name, scope[a.Name], body)
		}
		if !strings.Contains(body, "reconnect Slack") {
			t.Errorf("%s: refusal must say how to fix it: %s", a.Name, body)
		}
	}
	if len(*calls) != 4 {
		t.Fatalf("each act asks Slack exactly once; got %d calls", len(*calls))
	}
}

// TestSlackActRefusesABadArgumentBeforeCallingSlack pins the ORDER. Every channel
// below is a NAME, which costs a conversations.list to resolve — so zero recorded
// calls proves the timestamp is checked before the lookup rather than after it.
func TestSlackActRefusesABadArgumentBeforeCallingSlack(t *testing.T) {
	app, calls := slackActApp(t, func(string) string {
		return `{"ok":true,"channels":[{"id":"C123","name":"hanzo-gtm","is_member":true}]}`
	})
	for _, tc := range []struct {
		Name, Method, Path string
		Body               any
	}{
		{"words are not a timestamp", http.MethodPut, "/v1/provider/slack/messages",
			map[string]any{"channel": "#hanzo-gtm", "ts": "yesterday", "text": "edited"}},
		{"a timestamp has a fraction", http.MethodPut, "/v1/provider/slack/messages",
			map[string]any{"channel": "#hanzo-gtm", "ts": "1710000000", "text": "edited"}},
		{"delete reads its ts off the query", http.MethodDelete,
			"/v1/provider/slack/messages?channel=%23hanzo-gtm&ts=1710000000", nil},
		{"six digits, not seven", http.MethodPost, "/v1/provider/slack/reactions",
			map[string]any{"channel": "#hanzo-gtm", "ts": "1710000000.0000001", "name": "eyes"}},
		{"no ts at all", http.MethodPost, "/v1/provider/slack/reactions",
			map[string]any{"channel": "#hanzo-gtm", "name": "eyes"}},
	} {
		if r := req(t, app, tc.Method, tc.Path, "acme", tc.Body); r.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d %s", tc.Name, r.Code, r.Body)
		}
	}
	// Search has no timestamp; its own pre-network guards are the query and the page.
	for _, body := range []map[string]any{
		{"query": "   "},
		{"query": "pipeline", "count": 101},
		{"query": "pipeline", "page": 101},
	} {
		if r := req(t, app, http.MethodPost, "/v1/provider/slack/search", "acme", body); r.Code != http.StatusBadRequest {
			t.Errorf("search %v: want 400, got %d %s", body, r.Code, r.Body)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("a refused argument reached Slack %d times: %+v", len(*calls), *calls)
	}
}

// TestSlackActFailsClosedWithoutAConnectedWorkspace: the token is custodied per
// org, so an org that has connected nothing has nothing to act with. It is
// refused before the wire, which is what keeps one tenant's act off another
// tenant's workspace.
func TestSlackActFailsClosedWithoutAConnectedWorkspace(t *testing.T) {
	app, calls := slackActApp(t, func(string) string { return `{"ok":true}` })
	for _, a := range slackActs() {
		r := req(t, app, a.Method, a.Path, "unconnected", a.Body)
		if r.Code != http.StatusBadRequest {
			t.Errorf("%s unconnected: want 400, got %d %s", a.Name, r.Code, r.Body)
		}
		if !strings.Contains(string(r.Body), "not connected") {
			t.Errorf("%s unconnected: say what is missing: %s", a.Name, r.Body)
		}
	}
	// No principal at all: there is no org whose token could be resolved.
	for _, a := range slackActs() {
		if r := req(t, app, a.Method, a.Path, "", a.Body); r.Code != http.StatusUnauthorized {
			t.Errorf("%s anonymous: want 401, got %d %s", a.Name, r.Code, r.Body)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("a caller with no workspace reached Slack %d times: %+v", len(*calls), *calls)
	}
}
