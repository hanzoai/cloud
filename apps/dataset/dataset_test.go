package dataset

// dataset_test.go drives the plane the way a caller does — over the real wire,
// through the real Bridge, with the header pair the identity boundary mints —
// and asserts the four properties the plane exists to have: tenancy,
// determinism, immutability and durability across a restart.
//
// The store underneath is fake_test.go's, which parses the WHERE clause out of
// every statement and evaluates it. So an isolation test here fails if the plane
// stops binding the tenant, rather than passing because the harness was doing the
// scoping.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/fiber/v3"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/tenant"
	"github.com/zap-proto/zip"
)

var httpCfg = fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true}

const brand = "hanzo"

// newPlane builds a plane over a fake store. It is the constructor Mount uses
// minus the background ensure, so a test drives the schema step explicitly and
// nothing races it.
func newPlane(f *fake) *plane {
	base := cloud.NewBase(cloud.Deps{Logger: luxlog.New("test"), Brand: brand}, "dataset")
	return &plane{store: f, brand: brand, bill: base.Bill, log: base.Log, busy: map[string]inflight{}}
}

func mountHTTP(t *testing.T, p *plane) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := mount(p, app); err != nil {
		t.Fatalf("mount: %v", err)
	}
	return app
}

// do drives one request. A non-empty org sets BOTH X-Org-Id and X-User-Id, the
// pair the identity boundary mints for a validated principal.
func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	rq := httptest.NewRequest(method, path, r)
	if raw != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Fiber().Test(rq, httpCfg)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// ── a source surface to build from ───────────────────────────────────────────

var origin0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// surface writes n subjects of one org, each with `per` buckets an hour apart,
// starting at an offset that spreads them across the window.
func surface(f *fake, org string, subjects, per int) {
	k, err := tenant.Mint(brand, org)
	if err != nil {
		panic(err)
	}
	for i := range subjects {
		for j := range per {
			f.feature = append(f.feature, featRow{
				Org:     k.String(),
				Kind:    kindPerson,
				Subject: fmt.Sprintf("%s-p%03d", org, i),
				Bucket:  origin0.Add(time.Duration(i*per+j) * time.Hour),
				Value: map[string]float64{
					"events": float64(i + j + 1), "sessions": 1, "distincts": 1, "paths": float64(j + 1),
					"errors": 0, "calls": float64(j), "failures": 0, "tokens": float64(100 * (j + 1)),
					"spend_nano": float64(1000 * (i + 1)), "ips": 1,
				},
			})
		}
	}
}

// declared is a spec over the whole of [twin]'s and [surface]'s extent, matured
// to nothing so the window is admitted in full. The window ends just past the
// last bucket so the derived cuts (70% and 85% of the window by time) fall INSIDE
// the data and all three splits are populated.
func declared(name string) mlDatasetSpec {
	return mlDatasetSpec{
		Name:    name,
		Kind:    kindPerson,
		From:    origin0.Add(-time.Hour).Format(time.RFC3339),
		To:      origin0.Add(61 * time.Hour).Format(time.RFC3339),
		Horizon: 0,
		Seed:    "fixed-seed",
	}
}

// settled waits for a materialisation to reach a terminal state, then returns the
// version as `describe` reports it.
func settled(t *testing.T, app *zip.App, org, name string) mlDataset {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, body := do(t, app, http.MethodGet, "/v1/ml/datasets/"+name, org, nil)
		if code != http.StatusOK {
			t.Fatalf("describe %s: %d (%s)", name, code, body)
		}
		var v mlDatasetVersions
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatalf("describe %s: %v", name, err)
		}
		if len(v.Items) == 0 {
			t.Fatalf("describe %s: no versions", name)
		}
		switch v.Items[0].Status {
		case statusReady, statusRefused:
			return v.Items[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("materialisation of %s never settled (status %q)", name, v.Items[0].Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// build declares and materialises one dataset, and returns the settled version.
func build(t *testing.T, app *zip.App, org string, in mlDatasetSpec) mlDataset {
	t.Helper()
	if code, body := do(t, app, http.MethodPost, "/v1/ml/datasets", org, in); code != http.StatusOK {
		t.Fatalf("declare: %d (%s)", code, body)
	}
	if code, body := do(t, app, http.MethodPost, "/v1/ml/datasets/"+in.Name+"/materialize", org, nil); code != http.StatusAccepted {
		t.Fatalf("materialize: want 202, got %d (%s)", code, body)
	}
	return settled(t, app, org, in.Name)
}

// ── tenancy ──────────────────────────────────────────────────────────────────

// TestForeignOrgSeesNothing is the isolation proof. Two orgs each build a dataset
// with the SAME name over their own surface; neither can list, describe, export,
// trace or dispose of the other's.
//
// The store is shared and unpartitioned by the harness — one slice of manifest
// rows and one of dataset rows — so every refusal below comes from the predicate
// the plane wrote, not from where the test put the data.
func TestForeignOrgSeesNothing(t *testing.T) {
	f := &fake{}
	surface(f, "acme", 6, 4)
	surface(f, "globex", 6, 4)
	p := newPlane(f)
	app := mountHTTP(t, p)

	a := build(t, app, "acme", declared("shared"))
	b := build(t, app, "globex", declared("shared"))
	if a.Status != statusReady || b.Status != statusReady {
		t.Fatalf("both must be ready: %q / %q (%s / %s)", a.Status, b.Status, a.Refusal, b.Refusal)
	}
	if a.Digest == b.Digest {
		t.Fatal("two orgs' datasets over different surfaces produced one digest")
	}
	if a.Counts.Rows != 24 || b.Counts.Rows != 24 {
		t.Fatalf("each org holds 24 of its own rows; got %d and %d", a.Counts.Rows, b.Counts.Rows)
	}

	// A list is only ever this org's.
	for _, org := range []string{"acme", "globex"} {
		code, body := do(t, app, http.MethodGet, "/v1/ml/datasets", org, nil)
		if code != http.StatusOK {
			t.Fatalf("list %s: %d (%s)", org, code, body)
		}
		var list mlDatasetList
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Items) != 1 {
			t.Fatalf("%s sees %d datasets, want 1", org, len(list.Items))
		}
	}

	// An export never crosses. acme's rows are all acme's subjects.
	code, body := do(t, app, http.MethodGet, "/v1/ml/datasets/shared/export?limit=1000", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("export: %d (%s)", code, body)
	}
	var page mlDatasetRows
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 24 {
		t.Fatalf("acme exported %d rows, want its own 24", len(page.Rows))
	}
	for _, r := range page.Rows {
		if !strings.HasPrefix(r.Subject, "acme-") {
			t.Fatalf("acme's export carries %q, which is not acme's", r.Subject)
		}
	}

	// A disposal is per tenant: acme drops its own and globex still has its own,
	// with every row intact.
	if code, body := do(t, app, http.MethodDelete, "/v1/ml/datasets/shared", "acme", nil); code != http.StatusOK {
		t.Fatalf("dispose: %d (%s)", code, body)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/ml/datasets/shared", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("acme's disposed dataset still describes: %d", code)
	}
	code, body = do(t, app, http.MethodGet, "/v1/ml/datasets/shared/export?limit=1000", "globex", nil)
	if code != http.StatusOK {
		t.Fatalf("globex export after acme's disposal: %d (%s)", code, body)
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 24 {
		t.Fatalf("acme's disposal took %d of globex's rows", 24-len(page.Rows))
	}
}

// TestNoPrincipalReachesNothing is the closed door: without the validated header
// pair there is no tenant, and every leaf refuses.
func TestNoPrincipalReachesNothing(t *testing.T) {
	f := &fake{}
	app := mountHTTP(t, newPlane(f))
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/v1/ml/datasets", declared("x")},
		{http.MethodGet, "/v1/ml/datasets", nil},
		{http.MethodGet, "/v1/ml/datasets/x", nil},
		{http.MethodPost, "/v1/ml/datasets/x/materialize", nil},
		{http.MethodGet, "/v1/ml/datasets/x/lineage", nil},
		{http.MethodGet, "/v1/ml/datasets/x/export", nil},
		{http.MethodDelete, "/v1/ml/datasets/x", nil},
	} {
		code, body := do(t, app, tc.method, tc.path, "", tc.body)
		if code != http.StatusForbidden {
			t.Fatalf("%s %s with no principal: want 403, got %d (%s)", tc.method, tc.path, code, body)
		}
	}
	if len(f.manifest) != 0 || len(f.rows) != 0 {
		t.Fatal("an unprincipalled request wrote to the store")
	}
}

// TestTheAnonymousLaneIsNotATenant: the reserved org the event door files
// credential-less writes under has no dataset plane, because a stranger's rows
// are not an organisation's.
func TestTheAnonymousLaneIsNotATenant(t *testing.T) {
	app := mountHTTP(t, newPlane(&fake{}))
	code, body := do(t, app, http.MethodGet, "/v1/ml/datasets", tenant.Public, nil)
	if code != http.StatusForbidden {
		t.Fatalf("the anonymous lane reached the plane: %d (%s)", code, body)
	}
}

// ── determinism ──────────────────────────────────────────────────────────────

// TestSameSeedSameSplit is the reproducibility proof.
//
// Two orgs with IDENTICAL surfaces (same subject ids, same buckets, same
// coordinates) declare the same spec with the same seed. Version 1 of each must
// agree on the digest, on the split of every row, and on every row id — because
// the digest covers the spec and the rows, and the split is a pure function of
// the cuts and each subject's first instant.
func TestSameSeedSameSplit(t *testing.T) {
	f := &fake{}
	twin(f, "one")
	twin(f, "two")
	app := mountHTTP(t, newPlane(f))

	a := build(t, app, "one", declared("d"))
	b := build(t, app, "two", declared("d"))
	if a.Status != statusReady || b.Status != statusReady {
		t.Fatalf("%q %q", a.Refusal, b.Refusal)
	}
	if a.Digest != b.Digest {
		t.Fatalf("the same spec over the same rows produced two digests:\n  %s\n  %s", a.Digest, b.Digest)
	}
	if a.Counts != b.Counts {
		t.Fatalf("the same spec produced different counts: %+v vs %+v", a.Counts, b.Counts)
	}
	if a.Counts.Train == 0 || a.Counts.Val == 0 || a.Counts.Test == 0 {
		t.Fatalf("a three-way split with an empty side is not a split: %+v", a.Counts)
	}

	left := exported(t, app, "one", "d")
	right := exported(t, app, "two", "d")
	if len(left) != len(right) {
		t.Fatalf("%d rows vs %d", len(left), len(right))
	}
	for i := range left {
		if left[i].ID != right[i].ID || left[i].Split != right[i].Split {
			t.Fatalf("row %d differs: %+v vs %+v", i, left[i], right[i])
		}
	}
}

// TestADifferentSeedIsADifferentMembership proves the seed is load-bearing rather
// than decorative: under a cap that forces a sample, two seeds admit different
// subjects and therefore produce different digests.
func TestADifferentSeedIsADifferentMembership(t *testing.T) {
	f := &fake{}
	twin(f, "one")
	twin(f, "two")
	app := mountHTTP(t, newPlane(f))

	capped := declared("d")
	capped.Rows = 20 // the surface holds 60, so the plane must sample
	capped.Seed = "seed-a"
	a := build(t, app, "one", capped)
	capped.Seed = "seed-b"
	b := build(t, app, "two", capped)

	if a.Status != statusReady || b.Status != statusReady {
		t.Fatalf("%q %q", a.Refusal, b.Refusal)
	}
	if a.Share >= shareDenominator || b.Share >= shareDenominator {
		t.Fatalf("a capped window must sample: shares %d and %d", a.Share, b.Share)
	}
	if a.Digest == b.Digest {
		t.Fatal("two seeds selected the same membership; the seed is not reaching the store")
	}
	left, right := subjectsOf(exported(t, app, "one", "d")), subjectsOf(exported(t, app, "two", "d"))
	if equalSets(left, right) {
		t.Fatalf("two seeds admitted the same subjects: %v", left)
	}
}

// TestNoSubjectStraddlesASplit is the entity-leak guard, asserted as a property
// of the published rows rather than of the function that assigned them.
func TestNoSubjectStraddlesASplit(t *testing.T) {
	f := &fake{}
	twin(f, "one")
	app := mountHTTP(t, newPlane(f))
	build(t, app, "one", declared("d"))

	where := map[string]string{}
	for _, r := range exported(t, app, "one", "d") {
		key := r.Kind + "\x00" + r.Subject
		if w, seen := where[key]; seen && w != r.Split {
			t.Fatalf("subject %q is in both %q and %q", r.Subject, w, r.Split)
		}
		where[key] = r.Split
	}
	if len(where) == 0 {
		t.Fatal("nothing was exported")
	}
}

// ── immutability ─────────────────────────────────────────────────────────────

// TestAPublishedVersionCannotBeMutated is the immutability proof, at both layers.
//
// At the DOOR: a ready version refuses materialisation, and so does a version
// whose earlier attempt did not complete.
//
// At the ENGINE: writing a lower-ranked lifecycle row for a published version —
// which is what a stale retry, a replayed message or a confused second process
// would produce — does not displace it, because `ready` is the greatest value of
// the ReplacingMergeTree version column. The fake reproduces exactly that
// collapse rule, so this asserts the property the real engine gives.
func TestAPublishedVersionCannotBeMutated(t *testing.T) {
	f := &fake{}
	twin(f, "one")
	p := newPlane(f)
	app := mountHTTP(t, p)

	first := build(t, app, "one", declared("d"))
	if first.Version != 1 || first.Status != statusReady {
		t.Fatalf("want ready version 1, got %d %q", first.Version, first.Status)
	}

	// The door refuses a second materialisation of a published version.
	code, body := do(t, app, http.MethodPost, "/v1/ml/datasets/d/materialize", "one", nil)
	if code != http.StatusConflict {
		t.Fatalf("re-materialising a published version: want 409, got %d (%s)", code, body)
	}
	if !strings.Contains(string(body), "immutable") {
		t.Fatalf("the refusal does not say why: %s", body)
	}

	// The engine refuses to let a lower-ranked write displace it. Every stage
	// below the terminal one is tried.
	k, _ := tenant.Mint(brand, "one")
	for _, stale := range []string{statusDeclared, statusMaterialize, statusRefused} {
		e := entry{
			Name: "d", Version: first.Version, At: time.Now().UTC().Add(time.Hour),
			By: "someone-else", Status: stale, Refusal: "forged", Digest: "0000",
			Spec: record{Name: "d", Dims: []string{"events"}, From: "x", To: "y", Cuts: []string{"a", "b"}},
		}
		if err := p.put(t.Context(), k, e); err != nil {
			t.Fatalf("put %s: %v", stale, err)
		}
		got, ok, err := p.version(t.Context(), k, "d", first.Version)
		if err != nil || !ok {
			t.Fatalf("read back after a %s write: %v", stale, err)
		}
		if got.Status != statusReady || got.Digest != first.Digest {
			t.Fatalf("a %s write displaced the published version: status %q digest %q",
				stale, got.Status, got.Digest)
		}
	}

	// And the bytes are still the bytes: the export is unchanged.
	after := exported(t, app, "one", "d")
	if len(after) != first.Counts.Rows {
		t.Fatalf("the published rows changed: %d, want %d", len(after), first.Counts.Rows)
	}

	// A NEW version is the only way forward, and it is a different version.
	second := build(t, app, "one", declared("d"))
	if second.Version != 2 {
		t.Fatalf("the next declaration is version %d, want 2", second.Version)
	}
	if again := describeVersion(t, app, "one", "d", 1); again.Digest != first.Digest {
		t.Fatalf("version 1's digest moved when version 2 was published: %s -> %s", first.Digest, again.Digest)
	}
}

// TestAnIncompleteAttemptIsNeverReadable: a version whose materialisation was
// recorded and never finished is not `ready`, cannot be exported, and cannot be
// re-attempted — because a second run under one version number would union two
// runs' rows and make the digest a lie.
func TestAnIncompleteAttemptIsNeverReadable(t *testing.T) {
	f := &fake{}
	twin(f, "one")
	p := newPlane(f)
	app := mountHTTP(t, p)

	if code, body := do(t, app, http.MethodPost, "/v1/ml/datasets", "one", declared("d")); code != http.StatusOK {
		t.Fatalf("declare: %d (%s)", code, body)
	}
	// The attempt is recorded, and then the process dies — which is exactly the
	// register row a crash leaves behind.
	k, _ := tenant.Mint(brand, "one")
	e, ok, err := p.latest(t.Context(), k, "d")
	if err != nil || !ok {
		t.Fatalf("latest: %v", err)
	}
	e.Status = statusMaterialize
	if err := p.put(t.Context(), k, e); err != nil {
		t.Fatal(err)
	}

	code, body := do(t, app, http.MethodPost, "/v1/ml/datasets/d/materialize", "one", nil)
	if code != http.StatusConflict {
		t.Fatalf("re-attempting an incomplete version: want 409, got %d (%s)", code, body)
	}
	if !strings.Contains(string(body), "did not complete") {
		t.Fatalf("the refusal does not name the state: %s", body)
	}
	if code, body := do(t, app, http.MethodGet, "/v1/ml/datasets/d/export", "one", nil); code != http.StatusConflict {
		t.Fatalf("exporting an unpublished version: want 409, got %d (%s)", code, body)
	}
}

// ── durability ───────────────────────────────────────────────────────────────

// TestARestartChangesNothing is the durability proof for a plane that must
// survive `strategy: Recreate` at one replica.
//
// A SECOND plane over the same store — a different process, with an empty
// in-flight map and no cached anything — answers identically. It holds no dataset
// state to lose, which is the whole reason the register lives in the store.
func TestARestartChangesNothing(t *testing.T) {
	f := &fake{}
	twin(f, "one")
	before := mountHTTP(t, newPlane(f))
	built := build(t, before, "one", declared("d"))

	after := mountHTTP(t, newPlane(f))
	got := describeVersion(t, after, "one", "d", built.Version)
	if got.Digest != built.Digest || got.Status != statusReady || got.Counts != built.Counts {
		t.Fatalf("a restarted plane answers differently:\n  before %+v\n  after  %+v", built, got)
	}
	if len(exported(t, after, "one", "d")) != built.Counts.Rows {
		t.Fatal("the rows did not survive the restart")
	}
	// And the new process is not holding a stale in-flight claim from the old one.
	if _, running := newPlane(f).running(mustKey("one")); running {
		t.Fatal("a fresh plane claims a materialisation it never started")
	}
}

// TestAnUnreachableStoreRefusesRatherThanAnswersEmpty. An empty list on a dead
// store reads exactly like a tenant that has declared nothing, and only one of
// those is true.
func TestAnUnreachableStoreRefusesRatherThanAnswersEmpty(t *testing.T) {
	f := &fake{down: true}
	app := mountHTTP(t, newPlane(f))
	code, body := do(t, app, http.MethodGet, "/v1/ml/datasets", "one", nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 from a dead store, got %d (%s)", code, body)
	}
	if strings.Contains(string(body), `"items":[]`) {
		t.Fatalf("a dead store answered with an empty list: %s", body)
	}
}

// TestTheStoresOwnWordsNeverReachTheCaller. zip's default handler puts an
// unrecognised error's own text in the response body, and a driver message names
// the statement, the table and the host. Every store failure is one fact — the
// warehouse did not answer — so the caller gets that, and the operator's log gets
// the rest.
func TestTheStoresOwnWordsNeverReachTheCaller(t *testing.T) {
	const driver = "clickhouse [execute]: code: 60, DB::Exception: Table hanzo.risk_dataset does not exist (host datastore.hanzo.svc:9000)"
	f := &fake{fail: errors.New(driver)}
	app := mountHTTP(t, newPlane(f))

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/ml/datasets"},
		{http.MethodGet, "/v1/ml/datasets/d"},
		{http.MethodGet, "/v1/ml/datasets/d/export"},
		{http.MethodGet, "/v1/ml/datasets/d/lineage"},
		{http.MethodDelete, "/v1/ml/datasets/d"},
		{http.MethodPost, "/v1/ml/datasets/d/materialize"},
	} {
		code, body := do(t, app, tc.method, tc.path, "one", nil)
		if code == http.StatusOK {
			t.Errorf("%s %s answered 200 over a store that could not read", tc.method, tc.path)
		}
		for _, leaked := range []string{"clickhouse", "DB::Exception", "risk_dataset", "9000", "host"} {
			if strings.Contains(strings.ToLower(string(body)), strings.ToLower(leaked)) {
				t.Errorf("%s %s published %q from the driver: %s", tc.method, tc.path, leaked, body)
			}
		}
	}
	// A declare fails the same way, and writes nothing.
	code, body := do(t, app, http.MethodPost, "/v1/ml/datasets", "one", declared("d"))
	if code == http.StatusOK || strings.Contains(string(body), "clickhouse") {
		t.Fatalf("declare over a failing store: %d (%s)", code, body)
	}
}

// TestARowFromAnotherTenantIsRefusedNotFiltered. Every statement already binds
// the tenant; this is the OTHER half — the check on the way out, which is the half
// that survives someone editing a WHERE clause.
//
// A store that returned a foreign row is a store this plane must stop reading,
// not one to quietly clean up after: filtering would hide a defect that is, by
// definition, already cross-tenant. And the refusal must say nothing, because
// what it knows is another tenant's.
func TestARowFromAnotherTenantIsRefusedNotFiltered(t *testing.T) {
	f := &fake{}
	surface(f, "globex", 6, 4)
	app := mountHTTP(t, newPlane(f))
	if got := build(t, app, "globex", declared("secrets")); got.Status != statusReady {
		t.Fatalf("refused: %s", got.Refusal)
	}

	// The store starts handing back rows the predicate excluded.
	f.mu.Lock()
	f.leak = true
	f.mu.Unlock()

	code, body := do(t, app, http.MethodGet, "/v1/ml/datasets", "acme", nil)
	if code == http.StatusOK {
		t.Fatalf("acme was served globex's register: %s", body)
	}
	for _, leaked := range []string{"secrets", "globex"} {
		if strings.Contains(string(body), leaked) {
			t.Fatalf("the refusal names %q, which is not acme's to learn: %s", leaked, body)
		}
	}
}

// ── the maturity horizon ─────────────────────────────────────────────────────

// TestTheHorizonExcludesTheImmatureTail is R1, asserted directly: rows younger
// than the horizon are not admitted, so a label that could not have existed at
// scoring time cannot reach the training set.
func TestTheHorizonExcludesTheImmatureTail(t *testing.T) {
	f := &fake{}
	now := time.Now().UTC().Truncate(time.Hour)
	k := mustKey("one")
	// Forty daily buckets ending now. A five-day horizon must admit the
	// thirty-five that have matured and none of the five that have not.
	const days, horizon = 40, 5
	// Half-day offsets so no bucket sits ON the maturity boundary: the plane reads
	// the clock a moment after this test does, and a bucket exactly at the cut
	// would flip on that difference and make the assertion flaky rather than wrong.
	at := func(i int) time.Time { return now.Add(-time.Duration(days-i)*24*time.Hour - 12*time.Hour) }
	mature := now.Add(-horizon * 24 * time.Hour)
	want := 0
	for i := range days {
		if at(i).Before(mature) {
			want++
		}
		f.feature = append(f.feature, featRow{
			Org: k.String(), Kind: kindPerson,
			Subject: fmt.Sprintf("s%02d", i),
			Bucket:  at(i),
			Value:   map[string]float64{"events": 1, "sessions": 1, "distincts": 1, "paths": 1, "errors": 0, "calls": 0, "failures": 0, "tokens": 0, "spend_nano": 0, "ips": 1},
		})
	}
	app := mountHTTP(t, newPlane(f))

	in := mlDatasetSpec{
		Name: "mature", Kind: kindPerson,
		From:    now.Add(-(days + 1) * 24 * time.Hour).Format(time.RFC3339),
		To:      now.Add(time.Hour).Format(time.RFC3339),
		Horizon: horizon,
		Seed:    "s",
	}
	got := build(t, app, "one", in)
	if got.Status != statusReady {
		t.Fatalf("refused: %s", got.Refusal)
	}
	if got.Counts.Rows != want {
		t.Fatalf("the horizon admitted %d rows, want the %d that have matured", got.Counts.Rows, want)
	}
	for _, r := range exported(t, app, "one", "mature") {
		at, err := time.Parse(time.RFC3339, r.At)
		if err != nil {
			t.Fatal(err)
		}
		if !at.Before(mature) {
			t.Fatalf("row %s at %s is younger than the %d-day horizon", r.ID, r.At, horizon)
		}
	}
}

// TestAWindowYoungerThanItsHorizonIsRefusedAtTheDoor: saying so at declare time
// beats a ready dataset with zero rows, which looks like a tenant with no activity.
func TestAWindowYoungerThanItsHorizonIsRefusedAtTheDoor(t *testing.T) {
	app := mountHTTP(t, newPlane(&fake{}))
	now := time.Now().UTC()
	in := mlDatasetSpec{
		Name: "young", From: now.Add(-2 * time.Hour).Format(time.RFC3339),
		To: now.Format(time.RFC3339), Horizon: 30,
	}
	code, body := do(t, app, http.MethodPost, "/v1/ml/datasets", "one", in)
	if code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", code, body)
	}
	if !strings.Contains(string(body), "maturity horizon") {
		t.Fatalf("the refusal does not name the horizon: %s", body)
	}
}

// ── lineage ──────────────────────────────────────────────────────────────────

// TestLineageIsMeasuredNotAsserted: the plane re-asks the source and reports what
// it finds, so a source that has since expired the window makes Reproducible
// false with a reason instead of leaving a claim nobody can check.
func TestLineageIsMeasuredNotAsserted(t *testing.T) {
	f := &fake{ttl: "TTL bucket + toIntervalDay(400)"}
	twin(f, "one")
	app := mountHTTP(t, newPlane(f))
	built := build(t, app, "one", declared("d"))

	got := lineageOf(t, app, "one", "d", built.Version)
	if !got.Reproducible {
		t.Fatalf("the source is intact and lineage says otherwise: %s", got.Refusal)
	}
	if got.Retention != "TTL bucket + toIntervalDay(400)" {
		t.Fatalf("the source's retention was not read from the store: %q", got.Retention)
	}
	if got.Holds != got.Rows {
		t.Fatalf("the source holds %d and the version was built from %d", got.Holds, got.Rows)
	}

	// Expire the source's older half; the dataset still holds its own rows and
	// says plainly that they can no longer be re-derived.
	f.mu.Lock()
	kept := f.feature[:0:0]
	for _, r := range f.feature {
		if r.Bucket.After(origin0.Add(30 * time.Hour)) {
			kept = append(kept, r)
		}
	}
	f.feature = kept
	f.mu.Unlock()

	got = lineageOf(t, app, "one", "d", built.Version)
	if got.Reproducible {
		t.Fatal("lineage claims reproducible over a source that has expired the window")
	}
	if got.Refusal == "" {
		t.Fatal("lineage refuses reproducibility without saying why")
	}
	if len(exported(t, app, "one", "d")) != built.Counts.Rows {
		t.Fatal("an expired source took the dataset's own rows with it")
	}
}

// ── bounds ───────────────────────────────────────────────────────────────────

// TestOneMaterializationPerOrg is the R5 gate: a tenant looping the op spends one
// warehouse scan, not a thousand.
func TestOneMaterializationPerOrg(t *testing.T) {
	f := &fake{}
	twin(f, "one")
	p := newPlane(f)
	app := mountHTTP(t, p)

	if code, body := do(t, app, http.MethodPost, "/v1/ml/datasets", "one", declared("d")); code != http.StatusOK {
		t.Fatalf("declare: %d (%s)", code, body)
	}
	// Hold the slot the way a running job does, then ask again.
	if _, err := p.claim(mustKey("one"), "d", 1); err != nil {
		t.Fatalf("the slot was already held: %v", err)
	}
	code, body := do(t, app, http.MethodPost, "/v1/ml/datasets/d/materialize", "one", nil)
	if code != http.StatusConflict {
		t.Fatalf("a second materialisation: want 409, got %d (%s)", code, body)
	}
	if !strings.Contains(string(body), "one at a time") {
		t.Fatalf("the refusal does not name the bound: %s", body)
	}
	p.release(mustKey("one"))

	// Another org is unaffected: the gate is per tenant, not global.
	surface(f, "two", 3, 3)
	if got := build(t, app, "two", declared("d")); got.Status != statusReady {
		t.Fatalf("one org's in-flight job blocked another: %s", got.Refusal)
	}
}

// TestTheProcessIsBoundedAcrossTenantsToo is the other half of R5, and the half a
// per-tenant limit cannot give.
//
// One-per-tenant means a tenant cannot spend the plane on itself. It says nothing
// about maxJobs+1 DIFFERENT tenants each holding their own single slot, which is
// that many concurrent scans of a store they all share and that many datasets
// resident in one process. Here the ceiling is filled by other tenants and the
// next one is refused 503 — a retryable statement that the plane is full, told
// apart from the 409 that says the caller's own job is running.
func TestTheProcessIsBoundedAcrossTenantsToo(t *testing.T) {
	f := &fake{}
	p := newPlane(f)
	app := mountHTTP(t, p)

	for i := range maxJobs {
		if _, err := p.claim(mustKey(fmt.Sprintf("org%02d", i)), "d", 1); err != nil {
			t.Fatalf("filling slot %d: %v", i, err)
		}
	}
	// A tenant holding no slot of its own still cannot start one: the resource it
	// would spend is the plane's, not its own.
	surface(f, "late", 3, 3)
	if code, body := do(t, app, http.MethodPost, "/v1/ml/datasets", "late", declared("d")); code != http.StatusOK {
		t.Fatalf("declare: %d (%s)", code, body)
	}
	code, body := do(t, app, http.MethodPost, "/v1/ml/datasets/d/materialize", "late", nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("a full plane: want 503, got %d (%s)", code, body)
	}
	if strings.Contains(string(body), "one at a time") {
		t.Fatalf("a full plane blamed the caller's own job: %s", body)
	}

	// The refusal moved nothing: version 1 is still `declared`, so once a slot
	// frees the SAME version materialises. A refusal that had recorded an attempt
	// would have left this caller a version it could never build, and the ceiling
	// would destroy work rather than defer it.
	p.release(mustKey("org00"))
	if code, body := do(t, app, http.MethodPost, "/v1/ml/datasets/d/materialize", "late", nil); code != http.StatusAccepted {
		t.Fatalf("materialising once the plane freed: want 202, got %d (%s)", code, body)
	}
	got := settled(t, app, "late", "d")
	if got.Status != statusReady || got.Version != 1 {
		t.Fatalf("want ready version 1, got %d %q (%s)", got.Version, got.Status, got.Refusal)
	}
}

// TestADeadStoreIsNeverReportedAsTheCallersMistake. An export that cannot reach
// the warehouse is a 503; only an unknown split is a 400. Deriving one error from
// another's text collapses the two and tells a caller to fix a request that was
// fine.
func TestADeadStoreIsNeverReportedAsTheCallersMistake(t *testing.T) {
	f := &fake{}
	twin(f, "one")
	app := mountHTTP(t, newPlane(f))
	build(t, app, "one", declared("d"))

	if code, body := do(t, app, http.MethodGet, "/v1/ml/datasets/d/export?split=holdout", "one", nil); code != http.StatusBadRequest {
		t.Fatalf("an unknown split: want 400, got %d (%s)", code, body)
	}

	f.mu.Lock()
	f.down = true
	f.mu.Unlock()
	code, body := do(t, app, http.MethodGet, "/v1/ml/datasets/d/export?split=train", "one", nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("an export over a dead store: want 503, got %d (%s)", code, body)
	}
	if strings.Contains(string(body), "split") {
		t.Fatalf("a dead store was reported as a bad split: %s", body)
	}
}

// TestTheRowCapBindsAndTheVersionSaysSo.
func TestTheRowCapBindsAndTheVersionSaysSo(t *testing.T) {
	f := &fake{}
	twin(f, "one")
	app := mountHTTP(t, newPlane(f))
	in := declared("d")
	in.Rows = 12
	got := build(t, app, "one", in)
	if got.Status != statusReady {
		t.Fatalf("refused: %s", got.Refusal)
	}
	if got.Counts.Rows > 12 {
		t.Fatalf("the cap did not bind: %d rows for a cap of 12", got.Counts.Rows)
	}
	if got.Share >= shareDenominator {
		t.Fatalf("a capped window reports a full share: %d", got.Share)
	}
	// Whatever was admitted is still subject-coherent — the trailing partial
	// subject of a truncated read is dropped whole.
	where := map[string]string{}
	for _, r := range exported(t, app, "one", "d") {
		if w, seen := where[r.Subject]; seen && w != r.Split {
			t.Fatalf("truncation split subject %q", r.Subject)
		}
		where[r.Subject] = r.Split
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// twin writes a surface that is identical for every org that gets it: the same
// subject ids, buckets and coordinates. It is what makes the determinism test a
// test of the plane and not of the data.
func twin(f *fake, org string) {
	k, err := tenant.Mint(brand, org)
	if err != nil {
		panic(err)
	}
	for i := range 20 {
		for j := range 3 {
			f.feature = append(f.feature, featRow{
				Org: k.String(), Kind: kindPerson,
				Subject: fmt.Sprintf("s%03d", i),
				Bucket:  origin0.Add(time.Duration(i*3+j) * time.Hour),
				Value: map[string]float64{
					"events": float64(i + 1), "sessions": 1, "distincts": 1, "paths": float64(j + 1),
					"errors": 0, "calls": 1, "failures": 0, "tokens": 10, "spend_nano": 5, "ips": 1,
				},
			})
		}
	}
}

func mustKey(org string) tenant.Key {
	k, err := tenant.Mint(brand, org)
	if err != nil {
		panic(err)
	}
	return k
}

func exported(t *testing.T, app *zip.App, org, name string) []mlDatasetRow {
	t.Helper()
	code, body := do(t, app, http.MethodGet, "/v1/ml/datasets/"+name+"/export?limit=5000", org, nil)
	if code != http.StatusOK {
		t.Fatalf("export %s: %d (%s)", name, code, body)
	}
	var page mlDatasetRows
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("export %s: %v", name, err)
	}
	return page.Rows
}

func describeVersion(t *testing.T, app *zip.App, org, name string, version int) mlDataset {
	t.Helper()
	code, body := do(t, app, http.MethodGet, "/v1/ml/datasets/"+name, org, nil)
	if code != http.StatusOK {
		t.Fatalf("describe %s: %d (%s)", name, code, body)
	}
	var v mlDatasetVersions
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	for _, e := range v.Items {
		if e.Version == version {
			return e
		}
	}
	t.Fatalf("version %d of %s is not in the description", version, name)
	return mlDataset{}
}

func lineageOf(t *testing.T, app *zip.App, org, name string, version int) mlLineage {
	t.Helper()
	code, body := do(t, app, http.MethodGet,
		fmt.Sprintf("/v1/ml/datasets/%s/lineage?version=%d", name, version), org, nil)
	if code != http.StatusOK {
		t.Fatalf("lineage %s: %d (%s)", name, code, body)
	}
	var out mlLineage
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func subjectsOf(rows []mlDatasetRow) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Subject] = true
	}
	return out
}

func equalSets(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
