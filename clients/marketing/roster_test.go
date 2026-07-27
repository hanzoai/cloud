package marketing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	model "github.com/hanzoai/iam/pkg/model"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// user builds a roster fixture the way IAM returns one: owner is the org, Id is
// the stable opaque subject a token carries.
func user(org, name, id, email string) *model.User {
	u := &model.User{Id: id, Owner: org, Name: name, Email: email}
	return u
}

// fakeRoster swaps the IAM read for a fixed per-org roster and returns a restore
// func. Every test that resolves an audience uses this so no test ever depends on
// a co-mounted IAM.
func fakeRoster(t *testing.T, byOrg map[string][]*model.User) func() {
	t.Helper()
	prev := rosterFn
	rosterFn = func(org string) ([]*model.User, error) { return byOrg[org], nil }
	return func() { rosterFn = prev }
}

// mountRoutes registers the real marketing routes on a real zip app over a fresh
// store, so tests exercise the shipped handlers and paths, not a re-implementation.
func mountRoutes(t *testing.T) (*zip.App, *cloud.Service[state]) {
	t.Helper()
	s := &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.NewNoOpLogger()},
		State: state{store: testStore(t)},
	}
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	routes(app, s)
	return app, s
}

// call issues a request as a VALIDATED principal of org (X-User-Id makes the
// principal validated; X-Org-Id is the org it acts in).
func call(t *testing.T, app *zip.App, method, path, org, body string) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", "z")
	req.Header.Set("X-Org-Id", org)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func num(m map[string]any, k string) int {
	f, _ := m[k].(float64)
	return int(f)
}

// TestResolveWholeOrgRoster is the "email everyone in my org" contract: an
// audience with no event resolves to every mailable customer, de-duplicated, and
// only ever the caller's OWN org.
func TestResolveWholeOrgRoster(t *testing.T) {
	defer fakeRoster(t, map[string][]*model.User{
		"hanzo": {
			user("hanzo", "ada", "id-ada", "ada@hanzo.ai"),
			user("hanzo", "bob", "id-bob", "Bob@Hanzo.ai "),  // normalized
			user("hanzo", "bob2", "id-bob2", "bob@hanzo.ai"), // same mailbox → ONE mail
		},
		"acme": {user("acme", "eve", "id-eve", "eve@acme.com")},
	})()

	r, err := resolveAudience(context.Background(), "hanzo", Audience{Name: "All customers"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := []string{"ada@hanzo.ai", "bob@hanzo.ai"}
	if len(r.addresses) != len(want) {
		t.Fatalf("want %v, got %v", want, r.addresses)
	}
	for i, w := range want {
		if r.addresses[i] != w {
			t.Fatalf("want %v, got %v", want, r.addresses)
		}
	}

	// TENANT ISOLATION: hanzo's audience can never resolve acme's customer.
	for _, a := range r.addresses {
		if strings.HasSuffix(a, "@acme.com") {
			t.Fatalf("cross-tenant leak: hanzo resolved %q", a)
		}
	}
	acme, err := resolveAudience(context.Background(), "acme", Audience{})
	if err != nil {
		t.Fatalf("resolve acme: %v", err)
	}
	if len(acme.addresses) != 1 || acme.addresses[0] != "eve@acme.com" {
		t.Fatalf("acme want [eve@acme.com], got %v", acme.addresses)
	}
}

// TestMatchCohort pins the identity join: a cohort identifier resolves to a
// mailbox by ANY identifier that names the same person, and an identifier that
// names nobody is counted rather than mailed.
func TestMatchCohort(t *testing.T) {
	roster := []*model.User{
		user("hanzo", "ada", "id-ada", "ada@hanzo.ai"),
		user("hanzo", "bob", "id-bob", "bob@hanzo.ai"),
		user("hanzo", "cyd", "id-cyd", "cyd@hanzo.ai"),
	}
	got := matchCohort(roster, []string{
		"id-ada",       // stable opaque subject
		"hanzo/bob",    // owner/name subject
		"CYD",          // bare username, case-insensitive
		"id-ada",       // repeat → one mail
		"anon-9f2c",    // an anonymous visitor: no customer
		"x@nowhere.io", // an address we do not know
	})
	want := []string{"ada@hanzo.ai", "bob@hanzo.ai", "cyd@hanzo.ai"}
	if len(got.addresses) != len(want) {
		t.Fatalf("want %v, got %v", want, got.addresses)
	}
	for i, w := range want {
		if got.addresses[i] != w {
			t.Fatalf("want %v, got %v", want, got.addresses)
		}
	}
	if got.unmatched != 2 {
		t.Fatalf("want 2 unmatched cohort ids, got %d", got.unmatched)
	}
}

// TestResolveFailsClosedWithoutIAM: when the identity store cannot answer, the
// resolution REFUSES and the preview says so. An announcement that silently
// reaches nobody — reported as a success — is the failure this prevents.
// (That iamRoster itself refuses an unmounted IAM is asserted in
// TestRosterReadsTheRealEmbeddedIAM, before it mounts one.)
func TestResolveFailsClosedWithoutIAM(t *testing.T) {
	prev := rosterFn
	rosterFn = func(string) ([]*model.User, error) { return nil, errIAMUnavailable }
	defer func() { rosterFn = prev }()

	if _, err := resolveAudience(context.Background(), "hanzo", Audience{}); err != errIAMUnavailable {
		t.Fatalf("want errIAMUnavailable, got %v", err)
	}
	p := evalAudience(context.Background(), "hanzo", Audience{})
	if p.Available || p.Deliverable != 0 || p.Reason == "" {
		t.Fatalf("preview must be honest-unavailable, got %+v", p)
	}
	// The handler surfaces it as a 503, not a 200 with an empty send.
	if err := mapErr(errIAMUnavailable, ""); err == nil || !strings.Contains(err.Error(), "identity store unavailable") {
		t.Fatalf("want a 503-shaped refusal, got %v", err)
	}
}

// TestAnnounceToEveryCustomer is the END-TO-END product announcement: create a
// whole-org audience, fan it into a one-step sequence over the real /v1 routes,
// and let the drip engine send. The opted-out customer is enrolled like everyone
// else and STILL never reaches the rail — the suppression gate holds on the
// audience path exactly as it does on a hand-typed address.
func TestAnnounceToEveryCustomer(t *testing.T) {
	ctx := context.Background()
	app, s := mountRoutes(t)
	got, restore := captureSends(t)
	defer restore()
	defer fakeRoster(t, map[string][]*model.User{
		"hanzo": {
			user("hanzo", "ada", "id-ada", "ada@hanzo.ai"),
			user("hanzo", "optout", "id-optout", "optout@hanzo.ai"),
			user("hanzo", "cyd", "id-cyd", "cyd@hanzo.ai"),
		},
		"acme": {user("acme", "eve", "id-eve", "eve@acme.com")},
	})()

	// A customer opted out of marketing email.
	if err := s.State.store.Suppress(ctx, Suppression{Org: "hanzo", Channel: "email", Address: "optout@hanzo.ai", CreatedAt: 1}); err != nil {
		t.Fatalf("suppress: %v", err)
	}

	// The audience: every customer (no event filter).
	code, aud := call(t, app, http.MethodPost, "/v1/marketing/audiences", "hanzo", `{"name":"All customers"}`)
	if code != http.StatusCreated {
		t.Fatalf("create audience: %d %v", code, aud)
	}
	audienceID := str(aud, "id")

	// Preview shows exactly who it would reach, before anything is sent.
	code, prev := call(t, app, http.MethodGet, "/v1/marketing/audiences/"+audienceID+"/preview", "hanzo", "")
	if code != http.StatusOK || prev["available"] != true || num(prev, "deliverable") != 3 {
		t.Fatalf("preview want 3 deliverable, got %d (%v)", num(prev, "deliverable"), prev)
	}

	// The announcement: a one-step sequence, sent immediately.
	code, seq := call(t, app, http.MethodPost, "/v1/marketing/sequences", "hanzo", `{"name":"New model available","status":"active"}`)
	if code != http.StatusCreated {
		t.Fatalf("create sequence: %d %v", code, seq)
	}
	seqID := str(seq, "id")
	code, _ = call(t, app, http.MethodPost, "/v1/marketing/sequences/"+seqID+"/steps", "hanzo",
		`{"subject":"Zen 5 is live","body":"A new model is available.","delaySeconds":0}`)
	if code != http.StatusCreated {
		t.Fatalf("add step: %d", code)
	}

	// Fan the audience in — the ONE enroll route, an audience instead of an address.
	code, res := call(t, app, http.MethodPost, "/v1/marketing/sequences/"+seqID+"/enroll", "hanzo",
		`{"audienceId":"`+audienceID+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("enroll audience: %d %v", code, res)
	}
	if num(res, "resolved") != 3 || num(res, "enrolled") != 3 || num(res, "alreadyEnrolled") != 0 {
		t.Fatalf("want resolved=3 enrolled=3 already=0, got %v", res)
	}

	// Send.
	if n, err := processDue(ctx, s, 1<<40, 100); err != nil || n != 3 {
		t.Fatalf("processDue want (3,nil), got (%d,%v)", n, err)
	}

	// THE GATE HELD: the opted-out customer never reached the rail; everyone else did.
	reached := map[string]bool{}
	for _, m := range *got {
		reached[m.to] = true
		if m.org != "hanzo" || m.subject != "Zen 5 is live" {
			t.Fatalf("unexpected delivery %+v", m)
		}
	}
	if reached["optout@hanzo.ai"] {
		t.Fatalf("SUPPRESSION BREACH: an opted-out customer was mailed (%+v)", *got)
	}
	if reached["eve@acme.com"] {
		t.Fatalf("CROSS-TENANT BREACH: another org's customer was mailed (%+v)", *got)
	}
	if !reached["ada@hanzo.ai"] || !reached["cyd@hanzo.ai"] || len(reached) != 2 {
		t.Fatalf("want exactly ada+cyd mailed, got %v", reached)
	}

	// The ledger records the skip as a skip, not a send.
	var status string
	if err := s.State.store.db.QueryRowContext(ctx,
		`SELECT status FROM marketing_sends WHERE address=?`, "optout@hanzo.ai").Scan(&status); err != nil {
		t.Fatalf("read send ledger: %v", err)
	}
	if status != "skipped" {
		t.Fatalf("opted-out send must be recorded skipped, got %q", status)
	}

	// Re-POSTing the same announcement is IDEMPOTENT: nobody is enrolled twice,
	// so nobody is mailed twice.
	code, again := call(t, app, http.MethodPost, "/v1/marketing/sequences/"+seqID+"/enroll", "hanzo",
		`{"audienceId":"`+audienceID+`"}`)
	if code != http.StatusCreated || num(again, "enrolled") != 0 || num(again, "alreadyEnrolled") != 3 {
		t.Fatalf("re-enroll want enrolled=0 already=3, got %d %v", code, again)
	}
	before := len(*got)
	if _, err := processDue(ctx, s, 1<<40, 100); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if len(*got) != before {
		t.Fatalf("second sweep must not re-send, got %+v", (*got)[before:])
	}
}

// TestEnrollAudienceIsOrgScoped: one org can never fan out over another org's
// audience, and a single-address enroll still returns its enrollment id.
func TestEnrollAudienceIsOrgScoped(t *testing.T) {
	app, _ := mountRoutes(t)
	defer fakeRoster(t, map[string][]*model.User{
		"hanzo": {user("hanzo", "ada", "id-ada", "ada@hanzo.ai")},
		"acme":  {user("acme", "eve", "id-eve", "eve@acme.com")},
	})()

	_, aud := call(t, app, http.MethodPost, "/v1/marketing/audiences", "hanzo", `{"name":"All customers"}`)
	audienceID := str(aud, "id")

	// acme creates its own sequence and points it at HANZO's audience id.
	_, seq := call(t, app, http.MethodPost, "/v1/marketing/sequences", "acme", `{"name":"Acme blast","status":"active"}`)
	seqID := str(seq, "id")
	call(t, app, http.MethodPost, "/v1/marketing/sequences/"+seqID+"/steps", "acme", `{"body":"hi"}`)

	code, res := call(t, app, http.MethodPost, "/v1/marketing/sequences/"+seqID+"/enroll", "acme",
		`{"audienceId":"`+audienceID+`"}`)
	if code != http.StatusNotFound {
		t.Fatalf("acme must not reach hanzo's audience: %d %v", code, res)
	}

	// A single named address still works and still returns its enrollment id.
	code, one := call(t, app, http.MethodPost, "/v1/marketing/sequences/"+seqID+"/enroll", "acme",
		`{"address":"eve@acme.com"}`)
	if code != http.StatusCreated || num(one, "enrolled") != 1 || str(one, "enrollmentId") == "" {
		t.Fatalf("single-address enroll: %d %v", code, one)
	}

	// Naming both, or neither, is refused — there is one way to say who.
	for _, body := range []string{`{}`, `{"address":"a@b.com","audienceId":"` + audienceID + `"}`} {
		if code, _ := call(t, app, http.MethodPost, "/v1/marketing/sequences/"+seqID+"/enroll", "acme", body); code != http.StatusBadRequest {
			t.Fatalf("body %s want 400, got %d", body, code)
		}
	}
}
