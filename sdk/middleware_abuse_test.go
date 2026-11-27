package cloud

// The abuse gate's specification. Four groups, each answering one question a
// reviewer will ask:
//
//	POLICY OUTCOMES — does each verdict produce the right wire answer?
//	FAIL POLICY     — does an absent/erroring scorer allow ordinary traffic and
//	                  refuse a privileged grant?
//	TENANT SCOPING  — can one org's traffic reach, move or read another's?
//	AGENCY          — is a customer's automation told apart from a bad bot, and
//	                  is it told apart from FACTS rather than from a header?
//
// Every test drives the middleware through a real zip app and a real request, so
// what is asserted is what a client would see.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// The two deployment shapes a middleware can find itself in. They are the same
// app apart from whether the identity boundary ran, which is exactly the fact the
// gate must turn on.
const (
	withBoundary    = true
	withoutBoundary = false
)

// boundary is the test's identity boundary: it mints the principal from the same
// headers SanitizeIdentity mints it from, and parks it where only a boundary can.
func boundary() zip.Handler {
	return func(c *zip.Ctx) error {
		if u := c.User(); u != "" {
			principal.Mint(c, principal.Principal{Org: c.Org(), User: u})
		} else {
			principal.Mint(c, principal.Principal{})
		}
		return c.Next()
	}
}

// staticModeStore builds a real edge.Store armed to mode — on the platform row
// (the anonymous lane) and on each tenant these tests drive, BY NAME. Mode does
// not inherit: arming one scope arms exactly that scope, which is the property
// TestPolicy_ArmingThePlatformRowDoesNotArmTenants pins. Using the real store
// rather than a fake keeps the test honest about how Mode actually resolves.
func staticModeStore(t *testing.T, mode string) *edge.Store {
	t.Helper()
	s, err := edge.New(t.TempDir(), "admin", edge.Policy{Mode: mode})
	if err != nil {
		t.Fatalf("edge.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if mode != "" {
		for _, org := range []string{"acme", "globex"} {
			if _, err := s.Put(t.Context(), org, edge.Policy{Mode: mode}); err != nil {
				t.Fatalf("arm %s: %v", org, err)
			}
		}
	}
	return s
}

// call drives one request. org is the validated tenant (the header
// SanitizeIdentity mints); cred is the raw credential the caller presents; ip is
// the forwarded client address.
func abuseHit(app *zip.App, method, path, org, cred, ip string) *http.Response {
	req := httptest.NewRequest(method, path, nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	if cred != "" {
		req.Header.Set("Authorization", "Bearer "+cred)
	}
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		return &http.Response{StatusCode: 0}
	}
	return resp
}

func abuseVerdict(action string) RiskScorer {
	return func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		return RiskVerdict{ID: "d-" + action, Action: action, Score: 0.9, Cause: "test"}, nil
	}
}

func abuseBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b := make([]byte, 4096)
	n, _ := resp.Body.Read(b)
	return string(b[:n])
}

// nowish is the clock the sensor tests read. A single call site so a future move
// to an injected clock has one place to change.
func abuseNow() time.Time { return time.Now() }
