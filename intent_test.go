package cloud

import (
	"context"
	"io"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	for _, out := range []string{"/v1/crmx/companies", "/v1/projects", "/v1/crm2", "/mcp"} {
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
		{Method: "POST", Path: "/v1/projects"},      // not yet governed
		{Method: "POST", Path: "/v1/crm/companies"}, // governed, but off the HTTP path
	} {
		if err := Intent()(context.Background(), op, nil); err != nil {
			t.Errorf("%s %s was refused: %v", op.Method, op.Path, err)
		}
	}
}

// TestADeploymentWithoutTheKeyDoesNotCompose: a process that would refuse every
// change a browser makes says so at BOOT, in one line naming the value to set,
// rather than 403-ing silently for as long as the pod runs. That failure cannot be
// seen from inside one process, so refusing to compose is the only place to catch it.
func TestADeploymentWithoutTheKeyDoesNotCompose(t *testing.T) {
	app := zip.New(zip.Config{})
	sc := newScope(app, "crm", nil)
	zip.Post(sc.Group("/v1/crm"), "/companies",
		func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })

	t.Setenv(attest.KeyEnv, "")
	err := keyed(app, true)
	if err == nil {
		t.Fatal("a deployment serving a governed change composed on a key of its own")
	}
	if !strings.Contains(err.Error(), attest.KeyEnv) || !strings.Contains(err.Error(), "/v1/crm/companies") {
		t.Errorf("the refusal names neither the value to set nor the surface that needs it: %v", err)
	}
	if err := keyed(app, false); err != nil {
		t.Errorf("a machine with no secret store was refused: %v", err)
	}
	t.Setenv(attest.KeyEnv, goodKey)
	if err := keyed(app, true); err != nil {
		t.Errorf("a provisioned deployment was refused: %v", err)
	}
}

// TestAnUngovernedProgramNeedsNoKey: the boot rule is scoped to what the process
// SERVES, so the 100-odd apps outside the governed list keep composing without a
// value they have no use for. It is what makes the rollout increment-sized.
func TestAnUngovernedProgramNeedsNoKey(t *testing.T) {
	app := zip.New(zip.Config{})
	sc := newScope(app, "projects", nil)
	zip.Post(sc.Group("/v1/projects"), "/",
		func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
	t.Setenv(attest.KeyEnv, "")
	if err := keyed(app, true); err != nil {
		t.Errorf("a deployment serving no governed change was refused a key it does not use: %v", err)
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
	tok, _ := attest.Process().Mint("u", "acme")
	if body := ask(map[string]string{"Cookie": "hanzo_iam_token=v", attest.Header: tok}); strings.Contains(body, Unasked) {
		t.Errorf("the graph refused a caller who echoed the token: %s", body)
	}
	if ran != 1 {
		t.Errorf("the handler ran %d times for a change that was asked for; want 1", ran)
	}
}

// TestAKeylessProcessRefusesTheChangeAndServesTheRead is why the eleven mount-time
// account.Shared checks are gone, and it is the property they were mistaken for.
//
// Those checks read the same environment this one does and refused the whole app —
// every route, every caller — when the key was absent. That is not what the control
// protects. Intended consults the key in exactly one of its four branches, and that
// branch already returns 403; the other three answer before the key is ever read.
// A process with no key holds a random one (attest.Process), so it REFUSES every
// token minted elsewhere and can be handed no forgery. Refusing to serve on top of
// that bought nothing and cost the public reads — /v1/billing/plans, /v1/s3/health,
// /v1/code/tree — which answer no token at all.
//
// So: keyless is not keyless-and-open. It is keyless-and-refusing, on the one path
// that asks.
func TestAKeylessProcessRefusesTheChangeAndServesTheRead(t *testing.T) {
	t.Setenv(attest.KeyEnv, "")

	// The RAW-ROUTE shape of the control, which is what apps/account RequireCSRF
	// installs and one line over Intended — the same decision the typed-op
	// authorizer reaches, asked where an HTTP request can actually meet it.
	app := zip.New(zip.Config{})
	app.Post("/v1/crm/companies", func(c *zip.Ctx) error {
		if err := Intended(c); err != nil {
			return err
		}
		return c.String(200, "served")
	})

	// COMPOSES. A process serving a governed change answers to `keyed`, and to
	// nothing else — that is the whole of this change.
	if err := keyed(app, false); err != nil {
		t.Fatalf("a machine with no secret store was refused: %v", err)
	}

	// AND THE AMBIENT CHANGE IS STILL REFUSED. This is the security property the
	// mount checks were credited with and never held: it lives here, per request.
	req := httptest.NewRequest("POST", "/v1/crm/companies", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "session=whatever")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("POST with an ambient cookie and no shared key = %d %q, want 403 — "+
			"a keyless process must refuse the change, not serve it", resp.StatusCode, string(body))
	}

	// AND IT STILL SERVES THE CALLERS THE KEY NEVER GATED. No Cookie means no
	// ambient credential and nothing a page could have sent — the in-cluster hop
	// and the machine caller. The mount checks 503'd these too, which is the cost
	// that bought nothing: websearch's API-key arm, s3's ungated probe, code's
	// free reads and billing's public catalog all live on this branch.
	req = httptest.NewRequest("POST", "/v1/crm/companies", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("POST with no ambient credential and no shared key = %d %q, want 200 — "+
			"a key question got between a machine caller and a surface it does not gate",
			resp.StatusCode, string(body))
	}
}

// TestTheReservedAdminOrgIsNotConfigurable closes the knob SanitizeIdentity says is
// gone. It was gone from that one file; three readers kept it alive, and one of them
// computed the IsSuperAdmin bit from it.
//
// The reserved org is the SuperAdmin predicate — IAM decides `owner == "admin"` and
// authz publishes the constant. A consumer-side value can only ever make cloud
// DISAGREE with the token it is reading, and disagreement in this direction is
// privilege escalation: set it to "hanzo" and every member of the hanzo org holds
// platform sudo here while IAM considers none of them a SuperAdmin.
//
// The fields are DELETED, so the compiler now refuses the escalation and no runtime
// assertion can reach one. What is left to test is that nobody puts the knob back —
// which is a fact about the SOURCE, so that is what this reads. A default that is
// merely correct is one assignment away from wrong; an absent field is not.
func TestTheReservedAdminOrgIsNotConfigurable(t *testing.T) {
	var found []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") || strings.Contains(path, "/testdata/") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "//") || !strings.Contains(t, "IAM_ADMIN_ORG") {
				continue
			}
			found = append(found, path+": "+t)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, f := range found {
		t.Errorf("the reserved admin org is readable from the environment again:\n  %s\n"+
			"it is authz.AdminOrg, the issuer's constant — a consumer-side value only lets "+
			"cloud disagree with the token it reads, and that disagreement is platform sudo", f)
	}
}
