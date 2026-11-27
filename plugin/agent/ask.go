package main

// ask.go is the MCP server a coding run's harness asks its person through.
//
//	POST /v1/agent/ask
//
// It serves ONE tool, ask_user, to ONE caller: the run whose ticket rides the
// request (coding/key.go). The harness is told about it in its own MCP config
// (coding argvFor), calls it when a decision is its person's, and is handed the
// answer as the tool's result. The question lands in the run's own thread and
// only the run's own person can answer it (agents/ask.go).
//
// It is a server of its own, rather than the fleet's composed MCP server, because
// that one lists every tool the org has and authenticates an org's credential,
// and a box executing a model's output should hold neither. Here the ticket is
// the whole of the authority and ask_user the whole of the surface: a call with
// no ticket, a finished run's ticket, or any other tool is refused.
//
// THE CALL WAITS FOR A PERSON, up to half an hour, so the answer to a tools/call
// is an event stream: a comment every keepaliveEvery holds the connection open
// through every proxy on the way, and the one event carries the result. A client
// that cannot read a stream is answered in one JSON body when the wait is over.
//
// It lives at the composition root for the reason coding.go gives: the ticket is
// the coding engine's and the question is the agents app's.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/apps/coding"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// keepaliveEvery is the gap between two comments on a waiting call's stream,
// well inside the idle limit of any proxy between the sandbox and here.
var keepaliveEvery = 15 * time.Second

// holds and askFor are the coding engine's ticket check and ask, as values so a
// test can stand in for a run.
var (
	holds  = coding.Holds
	askFor = coding.Ask
)

func init() {
	spec.Describe("/v1/agent/ask", http.MethodPost,
		"The MCP server a coding run's harness asks its person through.",
		"Speaks MCP over streamable HTTP and serves one tool, ask_user: a question and 2 to 8 "+
			"options, shown as buttons in the run's thread, answered by the person the run is for. "+
			"The call waits for the answer, up to 30 minutes, streaming as text/event-stream with a "+
			"comment every 15 seconds, and returns it as the tool's result, or says none came.\n\n"+
			"The caller is a coding run, not a tenant: the request carries the run's ticket in "+
			"X-Hanzo-Run and no bearer. The ticket is minted for one run, opens this server and "+
			"nothing else, names the run the question belongs to, and ends with the run. A request "+
			"without a live ticket is 401, and any tool but ask_user is refused.")
}

// message is one JSON-RPC message from the harness.
type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// askServer answers one MCP message for the run the ticket names.
func askServer(c *zip.Ctx) error {
	ticket := strings.Clone(c.Header(coding.TicketHeader))
	switch ok, err := holds(ticket); {
	case err != nil:
		return c.JSON(http.StatusServiceUnavailable, rpcError(nil, -32001, "the run this ticket belongs to could not be looked for; ask again"))
	case !ok:
		c.SetHeader("WWW-Authenticate", `Ticket realm="run"`)
		return c.JSON(http.StatusUnauthorized, rpcError(nil, -32001, "this server answers a coding run's ticket, and none rode this request"))
	}
	var m message
	if err := json.Unmarshal(c.Body(), &m); err != nil {
		return c.JSON(http.StatusBadRequest, rpcError(nil, -32700, "not a JSON-RPC message"))
	}
	if len(m.ID) == 0 {
		return c.NoContent(http.StatusAccepted) // a notification: nothing to answer
	}
	id := append(json.RawMessage(nil), m.ID...)
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(m.Params, &p)
		return c.JSON(http.StatusOK, rpcResult(id, map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "run", "version": "1"},
		}))
	case "ping":
		return c.JSON(http.StatusOK, rpcResult(id, map[string]any{}))
	case "tools/list":
		t := agents.AskTool
		return c.JSON(http.StatusOK, rpcResult(id, map[string]any{"tools": []map[string]any{
			{"name": t.Name, "description": t.Description, "inputSchema": t.Schema},
		}}))
	case "tools/call":
	default:
		return c.JSON(http.StatusOK, rpcError(id, -32601, "this server has tools/list and tools/call, not "+m.Method))
	}

	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	var in client.AskIn
	if err := json.Unmarshal(m.Params, &call); err != nil {
		return c.JSON(http.StatusOK, rpcError(id, -32602, "tools/call needs a name and arguments"))
	}
	if call.Name != agents.AskTool.Name {
		return c.JSON(http.StatusOK, toolResult(id, "this server has one tool, "+agents.AskTool.Name+", not "+call.Name, true))
	}
	if err := json.Unmarshal(call.Arguments, &in); err != nil {
		return c.JSON(http.StatusOK, toolResult(id, "ask_user's arguments are {question, options}: "+err.Error(), true))
	}

	if !strings.Contains(c.Header("Accept"), "text/event-stream") {
		text, err := asked(context.Background(), ticket, in)
		return c.JSON(http.StatusOK, toolResult(id, text, err != nil))
	}
	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-cache")
	c.SetHeader("X-Accel-Buffering", "no")
	return c.SendStreamWriter(func(w *bufio.Writer) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		type result struct {
			text string
			err  error
		}
		done := make(chan result, 1)
		go func() {
			text, err := asked(ctx, ticket, in)
			done <- result{text, err}
		}()
		beat := time.NewTicker(keepaliveEvery)
		defer beat.Stop()
		for {
			select {
			case r := <-done:
				b, _ := json.Marshal(toolResult(id, r.text, r.err != nil))
				_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
				_ = w.Flush()
				return
			case <-beat.C:
				if _, err := w.WriteString(": waiting\n\n"); err != nil || w.Flush() != nil {
					return // the harness went away: cancel closes its question
				}
			}
		}
	})
}

// asked runs the ask and says what the harness is told when it could not be made.
func asked(ctx context.Context, ticket string, in client.AskIn) (string, error) {
	text, err := askFor(ctx, ticket, in)
	if errors.Is(err, coding.ErrNoTicket) {
		return "The run this question belonged to is over.", err
	}
	if err != nil {
		return err.Error(), err
	}
	return text, nil
}

func rpcResult(id json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func rpcError(id json.RawMessage, code int, msg string) map[string]any {
	if id == nil {
		id = json.RawMessage("null")
	}
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}}
}

// toolResult is a tools/call result: text, and whether it reports a failure.
func toolResult(id json.RawMessage, text string, failed bool) map[string]any {
	return rpcResult(id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": failed,
	})
}
