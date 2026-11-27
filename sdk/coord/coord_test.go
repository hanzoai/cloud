package coord

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// down is a KV that answers nothing: down at boot, or refusing every write and read.
type down struct{ err error }

func (d down) Join(context.Context, string, Record, time.Duration, bool) (*Record, error) {
	return nil, d.err
}
func (d down) Leave(context.Context, string, string, string) error { return d.err }
func (d down) Members(context.Context, string, time.Duration) ([]Record, error) {
	return nil, d.err
}
func (d down) Spend(context.Context, []string, []int, int, time.Duration) (int, time.Time, error) {
	return 0, time.Time{}, d.err
}
func (d down) Take(context.Context, string, int) (bool, error)           { return false, d.err }
func (d down) Once(context.Context, string, time.Duration) (bool, error) { return false, d.err }
func (d down) Quota(context.Context, []string, []int, []time.Duration) ([]int, []time.Duration, bool, error) {
	return nil, nil, false, d.err
}
func (d down) Counted(context.Context, []string) ([]int, error) { return nil, d.err }
func (d down) Unquota(context.Context, []string) error          { return d.err }
func (d down) Span(context.Context, string, int64, int64, bool) (int64, error) {
	return 0, d.err
}
func (d down) Commit(context.Context, string, string, int64, time.Duration) (int64, error) {
	return 0, d.err
}
func (d down) Uncommit(context.Context, string, string, int64) error { return d.err }
func (d down) Committed(context.Context, string) (int64, error)      { return 0, d.err }

// A POD THAT CANNOT READ THE LIVE SET IS STILL IN IT. Reading an empty set as "no
// pods" made every org's fence refuse (no members, no owner) the moment the KV was
// down at boot or refused writes; this pod is a member unless it is leaving.
func TestTheLiveSetAlwaysHoldsThisPod(t *testing.T) {
	c := solo(t, down{ErrUnavailable}, "cloud-0", nil)
	if got := ids(c.Members()); !slices.Equal(got, []string{"cloud-0"}) {
		t.Fatalf("with the KV down: members %v, want this pod", got)
	}
	if c.Peered() {
		t.Fatal("one pod whose KV is down reads as peered")
	}
	if c.Err() != nil {
		t.Fatalf("a KV that does not answer at boot refused the pod: %v", c.Err())
	}
	c.Leave(context.Background())
	if got := c.Members(); len(got) != 0 {
		t.Fatalf("a leaving pod still counts itself: %v", ids(got))
	}
}

// PEERED IS WHAT THE DEPLOYMENT DECLARES. One pod with a KV is one writer: Base
// mounts and an org store acknowledges its writes. A second pod in the live set
// changes nothing — the set ages out within a heartbeat of a KV outage, and a pod
// that read that as proof it was alone would act beside a peer doing the same —
// while a deployment that declares two writers is peered from its first instant.
func TestPeeredIsWhatTheDeploymentDeclares(t *testing.T) {
	kv := newMem()
	a := solo(t, kv, "cloud-0", nil)
	if a.Peered() {
		t.Fatal("one pod with a shared KV reads as peered")
	}
	solo(t, kv, "cloud-1", nil)
	eventually(t, time.Second, "the second pod never appeared in the live set", func() bool { return len(a.Members()) == 2 })
	if a.Peered() {
		t.Fatal("a second live pod nothing declared made the first peered")
	}
	if b := pod(t, newMem(), "cloud-2", true, nil); !b.Peered() {
		t.Fatal("two declared writers do not read as peered")
	}
}

// ONLY THE ROOT JOINS; A PROCESS IT SPAWNS NEVER TAKES THE POD WITH IT. Every app
// process joining under the pod's id meant the idle reaper stopping one deleted the
// shared entry, and the address flipped to whichever process heartbeated last.
func TestAChildLeavingTakesNothing(t *testing.T) {
	kv := newMem()
	root := pod(t, kv, "cloud-0", true, nil)
	other := pod(t, kv, "cloud-1", true, nil)
	for range 3 {
		child := pod(t, kv, "cloud-0", false, nil)
		eventually(t, time.Second, "a child never read the live set", func() bool { return len(child.Members()) == 2 })
		child.Close(context.Background())
	}
	time.Sleep(3 * refreshEvery)
	if got := ids(other.Members()); !slices.Equal(got, []string{"cloud-0", "cloud-1"}) {
		t.Fatalf("after three children of cloud-0 came and went its peer sees %v", got)
	}
	rs, _ := kv.Members(context.Background(), root.Key("set", membersSet), presenceTTL)
	if len(rs) != 2 || rs[0].Addr == "" || rs[1].Addr == "" {
		t.Fatalf("the live set holds %+v", rs)
	}
}

// A MEMBER NOBODY HOLDING THE MASTER SIGNED IS NOBODY. The KV has no password; the
// shard router forwards whole requests, credentials included, to whatever the live
// set names. Unsigned, signed under another deployment's key, or replayed past its
// heartbeat: none of them is a member.
func TestAMemberNobodySignedIsIgnored(t *testing.T) {
	kv := newMem()
	c := solo(t, kv, "cloud-0", nil)
	set := c.Key("set", membersSet)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	_, _ = kv.Join(ctx, set, Record{ID: "planted", Addr: "203.0.113.9:8443", Instance: "x", At: now, Sig: "00"}, presenceTTL, false)
	foreign := over(newMem(), newMem(), true, Config{Periods: fast, Self: Member{ID: "foreign", Addr: "203.0.113.10:8443"}, Instance: "y", Secret: []byte("another deployment's master key")})
	_, _ = kv.Join(ctx, set, foreign.record(), presenceTTL, false)
	stale := Record{ID: "replayed", Addr: "203.0.113.11:8443", Instance: "z", At: now - (presenceTTL + skew + time.Second).Milliseconds()}
	stale.Sig = c.sign(stale)
	_, _ = kv.Join(ctx, set, stale, presenceTTL, false)
	c.refresh(ctx)
	if got := ids(c.Members()); !slices.Equal(got, []string{"cloud-0"}) {
		t.Fatalf("planted members were taken as live: %v", got)
	}
	if c.Peered() {
		t.Fatal("planted members made one pod peered")
	}
}

// TWO PODS UNDER ONE ID ARE REFUSED: the second does not serve.
func TestADuplicatePodIsRefused(t *testing.T) {
	kv := newMem()
	pod(t, kv, "cloud-0", true, nil)
	twin := over(kv, newMem(), true, Config{Periods: fast, Deployment: "test", Self: Member{ID: "cloud-0", Addr: "elsewhere:8443"}, Instance: "other-host", Secret: secret, Root: true})
	twin.Start(context.Background())
	defer twin.Close(context.Background())
	if !errors.Is(twin.Err(), ErrDuplicate) {
		t.Fatalf("a second pod under a live pod's id: err %v", twin.Err())
	}
}

// ONE PROCESS RUNS A LOOP, AND IT IS THE STORE'S ROUND THAT SAYS WHICH.
func TestOneProcessHoldsALoop(t *testing.T) {
	t.Parallel()
	kv, st := newMem(), newCAS()
	var pods []*Coord
	for _, id := range []string{"cloud-0", "cloud-1", "cloud-2"} {
		pods = append(pods, pod(t, kv, id, true, st))
	}
	var leads []*Leader
	for _, p := range pods {
		leads = append(leads, p.Lead("sweep"))
	}
	held := func() (n int) {
		for _, l := range leads {
			if l.Hold(context.Background()) == nil {
				n++
			}
		}
		return n
	}
	eventually(t, 2*time.Second, "no process came to hold the loop", func() bool { return held() == 1 })
	for range 20 {
		if n := held(); n > 1 {
			t.Fatalf("%d processes hold one loop", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// THE FIRST PASS IS NOT LOST to a claim still in flight.
func TestTheFirstPassRuns(t *testing.T) {
	c := pod(t, newMem(), "cloud-0", true, newCAS())
	ran := false
	if err := c.Lead("sweep").Do(context.Background(), func(context.Context) { ran = true }); err != nil || !ran {
		t.Fatalf("the first pass of a free loop: ran=%v err=%v", ran, err)
	}
}

// A PASS NEVER OUTLIVES ITS LEASE. The holder is cut off from the store while its
// pass runs, so its renewals stop landing; another pod takes the loop once the
// lease has gone a term unrenewed. Every effect the old holder makes ends before the
// new holder's first.
func TestAPassNeverOutlivesItsLease(t *testing.T) {
	t.Parallel()
	kv, st := newMem(), newCAS()
	view := &cut{ConditionalStore: st}
	a := pod(t, kv, "cloud-0", true, view)
	la := a.Lead("meter")
	if err := la.Do(context.Background(), func(context.Context) {}); err != nil {
		t.Fatalf("cloud-0 does not hold a free loop: %v", err)
	}
	b := pod(t, kv, "cloud-1", true, st)
	lb := b.Lead("meter")
	fx := &effects{}
	passA := make(chan error, 1)
	go func() { passA <- la.Do(context.Background(), work(la, "cloud-0", fx, 5*time.Millisecond)) }()
	time.Sleep(100 * time.Millisecond)
	round0, _ := st.lease("meter")
	view.off.Store(true) // cloud-0's node loses the store; it is still alive and live
	passB := make(chan error, 1)
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := lb.Do(ctx, work(lb, "cloud-1", fx, 5*time.Millisecond))
			cancel()
			if err == nil {
				passB <- nil
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	select {
	case <-passA:
	case <-time.After(3 * time.Second):
		t.Fatal("the old holder's pass never ended")
	}
	select {
	case <-passB:
	case <-time.After(5 * time.Second):
		t.Fatal("the new holder never ran")
	}
	round1, owner := st.lease("meter")
	if round1 <= round0 || owner != b.owner {
		t.Fatalf("lease went from round %d to %d held by %q; want a higher round held by cloud-1", round0, round1, owner)
	}
	if len(fx.by("cloud-0")) == 0 || len(fx.by("cloud-1")) == 0 {
		t.Fatalf("effects: cloud-0 %d, cloud-1 %d", len(fx.by("cloud-0")), len(fx.by("cloud-1")))
	}
	fx.disjoint(t)
	lastA, firstB := fx.by("cloud-0")[len(fx.by("cloud-0"))-1], fx.by("cloud-1")[0]
	t.Logf("old holder's last effect ended %v before the new holder's first began", firstB.start.Sub(lastA.end).Round(time.Millisecond))
}

// CLOSE DRAINS BEFORE IT LETS GO. A pod shutting down finishes the pass it is in,
// then writes the lease free, and the next holder starts then, not a term later and
// never before.
func TestCloseDrainsPassesBeforeLettingGo(t *testing.T) {
	t.Parallel()
	kv, st := newMem(), newCAS()
	a := pod(t, kv, "cloud-0", true, st)
	la := a.Lead("meter")
	eventually(t, time.Second, "cloud-0 never held the free loop", func() bool { return la.Hold(context.Background()) == nil })
	b := pod(t, kv, "cloud-1", true, st)
	lb := b.Lead("meter")
	fx := &effects{}
	passDone := make(chan time.Time, 1)
	go func() {
		_ = la.Do(context.Background(), func(context.Context) {
			start := time.Now()
			time.Sleep(300 * time.Millisecond) // an effect in flight, deaf to its context
			fx.add("cloud-0", start, time.Now())
		})
		passDone <- time.Now()
	}()
	time.Sleep(50 * time.Millisecond)
	closed := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		a.Close(ctx)
		close(closed)
	}()
	var first time.Time
	eventually(t, 3*time.Second, "cloud-1 never took the loop cloud-0 let go", func() bool {
		if lb.Hold(context.Background()) != nil {
			return false
		}
		first = time.Now()
		return true
	})
	<-closed
	ended := <-passDone
	if first.Before(ended) {
		t.Fatalf("cloud-1 held the loop %v before cloud-0's pass ended", ended.Sub(first))
	}
	if wait := first.Sub(ended); wait > term {
		t.Fatalf("cloud-1 waited %v after a pod that let the loop go; a released lease needs no wait", wait)
	}
}

// ONE FAILED READ OF THE STORE DOES NOT DROP THE LOOP: the holder keeps it for the
// rest of its term, and nobody else takes it from a live holder.
func TestAStoreHiccupKeepsTheLoop(t *testing.T) {
	t.Parallel()
	kv, st := newMem(), newCAS()
	a := pod(t, kv, "cloud-0", true, st)
	la := a.Lead("meter")
	eventually(t, time.Second, "cloud-0 never held the free loop", func() bool { return la.Hold(context.Background()) == nil })
	b := pod(t, kv, "cloud-1", true, st)
	lb := b.Lead("meter")
	eventually(t, time.Second, "cloud-1 never read the lease", func() bool {
		lb.mu.Lock()
		defer lb.mu.Unlock()
		return lb.other
	})
	st.fail(true)
	deadline := time.Now().Add(term - effectBudget - 2*confirmEvery)
	for time.Now().Before(deadline) {
		if la.Hold(context.Background()) != nil {
			t.Fatal("the holder dropped the loop on a store that missed a read")
		}
		time.Sleep(10 * time.Millisecond)
	}
	st.fail(false)
	time.Sleep(3 * confirmEvery)
	if la.Hold(context.Background()) != nil || lb.Hold(context.Background()) == nil {
		t.Fatal("after the store came back the loop is not where it was")
	}
}

// A STORE THAT STAYS DOWN STOPS THE HOLDER at the end of its term where another pod
// is live: a right it cannot confirm is not a right. (A pod alone runs on; see
// red_test.go.)
func TestAStoreThatStaysDownEndsTheTerm(t *testing.T) {
	t.Parallel()
	kv, st := newMem(), newCAS()
	a := pod(t, kv, "cloud-0", true, st)
	pod(t, kv, "cloud-1", true, st)
	la := a.Lead("meter")
	eventually(t, time.Second, "cloud-0 never held the free loop", func() bool { return la.Hold(context.Background()) == nil })
	st.fail(true)
	eventually(t, term, "the holder acted past a term it could not confirm", func() bool { return la.Hold(context.Background()) != nil })
}

// A RESTARTED PROCESS WAITS OUT THE ONE IT REPLACES. The old process died holding
// the loop and never let it go; its successor in the same pod takes it once the
// lease has gone a term without a renewal, and not before.
func TestARestartedProcessWaitsOutItsPredecessor(t *testing.T) {
	t.Parallel()
	kv, st := newMem(), newCAS()
	old := pod(t, kv, "cloud-0", true, st)
	lo := old.Lead("meter")
	eventually(t, time.Second, "the first process never held the loop", func() bool { return lo.Hold(context.Background()) == nil })
	old.closed.Store(true) // killed: it neither drains nor lets go
	close(old.stop)
	died := time.Now()
	succ := pod(t, kv, "cloud-0", false, st)
	ls := succ.Lead("meter")
	ran := time.Time{}
	eventually(t, 2*staleAfter(), "the successor never ran", func() bool {
		return ls.Do(context.Background(), func(context.Context) { ran = time.Now() }) == nil
	})
	if ran.Sub(died) < term {
		t.Fatalf("the successor acted %v after its predecessor died; it must wait out the term (%v)", ran.Sub(died), term)
	}
}

// WHEN THE KV LOSES EVERYTHING the store still holds the round: a takeover raises
// it, so no two processes ever hold one round, and their effects never overlap.
func TestAKVThatLosesEverythingLeavesOneHolder(t *testing.T) {
	t.Parallel()
	kv, st := newSwap(), newCAS()
	a := pod(t, kv, "cloud-0", true, st)
	b := pod(t, kv, "cloud-1", true, st)
	la, lb := a.Lead("meter"), b.Lead("meter")
	fx := &effects{}
	run := func(l *Leader, who string, stop <-chan struct{}) {
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = l.Do(context.Background(), work(l, who, fx, 3*time.Millisecond))
			time.Sleep(5 * time.Millisecond)
		}
	}
	stop := make(chan struct{})
	go run(la, "cloud-0", stop)
	go run(lb, "cloud-1", stop)
	time.Sleep(300 * time.Millisecond)
	before, _ := st.lease("meter")
	kv.lose() // a failover that kept nothing
	time.Sleep(2 * time.Second)
	close(stop)
	time.Sleep(50 * time.Millisecond)
	after, _ := st.lease("meter")
	if after < before {
		t.Fatalf("the round went back from %d to %d", before, after)
	}
	fx.disjoint(t)
}

// swap is a KV that can lose everything it held at once.
type swap struct {
	Backend
	b atomic.Pointer[mem]
}

func newSwap() *swap {
	s := &swap{}
	s.b.Store(newMem())
	return s
}

func (s *swap) lose() { s.b.Store(newMem()) }
func (s *swap) Join(ctx context.Context, set string, r Record, ttl time.Duration, force bool) (*Record, error) {
	return s.b.Load().Join(ctx, set, r, ttl, force)
}
func (s *swap) Leave(ctx context.Context, set, id, instance string) error {
	return s.b.Load().Leave(ctx, set, id, instance)
}
func (s *swap) Members(ctx context.Context, set string, ttl time.Duration) ([]Record, error) {
	return s.b.Load().Members(ctx, set, ttl)
}

// WITH NO OBJECT STORE a loop runs only where the deployment has one writer.
func TestWithNoStoreALoopRunsOnlyAlone(t *testing.T) {
	kv := newMem()
	a := solo(t, kv, "cloud-0", nil)
	la := a.Lead("meter")
	if err := la.Do(context.Background(), func(context.Context) {}); err != nil {
		t.Fatalf("a lone pod with no store did not run its loop: %v", err)
	}
	b := pod(t, kv, "cloud-1", true, nil)
	if b.Lead("meter").Hold(context.Background()) == nil {
		t.Fatal("a pod of a deployment declaring two writers runs a loop with no fence")
	}
}

// Counts and tokens are the deployment's, whichever pod is asked.
func TestCountsAndTokensAreShared(t *testing.T) {
	kv := newMem()
	a, b := pod(t, kv, "cloud-0", true, nil), pod(t, kv, "cloud-1", true, nil)
	ctx := context.Background()
	if left, _ := a.Spend(ctx, "w", []string{"k"}, []int{1}, 2, time.Minute); left != 1 {
		t.Fatalf("first spend left %d", left)
	}
	b.Spend(ctx, "w", []string{"k"}, []int{1}, 2, time.Minute)
	if left, _ := a.Spend(ctx, "w", []string{"k"}, []int{1}, 2, time.Minute); left >= 0 {
		t.Fatal("a third request against a limit of two, over two pods, was admitted")
	}
	if first, err := a.Once(ctx, "link", "n1", time.Minute); !first || err != nil {
		t.Fatalf("first redemption: %v %v", first, err)
	}
	if first, _ := b.Once(ctx, "link", "n1", time.Minute); first {
		t.Fatal("a token was redeemed twice, once on each pod")
	}
}

// The person limiter is a token bucket: a full minute's worth at once, then one
// every 60/perMin seconds.
func TestTakeIsATokenBucket(t *testing.T) {
	c := Alone()
	ctx := context.Background()
	for i := range 120 {
		if !c.Take(ctx, "people", "p:u", 120) {
			t.Fatalf("token %d of a full bucket of 120 was refused", i+1)
		}
	}
	if c.Take(ctx, "people", "p:u", 120) {
		t.Fatal("an empty bucket admitted")
	}
	time.Sleep(600 * time.Millisecond) // one token refills every 500ms
	if !c.Take(ctx, "people", "p:u", 120) {
		t.Fatal("a refilled token was refused")
	}
	if !c.Take(ctx, "people", "p:other", 120) {
		t.Fatal("a second person shares the first's bucket")
	}
}

// A KV that cannot answer is not a reason to admit everyone or to redeem a token
// twice. One writer counts here; several refuse a token they cannot check.
func TestAnUnansweringKVCountsHereAndRedeemsNothing(t *testing.T) {
	c := over(down{ErrUnavailable}, newMem(), true, Config{Periods: fast, Self: Member{ID: "x"}, Secret: secret})
	ctx := context.Background()
	c.Spend(ctx, "w", []string{"k"}, []int{1}, 1, time.Minute)
	if left, _ := c.Spend(ctx, "w", []string{"k"}, []int{1}, 1, time.Minute); left >= 0 {
		t.Fatal("with the KV down the window admitted everyone")
	}
	onceTwice(t, c, false)
	peered := over(down{ErrUnavailable}, newMem(), true, Config{Periods: fast, Self: Member{ID: "x"}, Secret: secret, Declared: 2})
	onceTwice(t, peered, true)
}

// onceTwice redeems one token twice on c: refused as unavailable when c is one of
// several writers, else first and then spent.
func onceTwice(t *testing.T, c *Coord, refused bool) {
	t.Helper()
	ctx := context.Background()
	first, err := c.Once(ctx, "link", "n", time.Minute)
	if refused {
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("several writers, a token the KV cannot check answered %v %v", first, err)
		}
		return
	}
	again, err2 := c.Once(ctx, "link", "n", time.Minute)
	if !first || err != nil || again || err2 != nil {
		t.Fatalf("one writer, KV unreachable: first=%v (%v) again=%v (%v); want it counted here", first, err, again, err2)
	}
}

// PEERS PROVE THEMSELVES BOTH WAYS: a pod of this deployment reaches another over
// mutual TLS, and neither end accepts a party the deployment's authority did not
// certify.
func TestPeersProveThemselves(t *testing.T) {
	a := over(newMem(), newMem(), false, Config{Periods: fast, Self: Member{ID: "cloud-0"}, Secret: secret})
	b := over(newMem(), newMem(), false, Config{Periods: fast, Self: Member{ID: "cloud-1"}, Secret: secret})
	stranger := over(newMem(), newMem(), false, Config{Periods: fast, Self: Member{ID: "x"}, Secret: []byte("not this deployment's master key")})
	srv, err := a.ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	l, err := tls.Listen("tcp", "127.0.0.1:0", srv)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if err := conn.(*tls.Conn).Handshake(); err == nil {
					_, _ = conn.Write([]byte("ok"))
				}
			}()
		}
	}()
	dial := func(c *Coord) error {
		cfg, err := c.ClientTLS()
		if err != nil {
			return err
		}
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", l.Addr().String(), cfg)
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		got, err := io.ReadAll(conn)
		if string(got) != "ok" {
			return errors.New("refused: " + string(got) + " " + errorString(err))
		}
		return nil
	}
	if err := dial(b); err != nil {
		t.Fatalf("a peer of the same deployment was refused: %v", err)
	}
	if err := dial(stranger); err == nil {
		t.Fatal("a caller another authority certified was served")
	}
	// The other direction: a listener another authority certified is refused by a peer.
	ssrv, _ := stranger.ServerTLS()
	sl, err := tls.Listen("tcp", "127.0.0.1:0", ssrv)
	if err != nil {
		t.Fatal(err)
	}
	defer sl.Close()
	go func() {
		conn, err := sl.Accept()
		if err == nil {
			_ = conn.(*tls.Conn).Handshake()
			conn.Close()
		}
	}()
	cfg, _ := b.ClientTLS()
	if conn, err := tls.Dial("tcp", sl.Addr().String(), cfg); err == nil {
		conn.Close()
		t.Fatal("a peer handed its caller's credentials to a listener this deployment did not certify")
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// flaky is a KV that can stop answering, for one process.
type flaky struct {
	Backend
	off atomic.Bool
}

func (f *flaky) Join(ctx context.Context, set string, r Record, ttl time.Duration, force bool) (*Record, error) {
	if f.off.Load() {
		return nil, ErrUnavailable
	}
	return f.Backend.Join(ctx, set, r, ttl, force)
}

func (f *flaky) Members(ctx context.Context, set string, ttl time.Duration) ([]Record, error) {
	if f.off.Load() {
		return nil, ErrUnavailable
	}
	return f.Backend.Members(ctx, set, ttl)
}

// THE LIVE SET HOLDS STILL THROUGH A KV OUTAGE. A pod whose KV stops answering
// keeps the set it last read: had it aged to itself, two live pods would each elect
// themselves owner of every org. A peer that dies in the outage keeps its orgs until
// the KV answers again.
func TestALiveSetHoldsStillThroughAnOutage(t *testing.T) {
	t.Parallel()
	kv := newMem()
	view := &flaky{Backend: kv}
	a := pod(t, view, "cloud-0", true, nil)
	pod(t, kv, "cloud-1", true, nil)
	eventually(t, time.Second, "cloud-0 never saw cloud-1", func() bool { return len(a.Members()) == 2 })
	view.off.Store(true)
	deadline := time.Now().Add(presenceTTL + skew + time.Second)
	for time.Now().Before(deadline) {
		if got := ids(a.Members()); !slices.Equal(got, []string{"cloud-0", "cloud-1"}) {
			t.Fatalf("in the outage the live set became %v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A RECORD NAMES A MEMBER OF ONE DEPLOYMENT. Two deployments that share a master and
// a KV (a testnet beside production) sign under their own prefix, so a record lifted
// from one set and written into the other names nobody.
func TestARecordFromAnotherDeploymentNamesNobody(t *testing.T) {
	kv := newMem()
	prod := pod(t, kv, "cloud-0", true, nil)
	testnet := over(newMem(), newMem(), true, Config{Periods: fast, Deployment: "testnet", Self: Member{ID: "cloud-9", Addr: "cloud-9:8443"}, Instance: "t", Secret: secret})
	_, _ = kv.Join(context.Background(), prod.Key("set", membersSet), testnet.record(), presenceTTL, false)
	prod.refresh(context.Background())
	if got := ids(prod.Members()); !slices.Equal(got, []string{"cloud-0"}) {
		t.Fatalf("a record signed for another deployment reads as live: %v", got)
	}
}

// NO PROCESS TAKES A LEASE ANOTHER IS RENEWING, NOT EVEN ONE OF ITS OWN POD. Two
// processes under one pod id share a loop's name; the one that holds it keeps it
// for as long as it renews.
func TestASiblingProcessNeverStealsARenewedLease(t *testing.T) {
	t.Parallel()
	kv, st := newMem(), newCAS()
	first := pod(t, kv, "cloud-0", true, st)
	lf := first.Lead("meter")
	eventually(t, time.Second, "the first process never held the loop", func() bool { return lf.Hold(context.Background()) == nil })
	sibling := pod(t, kv, "cloud-0", false, st)
	ls := sibling.Lead("meter")
	deadline := time.Now().Add(3 * staleAfter())
	for time.Now().Before(deadline) {
		if ls.Hold(context.Background()) == nil {
			t.Fatal("a sibling process took a lease its holder was renewing")
		}
		if lf.Hold(context.Background()) != nil {
			t.Fatal("the renewing holder lost its lease")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ON A SLOW, LOSSY STORE THE LOOP STILL CHANGES HANDS WITHOUT OVERLAP. Three peers
// on a store whose every leg takes up to one and a half renewal periods, whose
// writes land after their caller gave up, and which loses a fifth of its replies;
// whoever holds the loop dies hard twice. Someone keeps running it, and no two run
// it at once.
func TestASlowStoreHandsTheLoopOverWithoutOverlap(t *testing.T) {
	t.Parallel()
	st := newCAS()
	fx := &effects{}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	type proc struct {
		c *Coord
		l *Leader
	}
	var ps []proc
	for _, id := range []string{"p0", "p1", "p2"} {
		c := over(newMem(), newMem(), false, Config{Periods: fast, Deployment: "t", Self: Member{ID: id}, Instance: id, Secret: secret, Declared: 3})
		c.SetStore(&slow{casStore: st, max: confirmEvery * 3 / 2, lose: 0.2})
		c.Start(context.Background())
		l := c.Lead("x")
		ps = append(ps, proc{c, l})
		go loop(ctx, l, id, fx)
	}
	for range 2 {
		time.Sleep(3 * time.Second)
		_, owner := st.lease("x")
		for _, p := range ps {
			if p.c.owner == owner {
				p.c.closed.Store(true) // dies hard: no drain, no release
			}
		}
	}
	<-ctx.Done()
	fx.disjoint(t)
	ran := 0
	for _, id := range []string{"p0", "p1", "p2"} {
		if len(fx.by(id)) > 0 {
			ran++
		}
	}
	if ran < 2 {
		t.Fatalf("over two hard deaths the loop ran in %d processes; it never changed hands", ran)
	}
}

// A process told to share (KV_URL, CLOUD_PUBSUB_URL) with no driver linked refuses:
// answering alone would let every pod spend a limit and run a loop for itself.
func TestASharedURLWithNoDriverIsRefused(t *testing.T) {
	_, err := New(Config{URL: "kv://127.0.0.1:1", Self: Member{ID: "cloud-0"}, Secret: secret})
	if !errors.Is(err, ErrNoDriver) {
		t.Fatalf("KV_URL with no driver: %v, want ErrNoDriver", err)
	}
	if _, err := New(Config{Self: Member{ID: "cloud-0"}}); err != nil {
		t.Fatalf("no KV_URL is one pod answering alone: %v", err)
	}
}
