package claw

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// These tests drive the mounted surface — the same two doors a client uses —
// and stand a fake in for the one thing that is not in this process: the
// cloud's completions client. Nothing else is faked, so what passes here is
// the transcript, the run bookkeeping and the event stream a UI reads.

// aiFake is the cloud's completions client, held still. It records what it was
// asked and answers what the test told it to; hold makes it wait for its
// context to end, which is what an abort and a timeout look like from here.
type aiFake struct {
	mu     sync.Mutex
	calls  int
	prompt string
	reply  string
	err    error
	hold   bool
	org    string
	bill   string
}

// charged is the data scope and the ledger the last turn named.
func (f *aiFake) charged() (org, bill string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.org, f.bill
}

func (f *aiFake) ChatCompletion(ctx context.Context, req *cloud.ChatRequest) (*cloud.ChatResponse, error) {
	f.mu.Lock()
	f.calls++
	f.prompt = req.Prompt
	f.org, f.bill = req.Org, req.BillingOrg
	hold, reply, err := f.hold, f.reply, f.err
	f.mu.Unlock()
	if hold {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return &cloud.ChatResponse{Content: reply, PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}, nil
}

func (f *aiFake) Embed(context.Context, *cloud.EmbedRequest) ([][]float32, error) { return nil, nil }

func (f *aiFake) asked() (int, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.prompt
}

// aiCatalogue is the same client for one that can also enumerate what it
// serves, which is the optional capability models.list reads.
type aiCatalogue struct {
	*aiFake
	served []string
}

func (f *aiCatalogue) Models(context.Context) ([]string, error) { return f.served, nil }

// aiServes fills the deps a deployment fills: the cloud's completions client
// and the model a turn falls back to. There is no second way to bind one.
func aiServes(client cloud.AIClient, model string) func(*cloud.Deps) {
	return func(d *cloud.Deps) {
		d.AI = client
		d.AIDefaultModel = model
	}
}

// aiSend starts a turn and returns the acknowledgement. A session still
// carrying the turn before this one answers a retryable refusal, and taking
// that invitation is what a client does, so the helper does it too.
func aiSend(t *testing.T, app *zip.App, w who, key, run, message string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, frame := ask(t, app, w, "s:"+run, "chat.send",
			`{"sessionKey":"`+key+`","message":"`+message+`","idempotencyKey":"`+run+`","deliver":false}`)
		if frame["ok"] == true {
			return payload(t, frame)
		}
		e, _ := frame["error"].(map[string]any)
		if e["retryable"] != true || time.Now().After(deadline) {
			t.Fatalf("chat.send refused: %v", frame)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// aiPage reads one page of a transcript.
func aiPage(t *testing.T, app *zip.App, w who, params string) map[string]any {
	t.Helper()
	_, frame := ask(t, app, w, "h", "chat.history", params)
	if frame["ok"] != true {
		t.Fatalf("chat.history refused: %v", frame)
	}
	return payload(t, frame)
}

// aiSettled waits until a session's transcript holds n messages. A turn runs
// after its acknowledgement, so a test that reads immediately reads too early.
func aiSettled(t *testing.T, app *zip.App, w who, key string, n int) []any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		page := aiPage(t, app, w, `{"sessionKey":"`+key+`"}`)
		messages, _ := page["messages"].([]any)
		if len(messages) >= n {
			return messages
		}
		if time.Now().After(deadline) {
			t.Fatalf("the transcript holds %d messages, want %d", len(messages), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// aiSaid is one message's text.
func aiSaid(t *testing.T, message any) string {
	t.Helper()
	m, ok := message.(map[string]any)
	if !ok {
		t.Fatalf("not a message: %v", message)
	}
	content, _ := m["content"].([]any)
	var b strings.Builder
	for _, block := range content {
		if part, ok := block.(map[string]any); ok {
			text, _ := part["text"].(string)
			b.WriteString(text)
		}
	}
	return b.String()
}

// aiSeq is a message's position in its transcript, read where a client reads
// it: the __openclaw envelope.
func aiSeq(t *testing.T, message any) float64 {
	t.Helper()
	m, _ := message.(map[string]any)
	mark, ok := m["__openclaw"].(map[string]any)
	if !ok {
		t.Fatalf("message carries no identity envelope: %v", message)
	}
	seq, _ := mark["seq"].(float64)
	return seq
}

// aiChat reads frames until one is a chat event in the named state.
func aiChat(t *testing.T, ws *websocket.Conn, state string) map[string]any {
	t.Helper()
	for range 40 {
		frame := next(t, ws)
		if frame["type"] != "event" || frame["event"] != "chat" {
			continue
		}
		p, _ := frame["payload"].(map[string]any)
		if p["state"] == state {
			return p
		}
	}
	t.Fatalf("no chat event reached state %q", state)
	return nil
}

// A send starts a turn and answers before it finishes: the run is the id the
// caller minted, and both halves of the exchange land in the transcript.
func TestAiSendStartsATurnAndLandsBothMessages(t *testing.T) {
	model := &aiFake{reply: "the sky is blue because of Rayleigh scattering"}
	app := mount(t, aiServes(model, "flash"))
	me := who{org: "acme"}

	ack := aiSend(t, app, me, "why", "run-1", "why is the sky blue")
	if ack["runId"] != "run-1" {
		t.Errorf("the run is %v, want the idempotency key the client minted", ack["runId"])
	}
	if ack["status"] != "started" {
		t.Errorf("the send answered %v, want started", ack["status"])
	}
	if ack["messageSeq"] != float64(1) {
		t.Errorf("the first message is at %v, want 1", ack["messageSeq"])
	}

	messages := aiSettled(t, app, me, "why", 2)
	if len(messages) != 2 {
		t.Fatalf("the transcript holds %d messages, want the question and the answer", len(messages))
	}
	first, _ := messages[0].(map[string]any)
	last, _ := messages[1].(map[string]any)
	if first["role"] != "user" || last["role"] != "assistant" {
		t.Fatalf("the transcript reads %v then %v", first["role"], last["role"])
	}
	if got := aiSaid(t, messages[0]); got != "why is the sky blue" {
		t.Errorf("the question was stored as %q", got)
	}
	if got := aiSaid(t, messages[1]); got != model.reply {
		t.Errorf("the answer was stored as %q, want %q", got, model.reply)
	}
	if last["model"] != "flash" {
		t.Errorf("the answer names model %v, want the one the turn ran on", last["model"])
	}
	usage, _ := last["usage"].(map[string]any)
	if usage["input"] != float64(11) || usage["output"] != float64(7) {
		t.Errorf("the answer carries usage %v, want what the completion reported", usage)
	}
	if aiSeq(t, messages[0]) != 1 || aiSeq(t, messages[1]) != 2 {
		t.Errorf("the transcript is numbered %v then %v", aiSeq(t, messages[0]), aiSeq(t, messages[1]))
	}

	// The model is asked the conversation, not only the last line.
	if _, prompt := model.asked(); !strings.Contains(prompt, "why is the sky blue") {
		t.Errorf("the prompt did not carry the question: %q", prompt)
	}

	// A send to a key nobody has used opens the session through the family
	// that owns one, so the roster knows about it afterwards.
	_, frame := ask(t, app, me, "d", "sessions.describe", `{"key":"why"}`)
	if payload(t, frame)["session"] == nil {
		t.Error("a send to an unused key left no session behind")
	}
}

// The turn is carried by chat events, and both the delta and the terminal
// carry the whole message: a client that missed a frame converges from the
// snapshot instead of re-reading the transcript to find the reply.
func TestAiTurnCarriesTheMessageOnEveryEvent(t *testing.T) {
	url := serve(t, aiServes(&aiFake{reply: "hello back"}, "flash"))
	ws := dial(t, url, who{org: "acme"})
	if frame := say(t, ws, "1:a", "connect", ""); frame["ok"] != true {
		t.Fatalf("connect: %v", frame)
	}

	frame := say(t, ws, "2:a", "chat.send",
		`{"sessionKey":"talk","message":"hello","idempotencyKey":"run-9"}`)
	// The acknowledgement and the turn's first events race on one socket, so
	// the response is found among the frames rather than assumed to be first.
	for frame["type"] == "event" {
		frame = next(t, ws)
	}
	if frame["ok"] != true {
		t.Fatalf("chat.send refused: %v", frame)
	}

	delta := aiChat(t, ws, "delta")
	if delta["runId"] != "run-9" || delta["sessionKey"] != "talk" {
		t.Errorf("the delta is addressed %v/%v", delta["runId"], delta["sessionKey"])
	}
	if delta["deltaText"] != "hello back" {
		t.Errorf("the delta carries %v", delta["deltaText"])
	}
	if aiSaid(t, delta["message"]) != "hello back" {
		t.Errorf("the delta carries no full snapshot: %v", delta["message"])
	}

	final := aiChat(t, ws, "final")
	if final["message"] == nil {
		t.Fatal("the terminal event carries no message; a client would re-read chat.history hunting for the reply")
	}
	if aiSaid(t, final["message"]) != "hello back" {
		t.Errorf("the terminal snapshot is %v", final["message"])
	}
	if delta["seq"] == final["seq"] {
		t.Errorf("two events of one run share seq %v", final["seq"])
	}
}

// A send that was already accepted replays its first run. This is what makes a
// retry after a lost answer land once rather than speak twice.
func TestAiSendReplaysAnAcceptedRun(t *testing.T) {
	model := &aiFake{reply: "once"}
	app := mount(t, aiServes(model, "flash"))
	me := who{org: "acme"}

	first := aiSend(t, app, me, "s", "run-2", "hello")
	aiSettled(t, app, me, "s", 2)
	second := aiSend(t, app, me, "s", "run-2", "hello")

	if second["runId"] != first["runId"] {
		t.Errorf("the retry started run %v, not the run %v it was retrying", second["runId"], first["runId"])
	}
	if second["status"] != "ok" {
		t.Errorf("a retry of a finished run answered %v, want ok", second["status"])
	}
	if calls, _ := model.asked(); calls != 1 {
		t.Errorf("the model was asked %d times for one accepted send", calls)
	}
	if messages := aiSettled(t, app, me, "s", 2); len(messages) != 2 {
		t.Errorf("the retry wrote %d messages into the transcript", len(messages))
	}
}

// One turn at a time on a session: a second send is told to come back rather
// than allowed to interleave a second answer into one transcript.
func TestAiSendRefusesASecondTurnOnABusySession(t *testing.T) {
	app := mount(t, aiServes(&aiFake{hold: true}, "flash"))
	me := who{org: "acme"}

	aiSend(t, app, me, "busy", "run-3", "first")
	// The held turn outlives this test unless it is stopped, the way a run
	// outlives the request that started it.
	t.Cleanup(func() { _, _ = ask(t, app, me, "z", "chat.abort", `{"sessionKey":"busy"}`) })

	_, frame := ask(t, app, me, "x", "chat.send",
		`{"sessionKey":"busy","message":"second","idempotencyKey":"run-4"}`)
	if frame["ok"] != false {
		t.Fatalf("a second turn started on a busy session: %v", frame)
	}
	e := wrong(t, frame)
	if e["code"] != "UNAVAILABLE" {
		t.Errorf("the refusal code is %v, want UNAVAILABLE so the client retries", e["code"])
	}
	msg, _ := e["message"].(string)
	if !strings.Contains(msg, "busy") {
		t.Errorf("the refusal does not name the session that is occupied: %v", msg)
	}
	// The run holding the session was minted by another caller. Naming it back
	// tells this one something it did not send and does not need.
	if strings.Contains(msg, "run-3") {
		t.Errorf("the refusal echoed another caller's run id: %v", msg)
	}
	if e["retryable"] != true {
		t.Errorf("the refusal is not retryable: %v", e)
	}
}

// An abort ends the turn it names and says which one it stopped.
func TestAiAbortStopsALiveTurn(t *testing.T) {
	app := mount(t, aiServes(&aiFake{hold: true}, "flash"))
	me := who{org: "acme"}

	aiSend(t, app, me, "stop", "run-5", "take your time")
	_, frame := ask(t, app, me, "a", "chat.abort", `{"sessionKey":"stop","runId":"run-5"}`)
	if frame["ok"] != true {
		t.Fatalf("chat.abort refused: %v", frame)
	}
	out := payload(t, frame)
	if out["status"] != "aborted" || out["abortedRunId"] != "run-5" {
		t.Fatalf("the abort answered %v", out)
	}

	// The run itself unwinds and records how it ended, which a retry reads.
	deadline := time.Now().Add(10 * time.Second)
	for {
		ack := aiSend(t, app, me, "stop", "run-5", "take your time")
		if ack["status"] == "ok" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the aborted run never settled: %v", ack)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Nothing to stop the second time.
	_, frame = ask(t, app, me, "a2", "chat.abort", `{"sessionKey":"stop"}`)
	if payload(t, frame)["status"] != "no-active-run" {
		t.Errorf("a second abort answered %v", payload(t, frame))
	}
}

// A page counts back from the newest message. A handler that paged from the
// oldest would answer this with the opening of the conversation.
func TestAiHistoryPagesBackFromTheNewest(t *testing.T) {
	app := mount(t, aiServes(&aiFake{reply: "ok"}, "flash"))
	me := who{org: "acme"}

	for turn, run := range []string{"r1", "r2", "r3"} {
		aiSend(t, app, me, "long", run, "line "+run)
		aiSettled(t, app, me, "long", 2*(turn+1))
	}

	tail := aiPage(t, app, me, `{"sessionKey":"long","limit":2}`)
	if tail["totalMessages"] != float64(6) {
		t.Fatalf("the transcript is %v messages long, want 6", tail["totalMessages"])
	}
	page, _ := tail["messages"].([]any)
	if len(page) != 2 {
		t.Fatalf("a limit of 2 returned %d messages", len(page))
	}
	if aiSeq(t, page[0]) != 5 || aiSeq(t, page[1]) != 6 {
		t.Fatalf("the tail page holds messages %v and %v, want the newest two",
			aiSeq(t, page[0]), aiSeq(t, page[1]))
	}
	if tail["hasMore"] != true || tail["nextOffset"] != float64(2) {
		t.Errorf("the tail page reports hasMore=%v nextOffset=%v", tail["hasMore"], tail["nextOffset"])
	}
	if tail["completeSnapshot"] == true {
		t.Error("a partial page called itself complete")
	}

	older := aiPage(t, app, me, `{"sessionKey":"long","limit":2,"offset":2}`)
	page, _ = older["messages"].([]any)
	if len(page) != 2 || aiSeq(t, page[0]) != 3 || aiSeq(t, page[1]) != 4 {
		t.Fatalf("the page before the tail holds %v", page)
	}
	if older["nextOffset"] != float64(4) {
		t.Errorf("the second page reports nextOffset %v, want 4", older["nextOffset"])
	}

	whole := aiPage(t, app, me, `{"sessionKey":"long"}`)
	if whole["hasMore"] != false || whole["completeSnapshot"] != true {
		t.Errorf("a page holding the whole transcript reports %v", whole)
	}
}

// A key nobody has spoken to has an empty transcript, which is an answer, not
// a failure: a client opens a session before anything is said in it.
func TestAiHistoryOnAnUnusedKeyIsEmpty(t *testing.T) {
	app := mount(t)
	page := aiPage(t, app, who{org: "acme"}, `{"sessionKey":"nobody"}`)
	if messages, _ := page["messages"].([]any); len(messages) != 0 {
		t.Errorf("an unused key answered %v", messages)
	}
	if page["totalMessages"] != float64(0) || page["completeSnapshot"] != true {
		t.Errorf("an empty transcript reports %v", page)
	}

	// A cursor names a position in a stream this surface does not mint, so the
	// client is told to read a fresh tail.
	_, frame := ask(t, app, who{org: "acme"}, "c", "chat.history",
		`{"sessionKey":"nobody","cursor":"whatever"}`)
	if payload(t, frame)["kind"] != "reset" {
		t.Errorf("a cursor answered %v, want a reset", payload(t, frame))
	}
}

// One message, fetched by id, with the reasons it might not come back.
func TestAiMessageGetFindsOneAndSaysWhyItCannot(t *testing.T) {
	app := mount(t, aiServes(&aiFake{reply: "answered"}, "flash"))
	me := who{org: "acme"}

	aiSend(t, app, me, "one", "run-6", "ask")
	messages := aiSettled(t, app, me, "one", 2)
	mark, _ := messages[1].(map[string]any)["__openclaw"].(map[string]any)
	id, _ := mark["id"].(string)

	_, frame := ask(t, app, me, "g", "chat.message.get",
		`{"sessionKey":"one","messageId":"`+id+`"}`)
	out := payload(t, frame)
	if out["ok"] != true || aiSaid(t, out["message"]) != "answered" {
		t.Fatalf("the message came back as %v", out)
	}

	_, frame = ask(t, app, me, "g2", "chat.message.get",
		`{"sessionKey":"one","messageId":"msg_nothing"}`)
	out = payload(t, frame)
	if out["ok"] != false || out["unavailableReason"] != "not_found" {
		t.Errorf("an unknown id answered %v", out)
	}

	_, frame = ask(t, app, me, "g3", "chat.message.get",
		`{"sessionKey":"one","messageId":"`+id+`","maxChars":2}`)
	if payload(t, frame)["unavailableReason"] != "oversized" {
		t.Errorf("a message past the caller's budget answered %v", payload(t, frame))
	}
}

// A turn cannot be started against a gateway with no inference behind it, and
// the refusal says so rather than answering as if it had.
func TestAiSendRefusesWithNoInferenceBound(t *testing.T) {
	app := mount(t)
	_, frame := ask(t, app, who{org: "acme"}, "s", "chat.send",
		`{"sessionKey":"k","message":"hello","idempotencyKey":"run-7"}`)
	if frame["ok"] != false {
		t.Fatalf("a send was accepted with no model behind it: %v", frame)
	}
	e := wrong(t, frame)
	if e["code"] != "UNAVAILABLE" {
		t.Errorf("the refusal code is %v", e["code"])
	}
	if msg, _ := e["message"].(string); !strings.Contains(msg, "inference") {
		t.Errorf("the refusal does not say what is missing: %v", msg)
	}
}

// A send names the run it starts, and a send with nothing to say is a mistake
// the caller can correct.
func TestAiSendChecksItsParameters(t *testing.T) {
	app := mount(t, aiServes(&aiFake{reply: "x"}, "flash"))
	me := who{org: "acme"}

	for _, tc := range []struct{ name, params, want string }{
		{"no run id", `{"sessionKey":"k","message":"hi"}`, "idempotencyKey"},
		{"no session", `{"message":"hi","idempotencyKey":"r"}`, "sessionKey"},
		{"nothing said", `{"sessionKey":"k","message":"  ","idempotencyKey":"r"}`, "empty"},
		{"an attachment", `{"sessionKey":"k","message":"hi","idempotencyKey":"r","attachments":[{"type":"image"}]}`, "attachment"},
		{"an unknown queue", `{"sessionKey":"k","message":"hi","idempotencyKey":"r","queueMode":"later"}`, "queueMode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, frame := ask(t, app, me, "s", "chat.send", tc.params)
			if frame["ok"] != false {
				t.Fatalf("accepted: %v", frame)
			}
			e := wrong(t, frame)
			if e["code"] != "INVALID_REQUEST" {
				t.Errorf("code is %v, want INVALID_REQUEST", e["code"])
			}
			if msg, _ := e["message"].(string); !strings.Contains(msg, tc.want) {
				t.Errorf("the refusal reads %q, want it to name %q", msg, tc.want)
			}
		})
	}

	// The control UI marks a send it replays after a reconnect. The gateway
	// strips that mark before validating, so a surface that refused it would
	// break every send made after a dropped socket.
	_, frame := ask(t, app, me, "s", "chat.send",
		`{"sessionKey":"k","message":"hi","idempotencyKey":"r-resume","__controlUiReconnectResume":true}`)
	if frame["ok"] != true {
		t.Errorf("a replayed send was refused: %v", frame)
	}
}

// The catalog is what the inference gateway serves, and a qualified id says
// which provider serves it.
func TestAiModelsListNamesWhatTheGatewayServes(t *testing.T) {
	app := mount(t, aiServes(&aiCatalogue{aiFake: &aiFake{}, served: []string{"openai/gpt-x", "flash"}}, "flash"))

	_, frame := ask(t, app, who{org: "acme"}, "m", "models.list", `{"view":"default"}`)
	if frame["ok"] != true {
		t.Fatalf("models.list refused: %v", frame)
	}
	models, _ := payload(t, frame)["models"].([]any)
	if len(models) != 2 {
		t.Fatalf("the catalog holds %d models", len(models))
	}
	first, _ := models[0].(map[string]any)
	if first["id"] != "openai/gpt-x" || first["provider"] != "openai" || first["name"] != "gpt-x" {
		t.Errorf("a qualified id was read as %v", first)
	}
	second, _ := models[1].(map[string]any)
	if second["id"] != "flash" {
		t.Errorf("an unqualified id was read as %v", second)
	}
	// An unqualified id names no provider, and this gateway does not know
	// which one routes it. The field is omitted rather than filled with the
	// deployment's brand, which means something else entirely.
	if _, named := second["provider"]; named {
		t.Errorf("an unqualified model was given provider %v", second["provider"])
	}
	// Whether a model can be reached right now is the inference gateway's
	// fact, not this surface's. Claiming it for every row is an invention.
	for _, m := range models {
		if row, _ := m.(map[string]any); row["available"] != nil {
			t.Errorf("the catalog asserts availability it cannot know: %v", row)
		}
	}
	if selection, _ := payload(t, frame)["accountSelection"].(map[string]any); selection["kind"] != "automatic" {
		t.Errorf("the account selection is %v", selection)
	}

	// A client that cannot enumerate leaves the configured model as the one
	// this deployment can reach.
	plain := mount(t, aiServes(&aiFake{}, "flash"))
	_, frame = ask(t, plain, who{org: "acme"}, "m2", "models.list", "")
	models, _ = payload(t, frame)["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("a gateway that lists nothing offered %v", models)
	}
	only, _ := models[0].(map[string]any)
	if only["id"] != "flash" {
		t.Errorf("the one reachable model is %v", only)
	}
	if only["available"] != nil {
		t.Errorf("a model this gateway has never probed was reported %v", only["available"])
	}
}

// Two catalog parameters that contradict each other are a request the caller
// can correct, which is INVALID_REQUEST and not a refusal of some other kind:
// a client that reads UNAVAILABLE backs off and sends the same contradiction
// again.
func TestAiModelsListRefusesContradictions(t *testing.T) {
	app := mount(t, aiServes(&aiFake{}, "flash"))
	for _, params := range []string{
		`{"preparedOnly":true,"refresh":true}`,
		`{"sessionKey":"k","authProfileId":"p"}`,
		`{"view":"sideways"}`,
	} {
		_, frame := ask(t, app, who{org: "acme"}, "m", "models.list", params)
		if frame["ok"] != false {
			t.Errorf("%s was accepted", params)
			continue
		}
		if code := wrong(t, frame)["code"]; code != "INVALID_REQUEST" {
			t.Errorf("%s was refused with %v, want INVALID_REQUEST", params, code)
		}
	}
	// A catalog read that contradicts nothing is answered, so the refusals
	// above are about the parameters rather than about the method.
	if _, frame := ask(t, app, who{org: "acme"}, "m", "models.list", `{"view":"all"}`); frame["ok"] != true {
		t.Errorf("a well-formed catalog read was refused: %v", frame)
	}
}

// models.authLogout is not on the surface. It removes a saved provider
// credential and this gateway saves none, so it could only refuse — and a name
// on the advertised list is what puts a Sign out control on the model page.
func TestAiModelLogoutIsNotOffered(t *testing.T) {
	app := mount(t, aiServes(&aiFake{}, "flash"))
	_, frame := ask(t, app, who{org: "acme", admin: true}, "1:a", "connect", "")
	features, _ := payload(t, frame)["features"].(map[string]any)
	for _, m := range features["methods"].([]any) {
		if m == "models.authLogout" {
			t.Fatal("models.authLogout is advertised and cannot be answered")
		}
	}
	// models.list, which the same page reads, is there: the absence above is
	// about the one method, not about the family.
	surface.mu.RLock()
	defer surface.mu.RUnlock()
	if _, ok := surface.methods["models.list"]; !ok {
		t.Error("models.list is not on the surface")
	}
	if _, ok := surface.methods["models.authLogout"]; ok {
		t.Error("models.authLogout is registered")
	}
}

// Every session that names no agent belongs to the default one, so the roster
// carries it whether or not anyone has configured it.
func TestAiAgentsListCarriesTheDefaultAgent(t *testing.T) {
	app := mount(t)
	_, frame := ask(t, app, who{org: "acme"}, "a", "agents.list", `{}`)
	if frame["ok"] != true {
		t.Fatalf("agents.list refused: %v", frame)
	}
	out := payload(t, frame)
	if out["defaultId"] != sessionAgent || out["mainKey"] != sessionAgent {
		t.Errorf("the roster names %v as default and %v as main", out["defaultId"], out["mainKey"])
	}
	if out["scope"] != "per-sender" {
		t.Errorf("the scope is %v", out["scope"])
	}
	agents, _ := out["agents"].([]any)
	if len(agents) != 1 {
		t.Fatalf("the roster holds %v", agents)
	}
	if only, _ := agents[0].(map[string]any); only["id"] != sessionAgent {
		t.Errorf("the one agent is %v", only)
	}
}

// An update writes what it was sent and leaves the rest; a model of null
// returns the agent to the deployment's own.
func TestAiAgentUpdateSetsAndClearsAModel(t *testing.T) {
	app := mount(t)
	boss := who{org: "acme", admin: true}

	_, frame := ask(t, app, boss, "u", "agents.update", `{"agentId":"main","name":"Ada","model":"gpt-x"}`)
	if frame["ok"] != true || payload(t, frame)["agentId"] != "main" {
		t.Fatalf("agents.update answered %v", frame)
	}

	_, frame = ask(t, app, boss, "a", "agents.list", `{}`)
	agents, _ := payload(t, frame)["agents"].([]any)
	first, _ := agents[0].(map[string]any)
	if first["name"] != "Ada" {
		t.Errorf("the agent is called %v", first["name"])
	}
	if model, _ := first["model"].(map[string]any); model["primary"] != "gpt-x" {
		t.Errorf("the agent runs on %v", first["model"])
	}

	// A name left unsent stays; a model set to null goes.
	_, frame = ask(t, app, boss, "u2", "agents.update", `{"agentId":"main","model":null}`)
	if frame["ok"] != true {
		t.Fatalf("clearing the model: %v", frame)
	}
	_, frame = ask(t, app, boss, "a2", "agents.list", `{}`)
	agents, _ = payload(t, frame)["agents"].([]any)
	first, _ = agents[0].(map[string]any)
	if first["model"] != nil {
		t.Errorf("a cleared model came back as %v", first["model"])
	}
	if first["name"] != "Ada" {
		t.Errorf("an unsent field was overwritten: %v", first["name"])
	}

	// Configuring an agent is an act on the deployment.
	_, frame = ask(t, app, who{org: "acme"}, "u3", "agents.update", `{"agentId":"main","name":"nope"}`)
	if wrong(t, frame)["code"] != "FORBIDDEN" {
		t.Errorf("a member configured an agent: %v", frame)
	}
}

// A turn runs on the agent's model when the session names none.
func TestAiTurnRunsOnTheAgentsModel(t *testing.T) {
	model := &aiFake{reply: "done"}
	app := mount(t, aiServes(model, "flash"))
	if _, frame := ask(t, app, who{org: "acme", admin: true}, "u", "agents.update",
		`{"agentId":"main","model":"gpt-x"}`); frame["ok"] != true {
		t.Fatalf("agents.update: %v", frame)
	}

	me := who{org: "acme"}
	aiSend(t, app, me, "pick", "run-8", "hello")
	messages := aiSettled(t, app, me, "pick", 2)
	if reply, _ := messages[1].(map[string]any); reply["model"] != "gpt-x" {
		t.Errorf("the turn ran on %v, want the agent's own model", reply["model"])
	}
}

// What a client may type into a session: the commands it can name, and whether
// one session drives several agents.
func TestAiMetadataAnswersACatalogue(t *testing.T) {
	app := mount(t)
	_, frame := ask(t, app, who{org: "acme"}, "m", "chat.metadata", `{"agentId":"main"}`)
	if frame["ok"] != true {
		t.Fatalf("chat.metadata refused: %v", frame)
	}
	out := payload(t, frame)
	if commands, ok := out["commands"].([]any); !ok || commands == nil {
		t.Errorf("the catalog is %v, want a list even when it is empty", out["commands"])
	}
	if out["swarmEnabled"] != false {
		t.Errorf("swarmEnabled is %v", out["swarmEnabled"])
	}

	_, frame = ask(t, app, who{org: "acme"}, "m2", "chat.metadata",
		`{"sessionKey":"k","authProfileId":"p"}`)
	if frame["ok"] != false {
		t.Error("a preview of a new chat was combined with a session's own")
	}
}

// The catalog comes from whoever owns commands, and it arrives as that family's
// own Go value rather than as JSON. A reader that assumed one shape would hand
// the composer an empty palette and log nothing, so this stands a family in
// that answers with a typed slice — which is the idiom every other family here
// uses.
func TestAiMetadataCarriesTheCommandsAnotherFamilyOwns(t *testing.T) {
	app := mount(t)
	_, frame := ask(t, app, who{org: "acme"}, "m", "chat.metadata", `{"agentId":"main"}`)
	commands, _ := payload(t, frame)["commands"].([]any)
	if len(commands) != len(probeCommands) {
		t.Fatalf("the palette holds %d commands, want the %d the catalog serves", len(commands), len(probeCommands))
	}
	first, _ := commands[0].(map[string]any)
	if first["name"] != probeCommands[0].Name {
		t.Errorf("the first command reads %v, want %q", first, probeCommands[0].Name)
	}
	if first["description"] != probeCommands[0].Description {
		t.Errorf("the command lost its description on the way through: %v", first)
	}
}

// A short link is the prefix of the id a session carries, and startup follows
// it to that session's first page — through the family that owns sessions, so
// the resolution and the read agree on which session was meant.
func TestAiStartupResolvesAShortLink(t *testing.T) {
	app := mount(t, aiServes(&aiFake{reply: "hi"}, "flash"))
	me := who{org: "acme"}
	aiSend(t, app, me, "boot", "run-10", "hello")
	aiSettled(t, app, me, "boot", 2)

	_, frame := ask(t, app, me, "d", "sessions.describe", `{"key":"boot"}`)
	row, _ := payload(t, frame)["session"].(map[string]any)
	id, _ := row["sessionId"].(string)
	if len(id) < 12 {
		t.Fatalf("the session carries no id to abbreviate: %v", row)
	}
	short := id[:12]

	_, frame = ask(t, app, me, "s", "chat.startup", `{"shortId":"`+short+`"}`)
	if frame["ok"] != true {
		t.Fatalf("chat.startup refused a live short link: %v", frame)
	}
	out := payload(t, frame)
	if out["sessionId"] != id {
		t.Errorf("the short link landed on session %v, want %v", out["sessionId"], id)
	}
	if messages, _ := out["messages"].([]any); len(messages) != 2 {
		t.Errorf("startup answered %d messages", len(messages))
	}

	// A short link nothing matches is a link the caller can correct.
	_, frame = ask(t, app, me, "s2", "chat.startup", `{"shortId":"ffffffffffff"}`)
	if frame["ok"] != false {
		t.Errorf("a short link matching nothing resolved: %v", frame)
	}

	// Given a session key it is the history read, so a client opens a session
	// in one round trip.
	_, frame = ask(t, app, me, "s3", "chat.startup", `{"sessionKey":"boot","limit":10}`)
	if frame["ok"] != true {
		t.Fatalf("chat.startup refused: %v", frame)
	}
	if messages, _ := payload(t, frame)["messages"].([]any); len(messages) != 2 {
		t.Errorf("startup answered %d messages", len(messages))
	}
}

// One org's conversation is another org's nothing, in BOTH places this family
// keeps state: the transcript, which is a file chosen by the caller's org, and
// the live turns, which are one map in one process. A session key is a string
// the client chooses, so two tenants using "main" is the ordinary case, and a
// lookup that matched on the key alone would let either one reach into the
// other.
func TestAiTurnsDoNotCrossOrgs(t *testing.T) {
	app := mount(t, aiServes(&aiFake{hold: true}, "flash"))
	ours, theirs := who{org: "acme"}, who{org: "other"}

	aiSend(t, app, ours, "shared", "acme-run", "ours")
	t.Cleanup(func() { _, _ = ask(t, app, ours, "z", "chat.abort", `{"sessionKey":"shared"}`) })

	// The other org holds no turn on that key, so it has nothing to abort.
	_, frame := ask(t, app, theirs, "a", "chat.abort", `{"sessionKey":"shared"}`)
	if frame["ok"] != true {
		t.Fatalf("chat.abort refused: %v", frame)
	}
	stopped := payload(t, frame)
	if stopped["status"] != "no-active-run" {
		t.Fatalf("one org aborted another org's turn: %v", stopped)
	}
	if stopped["abortedRunId"] != nil {
		t.Errorf("the abort named another org's run: %v", stopped["abortedRunId"])
	}

	// Nor is the other org's own send blocked by a session it does not have.
	_, frame = ask(t, app, theirs, "s", "chat.send",
		`{"sessionKey":"shared","message":"mine","idempotencyKey":"other-run"}`)
	if frame["ok"] != true {
		t.Fatalf("one org's turn refused another org's send: %v", frame)
	}
	t.Cleanup(func() { _, _ = ask(t, app, theirs, "z2", "chat.abort", `{"sessionKey":"shared"}`) })

	// And the transcripts do not meet.
	page := aiPage(t, app, theirs, `{"sessionKey":"shared"}`)
	messages, _ := page["messages"].([]any)
	if len(messages) != 1 || aiSaid(t, messages[0]) != "mine" {
		t.Errorf("the other org read %v", messages)
	}
}

// Each method costs what the protocol says it costs.
func TestAiMethodsCostWhatTheProtocolSays(t *testing.T) {
	want := map[string]Scope{
		"chat.send": Write, "chat.abort": Write,
		"chat.history": Read, "chat.startup": Read,
		"chat.metadata": Read, "chat.message.get": Read,
		"models.list": Read,
		"agents.list": Read, "agents.update": Admin,
	}
	surface.mu.RLock()
	defer surface.mu.RUnlock()
	for name, need := range want {
		m, ok := surface.methods[name]
		if !ok {
			t.Errorf("%s is not on the surface", name)
			continue
		}
		if m.need != need {
			t.Errorf("%s costs %q, want %q", name, m.need, need)
		}
	}
}

// queueMode says how a send joins a session that already has a run. Only
// interrupt is served: the other three need an admission queue that holds a
// message until the running turn can take it, and there is none here. A
// parameter that changes behaviour and is quietly ignored is worse than one
// that is refused, so the three are refused by name.
func TestAiQueueModeInterruptsOrIsRefused(t *testing.T) {
	app := mount(t, aiServes(&aiFake{hold: true}, "flash"))
	me := who{org: "acme"}
	aiSend(t, app, me, "q", "run-first", "one")
	t.Cleanup(func() { _, _ = ask(t, app, me, "z", "chat.abort", `{"sessionKey":"q"}`) })

	for _, mode := range []string{"steer", "followup", "collect"} {
		_, frame := ask(t, app, me, "s:"+mode, "chat.send",
			`{"sessionKey":"q","message":"m","idempotencyKey":"run-`+mode+`","queueMode":"`+mode+`"}`)
		if frame["ok"] != false {
			t.Errorf("queueMode %q was accepted and does nothing", mode)
			continue
		}
		// A caller-correctable refusal, not a retryable one: waiting and
		// sending the same mode again will never work.
		e := wrong(t, frame)
		if e["code"] != "INVALID_REQUEST" {
			t.Errorf("queueMode %q was refused with %v, want INVALID_REQUEST", mode, e["code"])
		}
		if e["retryable"] == true {
			t.Errorf("queueMode %q invites a retry that cannot succeed", mode)
		}
	}

	// A mode outside the protocol's own set is refused too.
	if _, frame := ask(t, app, me, "s:x", "chat.send",
		`{"sessionKey":"q","message":"m","idempotencyKey":"run-x","queueMode":"sideways"}`); frame["ok"] != false {
		t.Error("a queueMode outside the protocol's set was accepted")
	}

	// interrupt displaces the running turn and takes its place.
	_, frame := ask(t, app, me, "s:i", "chat.send",
		`{"sessionKey":"q","message":"instead","idempotencyKey":"run-second","queueMode":"interrupt"}`)
	if frame["ok"] != true {
		t.Fatalf("an interrupt was refused on a busy session: %v", frame)
	}
	if payload(t, frame)["runId"] != "run-second" {
		t.Errorf("the interrupt answered %v", payload(t, frame))
	}
}

// A displaced turn unwinds after its replacement has claimed the session. It
// must not stamp its own outcome onto the row the replacement already owns, or
// the sidebar reads "stopped" while a model is being asked.
func TestAiInterruptedTurnDoesNotSettleOverItsReplacement(t *testing.T) {
	app := mount(t, aiServes(&aiFake{hold: true}, "flash"))
	me := who{org: "acme"}
	aiSend(t, app, me, "i", "run-old", "one")
	t.Cleanup(func() { _, _ = ask(t, app, me, "z", "chat.abort", `{"sessionKey":"i"}`) })

	if _, frame := ask(t, app, me, "s:i", "chat.send",
		`{"sessionKey":"i","message":"instead","idempotencyKey":"run-new","queueMode":"interrupt"}`); frame["ok"] != true {
		t.Fatalf("the interrupt was refused: %v", frame)
	}
	// Let the displaced turn finish unwinding, which is when it would write.
	deadline := time.Now().Add(5 * time.Second)
	for chatFind("acme", "", "run-old", "") != nil {
		if time.Now().After(deadline) {
			t.Fatal("the displaced turn never unwound")
		}
		time.Sleep(5 * time.Millisecond)
	}

	_, frame := ask(t, app, me, "d", "sessions.describe", `{"key":"i"}`)
	row, _ := payload(t, frame)["session"].(map[string]any)
	if row["lastRunId"] != "run-new" {
		t.Errorf("the row names run %v, want the live one", row["lastRunId"])
	}
	if row["status"] != "running" {
		t.Errorf("the row reports %v while its live turn is running", row["status"])
	}
}

// A client that reloads mid-turn restores its stop control from the page it
// reads. Without the run on that page the conversation looks settled while a
// model is still being asked, and the Stop button never appears.
func TestAiHistoryCarriesTheRunStillInFlight(t *testing.T) {
	app := mount(t, aiServes(&aiFake{hold: true}, "flash"))
	me := who{org: "acme"}
	aiSend(t, app, me, "f", "run-live", "wait")
	t.Cleanup(func() { _, _ = ask(t, app, me, "z", "chat.abort", `{"sessionKey":"f"}`) })

	page := aiPage(t, app, me, `{"sessionKey":"f"}`)
	flight, _ := page["inFlightRun"].(map[string]any)
	if flight == nil {
		t.Fatalf("the page reports no run in flight while one is running: %v", page)
	}
	if flight["runId"] != "run-live" {
		t.Errorf("the page names run %v", flight["runId"])
	}
	if flight["sessionAbortable"] != true {
		t.Errorf("the page says the run cannot be stopped, and sessions.abort stops it: %v", flight)
	}

	// A settled conversation carries no run.
	if _, frame := ask(t, app, me, "a", "chat.abort", `{"sessionKey":"f"}`); frame["ok"] != true {
		t.Fatalf("chat.abort: %v", frame)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		page = aiPage(t, app, me, `{"sessionKey":"f"}`)
		if page["inFlightRun"] == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a settled page still reports a run in flight: %v", page["inFlightRun"])
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The two page budgets measure different things. maxBytes bounds what the page
// weighs on the wire, so it counts the encoded message — the role, the stamps
// and the content envelope included. maxChars bounds what a reader is shown, so
// it counts the text. Holding a caller who asked for bytes to a count of
// characters hands back a page several times its stated budget.
func TestAiHistoryHoldsEachBudgetToItsOwnMeasure(t *testing.T) {
	app := mount(t, aiServes(&aiFake{reply: "0123456789"}, "flash"))
	me := who{org: "acme"}
	for i, run := range []string{"b1", "b2", "b3"} {
		aiSend(t, app, me, "budget", run, "0123456789")
		aiSettled(t, app, me, "budget", 2*(i+1))
	}

	// Six messages of ten characters each: 60 characters of text, and far more
	// than 60 bytes once each is encoded.
	full := aiPage(t, app, me, `{"sessionKey":"budget"}`)
	messages, _ := full["messages"].([]any)
	if len(messages) != 6 {
		t.Fatalf("the transcript holds %d messages", len(messages))
	}

	byChars := aiPage(t, app, me, `{"sessionKey":"budget","maxChars":25}`)
	kept, _ := byChars["messages"].([]any)
	if len(kept) != 2 {
		t.Errorf("a 25-character budget kept %d messages of ten characters each", len(kept))
	}

	// The same number read as bytes must keep fewer, because a message weighs
	// more than its text.
	byBytes := aiPage(t, app, me, `{"sessionKey":"budget","maxBytes":25}`)
	weighed, _ := byBytes["messages"].([]any)
	if len(weighed) >= len(kept) {
		t.Errorf("a 25-byte budget kept %d messages and a 25-character budget kept %d; the two are being measured the same way",
			len(weighed), len(kept))
	}
	if len(weighed) != 1 {
		t.Errorf("a budget smaller than one message kept %d, want the newest one whatever the budget", len(weighed))
	}
	// The total is the transcript's, not the page's: a client computes where
	// the page sits in the conversation from it.
	if byBytes["totalMessages"] != float64(6) {
		t.Errorf("a trimmed page reports %v total messages", byBytes["totalMessages"])
	}
}

// Inference is reached with the binary's own identity, and the request names
// two orgs: the one whose data is in scope, and the one whose ledger pays. A
// platform admin acting in another org pays from its own ledger, so the debit
// never lands on the tenant being acted on.
func TestAiTurnBillsTheCallersOwnLedger(t *testing.T) {
	model := &aiFake{reply: "ok"}
	app := mount(t, aiServes(model, "flash"))
	aiSend(t, app, who{org: "acme"}, "bill", "run-bill", "hello")
	aiSettled(t, app, who{org: "acme"}, "bill", 2)

	org, bill := model.charged()
	if org != "acme" {
		t.Errorf("the turn read the data of org %q", org)
	}
	if bill != "acme" {
		t.Errorf("the turn was billed to %q, want the caller's own home org", bill)
	}
}

// A transcript's document id is the message's position, so a position chosen
// from a count read a moment earlier is a position another writer may already
// have taken — and the write that follows replaces a message instead of adding
// one. The server answers that send with the position it took, so the loss is
// silent in both directions: the client is told where its message is, and it is
// not there.
//
// queueMode "interrupt" is the reachable case. The composer drains its own
// sends through one lane, but a slash command sends outside that lane
// (ui/src/pages/chat/chat-command-executor.ts sends chat.send with
// queueMode "interrupt" through a raw request), and a second tab has a lane of
// its own. An interrupt is always granted, so every one of these proceeds.
//
// The model never answers, so the transcript can hold only the questions the
// sends themselves wrote and any shortfall is a lost question.
func TestConcurrentInterruptsKeepEveryQuestion(t *testing.T) {
	app := mount(t, aiServes(&aiFake{hold: true}, "m"))
	me := who{org: "acme"}
	if _, frame := ask(t, app, me, "1:a", "sessions.create", `{"key":"k"}`); frame["ok"] != true {
		t.Fatalf("create: %v", frame)
	}

	const sends = 8
	params := make([]string, sends)
	for i := range params {
		params[i] = fmt.Sprintf(
			`{"sessionKey":"k","message":"m%d","idempotencyKey":"run%d","queueMode":"interrupt","deliver":false}`, i, i)
	}
	want := map[string]bool{}
	for i, frame := range atOnce(t, app, me, "chat.send", params) {
		if frame["ok"] != true {
			t.Fatalf("send %d refused: %v", i, frame)
		}
		want[fmt.Sprintf("m%d", i)] = true
	}

	held := map[string]bool{}
	messages, _ := aiPage(t, app, me, `{"sessionKey":"k"}`)["messages"].([]any)
	for _, m := range messages {
		held[aiSaid(t, m)] = true
	}
	lost := []string{}
	for text := range want {
		if !held[text] {
			lost = append(lost, text)
		}
	}
	sort.Strings(lost)
	if len(lost) > 0 {
		t.Errorf("%d of %d accepted sends are not in the transcript: %v — each was answered with a position it does not hold",
			len(lost), sends, lost)
	}
}

// The same, with a model that answers: a reply takes its position at the moment
// it is written, so a question that landed while the model was being asked is
// still there afterwards.
func TestAReplyDoesNotOverwriteAQuestionThatArrivedWhileItRan(t *testing.T) {
	app := mount(t, aiServes(&aiFake{reply: "answered"}, "m"))
	me := who{org: "acme"}
	if _, frame := ask(t, app, me, "1:a", "sessions.create", `{"key":"k"}`); frame["ok"] != true {
		t.Fatalf("create: %v", frame)
	}

	const sends = 6
	params := make([]string, sends)
	for i := range params {
		params[i] = fmt.Sprintf(
			`{"sessionKey":"k","message":"q%d","idempotencyKey":"run%d","queueMode":"interrupt","deliver":false}`, i, i)
	}
	for i, frame := range atOnce(t, app, me, "chat.send", params) {
		if frame["ok"] != true {
			t.Fatalf("send %d refused: %v", i, frame)
		}
	}

	// Every question, whatever the surviving turn did with its answer.
	messages := aiSettled(t, app, me, "k", sends)
	held := map[string]bool{}
	for _, m := range messages {
		held[aiSaid(t, m)] = true
	}
	for i := range sends {
		if text := fmt.Sprintf("q%d", i); !held[text] {
			t.Errorf("question %q is gone from the transcript: %v", text, held)
		}
	}
}
