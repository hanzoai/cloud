package funds

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/coord/coordtest"
)

// An event is heard by every listener, in the process that told it and on the other pods,
// once each, and by nobody after they stop listening.
//
// An event that names no org and no plan is told to nobody.
//
// MUTATION: publish to the bus and call no local listener, and the process that told it
// (one host holding both halves) never hears; call the local listeners and publish nothing,
// and the other pods never do; tell an event that names nobody and every pod hears it.
func TestAnEventIsHeardOnEveryPodOnce(t *testing.T) {
	pod := coordtest.Memory(t)
	a, b := pod("cloud-0"), pod("cloud-1")
	var mu sync.Mutex
	heard := map[string][]Event{}
	hear := func(id string) func(Event) {
		return func(e Event) { mu.Lock(); heard[id] = append(heard[id], e); mu.Unlock() }
	}
	stopA := Hear(a, hear("a"), nil)
	stopB := Hear(b, hear("b"), nil)
	wait := func(n int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			ok := len(heard["a"]) >= n && len(heard["b"]) >= n
			mu.Unlock()
			if ok {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("heard %v", heard)
			}
			time.Sleep(time.Millisecond)
		}
	}

	Tell(a, Event{Org: "acme", Kind: Credit})
	wait(1)
	Tell(b, Event{Plan: "go", Kind: Cap})
	wait(2)
	Tell(a, Event{Kind: Credit}) // names nobody: heard by nobody
	Tell(b, Event{Kind: Cap})
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if want := []Event{{Org: "acme", Kind: Credit}, {Plan: "go", Kind: Cap}}; !slices.Equal(heard["a"], want) || !slices.Equal(heard["b"], want) {
		t.Fatalf("heard %+v, want both pods to hear both events once, in order", heard)
	}
	mu.Unlock()
	stopA()
	stopB()
	Tell(a, Event{Org: "acme", Kind: Plan})
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(heard["a"]) != 2 || len(heard["b"]) != 2 {
		t.Fatalf("a listener that stopped heard %+v", heard)
	}
}
