package pubsub

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/nats-io/nats-server/v2/server"

	_ "github.com/hanzoai/cloud/internal/driver/kv"
	"github.com/hanzoai/cloud/sdk/coord"
)

// ackWindow is the window coord gives the listeners of a question to say so.
const ackWindow = 150 * time.Millisecond

var secret = []byte("0123456789abcdef0123456789abcdef")

var fast = coord.Periods{Presence: time.Second, Skew: 500 * time.Millisecond, Refresh: 50 * time.Millisecond}

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

type heard struct {
	mu  sync.Mutex
	got []string
}

func (h *heard) hear(_ string, p []byte) {
	h.mu.Lock()
	h.got = append(h.got, string(p))
	h.mu.Unlock()
}

func (h *heard) n() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.got)
}

// natsd is a real nats-server, in process: the shared pubsub cluster of a deployment.
type natsd struct {
	t    *testing.T
	port int
	user string
	pass string
	srv  *server.Server
}

func startNATS(t *testing.T) *natsd {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	n := &natsd{t: t, port: port, user: "cloud", pass: "s3cret-for-tests"}
	n.start()
	t.Cleanup(func() { n.stop() })
	return n
}

func (n *natsd) start() {
	n.t.Helper()
	srv, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: n.port, Username: n.user, Password: n.pass, NoLog: true, NoSigs: true, MaxPayload: 8 << 20})
	if err != nil {
		n.t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		n.t.Fatal("nats-server did not start")
	}
	n.srv = srv
}

func (n *natsd) stop() {
	if n.srv != nil {
		n.srv.Shutdown()
		n.srv.WaitForShutdown()
		n.srv = nil
	}
}

func (n *natsd) url() string {
	return "nats://" + n.user + ":" + n.pass + "@127.0.0.1:" + strconv.Itoa(n.port)
}

// natsPod is a pod whose KV is mr and whose bus is the pubsub cluster.
func natsPod(t *testing.T, mr *miniredis.Miniredis, n *natsd, id string, secretKey []byte) *coord.Coord {
	t.Helper()
	c, err := coord.New(coord.Config{Periods: fast, URL: "kv://" + mr.Addr(), BusURL: n.url(), Deployment: "test", Self: coord.Member{ID: id, Addr: id + ":8443"}, Instance: id, Secret: secretKey})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

// deployment is pods on one KV and one pubsub cluster.
type deployment struct {
	pod  func(id string) *coord.Coord
	nats *natsd
}

func natsDeployment(t *testing.T) deployment {
	mr := miniredis.RunT(t)
	n := startNATS(t)
	return deployment{pod: func(id string) *coord.Coord { return natsPod(t, mr, n, id, secret) }, nats: n}
}

// A message reaches the other pods in order and never the sender.
func TestPublishReachesTheOtherPodsInOrderOverPubsub(t *testing.T) {
	d := natsDeployment(t)
	a, b := d.pod("cloud-0"), d.pod("cloud-1")
	var ha, hb heard
	defer a.Subscribe("room", ha.hear, nil)()
	defer b.Subscribe("room", hb.hear, nil)()
	const n = 300
	for i := range n {
		if err := a.Publish("room", fmt.Appendf(nil, "m%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 5*time.Second, "the other pod hears all 300", func() bool { return hb.n() == n })
	for i, got := range hb.got {
		if want := fmt.Sprintf("m%d", i); got != want {
			t.Fatalf("heard %q at %d, want %q: order is lost", got, i, want)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if got := ha.n(); got != 0 {
		t.Fatalf("the sender heard %d of its own messages", got)
	}
}

// A cluster that goes away and comes back tells its listeners they were deaf.
func TestAReconnectIsAGapOverPubsub(t *testing.T) {
	d := natsDeployment(t)
	a, b := d.pod("cloud-0"), d.pod("cloud-1")
	var hb heard
	var gaps atomic.Int32
	defer b.Subscribe("room", hb.hear, func() { gaps.Add(1) })()
	_ = a.Publish("room", []byte("before"))
	eventually(t, 3*time.Second, "before is heard", func() bool { return hb.n() == 1 })
	d.nats.stop()
	time.Sleep(200 * time.Millisecond)
	d.nats.start()
	eventually(t, 10*time.Second, "the listener is told it was deaf", func() bool { return gaps.Load() >= 1 })
	eventually(t, 10*time.Second, "messages flow again", func() bool {
		_ = a.Publish("room", []byte("after"))
		return hb.n() >= 2
	})
}

// A message sent on the pubsub cluster is readable only by what holds the master, and
// the cluster sees no subject name of the deployment's.
func TestThePubsubCarriesNoPlaintextNorSubjectNames(t *testing.T) {
	d := natsDeployment(t)
	a, b := d.pod("cloud-0"), d.pod("cloud-1")
	var hb heard
	defer b.Subscribe("secret-room", hb.hear, nil)()
	if err := a.Publish("secret-room", []byte("the launch code")); err != nil {
		t.Fatal(err)
	}
	eventually(t, 3*time.Second, "heard", func() bool { return hb.n() == 1 })
	if got := subjectOf(a.Key("bus", "secret-room")); len(got) != len("coord.")+32 {
		t.Fatalf("subject %q is not a digest under coord.", got)
	}
}

// A question to a subject nobody serves is answered once the listeners have had their
// window, and a question to a serving pod is answered as soon as it answers, however
// long the pod works: the window bounds the saying-so, not the work.
func TestAnAckedQuestionWaitsForTheWorkNotTheWindow(t *testing.T) {
	d := natsDeployment(t)
	a, b := d.pod("cloud-0"), d.pod("cloud-1")
	defer b.Serve("slow", func(ctx context.Context, _ string, q []byte) ([]byte, bool) {
		time.Sleep(3 * ackWindow)
		return []byte("late " + string(q)), true
	})()
	start := time.Now()
	ans, ok, err := a.Call(context.Background(), "slow", []byte("x"))
	if err != nil || !ok || string(ans.Body) != "late x" {
		t.Fatalf("a slow yes: %q ok %v err %v", ans.Body, ok, err)
	}
	if took := time.Since(start); took < 3*ackWindow {
		t.Fatalf("answered in %v, before the work could have been done", took)
	}
	// Gather waits for the slow pod too, and counts it.
	all, err := a.Gather(context.Background(), "slow", []byte("y"))
	if err != nil || len(all) != 1 {
		t.Fatalf("gather: %v %v", all, err)
	}
	start = time.Now()
	if _, ok, err := a.Call(context.Background(), "unserved", nil); ok || err != nil {
		t.Fatalf("an unserved subject: ok %v err %v", ok, err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("an unserved subject took %v", took)
	}
}

// Two pods and a handler that says no: the asker hears from everyone and stops.
func TestAnAckedQuestionStopsWhenEveryListenerHasSaidNo(t *testing.T) {
	d := natsDeployment(t)
	a, b, c := d.pod("cloud-0"), d.pod("cloud-1"), d.pod("cloud-2")
	var asked atomic.Int32
	no := func(context.Context, string, []byte) ([]byte, bool) { asked.Add(1); return nil, false }
	defer b.Serve("q", no)()
	defer c.Serve("q", no)()
	start := time.Now()
	_, ok, err := a.Call(context.Background(), "q", nil)
	if ok || err != nil {
		t.Fatalf("ok %v err %v", ok, err)
	}
	if asked.Load() != 2 {
		t.Fatalf("%d pods were asked, want 2", asked.Load())
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("took %v", took)
	}
}

// A bus dialed with the wrong password is not a bus: the pod cannot publish, and a
// right one can.
func TestTheWrongPasswordIsNoBus(t *testing.T) {
	mr := miniredis.RunT(t)
	n := startNATS(t)
	bad, err := coord.New(coord.Config{Periods: fast, URL: "kv://" + mr.Addr(), BusURL: fmt.Sprintf("nats://cloud:wrong@127.0.0.1:%d", n.port), Deployment: "test", Self: coord.Member{ID: "x"}, Instance: "x", Secret: secret})
	if err != nil {
		return // refused at once is also correct
	}
	defer bad.Close(context.Background())
	good := natsPod(t, mr, n, "cloud-1", secret)
	var h heard
	defer good.Subscribe("room", h.hear, nil)()
	_ = bad.Publish("room", []byte("from a pod with the wrong password"))
	time.Sleep(500 * time.Millisecond)
	if h.n() != 0 {
		t.Fatal("a pod with the wrong password was heard")
	}
}
