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
// org, e.g. "hanzo". That org is one this package already refuses to hand to a
// customer (onboarding.go's reservedOrgs), so landing in it is not owning it.
//
// Read as "already has an org" it sent them down the ADDITIONAL branch, which
// creates an org and leaves the user OUTSIDE it, and answered `personal: true`
// with 409 "you already have an organization" — thirty seconds after signing up,
// about an org that was never theirs.

// TestOnboard_OAuthSignup_GetsItsOwnOrg drives a real OAuth-shaped signup end to
// end through the mounted route: a validated principal whose org is the sign-up
// application's, asking for a personal space. It must end OWNING its own org.
func TestOnboard_OAuthSignup_GetsItsOwnOrg(t *testing.T) {
	f := newFakeIAM()
	// The row federated sign-up wrote: owner is the sign-up application's org, and
	// the user admins nothing there — they were deposited, not enrolled.
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
	if resp.Additional {
		t.Fatalf("a fresh signup's FIRST org must not be an additional one: %+v", resp)
	}
	if resp.Org != "dave" {
		t.Fatalf("personal org slug = %q, want %q", resp.Org, "dave")
	}
	// The whole point: they must end up IN it. An org they do not own is the bug.
	if f.movedTo["hanzo/dave"] != "dave" {
		t.Fatalf("signup must be moved into the org it just created, movedTo=%v", f.movedTo)
	}
	if owner, _ := f.createdOrgs[0]["owner"].(string); owner != adminOrg {
		t.Fatalf("created org must be owned by %q, got %q", adminOrg, owner)
	}
}

// TestOnboard_OAuthSignup_NamedOrgAlsoMoves is the same first run through the
// other path — a named org rather than a personal one. It took the ADDITIONAL
// branch silently: 200, an org created, and the founder left outside it.
func TestOnboard_OAuthSignup_NamedOrgAlsoMoves(t *testing.T) {
	f := newFakeIAM()
	f.user["hanzo/dave"] = map[string]any{"owner": "hanzo", "name": "dave", "isAdmin": false}
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	code, body := call(t, app, http.MethodPost, "/v1/account/orgs", "dave", "hanzo", `{"name":"Acme Rockets"}`)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	var resp onboardResp
	mustJSON(t, body, &resp)
	if resp.Additional {
		t.Fatalf("a fresh signup's first named org must not be additional: %+v", resp)
	}
	if f.movedTo["hanzo/dave"] != "acme-rockets" {
		t.Fatalf("founder must be moved into their own org, movedTo=%v", f.movedTo)
	}
}

// TestOnboard_SuperAdminKeepsTheirOrg holds the line the landing-org rule must not
// cross. A SuperAdmin's privilege IS their membership of the reserved `admin` org
// (owner == "admin"), so treating that as a landing and moving them out would
// strip the very thing that makes them one. Standing beats the landing, and only
// IAM may attest to it — a header would let a caller elect their own move.
func TestOnboard_SuperAdminKeepsTheirOrg(t *testing.T) {
	f := newFakeIAM()
	f.user["admin/root"] = map[string]any{"owner": "admin", "name": "root", "isAdmin": true}
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	// A named additional org: created, but the SuperAdmin is NOT moved.
	code, body := call(t, app, http.MethodPost, "/v1/account/orgs", "root", "admin", `{"name":"Side Project"}`)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	var resp onboardResp
	mustJSON(t, body, &resp)
	if !resp.Additional {
		t.Fatalf("a SuperAdmin's new org is an ADDITIONAL one: %+v", resp)
	}
	if len(f.movedTo) != 0 {
		t.Fatalf("a SuperAdmin must never be moved out of the admin org, movedTo=%v", f.movedTo)
	}

	// And the 409 stays correct where it was always correct: asking for a personal
	// space when you already hold one is still a conflict.
	code, _ = call(t, app, http.MethodPost, "/v1/account/orgs", "root", "admin", `{"personal":true}`)
	if code != http.StatusConflict {
		t.Fatalf("personal-while-orged: want 409, got %d", code)
	}
}

// TestOnboard_MemberOfATenantIsNotFirstRun keeps an invited teammate where they
// are. Their org is a real tenant, not a landing, so their new org is additional
// however little standing they hold in it — a move would yank them out of the
// team that invited them.
func TestOnboard_MemberOfATenantIsNotFirstRun(t *testing.T) {
	f := newFakeIAM()
	f.user["acme/bob"] = map[string]any{"owner": "acme", "name": "bob", "isAdmin": false}
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	code, body := call(t, app, http.MethodPost, "/v1/account/orgs", "bob", "acme", `{"name":"Side Project"}`)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	var resp onboardResp
	mustJSON(t, body, &resp)
	if !resp.Additional {
		t.Fatalf("a tenant member's new org is additional: %+v", resp)
	}
	if len(f.movedTo) != 0 {
		t.Fatalf("a tenant member must never be moved, movedTo=%v", f.movedTo)
	}
}
