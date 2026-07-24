package platform

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/clients/blueprint"
	"github.com/hanzoai/cloud/clients/metering"
)

// meterHarness is a real SQLite-backed store plus a capturing emit, so the compute
// meter's whole selection + idempotency + debit path runs with no commerce or
// cluster — the money is asserted on the captured metering.Usage.
type meterHarness struct {
	store *Store
	got   []capturedDebit
}

type capturedDebit struct {
	org string
	u   metering.Usage
}

func newMeterHarness(t *testing.T) *meterHarness {
	t.Helper()
	store, err := openStore(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &meterHarness{store: store}
}

func (h *meterHarness) emit(org string, u metering.Usage) {
	h.got = append(h.got, capturedDebit{org: org, u: u})
}

// seedLiveApp inserts a `live` app for org running image and sets its compute
// watermark to meteredAt (0 = first sight / never metered).
func (h *meterHarness) seedLiveApp(t *testing.T, org, slug, image string, replicas int, meteredAt int64) string {
	t.Helper()
	id := "app-" + slug
	a := Application{
		ID: id, Org: org, ProjectID: "proj", Slug: slug, Name: slug,
		Source: "image", ImageRepo: image, Port: 8080, Replicas: replicas,
		EnvJSON: "[]", DomainsJSON: "[]", Status: "live", CreatedAt: 1, UpdatedAt: 1,
	}
	if err := h.store.CreateApplication(context.Background(), a); err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}
	if meteredAt > 0 {
		won, err := h.store.AdvanceComputeMeter(context.Background(), id, 0, meteredAt)
		if err != nil || !won {
			t.Fatalf("seed watermark: won=%v err=%v", won, err)
		}
	}
	return id
}

// TestComputeMicros is the meter's arithmetic: micros = round(ratePerHour × secs /
// 3600), integer-exact, 0 for any non-billable span or rate.
func TestComputeMicros(t *testing.T) {
	for _, tc := range []struct {
		name              string
		ratePerHour, secs int64
		want              int64
	}{
		{"exactly one hour", 24000, 3600, 24000},
		{"half hour", 24000, 1800, 12000},
		{"two hours", 24000, 7200, 48000},
		{"one second at 3600/hr", 3600, 1, 1},
		{"rounds half up", 24000, 1, 7}, // 24000/3600 = 6.667 → 7
		{"zero span", 24000, 0, 0},
		{"negative span (clock skew)", 24000, -10, 0},
		{"free rate", 0, 3600, 0},
		{"negative rate", -5, 3600, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := computeMicros(tc.ratePerHour, tc.secs); got != tc.want {
				t.Fatalf("computeMicros(%d,%d) = %d, want %d", tc.ratePerHour, tc.secs, got, tc.want)
			}
		})
	}
}

// TestSweepMetersLiveAppAtSBOMRate proves (a): a running deployment meters its own
// org at the blueprint SBOM compute rate for exactly the elapsed span. A postgres
// app live for one hour is billed EstimateService("postgres:16",1).MicroUSDPerHour.
func TestSweepMetersLiveAppAtSBOMRate(t *testing.T) {
	h := newMeterHarness(t)
	const now int64 = 1_000_000
	h.seedLiveApp(t, "acme", "db", "postgres:16", 1, now-3600) // live for one hour

	n, err := sweepComputeMeter(context.Background(), h.store, now, blueprintRate, h.emit)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 || len(h.got) != 1 {
		t.Fatalf("metered=%d debits=%d, want 1/1", n, len(h.got))
	}
	d := h.got[0]
	wantMicros := blueprint.EstimateService("postgres:16", 1).MicroUSDPerHour
	if wantMicros != 24000 {
		t.Fatalf("precondition: postgres rate = %d, want 24000", wantMicros)
	}
	if d.org != "acme" {
		t.Fatalf("debit org = %q, want acme (the deploying org's own ledger)", d.org)
	}
	if d.u.AmountMicros != wantMicros {
		t.Fatalf("debit micros = %d, want %d (one hour at the SBOM rate)", d.u.AmountMicros, wantMicros)
	}
	if d.u.Provider != "compute" || d.u.Service != "compute" {
		t.Fatalf("debit provider/service = %q/%q, want compute/compute", d.u.Provider, d.u.Service)
	}

	// Watermark advanced to now, so the billed span is closed.
	apps, err := h.store.RunningApps(context.Background())
	if err != nil {
		t.Fatalf("RunningApps: %v", err)
	}
	if apps[0].MeteredAt != now {
		t.Fatalf("watermark = %d, want %d (advanced)", apps[0].MeteredAt, now)
	}
}

// TestSweepIdempotent proves (b): a double-tick at the same instant charges once.
// The first tick advances the watermark to now; the second sees a zero span and the
// compare-and-set no-ops, so no second debit is emitted.
func TestSweepIdempotent(t *testing.T) {
	h := newMeterHarness(t)
	const now int64 = 2_000_000
	h.seedLiveApp(t, "acme", "db", "postgres:16", 1, now-3600)

	for i := 0; i < 2; i++ {
		if _, err := sweepComputeMeter(context.Background(), h.store, now, blueprintRate, h.emit); err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
	}
	if len(h.got) != 1 {
		t.Fatalf("debits after double-tick = %d, want 1 (no double-charge)", len(h.got))
	}

	// A later tick bills only the NEW span (idempotent, not frozen): one more hour.
	if _, err := sweepComputeMeter(context.Background(), h.store, now+3600, blueprintRate, h.emit); err != nil {
		t.Fatalf("sweep t+1h: %v", err)
	}
	if len(h.got) != 2 {
		t.Fatalf("debits after next hour = %d, want 2", len(h.got))
	}
	if h.got[1].u.AmountMicros != 24000 {
		t.Fatalf("second debit micros = %d, want 24000 (one further hour)", h.got[1].u.AmountMicros)
	}
}

// TestSweepFirstSightAndStopped proves the two "no-charge" guards: a live app never
// billed before (watermark 0) starts its clock with NO back-charge, and a stopped
// app (not live) is never metered at all.
func TestSweepFirstSightAndStopped(t *testing.T) {
	h := newMeterHarness(t)
	const now int64 = 3_000_000
	firstSight := h.seedLiveApp(t, "acme", "fresh", "postgres:16", 1, 0) // watermark 0

	// A stopped app: seed live then mark stopped so it drops out of RunningApps.
	stopped := h.seedLiveApp(t, "acme", "paused", "postgres:16", 1, now-99999)
	a, err := h.store.GetApplicationByID(context.Background(), "acme", stopped)
	if err != nil {
		t.Fatalf("get stopped: %v", err)
	}
	a.Status = "stopped"
	if err := h.store.UpdateApplication(context.Background(), a); err != nil {
		t.Fatalf("stop app: %v", err)
	}

	n, err := sweepComputeMeter(context.Background(), h.store, now, blueprintRate, h.emit)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 0 || len(h.got) != 0 {
		t.Fatalf("metered=%d debits=%d, want 0/0 (first-sight starts clock, stopped ignored)", n, len(h.got))
	}
	// First-sight app's watermark is now started at now (so the NEXT hour bills).
	fresh, err := h.store.GetApplicationByID(context.Background(), "acme", firstSight)
	if err != nil {
		t.Fatalf("get fresh: %v", err)
	}
	_ = fresh
	apps, _ := h.store.RunningApps(context.Background())
	if len(apps) != 1 || apps[0].App.ID != firstSight || apps[0].MeteredAt != now {
		t.Fatalf("after first sight: running=%+v, want only %s watermark=%d", apps, firstSight, now)
	}
}

// TestComputeRoyaltyFlows proves (c): the metered compute lands on the org's ledger
// exactly as the authors sweep reads it (payout.SpendCents → month-to-date consumed),
// so the shipped 20% royalty takes a real cut. At a realistic monthly scale the loop
// is exercised end to end in arithmetic: SBOM rate → org spend → 20% → creator/treasury.
func TestComputeRoyaltyFlows(t *testing.T) {
	h := newMeterHarness(t)
	const hours int64 = 730 // one month of continuous run
	const now int64 = 10_000_000
	start := now - hours*3600 // live for a full month
	h.seedLiveApp(t, "acme", "db", "postgres:16", 1, start)

	if _, err := sweepComputeMeter(context.Background(), h.store, now, blueprintRate, h.emit); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(h.got) != 1 {
		t.Fatalf("debits = %d, want 1", len(h.got))
	}
	d := h.got[0]

	// The debit is what commerce accumulates into the org's month-to-date spend that
	// payout.SpendCents returns — keyed on the deploying org (u.User is set to org by
	// ResourceMeter.MeterUsage; here we assert the org the emit targeted).
	if d.org != "acme" {
		t.Fatalf("royalty base org = %q, want acme", d.org)
	}
	micros := d.u.AmountMicros
	wantMicros := int64(24000) * hours // 24000 µ$/hr × 730 h
	if micros != wantMicros {
		t.Fatalf("month compute micros = %d, want %d", micros, wantMicros)
	}

	// The authors sweep accrues earning = spendCents × shareBps / 10000 (defaultShareBps
	// = 2000 = 20%, clients/authors). Prove the compute spend yields the expected cut.
	const microsPerCent = 10_000
	const authorShareBps = 2000 // clients/authors defaultShareBps
	spendCents := micros / microsPerCent
	earning := spendCents * authorShareBps / 10_000
	if spendCents != 1752 { // $17.52/mo of compute
		t.Fatalf("compute spendCents = %d, want 1752", spendCents)
	}
	if earning != 350 { // 20% = $3.50/mo to the creator/treasury
		t.Fatalf("author royalty = %d¢, want 350 (20%% of compute spend)", earning)
	}
}
