package label

// mirror_test.go pins the SHAPE of the columnar copy. It cannot exercise the
// warehouse — there is none in this suite, and the honest consequence is stated
// in the package handoff — but every property below is a property of the
// statement text and the bindings, which is where the tenant boundary and the
// as-of rule actually live.

import (
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/tenant"
)

// TestTheMirrorSortKeyKeepsCorrections is the finding that changed the table.
//
// A ReplacingMergeTree collapses rows that agree on the WHOLE sort key. Sorted
// by (org, kind, subject, at, source) alone, a source that corrects itself
// DELETES its own earlier assertion at the next merge — and with it the answer
// every observation instant before the correction is supposed to see. `seen` and
// `id` in the key are what make a correction a second row instead of an
// overwrite, which is the same property the record plane gets from having no
// UPDATE statement.
func TestTheMirrorSortKeyKeepsCorrections(t *testing.T) {
	order := labelDDL[strings.Index(labelDDL, "ORDER BY"):]
	for _, term := range []string{"org", "kind", "subject", "at", "source", "seen", "id"} {
		if !strings.Contains(order, term) {
			t.Errorf("the sort key omits %q: %s", term, order)
		}
	}
	if !strings.HasPrefix(order, "ORDER BY (org,") {
		t.Errorf("org does not LEAD the sort key, so a tenant read is a filter and not a prefix scan: %s", order)
	}
}

// TestTheMirrorHasNoTableTTL pins the retention argument. A table TTL is a
// fleet-wide clock no tenant can hold a record past — which is the opposite of
// per-tenant retention, and a label can be the input to an adverse action.
func TestTheMirrorHasNoTableTTL(t *testing.T) {
	if strings.Contains(strings.ToUpper(labelDDL), "TTL") {
		t.Fatal("the record mirror carries a table TTL, so a tenant cannot hold a compliance record past it")
	}
}

// TestThePartitionKeyIsBoundedInCardinality.
//
// The partition key was `org`: one partition per tenant, on a shared single-pod
// engine, so directory count, part metadata and merge scheduling all grew with
// the tenant count. It was justified as making disposal a DROP PARTITION, which
// purge() deliberately does not do — a retention boundary disposes of a PREFIX of
// a tenant's history, so the sweep is a lightweight DELETE and the justification
// was never taken up.
//
// The month is what every other table in the fleet partitions by, and it is
// bounded by TIME rather than by how many customers we have. Isolation is
// unaffected and always was: it comes from `org` leading the sort key and being a
// bound predicate on every statement, both asserted here.
func TestThePartitionKeyIsBoundedInCardinality(t *testing.T) {
	if strings.Contains(labelDDL, "PARTITION BY org") {
		t.Fatal("the mirror partitions by tenant: the partition count then grows with the customer count on a shared warehouse")
	}
	if !strings.Contains(labelDDL, "PARTITION BY toYYYYMM(at)") {
		t.Fatalf("the mirror does not partition by month, as every other table in the fleet does: %s", labelDDL)
	}
	// The isolation the partition was wrongly credited with, where it actually
	// lives.
	order := labelDDL[strings.Index(labelDDL, "ORDER BY"):]
	if !strings.HasPrefix(order, "ORDER BY (org,") {
		t.Fatalf("org does not lead the sort key: %s", order)
	}
	if !strings.Contains(ResolvedSQL(), "WHERE org = ?") || !strings.Contains(
		mustDeletion(t), "WHERE org = ? AND id IN (") {
		t.Fatal("a statement reaches the shared table without a bound tenant predicate")
	}
}

func mustDeletion(t *testing.T) string {
	t.Helper()
	stmt, _ := deletion(tenantFor(t), []string{"aaa"})
	return stmt
}

// tenantFor mints the qualified key the columnar statements bind.
func tenantFor(t *testing.T) tenant.Key {
	t.Helper()
	tn, err := tenant.Mint("hanzo", "acme")
	if err != nil {
		t.Fatal(err)
	}
	return tn
}

// TestTheColumnarWriteBindsTheMintedTenantOnEveryRow. The tenant key is the only
// thing separating two tenants in a SHARED table, so it must lead every row and
// it must be BOUND — a tenant key interpolated into statement text is a tenant
// key some future value can escape out of.
func TestTheColumnarWriteBindsTheMintedTenantOnEveryRow(t *testing.T) {
	tn := tenantFor(t)
	now := time.Now().UTC()
	facts := []Fact{}
	for _, s := range []Source{Dispute, Review} {
		f, err := admit(Fact{Kind: KindTransaction, Subject: "tx'; DROP TABLE hanzo.risk_label; --",
			At: now.Add(-time.Hour), Seen: now, Disposition: Productive, Source: s,
			Evidence: "e", By: "hanzo/u", Confidence: 1}, now)
		if err != nil {
			t.Fatal(err)
		}
		facts = append(facts, f)
	}
	stmt, args := insert(tn, facts)
	if strings.Contains(stmt, "acme") || strings.Contains(stmt, "DROP TABLE") {
		t.Fatalf("a value was interpolated into the statement: %s", stmt)
	}
	const width = 12
	if len(args) != len(facts)*width {
		t.Fatalf("bound %d args for %d rows of %d columns", len(args), len(facts), width)
	}
	for i := range facts {
		if args[i*width] != tn.String() {
			t.Fatalf("row %d binds %v as its tenant, want %q", i, args[i*width], tn)
		}
	}
}

// TestTheColumnarDisposalIsBoundedByBothTenantAndID.
//
// `org` alone would let a disposal reach a neighbour if a key were ever
// mis-minted. `id` alone would let one tenant's boundary delete another tenant's
// row carrying the same content digest — and that is not hypothetical, because
// the digest is over the assertion's CONTENT and two tenants can assert
// identical facts about identically-named subjects. Both, always.
func TestTheColumnarDisposalIsBoundedByBothTenantAndID(t *testing.T) {
	tn := tenantFor(t)
	stmt, args := deletion(tn, []string{"aaa", "bbb"})
	if !strings.Contains(stmt, "WHERE org = ? AND id IN (?,?)") {
		t.Fatalf("the disposal is not bounded by tenant AND id: %s", stmt)
	}
	if args[0] != tn.String() {
		t.Fatalf("the disposal does not bind the tenant first: %v", args)
	}
	if len(args) != 3 {
		t.Fatalf("bound %d args, want the tenant plus 2 ids", len(args))
	}
	// Two tenants asserting the identical fact really do share a digest — which
	// is exactly why `org` cannot be dropped from the predicate.
	now := time.Now().UTC()
	base := Fact{Kind: KindTransaction, Subject: "tx-1", At: now.Add(-time.Hour), Seen: now,
		Disposition: Productive, Source: Dispute, Evidence: "dp-1", By: "svc", Confidence: 1}
	a, _ := admit(base, now)
	b, _ := admit(base, now)
	if a.ID != b.ID {
		t.Fatal("two identical assertions produced different digests; the premise of this test is wrong")
	}
}
