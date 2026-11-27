package main

// relay.go is the MCP address a coding run's harness reaches one of its org's
// MCP servers through.
//
//	POST /v1/agent/mcp/:server
//
// The harness is never handed a server's own address or its credential: the run's
// person can open a terminal on its sandbox, and the credential is the org's. It
// is handed this address for each server its kit carries (coding kit.go), with the
// run's ticket as the header, exactly as it is handed the ask server (ask.go). The
// ticket and the server's id are the whole of the authority: a request without a
// live ticket is 401, and a server the run's kit did not carry is 403. The rest
// is the tool app's (apps/tools relay.go), asked over the plane as the run's
// person in the run's org — which reads the credential from KMS, adds it, dials
// the server through its SSRF-guarded client, and answers only the tools of it an
// admin of the org activated.
//
// It speaks streamable HTTP, answering every request in one JSON body.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/apps/coding"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// carried and relayFor are the coding engine's check and relay, as values so a
// test can stand in for a run.
var (
	carried  = coding.Carried
	relayFor = coding.Relay
)

func init() {
	spec.Describe("/v1/agent/mcp/:server", http.MethodPost,
		"The MCP address a coding run's harness reaches one of its org's MCP servers through.",
		"Speaks MCP over streamable HTTP for one of the org's MCP servers, named by its id: "+
			"tools/list answers the tools of it an admin of the org activated, and tools/call calls one. "+
			"The server's address and credential never reach the run: the tool plane adds the credential "+
			"and dials the server.\n\n"+
			"The caller is a coding run, not a tenant: the request carries the run's ticket in X-Hanzo-Run "+
			"and no bearer. A request without a live ticket is 401, and a server the run's kit did not "+
			"carry when it started is 403. A run has at most four requests in flight here and twenty "+
			"in a burst, then one a second; a request past that is 429 with Retry-After.")
}

// relayServer answers one MCP message for the server the path names, for the
// run the ticket names.
func relayServer(c *zip.Ctx) error {
	ticket := strings.Clone(c.Header(coding.TicketHeader))
	server := strings.Clone(c.Param("server"))
	switch err := carried(ticket, server); {
	case errors.Is(err, coding.ErrNoTicket):
		c.SetHeader("WWW-Authenticate", `Ticket realm="run"`)
		return c.JSON(http.StatusUnauthorized, rpcError(nil, -32001, "this server answers a coding run's ticket, and none rode this request"))
	case errors.Is(err, coding.ErrPods):
		return c.JSON(http.StatusServiceUnavailable, rpcError(nil, -32001, "the run this ticket belongs to could not be looked for; ask again"))
	case err != nil:
		return c.JSON(http.StatusForbidden, rpcError(nil, -32001, "this run's ticket does not open "+server))
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
			"serverInfo":      map[string]any{"name": server, "version": "1"},
		}))
	case "ping":
		return c.JSON(http.StatusOK, rpcResult(id, map[string]any{}))
	case "tools/list", "tools/call":
	default:
		return c.JSON(http.StatusOK, rpcError(id, -32601, "this server has tools/list and tools/call, not "+m.Method))
	}
	params := append(json.RawMessage(nil), m.Params...)
	out, err := relayFor(context.Background(), ticket, server, m.Method, params)
	switch {
	case errors.Is(err, coding.ErrNoTicket):
		return c.JSON(http.StatusUnauthorized, rpcError(id, -32001, "the run this ticket belonged to is over"))
	case errors.Is(err, coding.ErrNotCarried):
		return c.JSON(http.StatusForbidden, rpcError(id, -32001, "this run's ticket does not open "+server))
	case errors.Is(err, coding.ErrPods):
		return c.JSON(http.StatusServiceUnavailable, rpcError(id, -32001, "the run this ticket belongs to could not be looked for; ask again"))
	case errors.Is(err, coding.ErrRelayLimit):
		c.SetHeader("Retry-After", "1")
		return c.JSON(http.StatusTooManyRequests, rpcError(id, -32000, err.Error()))
	case err != nil:
		return c.JSON(http.StatusOK, rpcError(id, -32603, relayFailure(err)))
	}
	return c.JSON(http.StatusOK, rpcResult(id, json.RawMessage(out)))
}

// relayFailure is what the harness is told when a relayed request failed. The
// tool app's own answer is said as it said it — it names the server by its id
// and never by its address. Anything else is a transport that failed on the way,
// whose text may spell an address, a socket or a query, and the harness runs in
// a sandbox its person can open: it is told only that the server was not reached.
func relayFailure(err error) string {
	if he, ok := errors.AsType[*zip.HTTPError](err); ok {
		return he.Msg
	}
	return "upstream unreachable"
}
