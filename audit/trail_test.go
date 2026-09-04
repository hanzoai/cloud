package audit

// Tests for the FAMILY: the naming rule, the enumeration that finds every chain, and
// the three verdicts a chain can carry.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/namespace"
	sqlitedrv "github.com/hanzoai/sqlite"
)

// cekOpen opens a store the way every store in this tree is opened.
func cekOpen(dir, name string) (*sql.DB, error) {
	return cek.Open(namespace.System(), name, dir)
}

// TestNameIsPerProcess pins the one-writer rule for the audit chain.
//
// audit_log.seq is a gapless chain position and every row's prev_hash seals the one
// before it, so the chain only means anything if a single process appends to it. When
// cloud was one binary that was automatic. The moment subsystems became plugin CHILD
// PROCESSES sharing a DataDir, every one of them opened the same audit.db, recovered
// its own in-memory nextSeq, and raced for the same PRIMARY KEY — v1.801.313 produced
// "UNIQUE constraint failed: audit_log.seq" ~94 times a minute and, because the audit
// gate fails closed, refused every POST in the fleet.
//
// Two writers cannot share a hash chain; they can only fork it. Each process therefore
// gets its own file. If someone collapses these back onto one name, this fails before
// the fleet does.
func TestNameIsPerProcess(t *testing.T) {
	for _, tc := range []struct {
		proc string
		want string
	}{
		{"cloud", "audit"}, // host keeps the canonical name (and its history)
		{"", "audit"},      // unknown proc must not invent a second host chain
		{"tasks", "audit-tasks"},
		{"integrations", "audit-integrations"},
		{"compute", "audit-compute"},
	} {
		t.Run(tc.proc, func(t *testing.T) {
			if got := Name(tc.proc); got != tc.want {
				t.Fatalf("Name(%q) = %q, want %q", tc.proc, got, tc.want)
			}
		})
	}

	// The property that actually matters: distinct processes never collide.
	seen := map[string]string{}
	for _, p := range []string{"cloud", "tasks", "integrations", "compute", "commerce", "iam"} {
		n := Name(p)
		if prev, dup := seen[n]; dup {
			t.Fatalf("processes %q and %q share audit chain %q — two writers on one hash chain", prev, p, n)
		}
		seen[n] = p
	}
}

// TestMemberIsTheInverseOfName pins enumeration against naming.
//
// These are the two halves of one fact and they are the halves that can silently
// disagree: a member() too narrow drops a live chain from the trail (the reader
// reports a clean family that is missing the broken one), a member() too wide walks a
// store that is not a chain and reports it unread forever. Neither shows up as a
// failure anywhere else, so it is asserted here over the real generator.
func TestMemberIsTheInverseOfName(t *testing.T) {
	for _, proc := range []string{"", "cloud", "tasks", "iam", "admin", "compute", "o11y", "commerce"} {
		if n := Name(proc); !member(n) {
			t.Errorf("Name(%q) = %q, which member() does not recognise — that chain would be invisible to the trail", proc, n)
		}
	}
	// Stores that are NOT chains. "auditlog" is the real trap: it is a package name
	// in this tree and it starts with the prefix, so a bare prefix match takes it.
	for _, notAChain := range []string{"auditlog", "audits", "auditor", "kms", "pricing", "provisioning", ""} {
		if member(notAChain) {
			t.Errorf("member(%q) is true, but Name() can never produce it — the trail would walk a store that is not a chain", notAChain)
		}
	}
}

// TestChainsFindsEveryChainAndNothingElse is the enumeration test, and it is the one
// the shipped defect was invisible to: NOTHING anywhere enumerated the audit-*.db
// family. Every reader opened exactly the chain its own process wrote, so a
// deployment with 128 live chains had 127 of them read by no code at all.
//
// It also pins the LOCATION, which two separate readings got wrong: cek puts a chain
// at {dir}/orgs/_platform/<name>.db, and the data root itself holds an abandoned
// pre-cek generation. A reader that lists {dir} finds frozen files and concludes the
// trail died; a reader that lists the wrong directory finds nothing and reports a
// clean, empty trail.
func TestChainsFindsEveryChainAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"audit", "audit-iam", "audit-tasks", "audit-visor"} {
		mustClosedChain(t, dir, name, 1)
	}
	// A store that is not a chain, in the same directory, and a decoy at the
	// ABANDONED pre-cek location, which must not be mistaken for a live chain.
	mustClosedStore(t, dir, "auditlog")
	mustClosedStore(t, dir, "pricing")
	writeDecoy(t, filepath.Join(dir, "audit.db"))
	writeDecoy(t, filepath.Join(dir, "audit-legacy.db"))

	got, err := chains(dir)
	if err != nil {
		t.Fatalf("chains: %v", err)
	}
	want := []string{"audit", "audit-iam", "audit-tasks", "audit-visor"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("chains = %v, want %v (in name order)", got, want)
	}
}

// TestChainsRefusesAnEmptyDir proves the reader cannot be pointed at nothing and
// answer quietly. Zero chains is not a clean trail: this process writes one, so an
// empty answer means the layout moved, and reporting {intact:0, broken:0} for it is a
// fabricated pass — the exact class of lie this whole change is about.
func TestChainsRefusesAnEmptyDir(t *testing.T) {
	rec, _ := openTemp(t)
	empty := t.TempDir()
	rec.dir = empty // point it where no chain lives
	if _, err := rec.Trail(context.Background()); err == nil {
		t.Fatal("Trail over a directory holding no chain returned no error — an empty family reads as a clean one")
	}
}

// TestTrailReportsEveryChainSeparately proves the answer is a SET: each chain named,
// each carrying its own verdict, and the counts summing to the family.
func TestTrailReportsEveryChainSeparately(t *testing.T) {
	rec, dir := openTemp(t)
	ctx := context.Background()
	for i := range 4 {
		if _, err := rec.Append(ctx, sampleRecord("DELETE /v1/admin/orgs")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	mustClosedChain(t, dir, "audit-iam", 3)
	mustClosedChain(t, dir, "audit-tasks", 5)

	tr, err := rec.Trail(ctx)
	if err != nil {
		t.Fatalf("Trail: %v", err)
	}
	if len(tr.Chains) != 3 {
		t.Fatalf("Trail reported %d chains, want 3 — the family is not being enumerated", len(tr.Chains))
	}
	for i, want := range []string{"audit", "audit-iam", "audit-tasks"} {
		if tr.Chains[i].Name != want {
			t.Fatalf("chain %d = %q, want %q", i, tr.Chains[i].Name, want)
		}
		if tr.Chains[i].Verdict == "" {
			t.Fatalf("chain %q carries no verdict", want)
		}
	}
	if tr.Intact+tr.Broken+tr.Unread != len(tr.Chains) {
		t.Fatalf("counts %d/%d/%d do not sum to %d chains", tr.Intact, tr.Broken, tr.Unread, len(tr.Chains))
	}

	// This process's OWN chain is always readable — it is walked through the handle
	// already held, on every build.
	own, ok := chainOf(tr, "audit")
	if !ok || own.Verdict != Intact || own.Count != 4 {
		t.Fatalf("own chain = %+v, want intact with 4 records", own)
	}

	// The SIBLINGS' verdicts are a property of the build, and asserting the right one
	// is what stops this test from passing for the wrong reason: without this, a run
	// that silently fell back to the envelope would report both siblings unread and
	// still satisfy every assertion above.
	for name, records := range map[string]uint64{"audit-iam": 3, "audit-tasks": 5} {
		sib, ok := chainOf(tr, name)
		if !ok {
			t.Fatalf("sibling %q absent from the trail", name)
		}
		if !sqlitedrv.CodecLinked() {
			if sib.Verdict != Unread {
				t.Errorf("%s = %q on a build with no codec, want %q — it cannot be opened safely here", name, sib.Verdict, Unread)
			}
			continue
		}
		if sib.Verdict != Intact || sib.Count != records {
			t.Errorf("%s = %+v, want intact with %d records", name, sib, records)
		}
	}
}

// TestUnreadableChainIsNeverAPass is the safety property, stated as a test.
//
// A chain that could not be read must come back Unread with a reason — never Intact,
// and never absent. Absent is the worse of the two: a caller counting three intact
// chains out of three cannot tell that a fourth was skipped, which is how one
// unreadable chain disappears into a green summary.
func TestUnreadableChainIsNeverAPass(t *testing.T) {
	rec, dir := openTemp(t)
	// A file in the family's directory, with a chain's NAME, that is not a chain.
	// Nothing can decrypt it, so nothing can say anything about its contents.
	writeDecoy(t, chainPath(t, dir, "audit-corrupt"))

	tr, err := rec.Trail(context.Background())
	if err != nil {
		t.Fatalf("Trail: %v", err)
	}
	iv, ok := chainOf(tr, "audit-corrupt")
	if !ok {
		t.Fatal("an unreadable chain vanished from the trail — a caller cannot tell it was skipped")
	}
	if iv.Verdict != Unread {
		t.Fatalf("unreadable chain verdict = %q, want %q — an unreadable chain must never read as a passing one", iv.Verdict, Unread)
	}
	if iv.Reason == "" {
		t.Error("unread chain carries no reason; an operator has nothing to act on")
	}
	if tr.Unread != 1 {
		t.Errorf("unread count = %d, want 1", tr.Unread)
	}
}

// TestSiblingChainsAreReadOnlyOrNotReadAtAll pins the constraint that shaped this
// design, so a later change cannot quietly undo it.
//
// Without the C codec, cek falls back to the pure-Go envelope: it decrypts a file
// into a handle-private RAM copy and RE-ENCRYPTS THAT COPY BACK OVER THE REAL PATH on
// Close ("SINGLE WRITER per file ... last close wins"). So opening a sibling chain to
// READ it would seal a stale snapshot over a chain another process is still appending
// to — a verification that destroys the evidence. mk/fleet.mk's `dist` builds every
// published plugin CGO_ENABLED=0, so that build is a real deployment, not a corner.
//
// The assertion is on the OUTCOME either way: after a Trail walk, every sibling chain
// still holds exactly the records it held. That is the property that matters, and it
// holds on both builds — through the C codec on one, and by declining to open on the
// other.
func TestSiblingChainsAreReadOnlyOrNotReadAtAll(t *testing.T) {
	rec, dir := openTemp(t)
	ctx := context.Background()
	mustClosedChain(t, dir, "audit-iam", 6)

	if _, err := rec.Trail(ctx); err != nil {
		t.Fatalf("Trail: %v", err)
	}

	// Re-open the sibling and count. A seal-over would have replaced it.
	sib, err := Open(dir, "audit-iam", nil)
	if err != nil {
		t.Fatalf("reopen sibling: %v", err)
	}
	defer func() { _ = sib.Close() }()
	iv, err := sib.Verify(ctx)
	if err != nil {
		t.Fatalf("verify sibling after Trail: %v", err)
	}
	if iv.Verdict != Intact || iv.Count != 6 {
		t.Fatalf("sibling after Trail = %+v, want intact with 6 records — reading the family damaged it", iv)
	}

	// And on a build that cannot read a sibling safely, the trail must SAY so rather
	// than skip it or pass it.
	if !sqlitedrv.CodecLinked() {
		tr, err := rec.Trail(ctx)
		if err != nil {
			t.Fatalf("Trail: %v", err)
		}
		sibling, ok := chainOf(tr, "audit-iam")
		if !ok {
			t.Fatal("sibling chain absent from the trail on a build that cannot read it")
		}
		if sibling.Verdict != Unread {
			t.Fatalf("sibling verdict = %q on a build with no codec, want %q", sibling.Verdict, Unread)
		}
	}
}

// chainOf finds one chain's verdict in a trail.
func chainOf(tr Trail, name string) (Integrity, bool) {
	for _, ch := range tr.Chains {
		if ch.Name == name {
			return ch, true
		}
	}
	return Integrity{}, false
}

// chainPath is where cek puts a chain — asked of namespace, never spelled here.
func chainPath(t *testing.T, dir, name string) string {
	t.Helper()
	p, err := namespace.Path(dir, namespace.System(), name)
	if err != nil {
		t.Fatalf("namespace.Path(%q): %v", dir, err)
	}
	return p
}

// mustClosedChain writes a sibling chain with n records and closes it — another
// process's chain at rest.
func mustClosedChain(t *testing.T, dir, name string, n int) {
	t.Helper()
	rec, err := Open(dir, name, nil)
	if err != nil {
		t.Fatalf("open chain %s: %v", name, err)
	}
	for i := range n {
		if _, err := rec.Append(context.Background(), sampleRecord("POST /v1/kms/secrets")); err != nil {
			t.Fatalf("seed %s %d: %v", name, i, err)
		}
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("close chain %s: %v", name, err)
	}
}

// mustClosedStore creates a store that is NOT a chain, in the family's directory.
func mustClosedStore(t *testing.T, dir, name string) {
	t.Helper()
	db, err := cekOpen(dir, name)
	if err != nil {
		t.Fatalf("open store %s: %v", name, err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS whatever (k TEXT)`); err != nil {
		t.Fatalf("seed store %s: %v", name, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store %s: %v", name, err)
	}
}

// writeDecoy drops bytes that are not a database at path.
func writeDecoy(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("decoy dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("not a database"), 0o600); err != nil {
		t.Fatalf("decoy %s: %v", path, err)
	}
}
