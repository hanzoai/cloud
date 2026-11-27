package provider

// slack_controls_test.go — a run's message carries native buttons while it works
// and none once it is over, and a press is carried as the Slack user who made it.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// chatCall is one chat.* call a test's Slack received.
type chatCall struct {
	method string
	body   map[string]any
}

// slackRecorder is a Slack Web API that binds acme's workspace (TACME) and
// records every chat.* call.
type slackRecorder struct {
	mu    sync.Mutex
	calls []chatCall
}

func recordSlack(t *testing.T) *slackRecorder {
	t.Helper()
	rec := &slackRecorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth.v2.access", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "access_token": "xoxb-acme", "scope": "chat:write",
			"bot_user_id": "BACME", "team": map[string]string{"id": "TACME", "name": "acme"}})
	})
	for _, m := range []string{"/chat.postMessage", "/chat.update", "/chat.postEphemeral"} {
		mux.HandleFunc(m, func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			rec.mu.Lock()
			rec.calls = append(rec.calls, chatCall{strings.TrimPrefix(r.URL.Path, "/"), body})
			rec.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "111.2"})
		})
	}
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	orig := slackWebAPIBase
	slackWebAPIBase = ts.URL
	t.Cleanup(func() { slackWebAPIBase = orig })
	return rec
}

func (r *slackRecorder) last(method string) (map[string]any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.calls) - 1; i >= 0; i-- {
		if r.calls[i].method == method {
			return r.calls[i].body, true
		}
	}
	return nil, false
}

// actions is the action ids of a message's buttons, in order, and its Stop.
func actions(t *testing.T, body map[string]any) ([]string, map[string]any) {
	t.Helper()
	blocks, _ := body["blocks"].([]any)
	for _, b := range blocks {
		blk := b.(map[string]any)
		if blk["type"] != "actions" {
			continue
		}
		var ids []string
		var stop map[string]any
		for _, e := range blk["elements"].([]any) {
			el := e.(map[string]any)
			ids = append(ids, el["action_id"].(string))
			if el["action_id"] == actStop {
				stop = el
			}
		}
		return ids, stop
	}
	return nil, nil
}

// While a run works its message is its text with Stop (danger, confirmed) and
// Pause under it; paused, Continue and Stop; an edit that ends it takes the
// buttons away. The plain text rides beside the blocks every time.
func TestARunsMessageCarriesNativeControls(t *testing.T) {
	slackConfiguredEnv(t)
	rec := recordSlack(t)
	app := newApp(t, newKMS(t))
	connectSlack(t, app, "acme", "acmecode")
	ctx := cloud.For(context.Background(), "acme")
	ctl := client.Controls{Session: "sess_1", Kind: client.ControlsCoding, Owner: "UOWNER"}

	if _, err := planeSlackSend(ctx, &client.SlackSendIn{Channel: "C1", Thread: "111.1", Text: "Working…", Controls: ctl}); err != nil {
		t.Fatal(err)
	}
	body, _ := rec.last("chat.postMessage")
	ids, stop := actions(t, body)
	if strings.Join(ids, ",") != actStop+","+actPause || body["text"] != "Working…" {
		t.Fatalf("a working run shows %v with text %q", ids, body["text"])
	}
	confirm, _ := stop["confirm"].(map[string]any)
	if stop["style"] != "danger" || confirm["title"].(map[string]any)["text"] != "Stop this run?" ||
		confirm["text"].(map[string]any)["text"] != "Work so far is kept on its branch." {
		t.Fatalf("Stop must be danger and confirmed: %+v", stop)
	}
	if got, ok := parseControlValue(stop["value"].(string)); !ok || got != ctl {
		t.Fatalf("the button carries %+v, want %+v", got, ctl)
	}

	paused := ctl
	paused.Paused = true
	if _, err := planeSlackSend(ctx, &client.SlackSendIn{Channel: "C1", Text: "Paused.", Update: "111.2", Controls: paused}); err != nil {
		t.Fatal(err)
	}
	body, _ = rec.last("chat.update")
	if ids, _ := actions(t, body); strings.Join(ids, ",") != actContinue+","+actStop {
		t.Fatalf("a paused run shows %v", ids)
	}

	if _, err := planeSlackSend(ctx, &client.SlackSendIn{Channel: "C1", Text: "Pushed.", Update: "111.2"}); err != nil {
		t.Fatal(err)
	}
	body, _ = rec.last("chat.update")
	if blocks, ok := body["blocks"].([]any); !ok || len(blocks) != 0 || body["text"] != "Pushed." {
		t.Fatalf("an ended run must take its buttons away and keep its text: %+v", body)
	}
}

// A press reaches channels as the SLACK USER who pressed it, in the org the
// signed team resolves to, never the payload's; and a press channels refuses is
// answered privately to the presser, naming whose run it is — never in the room.
func TestAPressIsCarriedAsItsPresser(t *testing.T) {
	slackConfiguredEnv(t)
	rec := recordSlack(t)
	app := newApp(t, newSigningKMS(t, "press-secret"))
	connectSlack(t, app, "acme", "acmecode")

	got := make(chan client.TransportActIn, 1)
	prev := actOn
	actOn = func(ctx context.Context, in *client.TransportActIn) (*client.SessionControlled, error) {
		if org := cloud.Who(ctx).Org; org != "acme" {
			t.Errorf("the press was carried for org %q, want acme", org)
		}
		got <- *in
		return nil, zip.ErrForbidden("only the person whose run this is can steer it")
	}
	t.Cleanup(func() { actOn = prev })

	ctl := client.Controls{Session: "sess_1", Kind: client.ControlsTurn, Owner: "UOWNER"}
	payload, _ := json.Marshal(map[string]any{
		"type": "block_actions", "team": map[string]string{"id": "TACME"}, "user": map[string]string{"id": "UOTHER"},
		"actions":   []map[string]string{{"action_id": actStop, "value": controlValue(ctl)}},
		"container": map[string]string{"type": "message", "message_ts": "111.2", "channel_id": "C1"},
		"channel":   map[string]string{"id": "C1"},
		"message":   map[string]string{"ts": "111.2", "thread_ts": "111.1"},
	})
	res := slackPost(t, app, "/v1/provider/slack/events", "press-secret", "application/x-www-form-urlencoded",
		"payload="+url.QueryEscape(string(payload)))
	if res.Code != http.StatusOK {
		t.Fatalf("a press must be acked at once, got %d", res.Code)
	}
	select {
	case in := <-got:
		want := client.TransportActIn{Provider: "slack", Account: "tacme", Channel: "C1", Thread: "111.1",
			Message: "111.2", User: "UOTHER", Command: client.SessionStop, Controls: ctl}
		if in != want {
			t.Fatalf("carried %+v, want %+v", in, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the press never reached channels")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if body, ok := rec.last("chat.postEphemeral"); ok {
			if body["user"] != "UOTHER" || body["text"] != "Only <@UOWNER> can stop this run." || body["thread_ts"] != "111.1" {
				t.Fatalf("the refusal must go privately to the presser, naming the owner: %+v", body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the presser was never told")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := rec.last("chat.postMessage"); ok {
		t.Fatal("a press posted a message into the room")
	}
}

// A question is its text, an answer per option — buttons up to five, one select
// past that — and a line saying a reply in words answers it too; the model's text
// is escaped before it meets mrkdwn. Answered, the same message keeps its text
// and loses its options.
func TestAQuestionIsDrawnWithAnAnswerPerOption(t *testing.T) {
	slackConfiguredEnv(t)
	rec := recordSlack(t)
	app := newApp(t, newKMS(t))
	connectSlack(t, app, "acme", "acmecode")
	opts := func(n int) []client.Option {
		out := make([]client.Option, n)
		for i := range out {
			out[i] = client.Option{Label: string(rune('A' + i)), Description: "choice " + string(rune('A'+i))}
		}
		return out
	}
	ask := func(n int) []any {
		q := client.Question{ID: "qst_1", Text: "Which <!channel>?", Options: opts(n), Owner: "UOWNER"}
		if _, err := planeChatSend(context.Background(), &client.ChatSendIn{Org: "acme", Provider: "slack", Channel: "C1",
			ReplyTo: "111.1", Text: "Which?\n1. A", Question: q}); err != nil {
			t.Fatal(err)
		}
		body, _ := rec.last("chat.postMessage")
		blocks, _ := body["blocks"].([]any)
		if len(blocks) != 3 {
			t.Fatalf("%d options drew %d blocks: %+v", n, len(blocks), body)
		}
		return blocks
	}

	blocks := ask(5)
	section := blocks[0].(map[string]any)["text"].(map[string]any)["text"].(string)
	if strings.Contains(section, "<!channel>") || !strings.Contains(section, "&lt;!channel&gt;") || !strings.Contains(section, "choice E") {
		t.Fatalf("the question must be escaped and carry the descriptions: %q", section)
	}
	els := blocks[1].(map[string]any)["elements"].([]any)
	if len(els) != 5 || els[4].(map[string]any)["type"] != "button" || els[4].(map[string]any)["action_id"] != "hanzo_answer_4" {
		t.Fatalf("five options must be five buttons: %+v", els)
	}
	if q, owner, ok := parseQuestionValue(els[0].(map[string]any)["value"].(string)); !ok || q != "qst_1" || owner != "UOWNER" {
		t.Fatalf("a button must carry its question and whose it is: %+v", els[0])
	}
	if ctx := blocks[2].(map[string]any)["elements"].([]any)[0].(map[string]any)["text"]; ctx != "Or reply in this thread in your own words." {
		t.Fatalf("the reply line reads %q", ctx)
	}

	els = ask(6)[1].(map[string]any)["elements"].([]any)
	sel := els[0].(map[string]any)
	if len(els) != 1 || sel["type"] != "static_select" || sel["action_id"] != actAnswer || len(sel["options"].([]any)) != 6 {
		t.Fatalf("six options must be one select: %+v", els)
	}

	if _, err := planeChatSend(context.Background(), &client.ChatSendIn{Org: "acme", Provider: "slack", Channel: "C1",
		Edit: "111.2", Text: "✓ A — chosen by <@UOWNER>"}); err != nil {
		t.Fatal(err)
	}
	body, _ := rec.last("chat.update")
	if b, ok := body["blocks"].([]any); !ok || len(b) != 0 || body["text"] != "✓ A — chosen by <@UOWNER>" {
		t.Fatalf("the answered question must keep its text and lose its options: %+v", body)
	}
}

// A pressed option is carried as the label the presser saw, on the question the
// button names, as the Slack user who pressed; a refused press is told privately
// whose question it is.
func TestAPressedOptionIsCarriedAsItsPresser(t *testing.T) {
	slackConfiguredEnv(t)
	rec := recordSlack(t)
	app := newApp(t, newSigningKMS(t, "press-secret"))
	connectSlack(t, app, "acme", "acmecode")
	got := make(chan client.TransportActIn, 2)
	prev := actOn
	actOn = func(_ context.Context, in *client.TransportActIn) (*client.SessionControlled, error) {
		got <- *in
		return nil, zip.ErrForbidden("only the person this question is for can answer it")
	}
	t.Cleanup(func() { actOn = prev })

	value := `{"q":"qst_1","o":"UOWNER","i":1}`
	for _, a := range []map[string]any{
		{"action_id": "hanzo_answer_1", "value": value, "text": map[string]string{"type": "plain_text", "text": "Postgres"}},
		{"action_id": actAnswer, "selected_option": map[string]any{"value": value, "text": map[string]string{"text": "Postgres"}}},
	} {
		payload, _ := json.Marshal(map[string]any{
			"type": "block_actions", "team": map[string]string{"id": "TACME"}, "user": map[string]string{"id": "UOTHER"},
			"actions":   []map[string]any{a},
			"container": map[string]string{"type": "message", "message_ts": "111.2", "channel_id": "C1"},
			"message":   map[string]string{"ts": "111.2", "thread_ts": "111.1"},
		})
		if res := slackPost(t, app, "/v1/provider/slack/events", "press-secret", "application/x-www-form-urlencoded",
			"payload="+url.QueryEscape(string(payload))); res.Code != http.StatusOK {
			t.Fatalf("a press must be acked at once, got %d", res.Code)
		}
		select {
		case in := <-got:
			want := client.TransportActIn{Provider: "slack", Account: "tacme", Channel: "C1", Thread: "111.1", Message: "111.2",
				User: "UOTHER", Command: client.SessionAnswer, Question: "qst_1", Answer: "Postgres", Controls: client.Controls{Owner: "UOWNER"}}
			if in != want {
				t.Fatalf("carried %+v, want %+v", in, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the press never reached channels")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if body, ok := rec.last("chat.postEphemeral"); ok {
			if body["user"] != "UOTHER" || body["text"] != "Only <@UOWNER> can answer this question." {
				t.Fatalf("the refusal must go privately to the presser, naming whose question it is: %+v", body)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the presser was never told")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
