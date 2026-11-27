package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// slack_rooms_test.go — which messages the agent answers, and where.
//
// In a DM it answers every message. In a room (a channel, a private channel, a
// group DM) it answers only what is said TO it: an @mention of the bot, or a
// message in a thread it answered in or was summoned into. A message that only
// spells the app's name is not addressed to it, and neither is a reply under a
// notice the bot posted outside a turn.

// TestOnlyWhatIsSaidToTheAgentIsHeard drives the pure router and the addressed
// decision over every surface Slack can deliver, and asserts both halves: whether
// a turn runs at all, and where the answer goes.
func TestOnlyWhatIsSaidToTheAgentIsHeard(t *testing.T) {
	const team = `"type":"event_callback","team_id":"T1","event_id":"E1","authorizations":[{"user_id":"UBOT","is_bot":true}],`
	joined := map[string]bool{"C1:9.9": true, "C1:4.0": true} // threads the agent was summoned into or answered in
	cases := []struct {
		name     string
		event    string
		heard    bool
		wantThre string // the reply's thread_ts: "" means answer in the room
	}{
		// A DM is a conversation with the agent: every message is answered.
		{"dm", `{"type":"message","channel_type":"im","user":"U1","text":"hi","channel":"D1","ts":"1.1"}`, true, ""},
		{"threaded dm", `{"type":"message","channel_type":"im","user":"U1","text":"hi","channel":"D1","ts":"2.2","thread_ts":"1.1"}`, true, "1.1"},

		// In a room, a message nobody addressed to the agent is not answered —
		// including one that spells the app's name without mentioning it.
		{"public channel chatter", `{"type":"message","channel_type":"channel","user":"U1","text":"hi all","channel":"C1","ts":"1.1"}`, false, ""},
		{"private channel chatter", `{"type":"message","channel_type":"group","user":"U1","text":"hi all","channel":"G1","ts":"1.1"}`, false, ""},
		{"group dm chatter", `{"type":"message","channel_type":"mpim","user":"U1","text":"hi all","channel":"G2","ts":"1.1"}`, false, ""},
		{"the name without a mention", `{"type":"message","channel_type":"channel","user":"U1","text":"Hanzo are you still speaking out of turn?","channel":"C1","ts":"1.1"}`, false, ""},
		{"mentioning someone else", `{"type":"message","channel_type":"channel","user":"U1","text":"<@U2> let us know how it goes","channel":"C1","ts":"1.1"}`, false, ""},
		{"a thread the bot is not in", `{"type":"message","channel_type":"channel","user":"U1","text":"thanks","channel":"C1","ts":"2.2","thread_ts":"1.1","parent_user_id":"U2"}`, false, ""},
		{"a reply under a notice the bot posted", `{"type":"message","channel_type":"channel","user":"U1","text":"why did it fail","channel":"C1","ts":"5.1","thread_ts":"5.0","parent_user_id":"UBOT"}`, false, ""},

		// An @mention is answered where it was said.
		{"app_mention in the room", `{"type":"app_mention","user":"U1","text":"<@UBOT> hi","channel":"C1","ts":"3.3"}`, true, ""},
		{"app_mention in a thread", `{"type":"app_mention","user":"U1","text":"<@UBOT> hi","channel":"C1","ts":"3.3","thread_ts":"1.1"}`, true, "1.1"},
		{"message naming the bot mid-sentence", `{"type":"message","channel_type":"channel","user":"U1","text":"yo <@UBOT|hanzo> can you help","channel":"C1","ts":"3.3"}`, true, ""},

		// A thread the agent answered in, or was summoned into, is the agent's —
		// including a reply that carries a file or is also sent to the channel.
		{"reply under the agent's answer", `{"type":"message","channel_type":"channel","user":"U1","text":"and then?","channel":"C1","ts":"4.4","thread_ts":"4.0","parent_user_id":"UBOT"}`, true, "4.0"},
		{"reply in a thread it was summoned into", `{"type":"message","channel_type":"channel","user":"U1","text":"and then?","channel":"C1","ts":"9.10","thread_ts":"9.9","parent_user_id":"U2"}`, true, "9.9"},
		{"a file in its thread", `{"type":"message","subtype":"file_share","channel_type":"channel","user":"U1","text":"this one","channel":"C1","ts":"9.11","thread_ts":"9.9"}`, true, "9.9"},
		{"a reply also sent to the channel", `{"type":"message","subtype":"thread_broadcast","channel_type":"channel","user":"U1","text":"and this","channel":"C1","ts":"9.12","thread_ts":"9.9"}`, true, "9.9"},

		// The loop guard, at the router. Both spellings Slack uses for "this is a
		// bot talking" — the bot_id field and the bot_message subtype.
		{"own post by bot_id", `{"type":"message","channel_type":"channel","bot_id":"B1","text":"echo","channel":"C1","ts":"1.1"}`, false, ""},
		{"own post by subtype", `{"type":"message","channel_type":"channel","subtype":"bot_message","user":"U1","text":"echo","channel":"C1","ts":"1.1"}`, false, ""},

		// Housekeeping is not conversation.
		{"edit", `{"type":"message","channel_type":"im","subtype":"message_changed","user":"U1","text":"hi","channel":"D1","ts":"1.1"}`, false, ""},
		{"delete", `{"type":"message","channel_type":"im","subtype":"message_deleted","user":"U1","text":"hi","channel":"D1","ts":"1.1"}`, false, ""},
		{"join notice", `{"type":"message","channel_type":"channel","subtype":"channel_join","user":"U1","text":"<@UBOT> joined","channel":"C1","ts":"1.1"}`, false, ""},
		{"empty text", `{"type":"message","channel_type":"im","user":"U1","text":"","channel":"D1","ts":"1.1"}`, false, ""},
		{"unknown surface", `{"type":"message","channel_type":"nowhere","user":"U1","text":"hi","channel":"X1","ts":"1.1"}`, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := routeSlackEvent([]byte(`{` + team + `"event":` + c.event + `}`))
			heard := false
			if d.Kind == slackRouteAgent {
				var err error
				heard, err = d.addressed(d.Bot, func() (bool, error) { return joined[d.Channel+":"+d.ThreadTS], nil })
				if err != nil {
					t.Fatal(err)
				}
			}
			if heard != c.heard {
				t.Fatalf("heard = %v, want %v (%+v)", heard, c.heard, d)
			}
			if heard && d.ThreadTS != c.wantThre {
				t.Fatalf("reply thread_ts = %q, want %q", d.ThreadTS, c.wantThre)
			}
		})
	}
}

// A thread record that cannot be read is an error, never a silent "not
// addressed": the endpoint answers it with a redeliverable non-2xx.
func TestUnreadableThreadStateIsAnError(t *testing.T) {
	d := routeSlackEvent([]byte(`{"type":"event_callback","team_id":"T1","event_id":"E1","event":{"type":"message","channel_type":"channel","user":"U1","text":"and then?","channel":"C1","ts":"2.2","thread_ts":"1.1"}}`))
	if _, err := d.addressed("UBOT", func() (bool, error) { return false, errors.New("disk") }); err == nil {
		t.Fatal("an unreadable thread record must surface as an error")
	}
}

func TestSlackMentions(t *testing.T) {
	for text, want := range map[string][]string{
		"no one":                         nil,
		"<@UBOT> hi":                     {"UBOT"},
		"yo <@UBOT|hanzo> and <@U2>":     {"UBOT", "U2"},
		"<!channel> <#C1|general> <@U3>": {"U3"},
		"broken <@UBOT":                  nil,
	} {
		if got := slackMentions(text); !slices.Equal(got, want) {
			t.Errorf("slackMentions(%q) = %v, want %v", text, got, want)
		}
	}
}

// TestJoiningAChannelIsRouted proves the bot's own arrival in a room is carried
// rather than dropped into the default arm.
func TestJoiningAChannelIsRouted(t *testing.T) {
	d := routeSlackEvent([]byte(`{"type":"event_callback","team_id":"T1","event_id":"E1","event":{"type":"member_joined_channel","user":"BACME","channel":"C9"}}`))
	if d.Kind != slackRouteJoined || d.Channel != "C9" || d.User != "BACME" {
		t.Fatalf("member_joined_channel must be routed, got %+v", d)
	}
}

// TestTheBotsOwnMessageNeverRunsATurn is the loop guard at the ENDPOINT, where
// the route-level bot_id check cannot reach: a post carrying no bot_id whose
// author is THIS org's own bot user. Behaviour, not status — both requests answer
// 200 — so the assertion is what was RECORDED: a turn burns the event_id, and an
// echo must burn nothing.
func TestTheBotsOwnMessageNeverRunsATurn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		slackConfiguredEnv(t)
		authCh := make(chan string, 2)
		stubSlackBridge(t, authCh)
		app := newApp(t, newSigningKMS(t, "loop-secret"))
		if cb := connectSlack(t, app, "acme", "acmecode"); cb.Code != http.StatusFound {
			t.Fatalf("connect: %d (%s)", cb.Code, cb.Body)
		}

		// BACME is the bot user id the install recorded for this org.
		echo := `{"type":"event_callback","team_id":"TACME","event_id":"EvEcho","event":{"type":"message","channel_type":"channel","user":"BACME","text":"my own words","channel":"C1","ts":"1.1"}}`
		if res := slackPost(t, app, "/v1/provider/slack/events", "loop-secret", "application/json", echo); res.Code != http.StatusOK {
			t.Fatalf("echo ack want 200, got %d (%s)", res.Code, res.Body)
		}
		if fresh, err := mounted.State.store.MarkEvent(context.Background(), "slack", "EvEcho"); err != nil || !fresh {
			t.Fatalf("the bot's own message must record nothing (fresh=%v err=%v)", fresh, err)
		}

		// A person's @mention in the SAME room does run: the guard is the author, not
		// the surface.
		human := `{"type":"event_callback","team_id":"TACME","event_id":"EvHuman","event":{"type":"message","channel_type":"channel","user":"Uacme","text":"<@BACME> a real question","channel":"C1","ts":"2.2"}}`
		if res := slackPost(t, app, "/v1/provider/slack/events", "loop-secret", "application/json", human); res.Code != http.StatusOK {
			t.Fatalf("human ack want 200, got %d (%s)", res.Code, res.Body)
		}
		if fresh, err := mounted.State.store.MarkEvent(context.Background(), "slack", "TACME:C1:2.2"); err != nil || fresh {
			t.Fatalf("a person's @mention in a channel must run a turn (fresh=%v err=%v)", fresh, err)
		}
	})
}

// TestChatterInARoomRunsNothing is the endpoint half of the addressed rule: a
// message in a room that names no one records nothing and runs no turn, and a
// reply in the thread an @mention opened does run.
func TestChatterInARoomRunsNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		slackConfiguredEnv(t)
		authCh := make(chan string, 2)
		stubSlackBridge(t, authCh)
		app := newApp(t, newSigningKMS(t, "chatter-secret"))
		if cb := connectSlack(t, app, "acme", "acmecode"); cb.Code != http.StatusFound {
			t.Fatalf("connect: %d (%s)", cb.Code, cb.Body)
		}
		post := func(event string) {
			t.Helper()
			body := `{"type":"event_callback","team_id":"TACME","event_id":"Ev","event":` + event + `}`
			if res := slackPost(t, app, "/v1/provider/slack/events", "chatter-secret", "application/json", body); res.Code != http.StatusOK {
				t.Fatalf("ack want 200, got %d (%s)", res.Code, res.Body)
			}
		}
		ran := func(key string) bool {
			t.Helper()
			fresh, err := mounted.State.store.MarkEvent(context.Background(), "slack", key)
			if err != nil {
				t.Fatal(err)
			}
			return !fresh
		}

		post(`{"type":"message","channel_type":"channel","user":"Uacme","text":"Hanzo are you still speaking out of turn?","channel":"C1","ts":"1.1"}`)
		if ran("TACME:C1:1.1") {
			t.Fatal("a message that only spells the app's name ran a turn")
		}

		// The summons, then a reply in the thread it roots, with no second mention.
		post(`{"type":"app_mention","user":"Uacme","text":"<@BACME> look at this","channel":"C1","ts":"2.2"}`)
		post(`{"type":"message","channel_type":"channel","user":"Uacme","text":"and this","channel":"C1","ts":"2.3","thread_ts":"2.2","parent_user_id":"Uacme"}`)
		if !ran("TACME:C1:2.2") || !ran("TACME:C1:2.3") {
			t.Fatal("an @mention and a reply in the thread it opened must each run a turn")
		}
		// The same thread in another channel is not the bot's.
		post(`{"type":"message","channel_type":"channel","user":"Uacme","text":"and this","channel":"C2","ts":"2.4","thread_ts":"2.2","parent_user_id":"Uacme"}`)
		if ran("TACME:C2:2.4") {
			t.Fatal("a thread in another channel ran a turn")
		}
	})
}

// TestTheAgentFollowsTheThreadsItAnswersIn: a turn's answer, posted through the
// chat send op, makes the thread under it the agent's, so a reply there runs a
// turn with no @mention. A notice the bot posts outside a turn does not.
func TestTheAgentFollowsTheThreadsItAnswersIn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		slackConfiguredEnv(t)
		var seq atomic.Int64
		mux := http.NewServeMux()
		mux.HandleFunc("/oauth.v2.access", func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "access_token": "xoxb-acme", "scope": "chat:write", "bot_user_id": "BACME",
				"team": map[string]string{"id": "TACME", "name": "TACME"},
			})
		})
		mux.HandleFunc("/chat.postMessage", func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": fmt.Sprintf("1800000000.%06d", seq.Add(1))})
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		orig := slackWebAPIBase
		slackWebAPIBase = srv.URL
		t.Cleanup(func() { slackWebAPIBase = orig })

		app := newApp(t, newSigningKMS(t, "follow-secret"))
		if cb := connectSlack(t, app, "acme", "acmecode"); cb.Code != http.StatusFound {
			t.Fatalf("connect: %d (%s)", cb.Code, cb.Body)
		}
		ctx := context.Background()

		answer, err := planeChatSend(ctx, &client.ChatSendIn{Org: "acme", Provider: "slack", Channel: "C1", Text: "the answer"})
		if err != nil || answer.MessageID == "" {
			t.Fatalf("answer not posted: %v %+v", err, answer)
		}
		notice, err := SendSlackAt(ctx, "acme", "C1", "", "", "deploy failed", client.Controls{}, client.Question{})
		if err != nil {
			t.Fatal(err)
		}
		if in, _ := mounted.State.store.InThread(ctx, "TACME", "C1", answer.MessageID); !in {
			t.Fatal("the thread under the agent's answer is not followed")
		}
		if in, _ := mounted.State.store.InThread(ctx, "TACME", "C1", notice); in {
			t.Fatal("the thread under a notice is followed")
		}

		reply := func(ts, thread string) bool {
			t.Helper()
			body := `{"type":"event_callback","team_id":"TACME","event_id":"Ev` + ts + `","event":{"type":"message","channel_type":"channel","user":"Uacme","text":"and then?","channel":"C1","ts":"` + ts + `","thread_ts":"` + thread + `","parent_user_id":"BACME"}}`
			if res := slackPost(t, app, "/v1/provider/slack/events", "follow-secret", "application/json", body); res.Code != http.StatusOK {
				t.Fatalf("ack want 200, got %d (%s)", res.Code, res.Body)
			}
			fresh, err := mounted.State.store.MarkEvent(ctx, "slack", "TACME:C1:"+ts)
			if err != nil {
				t.Fatal(err)
			}
			return !fresh
		}
		if !reply("1800000001.000001", answer.MessageID) {
			t.Fatal("a reply under the agent's answer ran no turn")
		}
		if reply("1800000001.000002", notice) {
			t.Fatal("a reply under a notice ran a turn")
		}
	})
}

// slackRoomPublic asks Slack once per room, answers "public" only for a public
// channel, and answers false whenever Slack cannot say.
func TestRoomVisibilityIsAskedOnce(t *testing.T) {
	slackConfiguredEnv(t)
	var asked atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth.v2.access", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "access_token": "xoxb-acme", "scope": "chat:write", "bot_user_id": "BACME",
			"team": map[string]string{"id": "TACME", "name": "TACME"},
		})
	})
	mux.HandleFunc("/conversations.info", func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		_ = r.ParseForm()
		switch r.FormValue("channel") {
		case "CPUB":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": map[string]any{"is_channel": true, "is_private": false}})
		case "CPRIV":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": map[string]any{"is_channel": true, "is_private": true}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "missing_scope"})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	orig := slackWebAPIBase
	slackWebAPIBase = srv.URL
	t.Cleanup(func() { slackWebAPIBase = orig })
	app := newApp(t, newSigningKMS(t, "room-secret"))
	if cb := connectSlack(t, app, "acme", "acmecode"); cb.Code != http.StatusFound {
		t.Fatalf("connect: %d (%s)", cb.Code, cb.Body)
	}
	ctx := context.Background()
	if !slackRoomPublic(ctx, "acme", "TACME", "CPUB") || !slackRoomPublic(ctx, "acme", "TACME", "CPUB") {
		t.Fatal("a public channel must read as public")
	}
	if slackRoomPublic(ctx, "acme", "TACME", "CPRIV") {
		t.Fatal("a private channel must not read as public")
	}
	if slackRoomPublic(ctx, "acme", "TACME", "GUNKNOWN") {
		t.Fatal("a room Slack will not describe must not read as public")
	}
	if n := asked.Load(); n != 3 {
		t.Fatalf("Slack was asked %d times for three rooms, want 3", n)
	}
}

// TestRetryIsDedupedOnTheMessage proves Slack's own retry (X-Slack-Retry-Num,
// same body) and the second event Slack sends for one @mention cost one turn and
// not two. There is no retry-header branch on purpose: the header is Slack SAYING
// it is a retry, and the message is the fact.
func TestRetryIsDedupedOnTheMessage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		slackConfiguredEnv(t)
		authCh := make(chan string, 2)
		stubSlackBridge(t, authCh)
		app := newApp(t, newSigningKMS(t, "retry-secret"))
		if cb := connectSlack(t, app, "acme", "acmecode"); cb.Code != http.StatusFound {
			t.Fatalf("connect: %d (%s)", cb.Code, cb.Body)
		}
		mention := `{"type":"event_callback","team_id":"TACME","event_id":"EvMention","event":{"type":"app_mention","user":"Uacme","text":"<@BACME> hello","channel":"C1","ts":"1.1"}}`
		message := `{"type":"event_callback","team_id":"TACME","event_id":"EvMessage","event":{"type":"message","channel_type":"channel","user":"Uacme","text":"<@BACME> hello","channel":"C1","ts":"1.1"}}`
		for _, delivery := range []struct{ name, body string }{{"app_mention", mention}, {"Slack's retry", mention}, {"message event", message}} {
			if res := slackPost(t, app, "/v1/provider/slack/events", "retry-secret", "application/json", delivery.body); res.Code != http.StatusOK {
				t.Fatalf("%s delivery want 200, got %d (%s)", delivery.name, res.Code, res.Body)
			}
		}
		// One row, whatever the number of deliveries: marking it again is not fresh.
		if fresh, err := mounted.State.store.MarkEvent(context.Background(), "slack", "TACME:C1:1.1"); err != nil || fresh {
			t.Fatalf("one message must be recorded exactly once (fresh=%v err=%v)", fresh, err)
		}
	})
}

// TestManifestDeclaresWhatTheRouterAnswers is the check that keeps the app
// declaration and the code from drifting apart. Subscribing to an event nothing
// answers is a promise the workspace cannot see us break, and the failure of the
// reverse — a surface the code handles that the manifest never asks Slack for —
// is total silence on that surface.
func TestManifestDeclaresWhatTheRouterAnswers(t *testing.T) {
	// A payload per subscribed event. A new subscription with no sample here fails
	// the test, which is the point: it forces the decision to be made in code.
	sample := map[string]string{
		"app_home_opened":                  `{"type":"app_home_opened","user":"U1","tab":"home"}`,
		"app_mention":                      `{"type":"app_mention","user":"U1","text":"<@B1> hi","channel":"C1","ts":"1.1"}`,
		"app_uninstalled":                  `{"type":"app_uninstalled"}`,
		"tokens_revoked":                   `{"type":"tokens_revoked","tokens":{"bot":["B1"]}}`,
		"assistant_thread_started":         `{"type":"assistant_thread_started"}`,
		"assistant_thread_context_changed": `{"type":"assistant_thread_context_changed"}`,
		"member_joined_channel":            `{"type":"member_joined_channel","user":"B1","channel":"C1"}`,
		"message.channels":                 `{"type":"message","channel_type":"channel","user":"U1","text":"hi","channel":"C1","ts":"1.1"}`,
		"message.groups":                   `{"type":"message","channel_type":"group","user":"U1","text":"hi","channel":"G1","ts":"1.1"}`,
		"message.im":                       `{"type":"message","channel_type":"im","user":"U1","text":"hi","channel":"D1","ts":"1.1"}`,
		"message.mpim":                     `{"type":"message","channel_type":"mpim","user":"U1","text":"hi","channel":"G2","ts":"1.1"}`,
	}
	events := slackManifest.Settings.Events.Bot
	if len(events) == 0 {
		t.Fatal("the embedded manifest parsed to no bot events at all")
	}
	for _, name := range events {
		ev, ok := sample[name]
		if !ok {
			t.Errorf("the manifest subscribes to %q and no test payload says what the router does with it", name)
			continue
		}
		d := routeSlackEvent([]byte(`{"type":"event_callback","team_id":"T1","event_id":"E1","event":` + ev + `}`))
		if d.Kind == slackRouteIgnore {
			t.Errorf("the manifest subscribes to %q and the router ignores it", name)
		}
	}
	// The four conversation surfaces must all be subscribed, or the agent is deaf
	// on the ones that are not.
	for _, name := range []string{"message.channels", "message.groups", "message.im", "message.mpim", "app_mention"} {
		if !slices.Contains(events, name) {
			t.Errorf("the manifest must subscribe to %q", name)
		}
	}
	// And the scopes that make each of them readable, plus the ones that let the
	// agent walk in and speak.
	for _, scope := range []string{
		"app_mentions:read", "channels:history", "channels:join", "channels:read",
		"chat:write", "groups:history", "groups:read", "im:history", "im:read",
		"im:write", "mpim:history", "mpim:read", "users:read",
	} {
		if !slices.Contains(slackDefaultScopes, scope) {
			t.Errorf("the manifest must request the %q scope", scope)
		}
	}
	// The embed is the ONE home: the consent URL asks for exactly what it declares.
	var raw struct {
		OAuth struct {
			Scopes struct {
				Bot []string `json:"bot"`
			} `json:"scopes"`
		} `json:"oauth_config"`
	}
	if err := json.Unmarshal(slackManifestJSON, &raw); err != nil {
		t.Fatalf("the embedded manifest is not valid JSON: %v", err)
	}
	if !slices.Equal(raw.OAuth.Scopes.Bot, slackDefaultScopes) {
		t.Fatalf("consent scopes = %v, manifest declares %v", slackDefaultScopes, raw.OAuth.Scopes.Bot)
	}
}
