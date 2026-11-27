package tools

// relay.go — an agent run reaches its org's MCP servers through this plane.
//
// A coding run's harness executes a model's output in a sandbox its person can
// open a terminal on, so nothing it holds is secret from them — and an org's
// server credential is the org's, not theirs. So the harness is never handed a
// server's address or its credential. It is handed the run's own MCP address
// (the agent app's /v1/agent/mcp/<server>, which opens to the run's ticket and to
// the servers in the run's kit), and each message it sends there arrives here
// over the plane, acting for the run's org. This is where the credential is read
// from KMS and injected, and where the server is dialled — through the same
// SSRF-guarded client every other call to an org's server takes (rpc), so the
// sandbox never reaches the server at all.
//
// It answers only what a run carries (kit.go): a server an admin registered, and
// of it only the tools an admin activated. A call goes through the registry's one
// dispatch (DispatchFrom), so activation, a price and its settlement are asked
// exactly as for POST /v1/tool/call, and a name a first-party source wins is not
// run here.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// serveRelay publishes the op. Mount calls it once the stores are open.
func serveRelay() {
	cloud.Plane().Post("/tools/relay", planeRelay,
		zip.WithOperationID(client.ToolsRelay),
		zip.WithSummary("Carry one MCP request of an agent run to one of its org's servers, with the server's credential added here"))
}

// planeRelay answers one MCP request an agent run of the org on the call makes
// of one of its servers: tools/list, the tools of it the run may call, or
// tools/call, one of them called.
func planeRelay(ctx context.Context, in *client.RelayIn) (*client.Relayed, error) {
	s := mounted
	org, ok := cloud.Tenant(ctx)
	if !ok {
		relayed(s, ctx, "", strings.TrimSpace(in.Server), "", in.Method, in.Session, "deny", http.StatusForbidden, "no org")
		return nil, zip.ErrForbidden("tools relay: the call acts for no org")
	}
	if s == nil || s.State.servers == nil || s.State.activation == nil || s.State.mcp == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "tools relay: the tool plane is not open")
	}
	srvID := strings.TrimSpace(in.Server)
	srv, err := s.State.servers.Get(ctx, org, srvID)
	switch {
	case errors.Is(err, ErrUnknownTool):
		relayed(s, ctx, org, srvID, srvID, in.Method, in.Session, "deny", http.StatusNotFound, "unknown MCP server")
		return nil, zip.ErrNotFound("tools relay: the org has no such MCP server")
	case err != nil:
		relayed(s, ctx, org, srvID, srvID, in.Method, in.Session, "deny", http.StatusInternalServerError, "server lookup error")
		return nil, zip.Errorf(http.StatusInternalServerError, "tools relay: %v", err)
	case !srv.Admitted:
		relayed(s, ctx, org, srv.ID, srv.URL, in.Method, in.Session, "deny", http.StatusForbidden, "server not admitted by org admin")
		return nil, zip.ErrForbidden("tools relay: no admin of the org registered this server, so a run does not carry it")
	}
	who := cloud.Who(ctx)
	muted, err := s.State.activation.Muted(ctx, org, who.User)
	if err != nil {
		relayed(s, ctx, org, srv.ID, srv.URL, in.Method, in.Session, "deny", http.StatusInternalServerError, "mute lookup error")
		return nil, zip.Errorf(http.StatusInternalServerError, "tools relay: %v", err)
	}
	if slices.Contains(muted, srv.ID) {
		relayed(s, ctx, org, srv.ID, srv.URL, in.Method, in.Session, "deny", http.StatusForbidden, "server muted by run user")
		return nil, zip.ErrForbidden("tools relay: the run's person muted " + srv.ID)
	}
	admitted, err := s.State.activation.Admitted(ctx, org, "")
	if err != nil {
		relayed(s, ctx, org, srv.ID, srv.URL, in.Method, in.Session, "deny", http.StatusInternalServerError, "admitted lookup error")
		return nil, zip.Errorf(http.StatusInternalServerError, "tools relay: %v", err)
	}
	admitted = slices.DeleteFunc(admitted, func(name string) bool { return slices.Contains(muted, name) })
	allowed := served(admitted, srv.ID)

	switch in.Method {
	case "tools/list":
		remote, err := s.State.mcp.tools(ctx, srv)
		if err != nil {
			relayed(s, ctx, org, srv.ID, srv.URL, in.Method, in.Session, "deny", http.StatusBadGateway, "upstream list failed")
			return nil, zip.Errorf(http.StatusBadGateway, "tools relay: %s did not list its tools: %v", srv.ID, err)
		}
		keep := []remoteTool{}
		for _, t := range remote {
			if slices.Contains(allowed, t.Name) {
				keep = append(keep, t)
			}
		}
		relayed(s, ctx, org, srv.ID, srv.URL, in.Method, in.Session, "allow", http.StatusOK, "called by an agent run")
		b, _ := json.Marshal(map[string]any{"tools": keep})
		return &client.Relayed{Result: b}, nil
	case "tools/call":
	default:
		relayed(s, ctx, org, srv.ID, srv.URL, in.Method, in.Session, "deny", http.StatusBadRequest, "unsupported method")
		return nil, zip.ErrBadRequest("tools relay: a run's harness asks tools/list and tools/call, not " + in.Method)
	}

	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(in.Params, &call); err != nil {
		relayed(s, ctx, org, srv.ID, srv.URL, in.Method, in.Session, "deny", http.StatusBadRequest, "malformed tools/call params")
		return nil, zip.ErrBadRequest("tools relay: tools/call takes a name and arguments")
	}
	name := srv.ID + "_" + call.Name
	if !slices.Contains(allowed, call.Name) {
		reason := "tool not activated by org admin"
		if slices.Contains(muted, name) {
			reason = "tool muted by run user"
		}
		relayed(s, ctx, org, name, srv.URL, in.Method, in.Session, "deny", http.StatusForbidden, reason)
		return nil, zip.ErrForbidden("tools relay: " + srv.ID + "'s " + call.Name + " is not admitted for this run")
	}
	out, err := Default().DispatchFrom(ctx, Principal{Org: org, User: who.User}, SourceMCP, name, call.Arguments)
	status, decision, reason := http.StatusOK, "allow", "called by an agent run"
	switch {
	case err == nil:
	case errors.Is(err, ErrUnknownTool):
		status, decision, reason, err = http.StatusNotFound, "deny", "tool not offered by upstream", zip.ErrNotFound("tools relay: "+srv.ID+" does not offer "+call.Name)
	case errors.Is(err, ErrNotActivated):
		status, decision, reason, err = http.StatusForbidden, "deny", "tool not activated", zip.ErrForbidden("tools relay: "+name+" is not activated")
	case errors.Is(err, ErrPaymentRequired), errors.Is(err, ErrChargerUnset):
		status, decision, reason, err = http.StatusPaymentRequired, "deny", "payment required", zip.Errorf(http.StatusPaymentRequired, "tools relay: %s is for sale, and a run has no way to pay for it", name)
	default:
		status, decision, reason, err = http.StatusBadGateway, "deny", "upstream call failed", zip.Errorf(http.StatusBadGateway, "tools relay: %s: %v", name, err)
	}
	relayed(s, ctx, org, name, srv.URL, in.Method, in.Session, decision, status, reason)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "tools relay: %s answered something that is not JSON", name)
	}
	return &client.Relayed{Result: b}, nil
}

// relayed writes every permitted or denied MCP relay request to the audit trail
// with destination host, policy decision ("allow" or "deny"), run session ID,
// and a deterministically redacted actor identity.
func relayed(s *cloud.Service[state], ctx context.Context, org, name, rawURL, method, session, decision string, status int, reason string) {
	if s == nil || s.State.audit == nil {
		return
	}
	who := cloud.Who(ctx)
	reqID := strings.TrimSpace(session)
	if reqID == "" {
		reqID = who.RequestID
	}
	dest := destinationHost(rawURL)
	if dest == "" {
		dest = name
	}
	if _, err := s.State.audit.Append(ctx, audit.Record{
		Actor:     audit.Actor{Org: org, Sub: redactActorSub(who.User)},
		Action:    "tools.relay",
		Resource:  audit.Resource{Type: "tools", ID: name},
		Auth:      audit.AuthContext{Method: "run", IsAdmin: who.Admin},
		Outcome:   audit.Outcome{Result: decision, Status: status, Reason: reason},
		Method:    method,
		Path:      dest,
		RequestID: reqID,
	}); err != nil {
		s.Log.Warn("audit append failed", "err", err)
	}
}

func redactActorSub(sub string) string {
	sub = strings.TrimSpace(sub)
	if sub == "" {
		return "anonymous"
	}
	sum := sha256.Sum256([]byte(sub))
	return "users/" + hex.EncodeToString(sum[:6])
}

func destinationHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}
