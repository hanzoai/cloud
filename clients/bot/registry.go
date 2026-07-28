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

// The node registry: which bot nodes are connected, and how to reach one.
//
// A bot node runs on someone's machine and dials in, holding a long-lived
// socket. That socket is the only way to reach it — you cannot spawn a
// rendezvous per request, because the node is already attached to one. This is
// that rendezvous.
//
// # Org is part of a node's identity, not a lookup filter
//
// The TypeScript registry this replaces keyed nodes by id alone
// (nodesById: Map<string, NodeSession>), with no tenant dimension anywhere in
// the type. Isolation was then a property of deployment — one shared singleton,
// per-viewer bearers, careful caching — rather than of the data structure.
//
// Here a node is addressed by (org, nodeID). Two orgs may use the same node id
// and never see each other, and a lookup without an org cannot compile. That is
// the difference between multi-tenant and single-tenant-with-care.
//
// # One registry, however many replicas
//
// A node holds ONE socket to ONE replica — whichever one the load balancer gave
// it. An HTTP invoke lands on ANY replica, so the replica serving the invoke is
// usually not the one holding the socket, and a fleet of N replicas would answer
// 1/N of its own invocations. Two things close that, and they are part of THIS
// registry rather than a second kind of registry beside it:
//
//   - a presence map, (org, node) -> replica, claimed when a node registers and
//     carrying a TTL, so a replica that dies stops advertising sockets it no
//     longer has instead of stranding its nodes forever; and
//   - a forward: a replica that does not hold the socket asks the one that does
//     and relays its answer.
//
// Both are OPTIONAL wiring (WithCluster), not a second type: a registry built
// without them is exactly the single-replica registry, and every local path runs
// the same code either way. Org is in the presence key for the same reason it is
// in NodeKey — a lookup by an org that does not own the node simply misses,
// which is the identical miss a node that does not exist produces. A presence map
// keyed by node id alone (bot:nodes:<nodeId>, one flat namespace, which is what
// this replaces) answers a foreign org's lookup with the real owner's replica id
// and then forwards the invocation there: the tenant boundary would exist in the
// socket layer and nowhere in the routing layer.
package bot

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	kv "github.com/hanzokv/go/v9"
	luxlog "github.com/luxfi/log"
)

// Errors a caller is expected to handle.
var (
	// ErrNoSuchNode means no node with that id is connected FOR THAT ORG. It is
	// deliberately indistinguishable from "exists but belongs to another org":
	// telling those apart would leak the existence of another tenant's nodes.
	ErrNoSuchNode = errors.New("bot: no such node")

	// ErrInvokeTimeout means the node held the socket but did not answer.
	ErrInvokeTimeout = errors.New("bot: node invoke timed out")

	// ErrNodeGone means the socket closed while the call was in flight.
	ErrNodeGone = errors.New("bot: node disconnected mid-invoke")
)

// NodeKey addresses a node. Both fields are required; there is no way to name a
// node without naming its tenant.
type NodeKey struct {
	Org    string
	NodeID string
}

func (k NodeKey) String() string { return k.Org + "/" + k.NodeID }

// Session is a connected node.
type Session struct {
	Key         NodeKey
	ConnID      string
	DisplayName string
	Platform    string
	Version     string
	Caps        []string
	Commands    []string
	Permissions map[string]bool
	RemoteIP    string
	ConnectedAt time.Time

	// send delivers one frame to this node's socket. The transport owns
	// framing; the registry only needs a way to write.
	send func(context.Context, []byte) error
}

// InvokeResult is what a node answered.
type InvokeResult struct {
	OK      bool
	Payload []byte
	Code    string
	Message string
}

// Frame builds the wire frame for one invocation.
//
// corrID is minted by the replica that HOLDS the socket, and only by it: that
// replica's pending table is keyed by it, its disconnect sweep cancels by its
// prefix, and the node echoes it back on a socket where anything else is
// refused. An EMPTY corrID means "not minted yet" — the frame built with one is
// the frame that travels to the owning replica, which stamps its own id in
// before writing it.
type Frame func(corrID string) ([]byte, error)

// Gate authorizes one invocation, on the replica that holds the node's socket —
// the only replica that knows what that node declared it can do.
//
// It is called at the SINGLE point where a frame is about to be written to a
// socket, so no path into a node can skip it: a locally-held node and one
// reached through a peer forward pass the same gate, once, with the same
// session in hand. Returning a *Denied makes the refusal travel a hop intact, so
// a caller gets the same answer wherever the socket happens to be.
//
// A nil gate authorizes everything. That is what a bare plumbing registry wants
// (fixtures, the routing tests) and what a serving one must never have.
type Gate func(s *Session, command string, params json.RawMessage) error

// Denied is a gate refusal. It carries a stable code so the caller can answer
// with it, and it survives a peer forward.
type Denied struct {
	Code    string
	Message string
}

func (d *Denied) Error() string {
	if d.Message == "" {
		return "bot: " + d.Code
	}
	return "bot: " + d.Message
}

// Registry tracks connected nodes, correlates in-flight invocations, and — when
// wired for a cluster — routes to sockets held by another replica.
//
// It is transport-agnostic about the SOCKET on purpose: the WS layer registers a
// Session with a send func and feeds answers back by correlation id. Keeping the
// socket out of here is what lets the routing be tested without a network.
type Registry struct {
	mu       sync.RWMutex
	byKey    map[NodeKey]*Session
	byConn   map[string]NodeKey
	pending  map[string]chan InvokeResult
	nextCorr uint64

	gate Gate

	// Cluster wiring. All zero ⇒ single replica: no presence, no forward, and
	// every path below is the local one.
	replica  string
	presence PresenceStore
	hop      Hop
	ttl      time.Duration
	renew    time.Duration
	token    []byte
	log      luxlog.Logger

	// cfgErr is a half-wired cluster: presence with no way to forward, or a
	// replica with no id. It is returned by Register and Invoke rather than
	// swallowed, so a misconfiguration refuses to serve instead of quietly
	// degrading to one-replica reachability — which is the exact failure the
	// cluster wiring exists to remove, and the one nobody would notice.
	cfgErr error
}

// Option is construction-time wiring. There is one constructor; what a Registry
// can do is what it was given, and nothing is switched on afterwards.
type Option func(*Registry)

// Cluster is what one replica needs to reach sockets it does not hold.
type Cluster struct {
	// Replica is this replica's stable id (CLOUD_POD_NAME / the StatefulSet
	// ordinal). It is what a presence claim names and what a Hop resolves.
	Replica string

	Presence PresenceStore
	Hop      Hop

	// TTL is the presence lease; Renew defaults to a third of it.
	TTL   time.Duration
	Renew time.Duration

	// PeerToken authenticates a forward. Empty disables PeerHandler entirely —
	// see PeerHandler for why that is the only safe default.
	PeerToken string

	Logger luxlog.Logger
}

// WithCluster makes this registry reachable from its peers. Omitting it is the
// single-replica registry, unchanged.
func WithCluster(c Cluster) Option {
	return func(r *Registry) {
		switch {
		case strings.TrimSpace(c.Replica) == "":
			r.cfgErr = fmt.Errorf("bot: a clustered registry needs this replica's id")
		case c.Presence == nil || c.Hop == nil:
			r.cfgErr = fmt.Errorf("bot: a clustered registry needs both a presence store and a hop")
		}
		if r.cfgErr != nil {
			return
		}
		r.replica, r.presence, r.hop = c.Replica, c.Presence, c.Hop
		r.token = []byte(c.PeerToken)
		r.ttl = c.TTL
		if r.ttl <= 0 {
			r.ttl = DefaultPresenceTTL
		}
		r.renew = c.Renew
		if r.renew <= 0 {
			r.renew = r.ttl / 3
		}
		if r.renew <= 0 {
			r.renew = defaultPresenceRenew
		}
		if c.Logger != nil {
			r.log = c.Logger
		}
	}
}

// WithGate installs the authorization every invocation passes at the socket.
func WithGate(g Gate) Option {
	return func(r *Registry) { r.gate = g }
}

// NewRegistry builds the one registry. With no options it is a single replica
// holding its own sockets — which is the whole of what a one-pod deployment
// needs, and the substrate every clustered path runs on.
func NewRegistry(opts ...Option) *Registry {
	r := &Registry{
		byKey:   make(map[NodeKey]*Session),
		byConn:  make(map[string]NodeKey),
		pending: make(map[string]chan InvokeResult),
		log:     luxlog.NewNoOpLogger(),
	}
	for _, o := range opts {
		if o != nil {
			o(r)
		}
	}
	return r
}

// clustered reports whether this registry can reach another replica's sockets.
func (r *Registry) clustered() bool { return r.presence != nil && r.hop != nil }

// Register adds a connected node, and claims it in the presence map.
//
// A second connection for the same (org, node) replaces the first and closes
// nothing: the old socket is simply no longer addressable. A node that
// reconnects after a network blip would otherwise be unreachable behind a dead
// entry until a timeout expired.
//
// A failed presence claim does NOT fail the registration. The node IS connected
// here and invocations landing here work; only invocations landing elsewhere
// miss — which they read as ErrNoSuchNode, the same answer as not-connected, so
// a KV outage degrades reachability without leaking anything. Refusing the
// registration instead would disconnect an entire fleet every time the KV blips.
// The renew loop re-takes the claim, so it heals within one tick.
func (r *Registry) Register(s *Session) error {
	if r.cfgErr != nil {
		return r.cfgErr
	}
	if s == nil || s.Key.Org == "" || s.Key.NodeID == "" {
		return fmt.Errorf("bot: a session needs both an org and a node id")
	}
	if s.send == nil {
		return fmt.Errorf("bot: session %s has no send func", s.Key)
	}
	r.mu.Lock()
	if prev, ok := r.byKey[s.Key]; ok {
		delete(r.byConn, prev.ConnID)
	}
	r.byKey[s.Key] = s
	r.byConn[s.ConnID] = s.Key
	r.mu.Unlock()

	if !r.clustered() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), presenceOpTimeout)
	defer cancel()
	if err := r.presence.Claim(ctx, s.Key, r.replica, r.ttl); err != nil {
		r.log.Warn("bot: presence claim failed; node reachable only on this replica until the next renew",
			"node", s.Key.String(), "replica", r.replica, "err", err)
	}
	return nil
}

// Unregister removes a node by its connection id, fails every call still waiting
// on it, and releases its presence claim.
//
// A pending invoke whose node has gone will never be answered; leaving it to time
// out would hold the caller for the full timeout on a question that is already
// unanswerable.
//
// The claim is released only if THIS connection is still the one holding the
// node. A node that reconnected here on a new socket, or moved to another
// replica, must not have its live claim released by the late teardown of the
// socket it left.
func (r *Registry) Unregister(connID string) {
	r.mu.Lock()
	key, ok := r.byConn[connID]
	live := false
	if ok {
		delete(r.byConn, connID)
		if s, exists := r.byKey[key]; exists && s.ConnID == connID {
			delete(r.byKey, key)
			live = true
		}
	}
	var orphaned []chan InvokeResult
	for id, ch := range r.pending {
		if strings.HasPrefix(id, connID+corrSep) {
			orphaned = append(orphaned, ch)
			delete(r.pending, id)
		}
	}
	r.mu.Unlock()

	for _, ch := range orphaned {
		select {
		case ch <- InvokeResult{OK: false, Code: "node_gone", Message: ErrNodeGone.Error()}:
		default:
		}
		close(ch)
	}

	if !live || !r.clustered() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), presenceOpTimeout)
	defer cancel()
	if err := r.presence.Release(ctx, key, r.replica); err != nil {
		r.log.Warn("bot: presence release failed; the claim expires on its own",
			"node", key.String(), "replica", r.replica, "err", err)
	}
}

// List returns the nodes connected TO THIS REPLICA for one org, and only that
// org.
//
// It is deliberately not cluster-wide: presence answers "which replica holds
// node X", which is what routing needs, and a fleet-wide roster is a different
// question (a scan of bot:node:<org>:*, exact because the org is its own key
// segment) with a different value shape.
func (r *Registry) List(org string) []*Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Session, 0, len(r.byKey))
	for k, s := range r.byKey {
		if k.Org == org {
			out = append(out, s)
		}
	}
	return out
}

// Invoke sends a command to a node, wherever in the cluster it is attached, and
// waits for its answer.
//
// The org comes from the caller's validated identity, never from the request
// body, so a caller cannot reach another tenant's node by naming it.
//
// The order — local first, presence second — keeps the single-replica path free
// of any KV round trip on the common case, and makes the clustered path the same
// code plus one lookup.
func (r *Registry) Invoke(ctx context.Context, key NodeKey, frame Frame, timeout time.Duration) (InvokeResult, error) {
	if r.cfgErr != nil {
		return InvokeResult{}, r.cfgErr
	}
	res, err := r.invokeLocal(ctx, key, frame, timeout)
	if !errors.Is(err, ErrNoSuchNode) || !r.clustered() {
		return res, err
	}

	replica, lookupErr := r.presence.Lookup(ctx, key)
	switch {
	case lookupErr == nil && replica != "" && replica != r.replica:
		// What crosses the hop is the frame with NO correlation id: only the
		// replica holding the socket may mint one, and it stamps its own in.
		req, ferr := frame("")
		if ferr != nil {
			return InvokeResult{}, ferr
		}
		return r.hop.Forward(ctx, replica, key, req, timeout)

	case lookupErr != nil && !errors.Is(lookupErr, ErrNoSuchNode):
		// A KV that is down and a claim that never existed are different
		// operational facts and the SAME fact to the caller: from here, that node
		// cannot be reached. The reason goes to the log, never to the answer — an
		// answer that varied with the reason would be an oracle.
		r.log.Warn("bot: presence lookup failed; treating the node as unreachable",
			"node", key.String(), "err", lookupErr)
	}

	// Also the replica == self case: presence says we hold a socket we do not.
	// Our own claim is stale, and the honest answer is that the node is gone.
	return InvokeResult{}, ErrNoSuchNode
}

// corrSep separates a correlation id's connection prefix from its counter. The
// separator is what makes the disconnect sweep exact: without it a connection id
// that is a prefix of another would cancel the other's in-flight calls.
const corrSep = ":"

// invokeLocal is the whole of an invocation against a socket THIS replica holds.
// It is the only place a frame is written to a node, so it is the only place the
// gate has to run — for a local caller and for a forwarded one alike.
func (r *Registry) invokeLocal(ctx context.Context, key NodeKey, frame Frame, timeout time.Duration) (InvokeResult, error) {
	r.mu.Lock()
	s, ok := r.byKey[key]
	if !ok {
		r.mu.Unlock()
		return InvokeResult{}, ErrNoSuchNode
	}
	r.nextCorr++
	corrID := fmt.Sprintf("%s%s%d", s.ConnID, corrSep, r.nextCorr)
	ch := make(chan InvokeResult, 1)
	r.pending[corrID] = ch
	send := s.send
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		delete(r.pending, corrID)
		r.mu.Unlock()
	}()

	payload, err := frame(corrID)
	if err != nil {
		return InvokeResult{}, err
	}
	if r.gate != nil {
		req, derr := decodeInvoke(payload)
		if derr != nil {
			return InvokeResult{}, derr
		}
		if gerr := r.gate(s, req.Command, paramsOf(req)); gerr != nil {
			return InvokeResult{}, gerr
		}
	}
	if err := send(ctx, payload); err != nil {
		return InvokeResult{}, fmt.Errorf("bot: send to %s: %w", key, err)
	}

	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case res, open := <-ch:
		if !open {
			return InvokeResult{}, ErrNodeGone
		}
		return res, nil
	case <-timer.C:
		return InvokeResult{}, ErrInvokeTimeout
	case <-ctx.Done():
		return InvokeResult{}, ctx.Err()
	}
}

// Answer delivers a node's reply to whoever is waiting on it.
//
// An answer with no waiter is dropped rather than queued: it means the caller
// already timed out or went away, and holding it would only surface as a reply
// to an unrelated later call.
func (r *Registry) Answer(corrID string, res InvokeResult) {
	r.mu.Lock()
	ch, ok := r.pending[corrID]
	if ok {
		delete(r.pending, corrID)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- res:
	default:
	}
}

// ---------------------------------------------------------------------------
// Presence: where the OTHER replicas' sockets are
// ---------------------------------------------------------------------------

// Defaults for the presence lease. TTL is the window in which a replica that
// dies without releasing still advertises its sockets; renew at a third of it so
// two consecutive KV failures do not expire a live claim.
const (
	DefaultPresenceTTL   = 45 * time.Second
	defaultPresenceRenew = DefaultPresenceTTL / 3
	presenceOpTimeout    = 3 * time.Second
	peerHopMargin        = 2 * time.Second

	// peerMaxBody must carry whatever a SOCKET carries. A hop that is narrower
	// than a frame makes size decide reachability: the same invocation succeeds
	// on the replica holding the node and fails on every other one, which is the
	// replica-dependent behaviour this routing exists to remove. So it is derived
	// from the frame cap, never picked. JSON renders []byte as base64 — 4 bytes
	// per 3 — and the envelope around it is a few hundred.
	peerMaxBody int64 = maxFrameBytes/3*4 + 64<<10
)

// KV is the slice of the Hanzo KV client the presence map uses. Narrow on
// purpose: the presence map is three operations, and naming only those three
// keeps the whole KV surface out of the registry's reach.
// github.com/hanzokv/go/v9's Cmdable satisfies it, so *kv.Client and
// *kv.ClusterClient are passed as-is.
type KV interface {
	Set(ctx context.Context, key string, value any, expiration time.Duration) *kv.StatusCmd
	Get(ctx context.Context, key string) *kv.StringCmd
	Eval(ctx context.Context, script string, keys []string, args ...any) *kv.Cmd
}

// Compile-time proof that the real client fits the narrow seam.
var _ KV = kv.Cmdable(nil)

// PresenceStore is the shared answer to "which replica holds this node's
// socket". Lookup reports ErrNoSuchNode for an absent claim: to a caller, a node
// nobody holds and a node in another tenant are the same non-answer, and that
// sameness is the point.
type PresenceStore interface {
	// Claim takes ownership unconditionally. A node that reconnects to another
	// replica must win immediately — the old replica's socket is dead whether or
	// not it has noticed yet.
	Claim(ctx context.Context, key NodeKey, replica string, ttl time.Duration) error

	// Renew extends a claim this replica still owns, and re-takes one that has
	// gone missing (a claim lost to a KV blip heals on the next tick). It must
	// NOT overwrite another replica's claim: a socket that has already moved
	// would otherwise be dragged back to a replica holding a half-open TCP
	// connection, where every invoke burns its full timeout.
	Renew(ctx context.Context, key NodeKey, replica string, ttl time.Duration) error

	// Release drops a claim only while it is still ours, for the same reason.
	Release(ctx context.Context, key NodeKey, replica string) error

	// Lookup returns the replica holding the node, or ErrNoSuchNode.
	Lookup(ctx context.Context, key NodeKey) (string, error)
}

// presencePrefix namespaces the map. The org sits in its own escaped segment, so
// "bot:node:<org>:*" is an exact per-tenant namespace — a scan or a key can
// never be crafted to reach across it.
const presencePrefix = "bot:node:"

// presenceKey is injective in (org, node): both segments are escaped, so no
// separator survives inside one and no two distinct pairs can collide onto one
// key. Node ids come from the node itself, which makes this a boundary, not a
// formatting nicety.
func presenceKey(k NodeKey) string {
	return presencePrefix + url.QueryEscape(k.Org) + ":" + url.QueryEscape(k.NodeID)
}

// renewScript extends or re-takes, never steals. Redis Lua yields false for a
// missing key, which is the "re-take" arm.
const renewScript = `local v = redis.call('get', KEYS[1])
if v == false or v == ARGV[1] then
  redis.call('set', KEYS[1], ARGV[1], 'PX', ARGV[2])
  return 1
end
return 0`

// releaseScript deletes only our own claim. An unconditional delete loses the
// race a node reconnecting to another replica creates: the old replica notices
// the dead socket last, and its cleanup would erase the new owner's live claim.
const releaseScript = `if redis.call('get', KEYS[1]) == ARGV[1] then
  return redis.call('del', KEYS[1])
end
return 0`

type kvPresence struct{ c KV }

// NewKVPresence backs the presence map with Hanzo KV.
func NewKVPresence(c KV) PresenceStore { return &kvPresence{c: c} }

func (p *kvPresence) Claim(ctx context.Context, key NodeKey, replica string, ttl time.Duration) error {
	return p.c.Set(ctx, presenceKey(key), replica, ttl).Err()
}

func (p *kvPresence) Renew(ctx context.Context, key NodeKey, replica string, ttl time.Duration) error {
	return p.c.Eval(ctx, renewScript, []string{presenceKey(key)}, replica, ttl.Milliseconds()).Err()
}

func (p *kvPresence) Release(ctx context.Context, key NodeKey, replica string) error {
	return p.c.Eval(ctx, releaseScript, []string{presenceKey(key)}, replica).Err()
}

func (p *kvPresence) Lookup(ctx context.Context, key NodeKey) (string, error) {
	v, err := p.c.Get(ctx, presenceKey(key)).Result()
	if errors.Is(err, kv.Nil) {
		return "", ErrNoSuchNode
	}
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", ErrNoSuchNode
	}
	return v, nil
}

// Run keeps this replica's claims alive until ctx ends, then drops them.
//
// The TTL is what unstrands nodes when a replica dies outright; releasing on a
// clean shutdown is what stops peers forwarding into a pod that is draining,
// during the window before that TTL would have expired. On a single-replica
// registry there is nothing to advertise, so it returns at once.
func (r *Registry) Run(ctx context.Context) {
	if !r.clustered() || r.cfgErr != nil {
		return
	}
	t := time.NewTicker(r.renew)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.releaseAll()
			return
		case <-t.C:
			r.renewAll(ctx)
		}
	}
}

// What this replica holds is read from the socket table, never mirrored beside
// it. A second copy is a second truth: it drifts the moment a session is
// registered or torn down, and a mirror that has missed a teardown renews the
// claim for a socket that is gone, forever, because nothing left in it ever
// expires. Reading the table means presence follows the sockets by construction.
func (r *Registry) ownedKeys() []NodeKey {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]NodeKey, 0, len(r.byKey))
	for k := range r.byKey {
		keys = append(keys, k)
	}
	return keys
}

func (r *Registry) renewAll(ctx context.Context) {
	for _, k := range r.ownedKeys() {
		opCtx, cancel := context.WithTimeout(ctx, presenceOpTimeout)
		err := r.presence.Renew(opCtx, k, r.replica, r.ttl)
		cancel()
		if err != nil {
			r.log.Warn("bot: presence renew failed", "node", k.String(), "replica", r.replica, "err", err)
		}
	}
}

// releaseAll runs on a context of its own: it is reached because the caller's
// context is already done, and a release on a dead context releases nothing.
func (r *Registry) releaseAll() {
	for _, k := range r.ownedKeys() {
		ctx, cancel := context.WithTimeout(context.Background(), presenceOpTimeout)
		err := r.presence.Release(ctx, k, r.replica)
		cancel()
		if err != nil {
			r.log.Warn("bot: presence release failed on shutdown", "node", k.String(), "err", err)
		}
	}
}

// ---------------------------------------------------------------------------
// The hop: reaching a socket another replica holds
// ---------------------------------------------------------------------------

// Hop carries an invocation to the replica that holds the node's socket.
//
// WHY HTTP AND NOT ZAP. ZAP is cloud's answer for INTER-SERVICE RPC — one
// service calling another across a trust boundary. This is not that: it is one
// replica of cloud reaching another replica of the SAME binary because a socket
// landed there, and cloud has already answered that question exactly once
// (shardrouter forwards an org's request to the pod owning its files, over the
// peer address from CLOUD_PEERS). A second mechanism for "reach my peer pod"
// would be a second answer to a settled question. Nor is there a ZAP client seam
// to reuse: zapface is a server face whose dispatch replays each call as an
// in-process HTTP request into the same Fiber app, so a ZAP hop here would be
// this HTTP hop with an extra codec in front of it.
//
// WHY NOT THE KV'S OWN PUB/SUB (publish to bot:invoke:<pod>, listen on
// bot:invoke-result:<originPod>, which is how this worked before): a
// request/response spliced out of two fire-and-forget channels needs a second
// correlation table, with its own timeouts and its own cleanup, layered on the
// one the registry already keeps — and it cannot tell a dead owner from a slow
// one, so a replica that died between the lookup and the publish costs the caller
// a full timeout instead of a dial error. One synchronous connection makes the
// correlation the connection itself.
//
// The interface is here so that transport can be replaced without the routing
// above it noticing.
type Hop interface {
	Forward(ctx context.Context, replica string, key NodeKey, req []byte, timeout time.Duration) (InvokeResult, error)
}

// The peer hop's wire. Both ends are this file, in this binary.
const (
	// PeerInvokePath is where PeerHandler must be mounted for NewHTTPHop to find
	// it. One constant, both ends.
	PeerInvokePath = "/v1/bot/peer/invoke"

	// peerTokenHeader carries the replica-to-replica secret. Deliberately not
	// Authorization: this request has no user identity to present, and putting a
	// machine secret there invites an identity middleware to read it as a
	// credential and 401 the hop.
	peerTokenHeader = "X-Bot-Peer-Token"
)

// The closed set of outcomes that survive a hop. Sentinels do not travel, so
// they are named on the wire and re-raised on the other side.
const (
	peerErrNoSuchNode = "no_such_node"
	peerErrTimeout    = "invoke_timeout"
	peerErrNodeGone   = "node_gone"
	peerErrDenied     = "denied"
	peerErrFailed     = "forward_failed"
)

type peerInvoke struct {
	Org    string `json:"org"`
	NodeID string `json:"node"`
	// Frame is the invocation as it will go on the wire, minus the correlation
	// id — see Frame.
	Frame     []byte `json:"frame,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
}

type peerAnswer struct {
	OK      bool   `json:"ok"`
	Payload []byte `json:"payload,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Err     string `json:"err,omitempty"`
}

// PeerHandler serves a forwarded invocation against THIS replica's sockets.
//
// # The one place an org arrives in a body
//
// Everywhere else the org comes from the gateway-injected X-Org-Id, precisely so
// no caller can name another tenant's node. Here it arrives in the body, because
// the request has already crossed the identity boundary on the replica that
// forwarded it — it is a machine hop, not a user call. What makes that safe is
// the token: it proves the caller is another replica of this binary, which
// derived that org from a validated header. With no token configured the handler
// serves nothing at all, because an unauthenticated endpoint that takes an org
// from a body is a cross-tenant invoke primitive for anything that can reach the
// pod.
//
// It invokes strictly LOCALLY, so a hop cannot chain into another hop:
// forwarding is loop-free by construction, with no hop counter to maintain. The
// gate runs here too, because invokeLocal is where it runs — a forwarded
// invocation is authorized by the replica that knows the node, exactly like a
// local one.
//
// Mount it outside the user-identity middleware; on zip that is
// zip.AdaptNetHTTP.
func (r *Registry) PeerHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if len(r.token) == 0 || r.cfgErr != nil {
			http.Error(w, "peer forwarding disabled", http.StatusServiceUnavailable)
			return
		}
		if subtle.ConstantTimeCompare([]byte(req.Header.Get(peerTokenHeader)), r.token) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, peerMaxBody))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var in peerInvoke
		if err := json.Unmarshal(body, &in); err != nil || in.Org == "" || in.NodeID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		key := NodeKey{Org: in.Org, NodeID: in.NodeID}
		res, invErr := r.invokeLocal(req.Context(), key, func(corrID string) ([]byte, error) {
			return stampInvoke(in.Frame, key, corrID)
		}, time.Duration(in.TimeoutMS)*time.Millisecond)

		out := peerAnswer{OK: res.OK, Payload: res.Payload, Code: res.Code, Message: res.Message}
		var denied *Denied
		switch {
		case invErr == nil:
		case errors.Is(invErr, ErrNoSuchNode):
			out = peerAnswer{Err: peerErrNoSuchNode}
		case errors.Is(invErr, ErrInvokeTimeout):
			out = peerAnswer{Err: peerErrTimeout}
		case errors.Is(invErr, ErrNodeGone):
			out = peerAnswer{Err: peerErrNodeGone}
		case errors.As(invErr, &denied):
			out = peerAnswer{Err: peerErrDenied, Code: denied.Code, Message: denied.Message}
		default:
			out = peerAnswer{Err: peerErrFailed, Message: invErr.Error()}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}

type httpHop struct {
	resolve func(replica string) (addr string, ok bool)
	token   string
	client  *http.Client
}

// NewHTTPHop forwards over the peer address the deployment already knows.
//
// resolve turns a replica id into its address — the same id→addr mapping the
// shard router elects over (CLOUD_PEERS, or the live membership), passed as a
// function so the registry depends on the shape of that answer and not on where
// it comes from. An address with no scheme is dialed as in-cluster http.
func NewHTTPHop(resolve func(replica string) (addr string, ok bool), token string) Hop {
	return &httpHop{
		resolve: resolve,
		token:   token,
		client: &http.Client{
			Transport: &http.Transport{
				// No Proxy: pod-to-pod traffic must never be routed through an
				// egress proxy that HTTP_PROXY happens to name.
				Proxy:               nil,
				MaxIdleConnsPerHost: 32,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

func (h *httpHop) Forward(ctx context.Context, replica string, key NodeKey, req []byte, timeout time.Duration) (InvokeResult, error) {
	addr, ok := h.resolve(replica)
	if !ok || addr == "" {
		// The claim names a replica that is no longer in the set. Whatever socket
		// it held left with it, so this is not-found, not an error.
		return InvokeResult{}, ErrNoSuchNode
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	body, err := json.Marshal(peerInvoke{
		Org:       key.Org,
		NodeID:    key.NodeID,
		Frame:     req,
		TimeoutMS: timeout.Milliseconds(),
	})
	if err != nil {
		return InvokeResult{}, err
	}

	// The owner's own timeout fires first, so an unanswering node comes back as a
	// clean ErrInvokeTimeout rather than a severed connection.
	ctx, cancel := context.WithTimeout(ctx, timeout+peerHopMargin)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, peerEndpoint(addr), bytes.NewReader(body))
	if err != nil {
		return InvokeResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(peerTokenHeader, h.token)

	resp, err := h.client.Do(httpReq)
	if err != nil {
		return InvokeResult{}, fmt.Errorf("bot: forward %s to replica %s: %w", key, replica, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, peerMaxBody))
		return InvokeResult{}, fmt.Errorf("bot: forward %s to replica %s: status %d", key, replica, resp.StatusCode)
	}

	var ans peerAnswer
	if err := json.NewDecoder(io.LimitReader(resp.Body, peerMaxBody)).Decode(&ans); err != nil {
		return InvokeResult{}, fmt.Errorf("bot: forward %s to replica %s: %w", key, replica, err)
	}
	switch ans.Err {
	case "":
		return InvokeResult{OK: ans.OK, Payload: ans.Payload, Code: ans.Code, Message: ans.Message}, nil
	case peerErrNoSuchNode:
		return InvokeResult{}, ErrNoSuchNode
	case peerErrTimeout:
		return InvokeResult{}, ErrInvokeTimeout
	case peerErrNodeGone:
		return InvokeResult{}, ErrNodeGone
	case peerErrDenied:
		// A refusal reads the same wherever the socket is: same code, same
		// message, same HTTP answer from the face that asked.
		return InvokeResult{}, &Denied{Code: ans.Code, Message: ans.Message}
	default:
		return InvokeResult{}, fmt.Errorf("bot: replica %s refused %s: %s", replica, key, ans.Message)
	}
}

func peerEndpoint(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimSuffix(addr, "/") + PeerInvokePath
	}
	return "http://" + addr + PeerInvokePath
}

// ---------------------------------------------------------------------------
// The two things routing reads out of a frame
// ---------------------------------------------------------------------------

// routedFrame is a node.invoke.request read back. Routing needs exactly two
// things from a frame: what is being asked (so the gate can answer), and where
// to stamp the correlation id and node id, which only the replica holding the
// socket can know. ws.go owns the wire; this reads it with ws.go's own types, so
// there is one description of the frame and not two.
type routedFrame struct {
	Type    string        `json:"type"`
	Event   string        `json:"event"`
	Payload invokeRequest `json:"payload"`
}

// decodeInvoke reads a frame as the invocation it carries, refusing anything
// that is not a node.invoke.request. A frame that arrived over a hop has already
// crossed a trust boundary; refusing here means a peer cannot make this replica
// write arbitrary bytes into one of its sockets.
func decodeInvoke(b []byte) (invokeRequest, error) {
	var f routedFrame
	if err := json.Unmarshal(b, &f); err != nil {
		return invokeRequest{}, fmt.Errorf("bot: unreadable invoke frame: %w", err)
	}
	if f.Type != frameEvent || f.Event != eventInvokeRequest {
		return invokeRequest{}, fmt.Errorf("bot: not an invoke frame")
	}
	return f.Payload, nil
}

// stampInvoke re-stamps a frame that crossed a hop with the two values only this
// replica can supply: the correlation id it just minted, and the node whose
// socket it is about to be written to.
func stampInvoke(b []byte, key NodeKey, corrID string) ([]byte, error) {
	req, err := decodeInvoke(b)
	if err != nil {
		return nil, err
	}
	req.ID = corrID
	req.NodeID = key.NodeID
	return json.Marshal(eventFrame{Type: frameEvent, Event: eventInvokeRequest, Payload: req})
}

// paramsOf yields an invocation's arguments as JSON. The wire carries them as a
// STRING holding JSON (bot's paramsJSON), so this is where that unwrapping
// happens for anything reading a frame rather than writing one.
func paramsOf(req invokeRequest) json.RawMessage {
	if req.ParamsJSON == nil {
		return nil
	}
	return json.RawMessage(*req.ParamsJSON)
}
