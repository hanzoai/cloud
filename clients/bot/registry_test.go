package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kv "github.com/hanzokv/go/v9"
)

// recorder collects what a session was sent. The send func runs on the invoking
// goroutine, so the slice needs a lock of its own — without one the test races
// the very thing it asserts about.
type recorder struct {
	mu     sync.Mutex
	frames [][]byte
}

func (r *recorder) add(b []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, append([]byte(nil), b...))
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames)
}

func session(org, node, conn string, sent *recorder) *Session {
	return &Session{
		Key:    NodeKey{Org: org, NodeID: node},
		ConnID: conn,
		send: func(_ context.Context, b []byte) error {
			if sent != nil {
				sent.add(b)
			}
			return nil
		},
	}
}

func frame(string) ([]byte, error) { return []byte("{}"), nil }

// The reason this port exists. Two orgs using the SAME node id must not reach
// each other — and the answer for a foreign node must be identical to the answer
// for a node that does not exist, or the error itself reveals another tenant.
func TestOrgsCannotReachEachOthersNodes(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(session("acme", "laptop-1", "c1", nil)); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(session("globex", "laptop-1", "c2", nil)); err != nil {
		t.Fatal(err)
	}

	// Same node id, two orgs, both registered: they are different nodes.
	if got := len(r.List("acme")); got != 1 {
		t.Fatalf("acme sees %d nodes, want 1", got)
	}
	if got := len(r.List("globex")); got != 1 {
		t.Fatalf("globex sees %d nodes, want 1", got)
	}

	// A third org sees nothing and gets the not-found answer, not a hint.
	if got := len(r.List("initech")); got != 0 {
		t.Fatalf("initech sees %d nodes, want 0", got)
	}
	_, err := r.Invoke(context.Background(), NodeKey{Org: "initech", NodeID: "laptop-1"}, frame, time.Second)
	if !errors.Is(err, ErrNoSuchNode) {
		t.Fatalf("cross-org invoke returned %v, want ErrNoSuchNode", err)
	}
}

// A node whose socket dies mid-call must fail immediately. Leaving the caller to
// time out holds it for the full timeout on a question already unanswerable.
func TestDisconnectFailsPendingInvokesImmediately(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(session("acme", "n1", "conn-1", nil)); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := r.Invoke(context.Background(), NodeKey{Org: "acme", NodeID: "n1"}, frame, 30*time.Second)
		done <- err
	}()

	// Let the invoke register its waiter, then drop the connection.
	time.Sleep(50 * time.Millisecond)
	r.Unregister("conn-1")

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, ErrNodeGone) {
			t.Fatalf("invoke returned %v; want a node-gone result", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("invoke did not fail on disconnect; it waited for the timeout instead")
	}
}

// A node that reconnects after a blip must be reachable again at once, not
// stuck behind a dead entry until something expires.
func TestReconnectReplacesTheStaleSession(t *testing.T) {
	r := NewRegistry()
	var first, second recorder
	if err := r.Register(session("acme", "n1", "old", &first)); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(session("acme", "n1", "new", &second)); err != nil {
		t.Fatal(err)
	}

	if got := len(r.List("acme")); got != 1 {
		t.Fatalf("reconnect left %d sessions, want 1", got)
	}
	go r.Invoke(context.Background(), NodeKey{Org: "acme", NodeID: "n1"}, frame, time.Second)
	time.Sleep(50 * time.Millisecond)
	if second.count() == 0 {
		t.Fatal("invoke went to the stale socket, not the reconnected one")
	}
}

// An answer nobody is waiting for is dropped. Queueing it would surface later as
// a reply to an unrelated call.
func TestOrphanAnswerIsDropped(t *testing.T) {
	r := NewRegistry()
	r.Answer("nobody-waiting", InvokeResult{OK: true}) // must not panic or block
}

func TestRegisterRejectsIncompleteSessions(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&Session{Key: NodeKey{NodeID: "n1"}, ConnID: "c"}); err == nil {
		t.Fatal("a session with no org was accepted")
	}
	if err := r.Register(&Session{Key: NodeKey{Org: "acme"}, ConnID: "c"}); err == nil {
		t.Fatal("a session with no node id was accepted")
	}
	if err := r.Register(session("acme", "n1", "c1", nil)); err != nil {
		t.Fatalf("a complete session was rejected: %v", err)
	}
}

// The gate is the single point where an invocation is authorized, and it sits at
// the socket — so nothing that writes to a node can go around it.
func TestGateRefusesAtTheSocket(t *testing.T) {
	var reached atomic.Bool
	r := NewRegistry(WithGate(func(_ *Session, command string, _ json.RawMessage) error {
		if command == "system.run" {
			return &Denied{Code: "COMMAND_NOT_ALLOWLISTED", Message: "no"}
		}
		return nil
	}))
	s := session("acme", "n1", "c1", nil)
	s.send = func(context.Context, []byte) error { reached.Store(true); return nil }
	if err := r.Register(s); err != nil {
		t.Fatal(err)
	}

	key := NodeKey{Org: "acme", NodeID: "n1"}
	_, err := r.Invoke(context.Background(), key, InvokeFrame(key, "system.run", nil, 0, ""), time.Second)
	var denied *Denied
	if !errors.As(err, &denied) || denied.Code != "COMMAND_NOT_ALLOWLISTED" {
		t.Fatalf("a denied command returned %v, want a *Denied carrying the code", err)
	}
	if reached.Load() {
		t.Fatal("a denied command was written to the node's socket")
	}
}

// ---------------------------------------------------------------------------
// A fake KV, so the REAL presence store is what these tests exercise: the real
// key layout, the real scripts, the real Nil handling. Only the server is fake,
// and its clock is the test's, which is what makes expiry deterministic.
// ---------------------------------------------------------------------------

type fakeEntry struct {
	val string
	exp time.Time // zero ⇒ no expiry
}

type fakeKV struct {
	mu    sync.Mutex
	now   time.Time
	data  map[string]fakeEntry
	gets  int
	sets  int
	evals int
	down  bool // every command fails, as a KV outage does
}

func newFakeKV() *fakeKV {
	return &fakeKV{now: time.Unix(1<<30, 0), data: map[string]fakeEntry{}}
}

func (f *fakeKV) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func (f *fakeKV) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

func (f *fakeKV) peek(key string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.get(key)
}

func (f *fakeKV) reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

// get must be called with the lock held.
func (f *fakeKV) get(key string) (string, bool) {
	e, ok := f.data[key]
	if !ok {
		return "", false
	}
	if !e.exp.IsZero() && !e.exp.After(f.now) {
		delete(f.data, key)
		return "", false
	}
	return e.val, true
}

func (f *fakeKV) Set(_ context.Context, key string, value any, ttl time.Duration) *kv.StatusCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets++
	if f.down {
		return kv.NewStatusResult("", errors.New("kv: down"))
	}
	e := fakeEntry{val: value.(string)}
	if ttl > 0 {
		e.exp = f.now.Add(ttl)
	}
	f.data[key] = e
	return kv.NewStatusResult("OK", nil)
}

func (f *fakeKV) Get(_ context.Context, key string) *kv.StringCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.down {
		return kv.NewStringResult("", errors.New("kv: down"))
	}
	v, ok := f.get(key)
	if !ok {
		return kv.NewStringResult("", kv.Nil)
	}
	return kv.NewStringResult(v, nil)
}

// Eval implements the documented effect of the two scripts this package sends.
// It matches on the script constants themselves, so a change to either script
// that this fake does not understand fails loudly instead of silently passing.
func (f *fakeKV) Eval(_ context.Context, script string, keys []string, args ...any) *kv.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evals++
	if f.down {
		return kv.NewCmdResult(nil, errors.New("kv: down"))
	}
	mine, _ := args[0].(string)
	cur, held := f.get(keys[0])

	switch script {
	case renewScript:
		if held && cur != mine {
			return kv.NewCmdResult(int64(0), nil)
		}
		ms, _ := args[1].(int64)
		f.data[keys[0]] = fakeEntry{val: mine, exp: f.now.Add(time.Duration(ms) * time.Millisecond)}
		return kv.NewCmdResult(int64(1), nil)

	case releaseScript:
		if held && cur == mine {
			delete(f.data, keys[0])
			return kv.NewCmdResult(int64(1), nil)
		}
		return kv.NewCmdResult(int64(0), nil)
	}
	return kv.NewCmdResult(nil, errors.New("fake kv: unknown script"))
}

// ---------------------------------------------------------------------------
// Two in-process replicas, forwarding to each other over the real HTTP hop.
// ---------------------------------------------------------------------------

const testPeerToken = "peer-secret"

// probe is a real invoke frame carrying arg as its params, so these tests drive
// the wire a node actually speaks — including the correlation id a hop stamps.
func probe(key NodeKey, arg []byte) Frame {
	params, err := json.Marshal(string(arg))
	if err != nil {
		panic(err)
	}
	return InvokeFrame(key, "probe", params, 0, "")
}

// probeArg reads back what probe put on the wire.
func probeArg(t *testing.T, wire []byte) (corrID string, arg string) {
	t.Helper()
	req, err := decodeInvoke(wire)
	if err != nil {
		t.Fatalf("a node was sent something that is not an invoke frame: %v", err)
	}
	if err := json.Unmarshal(paramsOf(req), &arg); err != nil {
		t.Fatalf("invoke params: %v", err)
	}
	return req.ID, arg
}

type replica struct {
	id   string
	reg  *Registry
	srv  *httptest.Server
	hits atomic.Int64
}

// cluster builds n replicas sharing one presence map, each able to forward to
// the others by id.
func cluster(t *testing.T, store PresenceStore, ids ...string) []*replica {
	t.Helper()
	reps := make([]*replica, len(ids))
	addrs := map[string]string{}
	resolve := func(id string) (string, bool) {
		a, ok := addrs[id]
		return a, ok
	}

	for i, id := range ids {
		r := &replica{id: id}
		r.reg = NewRegistry(WithCluster(Cluster{
			Replica:   id,
			Presence:  store,
			Hop:       NewHTTPHop(resolve, testPeerToken),
			TTL:       30 * time.Second,
			PeerToken: testPeerToken,
		}))

		mux := http.NewServeMux()
		peer := r.reg.PeerHandler()
		mux.Handle(PeerInvokePath, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			r.hits.Add(1)
			peer.ServeHTTP(w, req)
		}))
		r.srv = httptest.NewServer(mux)
		t.Cleanup(r.srv.Close)
		addrs[id] = r.srv.URL
		reps[i] = r
	}
	return reps
}

// attach registers a node that answers every invocation with reply + the
// argument it was sent.
func attach(t *testing.T, r *replica, org, node, conn string, reply []byte) {
	t.Helper()
	reg := r.reg
	s := &Session{
		Key:    NodeKey{Org: org, NodeID: node},
		ConnID: conn,
		send: func(_ context.Context, b []byte) error {
			corrID, arg := probeArg(t, b)
			go reg.Answer(corrID, InvokeResult{OK: true, Payload: append(append([]byte{}, reply...), arg...)})
			return nil
		},
	}
	if err := reg.Register(s); err != nil {
		t.Fatalf("register %s/%s on %s: %v", org, node, r.id, err)
	}
}

// The whole point: a node holds its socket to ONE replica, and an invoke that
// lands anywhere else still reaches it.
func TestNodeOnOneReplicaIsInvocableFromAnother(t *testing.T) {
	f := newFakeKV()
	reps := cluster(t, NewKVPresence(f), "cloud-0", "cloud-1")
	a, b := reps[0], reps[1]
	attach(t, a, "acme", "laptop-1", "c1", []byte("pong:"))

	key := NodeKey{Org: "acme", NodeID: "laptop-1"}
	res, err := b.reg.Invoke(context.Background(), key, probe(key, []byte("ping")), 2*time.Second)
	if err != nil {
		t.Fatalf("invoke from the replica that does NOT hold the socket: %v", err)
	}
	if !res.OK || string(res.Payload) != "pong:ping" {
		t.Fatalf("relayed answer was %+v, want the node's own answer", res)
	}
	if got := a.hits.Load(); got != 1 {
		t.Fatalf("owning replica saw %d forwarded calls, want 1", got)
	}

	// And the owner still answers locally, without a forward or a KV lookup.
	beforeGets, beforeHits := f.reads(), a.hits.Load()
	if _, err := a.reg.Invoke(context.Background(), key, probe(key, []byte("ping")), 2*time.Second); err != nil {
		t.Fatalf("local invoke on the owning replica: %v", err)
	}
	if f.reads() != beforeGets {
		t.Fatal("a locally-held node cost a presence lookup; the single-replica path must not consult the KV")
	}
	if a.hits.Load() != beforeHits {
		t.Fatal("the owning replica forwarded an invocation to itself")
	}
}

// The reason org is in the key. Two orgs may use the same node id: the foreign
// org's lookup must MISS — not resolve to the real owner and forward there —
// and its answer must be the one a nonexistent node gets.
func TestForeignOrgNeverResolvesAndIsNeverForwarded(t *testing.T) {
	f := newFakeKV()
	reps := cluster(t, NewKVPresence(f), "cloud-0", "cloud-1")
	a, b := reps[0], reps[1]
	attach(t, a, "acme", "laptop-1", "c1", []byte("acme:"))

	theirs := NodeKey{Org: "globex", NodeID: "laptop-1"}
	nobody := NodeKey{Org: "globex", NodeID: "no-such-machine"}
	_, foreign := b.reg.Invoke(context.Background(), theirs, probe(theirs, []byte("ping")), time.Second)
	_, absent := b.reg.Invoke(context.Background(), nobody, probe(nobody, []byte("ping")), time.Second)
	if !errors.Is(foreign, ErrNoSuchNode) {
		t.Fatalf("another tenant's node answered %v, want ErrNoSuchNode", foreign)
	}
	if !errors.Is(absent, ErrNoSuchNode) {
		t.Fatalf("a nonexistent node answered %v, want ErrNoSuchNode", absent)
	}
	if a.hits.Load() != 0 {
		t.Fatal("a foreign org's invocation was forwarded to the replica holding another tenant's node")
	}

	// Same id, two orgs, two claims: they are different nodes all the way down.
	attach(t, b, "globex", "laptop-1", "c2", []byte("globex:"))
	res, err := a.reg.Invoke(context.Background(), theirs, probe(theirs, []byte("ping")), 2*time.Second)
	if err != nil {
		t.Fatalf("globex's own node: %v", err)
	}
	if string(res.Payload) != "globex:ping" {
		t.Fatalf("globex reached %q — the wrong tenant's node", res.Payload)
	}
}

func TestNodeNobodyHoldsIsNotFound(t *testing.T) {
	f := newFakeKV()
	reps := cluster(t, NewKVPresence(f), "cloud-0", "cloud-1")

	ghost := NodeKey{Org: "acme", NodeID: "ghost"}
	_, err := reps[1].reg.Invoke(context.Background(), ghost, probe(ghost, []byte("ping")), time.Second)
	if !errors.Is(err, ErrNoSuchNode) {
		t.Fatalf("unheld node answered %v, want ErrNoSuchNode", err)
	}
	if reps[0].hits.Load() != 0 {
		t.Fatal("an unheld node was forwarded somewhere")
	}
}

// A replica that dies takes its sockets with it. Its claims must expire, or
// every peer keeps forwarding into a pod that is gone.
func TestDeadReplicaExpiresInsteadOfStrandingTheNode(t *testing.T) {
	f := newFakeKV()
	reps := cluster(t, NewKVPresence(f), "cloud-0", "cloud-1")
	a, b := reps[0], reps[1]
	attach(t, a, "acme", "n1", "c1", []byte("ok:"))

	key := NodeKey{Org: "acme", NodeID: "n1"}
	if _, err := b.reg.Invoke(context.Background(), key, probe(key, []byte("ping")), 2*time.Second); err != nil {
		t.Fatalf("before the replica died: %v", err)
	}

	// cloud-0 dies: no unregister, no release, no renew — just silence.
	a.srv.Close()
	before := a.hits.Load()
	f.advance(31 * time.Second) // one TTL

	_, err := b.reg.Invoke(context.Background(), key, probe(key, []byte("ping")), time.Second)
	if !errors.Is(err, ErrNoSuchNode) {
		t.Fatalf("after the owning replica died the node answered %v, want ErrNoSuchNode", err)
	}
	if a.hits.Load() != before {
		t.Fatal("an invocation was still forwarded to the dead replica")
	}

	// The node reconnects elsewhere and is reachable again at once.
	attach(t, b, "acme", "n1", "c2", []byte("ok:"))
	if _, err := b.reg.Invoke(context.Background(), key, probe(key, []byte("ping")), 2*time.Second); err != nil {
		t.Fatalf("after reconnecting to another replica: %v", err)
	}
}

// A node that moves replicas is torn down on the old one LAST. That teardown
// must not delete the claim the new owner just took, or the node goes dark until
// something expires.
func TestReleaseOnlyDropsOurOwnClaim(t *testing.T) {
	f := newFakeKV()
	reps := cluster(t, NewKVPresence(f), "cloud-0", "cloud-1")
	a, b := reps[0], reps[1]
	key := NodeKey{Org: "acme", NodeID: "n1"}

	attach(t, a, "acme", "n1", "c1", []byte("a:"))
	attach(t, b, "acme", "n1", "c2", []byte("b:")) // moved
	a.reg.Unregister("c1")                         // the old socket's teardown, late

	if v, ok := f.peek(presenceKey(key)); !ok || v != "cloud-1" {
		t.Fatalf("presence is %q/%v after the old replica cleaned up; want cloud-1 still holding it", v, ok)
	}
	res, err := a.reg.Invoke(context.Background(), key, probe(key, []byte("ping")), 2*time.Second)
	if err != nil {
		t.Fatalf("invoke after the move: %v", err)
	}
	if string(res.Payload) != "b:ping" {
		t.Fatalf("reached %q, want the node on its new replica", res.Payload)
	}
}

// The same race, seen from the renew loop: a replica that has not yet noticed
// its socket died must not drag the claim back from the replica now holding it.
func TestRenewDoesNotStealAClaimAnotherReplicaTook(t *testing.T) {
	f := newFakeKV()
	reps := cluster(t, NewKVPresence(f), "cloud-0", "cloud-1")
	a, b := reps[0], reps[1]
	key := NodeKey{Org: "acme", NodeID: "n1"}

	attach(t, a, "acme", "n1", "c1", []byte("a:"))
	attach(t, b, "acme", "n1", "c2", []byte("b:"))

	a.reg.renewAll(context.Background())
	if v, _ := f.peek(presenceKey(key)); v != "cloud-1" {
		t.Fatalf("renew moved the claim to %q; a renew may extend, never steal", v)
	}

	// The owner's renew does extend it: the claim outlives one TTL.
	f.advance(20 * time.Second)
	b.reg.renewAll(context.Background())
	f.advance(20 * time.Second)
	if v, ok := f.peek(presenceKey(key)); !ok || v != "cloud-1" {
		t.Fatalf("presence is %q/%v after a renew by its owner; want it still held", v, ok)
	}
}

// A claim that could not be written keeps the node working locally and heals on
// the next tick. Refusing the registration would drop a fleet on a KV blip.
func TestClaimFailureIsLocalOnlyAndHeals(t *testing.T) {
	f := newFakeKV()
	f.setDown(true)
	reps := cluster(t, NewKVPresence(f), "cloud-0", "cloud-1")
	a, b := reps[0], reps[1]
	key := NodeKey{Org: "acme", NodeID: "n1"}

	attach(t, a, "acme", "n1", "c1", []byte("a:")) // must not fail
	if _, err := a.reg.Invoke(context.Background(), key, probe(key, []byte("ping")), 2*time.Second); err != nil {
		t.Fatalf("the node is attached here; a local invoke must work with the KV down: %v", err)
	}
	if _, err := b.reg.Invoke(context.Background(), key, probe(key, []byte("ping")), time.Second); !errors.Is(err, ErrNoSuchNode) {
		t.Fatalf("with no presence entry a peer answered %v, want ErrNoSuchNode", err)
	}

	f.setDown(false)
	a.reg.renewAll(context.Background())
	if _, err := b.reg.Invoke(context.Background(), key, probe(key, []byte("ping")), 2*time.Second); err != nil {
		t.Fatalf("after the KV came back the claim did not heal: %v", err)
	}
}

// Presence is a hint; the socket table is the truth. A claim of our own that we
// can no longer honour must read as not-found, not send us round the network to
// ask ourselves.
func TestStaleSelfClaimIsNotFoundNotASelfForward(t *testing.T) {
	f := newFakeKV()
	store := NewKVPresence(f)
	reps := cluster(t, store, "cloud-0", "cloud-1")
	a := reps[0]
	key := NodeKey{Org: "acme", NodeID: "n1"}

	// A claim this replica holds for a socket it does not: the state a crash
	// between register and teardown leaves behind.
	if err := store.Claim(context.Background(), key, "cloud-0", 30*time.Second); err != nil {
		t.Fatal(err)
	}

	_, err := a.reg.Invoke(context.Background(), key, probe(key, []byte("ping")), time.Second)
	if !errors.Is(err, ErrNoSuchNode) {
		t.Fatalf("a stale claim of our own answered %v, want ErrNoSuchNode", err)
	}
	if a.hits.Load() != 0 {
		t.Fatal("a replica forwarded an invocation to itself")
	}
}

// The peer endpoint takes an org from a body, so the token is the only thing
// standing between it and a cross-tenant invoke primitive.
func TestPeerHandlerRefusesWithoutTheRightToken(t *testing.T) {
	f := newFakeKV()
	reps := cluster(t, NewKVPresence(f), "cloud-0", "cloud-1")
	attach(t, reps[0], "acme", "n1", "c1", []byte("a:"))

	body := strings.NewReader(`{"org":"acme","node":"n1","timeout_ms":1000}`)
	req, _ := http.NewRequest(http.MethodPost, reps[0].srv.URL+PeerInvokePath, body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an untokened peer call got %d, want 403", resp.StatusCode)
	}

	// With no token configured the endpoint serves nothing at all.
	closed := NewRegistry(WithCluster(Cluster{
		Replica:  "cloud-2",
		Presence: NewKVPresence(f),
		Hop:      NewHTTPHop(func(string) (string, bool) { return "", false }, ""),
	}))
	rec := httptest.NewRecorder()
	closed.PeerHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, PeerInvokePath, strings.NewReader(`{"org":"acme","node":"n1"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a peer endpoint with no token configured got %d, want 503", rec.Code)
	}
}

// A peer may forward an invocation; it may not make this replica write whatever
// it likes into one of its sockets. Only a node.invoke.request survives the hop.
func TestPeerHandlerRefusesAFrameThatIsNotAnInvoke(t *testing.T) {
	f := newFakeKV()
	reps := cluster(t, NewKVPresence(f), "cloud-0", "cloud-1")
	var wrote atomic.Bool
	s := session("acme", "n1", "c1", nil)
	s.send = func(context.Context, []byte) error { wrote.Store(true); return nil }
	if err := reps[0].reg.Register(s); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(peerInvoke{
		Org:       "acme",
		NodeID:    "n1",
		Frame:     []byte(`{"type":"event","event":"connect.challenge","payload":{}}`),
		TimeoutMS: 1000,
	})
	req, _ := http.NewRequest(http.MethodPost, reps[0].srv.URL+PeerInvokePath, bytes.NewReader(body))
	req.Header.Set(peerTokenHeader, testPeerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var ans peerAnswer
	if err := json.NewDecoder(resp.Body).Decode(&ans); err != nil {
		t.Fatal(err)
	}
	if ans.Err != peerErrFailed {
		t.Fatalf("a non-invoke frame answered %+v, want a refusal", ans)
	}
	if wrote.Load() {
		t.Fatal("a peer wrote a frame of its own choosing into this replica's socket")
	}
}

// A refusal must read the same wherever the socket is, or the caller's answer
// depends on which replica the load balancer picked.
func TestGateRefusalSurvivesAHop(t *testing.T) {
	f := newFakeKV()
	store := NewKVPresence(f)
	addrs := map[string]string{}
	resolve := func(id string) (string, bool) { a, ok := addrs[id]; return a, ok }

	owner := NewRegistry(
		WithCluster(Cluster{Replica: "cloud-0", Presence: store, Hop: NewHTTPHop(resolve, testPeerToken), PeerToken: testPeerToken}),
		WithGate(func(_ *Session, command string, _ json.RawMessage) error {
			return &Denied{Code: "COMMAND_NOT_DECLARED", Message: "node did not declare " + command}
		}),
	)
	srv := httptest.NewServer(owner.PeerHandler())
	defer srv.Close()
	addrs["cloud-0"] = srv.URL

	caller := NewRegistry(WithCluster(Cluster{
		Replica: "cloud-1", Presence: store, Hop: NewHTTPHop(resolve, testPeerToken), PeerToken: testPeerToken,
	}))

	var wrote atomic.Bool
	s := session("acme", "n1", "c1", nil)
	s.send = func(context.Context, []byte) error { wrote.Store(true); return nil }
	if err := owner.Register(s); err != nil {
		t.Fatal(err)
	}

	key := NodeKey{Org: "acme", NodeID: "n1"}
	_, err := caller.Invoke(context.Background(), key, probe(key, []byte("ping")), 2*time.Second)
	var denied *Denied
	if !errors.As(err, &denied) || denied.Code != "COMMAND_NOT_DECLARED" {
		t.Fatalf("a forwarded invocation the owner refused returned %v, want the owner's *Denied", err)
	}
	if wrote.Load() {
		t.Fatal("a refused invocation still reached the node's socket")
	}
}

// A node id is chosen by the node. Two tenants must not be able to collide onto
// one presence key by choosing one that swallows the separator.
func TestPresenceKeyIsInjectiveAcrossOrgAndNode(t *testing.T) {
	a := presenceKey(NodeKey{Org: "acme", NodeID: "x:y"})
	b := presenceKey(NodeKey{Org: "acme:x", NodeID: "y"})
	if a == b {
		t.Fatalf("two different (org, node) pairs share the key %q", a)
	}
	if !strings.HasPrefix(a, presencePrefix+"acme:") {
		t.Fatalf("key %q does not sit in its org's namespace", a)
	}
}

// A registry wired for a cluster it cannot reach must refuse to serve: half a
// cluster leaves nodes silently unreachable from every replica but their own,
// which is the failure nobody notices.
func TestHalfWiredClusterRefusesToServe(t *testing.T) {
	store := NewKVPresence(newFakeKV())
	hop := NewHTTPHop(func(string) (string, bool) { return "", false }, "t")
	for _, c := range []Cluster{
		{Presence: store, Hop: hop},           // no replica id
		{Replica: "cloud-0", Hop: hop},        // no presence map
		{Replica: "cloud-0", Presence: store}, // no way to forward
	} {
		r := NewRegistry(WithCluster(c))
		if err := r.Register(session("acme", "n1", "c1", nil)); err == nil {
			t.Fatalf("a registry wired with %+v accepted a node", c)
		}
		key := NodeKey{Org: "acme", NodeID: "n1"}
		if _, err := r.Invoke(context.Background(), key, probe(key, nil), time.Second); err == nil {
			t.Fatalf("a registry wired with %+v served an invocation", c)
		}
		rec := httptest.NewRecorder()
		r.PeerHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, PeerInvokePath, strings.NewReader(`{}`)))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("a half-wired peer endpoint answered %d, want 503", rec.Code)
		}
	}
}

// Size must not decide reachability. A payload a socket accepts must survive a
// hop, or the same invocation succeeds or fails depending on which replica the
// load balancer picked — the bug this routing exists to remove, wearing a
// different hat.
func TestHopCarriesWhateverASocketCarries(t *testing.T) {
	if peerMaxBody <= maxFrameBytes {
		t.Fatalf("a hop caps bodies at %d but a socket accepts frames of %d: the difference is the set of invocations that only work on one replica", peerMaxBody, maxFrameBytes)
	}

	f := newFakeKV()
	reps := cluster(t, NewKVPresence(f), "cloud-0", "cloud-1")
	a, b := reps[0], reps[1]

	// Comfortably past the 1 MiB an earlier cap allowed, in both directions:
	// base64 alone would put the request body over it.
	big := bytes.Repeat([]byte("x"), 2<<20)
	attach(t, a, "acme", "n1", "c1", big)

	key := NodeKey{Org: "acme", NodeID: "n1"}
	res, err := b.reg.Invoke(context.Background(), key, probe(key, big), 10*time.Second)
	if err != nil {
		t.Fatalf("forwarding a %d-byte invocation: %v", len(big), err)
	}
	if len(res.Payload) != 2*len(big) {
		t.Fatalf("relayed %d bytes, want %d — a hop truncated what the node answered", len(res.Payload), 2*len(big))
	}
}

// There is ONE Registry, so the socket transport cannot be handed a form of it
// that skips the presence claim — the wiring that type-checks is the wiring that
// works.
func TestTheTransportTakesTheOneRegistry(t *testing.T) {
	f := newFakeKV()
	reps := cluster(t, NewKVPresence(f), "cloud-0", "cloud-1")
	a, b := reps[0], reps[1]
	_ = NodeWS(a.reg, WSOptions{}) // the socket layer holds exactly this value

	key := NodeKey{Org: "acme", NodeID: "n1"}
	attach(t, a, "acme", "n1", "c1", []byte("seam:"))
	if v, ok := f.peek(presenceKey(key)); !ok || v != "cloud-0" {
		t.Fatalf("presence is %q/%v after a registration; want cloud-0 — the claim was skipped", v, ok)
	}
	res, err := b.reg.Invoke(context.Background(), key, probe(key, []byte("ping")), 2*time.Second)
	if err != nil {
		t.Fatalf("a registered node is unreachable from its peer: %v", err)
	}
	if string(res.Payload) != "seam:ping" {
		t.Fatalf("reached %q", res.Payload)
	}

	a.reg.Unregister("c1")
	if v, ok := f.peek(presenceKey(key)); ok {
		t.Fatalf("claim %q survived an unregister", v)
	}
}

// Presence is derived from the socket table, never mirrored beside it: a mirror
// that missed a teardown would renew a claim for a socket that is gone, forever,
// because nothing left in it ever expires.
func TestPresenceIsDerivedFromTheSocketTable(t *testing.T) {
	f := newFakeKV()
	reps := cluster(t, NewKVPresence(f), "cloud-0", "cloud-1")
	a := reps[0]
	key := NodeKey{Org: "acme", NodeID: "n1"}

	attach(t, a, "acme", "n1", "c1", []byte("a:"))

	// A claim lost to a blip is re-taken from the table, not from a bookkeeping
	// copy that may or may not still agree with it.
	f.advance(31 * time.Second)
	if v, ok := f.peek(presenceKey(key)); ok {
		t.Fatalf("claim %q outlived its TTL", v)
	}
	a.reg.renewAll(context.Background())
	if v, ok := f.peek(presenceKey(key)); !ok || v != "cloud-0" {
		t.Fatalf("presence is %q/%v after a renew; want the live socket re-claimed", v, ok)
	}

	// And a socket that is gone is renewed by nothing.
	a.reg.Unregister("c1")
	f.advance(31 * time.Second)
	a.reg.renewAll(context.Background())
	if v, ok := f.peek(presenceKey(key)); ok {
		t.Fatalf("claim %q outlived its socket and was renewed", v)
	}
}

// Run releases what this replica holds when it drains, so peers stop forwarding
// into it before the TTL would have caught up.
func TestDrainReleasesClaims(t *testing.T) {
	f := newFakeKV()
	reps := cluster(t, NewKVPresence(f), "cloud-0", "cloud-1")
	a := reps[0]
	attach(t, a, "acme", "n1", "c1", []byte("a:"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.reg.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return when its context ended")
	}

	if v, ok := f.peek(presenceKey(NodeKey{Org: "acme", NodeID: "n1"})); ok {
		t.Fatalf("a drained replica still advertises %q", v)
	}
}

// A single-replica registry has nothing to advertise, so Run is done at once —
// no ticker, no goroutine left behind by a deployment that never clustered.
func TestRunIsANoOpWithoutACluster(t *testing.T) {
	done := make(chan struct{})
	go func() { NewRegistry().Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run blocked on a registry with no cluster wiring")
	}
}
