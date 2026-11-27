package sandbox

// ended_test.go — hanzo-inc/cloud#428: a reaped run ends with a clear reason, and
// the reason outlives the row. A paid lease its wallet stopped carrying, or one
// past SANDBOX_MAX_SPEND_CENTS, ends as `cost-ceiling`; and whoever held a sandbox
// the reaper ended is told why when they next ask for it, for endedKeep.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/account"
)

// A paid lease that reaches its own expiry was not carried, and the sweep carries
// every paid lease its wallet covers. So with the balance read and short it is the
// cost ceiling; with the balance unread it is still only a lease that ran out, and
// a free lease is never a cost.
//
// MUTATION: drop the `why == "lease-expired"` branch in clocks.ends and the broke
// lease reads `lease-expired`.
func TestALeaseItsWalletStoppedCarryingEndsAtTheCostCeiling(t *testing.T) {
	c := clocks{connected: time.Hour, disconnected: 15 * time.Minute, absolute: 24 * time.Hour}
	now := time.Now()
	ran := Sandbox{Payer: "acme", Status: statusRunning, CreatedAt: now.Add(-2 * time.Hour).Unix(),
		LastUsedAt: now.Unix(), ConnectedAt: now.Unix(), ExpiresAt: now.Add(-time.Second).Unix()}
	free := ran
	free.Payer = ""
	short := func(v bool) purse { return purse{short: func() bool { return v }} }
	// spent is a wallet that covers the hour and a ledger that answered v about the
	// org's budget (#806).
	spent := func(v bool) purse {
		return purse{short: func() bool { return false }, budget: func() bool { return v }}
	}

	for _, tc := range []struct {
		what string
		m    Sandbox
		p    purse
		why  string
		says string // what the detail names, for a cost ceiling
	}{
		{"a paid lease its wallet could not carry", ran, short(true), costCeiling, "balance"},
		{"a paid lease whose balance could not be read", ran, short(false), "lease-expired", ""},
		{"a free lease, which bills nobody", free, short(true), "lease-expired", ""},
		{"a paid lease its org's budget could not carry", ran, spent(true), costCeiling, "budget"},
		{"a paid lease whose ledger did not refuse", ran, spent(false), "lease-expired", ""},
		{"a free lease, past any budget", free, spent(true), "lease-expired", ""},
	} {
		done, why, detail := c.closes(tc.m, now, tc.p)
		if !done || why != tc.why {
			t.Errorf("%s: ends=%v %q, want true %q", tc.what, done, why, tc.why)
		}
		if why == costCeiling && !strings.Contains(detail, tc.says) {
			t.Errorf("%s: detail %q does not say it was the %s", tc.what, detail, tc.says)
		}
	}
}

// SANDBOX_MAX_SPEND_CENTS ends a paid lease once it has cost that much, however
// busy it is. A lease under the cap, a free lease, a parked one and no cap at all
// are each left alone.
//
// MUTATION: drop the maxSpend block in clocks.ends and the lease at two hours runs on.
func TestALeasePastItsSpendCapEndsAtTheCostCeiling(t *testing.T) {
	now := time.Now()
	hour := purse{hour: func() int64 { return 60 }} // 60 cents an hour
	busy := func(heldFor time.Duration) Sandbox {
		return Sandbox{Payer: "acme", Status: statusRunning, CreatedAt: now.Add(-heldFor).Unix(),
			LastUsedAt: now.Unix(), ConnectedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()}
	}
	capped := clocks{connected: time.Hour, disconnected: 15 * time.Minute, absolute: 24 * time.Hour, maxSpend: 100}

	if done, why, detail := capped.closes(busy(2*time.Hour), now, hour); !done || why != costCeiling {
		t.Fatalf("a lease that cost 120 cents against a 100-cent cap: ends=%v %q", done, why)
	} else if !strings.Contains(detail, "100") {
		t.Errorf("the detail %q does not name the cap", detail)
	}
	if done, why, _ := capped.closes(busy(time.Hour), now, hour); done {
		t.Errorf("a lease that cost 60 cents against a 100-cent cap was ended (%s)", why)
	}
	free := busy(2 * time.Hour)
	free.Payer = ""
	if done, why, _ := capped.closes(free, now, hour); done {
		t.Errorf("a free lease was ended at a spend cap (%s)", why)
	}
	still := busy(2 * time.Hour)
	still.Status = statusParked
	if done, why, _ := capped.closes(still, now, hour); done {
		t.Errorf("a parked lease, on its own hold, was ended at a spend cap (%s)", why)
	}
	uncapped := capped
	uncapped.maxSpend = 0
	if done, why, _ := uncapped.closes(busy(48*time.Hour), now, purse{hour: func() int64 { return 1 << 30 }}); done && why == costCeiling {
		t.Error("with no cap configured a lease was ended at one")
	}
}

// What a lease has cost is its fee and its hours, rounded up a cent.
func TestWhatALeaseHasSpent(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	m := Sandbox{Class: "exec", CreatedAt: now.Unix() - 90*60}
	if got, want := spent(m, 60, now), ResourceFee("exec")+90; got != want {
		t.Errorf("an hour and a half at 60 cents = %d, want %d", got, want)
	}
	m.CreatedAt = now.Unix() - 1
	if got, want := spent(m, 60, now), ResourceFee("exec")+1; got != want {
		t.Errorf("one second is %d, want a cent past the fee (%d)", got, want)
	}
}

// A tombstone is kept endedKeep and no longer: it reads back while it is kept, is
// gone to a reader past it, and the next burial drops it.
func TestATombstoneIsKeptAWeek(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		st, err := openStore(open(mustNS(t, "acme"), "sandbox"))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		ctx := context.Background()
		const at = int64(1_800_000_000)
		week := int64(endedKeep / time.Second)

		old := tombstone{ID: "m_old", Org: "acme", Actor: "alice", Reason: "idle-disconnected", EndedAt: at}
		if err := st.Bury(ctx, old); err != nil {
			t.Fatalf("bury: %v", err)
		}
		if got, err := st.Ended(ctx, "acme", "m_old", at+week-1); err != nil || got.Reason != "idle-disconnected" || got.Actor != "alice" {
			t.Fatalf("a day-old tombstone read back as %+v, %v", got, err)
		}
		if _, err := st.Ended(ctx, "acme", "m_old", at+week+1); err != errNotFound {
			t.Fatalf("a tombstone past its keep still reads back (%v)", err)
		}
		if _, err := st.Ended(ctx, "other", "m_old", at); err != errNotFound {
			t.Fatalf("another org read this org's tombstone (%v)", err)
		}

		if err := st.Bury(ctx, tombstone{ID: "m_new", Org: "acme", Reason: costCeiling, EndedAt: at + week + 1}); err != nil {
			t.Fatalf("bury: %v", err)
		}
		var n int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM sandbox_ended WHERE id='m_old'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("a burial left %d tombstones past their keep (%v)", n, err)
		}
	})
}

// sweptAs leases one paid exec sandbox for acme, runs before (when given), lets
// set reshape its row, sweeps once and answers the tombstone the sweep left. The
// row has to be gone.
func sweptAs(t *testing.T, before func(), set string, args ...any) (*Service, tombstone) {
	t.Helper()
	s, _, _ := parking(t)
	ctx := context.Background()
	m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "exec"})
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if m.Payer == "" {
		t.Fatalf("the lease is free (%+v); this needs one a wallet pays for", m)
	}
	if before != nil {
		before()
	}
	st, err := storeFor(s, "acme")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE sandbox SET `+set+` WHERE id='`+m.ID+`'`, args...); err != nil {
		t.Fatalf("reshape the row: %v", err)
	}

	sweep(ctx, s)

	if _, err := st.Get(ctx, "acme", m.ID); err != errNotFound {
		t.Fatalf("the sweep left the row standing (%v)", err)
	}
	e, err := st.Ended(ctx, "acme", m.ID, time.Now().Unix())
	if err != nil {
		t.Fatalf("the sweep ended %s and kept no reason: %v", m.ID, err)
	}
	return s, e
}

// The issue's Verify, through the real sweep: a paid run whose wallet cannot cover
// another hour is stopped with reason cost-ceiling, and a run left past the idle
// allowance with its own clock — and either reason is still there once the row is
// gone.
//
// MUTATION: drop the st.Bury in end and neither tombstone exists.
func TestTheSweepRecordsWhyItEndedARun(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Run("wallet", func(t *testing.T) {
			now := time.Now().Unix()
			broke := func() { balanceCents = balance(0) }
			_, e := sweptAs(t, broke, `expires_at=$1, last_used_at=$2, connected_at=$2`, now-5, now)
			if e.Reason != costCeiling {
				t.Fatalf("a paid run its wallet stopped carrying ended as %q, want %q", e.Reason, costCeiling)
			}
		})
		t.Run("cap", func(t *testing.T) {
			t.Setenv("SANDBOX_MAX_SPEND_CENTS", "1")
			now := time.Now().Unix()
			_, e := sweptAs(t, nil, `created_at=$1, last_used_at=$2, connected_at=$2`, now-3*3600, now)
			if e.Reason != costCeiling || !strings.Contains(e.Detail, "cap") {
				t.Fatalf("a run past its spend cap ended as %q (%q), want %q naming the cap", e.Reason, e.Detail, costCeiling)
			}
		})
		t.Run("idle", func(t *testing.T) {
			now := time.Now().Unix()
			_, e := sweptAs(t, nil, `last_used_at=$1, connected_at=0`, now-2*3600)
			if e.Reason != "idle-disconnected" {
				t.Fatalf("an abandoned run ended as %q, want idle-disconnected", e.Reason)
			}
		})
	})
}

// A park the reaper took carries its reason into the tombstone, not the `parked`
// hold that later retired it; a failed start keeps what the cluster said.
func TestATombstoneNamesWhatEndedTheRunNotTheLastClock(t *testing.T) {
	why, detail := cause(Sandbox{Status: statusParked, Error: costCeiling}, "parked", "")
	if why != costCeiling {
		t.Errorf("a run the reaper parked at its cost ceiling is buried as %q (%q)", why, detail)
	}
	if why, _ := cause(Sandbox{Status: statusParked}, "parked", ""); why != "parked" {
		t.Errorf("a pause somebody asked for, retired after its hold, is buried as %q", why)
	}
	if why, detail := cause(Sandbox{Status: statusError, Error: "ImagePullBackOff"}, "start-failed", ""); why != "start-failed" || detail != "ImagePullBackOff" {
		t.Errorf("a failed start is buried as %q (%q)", why, detail)
	}
}

// Whoever held a sandbox the reaper ended is told why when they next reach for it
// — still a 404, the answer every caller reads as gone, now with the reason in
// the message and as `reason`. A member who did not hold it is answered as for an
// id that never existed.
//
// MUTATION: drop the buried call in find and the lessee is told only "not found".
func TestALostSandboxSaysWhyItWasEnded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := owning(t)
		m := leaseAs(t, app, alice)
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatalf("store: %v", err)
		}
		row, err := st.Get(context.Background(), "acme", m.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		end(context.Background(), s, st, row, "idle-disconnected", "")

		for _, who := range []member{alice, carol} {
			code, body := as(t, app, who, http.MethodPost, "/v1/sandbox/run", `{"id":"`+m.ID+`","argv":["true"]}`)
			if code != http.StatusNotFound || !strings.Contains(string(body), "idle-disconnected") ||
				!strings.Contains(string(body), `"reason"`) {
				t.Errorf("%s ran a command in a sandbox the reaper ended: %d %s, want 404 saying idle-disconnected", who.user, code, body)
			}
		}
		code, body := as(t, app, bob, http.MethodGet, "/v1/sandbox/"+m.ID, "")
		if code != http.StatusNotFound || strings.Contains(string(body), "idle") {
			t.Errorf("a member who never held it was told why it ended: %d %s", code, body)
		}
	})
}
