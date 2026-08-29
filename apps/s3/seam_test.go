package s3

// A route is one of the ways into an object-storage operation and not the only
// one. zip records a typed op and its route's fiber handler as two fields of one
// entry and wraps only the second, so a gate handed to Group or composed through
// With runs for REST and for nothing else — while MCP, the call plane and the
// graph invoke the op directly and the depth-0 identity middleware has already
// authenticated whoever is calling.
//
// These tests drive the SAME operation two ways and require the same money:
// admitted once, metered once, refused identically. Every row is PAIRED — a
// refusal is asserted beside the call that must get through — so a suite that
// only asked "did something refuse" cannot pass against an operation that
// refuses everything.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/apps/metering"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// testCSRFKey is the shared anti-forgery key this test binary holds. admit asks
// account's control, and account refuses to mount a verifier that invented its own
// key — in production the value comes from KMS on the pod, and here from one
// constant, so the mint and the verify inside this process agree.
const testCSRFKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// emptyBuckets is what the object store answers a ListBuckets with: a caller
// with no buckets, which is a SUCCESS and therefore the answer that must bill.
const emptyBuckets = `<?xml version="1.0" encoding="UTF-8"?>` +
	`<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
	`<Owner><ID>t</ID><DisplayName>t</DisplayName></Owner><Buckets></Buckets>` +
	`</ListAllMyBucketsResult>`

// seamApp mounts the real /v1/s3 surface — the real Mount, so the registrations
// under test are the ones that ship — with cloud.Bridge installed as serve.go
// installs it, which is what parks the request every seam reads its caller from.
// reached counts what the object store was asked, so "refused with nothing done"
// is measured rather than assumed.
func seamApp(t *testing.T, commerceURL string) (*zip.App, *int32) {
	t.Helper()
	var reached int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&reached, 1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, emptyBuckets)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(account.KeyEnv, testCSRFKey)
	t.Setenv("S3_ADMIN_ACCESS_KEY", "AKIATEST")
	t.Setenv("S3_ADMIN_SECRET_KEY", "secrettest")
	t.Setenv("S3_ADMIN_ENDPOINT", srv.Listener.Addr().String())
	m, err := metering.New(metering.Config{BaseURL: commerceURL, Token: "svc-token", Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("seam"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{Metering: m, Env: "mainnet"}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app, &reached
}

// byRoute drives the operation the way a browser does.
func byRoute(t *testing.T, app *zip.App, org, user string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/s3/buckets", nil)
	return send(t, app, req, org, user)
}

// byName drives the SAME operation the way MCP addresses it — by the op's own id,
// at a path under /mcp that is not /v1/s3/*. Nothing installed on this
// subsystem's group runs there, so only what the operation does for itself answers.
func byName(t *testing.T, app *zip.App, org, user string) (int, string) {
	t.Helper()
	frame := `{"jsonrpc":"2.0","id":1,"method":"tools/call",` +
		`"params":{"name":"get_s3_buckets","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(frame)))
	req.Header.Set("Content-Type", "application/json")
	return send(t, app, req, org, user)
}

// onPlane drives it over the call plane, whose whole input is the body and whose
// address is the op's name. A bodyless call is the shape a cross-site page can
// send, so it is the one worth measuring.
func onPlane(t *testing.T, app *zip.App, org, user string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, zip.CallPath+"get_s3_buckets", nil)
	return send(t, app, req, org, user)
}

func send(t *testing.T, app *zip.App, req *http.Request, org, user string) (int, string) {
	t.Helper()
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// answered reports whether an MCP frame carries the operation's answer rather
// than a refusal. tools/call always answers 200; the error lives in the envelope.
func answered(body string) bool {
	var f struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &f); err != nil {
		return false
	}
	return len(f.Error) == 0 && !f.Result.IsError
}

// TestTheMoneyRidesEveryWayIn. The same operation, over the route and by name,
// each admitted once and each billed once. PAIRED with the route so a by-name row
// that measured nothing would be visible: if the operation could not be reached
// at all by name, its debit count would match a refusal's.
func TestTheMoneyRidesEveryWayIn(t *testing.T) {
	bs := &billServer{available: 100000}
	app, reached := seamApp(t, bs.start(t))

	if st, body := byRoute(t, app, "acme", "u-acme"); st != http.StatusOK {
		t.Fatalf("route: %d %s, want 200", st, body)
	}
	if !waitFor(func() bool { return bs.debits() == 1 }) {
		t.Fatalf("debits after the route = %d, want 1", bs.debits())
	}
	if org, _ := bs.lastDebit(); org != "acme" {
		t.Fatalf("route debited %q, want the caller org", org)
	}

	st, body := byName(t, app, "acme", "u-acme")
	if st != http.StatusOK || !answered(body) {
		t.Fatalf("by name: %d %s — the operation must be reachable by name, "+
			"or the debit assertion below measures a refusal", st, body)
	}
	if !waitFor(func() bool { return bs.debits() == 2 }) {
		t.Fatalf("debits after the by-name call = %d, want 2 — the operation ran off the "+
			"route and was not billed, which is free work", bs.debits())
	}
	org, raw := bs.lastDebit()
	if org != "acme" {
		t.Fatalf("by-name debit went to %q, want the caller org", org)
	}
	var u struct {
		Amount   int64  `json:"amount"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	_ = json.Unmarshal(raw, &u)
	if u.Provider != "s3" || u.Model != "op" || u.Amount != cloud.DefaultResourceFeeCents {
		t.Errorf("by-name debit = %s/%s %d, want s3/op %d — the two ways in must bill the same thing",
			u.Provider, u.Model, u.Amount, cloud.DefaultResourceFeeCents)
	}
	if got := atomic.LoadInt32(reached); got != 2 {
		t.Errorf("the store was asked %d times for 2 successful operations", got)
	}

	// And the plane, whose input is its body: a bodyless call either reaches the
	// operation, in which case it is billed like the other two, or it does not
	// reach it, in which case nothing is billed. Never the third thing.
	before := bs.debits()
	stp, bodyp := onPlane(t, app, "acme", "u-acme")
	ran := stp == http.StatusOK || stp == http.StatusNoContent
	if ran && !waitFor(func() bool { return bs.debits() == before+1 }) {
		t.Fatalf("the plane answered %d (%s) and billed nothing", stp, bodyp)
	}
	if !ran && !waitFor(func() bool { return bs.debits() != before }) && bs.debits() != before {
		t.Fatalf("the plane refused with %d and billed anyway", stp)
	}
}

// TestTheBalanceIsAskedOnEveryWayIn. An unfunded org is refused by name exactly
// as it is on the route, with the store never asked. PAIRED: the funded call
// above proves this refusal is the balance and not unreachability.
func TestTheBalanceIsAskedOnEveryWayIn(t *testing.T) {
	bs := &billServer{available: 0}
	app, reached := seamApp(t, bs.start(t))

	if st, _ := byRoute(t, app, "acme", "u-acme"); st != http.StatusPaymentRequired {
		t.Fatalf("route with no balance = %d, want 402", st)
	}
	st, body := byName(t, app, "acme", "u-acme")
	if answered(body) {
		t.Fatalf("by name with no balance answered the operation: %d %s", st, body)
	}
	if !strings.Contains(body, "credits") {
		t.Errorf("by-name refusal does not speak of money: %s — an operation refused for "+
			"some other reason would pass this test with the balance gate deleted", body)
	}
	if got := atomic.LoadInt32(reached); got != 0 {
		t.Fatalf("the store was asked %d times for two refused operations, want 0", got)
	}
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for refused operations, want 0", bs.debits())
	}
}

// TestTheTenantIsAskedOnEveryWayIn. An org with no person behind it is not a
// validated principal, by name as on the route.
func TestTheTenantIsAskedOnEveryWayIn(t *testing.T) {
	bs := &billServer{available: 100000}
	app, reached := seamApp(t, bs.start(t))

	if st, _ := byRoute(t, app, "acme", ""); st != http.StatusForbidden {
		t.Fatalf("route with no principal = %d, want 403", st)
	}
	if st, body := byName(t, app, "acme", ""); answered(body) {
		t.Fatalf("by name with no principal answered the operation: %d %s", st, body)
	}
	if got := atomic.LoadInt32(reached); got != 0 {
		t.Fatalf("the store was asked %d times without a principal, want 0", got)
	}
	if bs.debits() != 0 {
		t.Fatalf("debits = %d without a principal, want 0", bs.debits())
	}
}

// === anti-forgery ===========================================================
//
// This plane spends the caller's balance on a READ, so the ordinary reason a read
// needs no token does not hold here: a page the caller never visited can send
// their browser to one of these addresses, the cookie they already hold
// authenticates it, and the debit lands on them. The answer is unreadable
// cross-origin, so nothing leaks — what moves is money.
//
// The control is account's, asked in the operation's own preamble, and it is a
// no-op the moment a caller PRESENTS a credential. These rows are paired around
// that: the same request with a Bearer, and the same request with the token it was
// asked for, both have to get through, or the refusal above would be indistinguishable
// from a surface that refuses everything.

// asVisitor drives a request the way a cross-site page can: the cookie the browser
// already holds, and nothing the page had to be able to read to obtain.
func asVisitor(t *testing.T, app *zip.App, req *http.Request, extra map[string]string) (int, string) {
	t.Helper()
	req.Header.Set("Cookie", "hanzo_iam_token=whatever")
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	return send(t, app, req, "acme", "u-acme")
}

func routeReq() *http.Request { return httptest.NewRequest(http.MethodGet, "/v1/s3/buckets", nil) }

func nameReq() *http.Request {
	frame := `{"jsonrpc":"2.0","id":1,"method":"tools/call",` +
		`"params":{"name":"get_s3_buckets","arguments":{}}}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(frame)))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// TestACookieAloneCannotSpend. On the route and by name alike, a request carrying
// only the ambient cookie is refused before the balance is touched; the same
// request presenting a credential is served and billed.
func TestACookieAloneCannotSpend(t *testing.T) {
	bs := &billServer{available: 100000}
	app, reached := seamApp(t, bs.start(t))

	st, body := asVisitor(t, app, routeReq(), nil)
	if st != http.StatusForbidden {
		t.Fatalf("route on a cookie alone = %d %s, want 403", st, body)
	}
	if _, body = asVisitor(t, app, nameReq(), nil); answered(body) {
		t.Fatalf("by name on a cookie alone answered the operation: %s", body)
	}
	if got := atomic.LoadInt32(reached); got != 0 {
		t.Fatalf("the store was asked %d times on a cookie alone, want 0", got)
	}
	if bs.debits() != 0 {
		t.Fatalf("debits = %d on a cookie alone, want 0 — that is the caller's money", bs.debits())
	}

	// A caller that PRESENTED a credential cannot be forged into, so it pays no
	// price for the control. Both ways in, so neither row above measured
	// unreachability.
	if st, body = asVisitor(t, app, routeReq(), map[string]string{"Authorization": "Bearer t"}); st != http.StatusOK {
		t.Fatalf("route with a bearer = %d %s, want 200 — the control must cost an API client nothing", st, body)
	}
	if _, body = asVisitor(t, app, nameReq(), map[string]string{"Authorization": "Bearer t"}); !answered(body) {
		t.Fatalf("by name with a bearer was refused: %s", body)
	}
	if !waitFor(func() bool { return bs.debits() == 2 }) {
		t.Fatalf("debits = %d after two credentialed calls, want 2", bs.debits())
	}
}

// TestTheTokenTheControlAsksForIsAccepted. The refusal above names a token; this
// obtains that token the way a browser does — from a same-origin response a
// cross-site page cannot read — and requires it to work. Without this row the
// control could be "refuse every cookie", which would be a broken surface rather
// than a defended one.
func TestTheTokenTheControlAsksForIsAccepted(t *testing.T) {
	bs := &billServer{available: 100000}
	app, _ := seamApp(t, bs.start(t))
	if err := account.Use(app, cloud.Deps{Brand: "hanzo"}); err != nil {
		t.Fatalf("Use: %v", err)
	}

	st, body := asVisitor(t, app, httptest.NewRequest(http.MethodGet, "/v1/account/csrf", nil), nil)
	if st != http.StatusOK {
		t.Fatalf("mint token = %d %s, want 200", st, body)
	}
	var mint struct {
		Token string `json:"csrfToken"`
	}
	if err := json.Unmarshal([]byte(body), &mint); err != nil || mint.Token == "" {
		t.Fatalf("no token in %s", body)
	}

	if st, body = asVisitor(t, app, routeReq(), map[string]string{"Sec-Fetch-Site": "same-origin"}); st != http.StatusOK {
		t.Fatalf("route with the minted token = %d %s, want 200", st, body)
	}
	if _, body = asVisitor(t, app, nameReq(), map[string]string{"Sec-Fetch-Site": "same-origin"}); !answered(body) {
		t.Fatalf("by name with the minted token was refused: %s", body)
	}
	if !waitFor(func() bool { return bs.debits() == 2 }) {
		t.Fatalf("debits = %d after two attested calls, want 2", bs.debits())
	}

	// A SIBLING SUBDOMAIN is refused. This stood as "a token minted for somebody
	// else is not this caller's" — identity binding, which was how the old MAC
	// covered a page on another *.hanzo.ai host: such a page CAN set a custom
	// header, so a token was the only thing separating it from the console.
	//
	// Sec-Fetch-Site separates them at the door and needs no token: that page's
	// request says `same-site`, and only the console's says `same-origin`. A
	// legitimate caller from another org is not forgery and is not refused here —
	// the old assertion could not tell those apart because a token was all it had.
	req := httptest.NewRequest(http.MethodGet, "/v1/s3/buckets", nil)
	req.Header.Set("Cookie", "hanzo_iam_token=whatever")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	if st, _ = send(t, app, req, "other", "u-other"); st != http.StatusForbidden {
		t.Fatalf("a sibling *.hanzo.ai page = %d, want 403", st)
	}
}
