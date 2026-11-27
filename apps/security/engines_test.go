package security

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// engines_test.go holds what the port to hanzo-sql changed that no behaviour test
// above reaches by itself.

// A scan whose findings hold text an engine cannot keep is refused whole, before
// anything is written: a reader never sees its header without its findings.
func TestAScanWithUnstorableTextIsRefusedWhole(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, err := openStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()
		sc := Scan{ID: "scan_1", Org: "acme", Findings: 2, CreatedAt: 1}
		fs := []StoredFinding{
			{ID: "f1", RuleID: "r", RuleName: "R", Severity: "high", Path: "a.go", Line: 1, CreatedAt: 1},
			{ID: "f2", RuleID: "r", RuleName: "R", Severity: "high", Path: "b\x00.go", Line: 2, CreatedAt: 1},
		}
		if err := s.SaveScan(ctx, sc, fs); !errors.Is(err, dialect.ErrText) {
			t.Fatalf("a path holding a NUL = %v, want ErrText", err)
		}
		if _, err := s.GetScan(ctx, "acme", "scan_1"); !errors.Is(err, errNotFound) {
			t.Fatalf("a refused scan left its header behind: %v", err)
		}
		fs[1].Path = "b.go"
		if err := s.SaveScan(ctx, sc, fs); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListFindings(ctx, "acme", "scan_1", "high", 10)
		if err != nil || len(got) != 2 {
			t.Fatalf("%d findings after the scan was saved, %v; want 2", len(got), err)
		}
	})
}
