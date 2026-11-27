package kv

import (
	"context"
	"errors"
	"net"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	kvc "github.com/hanzokv/go/v9"

	"github.com/hanzoai/cloud/sdk/coord"
)

// root is a pod's root process over the KV at url.
func root(t *testing.T, url, id string) *coord.Coord {
	t.Helper()
	c, err := coord.New(coord.Config{Periods: fast, URL: url, Deployment: "test", Self: coord.Member{ID: id, Addr: id + ":8443"}, Instance: id + "-host", Secret: secret, Root: true})
	if err != nil {
		t.Fatal(err)
	}
	c.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		c.Close(ctx)
	})
	return c
}

func raw(t *testing.T, url string) *kvc.Client {
	t.Helper()
	opt, err := kvc.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	c := kvc.NewClient(opt)
	t.Cleanup(func() { c.Close() })
	return c
}

// Planted entries name nobody on a real KV: an expiry no heartbeat could have
// written, a signature nobody holding the master made, and a genuine member's own
// record replayed after it left.
func TestKVIgnoresPlantedMembers(t *testing.T) {
	t.Parallel()
	url, _ := valkey(t)
	a, b := root(t, url, "cloud-0"), root(t, url, "cloud-1")
	eventually(t, 2*time.Second, "two pods never saw each other", func() bool { return len(a.Members()) == 2 && len(b.Members()) == 2 })
	kv := raw(t, url)
	ctx := context.Background()
	set := b.Key("set", "members")
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	kv.ZAdd(ctx, set, kvc.Z{Score: 9e15, Member: "planted"})
	kv.HSet(ctx, set+":addr", "planted", "203.0.113.9:8443|"+now+"|00")
	kv.HSet(ctx, set+":instance", "planted", "x")

	// cloud-0's own record, lifted while it was live.
	addr := kv.HGet(ctx, set+":addr", "cloud-0").Val()
	inst := kv.HGet(ctx, set+":instance", "cloud-0").Val()
	a.Close(ctx)
	// Replayed with an expiry no pod writes, and then with a plausible one after
	// its signed time is past a heartbeat.
	kv.ZAdd(ctx, set, kvc.Z{Score: 9e15, Member: "cloud-0"})
	kv.HSet(ctx, set+":addr", "cloud-0", addr)
	kv.HSet(ctx, set+":instance", "cloud-0", inst)
	time.Sleep(3 * refreshEvery)
	if got := ids(b.Members()); !slices.Equal(got, []string{"cloud-1"}) {
		t.Fatalf("with planted entries the live set reads %v", got)
	}
	time.Sleep(presenceTTL + skew)
	kv.ZAdd(ctx, set, kvc.Z{Score: float64(time.Now().Add(presenceTTL / 2).UnixMilli()), Member: "cloud-0"})
	time.Sleep(3 * refreshEvery)
	if got := ids(b.Members()); !slices.Equal(got, []string{"cloud-1"}) {
		t.Fatalf("a replayed Record past its heartbeat reads as live: %v", got)
	}
}

// A KV THAT REFUSES WRITES (min-replicas-to-write with no replica: NOREPLICAS) still
// answers who is alive, and the pod is still in its own live set. A read that does
// not hold this pod's own heartbeat is no proof the pod is live, so it is not taken
// for one. Counts fall back to this process; a single-use token is refused, not
// redeemed.
func TestKVRefusingWrites(t *testing.T) {
	url, _ := valkey(t, "--min-replicas-to-write", "1")
	c := root(t, url, "cloud-0")
	if c.Err() != nil {
		t.Fatalf("a KV refusing writes refused the pod: %v", c.Err())
	}
	if _, err := direct(t, url).Members(context.Background(), c.Key("set", "members"), presenceTTL); err != nil {
		t.Fatalf("reading the live set from a KV that refuses writes: %v", err)
	}
	if got := ids(c.Members()); !slices.Equal(got, []string{"cloud-0"}) {
		t.Fatalf("members %v, want this pod", got)
	}
	time.Sleep(2 * refreshEvery)
	if c.Live() {
		t.Fatal("a pod whose heartbeat never landed reads as live")
	}
	ctx := context.Background()
	c.Spend(ctx, "w", []string{"k"}, []int{1}, 1, time.Minute)
	if left, _ := c.Spend(ctx, "w", []string{"k"}, []int{1}, 1, time.Minute); left >= 0 {
		t.Fatal("a window over a KV refusing writes admitted past its limit")
	}
	onceTwice(t, c, false)
}

// A KV THAT HANGS costs one caller one timeout per openFor, and every other caller
// nothing. It times each call, so it runs alone: the tests that run together starve
// one another by more than the slack it allows.
func TestKVThatHangs(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		var held []net.Conn
		defer func() {
			for _, c := range held {
				c.Close()
			}
		}()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			held = append(held, c) // accepted, never answered
		}
	}()
	c, err := coord.New(coord.Config{Periods: fast, URL: "kv://" + l.Addr().String(), Deployment: "test", Self: coord.Member{ID: "cloud-0"}, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	slowCalls, start := 0, time.Now()
	for time.Since(start) < time.Second {
		t0 := time.Now()
		c.Spend(ctx, "w", []string{"k"}, []int{1}, 10, time.Minute)
		d := time.Since(t0)
		if d > callTimeout+200*time.Millisecond {
			t.Fatalf("one request waited %v on a hung KV", d)
		}
		if d > 50*time.Millisecond {
			slowCalls++
		}
		time.Sleep(5 * time.Millisecond)
	}
	if most := int(time.Second/(openFor+callTimeout)) + 2; slowCalls > most {
		t.Fatalf("%d requests waited on a hung KV in one second; the breaker allows %d", slowCalls, most)
	}
	t0 := time.Now()
	onceTwice(t, c, false)
	if d := time.Since(t0); d > callTimeout+200*time.Millisecond {
		t.Fatalf("a token on a hung KV took %v", d)
	}
}

// A FAILOVER THAT LOSES EVERYTHING: the pods are back in the live set within a
// heartbeat, and neither ever reads itself out of it.
func TestKVFailoverRejoins(t *testing.T) {
	url, restart := valkey(t)
	a, b := root(t, url, "cloud-0"), root(t, url, "cloud-1")
	eventually(t, 2*time.Second, "two pods never saw each other", func() bool { return len(a.Members()) == 2 })
	restart()
	stop := make(chan struct{})
	lost := make(chan string, 1)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if !slices.Contains(ids(a.Members()), "cloud-0") || !slices.Contains(ids(b.Members()), "cloud-1") {
				lost <- "a pod read itself out of the live set"
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	eventually(t, presenceTTL+2*time.Second, "the pods never found each other after the KV came back empty", func() bool {
		rs, err := direct(t, url).Members(context.Background(), a.Key("set", "members"), presenceTTL)
		return err == nil && len(rs) == 2 && len(a.Members()) == 2 && len(b.Members()) == 2
	})
	close(stop)
	select {
	case msg := <-lost:
		t.Fatal(msg)
	default:
	}
}

// Counts, buckets and tokens on a real KV are one across pods.
func TestKVSharesCounts(t *testing.T) {
	url, _ := valkey(t)
	a, b := root(t, url, "cloud-0"), root(t, url, "cloud-1")
	ctx := context.Background()
	admitted := 0
	for i := range 10 {
		c := a
		if i%2 == 1 {
			c = b
		}
		if left, _ := c.Spend(ctx, "w", []string{"k"}, []int{1}, 6, time.Minute); left >= 0 {
			admitted++
		}
	}
	if admitted != 6 {
		t.Fatalf("ten requests over two pods against a limit of six admitted %d", admitted)
	}
	taken := 0
	for i := range 10 {
		c := a
		if i%2 == 1 {
			c = b
		}
		if c.Take(ctx, "people", "p:u", 4) {
			taken++
		}
	}
	if taken != 4 {
		t.Fatalf("a bucket of four over two pods gave %d tokens", taken)
	}
	first, err := a.Once(ctx, "link", "n", time.Minute)
	again, _ := b.Once(ctx, "link", "n", time.Minute)
	if !first || err != nil || again {
		t.Fatalf("a token redeemed on two pods: first=%v (%v) again=%v", first, err, again)
	}
}

// A quota on a real KV is one count across pods, charged to every window or none.
func TestKVSharesQuotas(t *testing.T) {
	url, _ := valkey(t)
	a, b := root(t, url, "cloud-0"), root(t, url, "cloud-1")
	ctx := context.Background()
	ws := []coord.Window{{Key: "hour:u", Limit: 4, TTL: time.Hour}, {Key: "day:u", Limit: 6, TTL: time.Hour}}
	admitted := 0
	for i := range 10 {
		c := a
		if i%2 == 1 {
			c = b
		}
		if _, ok := c.Quota(ctx, "ai", ws); ok {
			admitted++
		}
	}
	if admitted != 4 {
		t.Fatalf("ten calls over two pods against an hour of four admitted %d", admitted)
	}
	if got := b.Counted(ctx, "ai", []string{"hour:u", "day:u"}); !slices.Equal(got, []int{4, 4}) {
		t.Fatalf("the refused calls were charged to the day: %v", got)
	}
	if ttl := raw(t, url).PTTL(ctx, a.Key("quota:ai", "day:u")).Val(); ttl <= 0 || ttl > time.Hour {
		t.Fatalf("the day's key expires in %v", ttl)
	}
}

// A caller that goes away mid-question says nothing about the KV: the breaker stays
// shut, and the next caller is counted on the KV, not in this process.
func TestACancelledCallerLeavesTheBreakerShut(t *testing.T) {
	url, _ := valkey(t)
	root(t, url, "cloud-0")
	b := direct(t, url)
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.ask(gone, func(ctx context.Context) error { return ctx.Err() }); err == nil {
		t.Fatal("a cancelled caller's question answered nil")
	}
	b.mu.Lock()
	open := !b.openTill.IsZero()
	b.mu.Unlock()
	if open {
		t.Fatal("a cancelled caller opened the breaker; every other caller would count in this process")
	}
}

// The ceiling script: one count over two pods, every window or none, each window's
// wait the KV's own PTTL.
func TestTheCeilingScriptOnMiniredis(t *testing.T) {
	mr := miniredis.RunT(t)
	a, b := miniPod(t, mr, "cloud-0"), miniPod(t, mr, "cloud-1")
	ctx := context.Background()
	ws := []coord.Window{{Key: "key/acme/k1", Limit: 3, TTL: time.Minute}, {Key: "org/acme", Limit: 2, TTL: time.Minute}}
	for i, c := range []*coord.Coord{a, b} {
		if used, _, ok := c.Ceiling(ctx, "rate", ws); !ok || !slices.Equal(used, []int{i + 1, i + 1}) {
			t.Fatalf("call %d: used %v ok %v", i+1, used, ok)
		}
	}
	used, wait, ok := a.Ceiling(ctx, "rate", ws)
	if ok || !slices.Equal(used, []int{2, 2}) {
		t.Fatalf("the org's third over two pods: used %v ok %v, want refused with nothing charged", used, ok)
	}
	if ttl := mr.TTL(a.Key("ceiling:rate", "org/acme")); ttl <= 0 || wait[1] != ttl {
		t.Fatalf("wait %v, the KV's TTL %v: the reset is not the KV's", wait[1], ttl)
	}
}

// A count whose expiry was lost — set by hand, restored without its TTL, written by
// an older script — gets one back on the next charge, so it never holds a caller
// refused for good.
func TestACountWithoutAnExpiryGetsOne(t *testing.T) {
	mr := miniredis.RunT(t)
	c := miniPod(t, mr, "cloud-0")
	full := c.Key("ceiling:rate", "org/stale")
	if err := mr.Set(full, "1"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.Ceiling(context.Background(), "rate", []coord.Window{{Key: "org/stale", Limit: 5, TTL: time.Minute}}); !ok {
		t.Fatal("a count of 1 under a limit of 5 was refused")
	}
	if ttl := mr.TTL(full); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("the count's expiry after a charge: %v, want inside the window", ttl)
	}
}

// The spend script: a window counted across two pods, the narrowest bucket first.
func TestTheSpendScriptOnMiniredis(t *testing.T) {
	mr := miniredis.RunT(t)
	a, b := miniPod(t, mr, "cloud-0"), miniPod(t, mr, "cloud-1")
	ctx := context.Background()
	admitted := 0
	for i := range 6 {
		c := a
		if i%2 == 1 {
			c = b
		}
		if left, _ := c.Spend(ctx, "login", []string{"203.0.113.9"}, []int{1}, 4, time.Minute); left >= 0 {
			admitted++
		}
	}
	if admitted != 4 {
		t.Fatalf("six requests over two pods against a limit of four admitted %d", admitted)
	}
}

// spanAt is a moment the span tests start from.
var spanAt = time.Date(2026, 11, 2, 22, 40, 0, 0, time.UTC)

// The same, on the production script, over two pods sharing one KV.
func TestTheSpanScriptOnMiniredis(t *testing.T) {
	mr := miniredis.RunT(t)
	spans(t, miniPod(t, mr, "cloud-0"), miniPod(t, mr, "cloud-1"))
	a := miniPod(t, mr, "cloud-2")
	if ttl := mr.TTL(a.Key("span:t", "s:u")); ttl <= 5*time.Hour || ttl > 5*time.Hour+coord.SpanKeep {
		t.Fatalf("the record is kept %v, want past the span's end by at most %v", ttl, coord.SpanKeep)
	}
}

// spans runs the span's contract on a and b, which share one span when they share a
// KV and are a's alone when they do not.
func spans(t *testing.T, a, b *coord.Coord) {
	t.Helper()
	ctx := context.Background()
	const length = 5 * time.Hour
	if got := a.Spanned(ctx, "t", "s:u", spanAt, length); !got.IsZero() {
		t.Fatalf("a span runs before any call: %v", got)
	}
	if got := a.Spanned(ctx, "t", "s:u", spanAt, length); !got.IsZero() {
		t.Fatalf("a read opened a span at %v", got)
	}
	if got := a.Span(ctx, "t", "s:u", spanAt, length); !got.Equal(spanAt) {
		t.Fatalf("the first call opened %v, want its own instant %v", got, spanAt)
	}
	later := spanAt.Add(length - time.Second)
	if got := a.Span(ctx, "t", "s:u", later, length); !got.Equal(spanAt) {
		t.Fatalf("a call a second before the end answered %v, want the running span %v", got, spanAt)
	}
	if got := a.Spanned(ctx, "t", "s:u", spanAt.Add(-time.Minute), length); !got.Equal(spanAt) {
		t.Fatalf("a clock a minute behind read %v, want the running span", got)
	}
	if got := a.Spanned(ctx, "t", "s:other", later, length); !got.IsZero() {
		t.Fatalf("another key reads %v", got)
	}
	end := spanAt.Add(length)
	if got := a.Spanned(ctx, "t", "s:u", end, length); !got.IsZero() {
		t.Fatalf("at its end the span still runs: %v", got)
	}
	next := end.Add(17 * time.Minute)
	if got := a.Span(ctx, "t", "s:u", next, length); !got.Equal(next) {
		t.Fatalf("the first call after the end opened %v, want its own instant %v", got, next)
	}
	if a.Shared() {
		if got := b.Spanned(ctx, "t", "s:u", next.Add(time.Hour), length); !got.Equal(next) {
			t.Fatalf("the other pod reads %v, want the span this one opened at %v", got, next)
		}
	}
}

// CALLS THAT ARRIVE TOGETHER OPEN ONE SPAN. Each states its own instant, and every
// one answers the instant of whichever opened first.
func TestCallsTogetherOpenOneSpan(t *testing.T) {
	mr := miniredis.RunT(t)
	pods := []*coord.Coord{miniPod(t, mr, "cloud-0"), miniPod(t, mr, "cloud-1")}
	ctx := context.Background()
	got := make([]time.Time, 40)
	var wg sync.WaitGroup
	for i := range got {
		wg.Go(func() {
			got[i] = pods[i%2].Span(ctx, "t", "s:u", spanAt.Add(time.Duration(i)*time.Second), time.Hour)
		})
	}
	wg.Wait()
	for i, s := range got {
		if !s.Equal(got[0]) {
			t.Fatalf("call %d answered %v and call 0 %v: two spans under one key", i, s, got[0])
		}
	}
}

// Holds are summed across pods: each pod's hold is part of the total every pod is
// answered, a hold given back leaves it, and a hold nobody gave back lapses with its
// ttl instead of standing for ever.
func TestHoldsAreSummedAcrossPodsOnAKV(t *testing.T) {
	run := func(t *testing.T, a, b *coord.Coord) {
		ctx := context.Background()
		if got := a.Commit(ctx, "reserve", "acme", "a1", 100, time.Minute); got != 100 {
			t.Fatalf("a's first hold: total %d, want 100", got)
		}
		if got := b.Commit(ctx, "reserve", "acme", "b1", 250, time.Minute); got != 350 {
			t.Fatalf("b's hold: total %d, want 350 (a's 100 and its own 250)", got)
		}
		if got := b.Commit(ctx, "reserve", "other", "b2", 7, time.Minute); got != 7 {
			t.Fatalf("another key's hold: total %d, want 7", got)
		}
		if got := a.Committed(ctx, "reserve", "acme"); got != 350 {
			t.Fatalf("a reads %d held, want 350", got)
		}
		b.Uncommit(ctx, "reserve", "acme", "b1", 250)
		b.Uncommit(ctx, "reserve", "acme", "b1", 250) // already given back
		if got := a.Committed(ctx, "reserve", "acme"); got != 100 {
			t.Fatalf("after b gave its hold back: %d, want 100", got)
		}
		a.Commit(ctx, "reserve", "acme", "a2", 5, 80*time.Millisecond)
		time.Sleep(120 * time.Millisecond)
		if got := b.Committed(ctx, "reserve", "acme"); got != 100 {
			t.Fatalf("a hold past its ttl still counts: %d, want 100", got)
		}
		a.Uncommit(ctx, "reserve", "acme", "a1", 100)
		if got := b.Committed(ctx, "reserve", "acme"); got != 0 {
			t.Fatalf("all holds given back: %d, want 0", got)
		}
	}
	url, _ := valkey(t)
	run(t, root(t, url, "cloud-0"), root(t, url, "cloud-1"))
}

// A KV that cannot be asked leaves each pod the total of its own holds, and a hold made
// then is given back there.
func TestHoldsFallBackToThePodsOwn(t *testing.T) {
	// Nothing listens here, so every question to the KV goes unanswered.
	c := root(t, "kv://127.0.0.1:1", "cloud-0")
	ctx := context.Background()
	if got := c.Commit(ctx, "reserve", "acme", "a1", 40, time.Minute); got != 40 {
		t.Fatalf("with the KV down: total %d, want this pod's 40", got)
	}
	if got := c.Commit(ctx, "reserve", "acme", "a2", 2, time.Minute); got != 42 {
		t.Fatalf("a second hold with the KV down: total %d, want 42", got)
	}
	c.Uncommit(ctx, "reserve", "acme", "a1", 40)
	if got := c.Committed(ctx, "reserve", "acme"); got != 2 {
		t.Fatalf("after giving one back: %d, want 2", got)
	}
}

// On a real KV the ceiling's waits are the KV's own PTTL, so a Retry-After never
// depends on this machine's clock agreeing with it.
func TestKVCeilingAnswersItsOwnClock(t *testing.T) {
	url, _ := valkey(t)
	a, b := root(t, url, "cloud-0"), root(t, url, "cloud-1")
	ctx := context.Background()
	ws := []coord.Window{{Key: "key/acme/k1", Limit: 3, TTL: time.Minute}, {Key: "org/acme", Limit: 2, TTL: time.Minute}}
	for i, c := range []*coord.Coord{a, b} {
		if used, _, ok := c.Ceiling(ctx, "rate", ws); !ok || !slices.Equal(used, []int{i + 1, i + 1}) {
			t.Fatalf("call %d: used %v ok %v", i+1, used, ok)
		}
	}
	used, wait, ok := b.Ceiling(ctx, "rate", ws)
	if ok || !slices.Equal(used, []int{2, 2}) {
		t.Fatalf("the org's third over two pods: used %v ok %v", used, ok)
	}
	ttl := raw(t, url).PTTL(ctx, a.Key("ceiling:rate", "org/acme")).Val()
	if d := wait[1] - ttl; d < -time.Second || d > time.Second || wait[1] <= 0 {
		t.Fatalf("wait %v, the KV's PTTL %v: the reset is not the KV's", wait[1], ttl)
	}
}

// R1: one unauthenticated KV write used to make a live pod's heartbeat answer
// coord.ErrDuplicate (Err() set; cmd/cloud watchDeployment halts the host), and a
// restarted root refuse to boot, for as long as the plant stayed. An entry under
// the pod's id that no live instance signed is overwritten; only a real second
// instance refuses.
func TestRedPlantedDuplicateHaltsPod(t *testing.T) {
	t.Parallel()
	url, _ := valkey(t)
	a := root(t, url, "cloud-0")
	eventually(t, time.Second, "joined", func() bool { return a.Err() == nil && len(a.Members()) == 1 })
	kv := raw(t, url)
	ctx := context.Background()
	set := a.Key("set", "members")
	// attacker: no key, no signature; a far-future score and a foreign instance
	kv.ZAdd(ctx, set, kvc.Z{Score: 9e15, Member: "cloud-0"})
	kv.HSet(ctx, set+":instance", "cloud-0", "attacker")
	time.Sleep(3 * presenceTTL)
	if a.Err() != nil {
		t.Fatalf("a planted entry refused the live pod: %v", a.Err())
	}
	if inst := kv.HGet(ctx, set+":instance", "cloud-0").Val(); inst != "cloud-0-host" {
		t.Fatalf("the planted entry stands: instance %q", inst)
	}

	// planted again, and the same pod restarts
	kv.ZAdd(ctx, set, kvc.Z{Score: 9e15, Member: "cloud-0"})
	kv.HSet(ctx, set+":instance", "cloud-0", "attacker")
	a.Close(ctx)
	b, err := coord.New(coord.Config{Periods: fast, URL: url, Deployment: "test", Self: coord.Member{ID: "cloud-0", Addr: "cloud-0:8443"}, Instance: "cloud-0-host", Secret: secret, Root: true})
	if err != nil {
		t.Fatal(err)
	}
	b.Start(ctx)
	defer b.Close(ctx)
	if b.Err() != nil {
		t.Fatalf("a restarted root refused over a planted entry: %v", b.Err())
	}

	// A real second instance under the id still refuses.
	twin, err := coord.New(coord.Config{Periods: fast, URL: url, Deployment: "test", Self: coord.Member{ID: "cloud-0", Addr: "elsewhere:8443"}, Instance: "other-host", Secret: secret, Root: true})
	if err != nil {
		t.Fatal(err)
	}
	twin.Start(ctx)
	defer twin.Close(ctx)
	if !errors.Is(twin.Err(), coord.ErrDuplicate) {
		t.Fatalf("a second live instance under the id: %v, want coord.ErrDuplicate", twin.Err())
	}
}

// direct is the driver's own backend at url, for a test that asks it a question no
// Coord asks.
func direct(t *testing.T, url string) *kv {
	t.Helper()
	b, err := newKV(url, openFor)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func onceTwice(t *testing.T, c *coord.Coord, refused bool) {
	t.Helper()
	ctx := context.Background()
	first, err := c.Once(ctx, "link", "n", time.Minute)
	if refused {
		if !errors.Is(err, coord.ErrUnavailable) {
			t.Fatalf("several writers, a token the KV cannot check answered %v %v", first, err)
		}
		return
	}
	again, err2 := c.Once(ctx, "link", "n", time.Minute)
	if !first || err != nil || again || err2 != nil {
		t.Fatalf("one writer, KV unreachable: first=%v (%v) again=%v (%v); want it counted here", first, err, again, err2)
	}
}
