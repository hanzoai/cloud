// Copyright © 2026 Hanzo AI. MIT License.

package planetest

// economic.go — the event plane's economic half, in memory.
//
// A rail states every payment between two orgs on the event plane after it
// settles, so a test that settles one and reads a year needs this peer. Its store
// is a bus and a warehouse no test process has, so the two ops are answered here
// under the contract client/economic.go states — and nothing more: a statement
// names a rail and a proof, and the event is what the RAIL answers for them, asked
// on the rail's own socket exactly as the event app asks it.

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	x402peer "github.com/hanzoai/cloud/client/x402"
	"github.com/zap-proto/zip"
)

// Economic holds every economic event stated to it: one per (rail, proof), the
// first statement kept.
type Economic struct {
	mu   sync.Mutex
	rows []client.EconomicEvent
}

// Events binds the event plane's two economic ops and returns their store. A
// statement is the rail's answer for the proof it names, and only a party to it
// may ask; a read answers only the events the caller's org is party to, with the
// ones a correction restates named. A page holds at most two events, so a reader
// that does not follow Next reads part of a year.
func Events(t *testing.T) *Economic {
	t.Helper()
	e := &Economic{}
	runtimeDir(t)
	app := zip.New(zip.Config{AppName: "event"})
	app.Post("/event/economic", e.state, zip.WithOperationID(client.EventEconomic))
	app.Post("/event/economics", e.list, zip.WithOperationID(client.EventEconomics))
	listen(t, app, "event")
	return e
}

// All is every event stated, in the order first stated.
func (e *Economic) All() []client.EconomicEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.rows)
}

// Await is every event stated, once at least n have been: a rail states off the
// path that settled the payment, from its own drainer, so a test in another
// process from the rail waits for it here.
func (e *Economic) Await(t *testing.T, n int) []client.EconomicEvent {
	t.Helper()
	deadline := time.Now().Add(awaitStated)
	for {
		if got := e.All(); len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// awaitStated bounds Await: a nudged drainer states within one pass.
const awaitStated = 10 * time.Second

// Put records an event as though its rail's owner had stated it — for a rail no
// app in the test's fleet runs.
func (e *Economic) Put(ev client.EconomicEvent) string {
	ev.V, ev.ID = client.EconomicVersion, client.EconomicID(ev.Rail, ev.Proof)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !slices.ContainsFunc(e.rows, func(r client.EconomicEvent) bool { return r.ID == ev.ID }) {
		e.rows = append(e.rows, ev)
	}
	return ev.ID
}

func (e *Economic) state(ctx context.Context, in *client.EconomicNotice) (*client.EconomicStated, error) {
	org := zip.CallerOf(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("economic: no org on the call")
	}
	if in.Rail != client.RailX402 {
		return nil, zip.ErrForbidden("economic: no app in this fleet owns rail " + in.Rail)
	}
	said, err := x402peer.X402Economic(ctx, in)
	if err != nil {
		return nil, err
	}
	if org != said.Buyer && org != said.Seller {
		return nil, zip.ErrForbidden("economic: the call acts for no party to this payment")
	}
	return &client.EconomicStated{ID: e.Put(*said), Landed: true}, nil
}

func (e *Economic) list(ctx context.Context, in *client.EconomicsIn) (*client.Economics, error) {
	org := zip.CallerOf(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("economics: no org on the call")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var mine []client.EconomicEvent
	for _, r := range e.rows {
		if (r.Buyer == org || r.Seller == org) && r.At >= in.From && r.At < in.To {
			if org != r.Buyer {
				r.Agent, r.Parent = "", "" // the seller's copy names no one inside the buyer
			}
			mine = append(mine, r)
		}
	}
	slices.SortStableFunc(mine, func(a, b client.EconomicEvent) int {
		return cmp.Or(cmp.Compare(a.At, b.At), strings.Compare(a.ID, b.ID))
	})
	start, _ := strconv.Atoi(in.Cursor)
	start = min(start, len(mine))
	end := min(start+2, len(mine))
	out := &client.Economics{Rows: mine[start:end]}
	if end < len(mine) {
		out.Next = strconv.Itoa(end)
	}
	for _, r := range out.Rows {
		if slices.ContainsFunc(e.rows, func(c client.EconomicEvent) bool {
			return c.Corrects == r.ID && c.Rail == r.Rail && c.Buyer == r.Buyer && c.Seller == r.Seller && c.At == r.At
		}) {
			out.Superseded = append(out.Superseded, r.ID)
		}
	}
	return out, nil
}
