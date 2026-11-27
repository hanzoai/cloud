package team

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/apps/team/token"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// hs256Only returns headers carrying ONLY team's own HS256 session token (no
// gateway X-Org-Id / X-User-Id headers) — the exact shape a browser or team
// client sends when authenticated by team's session token or account-token
// cookie rather than an IAM JWT.
func hs256Only(t *testing.T, acct, org string) map[string]string {
	t.Helper()
	tok, err := token.Generate(acct, "", map[string]any{"org": org}, expUnix(sessionTokenTTL), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"Authorization": "Bearer " + tok}
}

func hs256CookieOnly(t *testing.T, acct, org string) map[string]string {
	t.Helper()
	tok, err := token.Generate(acct, "", map[string]any{"org": org}, expUnix(sessionTokenTTL), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"Cookie": authCookie + "=" + tok}
}

// TestHS256SessionWithoutGatewayOrgHeaderReachesIAM reproduces the exact two
// production 502s:
//   - GET /v1/team/channels      -> 502 "team: spaces: spaces of: members: no org on the call"
//   - GET /v1/team/billing/plan  -> 502 "seat count unavailable"
//
// When a caller authenticates with team's own HS256 token or account-token
// cookie, the inbound HTTP request carries no gateway X-Org-Id header.
// accountStore must detach the inbound request before stating the verified org
// with cloud.For so zip does not prefer the empty request header over cloud.For.
func TestHS256SessionWithoutGatewayOrgHeaderReachesIAM(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		entitled := &fakeCommerce{ent: &cloud.LicenseEntitlement{ProductID: productTeam, Active: true, Plan: "pro"}}
		app, store := billingApp(t, entitled, nil)
		ctx := context.Background()

		ws, err := store.EnsureSpace(ctx, gateOrg, gateAcct, "Ada")
		if err != nil {
			t.Fatalf("EnsureSpace: %v", err)
		}
		if err := store.AddMember(ctx, gateOrg, ws.UUID, "00000000-0000-4000-8000-000000000011", "member"); err != nil {
			t.Fatalf("AddMember: %v", err)
		}

		for name, headers := range map[string]map[string]string{
			"bearer": hs256Only(t, gateAcct, gateOrg),
			"cookie": hs256CookieOnly(t, gateAcct, gateOrg),
		} {
			t.Run("billing/plan "+name, func(t *testing.T) {
				code, body := call(t, app, http.MethodGet, "/v1/team/billing/plan", headers, nil)
				if code != http.StatusOK {
					t.Fatalf("GET /v1/team/billing/plan (%s, no X-Org-Id) = %d %s, want 200", name, code, body)
				}
				var out planInfo
				if err := json.Unmarshal(body, &out); err != nil {
					t.Fatalf("decode: %v (%s)", err, body)
				}
				if out.Seats != 2 || out.Plan != "pro" || !out.Active {
					t.Fatalf("plan = %+v, want Seats=2 Plan=pro Active=true", out)
				}
			})
		}

		// Empty org must still fail closed on the IAM plane.
		if _, _, err := store.Seats(ctx, ""); err == nil {
			t.Fatal("Seats with empty org succeeded; must fail closed")
		}
		if _, err := store.SpacesOf(ctx, "", gateAcct); err == nil {
			t.Fatal("SpacesOf with empty org succeeded; must fail closed")
		}
	})
}

// TestChannelsListWithHS256BearerWithoutGatewayOrgHeader verifies that
// GET /v1/team/channels (which resolves the caller's spaces through SpacesOf ->
// iam.IAMMembers) succeeds when authenticated purely via team's HS256 token
// without an X-Org-Id header.
func TestChannelsListWithHS256BearerWithoutGatewayOrgHeader(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false)

	for name, headers := range map[string]map[string]string{
		"bearer": hs256Only(t, c.adaID, c.org),
		"cookie": hs256CookieOnly(t, c.adaID, c.org),
	} {
		t.Run(name, func(t *testing.T) {
			var got teamChannels
			c.must(t, http.StatusOK, http.MethodGet, "/v1/team/channels", headers, nil, &got)
			found := false
			for _, ch := range got.Channels {
				if ch.ID == general.ID {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("GET /v1/team/channels (%s, no X-Org-Id) missed %s: %+v", name, general.ID, got.Channels)
			}
		})
	}
}
