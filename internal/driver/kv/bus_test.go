package kv

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// heard collects what a subscriber is handed.
type heard struct {
	mu   sync.Mutex
	from []string
	got  []string
}

func (h *heard) hear(from string, p []byte) {
	h.mu.Lock()
	h.from, h.got = append(h.from, from), append(h.got, string(p))
	h.mu.Unlock()
}

func (h *heard) n() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.got)
}

// The KV carries the bus: a message reaches every other pod that listens, in order,
// and never the sender.
func TestPublishReachesTheOtherPodsInOrderOverTheKV(t *testing.T) {
	mr := miniredis.RunT(t)
	a, b, c := miniPod(t, mr, "cloud-0"), miniPod(t, mr, "cloud-1"), miniPod(t, mr, "cloud-2")
	var ha, hb, hc heard
	defer a.Subscribe("room", ha.hear, nil)()
	defer b.Subscribe("room", hb.hear, nil)()
	defer c.Subscribe("room", hc.hear, nil)()
	const n = 300
	for i := range n {
		if err := a.Publish("room", fmt.Appendf(nil, "m%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 5*time.Second, "both other pods hear all 300", func() bool { return hb.n() == n && hc.n() == n })
	for i, got := range hb.got {
		if want := fmt.Sprintf("m%d", i); got != want {
			t.Fatalf("pod b heard %q at %d, want %q: order is lost", got, i, want)
		}
	}
	if hb.from[0] != a.Owner() || hc.from[0] != a.Owner() {
		t.Fatalf("heard from %q and %q, want the sender %q", hb.from[0], hc.from[0], a.Owner())
	}
	time.Sleep(50 * time.Millisecond)
	if got := ha.n(); got != 0 {
		t.Fatalf("the sender heard %d of its own messages", got)
	}
}

// A question is answered by the pod that can.
func TestAQuestionIsAnsweredOverTheKV(t *testing.T) {
	mr := miniredis.RunT(t)
	a, b := miniPod(t, mr, "cloud-0"), miniPod(t, mr, "cloud-1")
	defer b.Serve("who", func(_ context.Context, _ string, q []byte) ([]byte, bool) {
		return append([]byte("b:"), q...), true
	})()
	ans, ok, err := a.Call(context.Background(), "who", []byte("x"))
	if err != nil || !ok || string(ans.Body) != "b:x" {
		t.Fatalf("answer %q ok %v err %v", ans.Body, ok, err)
	}
}

// A KV that drops every connection and comes back tells its listeners they were deaf.
func TestAReconnectIsAGapOverTheKV(t *testing.T) {
	mr := miniredis.RunT(t)
	a, b := miniPod(t, mr, "cloud-0"), miniPod(t, mr, "cloud-1")
	var hb heard
	var gaps atomic.Int32
	defer b.Subscribe("room", hb.hear, func() { gaps.Add(1) })()
	_ = a.Publish("room", []byte("before"))
	eventually(t, 3*time.Second, "before is heard", func() bool { return hb.n() == 1 })
	if gaps.Load() != 0 {
		t.Fatalf("a first subscription is no gap, got %d", gaps.Load())
	}
	mr.Close()
	if err := mr.Restart(); err != nil {
		t.Fatal(err)
	}
	eventually(t, 10*time.Second, "the listener is told it was deaf", func() bool { return gaps.Load() >= 1 })
	eventually(t, 10*time.Second, "messages flow again", func() bool {
		_ = a.Publish("room", []byte("after"))
		return hb.n() >= 2
	})
}

// What crosses the KV is sealed: the payload is not in it.
func TestTheKVCarriesNoPlaintext(t *testing.T) {
	mr := miniredis.RunT(t)
	a := miniPod(t, mr, "cloud-0")
	var mu sync.Mutex
	var raw [][]byte
	cancel, err := direct(t, "kv://"+mr.Addr()).Subscribe(a.Key("bus", "room"), func(m []byte) {
		mu.Lock()
		raw = append(raw, append([]byte(nil), m...))
		mu.Unlock()
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	secret := []byte("the chat line nobody else may read")
	_ = a.Publish("room", secret)
	eventually(t, 3*time.Second, "the message crosses", func() bool { mu.Lock(); defer mu.Unlock(); return len(raw) == 1 })
	mu.Lock()
	defer mu.Unlock()
	if bytes.Contains(raw[0], secret) {
		t.Fatal("the payload crossed the KV in the clear")
	}
}
