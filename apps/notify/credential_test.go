package notify

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// credApp is the credential harness: the routes the binary serves, behind the
// cloud.Bridge the composer installs, sealing into kms.
func credApp(t *testing.T, kms secret.Store) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	routes(app, &service{log: luxlog.New("test"), kms: kms})
	return app
}

// caller is who a request is as the boundary minted it: its org, whether a
// principal was validated at all, and whether it administers that org.
type caller struct {
	org   string
	user  string
	admin bool
}

var (
	orgAdmin = caller{org: "acme", user: "u_admin", admin: true}
	member   = caller{org: "acme", user: "u_member"}
	nobody   = caller{org: "acme"}
)

func credential(t *testing.T, app *zip.App, method, path string, who caller, body any) (int, string) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("X-Org-Id", who.org)
	if who.user != "" {
		rq.Header.Set("X-User-Id", who.user)
	}
	if who.admin {
		rq.Header.Set("X-User-IsOrgAdmin", "true")
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// An org admin sets a provider's credential where a send reads it, the receipt
// names it without its value, and the next send picks the provider it completed.
func TestAnOrgAdminSetsACredentialWhereASendReadsIt(t *testing.T) {
	kms := fakeKMS{m: map[string]string{}}
	app := credApp(t, kms)
	for key, v := range map[string]string{"account-sid": "ACxxxx", "auth-token": "tok-SECRET", "from-number": "+15551230000"} {
		code, body := credential(t, app, http.MethodPut, "/v1/notify/credentials/twilio/"+key, orgAdmin, map[string]string{"value": v})
		if code != http.StatusOK {
			t.Fatalf("set %s: %d %s", key, code, body)
		}
		if strings.Contains(body, v) {
			t.Fatalf("the receipt for %s echoed its value: %s", key, body)
		}
	}
	if got := kms.m["orgs/acme/notify/twilio/auth-token"]; got != "tok-SECRET" {
		t.Fatalf("auth-token sealed as %q, want it at the ref creds reads", got)
	}
	s := &service{kms: kms}
	if svc, err := s.pick(t.Context(), "acme", "sms"); err != nil || svc != "twilio" {
		t.Fatalf("pick after setting twilio = %q, %v", svc, err)
	}

	code, body := credential(t, app, http.MethodDelete, "/v1/notify/credentials/twilio/auth-token", orgAdmin, nil)
	if code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", code, body)
	}
	if _, ok := kms.m["orgs/acme/notify/twilio/auth-token"]; ok {
		t.Fatal("the removed credential is still sealed")
	}
	if code, body := credential(t, app, http.MethodDelete, "/v1/notify/credentials/twilio/auth-token", orgAdmin, nil); code != http.StatusNoContent {
		t.Fatalf("removing what is already gone: %d %s", code, body)
	}
}

// Setting a credential is an org admin's, and nothing reaches the store for
// anyone else; an address outside what a provider reads seals nothing either.
func TestOnlyAnOrgAdminSetsACredential(t *testing.T) {
	kms := fakeKMS{m: map[string]string{"orgs/acme/notify/twilio/auth-token": "kept"}}
	app := credApp(t, kms)
	for _, tc := range []struct {
		name   string
		method string
		path   string
		who    caller
		body   any
		want   int
	}{
		{"a member sets", http.MethodPut, "/v1/notify/credentials/twilio/auth-token", member, map[string]string{"value": "planted"}, http.StatusForbidden},
		{"a member removes", http.MethodDelete, "/v1/notify/credentials/twilio/auth-token", member, nil, http.StatusForbidden},
		{"nobody sets", http.MethodPut, "/v1/notify/credentials/twilio/auth-token", nobody, map[string]string{"value": "planted"}, http.StatusUnauthorized},
		{"an unknown provider", http.MethodPut, "/v1/notify/credentials/carrier-pigeon/api-key", orgAdmin, map[string]string{"value": "v"}, http.StatusNotFound},
		{"a key the provider does not read", http.MethodPut, "/v1/notify/credentials/twilio/api-key", orgAdmin, map[string]string{"value": "v"}, http.StatusBadRequest},
		{"an empty value", http.MethodPut, "/v1/notify/credentials/twilio/auth-token", orgAdmin, map[string]string{"value": ""}, http.StatusBadRequest},
		{"a value past the cap", http.MethodPut, "/v1/notify/credentials/twilio/auth-token", orgAdmin, map[string]string{"value": strings.Repeat("x", credentialCap+1)}, http.StatusBadRequest},
	} {
		if code, body := credential(t, app, tc.method, tc.path, tc.who, tc.body); code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, code, body, tc.want)
		}
	}
	if len(kms.m) != 1 || kms.m["orgs/acme/notify/twilio/auth-token"] != "kept" {
		t.Fatalf("a refused request reached the store: %v", kms.m)
	}
}

// A deployment with no KMS seals nothing and says so.
func TestACredentialNeedsAStore(t *testing.T) {
	app := credApp(t, nil)
	if code, body := credential(t, app, http.MethodPut, "/v1/notify/credentials/twilio/auth-token", orgAdmin, map[string]string{"value": "v"}); code != http.StatusServiceUnavailable {
		t.Fatalf("no KMS: %d %s, want 503", code, body)
	}
}
