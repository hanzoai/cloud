package dataset

// plane_test.go asserts the SHAPE of every statement this plane can run, not
// only the answers it gives.
//
// An answer test proves the plane behaved correctly against one store. A shape
// test proves the property that makes it behave correctly against every store:
// the tenant leads the predicate, the tenant is a BOUND value, no caller-derived
// string is ever composed into the text, and neither owned table carries a TTL.
// Those survive a rewrite of the reader; an answer does not.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/tenant"
)

// exercise runs every statement the plane can produce, once, and returns them.
func exercise(t *testing.T) ([]call, *fake) {
	t.Helper()
	f := &fake{ttl: "TTL bucket + toIntervalDay(400)"}
	twin(f, "one")
	app := mountHTTP(t, newPlane(f))
	built := build(t, app, "one", declared("d"))
	if built.Status != statusReady {
		t.Fatalf("refused: %s", built.Refusal)
	}
	exported(t, app, "one", "d")
	lineageOf(t, app, "one", "d", built.Version)
	if code, body := do(t, app, http.MethodDelete, "/v1/ml/datasets/d", "one", nil); code != 200 {
		t.Fatalf("dispose: %d (%s)", code, body)
	}
	seen := f.seen()
	if len(f.unknown) > 0 {
		t.Fatalf("the fake could not parse: %v", f.unknown)
	}
	return seen, f
}

// catalogue is the ONE statement in this package that carries no tenant: it asks
// the store about the SOURCE TABLE's own retention rule, which is a property of
// the store and not of anyone's rows. It is exempted BY NAME and prints itself
// here, so the exemption is a decision on the record rather than a gap.
const catalogue = "SELECT engine_full FROM system.tables WHERE database = ? AND name = ?"

// TestTheTenantLeadsEveryPredicate is the isolation invariant as a property of
// the SQL, not of the result.
func TestTheTenantLeadsEveryPredicate(t *testing.T) {
	seen, _ := exercise(t)
	key := mustKey("one").String()
	var checked int
	for _, c := range seen {
		switch {
		case strings.HasPrefix(c.Stmt, "CREATE "):
			continue
		case c.Stmt == catalogue:
			continue
		}
		checked++

		// Every INSERT names org first and binds the key first.
		if strings.HasPrefix(c.Stmt, "INSERT INTO ") {
			open := c.Stmt[strings.Index(c.Stmt, "(")+1:]
			if !strings.HasPrefix(open, "org, ") {
				t.Errorf("an insert does not lead with org: %s", c.Stmt)
			}
			if len(c.Args) == 0 || c.Args[0] != key {
				t.Errorf("an insert does not bind the tenant first: %s", c.Stmt)
			}
			continue
		}

		// Every DROP PARTITION names the tenant as the first component.
		if strings.Contains(c.Stmt, "DROP PARTITION") {
			if !strings.HasSuffix(c.Stmt, "DROP PARTITION (?, ?)") {
				t.Errorf("a disposal does not bind its partition: %s", c.Stmt)
			}
			if len(c.Args) != 2 || c.Args[0] != key {
				t.Errorf("a disposal does not lead with the tenant: %s %v", c.Stmt, c.Args)
			}
			continue
		}

		// Every read opens its WHERE with a bound org.
		i := strings.Index(c.Stmt, " WHERE ")
		if i < 0 {
			t.Errorf("a read with no predicate at all: %s", c.Stmt)
			continue
		}
		if !strings.HasPrefix(c.Stmt[i+len(" WHERE "):], "org = ?") {
			t.Errorf("a read whose predicate does not OPEN with org: %s", c.Stmt)
		}
		if len(c.Args) == 0 || c.Args[0] != key {
			t.Errorf("a read that does not bind the tenant first: %s %v", c.Stmt, c.Args)
		}
	}
	if checked < 8 {
		t.Fatalf("only %d statements were checked; the exercise no longer covers the surface", checked)
	}
}

// TestNothingCallerDerivedIsEverComposedIntoAStatement. The tenant key, the
// dataset name and the seed all arrive from outside; none of them may appear in
// the TEXT of a statement, because a value in the text is a value the store
// parses as syntax.
func TestNothingCallerDerivedIsEverComposedIntoAStatement(t *testing.T) {
	f := &fake{}
	twin(f, "one")
	app := mountHTTP(t, newPlane(f))
	in := declared("named")
	in.Seed = "seed'--x"
	built := build(t, app, "one", in)
	if built.Status != statusReady {
		t.Fatalf("refused: %s", built.Refusal)
	}
	exported(t, app, "one", "named")

	key := mustKey("one").String()
	for _, c := range f.seen() {
		for _, secret := range []string{key, "named", in.Seed, kindPerson} {
			if strings.Contains(c.Stmt, secret) {
				t.Errorf("%q is composed into a statement rather than bound:\n  %s", secret, c.Stmt)
			}
		}
	}
}

// TestADatasetNameCannotCarrySyntax. The name is bound everywhere, so this is
// about the name being readable back — but it is also the door that keeps
// anything shaped like SQL out of the register in the first place.
func TestADatasetNameCannotCarrySyntax(t *testing.T) {
	app := mountHTTP(t, newPlane(&fake{}))
	for _, bad := range []string{
		"a'; DROP TABLE hanzo.risk_row; --",
		"has space",
		"has/slash",
		"-leading",
		"trailing-",
		"",
		strings.Repeat("a", 65),
	} {
		in := declared("placeholder")
		in.Name = bad
		code, body := do(t, app, http.MethodPost, "/v1/ml/datasets", "one", in)
		if code != http.StatusBadRequest {
			t.Errorf("name %q was admitted with %d (%s)", bad, code, body)
		}
	}
	// Case is NORMALISED, not refused: one dataset has one name, so `Orders` and
	// `orders` are the same dataset rather than two that differ by a shift key.
	in := declared("MixedCase")
	if code, body := do(t, app, http.MethodPost, "/v1/ml/datasets", "one", in); code != http.StatusOK {
		t.Fatalf("a mixed-case name was refused: %d (%s)", code, body)
	}
	if code, body := do(t, app, http.MethodGet, "/v1/ml/datasets/mixedcase", "one", nil); code != http.StatusOK {
		t.Fatalf("the normalised name does not address the dataset: %d (%s)", code, body)
	}
}

// TestNeitherOwnedTableCarriesATTL is the retention invariant, asserted where it
// is decided. A table TTL is a fleet-wide clock no tenant can hold longer or
// shorten, which is the opposite of a retention decision belonging to the tenant
// whose records they are.
func TestNeitherOwnedTableCarriesATTL(t *testing.T) {
	for name, ddl := range map[string]string{manifestTable: manifestDDL, rowTable: rowDDL} {
		if strings.Contains(strings.ToUpper(ddl), "TTL ") {
			t.Errorf("%s declares a TTL; only the tenant's own disposal may expire these rows", name)
		}
		if !strings.Contains(ddl, "PARTITION BY (org,") {
			t.Errorf("%s is not partitioned with the tenant first, so a disposal could reach across one", name)
		}
		if !strings.Contains(ddl, "ORDER BY (org,") {
			t.Errorf("%s is not sorted with the tenant first, so a per-tenant read is a full scan", name)
		}
	}
}

// TestThePlaneNeverCreatesTheSourceTable. The source has one writer and one DDL
// owner; a second CREATE here would be a second definition with nothing making
// the two agree.
func TestThePlaneNeverCreatesTheSourceTable(t *testing.T) {
	f := &fake{}
	p := newPlane(f)
	if err := p.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.seen() {
		if strings.HasPrefix(c.Stmt, "CREATE") && strings.Contains(c.Stmt, sourceTable) {
			t.Fatalf("this plane creates a table it does not own: %s", c.Stmt)
		}
	}
}

// TestAnUnkeyedCallReachesNoStatement. Every entry point takes a tenant.Key, and
// the zero key — the value of a Key that was never minted — refuses before any
// statement is built.
func TestAnUnkeyedCallReachesNoStatement(t *testing.T) {
	f := &fake{}
	p := newPlane(f)
	ctx := context.Background()
	// The zero Key: a Key that was never minted. It cannot be written as a literal
	// outside its own package, so this is how a test reaches one at all.
	var zero tenant.Key

	if _, err := p.versions(ctx, zero, "d"); err == nil {
		t.Error("versions ran without a tenant")
	}
	if _, err := p.names(ctx, zero); err == nil {
		t.Error("names ran without a tenant")
	}
	if _, err := p.rows(ctx, zero, "d", 1, 0, 10, ""); err == nil {
		t.Error("rows ran without a tenant")
	}
	if err := p.put(ctx, zero, entry{Name: "d"}); err == nil {
		t.Error("put ran without a tenant")
	}
	if err := p.dispose(ctx, zero, "d"); err == nil {
		t.Error("dispose ran without a tenant")
	}
	if _, err := p.census(ctx, zero, spec{From: time.Now(), To: time.Now()}, time.Now()); err == nil {
		t.Error("census ran without a tenant")
	}
	if _, _, err := p.facts(ctx, zero, spec{Rows: 1}, time.Now(), shareDenominator); err == nil {
		t.Error("facts ran without a tenant")
	}
	for _, c := range f.seen() {
		t.Errorf("an unkeyed call reached the store: %s", c.Stmt)
	}
}
