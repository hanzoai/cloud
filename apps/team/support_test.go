package team

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/apps/team/token"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	model "github.com/hanzoai/iam/pkg/model"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// An invite writes an IAM membership with this service's own credential, so a
// support session is refused it here and IAM is never asked; the person's own
// invite is written. Nor does a support session get a workspace token, which
// would outlive it.
func TestASupportSessionGetsNoMembershipOrWorkspaceToken(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const org = "maxpower"
		const inviterAcct = "550e8400-e29b-41d4-a716-446655440000"
		var memberships atomic.Int32
		iamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/iam/users/get":
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]string{
					"owner": org, "name": "eve", "id": "113d4dd4-2486-40de-be2b-88d6e3e0b718", "email": r.URL.Query().Get("email"),
				}})
			case "/v1/iam/memberships":
				memberships.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": true})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(iamSrv.Close)

		planetest.ServeIdentity(t)
		store, err := openAccountStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		ws, _ := store.EnsureSpace(context.Background(), org, inviterAcct, "Max Power")
		g := &api{
			accounts: store,
			ident:    testIdent(store),
			cfg:      config{serverSecret: testSecret, iamEndpoint: iamSrv.URL, iamClientID: "hanzo-team", iamClientSecret: "team-secret", provider: "openid"},
			log:      luxlog.New("test"),
			commerce: &fakeCommerce{ent: &cloud.LicenseEntitlement{ProductID: productTeam, Active: true, Plan: "pro"}},
		}
		sess, _ := token.Generate(inviterAcct, "", orgsExtra(org, model.OrgRef{Org: org, Role: "admin"}), expUnix(sessionTokenTTL), testSecret)

		for _, tc := range []struct {
			act     principal.Act
			refused bool
		}{
			{principal.Act{Sub: "u-op", Owner: "admin", Name: "z"}, true},
			{principal.Act{}, false},
		} {
			before := memberships.Load()
			app := zip.New(zip.Config{Logger: luxlog.New("test")})
			app.Use(zip.Handler(func(c *zip.Ctx) error {
				principal.Mint(c, principal.Principal{Org: org, User: inviterAcct, Act: tc.act})
				return c.Next()
			}))
			g.register(app, func(h zip.Handler) zip.Handler { return h })
			_, body := call(t, app, http.MethodPost, "/v1/team/account", authAs(sess, org, inviterAcct),
				map[string]any{"method": "sendInvite", "params": map[string]any{
					"email": "eve@example.com", "workspaceUrl": ws.Slug, "role": roleMember}})
			wrote := memberships.Load() - before
			if tc.refused && (wrote != 0 || !strings.Contains(string(body), "support session")) {
				t.Fatalf("support session invite: %d membership writes, body %s; want refused before IAM", wrote, body)
			}
			if !tc.refused && wrote != 1 {
				t.Fatalf("the person's own invite: %d membership writes, body %s; want 1", wrote, body)
			}
			_, body = call(t, app, http.MethodPost, "/v1/team/account", authAs(sess, org, inviterAcct),
				map[string]any{"method": "selectWorkspace", "params": map[string]any{"workspaceUrl": ws.Slug}})
			if got := strings.Contains(string(body), "cannot open a workspace"); got != tc.refused {
				t.Fatalf("operator %q selectWorkspace: %s; refused=%v, want %v", tc.act.Sub, body, got, tc.refused)
			}
		}
	})
}
