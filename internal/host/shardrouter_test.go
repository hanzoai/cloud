package host

// Tests for the horizontal-scale shard router. The suite exists to PROVE the one
// invariant — no two pods ever write one tenant's SQLite file — at the routing
// layer:
//
//   - ExactlyOneOwnerPerOrg: the org→owner map is a total, deterministic FUNCTION
//     onto the peer set (every org has exactly one owner pod; same org, same owner).
//   - AllPodsAgree: every pod computes the SAME owner for an org, so two pods can
//     never both believe they own it (the dual-writer that would corrupt a file).
//   - OwnedServedLocally / ForwardsUnownedToOwner: the middleware serves an org it
//     owns locally and forwards one it does not to the owner — so a per-org store is
//     opened ONLY on the owner pod. The local downstream chain never runs for a
//     non-owned org.
//   - LoopGuardFailsClosed: a divergence (or a forged hop header) fails closed (421),
//     never serves another shard's data locally.
//   - NoOrgServedLocally: an org-less request is served locally (it touches no
//     per-org file, so it is safe wherever it lands).

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/ha"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

func testLog() luxlog.Logger { return luxlog.New("shardtest") }

// authority answers the TLS a pod of the deployment whose master key is key serves
// its peers with, and the TLS it calls them with.
func authority(t *testing.T, key byte) (server, client *tls.Config) {
	t.Helper()
	c, err := coord.New(coord.Config{Self: coord.Member{ID: "test"}, Secret: bytes.Repeat([]byte{key}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	server, err = c.ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	client, err = c.ClientTLS()
	if err != nil {
		t.Fatal(err)
	}
	return server, client
}

// peerListener is a peer's listener: TLS under srv, answering with h.
func peerListener(t *testing.T, srv *tls.Config, h http.HandlerFunc) string {
	t.Helper()
	p := httptest.NewUnstartedServer(h)
	p.TLS = srv
	p.StartTLS()
	t.Cleanup(p.Close)
	return strings.TrimPrefix(p.URL, "https://")
}

// threePeers is a fixed 3-pod ring; pass the addr each ordinal should forward to.
func threePeers(addr0, addr1, addr2 string) []ha.Member {
	return []ha.Member{
		{ID: "cloud-0", Addr: addr0},
		{ID: "cloud-1", Addr: addr1},
		{ID: "cloud-2", Addr: addr2},
	}
}

// findOrgOwnedBy brute-forces an org name whose file-shard slug is owned by id.
// With 3 peers ~1/3 of orgs map to each, so this returns within a few iterations.
func findOrgOwnedBy(t *testing.T, peers []ha.Member, id string) string {
	t.Helper()
	for i := range 100000 {
		org := fmt.Sprintf("org-%d", i)
		if o, ok := ha.Owner(namespace.Sanitize(org), peers); ok && o.ID == id {
			return org
		}
	}
	t.Fatalf("no org owned by %s found in the search space", id)
	return ""
}

// TestShardOwnership_AllPodsAgree proves that every pod, computing ownership from the
// SAME static peer set, selects the SAME owner for an org — so two pods can never both
// admit themselves as its writer. This is the anti-dual-writer property.
func TestShardOwnership_AllPodsAgree(t *testing.T) {
	peers := threePeers("a:8000", "b:8000", "c:8000")
	for i := range 5000 {
		org := fmt.Sprintf("acct-%d", i)
		slug := namespace.Sanitize(org)
		want, _ := ha.Owner(slug, peers)
		// Each pod (self = cloud-0/1/2) decides "do I own it?"; exactly the owner says yes.
		yes := 0
		for _, self := range []string{"cloud-0", "cloud-1", "cloud-2"} {
			r := &shardRouter{self: self, peers: peers, log: testLog(), clients: map[string]*fasthttp.HostClient{}}
			if amOwner := ownsLocally(r, slug); amOwner {
				yes++
				if self != want.ID {
					t.Fatalf("org %q: pod %s claims ownership but owner is %s", org, self, want.ID)
				}
			}
		}
		if yes != 1 {
			t.Fatalf("org %q: %d pods claim ownership, want exactly 1", org, yes)
		}
	}
}

// ownsLocally mirrors the router's serve-locally decision for a slug (owner==self).
func ownsLocally(r *shardRouter, slug string) bool {
	o, ok := ha.Owner(slug, r.peers)
	return ok && o.ID == r.self
}

// TestShardRouter_OwnedServedLocally: a request for an org THIS pod owns runs the
// local chain (the per-org store would be opened here, correctly).
func TestShardRouter_OwnedServedLocally(t *testing.T) {
	peers := threePeers("dead-0:1", "dead-1:1", "dead-2:1") // addrs unused: we must NOT forward
	r := &shardRouter{self: "cloud-0", peers: peers, log: testLog(), clients: map[string]*fasthttp.HostClient{}}
	org := findOrgOwnedBy(t, peers, "cloud-0")

	app := zip.New(zip.Config{})
	app.Use(r.Middleware())
	localHit := false
	app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error {
		localHit = true
		return c.JSON(http.StatusOK, map[string]string{"served": "local"})
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-Org-Id", org) // the validated, server-minted org (post-SanitizeIdentity)
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !localHit {
		t.Fatalf("org %q owned by self was NOT served locally", org)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "local") {
		t.Fatalf("body = %q, want the local marker", body)
	}
}

// TestShardRouter_ForwardsUnownedToOwner: a request for an org owned by a PEER is
// forwarded to that peer verbatim (carrying the loop-guard hop header), the peer's
// response streams back, and the LOCAL chain never runs — so no per-org store is
// opened on the wrong pod.
func TestShardRouter_ForwardsUnownedToOwner(t *testing.T) {
	var gotHop, gotOrg, gotPath string
	peerHit := false
	srv, cli := authority(t, 1)
	peerAddr := peerListener(t, srv, func(w http.ResponseWriter, req *http.Request) {
		peerHit = true
		gotHop = req.Header.Get(shardHopHeader)
		gotOrg = req.Header.Get("X-Org-Id")
		gotPath = req.URL.Path
		w.Header().Set("X-Served-By", "peer-owner")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "PEER-OWNER-RESPONSE")
	})

	// cloud-1's addr is the real peer; self is cloud-0. Route an org owned by cloud-1.
	peers := threePeers("unused-0:1", peerAddr, "unused-2:1")
	r := &shardRouter{self: "cloud-0", peers: peers, log: testLog(), tls: cli, clients: map[string]*fasthttp.HostClient{}}
	org := findOrgOwnedBy(t, peers, "cloud-1")

	app := zip.New(zip.Config{})
	app.Use(r.Middleware())
	localHit := false
	app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error {
		localHit = true
		return c.JSON(http.StatusOK, map[string]string{"served": "local-WRONG"})
	})

	req := httptest.NewRequest(http.MethodGet, "/probe?x=1", nil)
	req.Header.Set("X-Org-Id", org)
	resp, err := app.Test(req, zip.TestConfig{Timeout: 5 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !peerHit {
		t.Fatalf("owner peer was NOT hit for org %q owned by cloud-1", org)
	}
	if localHit {
		t.Fatalf("local chain ran for a peer-owned org %q — a per-org store could open on the WRONG pod", org)
	}
	if gotOrg != org {
		t.Fatalf("peer saw X-Org-Id=%q, want %q", gotOrg, org)
	}
	if gotHop != "cloud-0" {
		t.Fatalf("peer saw hop header %q, want the forwarding pod id cloud-0", gotHop)
	}
	if gotPath != "/probe" {
		t.Fatalf("peer saw path %q, want /probe (verbatim forward)", gotPath)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (peer's response)", resp.StatusCode)
	}
	if sb := resp.Header.Get("X-Served-By"); sb != "peer-owner" {
		t.Fatalf("X-Served-By = %q, want peer-owner (response is the owner's)", sb)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "PEER-OWNER-RESPONSE" {
		t.Fatalf("body = %q, want the peer owner's response", body)
	}
}

// TestShardRouter_NoOrgServedLocally: an org-less request (no validated principal)
// is served locally — it touches no per-org file, so it is safe wherever it lands and
// must not be forwarded (there is nowhere to forward it).
func TestShardRouter_NoOrgServedLocally(t *testing.T) {
	peers := threePeers("no-0:1", "no-1:1", "no-2:1")
	r := &shardRouter{self: "cloud-0", peers: peers, log: testLog(), clients: map[string]*fasthttp.HostClient{}}

	app := zip.New(zip.Config{})
	app.Use(r.Middleware())
	localHit := false
	app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error {
		localHit = true
		return c.JSON(http.StatusOK, map[string]string{"served": "local"})
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil) // NO X-Org-Id
	if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !localHit {
		t.Fatalf("org-less request was not served locally")
	}
}

// TestShardRouter_LoopGuardFailsClosed: a request for a peer-owned org that ALREADY
// carries the hop header (membership divergence, or a forged header) fails closed with
// 421 — it is NEVER served locally (which would open another shard's store) and never
// re-forwarded (which would loop).
func TestShardRouter_LoopGuardFailsClosed(t *testing.T) {
	peerHit := false
	srv, cli := authority(t, 1)
	peerAddr := peerListener(t, srv, func(w http.ResponseWriter, _ *http.Request) {
		peerHit = true
		w.WriteHeader(200)
	})
	peers := threePeers("unused-0:1", peerAddr, "unused-2:1")
	r := &shardRouter{self: "cloud-0", peers: peers, log: testLog(), tls: cli, clients: map[string]*fasthttp.HostClient{}}
	org := findOrgOwnedBy(t, peers, "cloud-1")

	app := zip.New(zip.Config{})
	app.Use(r.Middleware())
	localHit := false
	app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error { localHit = true; return c.JSON(200, map[string]int{"ok": 1}) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set(shardHopHeader, "cloud-2") // already forwarded once
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("status = %d, want 421 (fail-closed on divergence)", resp.StatusCode)
	}
	if localHit {
		t.Fatalf("loop-guard request was served LOCALLY — invariant risk (wrong-shard store open)")
	}
	if peerHit {
		t.Fatalf("loop-guard request was RE-FORWARDED — routing loop")
	}
}

// A FORWARD CARRIES ITS CALLER'S CREDENTIALS, SO IT GOES ONLY TO A PEER. A listener
// at an owner's address that another authority certified (a planted member, an
// address reused by something else) is never handed the request, and a pod with no
// key to prove itself forwards nothing.
func TestShardRouter_ForwardsOnlyToAPeer(t *testing.T) {
	_, cli := authority(t, 1)
	strangerSrv, _ := authority(t, 2)
	strangerHit := false
	stranger := peerListener(t, strangerSrv, func(w http.ResponseWriter, _ *http.Request) {
		strangerHit = true
		w.WriteHeader(200)
	})
	peers := threePeers("unused-0:1", stranger, "unused-2:1")
	org := findOrgOwnedBy(t, peers, "cloud-1")
	for _, c := range []struct {
		name string
		tls  *tls.Config
		want int
	}{
		{"a listener another authority certified", cli, http.StatusBadGateway},
		{"a pod with no key", nil, http.StatusServiceUnavailable},
	} {
		r := &shardRouter{self: "cloud-0", peers: peers, log: testLog(), tls: c.tls, clients: map[string]*fasthttp.HostClient{}}
		app := zip.New(zip.Config{})
		app.Use(r.Middleware())
		app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error { return c.JSON(200, map[string]int{"local": 1}) })
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("Authorization", "Bearer secret-of-the-caller")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if resp.StatusCode != c.want {
			t.Fatalf("%s: status %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
	if strangerHit {
		t.Fatal("a caller's request reached a listener this deployment did not certify")
	}
}

// TestNewShardRouter_DisabledCases: sharding is OFF (nil router, pass-through) for a
// single-pod set, an empty peer list, or a self that is not a member.
func TestNewShardRouter_DisabledCases(t *testing.T) {
	cases := []struct {
		name  string
		peers string
		self  string
	}{
		{"empty peers", "", "cloud-0"},
		{"single peer", "cloud-0@a:8000", "cloud-0"},
		{"self not in set", "cloud-0@a:8000,cloud-1@b:8000", "cloud-9"},
		{"no self", "cloud-0@a:8000,cloud-1@b:8000", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &cloud.Config{ShardPeers: tc.peers, ShardSelf: tc.self}
			if r := newShardRouter(cfg, testLog(), nil); r != nil {
				t.Fatalf("newShardRouter(%q, %q) = non-nil, want nil (sharding off)", tc.peers, tc.self)
			}
		})
	}
	// Enabled: 3 peers, self in set.
	cfg := &cloud.Config{ShardPeers: "cloud-0@a:8000,cloud-1@b:8000,cloud-2@c:8000", ShardSelf: "cloud-1"}
	r := newShardRouter(cfg, testLog(), nil)
	if r == nil {
		t.Fatalf("newShardRouter with valid 3-peer set returned nil")
	}
	if r.self != "cloud-1" || len(r.peers) != 3 {
		t.Fatalf("router self=%q peers=%d, want cloud-1/3", r.self, len(r.peers))
	}
}

// TestShardRouterLiveMembership: with a live-members source (durable plane on) the router
// elects over the CURRENT set, so removing a pod re-homes its orgs to a live successor,
// and the router activates even with no CLOUD_PEERS (a Deployment's random pod names).
func TestShardRouterLiveMembership(t *testing.T) {
	var live []ha.Member
	src := func() []ha.Member { return live }

	// No CLOUD_PEERS, but a live source + a stable self ⇒ routing is ON.
	cfg := &cloud.Config{ShardPeers: "", ShardSelf: "cloud-1"}
	r := newShardRouter(cfg, testLog(), src)
	if r == nil {
		t.Fatal("live membership must activate routing even without CLOUD_PEERS")
	}

	// Full set: every org has SOME owner drawn from the live members.
	live = []ha.Member{{ID: "cloud-0", Addr: "10.0.0.0:8080"}, {ID: "cloud-1", Addr: "10.0.0.1:8080"}, {ID: "cloud-2", Addr: "10.0.0.2:8080"}}
	owner3, ok := ha.Owner("acme", r.set())
	if !ok {
		t.Fatal("owner over the live set must resolve")
	}

	// Drain the elected owner out of the live set: the org re-homes to a DIFFERENT live pod
	// (never the drained one) — the routing half of zero-downtime.
	live = removeMember(live, owner3.ID)
	owner2, ok := ha.Owner("acme", r.set())
	if !ok || owner2.ID == owner3.ID {
		t.Fatalf("after draining %s the org must re-home to a live pod, got %q (ok=%v)", owner3.ID, owner2.ID, ok)
	}

	// Momentarily-empty live snapshot falls back to the static peers, never stranding.
	live = nil
	if got := r.set(); len(got) != len(r.peers) {
		t.Fatalf("empty live snapshot must fall back to static peers (%d), got %d", len(r.peers), len(got))
	}
}

func removeMember(ms []ha.Member, id string) []ha.Member {
	out := ms[:0:0]
	for _, m := range ms {
		if m.ID != id {
			out = append(out, m)
		}
	}
	return out
}

func TestParsePeers(t *testing.T) {
	got := parsePeers("cloud-0@cloud-0.cloud.hanzo.svc:8000, cloud-1@cloud-1.cloud.hanzo.svc:8000 ,cloud-2@c:8000")
	if len(got) != 3 {
		t.Fatalf("parsed %d peers, want 3: %v", len(got), got)
	}
	if got[0].ID != "cloud-0" || got[0].Addr != "cloud-0.cloud.hanzo.svc:8000" {
		t.Fatalf("peer0 = %+v", got[0])
	}
	// Bare id → addr == id (dev/local).
	bare := parsePeers("solo")
	if len(bare) != 1 || bare[0].ID != "solo" || bare[0].Addr != "solo" {
		t.Fatalf("bare parse = %+v", bare)
	}
	if parsePeers("  ") != nil {
		t.Fatalf("whitespace-only should parse to nil")
	}
	// Malformed entries (empty id/addr) are dropped, not silently mis-parsed.
	if p := parsePeers("@addr,id@,ok@a:1"); len(p) != 1 || p[0].ID != "ok" {
		t.Fatalf("malformed filtering = %+v, want only ok", p)
	}
}

func TestPeersContain(t *testing.T) {
	peers := threePeers("a", "b", "c")
	if !peersContain(peers, "cloud-1") {
		t.Fatalf("cloud-1 should be a member")
	}
	if peersContain(peers, "cloud-9") {
		t.Fatalf("cloud-9 must not be a member")
	}
}

// TestIsPeerDialError: only never-delivered dial failures are retryable; a delivered
// response failure (ambiguous) is not, so a non-idempotent POST is never double-sent.
func TestIsPeerDialError(t *testing.T) {
	if !isPeerDialError(fasthttp.ErrDialTimeout) {
		t.Fatalf("dial timeout should be retryable")
	}
	if !isPeerDialError(fasthttp.ErrNoFreeConns) {
		t.Fatalf("no free conns should be retryable")
	}
	if isPeerDialError(nil) {
		t.Fatalf("nil is not an error")
	}
	if isPeerDialError(fmt.Errorf("unexpected EOF after headers")) {
		t.Fatalf("a delivered-then-failed response must NOT be retried")
	}
	if !isPeerDialError(fmt.Errorf("dial tcp 10.0.0.1:8000: connect: connection refused")) {
		t.Fatalf("connection refused should be retryable")
	}
}
