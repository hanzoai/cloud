package leaderboard

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestOptin_UserFlowSelfOnly: private by default; a PUT lists the CALLER only.
func TestOptin_UserFlowSelfOnly(t *testing.T) {
	installFakeDS(t, nil)
	app := mountApp(t)

	// Default: private, but settable.
	code, body := doGet(t, app, "/v1/usage/leaderboard/optin", principalHeaders("acme", "alice"))
	if code != http.StatusOK {
		t.Fatalf("code=%d", code)
	}
	var v optinView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	if v.User.Listed || !v.User.CanSet {
		t.Fatalf("default must be private + settable: %+v", v.User)
	}

	// Opt in with a handle.
	code, body = doJSON(t, app, http.MethodPut, "/v1/usage/leaderboard/optin", principalHeaders("acme", "alice"), userOptinReq{Listed: true, Handle: "AliceZ"})
	if code != http.StatusOK {
		t.Fatalf("put code=%d body=%s", code, body)
	}

	// Reflected on GET.
	_, body = doGet(t, app, "/v1/usage/leaderboard/optin", principalHeaders("acme", "alice"))
	_ = json.Unmarshal(body, &v)
	if !v.User.Listed || v.User.Handle != "AliceZ" {
		t.Fatalf("after opt-in: %+v", v.User)
	}

	// The write is keyed on the caller: bob (same org) is unaffected + still private.
	_, body = doGet(t, app, "/v1/usage/leaderboard/optin", principalHeaders("acme", "bob"))
	_ = json.Unmarshal(body, &v)
	if v.User.Listed {
		t.Fatal("alice's opt-in must not list bob")
	}
}

// TestOptin_OrgRequiresAdmin: only an org admin may set the org's public listing.
func TestOptin_OrgRequiresAdmin(t *testing.T) {
	installFakeDS(t, nil)
	app := mountApp(t)

	code, _ := doJSON(t, app, http.MethodPut, "/v1/usage/leaderboard/optin/org", principalHeaders("acme", "alice"), orgOptinReq{Listed: true, Display: "Acme"})
	if code != http.StatusForbidden {
		t.Fatalf("non-admin org opt-in must be 403, got %d", code)
	}

	h := withHeader(principalHeaders("acme", "alice"), "X-User-IsOrgAdmin", "true")
	code, body := doJSON(t, app, http.MethodPut, "/v1/usage/leaderboard/optin/org", h, orgOptinReq{Listed: true, Display: "Acme"})
	if code != http.StatusOK {
		t.Fatalf("org admin opt-in must be 200, got %d body=%s", code, body)
	}
	var got orgOptinView
	_ = json.Unmarshal(body, &got)
	if !got.Listed || got.Display != "Acme" || !got.CanManage {
		t.Fatalf("org opt-in result: %+v", got)
	}
}

// TestOptin_BadHandleRejected: a handle outside the shape guard is a 400.
func TestOptin_BadHandleRejected(t *testing.T) {
	installFakeDS(t, nil)
	app := mountApp(t)
	code, _ := doJSON(t, app, http.MethodPut, "/v1/usage/leaderboard/optin", principalHeaders("acme", "alice"), userOptinReq{Listed: true, Handle: "bad<script>"})
	if code != http.StatusBadRequest {
		t.Fatalf("bad handle must be 400, got %d", code)
	}
}
