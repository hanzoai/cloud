package risk

// durable_test.go pins the property store.go's header claims: a record is not
// acknowledged until it is durable.
//
// The test drives the REAL durable plane — cloud.OrgStore over org.Durability
// over a conditional object store — with the object store standing in as an
// in-memory CAS register. Nothing here is a mock of our own code: the ship, the
// fence and the lease are the production ones, and the only substitution is the
// bucket.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/org"
	sqlitedrv "github.com/hanzoai/sqlite"
	"github.com/hanzoai/vfs/replica"
	"github.com/luxfi/aml/pkg/anomaly"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestADecisionIsShippedBeforeItIsAcknowledged.
//
// A decision is the record an adverse action is defended with. cloud deploys
// Recreate at one replica, so a decision that lives only on the pod's volume is
// one a rollout can lose AFTER the caller was told it was taken. The ship
// happens before the 200, or the 200 is a lie.
func TestADecisionIsShippedBeforeItIsAcknowledged(t *testing.T) {
	app, _, store := wireDurable(t)

	before := store.puts()
	code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"payment","subject":{"kind":"transaction","id":"tx-1"},
		  "amount":{"nano":12000000000,"currency":"USD","direction":"in"}}`)
	if code != http.StatusOK {
		t.Fatalf("decide = %d %s", code, body)
	}
	if got := store.puts() - before; got == 0 {
		t.Fatal("the decision was acknowledged without a single write to the durable object: " +
			"a rollout loses it and the caller was told it was taken")
	}
}

// TestAWriteThatCannotBeShippedIsNotAcknowledged.
//
// The object store goes away mid-flight. EVERY record write must then FAIL —
// loudly, with a status a caller retries on — rather than answer 200 over a row
// that exists on one pod's disk and nowhere else. The retry is exact because the
// caller's idempotency key is claimed inside the same transaction.
//
// EVERY MUTATING ROUTE IS IN THE TABLE, and that is what makes this the guard
// rather than three examples. Durability applied one call site at a time is a
// rule somebody has to remember, and the writes easiest to forget are the
// RETIREMENTS — a rule deleted, a mute lifted, a hold released — because an
// unshipped delete brings the old state back at the next rollout, firing or
// blocking, after a 204 said it was gone. A mutating op absent from the table
// and from the records-nothing list fails the companion test below, which reads
// the published subset.
//
// ONE app and ONE store for all of it. Every subject a retirement retires is
// created first, while the store is up; then the store fails ONCE and every
// write runs against it. A fresh durable harness per case would re-encrypt and
// fsync the whole file per setup write, which is minutes of the suite's wall
// clock to prove nothing the shared setup does not.
func TestAWriteThatCannotBeShippedIsNotAcknowledged(t *testing.T) {
	app, _, store := wireDurable(t)
	ids := seedForRetirement(t, app)

	store.fail(errors.New("the object store is unreachable"))
	for _, w := range durableWrites(ids) {
		code, body := req(t, app, w.method, w.path, "acme", "u_acme", w.body)
		if code < 500 {
			t.Errorf("%s (%s %s) answered %d %s while the durable object store was down — "+
				"the record exists on this pod only and the caller was told otherwise",
				w.what, w.method, w.path, code, body)
		}
	}
}

// subjects are the server-minted identifiers a retirement needs. Every id here
// is READ BACK from the create, never guessed: this plane mints its own, so a
// literal in a test would be testing a 404 path.
type subjects struct{ rule, spare, mute, control, decision string }

func seedForRetirement(t *testing.T, app *zip.App) subjects {
	t.Helper()
	var s subjects
	s.rule = mintedID(t, app, http.MethodPost, "/v1/risk/rules", ruleBody("durable"))
	s.spare = mintedID(t, app, http.MethodPost, "/v1/risk/rules", ruleBody("spare"))
	s.mute = mintedID(t, app, http.MethodPost, "/v1/risk/suppressions",
		`{"rule":"`+s.rule+`","kind":"transaction","reason":"noisy under test"}`)
	s.control = mintedID(t, app, http.MethodPost, "/v1/risk/controls",
		`{"subject":{"kind":"account","id":"acct-1"},"control":"payout-hold","reason":"under review"}`)
	s.decision = mintedID(t, app, http.MethodPost, "/v1/risk/decide",
		`{"stage":"payment","subject":{"kind":"transaction","id":"tx-seed"}}`)
	for _, c := range []call{
		{http.MethodPost, "/v1/risk/lists", `{"name":"blocked-ips","kind":"deny"}`},
		{http.MethodPost, "/v1/risk/lists/blocked-ips/entries", `{"values":["203.0.113.9"]}`},
		{http.MethodPost, "/v1/ml/train", trainBody},
	} {
		if code, body := req(t, app, c.method, c.path, "acme", "u_acme", c.body); code >= 300 {
			t.Fatalf("setup %s %s = %d %s", c.method, c.path, code, body)
		}
	}
	return s
}

// mintedID performs one create and returns the id the SERVER chose.
func mintedID(t *testing.T, app *zip.App, method, path, body string) string {
	t.Helper()
	code, out := req(t, app, method, path, "acme", "u_acme", body)
	if code >= 300 {
		t.Fatalf("setup %s %s = %d %s", method, path, code, out)
	}
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &v); err != nil || v.ID == "" {
		t.Fatalf("setup %s %s did not answer with an id: %s", method, path, out)
	}
	return v.ID
}

// call is one request.
type call struct{ method, path, body string }

// durableWrite is a mutating route and the name it fails under.
type durableWrite struct{ what, method, path, body string }

func ruleBody(name string) string {
	return `{"rule":{"name":"` + name + `","stage":"payment","action":"review","weight":0.5,"enabled":true,
	   "all":[{"field":"subject.kind","op":"eq","value":"transaction"}]}}`
}

// durableWrites is the table. The RETIREMENTS come last and each takes a
// DIFFERENT subject from the one an earlier case needs, because a write's local
// half lands even when its ship does not — that is the defect under test.
func durableWrites(s subjects) []durableWrite {
	return []durableWrite{
		{"a decision", http.MethodPost, "/v1/risk/decide",
			`{"stage":"payment","subject":{"kind":"transaction","id":"tx-2"}}`},
		{"a label on a decision", http.MethodPost, "/v1/risk/decisions/" + s.decision + "/label",
			`{"verdict":"fraud"}`},
		{"a policy version", http.MethodPut, "/v1/risk/policy",
			`{"stage":"payment","floor":"allow","reason":"the dispute rate doubled","bands":[{"at":0.9,"action":"block"}]}`},
		{"a rule", http.MethodPost, "/v1/risk/rules", ruleBody("another")},
		{"a rule change", http.MethodPatch, "/v1/risk/rules/" + s.rule, ruleBody("changed")},
		{"a list", http.MethodPost, "/v1/risk/lists", `{"name":"watched-ips","kind":"allow"}`},
		{"a deny-list entry", http.MethodPost, "/v1/risk/lists/blocked-ips/entries", `{"values":["198.51.100.4"]}`},
		{"a deny-list REMOVAL", http.MethodDelete, "/v1/risk/lists/blocked-ips/entries/203.0.113.9", ""},
		{"a mute", http.MethodPost, "/v1/risk/suppressions",
			`{"rule":"` + s.spare + `","kind":"account","reason":"noisy too"}`},
		{"a mute LIFTED", http.MethodDelete, "/v1/risk/suppressions/" + s.mute, ""},
		{"a control", http.MethodPost, "/v1/risk/controls",
			`{"subject":{"kind":"account","id":"acct-2"},"control":"block","reason":"card testing"}`},
		{"a control RELEASED", http.MethodDelete, "/v1/risk/controls/" + s.control, ""},
		{"the live/shadow switch", http.MethodPut, "/v1/risk/mode", `{"mode":"shadow"}`},
		{"the appetite", http.MethodPut, "/v1/ml/state/appetite", `{"review":0.01,"sample":0.001}`},
		{"a model snapshot", http.MethodPost, "/v1/ml/snapshot", `{}`},
		{"a search run", http.MethodPost, "/v1/ml/search", `{"limit":10}`},
		{"a rule RETIREMENT", http.MethodDelete, "/v1/risk/rules/" + s.spare, ""},
	}
}

// trainBody is enough observations that the model has something to snapshot.
const trainBody = `{"observations":[
 {"stage":"payment","subject":{"kind":"transaction","id":"t1"},"amount":{"nano":1000000000,"currency":"USD","direction":"in"}},
 {"stage":"payment","subject":{"kind":"transaction","id":"t2"},"amount":{"nano":2000000000,"currency":"USD","direction":"in"}},
 {"stage":"payment","subject":{"kind":"transaction","id":"t3"},"amount":{"nano":3000000000,"currency":"USD","direction":"in"}}]}`

// TestEveryMutatingRouteIsCoveredByTheDurabilityTable is the guard that survives
// a NEW op rather than a revert.
//
// The table above is only a guard while it is complete, and the way it stops
// being complete is somebody adding a route. This reads the app's OWN published
// subset — the artifact the SDKs are generated from — and insists every mutating
// path is either exercised by the table or named below as recording nothing.
func TestEveryMutatingRouteIsCoveredByTheDurabilityTable(t *testing.T) {
	// Routes that record NOTHING: they answer from memory or from a read, so
	// there is no row whose durability could be in question. Each is named, so
	// admitting one is a decision somebody wrote down.
	//
	// /v1/ml/calibrate and /v1/ml/replay DO record, and both ship — they are
	// exercised by their own tests in skew_test.go, which need 200 judged rows
	// apiece and would cost this table two more fits to say the same thing.
	silent := map[string]bool{
		"POST /v1/ml/score":      true, // scores one observation and learns nothing
		"POST /v1/ml/train":      true, // moves in-memory counters; /v1/ml/snapshot is the record
		"POST /v1/ml/restore":    true, // reads a snapshot back into memory
		"POST /v1/ml/evaluate":   true, // measures over rows already written
		"POST /v1/risk/simulate": true, // tries a rule against history and writes nothing
		"POST /v1/ml/calibrate":  true, // records, and ships — covered in skew_test.go
		"POST /v1/ml/replay":     true, // records, and ships — covered in skew_test.go
	}
	covered := map[string]bool{}
	for _, w := range durableWrites(subjects{rule: "R", spare: "S", mute: "M", control: "C", decision: "D"}) {
		covered[w.method+" "+routePattern(w.path)] = true
	}

	var doc struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(readSubset(t), &doc); err != nil {
		t.Fatalf("plugin/risk/openapi.json: %v", err)
	}
	if len(doc.Paths) == 0 {
		t.Fatal("no paths read, so this guard proved nothing")
	}
	for path, methods := range doc.Paths {
		for m := range methods {
			method := strings.ToUpper(m)
			switch method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			default:
				continue
			}
			key := method + " " + path
			if silent[key] || covered[key] {
				continue
			}
			t.Errorf("%s is a mutating route the durability table does not exercise: either it "+
				"writes a record and must ship before it acknowledges, or it records nothing and "+
				"must say so in the list above", key)
		}
	}
}

// routePattern turns a concrete request path back into the pattern the published
// subset names it by, so the table can be written with real, server-minted ids.
func routePattern(path string) string {
	switch {
	case strings.HasPrefix(path, "/v1/risk/rules/"):
		return "/v1/risk/rules/{id}"
	case strings.HasPrefix(path, "/v1/risk/suppressions/"):
		return "/v1/risk/suppressions/{id}"
	case strings.HasPrefix(path, "/v1/risk/controls/"):
		return "/v1/risk/controls/{id}"
	case strings.HasPrefix(path, "/v1/risk/lists/") && strings.Contains(path, "/entries/"):
		return "/v1/risk/lists/{name}/entries/{value}"
	case strings.HasPrefix(path, "/v1/risk/lists/") && strings.HasSuffix(path, "/entries"):
		return "/v1/risk/lists/{name}/entries"
	case strings.HasPrefix(path, "/v1/risk/decisions/") && strings.HasSuffix(path, "/label"):
		return "/v1/risk/decisions/{id}/label"
	}
	return path
}

// TestADeposedWriterDoesNotAcknowledgeItsOwnWrite is the OTHER half of the ship,
// and the half a warning would hide.
//
// A ship has three outcomes, not two. It can fail (an error — covered above); it
// can be acknowledged; or it can come back UNACKNOWLEDGED WITH NO ERROR, which
// is what the fence answers when a successor pod has advanced the round: this
// process is no longer this org's elected writer, so the row it just wrote is on
// a deposed pod's disk and the org's record is elsewhere. Nothing failed. If the
// plane treats that as success — or logs it and answers 200 — the caller is told
// a decision is on file that the surviving writer will never produce.
func TestADeposedWriterDoesNotAcknowledgeItsOwnWrite(t *testing.T) {
	app, _, store := wireDurable(t)

	// One good decision first, so the file, the lease and the object all exist:
	// what follows is a writer that was deposed, not one that never held the org.
	code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"payment","subject":{"kind":"transaction","id":"tx-1"}}`)
	if code != http.StatusOK {
		t.Fatalf("first decide = %d %s", code, body)
	}

	store.depose()
	code, body = req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"payment","subject":{"kind":"transaction","id":"tx-2"}}`)
	if code < 500 {
		t.Fatalf("a deposed writer answered %d %s — the decision is on this pod's disk and "+
			"the org's elected writer will never produce it", code, body)
	}
	if !strings.Contains(string(body), "elected writer") {
		t.Errorf("the refusal does not say why, so an operator reads it as a generic 5xx: %s", body)
	}
}

// TestTheRecordPlaneIsOpenedThroughTheDurableStore is the guard that survives a
// revert. The bare encrypted open is the open and nothing more, so a package
// that calls it directly has no hydrate, no fence and no ship whatever its
// comments say. This package reaches a file through the durable org store and no
// other way.
func TestTheRecordPlaneIsOpenedThroughTheDurableStore(t *testing.T) {
	// Spelled in two halves so this file does not match its own guard.
	bareOpen := "cloud.Org" + "DB("

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no source read, so this guard proved nothing")
	}
	found := false
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(string(body), "cloud.NewOrgStore") {
			found = true
		}
		if strings.Contains(string(body), bareOpen) {
			t.Errorf("%s opens a tenant file with cloud.OrgDB — that open is local-only, so every record "+
				"in it is as durable as one pod's volume", f)
		}
	}
	if !found {
		t.Error("nothing in this package opens its files through cloud.NewOrgStore, so nothing is durable")
	}
}

// TestCommitRefusesToShipAWriteThatFailed pins the order inside commit: the ship
// is the acknowledgement of a write that HAPPENED, so a failed write must not
// produce one.
func TestCommitRefusesToShipAWriteThatFailed(t *testing.T) {
	_, s := wireApp(t)
	boom := errors.New("the write failed")
	err := s.State.shelf.commit(Tenant("hanzo/acme"), func(*sql.DB) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("commit returned %v, want the write's own error", err)
	}
}

// ── the durable harness ─────────────────────────────────────────────────────

// wireDurable mounts the surface over a REAL durable org store whose object
// store is an in-memory CAS register.
func wireDurable(t *testing.T) (*zip.App, *stateService, *bucket) {
	t.Helper()
	dir := t.TempDir()
	log := luxlog.New("risktest")
	b := cloud.NewBase(cloud.Deps{Logger: log, DataDir: dir, Brand: "hanzo"}, "risk")
	store := newBucket()
	// The SAME checkpoint the composition root wires (build.go durableCheckpoint):
	// the ship reads the real on-disk path, and on an encrypting backend that path
	// is only fresh after the WAL is folded and re-encrypted. Without it this
	// harness would be testing a ship of a file that does not exist.
	b.Durable = org.NewDurability(store, soleWriter{}, nil, org.WithCheckpoint(checkpoint))

	shape, err := forest(anomaly.Config{}, aggregates())
	if err != nil {
		t.Fatalf("forest: %v", err)
	}
	s := &cloud.Service[state]{Base: b, State: state{
		brand:    "hanzo",
		dataDir:  dir,
		shelf:    newShelf(b),
		inflight: newInflight("a measurement"),
		running:  newInflight("an exhaustive search"),
		digest:   shape.Digest(),
		bill:     cloud.NewResourceMeter(cloud.Deps{Logger: log}, "risk"),
	}}
	app := zip.New(zip.Config{Logger: log, DisableStartupMessage: true})
	mount(s, app)
	t.Cleanup(func() { s.State.shelf.close() })
	return app, s, store
}

// checkpoint folds the WAL into the real path and re-encrypts it, which is what
// makes the bytes a ship reads the bytes a caller was told were written.
func checkpoint(ctx context.Context, db *sql.DB) error {
	// The fold runs on its OWN connection and RELEASES it before the re-encrypt:
	// the store holds one connection, and an envelope re-encrypt that asks for a
	// second while the first is still open waits for itself. (Production splits
	// these two for the same reason — build.go walCheckpointTruncate.)
	if err := fold(ctx, db); err != nil {
		return err
	}
	return sqlitedrv.Checkpoint(db)
}

func fold(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	var busy, frames, done int
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &done); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("checkpoint busy=%d", busy)
	}
	return nil
}

// soleWriter is the membership: one pod, which is therefore every org's elected
// writer. The election itself is tested in internal/org; here it only has to
// resolve to "this process owns the lease" so a ship can be acknowledged.
type soleWriter struct{}

func (soleWriter) Self() string          { return "pod-a" }
func (soleWriter) Members() []org.Member { return []org.Member{{ID: "pod-a"}} }

// bucket is an in-memory conditional store: the CAS register the fence and the
// ship both run over. It counts writes and can be made to fail.
type bucket struct {
	mu     sync.Mutex
	slots  map[string]*slot
	writes int
	err    error
	fenced bool
}

type slot struct {
	data []byte
	ver  int
}

func newBucket() *bucket { return &bucket{slots: map[string]*slot{}} }

func (b *bucket) puts() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writes
}

func (b *bucket) fail(err error) {
	b.mu.Lock()
	b.err = err
	b.mu.Unlock()
}

// depose models a successor pod that has advanced this org's recorded round: the
// store admits the lease renewal and refuses every RECORD ship as fenced. That
// is not a failure — the fence is working — so Sync answers (false, nil), which
// is the outcome an error check alone never sees.
func (b *bucket) depose() {
	b.mu.Lock()
	b.fenced = true
	b.mu.Unlock()
}

func (b *bucket) Get(_ context.Context, key string) ([]byte, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return nil, "", b.err
	}
	s, ok := b.slots[key]
	if !ok {
		return nil, "", replica.ErrNotFound
	}
	return append([]byte(nil), s.data...), strconv.Itoa(s.ver), nil
}

func (b *bucket) PutIfVersion(_ context.Context, key string, data []byte, expect string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return "", b.err
	}
	// A lease renewal is admitted — this process still runs; it is the RECORD
	// ship that a successor's higher round fences.
	if b.fenced && !strings.Contains(key, "lease") {
		return "", fmt.Errorf("bucket: %w", replica.ErrStaleRound)
	}
	s, ok := b.slots[key]
	cur := ""
	if ok {
		cur = strconv.Itoa(s.ver)
	}
	if cur != expect {
		return "", fmt.Errorf("bucket: %w: have %q want %q", replica.ErrConflict, cur, expect)
	}
	if !ok {
		s = &slot{}
		b.slots[key] = s
	}
	s.ver++
	s.data = append([]byte(nil), data...)
	if !strings.Contains(key, "lease") { // a lease renewal is not a record ship
		b.writes++
	}
	return strconv.Itoa(s.ver), nil
}
