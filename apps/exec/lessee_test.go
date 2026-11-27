package exec

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// A code session is its caller's own: every sandbox call states the subject the
// caller arrived with beside the tenant this subsystem resolved, so apps/sandbox
// records them as its lessee and answers a colleague naming it as absent. The admin
// bits do not travel, because this surface acts on the caller's own sessions only.
func TestSandboxCallsStateTheCallersSubject(t *testing.T) {
	in := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "alice", OrgAdmin: true, Admin: true})
	ctx, done, err := callCtx(in, "acme")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	who := zip.CallerOf(ctx)
	if who.Org != "acme" || who.User != "alice" {
		t.Fatalf("a sandbox call is stated as %+v, want alice in acme", who)
	}
	if who.Admin || who.OrgAdmin {
		t.Fatalf("a sandbox call carried the caller's admin bits: %+v", who)
	}
}

// The subject stated is the one a sandbox is held by, never X-User-Id: a caller
// whose token names no `sub` presents a username or a display name somebody else
// shares, and is refused rather than stated as them.
func TestACallerWithNoSubjectHoldsNoSession(t *testing.T) {
	for _, c := range []struct {
		name  string
		p     principal.Principal
		state string
	}{
		{"a token's sub", principal.Principal{Org: "acme", User: "4f1c-uuid", Stable: true}, "4f1c-uuid"},
		{"a token with no sub", principal.Principal{Org: "acme", User: "Zach Kelling"}, ""},
	} {
		app := zip.New(zip.Config{})
		app.Raw(http.MethodGet, "/probe", func(zc *zip.Ctx) error {
			principal.Mint(zc, c.p)
			ctx, done, err := callCtx(principal.WithSubject(zc.Context(), zc), "acme")
			if c.state == "" {
				var he *zip.HTTPError
				if !errors.As(err, &he) || he.Status != http.StatusForbidden {
					t.Errorf("%s: %v, want 403", c.name, err)
				}
				return zc.NoContent(http.StatusNoContent)
			}
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
				return zc.NoContent(http.StatusNoContent)
			}
			defer done()
			if who := zip.CallerOf(ctx); who.User != c.state {
				t.Errorf("%s: stated %q, want %q", c.name, who.User, c.state)
			}
			return zc.NoContent(http.StatusNoContent)
		})
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set("X-User-Id", c.p.User)
		if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
			t.Fatal(err)
		}
	}
}
