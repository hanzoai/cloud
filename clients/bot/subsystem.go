// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// subsystem.go — /v1/bot, the node control plane in the cloud binary.
//
//	GET  /v1/bot/connect            the socket a node dials and holds open
//	GET  /v1/bot/nodes              this org's connected nodes
//	POST /v1/bot/nodes/{id}/invoke  ask one of them to run a command
//	POST /v1/bot/peer/invoke        replica-to-replica forward (machine hop)
//
// # The org is the gateway's verdict, never the caller's
//
// Every route here takes its org from X-Org-Id, which the gateway injects after
// validating IAM and after stripping any client copy. It is never read from a
// body, a query, or a path — a caller that could name an org could name someone
// else's. principal.Org returns nothing without a validated principal, so the
// direct-to-pod path (where a forged X-Org-Id is restored but no user is) is
// refused rather than defaulted.
//
// The one exception is the peer hop, whose org DOES arrive in a body: it is a
// machine call from another replica of this binary, which derived that org from
// a validated header, and it authenticates with a shared token instead of a
// user. See PeerHandler.
//
// # Where authorization happens
//
// Once, at the socket. The registry runs the gate on the replica that holds the
// node — the only one that knows what that node declared it can do — so a
// locally-held node and one reached through a forward are authorized by the same
// code with the same session in hand. This file supplies that gate (policy.Check
// over the deployment's Mode); it does not re-check before invoking, because a
// second check in a place that sometimes has the session and sometimes does not
// is how the two answers drift apart.
package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	kv "github.com/hanzokv/go/v9"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Deployment knobs.
const (
	// envKVURL is the cloud binary's Hanzo KV address. The bot presence map is
	// its first user: with more than one replica a node's socket lands on one pod
	// and its invocations land on any, so the pods need somewhere shared to say
	// which of them holds which node.
	envKVURL = "CLOUD_KV_URL"

	// envPeerToken authenticates one replica's forward to another. Without it the
	// peer endpoint serves nothing, because an unauthenticated endpoint that
	// takes an org from a body is a cross-tenant invoke primitive.
	envPeerToken = "CLOUD_BOT_PEER_TOKEN"

	// envAllow and envDeny are the operator's overlay on the per-platform command
	// defaults. Allow is the ONLY way a dangerous command (camera.snap,
	// screen.record, sms.send…) becomes reachable at all; Deny wins over both.
	envAllow = "CLOUD_BOT_ALLOW"
	envDeny  = "CLOUD_BOT_DENY"

	// The replica set, read from the same variables the shard router reads, so a
	// deployment describes its pods once.
	envPeers   = "CLOUD_PEERS"
	envPodName = "CLOUD_POD_NAME"
	envPodAlt  = "POD_NAME"
)

const (
	// defaultInvokeTimeout is what a caller that names none gets; maxInvokeTimeout
	// bounds the ones that do, so a single request cannot pin a node's socket (and
	// a peer's connection) for an unbounded stretch.
	defaultInvokeTimeout = 30 * time.Second
	maxInvokeTimeout     = 5 * time.Minute

	// maxNodeID bounds the {id} path param before it is looked up. An oversize id
	// is not a node this org has, so it answers exactly like an absent one.
	maxNodeID = 256
)

// state is the plane's one value: the registry. The deployment's policy is not
// here — it is bound into the registry's gate, where it is applied, and a second
// copy beside it would be a second answer to the same question.
type state struct {
	reg *Registry
}

// running holds the presence-renew loop so Shutdown can end it. It is package
// state because Shutdown is a package function (cloud.MountSpec.Shutdown), and
// there is one bot plane per process.
var running struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// Mount registers the /v1/bot surface per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("bot.Mount: nil app")
	}
	if deps.Logger == nil {
		return errors.New("bot.Mount: nil deps.Logger")
	}
	base := cloud.NewBase(deps, "bot")

	// Auth is AuthIAM as a statement of fact, not a setting: every route here
	// requires a principal the gateway validated against IAM, so there is no
	// deployment of this file in which an anonymous caller reaches a node.
	mode := Mode{Auth: AuthIAM, Allow: envList(envAllow), Deny: envList(envDeny)}

	opts := []Option{WithGate(gate(mode))}
	if c, ok := clusterFromEnv(base.Log); ok {
		opts = append(opts, WithCluster(c))
	}
	reg := NewRegistry(opts...)

	s := &cloud.Service[state]{Base: base, State: state{reg: reg}}
	routes(app, s, deps)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { reg.Run(ctx); close(done) }()
	running.mu.Lock()
	running.cancel, running.done = cancel, done
	running.mu.Unlock()

	base.Log.Info("bot node plane mounted", "brand", deps.Brand, "allow", len(mode.Allow), "deny", len(mode.Deny))
	return nil
}

// Shutdown ends the presence-renew loop, which releases this replica's claims on
// the way out so peers stop forwarding into a pod that is draining.
func Shutdown(ctx context.Context) error {
	running.mu.Lock()
	cancel, done := running.cancel, running.done
	running.cancel, running.done = nil, nil
	running.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

// routes registers the surface. Handlers are wrapped in cloud.Terminal because
// this subsystem mounts AFTER the commerce embed, whose /v1 error filter rewrites
// any error a downstream handler PROPAGATES into a 500 — which would turn every
// refusal here (the 403 that is the tenant boundary, the 404 that is a node in
// another org) into an indistinguishable server error.
func routes(app cloud.Router, s *cloud.Service[state], deps cloud.Deps) {
	// One transport for the process, not one per dial: it carries the uptime a
	// node reads out of the handshake, which is the process's, not the socket's.
	ws := NodeWS(s.State.reg, WSOptions{ServerVersion: deps.Version, Logger: s.Log})

	// The socket nodes dial. The upgrade itself is refused without a validated
	// principal: the transport reads the org off the request, and off-gateway that
	// header is a client's claim until a principal proves otherwise — which would
	// let anyone attach a node into any tenant.
	app.Get("/v1/bot/connect", cloud.Terminal(cloud.Handle(s, func(_ *cloud.Service[state], c *zip.Ctx) error {
		if _, ok := principal.Org(c); !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		return ws(c)
	})))

	app.Get("/v1/bot/nodes", cloud.Terminal(cloud.Handle(s, listNodes)))
	app.Post("/v1/bot/nodes/:id/invoke", cloud.Terminal(cloud.Handle(s, invokeNode)))

	// The machine hop. It carries no user identity and authenticates with its own
	// token, so it is deliberately outside the principal gate the routes above use.
	app.Post(PeerInvokePath, zip.AdaptNetHTTP(s.State.reg.PeerHandler()))
}

// ---------------------------------------------------------------------------
// GET /v1/bot/nodes
// ---------------------------------------------------------------------------

// nodeView is one connected node as an operator sees it. Everything in it is the
// node's own self-report — useful to show, never load-bearing: what a node may
// be ASKED to do is the allowlist and the declared commands, checked at the
// socket, not this list.
type nodeView struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName,omitempty"`
	Platform    string   `json:"platform,omitempty"`
	Version     string   `json:"version,omitempty"`
	Caps        []string `json:"caps"`
	Commands    []string `json:"commands"`
	ConnectedAt string   `json:"connectedAt"`
}

type nodesView struct {
	Nodes []nodeView `json:"nodes"`
}

// listNodes returns the caller org's connected nodes — and only that org's,
// because the org is half of every key in the table it reads.
func listNodes(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	sessions := s.State.reg.List(org)
	out := make([]nodeView, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, nodeView{
			ID:          sess.Key.NodeID,
			DisplayName: sess.DisplayName,
			Platform:    sess.Platform,
			Version:     sess.Version,
			Caps:        nonNil(sess.Caps),
			Commands:    nonNil(sess.Commands),
			ConnectedAt: sess.ConnectedAt.UTC().Format(time.RFC3339),
		})
	}
	// A map has no order; a list a human reads should have one.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return c.JSON(http.StatusOK, nodesView{Nodes: out})
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// ---------------------------------------------------------------------------
// POST /v1/bot/nodes/{id}/invoke
// ---------------------------------------------------------------------------

// invokeBody is one invocation as a caller writes it. There is no node id and no
// org in it: the node is the path, the org is the caller's identity, and a field
// for either would be a field someone could set to a stranger's.
type invokeBody struct {
	Command        string          `json:"command"`
	Params         json.RawMessage `json:"params,omitempty"`
	TimeoutMs      int             `json:"timeoutMs,omitempty"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
}

// invokeView is what the node answered. payload is the node's own JSON, passed
// through: cloud routes the call, it does not interpret the result.
type invokeView struct {
	OK      bool            `json:"ok"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Code    string          `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
}

// deniedView is a refusal, carrying the stable code a client switches on.
type deniedView struct {
	Error  string `json:"error"`
	Code   string `json:"code"`
	Reason string `json:"reason,omitempty"`
}

func invokeNode(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	nodeID := strings.TrimSpace(c.Param("id"))
	if nodeID == "" {
		return zip.ErrBadRequest("a node id is required")
	}
	if len(nodeID) > maxNodeID {
		return zip.ErrNotFound("no such node for this org")
	}
	var in invokeBody
	if err := c.Bind(&in); err != nil {
		return zip.ErrBadRequest("invalid invoke body")
	}
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return zip.ErrBadRequest("command is required")
	}

	key := NodeKey{Org: org, NodeID: nodeID}
	params := in.Params
	if command == CommandSystemRun {
		// system.run carries control fields (approved, approvalDecision) that a
		// caller must not be able to set for itself. Sanitizing re-derives them
		// from the approval record and drops whatever was claimed.
		//
		// The store is nil: no approval registry is wired yet, so an invocation
		// that CLAIMS an approval is refused (APPROVALS_UNAVAILABLE) while an
		// ordinary one is unaffected. Fail-closed in the direction that matters.
		var p SystemRunParams
		if len(params) > 0 && json.Unmarshal(params, &p) != nil {
			return zip.ErrBadRequest("invalid system.run params")
		}
		next, d := SanitizeSystemRun(key, p, callerOf(c), nil, time.Now())
		if !d.Allow {
			return c.JSON(http.StatusForbidden, deniedView{Error: "denied", Code: d.Code, Reason: d.Reason})
		}
		b, err := json.Marshal(next)
		if err != nil {
			return zip.ErrInternal("bot: could not re-encode system.run params")
		}
		params = b
	}

	timeout := defaultInvokeTimeout
	if in.TimeoutMs > 0 {
		timeout = time.Duration(in.TimeoutMs) * time.Millisecond
	}
	if timeout > maxInvokeTimeout {
		timeout = maxInvokeTimeout
	}

	res, err := s.State.reg.Invoke(c.Context(), key,
		InvokeFrame(key, command, params, timeout, strings.TrimSpace(in.IdempotencyKey)), timeout)

	var denied *Denied
	switch {
	case err == nil:
		return c.JSON(http.StatusOK, invokeView{OK: res.OK, Payload: payloadJSON(res.Payload), Code: res.Code, Message: res.Message})
	case errors.As(err, &denied):
		return c.JSON(http.StatusForbidden, deniedView{Error: "denied", Code: denied.Code, Reason: denied.Message})
	case errors.Is(err, ErrNoSuchNode):
		// The same answer a node in another tenant gets, which is the point.
		return zip.ErrNotFound("no such node for this org")
	case errors.Is(err, ErrInvokeTimeout):
		return zip.Errorf(http.StatusGatewayTimeout, "bot: the node did not answer within %s", timeout)
	case errors.Is(err, ErrNodeGone):
		return zip.Errorf(http.StatusBadGateway, "bot: the node disconnected before it answered")
	default:
		s.Log.Warn("bot: invoke failed", "org", org, "node", nodeID, "command", command, "err", err)
		return zip.Errorf(http.StatusBadGateway, "bot: the node could not be reached")
	}
}

// payloadJSON passes a node's answer through only if it IS JSON. A node that
// answered with something else would otherwise corrupt this response body; the
// empty payload says "nothing usable came back", which the ok/code fields
// already qualify.
func payloadJSON(b []byte) json.RawMessage {
	if len(b) == 0 || !json.Valid(b) {
		return nil
	}
	return json.RawMessage(b)
}

// callerOf is who asked, as the approval rules bind it.
//
// Scopes are deliberately EMPTY. They would only ever widen what a caller may
// do — holding operator.approvals is what lets one pre-approve its own
// system.run without a record — and cloud's identity boundary mints no scope
// header, so the only way to fill them would be to read one a client can write.
// A caller that could name its own scopes could grant itself the exact
// permission the approval system exists to withhold.
func callerOf(c *zip.Ctx) Caller {
	return Caller{DeviceID: strings.TrimSpace(c.Header("X-Device-Id"))}
}

// ---------------------------------------------------------------------------
// The gate
// ---------------------------------------------------------------------------

// gate is the deployment's policy, bound once and run at every socket.
func gate(mode Mode) Gate {
	return func(s *Session, command string, params json.RawMessage) error {
		if d := Check(s, command, argvOf(command, params), mode); !d.Allow {
			return &Denied{Code: d.Code, Message: d.Reason}
		}
		return nil
	}
}

// argvOf is the argv Check consults for the host-exec commands where the argv IS
// the command being run. For everything else there is none, and Check ignores it.
func argvOf(command string, params json.RawMessage) []string {
	if command != CommandSystemRun || len(params) == 0 {
		return nil
	}
	var p SystemRunParams
	if json.Unmarshal(params, &p) != nil {
		return nil
	}
	argv, _, d := ResolveSystemRunCommand(p.Command, p.RawCommand)
	if !d.Allow {
		return nil
	}
	return argv
}

// ---------------------------------------------------------------------------
// Cluster wiring
// ---------------------------------------------------------------------------

// clusterFromEnv wires cross-replica routing when the deployment has more than
// one replica AND the two things that make a forward possible are configured.
//
// It is capability-detected rather than switched on: a single-pod deployment
// needs none of it and gets the local registry, which is the same code minus a
// lookup. A MULTI-pod deployment that is missing a piece is the dangerous case —
// nodes stay reachable only from the replica they attached to, and nothing
// visibly breaks — so that combination logs loudly instead of passing quietly.
func clusterFromEnv(log luxlog.Logger) (Cluster, bool) {
	peers := parsePeerAddrs(os.Getenv(envPeers))
	self := strings.TrimSpace(os.Getenv(envPodName))
	if self == "" {
		self = strings.TrimSpace(os.Getenv(envPodAlt))
	}
	if len(peers) < 2 || self == "" || peers[self] == "" {
		return Cluster{}, false // one pod, or a pod outside its own ring
	}

	url := strings.TrimSpace(os.Getenv(envKVURL))
	token := strings.TrimSpace(os.Getenv(envPeerToken))
	if url == "" || token == "" {
		log.Warn("bot: several replicas but no cross-replica routing; each node is reachable ONLY from the replica it attached to",
			"replicas", len(peers), "missing", missing(url == "", envKVURL, token == "", envPeerToken))
		return Cluster{}, false
	}
	opt, err := kv.ParseURL(url)
	if err != nil {
		log.Warn("bot: unusable KV address; each node is reachable ONLY from the replica it attached to",
			"env", envKVURL, "err", err)
		return Cluster{}, false
	}

	log.Info("bot: cross-replica routing on", "replica", self, "replicas", len(peers))
	return Cluster{
		Replica:  self,
		Presence: NewKVPresence(kv.NewClient(opt)),
		Hop: NewHTTPHop(func(id string) (string, bool) {
			addr, ok := peers[id]
			return addr, ok
		}, token),
		PeerToken: token,
		Logger:    log,
	}, true
}

// parsePeerAddrs reads CLOUD_PEERS ("id@addr,id2@addr2", or a bare "id" whose id
// is its own address) into id -> addr. Same value the shard router elects over,
// so the deployment names its pods once.
func parsePeerAddrs(v string) map[string]string {
	out := map[string]string{}
	for _, part := range splitList(v) {
		id, addr, ok := strings.Cut(part, "@")
		if !ok {
			id, addr = part, part
		}
		id, addr = strings.TrimSpace(id), strings.TrimSpace(addr)
		if id != "" && addr != "" {
			out[id] = addr
		}
	}
	return out
}

// missing names the unset variables, so a warning says what to fix.
func missing(aUnset bool, a string, bUnset bool, b string) string {
	var names []string
	if aUnset {
		names = append(names, a)
	}
	if bUnset {
		names = append(names, b)
	}
	return strings.Join(names, ", ")
}

func envList(name string) []string { return splitList(os.Getenv(name)) }

func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
