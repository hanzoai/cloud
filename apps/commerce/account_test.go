// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
)

// arrearsAt is a reader over switches a test sets, on a clock it moves.
func arrearsAt(t *testing.T, sw *switches, fail *bool, reads *int, at *time.Time) *arrears {
	t.Helper()
	return &arrears{
		pods: coord.Alone(), // its own, so what one test claims is not another's
		read: func(context.Context) (switches, error) {
			*reads++
			if *fail {
				return switches{}, errors.New("the flag plane is down")
			}
			return *sw, nil
		},
		spawn: func(f func()) { f() },
		told:  map[string]string{},
		now:   func() time.Time { return *at },
	}
}

// The switch names the orgs on account, any case. The first ask reads it; after
// that a stale answer serves the call that notices it, and the read that call starts
// serves the next, once per TTL. A flag plane that is down keeps the last answer: it
// neither stops an org on account nor puts one on.
func TestTheOnAccountSwitchIsReadOncePerTTLAndOutlivesAnOutage(t *testing.T) {
	ctx := context.Background()
	sw := switches{orgs: names(" Lux, zoo ,,hanzo")}
	fail, reads, at := true, 0, time.Unix(1_800_000_000, 0)
	a := arrearsAt(t, &sw, &fail, &reads, &at)

	if a.on(ctx, "lux") {
		t.Fatal("an org was on account before the switch was ever read")
	}
	fail = false
	if a.on(ctx, "lux") || reads != 1 {
		t.Fatalf("a failed read was tried again within the TTL: %d reads", reads)
	}
	at = at.Add(switchTTL)
	if a.on(ctx, "lux") || reads != 2 {
		t.Fatalf("the stale answer did not serve the call that noticed it: %d reads", reads)
	}
	if !a.on(ctx, "LUX") || !a.on(ctx, "zoo") || a.on(ctx, "acme") || a.on(ctx, "") || reads != 2 {
		t.Fatalf("after the switch answered: lux %v zoo %v acme %v, %d reads", a.on(ctx, "LUX"), a.on(ctx, "zoo"), a.on(ctx, "acme"), reads)
	}
	sw = switches{orgs: names("zoo")}
	fail = true
	at = at.Add(switchTTL)
	if !a.on(ctx, "lux") || !a.on(ctx, "lux") || reads != 3 {
		t.Fatalf("an outage took lux off account: %d reads", reads)
	}
	fail = false
	at = at.Add(switchTTL)
	a.on(ctx, "lux")
	if a.on(ctx, "lux") || !a.on(ctx, "zoo") || reads != 4 {
		t.Fatalf("taking lux off the switch did not take it off account: %d reads", reads)
	}
}

// An org on account that owes the alert or more is put before the SuperAdmins once a
// UTC day: one audit row, whatever number of debits follow. Owing less, off account,
// or on the sandbox books, nobody is told.
func TestAnOrgOnAccountPastItsAlertIsAuditedOnceADay(t *testing.T) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	finance.SetAccount(func(_ context.Context, org string) bool { return org == "lux" })
	t.Cleanup(func() { finance.SetAccount(nil) })
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	ctx := context.Background()

	sw := switches{orgs: names("lux"), alert: 10_000}
	fail, reads, at := false, 0, time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	a := arrearsAt(t, &sw, &fail, &reads, &at)
	a.trail = trail
	rows := func() int {
		t.Helper()
		got, _, err := trail.Query(ctx, audit.Filter{Action: "billing.account.owing"})
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	spend := func(org string, cents int64, ref string) {
		t.Helper()
		if err := fin.RecordUsage(ctx, money.UsageInput{Org: org, Subject: org, Amount: money.FromCents(cents), Ref: ref}); err != nil {
			t.Fatal(err)
		}
	}

	spend("lux", 9_999, "u1")
	a.owing("lux", false)
	if n := rows(); n != 0 {
		t.Fatalf("owing $99.99 against a $100 alert: %d rows, want none", n)
	}
	spend("lux", 1, "u2")
	a.owing("lux", false)
	a.owing("lux", false)
	a.owing("lux", true)
	if n := rows(); n != 1 {
		t.Fatalf("owing $100 against a $100 alert, told twice in a day: %d rows, want 1", n)
	}
	got, _, err := trail.Query(ctx, audit.Filter{Action: "billing.account.owing"})
	if err != nil || got[0].Resource.ID != "lux" || got[0].Actor.Org != "admin" {
		t.Fatalf("the row: %+v (%v)", got, err)
	}
	at = at.Add(24 * time.Hour)
	a.owing("lux", false)
	if n := rows(); n != 2 {
		t.Fatalf("the next day: %d rows, want 2", n)
	}

	spend("acme", 50_000, "a1")
	a.owing("acme", false)
	sw.alert = 0
	at = at.Add(24 * time.Hour)
	a.switches(ctx) // the stale answer starts the read that brings the alert to 0
	a.owing("lux", false)
	if n := rows(); n != 2 {
		t.Fatalf("off account, or with the alert at 0: %d rows, want still 2", n)
	}
}

// The line op reads an org on account as one: its line left is open, what it owes,
// how old that is, and what its own account used this month.
func TestThePlaneLineSaysAnOrgIsOnAccount(t *testing.T) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	finance.SetAccount(func(_ context.Context, org string) bool { return org == "lux" })
	t.Cleanup(func() { finance.SetAccount(nil) })
	ctx := context.Background()
	if err := fin.RecordUsage(ctx, money.UsageInput{Org: "lux", Subject: "lux", Amount: money.FromCents(4200), Ref: "u1"}); err != nil {
		t.Fatal(err)
	}
	got, err := (lineOps{}).line(cloud.For(ctx, "lux"), nil)
	if err != nil {
		t.Fatal(err)
	}
	owed, _ := got.Owed.FloorMinor()
	used, _ := got.Used.FloorMinor()
	if !got.Account || owed != 4200 || used != 4200 || got.Since == 0 || got.Period == 0 {
		t.Fatalf("line = %+v; want on account, $42 owed and used, its age and period", got)
	}
}

// The alert is once a UTC day per org for the DEPLOYMENT: three replicas see the
// org's debits in turn, and the SuperAdmins get one row and one pulse, not one from
// each replica. Each replica's own record of the day it has told is only what it
// already knows is done.
//
// It runs on the in-process KV; the KV driver's own tests (internal/driver/kv) hold
// the same counts to a real server.
//
// MUTATION: claim the day in the arrears' own map alone (the told check and set it
// had) and every replica tells.
func TestAnOwingAlertIsToldOncePerDayAcrossReplicas(t *testing.T) {
	t.Run("in-process KV", func(t *testing.T) { alertOnce(t, coordtest.Memory(t)) })
}

func alertOnce(t *testing.T, pod func(id string) *coord.Coord) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	finance.SetAccount(func(_ context.Context, org string) bool { return org == "lux" })
	t.Cleanup(func() { finance.SetAccount(nil) })
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	ctx := context.Background()
	if err := fin.RecordUsage(ctx, money.UsageInput{Org: "lux", Subject: "lux", Amount: money.FromCents(20_000), Ref: "u1"}); err != nil {
		t.Fatal(err)
	}

	sw := switches{orgs: names("lux"), alert: 10_000}
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	var replicas []*arrears
	for _, id := range []string{"cloud-0", "cloud-1", "cloud-2"} {
		fail, reads := false, 0
		a := arrearsAt(t, &sw, &fail, &reads, &at)
		a.pods, a.trail = pod(id), trail
		replicas = append(replicas, a)
	}
	rows := func() int {
		t.Helper()
		got, _, err := trail.Query(ctx, audit.Filter{Action: "billing.account.owing"})
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	for _, a := range replicas {
		a.owing("lux", false)
		a.owing("lux", false)
	}
	if n := rows(); n != 1 {
		t.Fatalf("three replicas saw an org owe past its alert: %d rows, want 1", n)
	}
	at = at.Add(24 * time.Hour)
	for _, a := range replicas {
		a.owing("lux", false)
	}
	if n := rows(); n != 2 {
		t.Fatalf("the next day: %d rows, want 2", n)
	}
}
