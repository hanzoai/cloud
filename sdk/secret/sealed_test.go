package secret

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
)

// leaseOn is the lease the fleet uses, on coordinator c.
func leaseOn(c *coord.Coord) Lease { return leaseFor(c, time.Minute) }

// leaseFor is the same lease, for ttl: a test that needs a lease to lapse in milliseconds.
func leaseFor(c *coord.Coord, ttl time.Duration) Lease {
	return Lease(c.Lease("secret:seal", ref, ttl))
}

// own is a lease on a coordinator of its own: one pod.
func own() Lease { return leaseOn(coord.Alone()) }

// mem is a KMS in memory that counts its reads and writes. gate, when set, is called
// inside every write before it lands and again after, so a test can hold writers
// together.
type mem struct {
	mu     sync.Mutex
	v      map[string][]byte
	reads  int
	puts   int
	getErr error
	putErr error
	gate   func(before bool)
}

func newMem() *mem { return &mem{v: map[string][]byte{}} }

func (m *mem) GetSecret(_ context.Context, ref string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	if m.getErr != nil {
		return nil, m.getErr
	}
	if v, ok := m.v[ref]; ok {
		return append([]byte(nil), v...), nil
	}
	return nil, ErrNone
}

func (m *mem) PutSecret(_ context.Context, ref string, v []byte) error {
	if m.gate != nil {
		m.gate(true)
	}
	m.mu.Lock()
	if m.putErr != nil {
		m.mu.Unlock()
		return m.putErr
	}
	m.puts++
	m.v[ref] = append([]byte(nil), v...)
	m.mu.Unlock()
	if m.gate != nil {
		m.gate(false)
	}
	return nil
}

func (m *mem) DeleteSecret(context.Context, string) error { return nil }
func (m *mem) Sign(context.Context, string, []byte) ([]byte, error) {
	return nil, errors.New("no signing")
}

const ref = "test/hmac"

// A key is minted once on first use by a caller that may mint, sealed in KMS, and
// every later read is the cache's: KMS is not asked again.
func TestAKeyIsMintedOnceAndCached(t *testing.T) {
	m, k := newMem(), NewSealed(ref, own())
	a, err := k.Get(context.Background(), m, true)
	if err != nil || len(a) != 32 {
		t.Fatalf("first read = %d bytes, %v", len(a), err)
	}
	if held := m.v[ref]; !bytes.Equal(held, a) {
		t.Fatal("the key returned is not the key KMS holds")
	}
	reads, puts := m.reads, m.puts
	b, err := k.Get(context.Background(), m, false)
	if err != nil || !bytes.Equal(a, b) || m.reads != reads || m.puts != puts {
		t.Fatalf("a cached read asked KMS again (%d reads, %d writes) or changed the key: %v", m.reads-reads, m.puts-puts, err)
	}
}

// A read that may not mint, finding no key, is ErrUnsealed and writes nothing; it
// is remembered briefly, a caller that may mint is not refused by that memory, and
// after the window KMS is asked again.
func TestAnAbsentKeyIsRememberedBrieflyAndNeverMintedByAReader(t *testing.T) {
	m, k := newMem(), NewSealed(ref, own())
	ctx := context.Background()
	if _, err := k.Get(ctx, m, false); !errors.Is(err, ErrUnsealed) {
		t.Fatalf("no key: %v", err)
	}
	if m.puts != 0 {
		t.Fatal("a read that may not mint wrote a key")
	}
	reads := m.reads
	if _, err := k.Get(ctx, m, false); !errors.Is(err, ErrUnsealed) || m.reads != reads {
		t.Fatalf("inside the window the absence is answered from memory: %v, %d new reads", err, m.reads-reads)
	}
	if key, err := k.Get(ctx, m, true); err != nil || len(key) != 32 {
		t.Fatalf("a caller that may mint is not refused by a remembered absence: %v", err)
	}

	m2, k2 := newMem(), NewSealed(ref, own())
	_, _ = k2.Get(ctx, m2, false)
	m2.v[ref] = bytes.Repeat([]byte{7}, 32)
	k2.at = time.Now().Add(-faultWait)
	if key, err := k2.Get(ctx, m2, false); err != nil || !bytes.Equal(key, m2.v[ref]) {
		t.Fatalf("after the window KMS is asked again: %v", err)
	}
}

// A key held that is too short is a fault for every caller, may-mint or not, and is
// never written over.
func TestAShortKeyIsAFaultAndIsNeverReplaced(t *testing.T) {
	for _, provision := range []bool{false, true} {
		m, k := newMem(), NewSealed(ref, own())
		m.v[ref] = []byte("short")
		if _, err := k.Get(context.Background(), m, provision); !errors.Is(err, ErrMalformed) {
			t.Fatalf("provision=%v: %v, want ErrMalformed", provision, err)
		}
		if string(m.v[ref]) != "short" || m.puts != 0 {
			t.Fatalf("provision=%v: the short key was written over: %q", provision, m.v[ref])
		}
	}
}

// A KMS that cannot answer is that error, mints nothing, is not mistaken for an
// absent key, and is answered from memory for the window, so a slow KMS holds up
// one request and not every one behind it.
func TestAFaultIsTheFaultAndIsRememberedBriefly(t *testing.T) {
	m, k := newMem(), NewSealed(ref, own())
	m.getErr = errors.New("the KMS is down")
	ctx := context.Background()
	_, err := k.Get(ctx, m, true)
	if err == nil || errors.Is(err, ErrUnsealed) || !strings.Contains(err.Error(), "read test/hmac") {
		t.Fatalf("a failed read answered %v; it must be the read's own error", err)
	}
	if m.puts != 0 {
		t.Fatal("a failed read minted a key")
	}
	reads := m.reads
	for range 5 {
		if _, err := k.Get(ctx, m, true); err == nil {
			t.Fatal("a remembered fault answered a key")
		}
	}
	if m.reads != reads {
		t.Fatalf("five reads in the window asked KMS %d more times", m.reads-reads)
	}
	m.getErr = nil
	k.at = time.Now().Add(-faultWait)
	if key, err := k.Get(ctx, m, true); err != nil || len(key) != 32 {
		t.Fatalf("after the window and the KMS back: %v", err)
	}
}

// No KMS at all fails closed.
func TestNoKMSIsUnconfigured(t *testing.T) {
	if _, err := NewSealed(ref, own()).Get(context.Background(), nil, true); !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("got %v, want ErrUnconfigured", err)
	}
}

// Many readers of one process share one mint: one write, one key, whoever asked.
func TestConcurrentFirstReadsInOneProcessMintOnce(t *testing.T) {
	m, k := newMem(), NewSealed(ref, own())
	release := make(chan struct{})
	m.gate = func(before bool) {
		if before {
			<-release
		}
	}
	var wg sync.WaitGroup
	keys := make([][]byte, 8)
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keys[i], _ = k.Get(context.Background(), m, true)
		}()
	}
	time.Sleep(50 * time.Millisecond) // every reader is in, and the one writer is held
	close(release)
	wg.Wait()
	if m.puts != 1 {
		t.Fatalf("%d writes, want 1", m.puts)
	}
	for i := range keys {
		if len(keys[i]) != 32 || !bytes.Equal(keys[i], keys[0]) {
			t.Fatalf("reader %d holds %x, reader 0 holds %x", i, keys[i], keys[0])
		}
	}
}

// pod is one pod's view of a shared KMS: the same store, with hooks that run on this pod's
// calls only, so a test can hold one pod still while another runs.
type pod struct {
	*mem
	afterGet func(n int) // called after this pod's n-th read, outside the store's lock
	gets     int
}

func (p *pod) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	v, err := p.mem.GetSecret(ctx, ref)
	p.gets++
	if p.afterGet != nil {
		p.afterGet(p.gets)
	}
	return v, err
}

// Two pods that first-use a key at the same moment end with one key, and a key one pod
// returns is the key the other returns. The interleaving is forced: pod B reads and finds
// nothing, then waits while pod A mints, puts, reads back and returns; only then does B go on.
// Anything that lets B mint after that leaves KMS holding B's key and A holding its own.
func TestTwoPodsThatFirstUseAtOnceEndWithOneKey(t *testing.T) {
	m := newMem()
	a, b := &pod{mem: m}, &pod{mem: m}
	parked, resume := make(chan struct{}), make(chan struct{})
	b.afterGet = func(n int) {
		if n == 1 { // B has read: absent
			close(parked)
			<-resume
		}
	}
	c := coord.Alone() // the one coordinator the two pods share
	podA, podB := NewSealed(ref, leaseOn(c)), NewSealed(ref, leaseOn(c))
	got := make(chan []byte, 1)
	go func() {
		k, err := podB.Get(context.Background(), b, true)
		if err != nil {
			t.Errorf("pod B: %v", err)
		}
		got <- k
	}()
	<-parked
	keyA, err := podA.Get(context.Background(), a, true)
	if err != nil {
		t.Fatalf("pod A: %v", err)
	}
	close(resume)
	keyB := <-got
	if !bytes.Equal(keyA, keyB) || !bytes.Equal(keyA, m.v[ref]) {
		t.Fatalf("pod A holds %x, pod B %x, KMS %x: want one key", keyA, keyB, m.v[ref])
	}
	if m.puts != 1 {
		t.Fatalf("KMS was written %d times, want once: the second pod found the key and minted nothing", m.puts)
	}
}

// sealedOf is a pod's key holder.

// A mint takes the lease; with it held by another, nothing is minted and the answer is
// ErrUnsealed wrapping ErrBusy, never remembered, so the next ask after the holder is done finds
// the key it made.
func TestAMintWhileAnotherPodHoldsTheLeaseMintsNothing(t *testing.T) {
	m, c := newMem(), coord.Alone()
	hold, _, err := c.Lease("secret:seal", ref, time.Minute)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	k := NewSealed(ref, leaseOn(c))
	_, err = k.Get(context.Background(), m, true)
	if !errors.Is(err, ErrBusy) || !errors.Is(err, ErrUnsealed) {
		t.Fatalf("got %v, want ErrUnsealed wrapping ErrBusy", err)
	}
	if m.puts != 0 || len(m.v) != 0 {
		t.Fatalf("a mint without the lease wrote %d times", m.puts)
	}
	hold()
	if key, err := k.Get(context.Background(), m, true); err != nil || len(key) != 32 {
		t.Fatalf("once the lease is free: %v", err)
	}
}

// A Sealed with no lease never mints: there is no option to skip the exclusion.
func TestAMintWithNoLeaseIsRefused(t *testing.T) {
	m := newMem()
	if _, err := NewSealed(ref, nil).Get(context.Background(), m, true); !errors.Is(err, ErrNoLease) {
		t.Fatalf("got %v, want ErrNoLease", err)
	}
	if m.puts != 0 {
		t.Fatal("a Sealed with no lease wrote a key")
	}
	m.v[ref] = bytes.Repeat([]byte{3}, 32)
	if key, err := NewSealed(ref, nil).Get(context.Background(), m, false); err != nil || !bytes.Equal(key, m.v[ref]) {
		t.Fatalf("a read needs no lease: %v", err)
	}
}

// probe is a pod's view of a shared KMS whose calls a test can slow, hold and tamper with.
type probe struct {
	*mem
	delay    time.Duration // every call takes this long
	enter    chan struct{} // closed when a put has begun
	hold     chan struct{} // a put waits for this to close before it lands
	afterPut func()        // runs inside the put once it has landed
}

func (p *probe) GetSecret(ctx context.Context, r string) ([]byte, error) {
	time.Sleep(p.delay)
	return p.mem.GetSecret(ctx, r)
}

func (p *probe) PutSecret(ctx context.Context, r string, v []byte) error {
	time.Sleep(p.delay)
	if p.enter != nil {
		close(p.enter)
	}
	if p.hold != nil {
		<-p.hold
	}
	err := p.mem.PutSecret(ctx, r, v)
	if p.afterPut != nil {
		p.afterPut()
	}
	return err
}

// A pod whose put outlasts its lease must not finish the mint, and must not give the lease
// back: it may be the next holder's by then. The next pod that takes it keeps it, and a third
// is refused for as long as it does. A's lease is one the test builds, which says it is held
// for 40 ms and counts its releases, so nothing is checked against the clock: A's put waits
// on a channel, A's answer is waited for up to ten seconds, and B's lease lasts a minute.
func TestAMintThatOutlastsItsLeaseReturnsUnsealedAndDoesNotRelease(t *testing.T) {
	m, c := newMem(), coord.Alone()
	a := &probe{mem: m, enter: make(chan struct{}), hold: make(chan struct{})}
	landed := sync.OnceFunc(func() { close(a.hold) }) // A's stray put lands, once
	defer landed()
	var released atomic.Int32
	short := Lease(func(context.Context) (func(), time.Duration, error) {
		return func() { released.Add(1) }, 40 * time.Millisecond, nil
	})
	res := make(chan error, 1)
	go func() { _, err := NewSealed(ref, short).Get(context.Background(), a, true); res <- err }()
	<-a.enter // A's put is in flight and will not land until released
	select {
	case err := <-res:
		if !errors.Is(err, ErrUnsealed) {
			t.Fatalf("pod A: %v, want ErrUnsealed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pod A's mint is not bounded by its lease: it was still waiting after ten seconds")
	}
	if n := released.Load(); n != 0 {
		t.Fatalf("pod A released a lease it could not be sure was still its own (%d releases)", n)
	}
	// B has the lease now, and pod C is refused beside it while A's put is still in flight.
	holdB, _, err := leaseFor(c, time.Minute)(context.Background())
	if err != nil {
		t.Fatalf("pod B: %v", err)
	}
	defer holdB()
	if _, err := NewSealed(ref, leaseFor(c, time.Minute)).Get(context.Background(), m, true); !errors.Is(err, ErrBusy) {
		t.Fatalf("pod C beside B's lease: %v, want ErrBusy", err)
	}
	landed()
}

// A pod never uses a key that KMS did not return to it inside its own lease: a read-back
// that is not what it wrote is not trusted, the answer is ErrUnsealed, and the next read
// takes whatever KMS holds.
func TestAReadBackThatIsNotWhatWasWrittenIsNotUsed(t *testing.T) {
	m := newMem()
	other := bytes.Repeat([]byte{5}, 32)
	p := &probe{mem: m}
	p.afterPut = func() { m.mu.Lock(); m.v[ref] = other; m.mu.Unlock() } // another writer lands first
	k := NewSealed(ref, own())
	key, err := k.Get(context.Background(), p, true)
	if !errors.Is(err, ErrUnsealed) || key != nil {
		t.Fatalf("got %x, %v; want no key and ErrUnsealed", key, err)
	}
	if got, err := k.Get(context.Background(), p, false); err != nil || !bytes.Equal(got, other) {
		t.Fatalf("the next read: %x, %v; want what KMS holds", got, err)
	}
}

// The whole mint is bounded by half the lease, not only each call by its own limit: three
// calls that are each quick enough but together outrun it end unsealed with nothing usable.
// A slow runner only makes the calls longer, and the lease it is given shorter, so it can
// only make this surer.
func TestTheWholeMintIsBoundedByHalfTheLease(t *testing.T) {
	const ttl = 40 * time.Millisecond // half is 20 ms; three calls of 12 ms are 36
	m := newMem()
	p := &probe{mem: m, delay: 12 * time.Millisecond}
	k := NewSealed(ref, leaseFor(coord.Alone(), ttl))
	key, err := k.Get(context.Background(), p, true)
	if !errors.Is(err, ErrUnsealed) || key != nil {
		t.Fatalf("got %x, %v; want no key and ErrUnsealed", key, err)
	}
	if k.key != nil {
		t.Fatal("a mint that outran its lease left a key in the cache")
	}
}
