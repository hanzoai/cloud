package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
)

// slack_rooms_test.go — which rooms the agent hears, and where it answers.
//
// The bar is membership: Slack delivers message.channels only for channels the
// app has JOINED, so every room it is in is a room it must hear. It heard one of
// the four (DMs) and acked the rest, which made @hanzo silent in every channel it
// had been invited to unless someone spelled its name out.

// TestEveryRoomTheBotIsInIsHeard drives the pure router over every surface Slack
// can deliver, and asserts BOTH halves of the decision: whether a turn runs at
// all, and where the answer goes.
func TestEveryRoomTheBotIsInIsHeard(t *testing.T) {
	const team = `"type":"event_callback","team_id":"T1","event_id":"E1",`
	cases := []struct {
		name     string
		event    string
		want     slackRouteKind
		wantThre string // the reply's thread_ts: "" means answer in the room
	}{
		{"public channel", `{"type":"message","channel_type":"channel","user":"U1","text":"hi","channel":"C1","ts":"1.1"}`,
			slackRouteAgent, ""},
		{"private channel", `{"type":"message","channel_type":"group","user":"U1","text":"hi","channel":"G1","ts":"1.1"}`,
			slackRouteAgent, ""},
		{"group dm", `{"type":"message","channel_type":"mpim","user":"U1","text":"hi","channel":"G2","ts":"1.1"}`,
			slackRouteAgent, ""},
		{"dm", `{"type":"message","channel_type":"im","user":"U1","text":"hi","channel":"D1","ts":"1.1"}`,
			slackRouteAgent, ""},

		// In a thread, the answer joins the thread — on every surface, not just DMs.
		{"threaded channel", `{"type":"message","channel_type":"channel","user":"U1","text":"hi","channel":"C1","ts":"2.2","thread_ts":"1.1"}`,
			slackRouteAgent, "1.1"},
		{"threaded dm", `{"type":"message","channel_type":"im","user":"U1","text":"hi","channel":"D1","ts":"2.2","thread_ts":"1.1"}`,
			slackRouteAgent, "1.1"},

		// A summons threads under itself even when the message is top-level: it is
		// not a turn in the room's conversation, so it leaves the room as it found it.
		{"mention threads under itself", `{"type":"app_mention","user":"U1","text":"<@B1> hi","channel":"C1","ts":"3.3"}`,
			slackRouteAgent, "3.3"},

		// The loop guard, at the router. Both spellings Slack uses for "this is a
		// bot talking" — the bot_id field and the bot_message subtype.
		{"own post by bot_id", `{"type":"message","channel_type":"channel","bot_id":"B1","text":"echo","channel":"C1","ts":"1.1"}`,
			slackRouteAck, ""},
		{"own post by subtype", `{"type":"message","channel_type":"channel","subtype":"bot_message","user":"U1","text":"echo","channel":"C1","ts":"1.1"}`,
			slackRouteAck, ""},

		// Housekeeping is not conversation.
		{"edit", `{"type":"message","channel_type":"channel","subtype":"message_changed","user":"U1","text":"hi","channel":"C1","ts":"1.1"}`,
			slackRouteAck, ""},
		{"delete", `{"type":"message","channel_type":"channel","subtype":"message_deleted","user":"U1","text":"hi","channel":"C1","ts":"1.1"}`,
			slackRouteAck, ""},
		{"join notice", `{"type":"message","channel_type":"channel","subtype":"channel_join","user":"U1","text":"joined","channel":"C1","ts":"1.1"}`,
			slackRouteAck, ""},
		{"empty text", `{"type":"message","channel_type":"channel","user":"U1","text":"","channel":"C1","ts":"1.1"}`,
			slackRouteAck, ""},
		{"unknown surface", `{"type":"message","channel_type":"nowhere","user":"U1","text":"hi","channel":"X1","ts":"1.1"}`,
			slackRouteAck, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := routeSlackEvent([]byte(`{` + team + `"event":` + c.event + `}`))
			if d.Kind != c.want {
				t.Fatalf("kind = %v, want %v (%+v)", d.Kind, c.want, d)
			}
			if d.Kind == slackRouteAgent && d.ThreadTS != c.wantThre {
				t.Fatalf("reply thread_ts = %q, want %q", d.ThreadTS, c.wantThre)
			}
		})
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
	slackConfiguredEnv(t)
	authCh := make(chan string, 2)
	stubSlackBridge(t, authCh)
	app := newApp(t, newSigningKMS(t, "loop-secret"))
	if cb := connectSlack(t, app, "acme", "acmecode"); cb.Code != http.StatusFound {
		t.Fatalf("connect: %d (%s)", cb.Code, cb.Body)
	}

	// BACME is the bot user id the install recorded for this org.
	echo := `{"type":"event_callback","team_id":"TACME","event_id":"EvEcho","event":{"type":"message","channel_type":"channel","user":"BACME","text":"my own words","channel":"C1","ts":"1.1"}}`
	if res := slackPost(t, app, "/v1/integration/slack/events", "loop-secret", "application/json", echo); res.Code != http.StatusOK {
		t.Fatalf("echo ack want 200, got %d (%s)", res.Code, res.Body)
	}
	if fresh, err := mounted.State.store.MarkEvent(context.Background(), "slack", "EvEcho"); err != nil || !fresh {
		t.Fatalf("the bot's own message must record nothing (fresh=%v err=%v)", fresh, err)
	}

	// A person's message in the SAME room does run: the guard is the author, not
	// the surface.
	human := `{"type":"event_callback","team_id":"TACME","event_id":"EvHuman","event":{"type":"message","channel_type":"channel","user":"Uacme","text":"a real question","channel":"C1","ts":"2.2"}}`
	if res := slackPost(t, app, "/v1/integration/slack/events", "loop-secret", "application/json", human); res.Code != http.StatusOK {
		t.Fatalf("human ack want 200, got %d (%s)", res.Code, res.Body)
	}
	if fresh, err := mounted.State.store.MarkEvent(context.Background(), "slack", "EvHuman"); err != nil || fresh {
		t.Fatalf("a person's message in a channel must run a turn (fresh=%v err=%v)", fresh, err)
	}
}

// TestRetryIsDedupedOnEventID proves Slack's own retry (X-Slack-Retry-Num, same
// event_id, same body) costs one turn and not two. There is no retry-header
// branch on purpose: the header is Slack SAYING it is a retry, and the event_id
// is the fact — a duplicate delivered without the header dedupes identically.
func TestRetryIsDedupedOnEventID(t *testing.T) {
	slackConfiguredEnv(t)
	authCh := make(chan string, 2)
	stubSlackBridge(t, authCh)
	app := newApp(t, newSigningKMS(t, "retry-secret"))
	if cb := connectSlack(t, app, "acme", "acmecode"); cb.Code != http.StatusFound {
		t.Fatalf("connect: %d (%s)", cb.Code, cb.Body)
	}
	body := `{"type":"event_callback","team_id":"TACME","event_id":"EvRetry","event":{"type":"message","channel_type":"channel","user":"Uacme","text":"hello","channel":"C1","ts":"1.1"}}`
	for _, delivery := range []string{"first", "Slack's retry"} {
		if res := slackPost(t, app, "/v1/integration/slack/events", "retry-secret", "application/json", body); res.Code != http.StatusOK {
			t.Fatalf("%s delivery want 200, got %d (%s)", delivery, res.Code, res.Body)
		}
	}
	// One row, whatever the number of deliveries: marking it again is not fresh.
	if fresh, err := mounted.State.store.MarkEvent(context.Background(), "slack", "EvRetry"); err != nil || fresh {
		t.Fatalf("a retried event must be recorded exactly once (fresh=%v err=%v)", fresh, err)
	}
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
