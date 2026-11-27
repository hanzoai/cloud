// Package funds is what the billing side tells the rest of the deployment when money,
// a plan or a cap changes in a way that may let a refused act go: credits added to a
// wallet, a plan opened, a cap raised or dropped.
//
// It is an EVENT, told once at the place the change is made, and heard by whoever was
// waiting on it: a coding run parked for a limit or a refusal of payment goes on when
// it is told (apps/coding). Nothing polls the balance, the plan or the cap to find
// out.
//
// It rides the deployment's coord bus, which delivers to the OTHER pods that listen. A
// listener on the coord that told it is called directly, since the bus does not deliver
// a pod its own message, so one process holding both halves (a test, a single-binary
// host) hears it too. The event carries the org it is about and what changed, and
// nothing else: a listener asks the stores of record for the rest.
package funds

import (
	"encoding/json"
	"sync"

	"github.com/hanzoai/cloud/sdk/coord"
)

// Subject is the channel on the coord bus the event is told on.
const Subject = "billing/funds"

// What changed.
const (
	Credit = "credit" // money was added to a wallet
	Plan   = "plan"   // a plan was opened or changed
	Cap    = "cap"    // a cap was set, raised or dropped
)

// Event is one change, and it names whom it is about: an org (a wallet credited, a plan
// opened, a cap on the org), or a plan (a cap on a plan, which each org on the plan
// reads). An event that names neither is about nobody and is heard as nothing: a change
// that may clear a refusal says whose.
type Event struct {
	Org  string `json:"org,omitempty"`
	Plan string `json:"plan,omitempty"`
	Kind string `json:"kind"`
}

// Names reports whether the event says whom it is about.
func (e Event) Names() bool { return e.Org != "" || e.Plan != "" }

var (
	mu    sync.Mutex
	hears = map[*coord.Coord]map[int]func(Event){}
	next  int
)

// Tell says e on c's bus, to the other pods, and to the listeners of c itself. Best
// effort, as the change that produced it is already made: a bus that is down loses a
// nudge, never the change, and what a listener waits for is kept in its own store of
// record, to be found again by the next nudge or the next start.
//
// An event that names nobody is not told: it cannot clear anything in particular, and a
// listener that took it for every org would try every refused act in the deployment.
func Tell(c *coord.Coord, e Event) {
	if !e.Names() {
		return
	}
	mu.Lock()
	local := make([]func(Event), 0, len(hears[c]))
	for _, fn := range hears[c] {
		local = append(local, fn)
	}
	mu.Unlock()
	for _, fn := range local {
		fn(e)
	}
	if c == nil {
		return
	}
	if b, err := json.Marshal(e); err == nil {
		_ = c.Publish(Subject, b)
	}
}

// Hear calls fn with each event told on c by this process or another pod, and answers
// the function that stops listening. gap is called when events may have been missed (the
// bus dropped this pod's connection): the listener then looks at its own store.
func Hear(c *coord.Coord, fn func(Event), gap func()) func() {
	mu.Lock()
	id := next
	next++
	if hears[c] == nil {
		hears[c] = map[int]func(Event){}
	}
	hears[c][id] = fn
	mu.Unlock()
	off := func() {}
	if c != nil {
		off = c.Subscribe(Subject, func(_ string, payload []byte) {
			var e Event
			if json.Unmarshal(payload, &e) == nil {
				fn(e)
			}
		}, gap)
	}
	return func() {
		off()
		mu.Lock()
		delete(hears[c], id)
		if len(hears[c]) == 0 {
			delete(hears, c)
		}
		mu.Unlock()
	}
}
