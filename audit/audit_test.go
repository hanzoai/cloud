package audit

// Tests for the tamper-evident audit chain. They exercise the REAL SQLite store
// (an on-disk temp db, not a mock) so the append-only write path, the hash-chain
// math, the verifier, redaction, and the filtered query are all proven
// end-to-end. The headline test is tamper-detection: a record edited directly in
// the database is DETECTED as breaking the chain.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/cek"
)

// openTemp opens a Recorder backed by a fresh on-disk SQLite file (not :memory:,
// because tamper tests re-open the same file via a second connection to edit it
// out-of-band — exactly what an attacker with DB access would do).
func openTemp(t *testing.T) (*Recorder, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	rec, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	return rec, path
}

// sampleRecord is a representative security event (a SuperAdmin org deletion).
func sampleRecord(action string) Record {
	return Record{
		Time:      time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
		Actor:     Actor{Org: "admin", Sub: "z@hanzo.ai", Email: "z@hanzo.ai"},
		Action:    action,
		Resource:  Resource{Type: "org", ID: "acme"},
		Auth:      AuthContext{Method: "jwt", IsAdmin: true},
		Outcome:   Outcome{Result: "success", Status: 200},
		SourceIP:  "203.0.113.7",
		UserAgent: "console",
		RequestID: "req-123",
		Method:    "DELETE",
		Path:      "/v1/admin/orgs/acme",
	}
}

// TestChain_AppendSealsAndLinks proves each appended record gets a monotonic seq,
// links its PrevHash to the previous record's Hash, and starts from the genesis
// anchor.
func TestChain_AppendSealsAndLinks(t *testing.T) {
	rec, _ := openTemp(t)
	ctx := context.Background()

	r0, err := rec.Append(ctx, sampleRecord("DELETE /v1/admin/orgs"))
	if err != nil {
		t.Fatalf("append 0: %v", err)
	}
	if r0.Seq != 0 {
		t.Fatalf("first seq = %d, want 0", r0.Seq)
	}
	if r0.PrevHash != genesisPrevHash {
		t.Fatalf("genesis prev = %q, want %q", r0.PrevHash, genesisPrevHash)
	}
	if r0.Hash == "" || r0.Hash == genesisPrevHash {
		t.Fatalf("hash not computed: %q", r0.Hash)
	}

	r1, err := rec.Append(ctx, sampleRecord("POST /v1/admin/roles"))
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if r1.Seq != 1 {
		t.Fatalf("second seq = %d, want 1", r1.Seq)
	}
	if r1.PrevHash != r0.Hash {
		t.Fatalf("link broken: r1.prev=%q, r0.hash=%q", r1.PrevHash, r0.Hash)
	}
	if r1.Hash == r0.Hash {
		t.Fatal("distinct records must have distinct hashes")
	}
}

// TestVerify_PassesOnUntamperedChain proves a well-formed chain verifies OK.
func TestVerify_PassesOnUntamperedChain(t *testing.T) {
	rec, _ := openTemp(t)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		if _, err := rec.Append(ctx, sampleRecord("POST /v1/admin/sync")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	integrity, err := rec.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !integrity.OK {
		t.Fatalf("chain not OK: broken at %d (%s)", integrity.BrokenAt, integrity.Reason)
	}
	if integrity.Count != 25 {
		t.Fatalf("count = %d, want 25", integrity.Count)
	}
	if integrity.BrokenAt != -1 {
		t.Fatalf("brokenAt = %d, want -1 on a good chain", integrity.BrokenAt)
	}
	// Head must equal the last record's hash.
	count, head := rec.Head()
	if count != 25 || head != integrity.HeadHash {
		t.Fatalf("head mismatch: (%d,%q) vs verify (%d,%q)", count, head, integrity.Count, integrity.HeadHash)
	}
}

// TestVerify_DetectsFieldTamper is the headline: an attacker with direct DB
// access edits a record's content (flips a denied outcome to success, or changes
// the actor). The stored hash no longer matches the recomputed hash, so Verify
// reports the exact seq where the chain breaks. THIS is the tamper-evidence
// property — an audit trail that can be silently forged is worse than none.
func TestVerify_DetectsFieldTamper(t *testing.T) {
	requireSharedStore(t) // tampers through a second handle while the Recorder holds the store open
	rec, path := openTemp(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := rec.Append(ctx, sampleRecord("DELETE /v1/admin/orgs")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// Sanity: clean chain verifies.
	if iv, _ := rec.Verify(ctx); !iv.OK {
		t.Fatalf("precondition: clean chain should verify, broke at %d", iv.BrokenAt)
	}

	// Tamper OUT OF BAND — a second connection issues an UPDATE the application
	// never would. This models an attacker who owns the file / a rogue DBA.
	tamperOutOfBand(t, path, `UPDATE audit_log SET actor_sub='attacker', result='success' WHERE seq=4`)

	iv, err := rec.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify after tamper: %v", err)
	}
	if iv.OK {
		t.Fatal("TAMPER NOT DETECTED — a modified record verified as OK; the chain is forgeable")
	}
	if iv.BrokenAt != 4 {
		t.Fatalf("brokenAt = %d, want 4 (the edited record)", iv.BrokenAt)
	}
	if !strings.Contains(iv.Reason, "hash mismatch") {
		t.Fatalf("reason = %q, want a hash-mismatch explanation", iv.Reason)
	}
}

// TestVerify_DetectsDeletion proves deleting a record (or a contiguous run) breaks
// the chain: the record after the hole has a PrevHash that no longer matches the
// now-preceding record, and the seq sequence gaps. Either way Verify flags it.
func TestVerify_DetectsDeletion(t *testing.T) {
	requireSharedStore(t) // tampers through a second handle while the Recorder holds the store open
	rec, path := openTemp(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := rec.Append(ctx, sampleRecord("POST /v1/admin/roles")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// Delete a MIDDLE record — the classic "cover your tracks" edit.
	tamperOutOfBand(t, path, `DELETE FROM audit_log WHERE seq=5`)

	iv, err := rec.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify after delete: %v", err)
	}
	if iv.OK {
		t.Fatal("DELETION NOT DETECTED — a removed record left the chain verifying OK")
	}
	// The break is observed at seq 6 (the record whose predecessor vanished): its
	// seq no longer follows the running counter (5 is missing), so the gap check
	// fires first at 6.
	if iv.BrokenAt != 6 {
		t.Fatalf("brokenAt = %d, want 6 (record after the hole)", iv.BrokenAt)
	}
}

// TestVerify_DetectsReorder proves swapping two records' positions (an attacker
// trying to reorder events) breaks the prev-hash linkage.
func TestVerify_DetectsReorder(t *testing.T) {
	requireSharedStore(t) // tampers through a second handle while the Recorder holds the store open
	rec, path := openTemp(t)
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		if _, err := rec.Append(ctx, sampleRecord("POST /v1/kms/secrets")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// Swap the hashes of seq 2 and seq 3 (content stays, linkage corrupts). Any
	// out-of-band shuffle that doesn't recompute the WHOLE suffix is detectable.
	tamperOutOfBand(t, path, `
UPDATE audit_log SET hash = (SELECT hash FROM audit_log WHERE seq=3) WHERE seq=2;`)

	iv, _ := rec.Verify(ctx)
	if iv.OK {
		t.Fatal("REORDER/HASH-SWAP NOT DETECTED")
	}
	if iv.BrokenAt < 0 {
		t.Fatalf("expected a break, got brokenAt=%d", iv.BrokenAt)
	}
}

// TestChain_RestartContinues proves a re-opened store continues the SAME chain
// (recovers seq + head) rather than forking — so a pod restart cannot silently
// reset the trail.
func TestChain_RestartContinues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	ctx := context.Background()

	rec1, err := Open(path, nil)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	var lastHash string
	for i := 0; i < 5; i++ {
		r, err := rec1.Append(ctx, sampleRecord("POST /v1/admin/sync"))
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		lastHash = r.Hash
	}
	_ = rec1.Close()

	rec2, err := Open(path, nil)
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	defer func() { _ = rec2.Close() }()

	count, head := rec2.Head()
	if count != 5 {
		t.Fatalf("recovered count = %d, want 5", count)
	}
	if head != lastHash {
		t.Fatalf("recovered head = %q, want %q", head, lastHash)
	}
	// The next append must chain onto the recovered head at seq 5.
	r5, err := rec2.Append(ctx, sampleRecord("DELETE /v1/admin/orgs"))
	if err != nil {
		t.Fatalf("append after restart: %v", err)
	}
	if r5.Seq != 5 || r5.PrevHash != lastHash {
		t.Fatalf("chain did not continue: seq=%d prev=%q (want seq 5 prev %q)", r5.Seq, r5.PrevHash, lastHash)
	}
	// And the whole continued chain still verifies.
	if iv, _ := rec2.Verify(ctx); !iv.OK {
		t.Fatalf("continued chain broke at %d (%s)", iv.BrokenAt, iv.Reason)
	}
}

// TestRedact_StripsSecrets proves the redactor removes credential-bearing fields
// (by key name, recursively) while keeping non-secret structure — so an explicit
// emit point's before/after can never carry a password/token/key.
func TestRedact_StripsSecrets(t *testing.T) {
	in := json.RawMessage(`{
      "name": "acme",
      "password": "hunter2",
      "apiKey": "sk-live-abc123",
      "passphrase": "correct horse",
      "wgPrivKey": "PRIVKEYBYTES",
      "recoveryPhrase": "twelve words here",
      "socialSecurityNumber": "078-05-1120",
      "config": {
        "clientSecret": "shh",
        "endpoint": "https://api.example.com",
        "nested": {"private_key": "-----BEGIN-----", "region": "sfo3"}
      },
      "tokens": ["t1", "t2"],
      "roles": ["admin", "viewer"]
    }`)
	out := Redact(in)

	s := string(out)
	// Secrets gone (incl. the edge-case key names: passphrase, privkey, phrase, ssn).
	for _, leak := range []string{"hunter2", "sk-live-abc123", "shh", "BEGIN",
		"correct horse", "PRIVKEYBYTES", "twelve words here", "078-05-1120"} {
		if strings.Contains(s, leak) {
			t.Fatalf("secret leaked through redaction: %q still present in %s", leak, s)
		}
	}
	// Non-secret structure preserved.
	for _, keep := range []string{"acme", "https://api.example.com", "sfo3", "viewer"} {
		if !strings.Contains(s, keep) {
			t.Fatalf("redaction dropped a non-secret value %q: %s", keep, s)
		}
	}
	// The redaction marker appears where secrets were.
	if !strings.Contains(s, redactedMarker) {
		t.Fatalf("no redaction marker in output: %s", s)
	}
	// "tokens" is a secret key → the whole array is redacted (not its elements).
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("redacted output is not valid JSON: %v", err)
	}
	if decoded["tokens"] != redactedMarker {
		t.Fatalf("secret-keyed array not redacted whole: %v", decoded["tokens"])
	}
}

// TestRedact_FailsClosedOnBadJSON proves unparseable input is never echoed back.
func TestRedact_FailsClosedOnBadJSON(t *testing.T) {
	out := Redact(json.RawMessage(`{not valid json, password=hunter2`))
	if strings.Contains(string(out), "hunter2") {
		t.Fatalf("bad JSON echoed a secret: %s", out)
	}
	if !strings.Contains(string(out), redactedMarker) {
		t.Fatalf("bad JSON should redact to a marker, got %s", out)
	}
}

// TestQuery_Filters proves the filtered read returns the right subset by actor,
// action, resource, and result, newest-first, with an accurate total.
func TestQuery_Filters(t *testing.T) {
	rec, _ := openTemp(t)
	ctx := context.Background()

	mk := func(org, action, res, result string) Record {
		r := sampleRecord(action)
		r.Actor.Org = org
		r.Resource.Type = res
		r.Outcome.Result = result
		return r
	}
	// A mixed set.
	seed := []Record{
		mk("admin", "DELETE /v1/admin/orgs", "org", "success"),
		mk("acme", "POST /v1/base/records", "records", "success"),
		mk("admin", "POST /v1/admin/roles", "roles", "deny"),
		mk("admin", "DELETE /v1/admin/orgs", "org", "success"),
		mk("acme", "POST /v1/kms/secrets", "secrets", "error"),
	}
	for i, r := range seed {
		if _, err := rec.Append(ctx, r); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	// Filter by org=admin → 3 rows.
	rows, total, err := rec.Query(ctx, Filter{Org: "admin"})
	if err != nil {
		t.Fatalf("query org: %v", err)
	}
	if total != 3 || len(rows) != 3 {
		t.Fatalf("org=admin: got %d rows, total %d, want 3/3", len(rows), total)
	}
	// Newest first: the last-appended admin row (seq 3) comes before seq 2, 0.
	if rows[0].Seq < rows[len(rows)-1].Seq {
		t.Fatalf("not newest-first: %d..%d", rows[0].Seq, rows[len(rows)-1].Seq)
	}

	// Filter by result=deny → 1 row (the 403-style role change).
	denies, dtotal, err := rec.Query(ctx, Filter{Result: "deny"})
	if err != nil {
		t.Fatalf("query deny: %v", err)
	}
	if dtotal != 1 || len(denies) != 1 || denies[0].Action != "POST /v1/admin/roles" {
		t.Fatalf("result=deny: got %d (%+v), want 1 role-change", dtotal, denies)
	}

	// Filter by resource=secrets → 1 row.
	secs, stotal, err := rec.Query(ctx, Filter{Resource: "secrets"})
	if err != nil {
		t.Fatalf("query resource: %v", err)
	}
	if stotal != 1 || len(secs) != 1 {
		t.Fatalf("resource=secrets: got %d, want 1", stotal)
	}
}

// TestQuery_SQLInjectionInFilterIsInert proves a malicious filter value is a
// parameter, never SQL: it simply matches nothing and cannot drop the table.
func TestQuery_SQLInjectionInFilterIsInert(t *testing.T) {
	rec, _ := openTemp(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := rec.Append(ctx, sampleRecord("POST /v1/admin/sync")); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	inject := Filter{Org: "admin'; DROP TABLE audit_log;--"}
	rows, total, err := rec.Query(ctx, inject)
	if err != nil {
		t.Fatalf("query should not error on injection attempt: %v", err)
	}
	if total != 0 || len(rows) != 0 {
		t.Fatalf("injection value matched %d rows, want 0", total)
	}
	// The table survived — a normal query still returns the seeded rows.
	if _, all, err := rec.Query(ctx, Filter{}); err != nil || all != 3 {
		t.Fatalf("table damaged by injection attempt: all=%d err=%v", all, err)
	}
}

// TestChain_ConcurrentAppendsStayGapless proves the serialized writer keeps the
// chain a true, gapless total order under CONCURRENT appends: many goroutines
// append at once, and the resulting chain must have every seq 0..N-1 exactly once
// AND verify. A race in the head/seq handoff would surface as a duplicate seq (a
// PRIMARY KEY error), a gap, or a broken link — all of which this catches.
func TestChain_ConcurrentAppendsStayGapless(t *testing.T) {
	rec, _ := openTemp(t)
	ctx := context.Background()

	const goroutines, per = 16, 20
	total := goroutines * per
	errCh := make(chan error, total)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if _, err := rec.Append(ctx, sampleRecord("POST /v1/admin/sync")); err != nil {
					errCh <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent append failed (race in seq/head handoff?): %v", err)
	}

	// The chain must verify and contain exactly `total` gapless records.
	iv, err := rec.Verify(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !iv.OK {
		t.Fatalf("concurrent chain broke at %d (%s)", iv.BrokenAt, iv.Reason)
	}
	if iv.Count != uint64(total) {
		t.Fatalf("recorded %d records, want %d (a lost/duplicated append)", iv.Count, total)
	}
}

// checkpointMirror is a Mirror that also captures checkpoints (implements
// CheckpointSink) so the test can assert the head digest reaches an independent
// sink.
type checkpointMirror struct {
	mu  sync.Mutex
	cps []Checkpoint
}

func (m *checkpointMirror) Append(context.Context, Record) error { return nil }
func (m *checkpointMirror) Checkpoint(_ context.Context, cp Checkpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cps = append(m.cps, cp)
	return nil
}
func (m *checkpointMirror) last() (Checkpoint, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.cps) == 0 {
		return Checkpoint{}, false
	}
	return m.cps[len(m.cps)-1], true
}

// TestCheckpoint_EmitsHeadDigest proves the AU-9 anchor: the periodic checkpoint
// emits the current (count, head) to the log function AND, when the mirror is a
// CheckpointSink, to the independent digest store — and a final checkpoint fires
// on Close. This is what an external monitor compares to detect tail-truncation.
func TestCheckpoint_EmitsHeadDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	mirror := &checkpointMirror{}
	rec, err := Open(path, mirror)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()

	var logged []Checkpoint
	var lmu sync.Mutex
	// every=0 → no ticker; we drive checkpoints via Close (final) + a manual tick.
	rec.StartCheckpoints(0, func(cp Checkpoint) {
		lmu.Lock()
		logged = append(logged, cp)
		lmu.Unlock()
	})

	for i := 0; i < 7; i++ {
		if _, err := rec.Append(ctx, sampleRecord("POST /v1/admin/sync")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// Close emits the FINAL checkpoint (count=7, head=chain head).
	if err := rec.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	lmu.Lock()
	n := len(logged)
	var lastLogged Checkpoint
	if n > 0 {
		lastLogged = logged[n-1]
	}
	lmu.Unlock()
	if n == 0 {
		t.Fatal("no checkpoint logged (Close should emit a final head digest)")
	}
	if lastLogged.Count != 7 {
		t.Errorf("final checkpoint count = %d, want 7", lastLogged.Count)
	}
	if lastLogged.Head == "" || lastLogged.Head == genesisPrevHash {
		t.Errorf("final checkpoint head not set: %q", lastLogged.Head)
	}
	// The independent sink also received the final digest.
	if cp, ok := mirror.last(); !ok || cp.Count != 7 {
		t.Errorf("checkpoint sink final = %+v (ok=%v), want count 7", cp, ok)
	}
}

// TestCheckpoint_DoubleStartIsSafe proves a second StartCheckpoints call is
// ignored (no re-arm, no field/WaitGroup race) — the Red-review robustness fix.
// Run under -race to catch a regression.
func TestCheckpoint_DoubleStartIsSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	rec, err := Open(path, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rec.StartCheckpoints(time.Hour, func(Checkpoint) {})
	rec.StartCheckpoints(time.Hour, func(Checkpoint) {}) // second call must be a no-op.
	// Append + close must not race or hang.
	if _, err := rec.Append(context.Background(), sampleRecord("POST /v1/admin/sync")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestCheckpoint_CloseSyncsToSink proves the FINAL checkpoint on Close reaches the
// independent sink SYNCHRONOUSLY (the Red-review durability fix) — the sink has
// the final count before Close returns, not on a detached goroutine that might
// not run before process exit.
func TestCheckpoint_CloseSyncsToSink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	mirror := &checkpointMirror{}
	rec, err := Open(path, mirror)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rec.StartCheckpoints(0, func(Checkpoint) {}) // no ticker; only the on-close checkpoint.
	for i := 0; i < 4; i++ {
		if _, err := rec.Append(context.Background(), sampleRecord("POST /v1/admin/sync")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Immediately after Close returns (no sleep), the sink MUST already have the
	// final digest — proving the Close-path write was synchronous.
	cp, ok := mirror.last()
	if !ok || cp.Count != 4 {
		t.Fatalf("sink final checkpoint = %+v (ok=%v), want count 4 synchronously on Close", cp, ok)
	}
}

// TestCheckpoint_CountMonotonicDetectsTruncation demonstrates the DETECTION an
// external monitor performs: consecutive checkpoints have non-decreasing Count;
// after a tail truncation the head reported by Head() drops below a prior
// checkpoint — the signal the o11y alert fires on.
func TestCheckpoint_CountMonotonicDetectsTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	rec, err := Open(path, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := rec.Append(ctx, sampleRecord("POST /v1/admin/sync")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	before, _ := rec.Head() // the monitor's last pinned checkpoint count.
	if before != 10 {
		t.Fatalf("pre-truncation count = %d, want 10", before)
	}
	_ = rec.Close()

	// Attacker truncates the tail (deletes the last 4 records) out of band.
	tamperOutOfBand(t, path, `DELETE FROM audit_log WHERE seq >= 6`)

	rec2, err := Open(path, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = rec2.Close() }()
	after, _ := rec2.Head()
	// The internal chain still verifies (a truncated prefix is self-consistent)…
	if iv, _ := rec2.Verify(ctx); !iv.OK {
		t.Fatalf("truncated prefix should self-verify, broke at %d", iv.BrokenAt)
	}
	// …but the count REGRESSED vs the pinned checkpoint — the truncation signal.
	if after >= before {
		t.Fatalf("count did not regress after truncation: before=%d after=%d", before, after)
	}
	t.Logf("truncation detected by count regression: %d → %d (chain-internal verify is OK; external anchor catches it)", before, after)
}

// tamperOutOfBand opens the SAME sqlite file on a SEPARATE connection and runs a
// mutating statement the audit application itself never issues — modeling an
// attacker with direct database/file access. The Recorder's own connection is
// unaffected; Verify then re-reads and must catch the damage.
func tamperOutOfBand(t *testing.T, path, stmt string) {
	t.Helper()
	// Opened through cek, like the Recorder itself: the store is encrypted at rest,
	// so a bare sql.Open cannot read it. The modelled adversary is one with database
	// access AND the key (an insider, or a compromised process) — file access alone
	// no longer suffices, which is the point of encrypting it.
	db, err := cek.Open(path)
	if err != nil {
		t.Fatalf("tamper open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("tamper exec %q: %v", stmt, err)
	}
}
