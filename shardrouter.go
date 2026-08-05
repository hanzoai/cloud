package cloud

// Shard router — the horizontal-scale writer partition for the unified binary.
//
// THE ONE INVARIANT. No two pods may EVER write the same tenant's SQLite file.
// cloud persists per-org SQLite (KMS orgs/<org>/kms.db, finance orgs/<org>/finance.db,
// and every org-keyed store) via cloud.OrgDB. SQLite WAL does not coordinate across
// pods, so two writers on one file corrupt it. This router is the mechanism that
// makes the invariant hold under replicas>1 WITHOUT any shared RWX volume:
//
//   - each pod is a StatefulSet ordinal (cloud-N) with its OWN RWO PVC (per-pod
//     volumeClaimTemplates), so two pods never share a physical data dir; and
//   - each org is pinned to EXACTLY ONE owner pod by rendezvous (HRW) hashing —
//     ha.Owner(orgSlug, peers) — so every request that touches org X's files is
//     served on X's owner pod and nowhere else.
//
// Two independent guarantees compose: even if routing had a bug, a non-owner pod
// physically cannot open the owner's file (it is on the owner's PVC, unmounted
// here); even if PVCs were shared, routing sends all of an org's writes to one pod.
//
// WHY IN-BINARY, NOT AT THE GATEWAY. The org is only known AFTER the identity trust
// boundary (SanitizeIdentity) validates the IAM JWT and mints X-Org-Id from the
// `owner` claim. The ingress/gateway in front of cloud does NOT validate the JWT, so
// it cannot hash on the validated org — only a raw, forgeable client header. This
// middleware runs immediately AFTER SanitizeIdentity, so it hashes the VALIDATED,
// server-minted org (never a client value), and everything downstream — audit,
// per-org rate limit, billing, and every subsystem store — runs ONLY on the owner.
//
// The forward reuses the transparent-reverse-proxy posture of the reader edge
// (reader_proxy.go): it preserves method/path/query/Host/headers/body, streams the
// response (SSE/chat pass through, never buffered — a per-org billing debit rides
// even a streaming chat, so streams route too), and retries ONLY a never-delivered
// dial across the owner's brief roll gap, so a peer restart surfaces as latency, not
// a 502. A rescheduled ordinal reattaches its SAME PVC → its SAME shard, so failover
// is safe by construction (its files move with it, and no peer ever owned them).
//
// Membership is STATIC (CLOUD_PEERS), identical on every pod, so all pods compute the
// SAME owner for an org — which structurally rules out (a) two pods both believing
// they own an org (dual-writer) and (b) a routing loop (the owner recomputes
// owner==self and serves). N is pinned; changing it remaps some orgs to a new owner
// whose PVC lacks their files — a tenant-file move, the documented follow-up (see the
// package report), never a live rebalance this router pretends to do.

import (
	"errors"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanzoai/ha"
	luxlog "github.com/luxfi/log"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

// shardHopHeader marks a request that a peer already forwarded once. If an owner
// mismatch is seen WITH this header present, membership has diverged (impossible
// under the static, identical CLOUD_PEERS set) — the router fails CLOSED (421)
// rather than loop or serve another shard's data. It carries the forwarding pod's
// id for trace/debug.
const shardHopHeader = "X-Hanzo-Shard-Hop"

// shardForwardTimeout bounds the dial-retry window across an owner's roll gap. It
// caps only the time spent trying to ESTABLISH the connection (a never-delivered
// dial is the only thing retried); once bytes flow, the stream is unbounded.
const shardForwardTimeout = 25 * time.Second

// shardRouter pins each org to its rendezvous-hash owner pod and forwards a request
// whose org this pod does not own. nil ⇒ sharding disabled (single-pod deployment),
// in which case Middleware is a pass-through — byte-identical to today.
type shardRouter struct {
	self  string      // this pod's stable id (StatefulSet ordinal name, e.g. cloud-1)
	peers []ha.Member // the full, stable writer set (id@addr), identical on every pod

	log luxlog.Logger

	mu      sync.Mutex
	clients map[string]*fasthttp.HostClient // addr → pooled streaming client, lazily built
}

// newShardRouter builds the router from config, or returns nil when sharding is off
// (CLOUD_PEERS names ≤1 pod, or no self id) so the caller wires no middleware and the
// single-pod path is unchanged. Config.Validate has already refused to boot a
// multi-peer set that does not contain self, so a non-nil router always owns a shard.
func newShardRouter(cfg *Config, log luxlog.Logger) *shardRouter {
	peers := parsePeers(cfg.ShardPeers)
	self := strings.TrimSpace(cfg.ShardSelf)
	if len(peers) < 2 || self == "" || !peersContain(peers, self) {
		return nil
	}
	return &shardRouter{self: self, peers: peers, log: log, clients: map[string]*fasthttp.HostClient{}}
}

// peerIDs returns the member ids for logging.
func (r *shardRouter) peerIDs() []string {
	ids := make([]string, len(r.peers))
	for i, m := range r.peers {
		ids[i] = m.ID
	}
	return ids
}

// Middleware returns the routing handler. It runs after SanitizeIdentity, so
// X-Org-Id is the VALIDATED, server-minted org — never a raw client header.
//
//   - No validated org (anonymous / org-less): served locally. Such a request
//     carries no principal and the F1 data-plane gate refuses any per-org store
//     access anyway, so it can never write a tenant file wherever it lands.
//   - Owned here: served locally (this pod is the org's single writer).
//   - Owned by a peer: the WHOLE request is forwarded there, so the peer — the org's
//     sole writer — performs every store touch, audit append, and billing debit.
func (r *shardRouter) Middleware() zip.Handler {
	return func(c *zip.Ctx) error {
		// Hash on the SAME injective slug the on-disk path uses (cloud.OrgDB folds
		// every org file through SanitizeOrg). Routing key ≡ file-shard key, so "the
		// pod that owns slug S writes exactly orgs/S/*" — the tightest binding to the
		// invariant. An org SanitizeOrg refuses (→ "") cannot open a file at all; it
		// is served locally and fails closed at the store layer, never misrouted.
		slug := SanitizeOrg(strings.TrimSpace(c.Header("X-Org-Id")))
		if slug == "" {
			return c.Continue()
		}
		owner, ok := ha.Owner(slug, r.peers)
		if !ok || owner.ID == r.self {
			return c.Continue()
		}
		return r.forward(c, owner, slug)
	}
}

// forward proxies the in-flight request to the org's owner pod and streams the
// response back. It returns nil after writing the peer's response (or a fail-closed
// error response) into fiber's own buffers — the request never runs the local
// downstream chain, so no local per-org store is opened for an org this pod does not
// own.
func (r *shardRouter) forward(c *zip.Ctx, owner ha.Member, slug string) error {
	fc := c.Fiber()
	req := fc.Request()
	resp := fc.Response()

	// Loop guard / divergence fail-closed. Under the static, identical peer set this
	// is unreachable (the owner always recomputes owner==self); if it ever fires,
	// membership diverged and serving locally would touch another shard's files, so
	// we refuse (421) instead. A client that forges the header only 421s ITS OWN
	// cross-shard request — never another tenant, never availability for others.
	if len(req.Header.Peek(shardHopHeader)) > 0 {
		r.log.Error("shard loop guard tripped — membership divergence", "org_slug", slug, "owner", owner.ID, "self", r.self)
		resp.Reset()
		resp.SetStatusCode(fasthttp.StatusMisdirectedRequest) // 421
		resp.Header.SetContentType("application/json")
		resp.SetBodyString(`{"error":{"code":"shard_misrouted","message":"shard ownership divergence; retry"}}`)
		return nil
	}
	req.Header.Set(shardHopHeader, r.self)

	hc := r.hostClient(owner.Addr)
	deadline := time.Now().Add(shardForwardTimeout)
	backoff := 100 * time.Millisecond
	resp.Reset()
	for {
		err := hc.Do(req, resp)
		if err == nil {
			return nil // resp (status + headers + streaming body) is fiber's; it flushes it
		}
		// Only a never-delivered dial is retried (the request cannot have executed on
		// the peer, so a retried non-idempotent POST is safe). Anything else — or the
		// budget exhausted — fails closed with a 502; a shard owner that is down is an
		// honest upstream error, never a silent local (wrong-shard) fallback.
		if !isPeerDialError(err) || time.Now().After(deadline) {
			r.log.Warn("shard forward failed", "org_slug", slug, "owner", owner.ID, "addr", owner.Addr, "err", err)
			resp.Reset()
			resp.SetStatusCode(fasthttp.StatusBadGateway)
			resp.Header.SetContentType("application/json")
			resp.SetBodyString(`{"error":{"code":"shard_owner_unavailable","message":"shard owner unavailable"}}`)
			return nil
		}
		select {
		case <-c.Context().Done():
			return c.Context().Err()
		case <-time.After(backoff):
		}
		if backoff < 2*time.Second {
			backoff *= 2
		}
		resp.Reset()
	}
}

// hostClient returns the pooled streaming client for a peer addr, building it once.
// StreamResponseBody keeps SSE/chat streams flowing token-by-token (never buffered);
// no ReadTimeout so a long-lived stream is not cut. The client dials addr directly
// (headless per-pod DNS cloud-N.<svc>:8000), forwarding the request — including its
// original Host — verbatim.
func (r *shardRouter) hostClient(addr string) *fasthttp.HostClient {
	r.mu.Lock()
	defer r.mu.Unlock()
	if hc, ok := r.clients[addr]; ok {
		return hc
	}
	hc := &fasthttp.HostClient{
		Addr:                     addr,
		StreamResponseBody:       true,
		MaxConns:                 1024,
		WriteTimeout:             30 * time.Second,
		MaxIdleConnDuration:      90 * time.Second,
		DisablePathNormalizing:   true,
		NoDefaultUserAgentHeader: true,
	}
	r.clients[addr] = hc
	return hc
}

// isPeerDialError reports whether err means the connection to the owner was never
// established — connection refused / no free conn / dial timeout / no such host —
// the class that occurs during an owner's rolling restart. Only these are retried,
// because the request provably never reached the peer, so a replay cannot
// double-execute it. A delivered-then-failed response is NOT retried (ambiguous).
func isPeerDialError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, fasthttp.ErrDialTimeout) ||
		errors.Is(err, fasthttp.ErrNoFreeConns) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return op.Op == "dial"
	}
	s := err.Error()
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "no such host") ||
		strings.Contains(s, "dialing to the given TCP address timed out")
}

// parsePeers parses a CLOUD_PEERS value ("id@addr,id2@addr2", or bare "id" with
// addr==id) into the HRW membership set. Whitespace-only entries are dropped; an
// entry with no '@' uses the id as its own addr (dev/local). This is the shard
// twin of internal/org.parseReplicasEnv, kept local so the router has no dependency
// on the replica package (the two share the ha.Member value type, not code).
func parsePeers(v string) []ha.Member {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	var out []ha.Member
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, addr, ok := strings.Cut(part, "@")
		if !ok {
			id, addr = part, part
		}
		id, addr = strings.TrimSpace(id), strings.TrimSpace(addr)
		if id == "" || addr == "" {
			continue
		}
		out = append(out, ha.Member{ID: id, Addr: addr})
	}
	return out
}

// peersContain reports whether id is one of the members — the "am I in my own ring?"
// boot check (Config.Validate) that keeps a misconfigured ordinal from black-holing
// every request by forwarding it away with no shard of its own.
func peersContain(peers []ha.Member, id string) bool {
	for _, m := range peers {
		if m.ID == id {
			return true
		}
	}
	return false
}
