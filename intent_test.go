package cloud

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/attest"
	"github.com/zap-proto/zip"
)

const goodKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestTheDialNamesRoots pins what the governed list means: a root covers its
// subtree, on SEGMENT boundaries and in the form the ROUTER matches, so a surface
// cannot be brought under the control — or left out of it — by how it is spelled.
// One capital letter is what turns this class of test from pedantry into the
// difference between fail-closed and fail-open.
func TestTheDialNamesRoots(t *testing.T) {
	for _, in := range []string{"/v1/crm/companies", "/V1/CRM/companies", "/v1/crm", "/v1/crm/"} {
		if !governs(in) {
			t.Errorf("%s answers under a governed root and was not governed", in)
		}
	}
	for _, out := range []string{"/v1/crmx/companies", "/v1/project", "/v1/crm2", "/mcp"} {
		if governs(out) {
			t.Errorf("%s is outside every governed root and was governed", out)
		}
	}
}

// TestAChangeOffTheHTTPPathIsNotForgeable: an in-process invoke has no browser and
// no cookie, so no page can have sent it. Refusing there would refuse the CLI,
// which is the one caller that cannot be tricked into anything.
func TestAChangeOffTheHTTPPathIsNotForgeable(t *testing.T) {
	t.Setenv(attest.KeyEnv, goodKey)
	if err := Intent()(context.Background(), zip.Op{Method: "POST", Path: "/v1/crm/companies"}, nil); err != nil {
		t.Fatalf("an in-process change was refused: %v", err)
	}
}

// TestOnlyAGovernedChangeIsAsked measures the two questions the control asks about
// the OPERATION, without a request: a read and an ungoverned surface must not even
// reach the credential test, so the control costs the rest of the fleet nothing
// while the list is short.
func TestOnlyAGovernedChangeIsAsked(t *testing.T) {
	for _, op := range []zip.Op{
		{Method: "GET", Path: "/v1/crm/companies"},  // a read is not a change
		{Method: "POST", Path: "/v1/project"},      // not yet governed
		{Method: "POST", Path: "/v1/crm/companies"}, // governed, but off the HTTP path
	} {
		if err := Intent()(context.Background(), op, nil); err != nil {
			t.Errorf("%s %s was refused: %v", op.Method, op.Path, err)
		}
	}
}

// TestTheRuleCoversTheGraph closes the seam inventory. A field of the graph
// resolves through op.direct — validate, authorize, the handler — with the
// REQUEST's context, so the control answers there as it does over REST. It is
// measured rather than argued because the graph is the one projection that reaches
// an op WITHOUT a route and without an MCP envelope, and a rule that read the
// transport instead of the operation would miss exactly it.
//
// The op carries an explicit id because the graph names its fields by one and
// SKIPS an op that has none — which is also why crm's own operations, none of
// which declares one, are absent from the graph and reachable only over REST,
// /mcp and the call plane.
func TestTheRuleCoversTheGraph(t *testing.T) {
	t.Setenv(attest.KeyEnv, goodKey)
	ran := 0
	app := zip.New(zip.Config{})
	app.Use(Bridge())
	app.Authorize(Rule(nil, nil))
	sc := newScope(app, "crm", nil)
	zip.Post(sc.Group("/v1/crm"), "/probe",
		func(context.Context, *struct{}) (*struct{ OK bool }, error) {
			ran++
			return &struct{ OK bool }{true}, nil
		},
		zip.WithOperationID("crm_probe"))
	app.MountGraph("/v1/graphql")
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}

	ask := func(hdr map[string]string) string {
		req := httptest.NewRequest("POST", "/v1/graphql",
			strings.NewReader(`{"query":"mutation { crm_probe { OK } }"}`))
		req.Header.Set("Content-Type", "text/plain") // CORS-simple: no preflight to refuse
		req.Header.Set("X-User-Id", "u")
		req.Header.Set("X-Org-Id", "acme")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	if body := ask(map[string]string{"Cookie": "hanzo_iam_token=v"}); !strings.Contains(body, Unasked) {
		t.Errorf("the graph ran a change on an ambient cookie alone: %s", body)
	}
	if ran != 0 {
		t.Errorf("the handler ran %d times for a change nobody asked for", ran)
	}
	if body := ask(map[string]string{"Cookie": "hanzo_iam_token=v", "Sec-Fetch-Site": "same-origin"}); strings.Contains(body, Unasked) {
		t.Errorf("the graph refused the console's own change: %s", body)
	}
	if ran != 1 {
		t.Errorf("the handler ran %d times for a change that was asked for; want 1", ran)
	}
}

// TestTheBrowserSaysWhereAChangeCameFrom is the whole anti-forgery control now.
//
// It replaced a 32-byte key every process had to hold the same copy of. That key
// proved a caller had first read GET /v1/account/csrf from THIS origin —
// which is the fact Sec-Fetch-Site states outright, and states unforgeably: it is
// a forbidden header, so no page can write it.
//
// The case that matters is the one SameSite cannot cover. SameSite is scoped to
// the registrable domain, so a page on any *.hanzo.ai host sends the session
// cookie and its request looks like the console's own. That request carries
// `same-site`, NOT `same-origin`, which is exactly where this refuses.
func TestTheBrowserSaysWhereAChangeCameFrom(t *testing.T) {
	app := zip.New(zip.Config{})
	app.Post("/v1/crm/companies", func(c *zip.Ctx) error {
		if err := Intended(c); err != nil {
			return err
		}
		return c.String(200, "served")
	})

	call := func(hdr map[string]string) int {
		req := httptest.NewRequest("POST", "/v1/crm/companies", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("test request: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	cookie := map[string]string{"Cookie": "session=whatever"}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range cookie {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	// THE ATTACK. A sibling subdomain rides the cookie and looks identical to the
	// console at every layer SameSite can see. The browser still tells us.
	if got := call(with(map[string]string{"Sec-Fetch-Site": "same-site"})); got != 403 {
		t.Errorf("a same-site (sibling subdomain) change = %d, want 403 — "+
			"this is the case SameSite cannot refuse and the whole reason the control exists", got)
	}
	if got := call(with(map[string]string{"Sec-Fetch-Site": "cross-site"})); got != 403 {
		t.Errorf("a cross-site change = %d, want 403", got)
	}

	// THE CONSOLE ITSELF, and a typed URL.
	if got := call(with(map[string]string{"Sec-Fetch-Site": "same-origin"})); got != 200 {
		t.Errorf("the console's own change = %d, want 200", got)
	}
	if got := call(with(map[string]string{"Sec-Fetch-Site": "none"})); got != 200 {
		t.Errorf("a user-initiated navigation = %d, want 200", got)
	}

	// NO SIGNAL AT ALL, with a cookie: refuse. A state change that will not say
	// where it came from is the shape being defended against, so absence is a no.
	if got := call(cookie); got != 403 {
		t.Errorf("a cookie-authenticated change with no Sec-Fetch-Site and no Origin = %d, "+
			"want 403 — absence must fail closed", got)
	}

	// A PRESENTED CREDENTIAL is not forgeable from a page, so it is never asked.
	// This is what keeps the CLI, the SDKs and every machine caller free.
	if got := call(map[string]string{"Authorization": "Bearer sk-whatever", "Sec-Fetch-Site": "cross-site"}); got != 200 {
		t.Errorf("a bearer-presenting caller = %d, want 200 — a page cannot set that header", got)
	}

	// NO COOKIE, no ambient credential, nothing to forge.
	if got := call(map[string]string{}); got != 200 {
		t.Errorf("a request with no ambient credential = %d, want 200", got)
	}
}
