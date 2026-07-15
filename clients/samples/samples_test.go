package samples

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/principal"
)

// good is a well-formed sample — the baseline each hostile case mutates from.
func good() Sample {
	return Sample{
		Org: "acme", Source: SourceAgent, Unit: "tgt-1", Host: "box.local",
		Kind: KindGPU, At: time.Unix(1700000000, 0).UTC(),
		CPUs: 8, Memory: 32 << 30, MemUsed: 8 << 30, MemFree: 24 << 30,
		Load1: 1.5, Load5: 1.2, Load15: 0.9, GPUUtil: 0.42,
		GPUs: 1, GPUModel: "GB10", CostCents: 0,
	}
}

// ── (b) sanitize bounds ─────────────────────────────────────────────────────

// A hostile or buggy source must never poison a column. Every non-finite float
// collapses to 0, every negative to 0, and every over-range number to its column's
// ceiling — the row is always well-formed no matter what was handed in.
func TestSanitizeBoundsHostileValues(t *testing.T) {
	s := Sample{
		Org: " acme ", Source: " AGENT ", Unit: " tgt-1 ", Kind: " GPU ",
		Host:    strings.Repeat("h", 400),
		CPUs:    1 << 20, // far past the UInt16 column
		Memory:  -5,
		MemUsed: -1, MemFree: -99,
		Load1: math.NaN(), Load5: math.Inf(1), Load15: -3,
		GPUUtil:   7.5, // a ratio > 1
		GPUs:      9999,
		GPUModel:  strings.Repeat("m", 200),
		CostCents: -100,
	}.sanitize()

	if s.Org != "acme" || s.Unit != "tgt-1" {
		t.Fatalf("keys must be trimmed: org=%q unit=%q", s.Org, s.Unit)
	}
	if s.Source != SourceAgent || s.Kind != KindGPU {
		t.Fatalf("vocab must be lower-cased: source=%q kind=%q", s.Source, s.Kind)
	}
	if len(s.Host) != maxHost {
		t.Fatalf("host must clamp to %d, got %d", maxHost, len(s.Host))
	}
	if len(s.GPUModel) != maxGPUModel {
		t.Fatalf("gpu model must clamp to %d, got %d", maxGPUModel, len(s.GPUModel))
	}
	if s.CPUs != maxCPUs {
		t.Fatalf("cpus must clamp to the UInt16 ceiling %d, got %d", maxCPUs, s.CPUs)
	}
	if s.GPUs != maxGPUs {
		t.Fatalf("gpus must clamp to the UInt8 ceiling %d, got %d", maxGPUs, s.GPUs)
	}
	if s.Memory != 0 || s.MemUsed != 0 || s.MemFree != 0 || s.CostCents != 0 {
		t.Fatalf("negatives must floor at 0: %+v", s)
	}
	if s.Load1 != 0 || s.Load5 != 0 || s.Load15 != 0 {
		t.Fatalf("NaN/Inf/negative loads must collapse to 0: %v %v %v", s.Load1, s.Load5, s.Load15)
	}
	if s.GPUUtil != 1 {
		t.Fatalf("gpu_util must clamp into [0,1], got %v", s.GPUUtil)
	}
}

// A huge-but-finite load must clamp to maxLoad rather than survive into the
// Float32 column as +Inf. This is the narrowing the args() binds depend on.
func TestSanitizeClampsLoadBelowFloat32Ceiling(t *testing.T) {
	s := good()
	s.Load1 = 1e300
	s = s.sanitize()
	if s.Load1 != maxLoad {
		t.Fatalf("load1 want clamp to %v, got %v", maxLoad, s.Load1)
	}
	if math.IsInf(float64(float32(s.Load1)), 0) {
		t.Fatal("clamped load must not narrow to +Inf in the Float32 column")
	}
}

// A zero At means "now" — the server owns the clock, so no row is ever written
// with a zero timestamp.
func TestSanitizeStampsZeroTime(t *testing.T) {
	s := good()
	s.At = time.Time{}
	if got := s.sanitize().At; got.IsZero() {
		t.Fatal("a zero At must be stamped with the server clock")
	}
}

// The binds are lossless for any sanitized sample: every narrowing (int→uint16,
// int→uint8, float64→float32) is inside the column's range by construction.
func TestArgsNarrowingIsLosslessForSanitizedSamples(t *testing.T) {
	s := Sample{
		Org: "acme", Source: SourceAgent, Unit: "u", Kind: KindGPU,
		CPUs: 1 << 30, GPUs: 1 << 20, Load1: 1e300, GPUUtil: 99,
		Memory: math.MaxInt64, CostCents: math.MaxInt64,
	}.sanitize()
	args := s.args()
	if len(args) != strings.Count(insertStmt, "?") {
		t.Fatalf("args=%d but insert has %d placeholders", len(args), strings.Count(insertStmt, "?"))
	}
	if got := args[6].(uint16); uint64(got) != uint64(s.CPUs) {
		t.Fatalf("cpus bind wrapped: %d != %d", got, s.CPUs)
	}
	if got := args[14].(uint8); uint64(got) != uint64(s.GPUs) {
		t.Fatalf("gpus bind wrapped: %d != %d", got, s.GPUs)
	}
	for i, a := range args {
		if f, ok := a.(float32); ok {
			if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
				t.Fatalf("arg %d narrowed to a non-finite float32: %v", i, f)
			}
		}
	}
}

// The column list, the INSERT placeholders and the bind count are ONE shape. This
// is the drift that silently corrupts a warehouse write, so it is asserted.
func TestInsertShapeMatchesColumnList(t *testing.T) {
	ncols := len(strings.Split(cols, ","))
	nph := strings.Count(insertStmt, "?")
	nargs := len(good().sanitize().args())
	if ncols != nph || nph != nargs {
		t.Fatalf("shape drift: %d columns, %d placeholders, %d args", ncols, nph, nargs)
	}
}

// ── validate: fail CLOSED on anything untenanted or misattributed ────────────

func TestValidateFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Sample)
		want error
	}{
		{"blank org", func(s *Sample) { s.Org = "" }, errOrg},
		{"whitespace org", func(s *Sample) { s.Org = "   " }, errOrg},
		{"oversized org", func(s *Sample) { s.Org = strings.Repeat("o", principal.MaxOrgLen+1) }, errOrg},
		{"blank unit", func(s *Sample) { s.Unit = "" }, errUnit},
		{"oversized unit", func(s *Sample) { s.Unit = strings.Repeat("u", maxUnit+1) }, errUnit},
		{"unknown source", func(s *Sample) { s.Source = "wat" }, errSource},
		{"blank source", func(s *Sample) { s.Source = "" }, errSource},
		{"unknown kind", func(s *Sample) { s.Kind = "toaster" }, errKind},
		{"blank kind", func(s *Sample) { s.Kind = "" }, errKind},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := good()
			tc.mut(&s)
			if err := s.sanitize().validate(); err != tc.want {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
	if err := good().sanitize().validate(); err != nil {
		t.Fatalf("a well-formed sample must validate, got %v", err)
	}
}

// An over-long org must be REJECTED, never truncated: a clamped org would write
// the row under a DIFFERENT tenant (a 129-char org silently becoming its own
// 128-char prefix could collide with a real one). Same for unit, which would
// merge two units' series. This is the sharpest tenancy edge in the package.
func TestOversizedKeysAreRejectedNotTruncated(t *testing.T) {
	victim := strings.Repeat("a", principal.MaxOrgLen) // a real 128-char org
	attacker := victim + "-extra"                      // wants to land in victim's series
	s := good()
	s.Org = attacker
	san := s.sanitize()
	if san.Org == victim {
		t.Fatal("sanitize truncated an over-long org onto another tenant's key")
	}
	if err := san.validate(); err != errOrg {
		t.Fatalf("an over-long org must fail closed, got %v", err)
	}

	u := good()
	u.Unit = strings.Repeat("u", maxUnit) + "-extra"
	if err := u.sanitize().validate(); err != errUnit {
		t.Fatalf("an over-long unit must fail closed, got %v", err)
	}
}

// ── (c) fail-soft: no datastore ⇒ Record is a no-op returning nil ────────────

// The whole plane is optional infrastructure. With no datastore configured every
// emitter must sail straight through — including, deliberately, one carrying a
// sample that would otherwise be rejected: nothing is written either way, so the
// caller is never punished for an absent warehouse.
func TestRecordIsNoOpWithoutDatastore(t *testing.T) {
	if err := Record(context.Background(), good()); err != nil {
		t.Fatalf("Record must no-op to nil without a datastore, got %v", err)
	}
	if err := Record(context.Background(), Sample{}); err != nil {
		t.Fatalf("even an empty sample must not fail without a datastore, got %v", err)
	}
}

// A cancelled context must not turn into a caller-visible failure either.
func TestRecordIgnoresCancelledContextWithoutDatastore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Record(ctx, good()); err != nil {
		t.Fatalf("Record must no-op to nil, got %v", err)
	}
}

// The reads are honest-empty (never fabricated) when the warehouse is absent, and
// still fail closed on a blank tenant BEFORE consulting it.
func TestReadsAreHonestEmptyWithoutDatastore(t *testing.T) {
	got, err := Series(context.Background(), Query{Org: "acme"})
	if err != nil || len(got) != 0 {
		t.Fatalf("Series want empty/nil, got %v / %v", got, err)
	}
	m, err := Latest(context.Background(), "acme")
	if err != nil || len(m) != 0 {
		t.Fatalf("Latest want empty/nil, got %v / %v", m, err)
	}
	if _, err := Series(context.Background(), Query{Org: ""}); err != errOrg {
		t.Fatalf("blank org must fail closed even with no datastore, got %v", err)
	}
	if _, err := Latest(context.Background(), ""); err != errOrg {
		t.Fatalf("blank org must fail closed even with no datastore, got %v", err)
	}
}
