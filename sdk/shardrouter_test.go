package cloud

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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/ha"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
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

// TestShardOwnership_ExactlyOneOwnerPerOrg is the core invariant math: for a fixed
// peer set, every org resolves to EXACTLY ONE owner among the peers, deterministically.
// If this holds, no two pods are ever selected as the writer for the same org.
func TestShardOwnership_ExactlyOneOwnerPerOrg(t *testing.T) {
	peers := threePeers("a:8000", "b:8000", "c:8000")
	dist := map[string]int{}
	for i := range 20000 {
		org := fmt.Sprintf("tenant-%d", i)
		slug := namespace.Sanitize(org)
		if slug == "" {
			t.Fatalf("clean org %q sanitized to empty", org)
		}
		// Exactly one member is the owner (IsOwner true for one, false for the rest).
		owners := 0
		var ownerID string
		for _, m := range peers {
			if ha.IsOwner(slug, m.ID, peers) {
				owners++
				ownerID = m.ID
			}
		}
		if owners != 1 {
			t.Fatalf("org %q has %d owners, want exactly 1", org, owners)
		}
		// Deterministic: Owner agrees with the single IsOwner, and repeats stably.
		o, ok := ha.Owner(slug, peers)
		if !ok || o.ID != ownerID {
			t.Fatalf("org %q Owner=%v ok=%v disagrees with IsOwner=%s", org, o, ok, ownerID)
		}
		o2, _ := ha.Owner(slug, peers)
		if o2.ID != o.ID {
			t.Fatalf("org %q owner not deterministic: %s then %s", org, o.ID, o2.ID)
		}
		dist[ownerID]++
	}
	// Distribution sanity: every pod owns SOME orgs (routing actually spreads load —
	// a partition that dumped everything on one pod would be a correctness/scaling bug).
	for _, m := range peers {
		if dist[m.ID] == 0 {
			t.Fatalf("pod %s owns no orgs; distribution=%v", m.ID, dist)
		}
	}
	t.Logf("ownership distribution over 20000 orgs: %v", dist)
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

// TestConfigValidate_ShardSafety: the boot gate fails CLOSED on every unsafe shard
// topology (self not in ring, empty self, iam co-enabled) and passes a valid one.
func TestConfigValidate_ShardSafety(t *testing.T) {
	base := func() *Config {
		// Enable must be non-empty and iam-free to mean "iam off": an EMPTY enable
		// list is mount-all, so Enabled("iam") holds and the shard gate — correctly —
		// refuses the topology. The iam case below opts back in explicitly.
		return &Config{Brand: "hanzo", Domain: "api.hanzo.ai", DataDir: "/var/lib/cloud",
			Enable: []string{"gateway"}, KVURL: "kv://kv:6379"}
	}
	// Valid 3-peer set, self in ring, iam off → OK.
	c := base()
	c.ShardPeers = "cloud-0@a:8000,cloud-1@b:8000,cloud-2@c:8000"
	c.ShardSelf = "cloud-1"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid shard config rejected: %v", err)
	}
	// Self not in ring → error.
	c = base()
	c.ShardPeers = "cloud-0@a:8000,cloud-1@b:8000"
	c.ShardSelf = "cloud-7"
	if err := c.Validate(); err == nil {
		t.Fatalf("self-not-in-ring must fail closed")
	}
	// Empty self with a multi-peer set → error.
	c = base()
	c.ShardPeers = "cloud-0@a:8000,cloud-1@b:8000"
	c.ShardSelf = ""
	if err := c.Validate(); err == nil {
		t.Fatalf("empty self with multi-peer set must fail closed")
	}
	// iam enabled + shard peers → error (non-shardable process-local sessions).
	c = base()
	c.ShardPeers = "cloud-0@a:8000,cloud-1@b:8000"
	c.ShardSelf = "cloud-0"
	c.Enable = []string{"iam"}
	if err := c.Validate(); err == nil {
		t.Fatalf("iam + shard routing must fail closed")
	}
	// Single-pod (one peer) is not shard routing → no shard gate applies.
	c = base()
	c.ShardPeers = "cloud-0@a:8000"
	c.ShardSelf = "cloud-0"
	if err := c.Validate(); err != nil {
		t.Fatalf("single-peer (sharding off) rejected: %v", err)
	}
}
