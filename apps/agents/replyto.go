package agents

// replyto.go — where a run started by THIS turn should narrate itself.
//
// A coding run outlives the turn that asks for it: the tool answers 202 and the
// work carries on long after the conversation has been replied to. So its
// progress cannot come back as the turn's answer — it has to be posted where the
// question was asked, and only the bridge knows where that is.
//
// It rides the CONTEXT rather than the tool-loop signatures because it is
// ambient, run-scoped and read only at dispatch, the same shape the tenant
// already travels in (cloud.For). Threading an address through completeWithTools
// and dispatchOne would put it in every call site that has no use for it, and
// the key is unexported, so nothing outside this package can plant one.
//
// THE MODEL IS NEVER ASKED FOR IT, AND NEVER BELIEVED ABOUT IT. A channel id is
// not something a brain can know, and a tool argument it invents is a run
// narrating into a channel nobody asked it into. The dispatcher writes the turn's
// own address in after the model has chosen the tool and before the tool is
// called — over anything already there, because the alternative is a model
// choosing where a tenant's work is announced.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud/client"
)

type replyKey struct{}

// said is the conversation a turn answers, as its bridge stated it: the channel,
// where a run the turn starts narrates, and the message in it, which a fact
// filed in the turn cites (attest.go).
type said struct {
	client.Reply
	client.Origin
}

// withReply parks the turn's conversation for the run about to happen. An
// address with no channel parks NOTHING: a surface with nowhere to talk is the
// ordinary case — the app holds a session id and reads the stream — and an empty
// value must never read back as a configured one.
func withReply(ctx context.Context, to client.Reply, from client.Origin) context.Context {
	if strings.TrimSpace(to.Channel) == "" {
		return ctx
	}
	return context.WithValue(ctx, replyKey{}, said{to, from})
}

// codingOp is the coding run's operation id. The fleet's MCP server publishes it
// as `create_agent_coding`, the verb phrase its id reads as (client/verbs.go), and
// answers to both.
const codingOp = "post_agent_coding"

// The board's two writes (apps/task). An issue filed or edited in a conversation
// is bound to it, and what happens to the issue after is said there; an issue
// handed to the agent starts a run that narrates there.
const (
	fileOp = "post_task_projects_by_key_issues"
	editOp = "patch_task_projects_by_key_issues_by_num"
)

// address writes the turn's address into the arguments of a call that has a use
// for it — a coding run, or a write to the board — in every spelling of the call
// (see amend), over anything the model put there.
//
// A coding call made inside a TURN names the turn as well (Reply.Turn): the turn
// follows the run into its own message (follow.go), so the run posts nothing of
// its own.
//
// A board write is given the THREAD instead, and no turn. An issue outlives the
// turn by days, so its conversation is the thread the question started — a
// question asked at the top of a channel is the root of the thread its answer
// opens — and a run a hand-off starts is not one this turn follows, so it
// narrates, with its controls, on its own.
func address(ctx context.Context, name, args string) string {
	s, ok := ctx.Value(replyKey{}).(said)
	if !ok {
		return args
	}
	to := s.Reply
	if l := liveFrom(ctx); l != nil {
		to.Turn = l.x.ID
	}
	if out, coding := amend(name, args, codingOp, func(input string) string { return set(input, "reply", to) }); coding {
		return out
	}
	thread := s.Reply
	if thread.Thread == "" {
		thread.Thread = s.Message
	}
	for _, op := range []string{fileOp, editOp} {
		if out, board := amend(name, args, op, func(input string) string { return set(input, "reply", thread) }); board {
			return out
		}
	}
	return args
}

// amend applies edit to the input of ONE operation, whichever way it was called,
// and reports whether the call was that operation.
//
// IT HANDLES EVERY SPELLING OF THE SAME CALL, and that is the whole of why this
// is not one line. The surface offers 1,189 operations grouped one tool per
// subsystem, because flat they were 977 KB merely to list (client/grouped.go),
// so the assistant that Slack actually talks to — the built-in, which declares
// ToolsAll — calls the tool `agent` with {"op":"create_agent_coding","input":{…}}
// and never the bare name. An agent that declares the operation by name gets the
// flat spelling. Either names the operation by its id or by its phrase.
//
// The envelope is read into the MCP server's own shape with its own decoder,
// which matches keys without regard to case, so {"Op":…,"Input":…} is the same
// call. It is written back in one spelling, leaving no second key for the server
// to prefer over the amended one.
//
// Everything else is returned untouched: another tool is not this operation, and
// arguments that will not parse belong to the tool plane to refuse with its own
// message.
func amend(name, args, op string, edit func(input string) string) (string, bool) {
	if is(name, op) {
		return edit(args), true
	}
	var e struct {
		Op    string          `json:"op"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal([]byte(args), &e) != nil || !is(e.Op, op) {
		return args, false
	}
	input := "{}"
	if len(e.Input) > 0 {
		input = string(e.Input)
	}
	e.Input = json.RawMessage(edit(input))
	b, err := json.Marshal(e)
	if err != nil {
		return args, true
	}
	return string(b), true
}

// is reports whether name is op, by either name the MCP server answers to.
func is(name, op string) bool { return name == op || name == client.Phrase(op) }

// set writes v at key, OVER whatever was there — under every key the tool's
// decoder reads as this one, which matches names as strings.EqualFold does. The
// keys it does not touch are carried as raw bytes: they are the tool's own, and
// this process has no opinion about a shape it does not own.
func set(args, key string, v any) string {
	obj, err := object(args)
	if err != nil {
		return args
	}
	b, err := json.Marshal(v)
	if err != nil {
		return args
	}
	for k := range obj {
		if strings.EqualFold(k, key) {
			delete(obj, k)
		}
	}
	obj[key] = b
	return encode(obj, args)
}

// object reads a tool call's arguments as the JSON object they have to be.
// Absent arguments are an empty object, which is what the tool plane makes of
// them too; anything else is an error and leaves the call alone.
func object(args string) (map[string]json.RawMessage, error) {
	s := strings.TrimSpace(args)
	if s == "" || s == "null" {
		return map[string]json.RawMessage{}, nil
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func encode(obj map[string]json.RawMessage, fallback string) string {
	b, err := json.Marshal(obj)
	if err != nil {
		return fallback
	}
	return string(b)
}
