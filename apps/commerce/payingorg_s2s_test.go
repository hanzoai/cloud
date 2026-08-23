// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// A MONEY OP IS REACHED IN TWO SHAPES AND THEY ARE NOT EQUALLY TRUSTED. callerOrg
// is where that distinction lives, and this is the test that it holds.
//
// With a request, the org arrives as a header. The identity boundary strips every
// authority header on ingress and restores that one for the data path even where
// it could not validate the caller — deliberately, on the stated grounds that
// whatever reads it gates on a validated principal. This is code that reads it, so
// this is code that gates: a header alone proves nothing here.
//
// With no request there is no header in play: the org is what plane.For stamped
// in-process, after an endpoint validated it, and zip reads a stated caller only
// on a request-free context.
//
// Both shapes are exercised against BOTH resolvers — callerOrg, which the eight
// ledger ops read by, and payingOrg, which the payment and cart ops read by — so
// neither can drift onto a second rule. Two copies of one rule is two rules, and
// the two ends of that drift fail opposite ways: too strict refuses the trusted
// service that legitimately carries no session, too loose asks nothing of a
// header. One resolver, both shapes, pinned here.

// tenantProbe is the empty In/Out a typed probe op needs.
type tenantProbe struct{}

// THE HTTP SHAPE. A tenant is resolved only for a caller something vouched for.
//
// The gateway is not assumed to be the only ingress — app.go's Identify says so
// plainly, and the identity boundary exists because of it. So "the edge would have
// stripped it" is not what makes a header trustworthy here. This is.
func TestOnARequestOnlyAVouchedOrgResolvesATenant(t *testing.T) {
	const token = "test-commerce-service-token"
	t.Setenv("COMMERCE_SERVICE_TOKEN", token)

	app := zip.New(zip.Config{Logger: luxlog.New("callerorg"), DisableStartupMessage: true})
	app.Use(zip.H(cloud.Bridge()))
	// TYPED ops, because that is the shape these resolvers are actually reached
	// in and it is not interchangeable with a raw route. zip binds the request
	// into the context on TYPED dispatch, so zip.CallerOf reads X-Org-Id off a
	// typed op and reads an empty stated caller off a raw one. A raw-route probe
	// therefore refuses a forged header for the wrong reason — no request found,
	// rather than a gate that held — and passes against code that has no gate at
	// all. The deleted predecessor of this test was raw, which is how it covered
	// this by accident and could not have caught the loosening.
	//
	// Both resolvers on one app, so a case that admits one and refuses the other
	// is a visible disagreement rather than a passing test in the other file.
	zip.Get(app, "/caller", func(ctx context.Context, _ *tenantProbe) (*tenantProbe, error) {
		if _, err := callerOrg(ctx, "probe"); err != nil {
			return nil, err
		}
		return &tenantProbe{}, nil
	}, zip.WithOperationID("probeCallerOrg"))
	zip.Get(app, "/paying", func(ctx context.Context, _ *tenantProbe) (*tenantProbe, error) {
		if _, err := payingOrg(ctx, "probe"); err != nil {
			return nil, err
		}
		return &tenantProbe{}, nil
	}, zip.WithOperationID("probePayingOrg"))

	for _, tc := range []struct {
		what    string
		token   string
		org     string
		refused bool // no tenant resolved
	}{
		// The service path, and it must stay open. `ai` is its own PROCESS, so it
		// cannot reach an in-process reader and asks over HTTP bearing the token and
		// no session. A tenant it cannot resolve is a tier it cannot read, and the
		// safe default for an unreadable tier is the lowest one — so refusing here
		// is not visible as a refusal, only as everyone being on the free tier.
		{"a trusted service naming its org", token, "hanzo", false},
		// Nobody stands behind the org, so there is no tenant to resolve.
		{"no credential at all, client names the org", "", "other-org", true},
		{"a wrong token", "not-the-token", "other-org", true},
		// One byte short. The compare is constant-time and whole-value, so a prefix
		// is as wrong as a random string.
		{"a near-miss token", token[:len(token)-1], "other-org", true},
		// Vouched for, but names nobody. There is no default tenant to fall back on.
		{"a valid token naming no org", token, "", true},
	} {
		for _, path := range []string{"/caller", "/paying"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.org != "" {
				req.Header.Set("X-Org-Id", tc.org)
			}
			resp, err := app.Test(req, zip.TestConfig{Timeout: 30_000_000_000, FailOnTimeout: true})
			if err != nil {
				t.Fatalf("%s %s: %v", tc.what, path, err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			refused := resp.StatusCode == http.StatusForbidden &&
				strings.Contains(string(body), "no validated org on the call")
			if refused != tc.refused {
				t.Errorf("%s %s: status %d %s — want tenant-refused=%v",
					tc.what, path, resp.StatusCode, strings.TrimSpace(string(body)), tc.refused)
			}
		}
	}
}

// THE PLANE SHAPE. No request, so the org is whatever the caller stated — and the
// only thing that states one is an endpoint that already validated it.
//
// payingOrg checks the tenant BEFORE co-residency, which is what makes this
// readable without a commerce embed: unresolved says "no validated org on the
// call", resolved gets past it and says "not co-resident". So the second message
// is the PASS. One refusal for one rule — the shape it was refused in is not
// something the caller needs told, and telling them would be telling them which
// endpoint they reached.
func TestOffARequestTheStatedOrgIsTheTenant(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "test-commerce-service-token")

	for _, tc := range []struct {
		what     string
		ctx      context.Context
		resolved bool
	}{
		{"a caller stating its org", cloud.For(context.Background(), "hanzo"), true},
		{"a caller stating an empty org", cloud.For(context.Background(), ""), false},
		{"no caller at all", context.Background(), false},
	} {
		org, err := callerOrg(tc.ctx, "probe")
		if (err == nil) != tc.resolved {
			t.Errorf("callerOrg %s: %q %v — want resolved=%v", tc.what, org, err, tc.resolved)
		}

		_, err = payingOrg(tc.ctx, "probe")
		if err == nil {
			t.Errorf("payingOrg %s: succeeded with no commerce co-resident", tc.what)
			continue
		}
		if strings.Contains(err.Error(), "no validated org on the call") == tc.resolved {
			t.Errorf("payingOrg %s: %v — want tenant-resolved=%v", tc.what, err, tc.resolved)
		}
	}
}
