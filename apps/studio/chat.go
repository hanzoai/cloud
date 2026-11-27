package studio

// chat.go — POST /v1/studio/chat: one turn of a conversation that plans and starts
// runs, streamed; and the threads it keeps.
//
// The assistant is a model bought for the caller's org (deps.AI, metered as every
// model call is). It is told what Studio can make — the catalog, each template's
// inputs — and answers in prose, then, when the person asked for something to be
// made, a plan after a marker line: the runs to start, each a RunCreate. The plan
// is never trusted for more than a run request is: every run it names is started
// through the same create a person's POST /v1/studio/runs takes, as that person,
// checked, priced and reserved the same way. A plan that names an asset the person
// cannot read starts nothing, exactly as a request would.
//
// A thread is its creator's alone. Its runs and their files are the person's like
// any other, shared or not by the same rules.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanzoai/orm/tenant"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/aimodel"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
)

// marker separates the assistant's prose from its plan.
const marker = "<<<RUNS"

// Chat bounds.
const (
	maxTurn    = 20000
	maxHistory = 20
	maxPlanned = 8
)

func init() {
	spec.Register(prefix+"/chat", http.MethodPost, ChatTurn{}, spec.Bytes{Type: "text/event-stream"})
	spec.Describe(prefix+"/chat", http.MethodPost,
		"One chat turn that plans and launches runs, streamed",
		"Sends one message in a thread (a new thread when `thread` is absent). The assistant answers in text and, "+
			"when the message asks for something to be made, plans it as runs and starts them; each is exactly the "+
			"run POST /v1/studio/runs would start, checked and paid for the same way.\n\n"+
			"Answers text/event-stream: `thread` {thread} first; `delta` {text} as the answer is written; `run` "+
			"{seq, run} for each run the turn started (follow it on its own events); `error` (a problem document) "+
			"when a planned run could not start — a wallet that cannot cover it is status 402 — and the turn "+
			"goes on; `done` {thread, message, runs} last, with the stored message. 402 before the stream when "+
			"the wallet cannot pay for the turn itself.")
}

// chat runs one turn. UNTYPED: the answer is a stream.
func (o ops) chat(c *zip.Ctx) error {
	if _, ok := principal.Org(c); !ok {
		return principal.Refused(c)
	}
	var in ChatTurn
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("the body is a chat turn: {message, thread?, assets?, brand?, model?}")
	}
	in.Message = strings.TrimSpace(in.Message)
	if in.Message == "" || len(in.Message) > maxTurn {
		return zip.ErrBadRequest(fmt.Sprintf("message is 1 to %d characters", maxTurn))
	}
	if len(in.Assets) > 16 {
		return zip.ErrBadRequest("a turn names at most 16 items")
	}
	// The turn outlives the request's handler, so it carries the caller and the
	// payer resolved now, while the request is real — and not the request itself,
	// which the stream's writer must never touch.
	ctx, me, err := o.who(cloud.Detach(context.Background(), c))
	if err != nil {
		return err
	}
	st := o.s.State
	if st.ai == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "this deployment has no model client")
	}
	for _, id := range in.Assets {
		if _, err := st.readable(ctx, id); err != nil {
			return zip.Errorf(http.StatusUnprocessableEntity, "item %s is not one you may use", id)
		}
	}
	th, history, err := st.thread(ctx, me, in.Thread, in.Message)
	if err != nil {
		return notFound(err)
	}
	user := messageRow{Thread: th.ID, ID: mint.ID("msg"), Role: "user", Text: in.Message, Assets: jsonOf(nonNil(in.Assets)), Runs: "[]", Created: now()}
	if err := st.db.Insert(ctx, "messages", user); err != nil {
		return err
	}
	req := &infer.ChatRequest{
		Model: or(in.Model, aimodel.DefaultModel), MaxTokens: 4000,
		Messages: st.prompt(history, in), Org: orgOf(ctx), Payer: cloud.PayerOf(ctx).Wallet, Project: cloud.PayerOf(ctx).Project,
	}
	req.Prompt = in.Message
	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-store")
	c.SetHeader("X-Accel-Buffering", "no")
	return c.SendStreamWriter(func(w *bufio.Writer) {
		send := func(kind string, v any) bool { return write(w, 0, kind, v) }
		if !send("thread", map[string]any{"thread": th.ID}) {
			return
		}
		text, err := st.complete(ctx, req, func(delta string) error {
			if !send("delta", map[string]any{"text": delta}) {
				return fmt.Errorf("the client went away")
			}
			return nil
		})
		if err != nil {
			send("error", problemOf(err))
			text = strings.TrimSpace(text)
		}
		prose, plan := split(text)
		var started []string
		for i, rc := range plan {
			if i == maxPlanned {
				break
			}
			if rc.Brand == "" {
				rc.Brand = in.Brand
			}
			r, err := o.create(ctx, &rc)
			if err != nil {
				send("error", problemOf(err))
				continue
			}
			started = append(started, r.ID)
			send("run", map[string]any{"seq": 0, "run": r})
		}
		reply := messageRow{Thread: th.ID, ID: mint.ID("msg"), Role: "assistant", Text: prose, Assets: "[]", Runs: jsonOf(nonNil(started)), Created: now()}
		if err := st.db.Insert(ctx, "messages", reply); err != nil {
			send("error", problemOf(err))
		}
		_, _ = st.db.Update(ctx, "threads", tenant.Row{"updated": now()}, tenant.Eq("id", th.ID))
		send("done", map[string]any{"thread": th.ID, "message": reply.view(), "runs": nonNil(started)})
	})
}

// complete runs the turn's completion, streamed when the client can.
func (s *state) complete(ctx context.Context, req *infer.ChatRequest, emit func(string) error) (string, error) {
	var b strings.Builder
	hidden := false
	forward := func(delta string) error {
		b.WriteString(delta)
		if hidden {
			return nil
		}
		if i := strings.Index(b.String(), marker); i >= 0 {
			hidden = true
			shown := b.Len() - len(delta)
			if i > shown {
				return emit(b.String()[shown:i])
			}
			return nil
		}
		return emit(delta)
	}
	if sc, ok := s.ai.(infer.StreamCompleter); ok {
		resp, err := sc.ChatStream(ctx, req, forward)
		if err != nil {
			return b.String(), err
		}
		if b.Len() == 0 && resp != nil {
			return resp.Content, forward(resp.Content)
		}
		return b.String(), nil
	}
	resp, err := s.ai.ChatCompletion(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, forward(resp.Content)
}

// split is a turn's answer as its prose and its plan.
func split(text string) (string, []RunCreate) {
	prose, plan, ok := strings.Cut(text, marker)
	prose = strings.TrimSpace(prose)
	if !ok {
		return prose, nil
	}
	v, err := parseJSON(plan)
	if err != nil {
		return prose, nil
	}
	b, _ := json.Marshal(v)
	var runs []RunCreate
	if json.Unmarshal(b, &runs) != nil {
		var one struct {
			Runs []RunCreate `json:"runs"`
		}
		if json.Unmarshal(b, &one) != nil {
			return prose, nil
		}
		runs = one.Runs
	}
	return prose, runs
}

// prompt is what the assistant is told: what Studio makes, the thread so far, and
// the turn.
func (s *state) prompt(history []messageRow, in ChatTurn) []infer.ChatMessage {
	var cat strings.Builder
	for _, t := range catalog() {
		props, _ := json.Marshal(t.Inputs["properties"])
		req, _ := json.Marshal(t.Inputs["required"])
		fmt.Fprintf(&cat, "- %s (%s): %s inputs=%s required=%s\n", t.ID, t.Kind, t.Description, props, req)
	}
	system := "You are Hanzo Studio, where people make images, posters, documents, slides, type systems, 3D models, " +
		"ads, video, film, voice and music. Answer briefly and warmly, in plain prose. When the person asks for something " +
		"to be made, say in a sentence what you will make, then write a line containing only " + marker + " followed by a " +
		"JSON array of runs to start, each {\"template\": id, \"input\": {...}, \"title\": short title}. Use only these " +
		"templates and their inputs:\n" + cat.String() +
		"An input naming a file uses its Library id (lib_...). Write prompts and briefs richly: the person's words, " +
		"made specific. When nothing should be made, write no " + marker + " line."
	msgs := []infer.ChatMessage{{Role: "system", Content: system}}
	for _, m := range history {
		text := m.Text
		var runs []string
		_ = json.Unmarshal([]byte(m.Runs), &runs)
		if len(runs) > 0 {
			text += "\n(started runs: " + strings.Join(runs, ", ") + ")"
		}
		msgs = append(msgs, infer.ChatMessage{Role: m.Role, Content: text})
	}
	turn := in.Message
	if len(in.Assets) > 0 {
		turn += "\n\n(attached Library items: " + strings.Join(in.Assets, ", ") + ")"
	}
	return append(msgs, infer.ChatMessage{Role: "user", Content: turn})
}

// thread is the caller's thread to continue, or a new one, and its last messages.
func (s *state) thread(ctx context.Context, me, id, first string) (threadRow, []messageRow, error) {
	if id == "" {
		t := firstLine(first)
		if len(t) > 60 {
			t = t[:60]
		}
		th := threadRow{ID: mint.ID("thr"), Creator: me, Title: t, Created: now(), Updated: now()}
		return th, nil, s.db.Insert(ctx, "threads", th)
	}
	th, ok, err := s.db.Select[threadRow]("threads").Where(tenant.And(tenant.Eq("id", id), mine(me))).First(ctx)
	if err != nil {
		return threadRow{}, nil, err
	}
	if !ok {
		return threadRow{}, nil, gone("thread")
	}
	msgs, err := s.db.Select[messageRow]("messages").Where(tenant.Eq("thread", id)).OrderBy("created DESC").Limit(maxHistory).All(ctx)
	if err != nil {
		return threadRow{}, nil, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return th, msgs, nil
}

func (m messageRow) view() Message {
	v := Message{ID: m.ID, Role: m.Role, Text: m.Text, Created: stamp(m.Created)}
	_ = json.Unmarshal([]byte(m.Assets), &v.Assets)
	_ = json.Unmarshal([]byte(m.Runs), &v.Runs)
	return v
}

// problemOf is an error as the stream's error frame carries it.
func problemOf(err error) Problem {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return Problem{Type: "about:blank", Status: he.Status, Detail: he.Msg, Code: he.Code, Title: http.StatusText(he.Status)}
	}
	return Problem{Type: "about:blank", Status: http.StatusBadGateway, Detail: firstLine(err.Error()), Title: http.StatusText(http.StatusBadGateway)}
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }

// ── threads ──────────────────────────────────────────────────────────────

// Lists your chat threads, most recent first.
func (o ops) threads(ctx context.Context, in *pageIn) (*ThreadPage, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	limit, after, err := paging(in.Limit, in.Cursor)
	if err != nil {
		return nil, err
	}
	cond := mine(me)
	if after != nil {
		after.col = "updated"
		cond = tenant.And(cond, after.before())
	}
	rows, err := o.s.State.db.Select[threadRow]("threads").Where(cond).OrderBy("updated DESC", "id DESC").Limit(limit + 1).All(ctx)
	if err != nil {
		return nil, err
	}
	out := &ThreadPage{Data: []ThreadCard{}}
	for i, t := range rows {
		if i == limit {
			out.Next = cursorOf(rows[i-1].Updated, rows[i-1].ID)
			break
		}
		out.Data = append(out.Data, ThreadCard{ID: t.ID, Title: t.Title, Updated: stamp(t.Updated)})
	}
	return out, nil
}

// Reads one of your threads and its messages, oldest first.
func (o ops) threadGet(ctx context.Context, in *ref) (*Thread, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	st := o.s.State
	th, ok, err := st.db.Select[threadRow]("threads").Where(tenant.And(tenant.Eq("id", in.ID), mine(me))).First(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, zip.ErrNotFound("thread not found")
	}
	msgs, err := st.db.Select[messageRow]("messages").Where(tenant.Eq("thread", th.ID)).OrderBy("created ASC").Limit(500).All(ctx)
	if err != nil {
		return nil, err
	}
	out := &Thread{ID: th.ID, Title: th.Title, Org: orgOf(ctx), Creator: th.Creator, Created: stamp(th.Created), Updated: stamp(th.Updated), Messages: []Message{}}
	for _, m := range msgs {
		out.Messages = append(out.Messages, m.view())
	}
	return out, nil
}

// Deletes one of your threads. Its runs and their files stay.
func (o ops) threadDelete(ctx context.Context, in *ref) (*cloudUnit, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	st := o.s.State
	return nil, st.db.Tx(ctx, func(tx *tenant.DB) error {
		n, err := tx.Delete(ctx, "threads", tenant.And(tenant.Eq("id", in.ID), mine(me)))
		if err != nil {
			return err
		}
		if n == 0 {
			return zip.ErrNotFound("thread not found")
		}
		_, err = tx.Delete(ctx, "messages", tenant.Eq("thread", in.ID))
		return err
	})
}
