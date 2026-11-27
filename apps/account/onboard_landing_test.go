package account

import (
	"net/http"
	"testing"
)

// The day-one path: a brand-new user signs up through OAuth and asks for their
// own space.
//
// Federated sign-up files the new user under the sign-up APPLICATION's own
// organization (iam internal/oidc/federation.go: `org := app.Organization`), so
// the very first request they ever make already carries an X-Org-Id — the brand
// org, e.g. "hanzo". They create their org from there and keep that account: the
// address they signed up with signs them in afterwards exactly as before.

// TestOnboard_OAuthSignup_GetsItsOwnOrg drives a real OAuth-shaped signup through
// the mounted route: a validated principal whose org is the sign-up application's,
// asking for a personal space. It ends owning that org, as hanzo/dave still.
func TestOnboard_OAuthSignup_GetsItsOwnOrg(t *testing.T) {
	f := newFakeIAM()
	f.user["hanzo/dave"] = map[string]any{
		"owner": "hanzo", "name": "dave", "type": "normal-user", "isAdmin": false,
	}
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	code, body := call(t, app, http.MethodPost, "/v1/account/orgs", "dave", "hanzo", `{"personal":true}`)
	if code != http.StatusOK {
		t.Fatalf("OAuth signup asking for its own space: want 200, got %d (%s)", code, body)
	}
	var resp onboardResp
	mustJSON(t, body, &resp)
	if resp.Org != "dave" {
		t.Fatalf("personal org slug = %q, want %q", resp.Org, "dave")
	}
	if f.roles["hanzo/dave|dave"] != "owner" {
		t.Fatalf("hanzo/dave does not own the org it created: %v", f.roles)
	}
	if len(f.rows) != 0 {
		t.Fatalf("the signup's account was rewritten: %v", f.rows)
	}
	if owner, _ := f.createdOrgs[0]["owner"].(string); owner != adminOrg {
		t.Fatalf("created org must be owned by %q, got %q", adminOrg, owner)
	}
}

// TestOnboard_OAuthSignup_NamedOrgKeepsTheAccount is the same first run with a
// named org rather than a personal one.
func TestOnboard_OAuthSignup_NamedOrgKeepsTheAccount(t *testing.T) {
	f := newFakeIAM()
	f.user["hanzo/dave"] = map[string]any{"owner": "hanzo", "name": "dave", "isAdmin": false}
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	code, body := call(t, app, http.MethodPost, "/v1/account/orgs", "dave", "hanzo", `{"name":"Acme Rockets"}`)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	if f.roles["hanzo/dave|acme-rockets"] != "owner" || len(f.rows) != 0 {
		t.Fatalf("roles = %v, rows = %v: want hanzo/dave to own acme-rockets and keep its account", f.roles, f.rows)
	}
}

// TestOnboard_SuperAdminKeepsTheirOrg holds the line for the platform operator. A
// SuperAdmin's privilege IS their own org being the reserved `admin` org, and a
// platform operator holds no role in a customer org: IAM refuses to record an
// admin-org account as a member. So the request is refused before anything is
// created, whichever org they are acting in.
func TestOnboard_SuperAdminKeepsTheirOrg(t *testing.T) {
	for _, actingIn := range []string{"admin", "acme"} {
		for _, body := range []string{`{"name":"Side Project"}`, `{"personal":true}`} {
			f := newFakeIAM()
			app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
			operator := map[string]string{
				"X-User-Id": "root", "X-User-Name": "root", "X-User-Owner": "admin",
				"X-Org-Id": actingIn, "X-User-IsAdmin": "true",
			}
			code, got := callH(t, app, http.MethodPost, "/v1/account/orgs", operator, body)
			if code != http.StatusForbidden {
				t.Fatalf("SuperAdmin in %s, %s: want 403, got %d (%s)", actingIn, body, code, got)
			}
			if len(f.createdOrgs) != 0 || len(f.granted) != 0 || len(f.rows) != 0 {
				t.Fatalf("SuperAdmin in %s: IAM was written: orgs=%v grants=%v rows=%v",
					actingIn, f.createdOrgs, f.granted, f.rows)
			}
		}
	}
}

// TestOnboard_MemberOfATenantKeepsTheirTeam keeps an invited teammate where they
// are: their new org is one more they belong to.
func TestOnboard_MemberOfATenantKeepsTheirTeam(t *testing.T) {
	f := newFakeIAM()
	f.user["acme/bob"] = map[string]any{"owner": "acme", "name": "bob", "isAdmin": false}
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	code, body := call(t, app, http.MethodPost, "/v1/account/orgs", "bob", "acme", `{"name":"Side Project"}`)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	if f.roles["acme/bob|side-project"] != "owner" || len(f.rows) != 0 {
		t.Fatalf("roles = %v, rows = %v: want acme/bob to own side-project and stay in acme", f.roles, f.rows)
	}
}

// A personal org is named for the person, from the IAM username. On the direct
// path X-User-Id is the UUID subject, and a slug built from it is a random string.
func TestOnboard_PersonalSlugFromUsername(t *testing.T) {
	const sub = "3f2b8c1e-9d4a-4b6f-a0e2-5c7d9e1f2a3b"
	for _, tc := range []struct {
		name, username, taken, wantOrg, wantDisplay string
	}{
		{"username", "dave", "", "dave", "Dave"},
		{"collision suffixes", "dave", "dave", "dave-2", "Dave"},
		{"email-like username", "dave.smith@example.com", "", "dave-smith", "Dave Smith"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeIAM()
			f.user["hanzo/"+sub] = map[string]any{"owner": "hanzo", "name": tc.username}
			if tc.taken != "" {
				f.orgs[tc.taken] = map[string]any{"owner": adminOrg, "name": tc.taken}
			}
			app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

			code, body := callH(t, app, http.MethodPost, "/v1/account/orgs",
				map[string]string{"X-User-Id": sub, "X-User-Name": tc.username, "X-User-Owner": "hanzo", "X-Org-Id": "hanzo"},
				`{"personal":true}`)
			if code != http.StatusOK {
				t.Fatalf("personal org: want 200, got %d (%s)", code, body)
			}
			var resp onboardResp
			mustJSON(t, body, &resp)
			if resp.Org != tc.wantOrg || resp.DisplayName != tc.wantDisplay {
				t.Fatalf("personal org = %q %q, want %q %q", resp.Org, resp.DisplayName, tc.wantOrg, tc.wantDisplay)
			}
		})
	}
}
