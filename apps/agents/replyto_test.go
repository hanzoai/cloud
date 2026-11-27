package agents

// replyto_test.go — the address a run is given, at the one moment it can be
// given one.
//
// The bar is not "a field was set". It is that the value lands WHERE THE CHILD
// READS IT, through the spelling the Slack path actually uses: the built-in
// assistant declares ToolsAll, so the surface offers the MCP server's tools
// GROUPED — one per subsystem — and the model calls `agent` with
// {"op":"create_agent_coding","input":{…}}. The server unwraps that and forwards
// ONLY `input` to the child (client/grouped.go), so anything written at the top
// level of a grouped call is dropped in transit and the run narrates nowhere.
//
// A test that drove only the flat spelling would have passed against exactly
// that bug, which is why every case below is asserted on what the child decodes.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/infer"
)

// delivered is what the coding op ACTUALLY receives: the envelope opened the way
// the MCP server opens it, then decoded as the op's own input.
func delivered(args string) client.CodingStartIn {
	var env struct {
		Op    string          `json:"op"`
		Input json.RawMessage `json:"input"`
	}
	body := args
	if json.Unmarshal([]byte(args), &env) == nil && env.Op != "" {
		body = string(env.Input)
	}
	var in client.CodingStartIn
	_ = json.Unmarshal([]byte(body), &in)
	return in
}

var (
	thread  = client.Reply{Provider: "slack", Channel: "C024BE91L", Thread: "1726459200.001900"}
	channel = client.Reply{Provider: "slack", Channel: "C024BE91L"}
	spoof   = `"reply":{"provider":"slack","channel":"C-SOMEWHERE-ELSE","thread":"9.9"}`
)

// A coding run asked for in a conversation is dispatched with THAT
// conversation's address — in both spellings of the call, over anything the
// model put there, and never for a call that is not a coding run.
func TestTheTurnsAddressReachesTheCodingCall(t *testing.T) {
	for name, tc := range map[string]struct {
		to   client.Reply
		tool string
		args string
		want client.Reply
	}{
		"grouped — the shape every Slack turn takes": {
			to: thread, tool: "agent",
			args: `{"op":"create_agent_coding","input":{"repo":"api","prompt":"fix the auth test"}}`,
			want: thread,
		},
		"grouped, no thread: the run answers in the channel": {
			to: channel, tool: "agent",
			args: `{"op":"create_agent_coding","input":{"repo":"api","prompt":"x"}}`,
			want: channel,
		},
		"grouped, and the model named its own channel": {
			to: thread, tool: "agent",
			args: `{"op":"create_agent_coding","input":{"repo":"api","prompt":"x",` + spoof + `}}`,
			want: thread,
		},
		"grouped, with no input at all": {
			to: thread, tool: "agent",
			args: `{"op":"create_agent_coding"}`,
			want: thread,
		},
		"flat — an agent that declared the operation by name": {
			to: thread, tool: "create_agent_coding",
			args: `{"repo":"api","prompt":"fix the auth test"}`,
			want: thread,
		},
		"flat, and the model named its own channel": {
			to: thread, tool: "create_agent_coding",
			args: `{"repo":"api","prompt":"x",` + spoof + `}`,
			want: thread,
		},
		"grouped, the envelope in another case": {
			to: thread, tool: "agent",
			args: `{"Op":"create_agent_coding","Input":{"repo":"api","prompt":"x",` + spoof + `}}`,
			want: thread,
		},
		"grouped, by the operation's id": {
			to: thread, tool: "agent",
			args: `{"op":"post_agent_coding","input":{"repo":"api","prompt":"x",` + spoof + `}}`,
			want: thread,
		},
		"flat, by the operation's id": {
			to: thread, tool: "post_agent_coding",
			args: `{"repo":"api","prompt":"x",` + spoof + `}`,
			want: thread,
		},
		"a turn with nowhere to talk gives nothing": {
			to: client.Reply{}, tool: "agent",
			args: `{"op":"create_agent_coding","input":{"repo":"api","prompt":"x"}}`,
			want: client.Reply{},
		},
		"a thread with no channel addresses nothing": {
			to: client.Reply{Provider: "slack", Thread: "1.9"}, tool: "create_agent_coding",
			args: `{"repo":"api","prompt":"x"}`,
			want: client.Reply{},
		},
		"another op in the same subsystem is not a run": {
			to: thread, tool: "agent",
			args: `{"op":"list_agent_sessions","input":{}}`,
			want: client.Reply{},
		},
		"another subsystem entirely": {
			to: thread, tool: "git",
			args: `{"op":"post_v1_git_repos","input":{"name":"api"}}`,
			want: client.Reply{},
		},
		"arguments that will not parse are the tool plane's to refuse": {
			to: thread, tool: "create_agent_coding",
			args: `{"repo":`,
			want: client.Reply{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := address(withReply(context.Background(), tc.to, client.Origin{}), tc.tool, tc.args)
			if in := delivered(got).Reply; in != tc.want {
				t.Fatalf("the coding op receives %+v, want %+v (dispatched %s)", in, tc.want, got)
			}
			// The model's own arguments survive the enrichment: a run that was
			// addressed and lost its repo is a run that does not happen.
			if strings.Contains(tc.args, `"repo":"api"`) && delivered(got).Repo != "api" {
				t.Fatalf("the model's arguments did not survive: %s", got)
			}
			// An untouched call is returned byte for byte, never re-encoded.
			if tc.want == (client.Reply{}) && got != tc.args {
				t.Fatalf("a call with no address to give was rewritten: %s", got)
			}
		})
	}
}

// The seam, through the real tool loop: a turn parks its address, the model asks
// for a coding run, and what reaches the tool plane carries the channel the
// question was asked in. This is the whole path dispatchOne is responsible for.
func TestADispatchCarriesTheTurnsAddress(t *testing.T) {
	plane := &fakePlane{offer: []infer.ToolDef{toolDef("agent")}, out: `{"sessionId":"sess_1","branch":"agent/abc"}`}
	withPlane(t, plane)
	ai := &scriptAI{replies: []infer.ChatResponse{
		{ToolCalls: []infer.ToolCall{{ID: "c1", Name: "agent",
			Arguments: `{"op":"create_agent_coding","input":{"repo":"api","prompt":"fix the auth test"}}`}},
			FinishReason: "tool_calls"},
		{Content: "Started — I will push to agent/abc."},
	}}

	a := mk("acme", "hanzo")
	a.Tools = []string{"agent"}
	ctx := withReply(context.Background(), thread, client.Origin{})
	r := executeRun(ctx, ai, "acme", "acme/u1", a, "fix the auth test in api", nil, "", "run_test")

	if r.Status != "ok" {
		t.Fatalf("want ok, got %q err=%q", r.Status, r.Error)
	}
	if len(plane.calls) != 1 {
		t.Fatalf("want exactly one dispatch, got %d (%+v)", len(plane.calls), plane.calls)
	}
	in := delivered(plane.calls[0].args)
	if in.Reply != thread {
		t.Fatalf("the run was dispatched with %+v, want the turn's own address %+v", in.Reply, thread)
	}
	if in.Repo != "api" || in.Prompt != "fix the auth test" {
		t.Fatalf("the model's own arguments did not reach the op: %+v", in)
	}
}

// A write to the board is given the THREAD: the one the question is in, or the
// thread a question at the top of a channel opens. It is given no turn — the run
// a hand-off starts narrates on its own — and a model's own channel is written over.
func TestABoardWriteIsGivenTheThread(t *testing.T) {
	top := client.Origin{Message: "1726459300.000100"}
	for name, tc := range map[string]struct {
		to   client.Reply
		from client.Origin
		tool string
		args string
		want client.Reply
	}{
		"filed in a thread": {to: thread, tool: "task",
			args: `{"op":"` + client.Phrase(fileOp) + `","input":{"key":"api","title":"x",` + spoof + `}}`,
			want: thread},
		"filed at the top of a channel: the thread it opens": {to: channel, from: top, tool: "task",
			args: `{"op":"` + fileOp + `","input":{"key":"api","title":"x"}}`,
			want: client.Reply{Provider: "slack", Channel: channel.Channel, Thread: top.Message}},
		"an edit, flat": {to: thread, tool: editOp,
			args: `{"key":"api","num":7,"assignee":"agent"}`,
			want: thread},
		"a read of the board is not a write": {to: thread, tool: "task",
			args: `{"op":"get_task_board","input":{}}`,
			want: client.Reply{}},
	} {
		t.Run(name, func(t *testing.T) {
			sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
				// Inside a live turn, which is where a chat turn's calls are made: a
				// coding call would be given the turn, and a board write must not be.
				l, _ := following(t)
				ctx := withReply(withLive(context.Background(), l), tc.to, tc.from)
				got := address(ctx, tc.tool, tc.args)
				if in := delivered(got).Reply; in != tc.want {
					t.Fatalf("the board op receives %+v, want %+v (dispatched %s)", in, tc.want, got)
				}

			})
		})
	}
}
