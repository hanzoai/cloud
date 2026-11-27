package tenure

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	s3driver "github.com/hanzoai/cloud/internal/driver/s3"
	"github.com/hanzoai/vfs/replica"
)

// red5 probes for the Owned()-reads-the-lease change (8e7b132a32).

var errRed5Down = errors.New("red5: store unreachable from this pod")

// partStore is one pod's path to the shared object store: it can be cut, hang,
// and counts reads.
type partStore struct {
	inner replica.ConditionalStore
	down  atomic.Bool
	hang  atomic.Bool
	gets  atomic.Int64
	puts  atomic.Int64
}

func (p *partStore) Get(ctx context.Context, key string) ([]byte, string, error) {
	p.gets.Add(1)
	if p.hang.Load() {
		<-ctx.Done()
		return nil, "", ctx.Err()
	}
	if p.down.Load() {
		return nil, "", errRed5Down
	}
	return p.inner.Get(ctx, key)
}

func (p *partStore) PutIfVersion(ctx context.Context, key string, data []byte, v string) (string, error) {
	p.puts.Add(1)
	if p.hang.Load() {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if p.down.Load() {
		return "", errRed5Down
	}
	return p.inner.PutIfVersion(ctx, key, data, v)
}

// orgElecting returns an org whose HRW owner over set is want.
func orgElecting(t *testing.T, set []Member, want string) string {
	for i := range 1000 {
		o := fmt.Sprintf("org%d", i)
		if electedOwnerID(o, set) == want {
			return o
		}
	}
	t.Fatal("no org elects " + want)
	return ""
}

type red5Pod struct {
	dy    *Durability
	store *partStore
	d     *Durable
	dbKey string
	org   string
	dir   string
}

func newRed5Pod(t *testing.T, cs replica.ConditionalStore, id string, set []Member, orgID string, opts ...DurabilityOption) *red5Pod {
	ps := &partStore{inner: cs}
	dy := NewDurability(ps, stubView{id: id, set: set}, nil, opts...)
	dbKey := replica.DBPath(orgID, "", "research")
	p := &red5Pod{dy: dy, store: ps, dbKey: dbKey, org: orgID, dir: t.TempDir()}
	p.d = dy.For(testNS(orgID), "research", dbKey, p.dir+"/research.db")
	return p
}

// A pod partitioned from the KV and the object store is dropped from its peer's
// live set (its heartbeat stopped), and the peer takes its org at a higher round.
// The partitioned pod's own set holds still, so it still elects itself, and the
// store it cannot reach leaves its last answer standing: both pods answer Owned.
func TestRed5PartitionedDeposedPodOwnsForEver(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	both := []Member{{ID: "cloud-0"}, {ID: "cloud-1"}}
	orgID := orgElecting(t, both, "cloud-0")

	var aLive atomic.Bool
	aLive.Store(true)
	a := newRed5Pod(t, cs, "cloud-0", both, orgID, WithLive(aLive.Load)) // A's set held still through its outage
	if err := a.d.Hydrate(ctx); err != nil {
		t.Fatal(err)
	}
	if !a.d.Owned() {
		t.Fatal("a does not own its org before the partition")
	}
	a.store.down.Store(true) // A loses the store (and the KV: B no longer sees it)
	aLive.Store(false)       // so A's own record has lapsed from the set B reads

	b := newRed5Pod(t, cs, "cloud-1", []Member{{ID: "cloud-1"}}, orgID)
	if err := b.d.Hydrate(ctx); err != nil {
		t.Fatal(err)
	}
	both2 := 0
	for range 50 {
		if a.d.Owned() && b.d.Owned() {
			both2++
		}
	}
	t.Logf("50 checks during the partition: both pods Owned on %d", both2)
	if both2 > 0 {
		t.Errorf("a deposed, partitioned pod keeps Owned()=true on its last answer (%d/50) while the new owner acts too", both2)
	}
}

// Two pods whose live sets each hold only themselves (a KV that reads but refuses
// writes, or a pod booted during a KV outage) each elect themselves. Owned now
// demotes the loser on its next call, and the next Each/For re-promotes it, which
// bumps the round and demotes the other: one full re-hydrate per pod per pass.
func TestRed5DivergentViewsFlapEveryPass(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	orgID := "acme"
	pods := []*red5Pod{
		newRed5Pod(t, cs, "cloud-0", []Member{{ID: "cloud-0"}}, orgID),
		newRed5Pod(t, cs, "cloud-1", []Member{{ID: "cloud-1"}}, orgID),
	}
	for _, p := range pods {
		if err := p.d.Hydrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	promotions := 0
	for range 10 { // ten loop passes on each pod
		for _, p := range pods {
			if p.d.Owned() {
				continue
			}
			if !p.d.PendingPromotion() {
				continue
			}
			ok, err := p.d.TryClaim(ctx)
			if err != nil || !ok {
				continue
			}
			// openDurable: a fresh Durable hydrates under the claimed lease.
			p.d = p.dy.For(testNS(orgID), "research", p.dbKey, p.dir+"/research.db")
			if err := p.d.Hydrate(ctx); err != nil {
				t.Fatal(err)
			}
			promotions++
		}
	}
	rec, _, _, _ := NewCASFencer(cs, stubView{}).readLease(ctx, leaseKey(orgID))
	t.Logf("10 passes x 2 pods: %d promotions (each a TryClaim CAS + CarryForward re-hydrate), lease round now %d, cloud-0 puts=%d cloud-1 puts=%d",
		promotions, rec, pods[0].store.puts.Load(), pods[1].store.puts.Load())
	if promotions > 2 {
		t.Errorf("divergent views flap: %d promotions in 10 passes", promotions)
	}
}

// Owned was a lock and an HRW (sandbox/meter.go settle says so). Now each call is
// one object-store GET; against a store that does not answer, each call waits
// ownedReach.
func TestRed5OwnedCostsAReadAndTwoSecondsWhenHung(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	p := newRed5Pod(t, cs, "cloud-0", []Member{{ID: "cloud-0"}}, "acme")
	if err := p.d.Hydrate(ctx); err != nil {
		t.Fatal(err)
	}
	before := p.store.gets.Load()
	for range 100 {
		p.d.Owned()
	}
	t.Logf("100 Owned() calls on one pod: %d object-store GETs", p.store.gets.Load()-before)

	p.store.hang.Store(true)
	start := time.Now()
	var owned bool
	for range 3 {
		owned = p.d.Owned()
	}
	el := time.Since(start)
	t.Logf("3 Owned() calls against a hung store: %v total, answered %v", el, owned)
	if el > 3*time.Second {
		t.Errorf("Owned() blocks %v per call on a hung store (was I/O-free)", el/3)
	}
}

// The production driver (hanzos3, 10 retries) against a store that refuses or that
// accepts and never answers: how long one Owned() (one Holder read) takes.
func TestRed5HoldsAgainstARealS3ClientOutage(t *testing.T) {
	refused, _ := net.Listen("tcp", "127.0.0.1:0")
	refusedAddr := refused.Addr().String()
	refused.Close()
	hole, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hole.Close()
	go func() {
		for {
			c, err := hole.Accept()
			if err != nil {
				return
			}
			_ = c // hold it open, answer nothing
		}
	}()
	for _, tc := range []struct{ name, addr string }{{"refused", refusedAddr}, {"blackhole", hole.Addr().String()}} {
		t.Setenv("S3_ADMIN_ENDPOINT", tc.addr)
		t.Setenv("S3_ADMIN_ACCESS_KEY", "ak")
		t.Setenv("S3_ADMIN_SECRET_KEY", "sk")
		t.Setenv("S3_SECURE", "false")
		store, err := s3driver.Open(s3driver.New())
		if err != nil {
			t.Fatal(err)
		}
		f := NewCASFencer(NewConditional(store, "durable"), stubView{id: "cloud-0", set: []Member{{ID: "cloud-0"}}})
		ctx, cancel := context.WithTimeout(context.Background(), ownedReach)
		start := time.Now()
		_, _, herr := f.Holder(ctx, "acme")
		cancel()
		t.Logf("%s store: one Holder read took %v (err=%v)", tc.name, time.Since(start).Round(time.Millisecond), herr != nil)
	}
}
