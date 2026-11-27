package agents

// steer_test.go — what a person says into a running turn is the turn's next
// input, handed over once, and a turn that has answered takes no more.

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// say records one message into the turn as its person does.
func say(t *testing.T, sto *Store, id, text string) error {
	t.Helper()
	x := reload(t, sto, id)
	_, err := apply(nil, context.Background(), sto, x, x.Actor, &controlIn{ID: id, Message: text}, CmdMessage,
		client.Reply{}, client.Origin{})
	return err
}

// Each message is handed to the run once, in order, and the cursor is on the row:
// a store opened afresh — a restart — hands over nothing already handed over.
func TestWhatIsSaidIsHandedOverOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()
		open := func() *Store {
			c := cloud.NewOrgStore[*Store](cloud.Base{DataDir: dir}, "agents", openStore)
			t.Cleanup(func() { _ = c.CloseAll() })
			st := &state{stores: c}
			return storeOf(t, st, "acme")
		}
		sto := open()
		turnSession(t, sto, "sess_c", "acme/z", "run_c")
		for _, text := range []string{"use the staging db", "and skip the migration"} {
			if err := say(t, sto, "sess_c", text); err != nil {
				t.Fatal(err)
			}
		}
		got, err := sto.Steers(context.Background(), "acme", "sess_c", false)
		if err != nil || len(got) != 2 || got[0] != "use the staging db" || got[1] != "and skip the migration" {
			t.Fatalf("first look: %q %v", got, err)
		}
		if again, _ := sto.Steers(context.Background(), "acme", "sess_c", false); len(again) != 0 {
			t.Fatalf("handed over twice: %q", again)
		}

		_ = sto.Close()
		sto = open()
		if again, _ := sto.Steers(context.Background(), "acme", "sess_c", false); len(again) != 0 {
			t.Fatalf("a restart handed over again: %q", again)
		}
		if err := say(t, sto, "sess_c", "one more"); err != nil {
			t.Fatal(err)
		}
		if got, _ := sto.Steers(context.Background(), "acme", "sess_c", false); len(got) != 1 || got[0] != "one more" {
			t.Fatalf("after a restart: %q", got)
		}

	})
}

// The last look seals the turn only when nothing is waiting; after it, a message
// is refused as a new turn's, and a resumed turn listens again.
func TestTheLastLookClosesTheTurn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, sto := meterStore(t)
		turnSession(t, sto, "sess_l", "acme/z", "run_l")
		if err := say(t, sto, "sess_l", "wait, one thing"); err != nil {
			t.Fatal(err)
		}
		if got, _ := sto.Steers(context.Background(), "acme", "sess_l", true); len(got) != 1 {
			t.Fatalf("a waiting message must be handed over rather than sealed past: %q", got)
		}
		if err := say(t, sto, "sess_l", "and another"); err != nil {
			t.Fatalf("the turn was sealed with a message waiting: %v", err)
		}
		_, _ = sto.Steers(context.Background(), "acme", "sess_l", true)
		if got, _ := sto.Steers(context.Background(), "acme", "sess_l", true); len(got) != 0 {
			t.Fatalf("nothing should be waiting: %q", got)
		}
		var he *zip.HTTPError
		if err := say(t, sto, "sess_l", "too late"); !errors.As(err, &he) || he.Status != http.StatusConflict {
			t.Fatalf("a message after the answer must be refused 409, got %v", err)
		}

		_, _ = sto.End(context.Background(), "acme", "sess_l", StatusStopped, time.Now().Unix())
		if ok, err := sto.Reopen(context.Background(), "acme", "sess_l", "run_l2", time.Now().Unix()); !ok || err != nil {
			t.Fatalf("reopen: %v %v", ok, err)
		}
		if err := say(t, sto, "sess_l", "carry on with the tests"); err != nil {
			t.Fatalf("a resumed turn must listen again: %v", err)
		}

	})
}

// steering is a scripted model with something said into the turn at a chosen
// completion, before the model answers it — as a person typing while it works.
type steering struct {
	*scriptAI
	at map[int]func()
}

func (s steering) ChatCompletion(ctx context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	if f := s.at[len(s.seen)]; f != nil {
		f()
	}
	return s.scriptAI.ChatCompletion(ctx, req)
}

// A message said between rounds reaches the next completion as the person's own
// words, and one said while the model writes its answer gets a round of its own:
// the answer is final only when nothing said is waiting.
func TestATurnHearsWhatIsSaidWhileItWorks(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, sto := meterStore(t)
		s := &cloud.Service[state]{Log: luxlog.New("test"), State: *st}
		turnSession(t, sto, "sess_h", "acme/z", "run_h")
		l := &live{s: s, x: reload(t, sto, "sess_h"), run: "run_h"}
		plane := &fakePlane{offer: []infer.ToolDef{toolDef("weather")}, out: `{"temp":21}`}
		withPlane(t, plane)
		ai := steering{
			scriptAI: &scriptAI{replies: []infer.ChatResponse{
				{ToolCalls: []infer.ToolCall{{ID: "c1", Name: "weather", Arguments: `{"city":"Tokyo"}`}}},
				{Content: "21 in Tokyo."},
				{Content: "21 in Tokyo, 18 in Paris."},
			}},
			// Said while the model writes its first answer.
			at: map[int]func(){1: func() { _ = say(t, sto, "sess_h", "and Paris") }},
		}
		_ = say(t, sto, "sess_h", "in celsius") // said before the loop first looks

		ctx := withLive(context.Background(), l)
		resp, _, err, _ := completeWithTools(ctx, ai, "acme", "acme/z",
			conversation("be brief", nil, "weather in Tokyo?"), "m", "", []infer.ToolDef{toolDef("weather")}, "run_h")
		if err != nil {
			t.Fatal(err)
		}
		if resp.Content != "21 in Tokyo, 18 in Paris." {
			t.Fatalf("the turn answered %q; the message said while it wrote was dropped", resp.Content)
		}
		if len(ai.seen) != 3 {
			t.Fatalf("want 3 completions, got %d", len(ai.seen))
		}
		if !has(ai.seen[0].Messages, "in celsius") {
			t.Fatal("a message said before the first round did not reach it")
		}
		last := ai.seen[2].Messages
		if n := len(last); last[n-1].Role != infer.RoleUser || last[n-1].Content != "and Paris" ||
			last[n-2].Role != infer.RoleAssistant || last[n-2].Content != "21 in Tokyo." {
			t.Fatalf("the extra round must carry the answer and then the message: %+v", last[n-2:])
		}
		if err := say(t, sto, "sess_h", "thanks"); err == nil {
			t.Fatal("the turn answered and still took a message")
		}

	})
}

func has(msgs []infer.ChatMessage, text string) bool {
	for _, m := range msgs {
		if m.Role == infer.RoleUser && m.Content == text {
			return true
		}
	}
	return false
}

// A surface steers a session only for the person it belongs to; anyone else is
// refused 403, and nothing is recorded.
func TestOnlyTheOwnerSteersASession(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountInproc(t)
		sto := storeOf(t, &mounted.State, "acme")
		turnSession(t, sto, "sess_o", "acme/alice", "run_o")
		ctx := cloud.For(context.Background(), "acme")

		_, err := planeSessionControl(ctx, &client.SessionControlIn{Subject: "bob", SessionID: "sess_o", Command: CmdMessage, Message: "hi"})
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusForbidden {
			t.Fatalf("bob steered alice's turn: %v", err)
		}
		if got, _ := sto.Steers(context.Background(), "acme", "sess_o", false); len(got) != 0 {
			t.Fatalf("a refused message was recorded: %q", got)
		}

		out, err := planeSessionControl(ctx, &client.SessionControlIn{Subject: "alice", SessionID: "sess_o", Command: CmdMessage, Message: "hi"})
		if err != nil || out.Was != StatusRunning {
			t.Fatalf("alice steering her own turn: %+v %v", out, err)
		}
		if got, _ := sto.Steers(context.Background(), "acme", "sess_o", false); len(got) != 1 || got[0] != "hi" {
			t.Fatalf("alice's message: %q", got)
		}

	})
}
