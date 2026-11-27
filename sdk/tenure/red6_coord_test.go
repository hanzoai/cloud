package tenure

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/namespace"
	"github.com/hanzoai/vfs/replica"
)

// red6: the real Coord (mem backend) + Membership + Durability wired as
// build.go wires them (WithWriters(declared), WithLive(c.Live)), driven through
// faults. No stubbed liveness.

type fault struct {
	coord.Backend
	rOff, wOff, noLeave atomic.Bool
}

func (f *fault) Join(ctx context.Context, set string, r coord.Record, ttl time.Duration, force bool) (*coord.Record, error) {
	if f.wOff.Load() {
		return nil, coord.ErrUnavailable
	}
	return f.Backend.Join(ctx, set, r, ttl, force)
}

func (f *fault) Leave(ctx context.Context, set, id, instance string) error {
	if f.noLeave.Load() || f.wOff.Load() {
		return nil
	}
	return f.Backend.Leave(ctx, set, id, instance)
}

func (f *fault) Members(ctx context.Context, set string, ttl time.Duration) ([]coord.Record, error) {
	if f.rOff.Load() {
		return nil, coord.ErrUnavailable
	}
	return f.Backend.Members(ctx, set, ttl)
}

// counting is one pod's path to the shared object store: cuttable, counts GETs.
type counting struct {
	replica.ConditionalStore
	off  atomic.Bool
	gets atomic.Int64
}

func (c *counting) Get(ctx context.Context, key string) ([]byte, string, error) {
	c.gets.Add(1)
	if c.off.Load() {
		return nil, "", fmt.Errorf("partitioned")
	}
	return c.ConditionalStore.Get(ctx, key)
}

func (c *counting) PutIfVersion(ctx context.Context, key string, data []byte, v string) (string, error) {
	if c.off.Load() {
		return "", fmt.Errorf("partitioned")
	}
	return c.ConditionalStore.PutIfVersion(ctx, key, data, v)
}

type r6org struct {
	id string
	ns namespace.Namespace
	d  *Durable
}

type r6node struct {
	id         string
	c          *coord.Coord
	f          *fault
	m          *Membership
	dy         *Durability
	st         *counting
	dir        string
	orgs       map[string]*r6org
	promotions int
}

func r6newNode(t *testing.T, kv coord.Backend, id string, cs replica.ConditionalStore, declared int, orgs ...string) *r6node {
	t.Helper()
	f := &fault{Backend: kv}
	return r6newNodeF(t, f, id, cs, declared, orgs...)
}

func r6newNodeF(t *testing.T, f *fault, id string, cs replica.ConditionalStore, declared int, orgs ...string) *r6node {
	t.Helper()
	c := podOf(t, f, id, true, declared)
	m := NewMembership(id, func(context.Context) ([]Member, error) {
		var out []Member
		for _, x := range c.Members() {
			out = append(out, Member(x))
		}
		return out, nil
	}, 2*time.Second)
	_ = m.Start(context.Background())
	t.Cleanup(m.Stop)
	st := &counting{ConditionalStore: cs}
	opts := []DurabilityOption{WithWriters(declared)}
	if c.Shared() {
		opts = append(opts, WithLive(c.Live))
	}
	n := &r6node{id: id, c: c, f: f, m: m, st: st, dir: t.TempDir(), orgs: map[string]*r6org{}}
	n.dy = NewDurability(st, m, nil, opts...)
	for _, o := range orgs {
		ro := &r6org{id: o, ns: namespace.MustOrgProject(o, "")}
		ro.d = n.open(ro)
		_ = ro.d.Hydrate(context.Background())
		n.orgs[o] = ro
	}
	return n
}

func (n *r6node) open(o *r6org) *Durable {
	return n.dy.For(o.ns, "research", replica.DBPath(o.id, "", "research"), n.dir+"/"+o.id+".db")
}

// step is one OrgStore pass for org o: Owned, else PendingPromotion -> claim ->
// reopen + hydrate (as orgdb promotion does).
func (n *r6node) step(o string) bool {
	ro := n.orgs[o]
	if ro.d.Owned() {
		return true
	}
	if !ro.d.PendingPromotion() {
		return false
	}
	ok, err := ro.d.TryClaim(context.Background())
	if err != nil || !ok {
		return false
	}
	ro.d = n.open(ro)
	if err := ro.d.Hydrate(context.Background()); err != nil {
		return false
	}
	n.promotions++
	return ro.d.Owned()
}

func r6orgElecting(t *testing.T, set []Member, want string, skip ...string) string {
	for i := range 5000 {
		o := fmt.Sprintf("org%d", i)
		if slices.Contains(skip, o) {
			continue
		}
		if w, _ := Owner(o, set); w.ID == want {
			return o
		}
	}
	t.Fatal("no org elects " + want)
	return ""
}

func r6converge(t *testing.T, ns ...*r6node) {
	t.Helper()
	eventually(t, 10*time.Second, "views never converged", func() bool {
		for _, n := range ns {
			if len(n.m.Members()) != len(ns) || len(n.c.Members()) != len(ns) {
				return false
			}
		}
		return true
	})
}

type r6stats struct {
	both, none, samples int
	lastA, firstB       time.Duration
}

// A pod cut from the KV AND the object store must stop acting before its peer
// claims its  Measured on the real Coord.Live, not a stub.
func TestRed6PartitionStopsBeforePeerClaims(t *testing.T) {
	t.Parallel()
	kv := coord.NewMemory().Backend()
	cs := newCAS()
	two := []Member{{ID: "cloud-0"}, {ID: "cloud-1"}}
	o := r6orgElecting(t, two, "cloud-0")
	a := r6newNode(t, kv, "cloud-0", cs, 2)
	b := r6newNode(t, kv, "cloud-1", cs, 2)
	r6converge(t, a, b)
	for _, n := range []*r6node{a, b} {
		ro := &r6org{id: o, ns: namespace.MustOrgProject(o, "")}
		ro.d = n.open(ro)
		_ = ro.d.Hydrate(context.Background())
		n.orgs[o] = ro
	}
	for range 40 {
		a.step(o)
		b.step(o)
		time.Sleep(50 * time.Millisecond)
	}
	if !a.step(o) || b.step(o) {
		t.Fatalf("steady: a owns=%v b owns=%v", a.step(o), b.step(o))
	}
	t0 := time.Now()
	a.f.rOff.Store(true)
	a.f.wOff.Store(true)
	a.st.off.Store(true)
	var s r6stats
	s.firstB = -1
	// Watched until the peer has claimed and for three more lapses of a heartbeat
	// after it: the cut pod stays cut, so what it does next it does in that time.
	const lapse = presenceTTL + skew
	for since := time.Since(t0); since < 20*lapse && (s.firstB < 0 || since < s.firstB+3*lapse); since = time.Since(t0) {
		ao, bo := a.step(o), b.step(o)
		s.samples++
		if ao {
			s.lastA = time.Since(t0)
		}
		if bo && s.firstB < 0 {
			s.firstB = time.Since(t0)
		}
		if ao && bo {
			s.both++
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("partition: A last Owned at %v, B first Owned at %v, both=%d/%d, A live=%v", s.lastA, s.firstB, s.both, s.samples, a.c.Live())
	if s.both > 0 || s.firstB < 0 || s.lastA >= s.firstB {
		t.Errorf("partitioned pod overlapped its successor: lastA=%v firstB=%v both=%d", s.lastA, s.firstB, s.both)
	}
}

// One writer: Owned asks the store nothing.
func TestRed6SoloOwnedIsIOFree(t *testing.T) {
	t.Parallel()
	kv := coord.NewMemory().Backend()
	cs := newCAS()
	a := r6newNode(t, kv, "cloud-0", cs, 1, "acme")
	time.Sleep(2500 * time.Millisecond)
	g := a.st.gets.Load()
	owned := 0
	for range 1000 {
		if a.orgs["acme"].d.Owned() {
			owned++
		}
	}
	a.st.off.Store(true)
	start := time.Now()
	for range 100 {
		if a.orgs["acme"].d.Owned() {
			owned++
		}
	}
	t.Logf("solo: %d/1100 owned, %d GETs, 100 calls with store cut took %v", owned, a.st.gets.Load()-g, time.Since(start))
	if a.st.gets.Load() != g || owned != 1100 {
		t.Errorf("solo Owned is not I/O-free / not owned")
	}
}

// One writer, its predecessor crashed (no leave) under a different pod name less
// than a heartbeat ago, and the KV refuses this pod's writes as it boots. Does the
// one pod still count itself? (The shard router forwards to whoever Members names.)
func TestRed6SoloBootsBesideACrashedPredecessor(t *testing.T) {
	for _, ro := range []bool{false, true} {
		t.Run(fmt.Sprintf("readonly=%v", ro), func(t *testing.T) {
			t.Parallel()
			kv := coord.NewMemory().Backend()
			fo := &fault{Backend: kv}
			old := podOf(t, fo, "cloud-old", true, 1)
			eventually(t, 2*time.Second, "old never joined", func() bool { return len(old.Members()) == 1 })
			fo.noLeave.Store(true)
			fo.wOff.Store(true) // its last heartbeat stands until it lapses
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			old.Close(ctx)
			cancel()
			fn := &fault{Backend: kv}
			fn.wOff.Store(true) // this pod's join fails at boot
			n := podOf(t, fn, "cloud-new", true, 1)
			if !ro {
				go func() { time.Sleep(300 * time.Millisecond); fn.wOff.Store(false) }() // a hiccup, not a fault
			}
			// Watched past the lapse of the predecessor's last heartbeat, and as long again.
			const watch = 2 * (presenceTTL + skew)
			t0 := time.Now()
			var without time.Duration
			for time.Since(t0) < watch {
				if !slices.Contains(ids(n.Members()), "cloud-new") {
					without += 20 * time.Millisecond
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Logf("readonly=%v: over %v the one pod was absent from its own live set for ~%v; now Members=%v",
				ro, watch, without, ids(n.Members()))
			if slices.Contains(ids(n.Members()), "cloud-old") && !slices.Contains(ids(n.Members()), "cloud-new") {
				t.Errorf("one pod's live set names only its dead predecessor: every org request is forwarded to it")
			}
		})
	}
}

// The periods every Coord of these tests keeps, so a lease changes hands in a
// second rather than a minute.
const (
	presenceTTL = time.Second
	skew        = 500 * time.Millisecond
)

var fast = coord.Periods{
	Term: 600 * time.Millisecond, Effect: 150 * time.Millisecond, Confirm: 50 * time.Millisecond, Watch: 10 * time.Millisecond,
	Presence: presenceTTL, Skew: skew, Refresh: 50 * time.Millisecond, Open: 300 * time.Millisecond,
}

// podOf is one pod's root over backend b, sharing it as a KV would, in a deployment
// that declares the given writers.
func podOf(t *testing.T, b coord.Backend, id string, root bool, declared int) *coord.Coord {
	t.Helper()
	c := coord.OnBackend(b, coord.Config{Periods: fast,
		Deployment: "test",
		Self:       coord.Member{ID: id, Addr: id + ":8443"},
		Instance:   id + "-host",
		Secret:     bytes.Repeat([]byte{7}, 32),
		Root:       root,
		Declared:   declared,
	})
	c.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		c.Close(ctx)
	})
	return c
}

func eventually(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("after %v: %s", d, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func ids(ms []coord.Member) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

// casStore is an object store with compare-and-set, in memory.
type casStore struct {
	mu   sync.Mutex
	objs map[string]casObj
}

type casObj struct {
	data    []byte
	version int
}

var (
	errMissing  = errors.Join(coord.ErrMissing, replica.ErrNotFound)
	errConflict = errors.Join(coord.ErrConflict, replica.ErrConflict)
)

func newCAS() *casStore { return &casStore{objs: map[string]casObj{}} }

func (s *casStore) Get(_ context.Context, key string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objs[key]
	if !ok {
		return nil, "", errMissing
	}
	return append([]byte(nil), o.data...), strconv.Itoa(o.version), nil
}

func (s *casStore) PutIfVersion(_ context.Context, key string, data []byte, expect string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objs[key]
	cur := ""
	if ok {
		cur = strconv.Itoa(o.version)
	}
	if cur != expect {
		return "", errConflict
	}
	o = casObj{data: append([]byte(nil), data...), version: o.version + 1}
	s.objs[key] = o
	return strconv.Itoa(o.version), nil
}
