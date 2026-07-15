package samples

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/principal"
)

// ── (a) cross-tenant isolation ──────────────────────────────────────────────
//
// org is the ONLY tenant key. These prove the three properties that make it one:
// it is ALWAYS present, it is always BOUND (never interpolated), and no other
// input can widen a read past it.

// Every read the package can build carries `org = ?` as its FIRST predicate, with
// the caller's own org as the first bind. There is no code path to a statement
// without it.
func TestEveryReadIsOrgScopedFirstAndBound(t *testing.T) {
	series, args, err := buildSeries(Query{Org: "acme", Unit: "u1", Source: SourceAgent, Range: "7d"})
	if err != nil {
		t.Fatalf("buildSeries: %v", err)
	}
	latest, largs, err := buildLatest("acme")
	if err != nil {
		t.Fatalf("buildLatest: %v", err)
	}
	for name, q := range map[string]struct {
		sql  string
		args []any
	}{"series": {series, args}, "latest": {latest, largs}} {
		if !strings.Contains(q.sql, "WHERE org = ?") {
			t.Fatalf("%s: org must lead the WHERE as a bound param: %s", name, q.sql)
		}
		if len(q.args) == 0 || q.args[0] != "acme" {
			t.Fatalf("%s: first bind must be the caller's org, got %v", name, q.args)
		}
		if strings.Contains(q.sql, "'acme'") {
			t.Fatalf("%s: org must never be interpolated: %s", name, q.sql)
		}
	}
}

// The heart of it: org B's read is built for org B, no matter what org A's
// identifiers it knows. Asking for another tenant's unit does not widen the scan —
// it narrows it to (B AND A's unit), which is by construction empty.
func TestCrossTenantReadCannotReachAnotherOrg(t *testing.T) {
	// org A's data.
	_, aArgs, err := buildSeries(Query{Org: "org-a", Unit: "tgt-secret"})
	if err != nil {
		t.Fatalf("buildSeries A: %v", err)
	}
	if aArgs[0] != "org-a" {
		t.Fatalf("A's read must bind org-a, got %v", aArgs[0])
	}

	// org B naming A's unit: still bound to org-b, so A's rows are unreachable.
	sql, bArgs, err := buildSeries(Query{Org: "org-b", Unit: "tgt-secret"})
	if err != nil {
		t.Fatalf("buildSeries B: %v", err)
	}
	if bArgs[0] != "org-b" {
		t.Fatalf("B's read must bind org-b, got %v", bArgs[0])
	}
	for _, a := range bArgs {
		if s, ok := a.(string); ok && s == "org-a" {
			t.Fatal("org-a must never appear in a read built for org-b")
		}
	}
	if strings.Contains(sql, "org-a") {
		t.Fatal("org-a must never appear in B's statement")
	}
	// The unit narrows WITHIN the tenant — it never replaces the tenant predicate.
	if strings.Index(sql, "org = ?") > strings.Index(sql, "unit = ?") {
		t.Fatalf("the org predicate must precede the unit narrower: %s", sql)
	}

	// Latest is per-tenant too — B's board can only ever be built from B's rows.
	_, lArgs, err := buildLatest("org-b")
	if err != nil {
		t.Fatalf("buildLatest B: %v", err)
	}
	if lArgs[0] != "org-b" {
		t.Fatalf("B's board must bind org-b, got %v", lArgs[0])
	}
}

// A missing tenant fails CLOSED — a read is never built "for everyone".
func TestBlankOrgFailsClosed(t *testing.T) {
	for _, o := range []string{"", "   ", "\t", strings.Repeat("o", principal.MaxOrgLen+1)} {
		if _, _, err := buildSeries(Query{Org: o}); err != errOrg {
			t.Fatalf("buildSeries(%q) must fail closed, got %v", o, err)
		}
		if _, _, err := buildLatest(o); err != errOrg {
			t.Fatalf("buildLatest(%q) must fail closed, got %v", o, err)
		}
	}
}

// ── injection ───────────────────────────────────────────────────────────────

// Nothing a caller supplies is ever rendered into a statement. A classic payload
// in every user-facing field must appear ONLY as a bind — the statement itself
// stays byte-identical to the benign one.
func TestCallerValuesAreBoundNeverBuilt(t *testing.T) {
	const evil = "x' OR 1=1 --"
	hostile, hargs, err := buildSeries(Query{Org: evil, Unit: evil, Range: evil})
	if err != nil {
		t.Fatalf("buildSeries: %v", err)
	}
	benign, _, err := buildSeries(Query{Org: "acme", Unit: "u1", Range: "24h"})
	if err != nil {
		t.Fatalf("buildSeries: %v", err)
	}
	if hostile != benign {
		t.Fatalf("a hostile value changed the STATEMENT:\n hostile=%s\n benign =%s", hostile, benign)
	}
	if strings.Contains(hostile, "OR 1=1") || strings.Contains(hostile, "--") {
		t.Fatalf("payload reached the statement: %s", hostile)
	}
	var found bool
	for _, a := range hargs {
		if a == evil {
			found = true
		}
	}
	if !found {
		t.Fatal("the payload must survive as a BOUND value (proving it was bound, not built)")
	}
}

// `source` is a CLOSED allowlist: an unknown value is rejected outright rather
// than reaching the statement in any form.
func TestSourceIsAllowlisted(t *testing.T) {
	for _, s := range []string{SourceAgent, SourceBYO, SourceCloud, SourceVisor, "AGENT", " byo "} {
		if _, _, err := buildSeries(Query{Org: "acme", Source: s}); err != nil {
			t.Fatalf("source %q must be accepted, got %v", s, err)
		}
	}
	for _, s := range []string{"wat", "agent'--", "agent OR 1=1", "'"} {
		if _, _, err := buildSeries(Query{Org: "acme", Source: s}); err != errSource {
			t.Fatalf("source %q must fail closed, got %v", s, err)
		}
	}
}

// `range` is a KEY into a closed map, never text. An unknown label can only ever
// resolve to the default window — it can never reach the statement.
func TestRangeIsAllowlistedAndDefaults(t *testing.T) {
	now := time.Now().UTC()
	for label, d := range ranges {
		got := now.Sub(since(label))
		if got < d-time.Minute || got > d+time.Minute {
			t.Fatalf("range %q want ~%v, got %v", label, d, got)
		}
	}
	for _, bad := range []string{"", "wat", "1h; DROP TABLE hanzo.compute_samples", "999d"} {
		got := now.Sub(since(bad))
		want := ranges[DefaultRange]
		if got < want-time.Minute || got > want+time.Minute {
			t.Fatalf("unknown range %q must fall back to %s, got %v", bad, DefaultRange, got)
		}
	}
	// The window is a time.Time BIND, never rendered text.
	_, args, err := buildSeries(Query{Org: "acme", Range: "1h; DROP TABLE hanzo.compute_samples"})
	if err != nil {
		t.Fatalf("buildSeries: %v", err)
	}
	if _, ok := args[1].(time.Time); !ok {
		t.Fatalf("the range bind must be a time.Time, got %T", args[1])
	}
}

// Every read is bounded twice — by a time predicate and by a LIMIT — so no query
// can walk the whole table.
func TestReadsAreBounded(t *testing.T) {
	sql, args, err := buildSeries(Query{Org: "acme"})
	if err != nil {
		t.Fatalf("buildSeries: %v", err)
	}
	if !strings.Contains(sql, "ts >= ?") || !strings.Contains(sql, "LIMIT ") {
		t.Fatalf("a series read must be time- and LIMIT-bounded: %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("want (org, since) binds, got %v", args)
	}
	lsql, _, err := buildLatest("acme")
	if err != nil {
		t.Fatalf("buildLatest: %v", err)
	}
	if !strings.Contains(lsql, "LIMIT 1 BY unit") || !strings.Contains(lsql, "ts >= ?") {
		t.Fatalf("a board read must be per-unit and time-bounded: %s", lsql)
	}
}

// An over-long unit is rejected on the read path too — the same rule as the write,
// so a key can never be silently truncated onto another unit's series.
func TestOversizedUnitFailsClosedOnRead(t *testing.T) {
	if _, _, err := buildSeries(Query{Org: "acme", Unit: strings.Repeat("u", maxUnit+1)}); err != errUnit {
		t.Fatalf("an over-long unit must fail closed, got %v", err)
	}
}

// ── caller error vs infrastructure error ────────────────────────────────────

// Every rejection this package can produce is a CALLER error, so an HTTP face can
// answer 400 with a safe message. Conflating the two is how a warehouse outage
// gets blamed on a tenant — or worse, how its text leaks to one.
func TestEveryRejectionIsMarkedInvalid(t *testing.T) {
	for _, err := range []error{errOrg, errUnit, errSource, errKind} {
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("%v must be an ErrInvalid so a face can answer 400", err)
		}
	}
	// The reads' rejections carry the marker through.
	if _, _, err := buildSeries(Query{Org: ""}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a blank org must be marked invalid, got %v", err)
	}
	if _, _, err := buildSeries(Query{Org: "acme", Source: "evil"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown source must be marked invalid, got %v", err)
	}
	// A caller-safe message names the closed vocabulary and nothing internal.
	if !strings.Contains(errSource.Error(), SourceAgent) {
		t.Fatalf("the source error must tell the caller the vocabulary: %v", errSource)
	}
	for _, err := range []error{errOrg, errUnit, errSource, errKind} {
		if strings.Contains(err.Error(), table) {
			t.Fatalf("a caller-facing error must not name our tables: %v", err)
		}
	}
}

// ── row decode ──────────────────────────────────────────────────────────────

// The driver hands each column back in its own native type; the decode must accept
// those natives and round-trip the value plane.
func TestSampleFromNativeDriverTypes(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	s := sampleFrom(map[string]any{
		"org": "acme", "source": SourceAgent, "unit": "tgt-1", "host": "box",
		"kind": KindGPU, "ts": at,
		"cpus": uint16(8), "memory": uint64(32 << 30), "mem_used": uint64(8 << 30),
		"mem_free": uint64(24 << 30), "load1": float32(1.5), "load5": float32(1.25),
		"load15": float32(0.5), "gpu_util": float32(0.5), "gpus": uint8(2),
		"gpu_model": "GB10", "cost_cents": uint64(42),
	})
	if s.Org != "acme" || s.Unit != "tgt-1" || s.Kind != KindGPU || !s.At.Equal(at) {
		t.Fatalf("identity did not round-trip: %+v", s)
	}
	if s.CPUs != 8 || s.GPUs != 2 || s.Memory != 32<<30 || s.CostCents != 42 {
		t.Fatalf("numbers did not round-trip: %+v", s)
	}
	if s.Load1 != 1.5 || s.GPUUtil != 0.5 {
		t.Fatalf("floats did not round-trip: %+v", s)
	}
}

// A missing or unexpectedly-typed column degrades to a zero value rather than
// panicking a read — a driver change can never crash the board.
func TestSampleFromToleratesJunk(t *testing.T) {
	s := sampleFrom(map[string]any{"org": 42, "cpus": "eight", "ts": "nope"})
	if s.Org != "" || s.CPUs != 0 || !s.At.IsZero() {
		t.Fatalf("junk must degrade to zero values, got %+v", s)
	}
	if s2 := sampleFrom(map[string]any{}); s2.Unit != "" {
		t.Fatalf("an empty row must decode to the zero Sample, got %+v", s2)
	}
}
