package graph

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/claim"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// orgAdmin is what the identity boundary mints for an admin of the org the
// request acts in. It is the one authority an erasure admits.
var orgAdmin = map[string]string{"X-User-IsOrgAdmin": "true"}

func idsOf(facts []Fact) []string {
	out := make([]string, 0, len(facts))
	for _, f := range facts {
		out = append(out, f.ID)
	}
	slices.Sort(out)
	return out
}

func noWitness([]Fact) error { return nil }

// TestEraseRemovesWhatNamesTheEntity pins the predicate: the entity as subject,
// and as the entity an edge points at — and nothing else. A property whose
// scalar spells the key is not a reference to it.
func TestEraseRemovesWhatNamesTheEntity(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		about := mk(t, "person:ada", "email", "ada@example.test", false, "crm", t0)
		out := mk(t, "person:ada", "works", "org:acme", true, "crm", t0)
		in := mk(t, "team:core", "member", "person:ada", true, "hr", t0)
		spelled := mk(t, "note:1", "mentions", "person:ada", false, "wiki", t0)
		other := mk(t, "team:core", "member", "person:bob", true, "hr", t0)
		if _, err := s.record(ctx, []Fact{about, out, in, spelled, other}); err != nil {
			t.Fatalf("record: %v", err)
		}

		var seen []Fact
		gone, held, remaining, err := s.erase(ctx, "person:ada", eraseMax, func(f []Fact) error {
			seen = f
			return nil
		})
		if err != nil || held != 0 || remaining != 0 {
			t.Fatalf("erase: held=%d remaining=%d err=%v", held, remaining, err)
		}
		if want := idsOf([]Fact{about, out, in}); !slices.Equal(idsOf(gone), want) {
			t.Fatalf("erased %v, want the subject's rows and the edge into it %v", idsOf(gone), want)
		}
		if !slices.Equal(idsOf(seen), idsOf(gone)) {
			t.Fatalf("the witness saw %v and the store removed %v", idsOf(seen), idsOf(gone))
		}

		left, err := s.read(ctx, filter{})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if want := idsOf([]Fact{spelled, other}); !slices.Equal(idsOf(left), want) {
			t.Fatalf("left %v, want %v", idsOf(left), want)
		}
		// The index lost them with the table: a search is a read, and an erased
		// subject must not be findable by its words either.
		found, err := s.read(ctx, filter{Match: terms("ada")})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if !slices.Equal(idsOf(found), []string{spelled.ID}) {
			t.Fatalf("a search for the erased subject found %v, want only the scalar that spells it", idsOf(found))
		}
	})
}

// TestEraseUnderHoldRemovesNothing: all or nothing. Removing the unheld rows
// and keeping the held one would report a subject erased that the held row
// still names.
func TestEraseUnderHoldRemovesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		about := mk(t, "person:ada", "email", "ada@example.test", false, "crm", t0)
		in := mk(t, "team:core", "member", "person:ada", true, "hr", t0)
		if _, err := s.record(ctx, []Fact{about, in}); err != nil {
			t.Fatalf("record: %v", err)
		}
		// No op places a hold on this plane yet, so the test places it where the
		// hold lives.
		if _, err := s.db.Exec(`UPDATE assertion SET hold = 1 WHERE id = $1`, in.ID); err != nil {
			t.Fatalf("hold: %v", err)
		}

		witnessed := false
		gone, held, _, err := s.erase(ctx, "person:ada", eraseMax, func([]Fact) error {
			witnessed = true
			return nil
		})
		if err != nil || held != 1 || len(gone) != 0 {
			t.Fatalf("erase under hold: gone=%d held=%d err=%v, want nothing gone and one held", len(gone), held, err)
		}
		if witnessed {
			t.Fatal("an erasure that removed nothing was recorded as made")
		}
		left, err := s.read(ctx, filter{})
		if err != nil || len(left) != 2 {
			t.Fatalf("after a refused erasure the store holds %d rows, want 2 (err=%v)", len(left), err)
		}
	})
}

// TestAnUnrecordedErasureIsNoErasure: the witness runs before the commit, so
// a trail that cannot take the record rolls the delete back.
func TestAnUnrecordedErasureIsNoErasure(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		if _, err := s.record(ctx, []Fact{mk(t, "person:ada", "email", "ada@example.test", false, "crm", t0)}); err != nil {
			t.Fatalf("record: %v", err)
		}
		down := errors.New("trail down")
		if _, _, _, err := s.erase(ctx, "person:ada", eraseMax, func([]Fact) error { return down }); !errors.Is(err, down) {
			t.Fatalf("erase with a failing witness: err=%v, want the witness's error", err)
		}
		left, err := s.read(ctx, filter{Entity: "person:ada"})
		if err != nil || len(left) != 1 {
			t.Fatalf("an unrecorded erasure removed rows: %d left, err=%v", len(left), err)
		}
	})
}

// TestEraseIsBoundedAndSaysWhatRemains: one call removes at most max rows and
// says how many still name the entity, so the next call finishes it.
func TestEraseIsBoundedAndSaysWhatRemains(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		var facts []Fact
		for _, v := range []string{"a", "b", "c"} {
			facts = append(facts, mk(t, "person:ada", "alias", v, false, "crm", t0))
		}
		if _, err := s.record(ctx, facts); err != nil {
			t.Fatalf("record: %v", err)
		}
		gone, _, remaining, err := s.erase(ctx, "person:ada", 2, noWitness)
		if err != nil || len(gone) != 2 || remaining != 1 {
			t.Fatalf("first call: gone=%d remaining=%d err=%v, want 2 and 1", len(gone), remaining, err)
		}
		gone, _, remaining, err = s.erase(ctx, "person:ada", 2, noWitness)
		if err != nil || len(gone) != 1 || remaining != 0 {
			t.Fatalf("second call: gone=%d remaining=%d err=%v, want 1 and 0", len(gone), remaining, err)
		}
	})
}

// TestErasedContentLeavesTheFile: a row no query returns but the file still
// holds is not erased. It reads the database's own bytes, before and after, in
// both journal modes — WAL is the one an org file runs in, and there the
// pre-erasure frames outlive the delete until a checkpoint.
func TestErasedContentLeavesTheFile(t *testing.T) {
	for _, mode := range []string{"delete", "wal"} {
		t.Run(mode, func(t *testing.T) { erasedFromTheFile(t, mode) })
	}
}

func erasedFromTheFile(t *testing.T, mode string) {
	path := filepath.Join(t.TempDir(), "graph.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1) // as locus.Single holds every org file
	if _, err := db.Exec(`PRAGMA journal_mode = ` + mode); err != nil {
		t.Fatalf("journal mode: %v", err)
	}
	s, err := openStore(db)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	const mark = "vorheesquintessa"
	if _, err := s.record(ctx, []Fact{
		mk(t, "person:"+mark, "email", mark+"@example.test", false, "crm", t0),
		mk(t, "team:core", "member", "person:"+mark, true, "hr", t0),
		mk(t, "team:core", "member", "person:bob", true, "hr", t0),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	bytesOf := func() string {
		t.Helper()
		var all []byte
		for _, suffix := range []string{"", "-wal", "-journal"} {
			b, err := os.ReadFile(path + suffix)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("read %s: %v", path+suffix, err)
			}
			all = append(all, b...)
		}
		return string(all)
	}
	// Without this the test would pass on a file that never held the text.
	if !strings.Contains(bytesOf(), mark) {
		t.Fatal("the file does not hold the subject before erasure, so this test measures nothing")
	}
	if _, _, _, err := s.erase(ctx, "person:"+mark, eraseMax, noWitness); err != nil {
		t.Fatalf("erase: %v", err)
	}
	if n := strings.Count(bytesOf(), mark); n != 0 {
		t.Fatalf("the file still holds the erased subject %d times", n)
	}
}

// ── over the wire ────────────────────────────────────────────────────────────

// audited mounts the plane with an audit trail and returns both. An erasure
// and a declaration are each refused on a deployment without one.
func audited(t *testing.T) (*zip.App, *audit.Recorder) {
	t.Helper()
	rec, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	return mount(t, cloud.Deps{Audit: rec}), rec
}

func erasure(t *testing.T, app *zip.App, authority map[string]string, project string, in graphEraseIn) (int, graphEraseOut) {
	t.Helper()
	code, b := callAs(t, app, authority, http.MethodPost, "/v1/graph/erase", project, in)
	var out graphEraseOut
	if code == http.StatusOK {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("the receipt is not the receipt shape: %v: %s", err, b)
		}
	}
	return code, out
}

func trail(t *testing.T, rec *audit.Recorder) []audit.Record {
	t.Helper()
	return trailOf(t, rec, "graph.erase")
}

// trailOf is what the trail holds of one action.
func trailOf(t *testing.T, rec *audit.Recorder, action string) []audit.Record {
	t.Helper()
	rows, _, err := rec.Query(context.Background(), audit.Filter{Action: action})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	return rows
}

// TestTheReceiptVerifiesAgainstTheTrail is the whole contract of one erasure:
// the subject's rows are gone and nothing else is, the receipt's digest is its
// ids', the trail holds that digest with who, when, why and how many — and not
// one byte of what was erased.
func TestTheReceiptVerifiesAgainstTheTrail(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, rec := audited(t)
		const subject = "person:quintessa-vorhees"
		assertFact(t, app, "", subject, "email", "q.vorhees@example.test", false)
		assertFact(t, app, "", "team:core", "member", subject, true)
		assertFact(t, app, "", "team:core", "member", "person:bob", true)

		code, r := erasure(t, app, orgAdmin, "", graphEraseIn{Entity: subject, Reason: "DSR-2026-0042, GDPR art. 17"})
		if code != http.StatusOK {
			t.Fatalf("erase: status %d", code)
		}
		if r.Entity != subject || len(r.IDs) != 2 || r.Remaining != 0 {
			t.Fatalf("receipt: %+v", r)
		}
		if !slices.IsSorted(r.IDs) || claim.Digest(r.IDs...) != r.Digest {
			t.Fatalf("the digest %s is not the digest of the ids %v", r.Digest, r.IDs)
		}
		if len(r.Stores) == 0 || r.Stores[0] != (graphStore{Store: "graph", Status: erased, Removed: 2}) {
			t.Fatalf("stores = %+v, want the graph first with its two rows", r.Stores)
		}
		if !slices.Equal(r.Sources, []string{"test"}) {
			t.Fatalf("sources = %v, want the one document the rows were filed from", r.Sources)
		}
		// Every store this op cannot reach is on the receipt, and so the receipt
		// does not call itself complete.
		var names []string
		for _, g := range r.Stores[1:] {
			if g.Status != unsupported || g.Detail == "" || g.Removed != 0 {
				t.Errorf("%+v: a store this op does not reach must say so and say what is missing", g)
			}
			names = append(names, g.Store)
		}
		if !slices.Equal(names, []string{"knowledge", "ai.memory"}) {
			t.Fatalf("unreached = %v: a receipt must name every store it does not reach", names)
		}
		if r.Complete {
			t.Fatal("the receipt calls itself complete while two stores may still hold the subject")
		}
		if _, err := time.Parse(time.RFC3339, r.At); err != nil || r.By == "" {
			t.Fatalf("at=%q by=%q", r.At, r.By)
		}

		if left := answered(t, app, "", "/v1/graph?entity="+subject); len(left) != 0 {
			t.Fatalf("the subject still has %d assertions", len(left))
		}
		if left := answered(t, app, "", "/v1/graph?value="+subject); len(left) != 0 {
			t.Fatalf("%d edges still point at the subject", len(left))
		}
		if left := answered(t, app, "", "/v1/graph?entity=team:core"); !slices.Equal(valuesOf(left), []string{"person:bob"}) {
			t.Fatalf("the unrelated edge went with it: %v", valuesOf(left))
		}

		rows := trail(t, rec)
		if len(rows) != 1 {
			t.Fatalf("the trail holds %d erasure records, want 1", len(rows))
		}
		got := rows[0]
		if got.Resource.ID != r.Digest || got.Actor.Org != "acme" || got.Actor.Sub == "" {
			t.Fatalf("trail record: %+v", got)
		}
		if !got.Time.Equal(mustTime(t, r.At)) {
			t.Fatalf("the trail says %v and the receipt says %s", got.Time, r.At)
		}
		var after struct {
			Reason  string `json:"reason"`
			Removed int    `json:"removed"`
			Digest  string `json:"digest"`
		}
		if err := json.Unmarshal(got.After, &after); err != nil {
			t.Fatalf("after: %v", err)
		}
		if after.Reason != "DSR-2026-0042, GDPR art. 17" || after.Removed != 2 || after.Digest != r.Digest {
			t.Fatalf("after = %+v", after)
		}
		whole, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		for _, erasedText := range []string{"vorhees", "quintessa", "example.test"} {
			if strings.Contains(string(whole), erasedText) {
				t.Fatalf("the trail holds %q, which is what was erased: %s", erasedText, whole)
			}
		}
		for _, id := range r.IDs {
			if strings.Contains(string(whole), id) {
				t.Fatalf("the trail holds the erased id %s; it keeps the digest in its place", id)
			}
		}
	})
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("time %q: %v", s, err)
	}
	return v
}

// TestTheDigestIsTheSetsNotTheOrders: one set of assertions filed in two orders
// in two graphs is erased under one digest, and an erasure that found nothing
// still answers a receipt that verifies.
func TestTheDigestIsTheSetsNotTheOrders(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := audited(t)
		file := [][3]string{
			{"person:ada", "email", "ada@example.test"},
			{"person:ada", "phone", "+1 555 0100"},
			{"person:ada", "city", "London"},
		}
		for i, f := range file {
			assertFact(t, app, "one", f[0], f[1], f[2], false)
			g := file[len(file)-1-i]
			assertFact(t, app, "two", g[0], g[1], g[2], false)
		}
		in := graphEraseIn{Entity: "person:ada", Reason: "DSR-7"}
		_, one := erasure(t, app, orgAdmin, "one", in)
		_, two := erasure(t, app, orgAdmin, "two", in)
		if len(one.IDs) != 3 || one.Digest != two.Digest || !slices.Equal(one.IDs, two.IDs) {
			t.Fatalf("one set, two digests: %s %v / %s %v", one.Digest, one.IDs, two.Digest, two.IDs)
		}

		code, again := erasure(t, app, orgAdmin, "one", in)
		if code != http.StatusOK || again.IDs == nil || len(again.IDs) != 0 || again.Digest != claim.Digest() {
			t.Fatalf("erasing what is already gone: status %d, %+v", code, again)
		}
		if again.Stores[0] != (graphStore{Store: "graph", Status: absent}) {
			t.Fatalf("a graph that held nothing reads %+v", again.Stores[0])
		}
	})
}

// TestEraseUnderHoldIsRefusedWhole: 409, nothing removed, nothing recorded.
func TestEraseUnderHoldIsRefusedWhole(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, rec := audited(t)
		assertFact(t, app, "", "person:ada", "email", "ada@example.test", false)
		assertFact(t, app, "", "team:core", "member", "person:ada", true)
		ns, err := principal.OrgNamespace("acme", "")
		if err != nil {
			t.Fatalf("namespace: %v", err)
		}
		st, err := mounted.stores.For(ns)
		if err != nil {
			t.Fatalf("store: %v", err)
		}
		if _, err := st.db.Exec(`UPDATE assertion SET hold = 1 WHERE entity = 'team:core'`); err != nil {
			t.Fatalf("hold: %v", err)
		}

		code, b := callAs(t, app, orgAdmin, http.MethodPost, "/v1/graph/erase", "", graphEraseIn{Entity: "person:ada", Reason: "DSR-9"})
		if code != http.StatusConflict {
			t.Fatalf("erase under hold: status %d: %s", code, b)
		}
		if left := answered(t, app, "", "/v1/graph"); len(left) != 2 {
			t.Fatalf("a refused erasure left %d of 2 rows", len(left))
		}
		if rows := trail(t, rec); len(rows) != 0 {
			t.Fatalf("a refused erasure was recorded as made: %+v", rows)
		}
	})
}

// TestEraseIsForTheOrgsOwnAdmins: a member is refused, and so is a SuperAdmin
// acting inside a tenant that has not made them one of its admins — erasure is
// never cross-tenant. Neither refusal removes anything.
func TestEraseIsForTheOrgsOwnAdmins(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, rec := audited(t)
		assertFact(t, app, "", "person:ada", "email", "ada@example.test", false)
		in := graphEraseIn{Entity: "person:ada", Reason: "DSR-3"}

		for name, authority := range map[string]map[string]string{
			"member":               nil,
			"superadmin elsewhere": {"X-User-IsAdmin": "true", "X-User-Owner": "admin"},
		} {
			if code, _ := erasure(t, app, authority, "", in); code != http.StatusForbidden {
				t.Fatalf("%s: status %d, want 403", name, code)
			}
		}
		if left := answered(t, app, "", "/v1/graph"); len(left) != 1 || len(trail(t, rec)) != 0 {
			t.Fatalf("a refused caller changed something: %d rows, %d records", len(left), len(trail(t, rec)))
		}
		if code, r := erasure(t, app, orgAdmin, "", in); code != http.StatusOK || len(r.IDs) != 1 {
			t.Fatalf("org admin: status %d, %+v", code, r)
		}
	})
}

// TestEraseWithoutATrailIsRefused: a deployment that cannot record an erasure
// does not make one, and neither does one whose trail fails at the write.
func TestEraseWithoutATrailIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		in := graphEraseIn{Entity: "person:ada", Reason: "DSR-4"}

		bare := mountGraph(t)
		assertFact(t, bare, "", "person:ada", "email", "ada@example.test", false)
		if code, _ := erasure(t, bare, orgAdmin, "", in); code != http.StatusServiceUnavailable {
			t.Fatalf("no trail: status %d, want 503", code)
		}
		if left := answered(t, bare, "", "/v1/graph"); len(left) != 1 {
			t.Fatalf("no trail, and %d of 1 rows left", len(left))
		}
		_ = Shutdown()

		app, rec := audited(t)
		assertFact(t, app, "", "person:ada", "email", "ada@example.test", false)
		_ = rec.Close()
		if code, _ := erasure(t, app, orgAdmin, "", in); code != http.StatusServiceUnavailable {
			t.Fatalf("a trail that fails: status %d, want 503", code)
		}
		if left := answered(t, app, "", "/v1/graph"); len(left) != 1 {
			t.Fatalf("a failed record, and %d of 1 rows left", len(left))
		}
	})
}

// TestAReasonMayNotNameTheSubject: the trail keeps the reason after the subject
// is gone, so a reason carrying the entity would be the tombstone this refuses
// to keep.
func TestAReasonMayNotNameTheSubject(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, rec := audited(t)
		assertFact(t, app, "", "person:ada", "email", "ada@example.test", false)
		for _, in := range []graphEraseIn{
			{Entity: "person:ada", Reason: "Erase PERSON:ADA per request"},
			{Entity: "person:ada", Reason: " "},
			{Entity: "", Reason: "DSR-5"},
		} {
			if code, _ := erasure(t, app, orgAdmin, "", in); code != http.StatusBadRequest {
				t.Fatalf("%+v: status %d, want 400", in, code)
			}
		}
		if left := answered(t, app, "", "/v1/graph"); len(left) != 1 || len(trail(t, rec)) != 0 {
			t.Fatalf("a refused erasure changed something: %d rows, %d records", len(left), len(trail(t, rec)))
		}
	})
}
