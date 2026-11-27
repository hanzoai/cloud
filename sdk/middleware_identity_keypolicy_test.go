package cloud

// A KEY'S POLICY IS APPLIED WHERE THE KEY BECOMES A PRINCIPAL.
//
// Each API key is its own credential with its own policy: an expiry and a
// revocation IAM enforces when it resolves the key, and a grant — what the key may
// reach, and whether it may write — that this boundary applies to every request's
// address. These tests drive real keys through SanitizeIdentity against a stub IAM
// that answers per key, the way keys/principal does.

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
	"github.com/zap-proto/zip"
)

// iamPolicyServer answers keys/principal per presented key: a live key resolves
// to acme/alice with its scope, a dead one is refused with IAM's reason code.
func iamPolicyServer(t *testing.T, scopes map[string]string, dead map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		key := r.URL.Query().Get("accessKey")
		if why, ok := dead[key]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"error","msg":"the entity does not exist","code":"` + why + `"}`))
			return
		}
		scope, ok := scopes[key]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"error","msg":"the entity does not exist","code":"key_unknown"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"acme","name":"alice","org":"acme","scope":"` + scope + `","key":"` + key + `-row"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// policyApp is the boundary in front of three capabilities' addresses.
func policyApp(t *testing.T, scopes, dead map[string]string) *zip.App {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	v := newIdentityValidator(testIssuer, jwksServer(t, &key.PublicKey).URL, 0)
	iam := iamPolicyServer(t, scopes, dead)
	v.keys = &iamKeys{base: iam.URL, auth: "Basic test", http: iam.Client(), cache: NewCache[keyID, keyAnswer](time.Minute)}

	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v))
	ok := func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"user": c.Header("X-User-Id"), "key": c.Header(HeaderKey)})
	}
	for _, path := range []string{"/v1/train/jobs", "/v1/chat/completions", "/v1/billing/balance"} {
		app.Raw(http.MethodGet, path, ok)
		app.Raw(http.MethodPost, path, ok)
	}
	return app
}

func send(t *testing.T, app *zip.App, method, path, key string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// The capabilities the addresses below belong to, as the routing table says.
func TestPolicyAddressesBelongToTheirCapabilities(t *testing.T) {
	for path, want := range map[string]string{
		"/v1/train/jobs": "train", "/v1/chat/completions": "ai", "/v1/billing/balance": "billing",
	} {
		if got := manifest.OwnerOf(path); got != want {
			t.Fatalf("OwnerOf(%s) = %q, want %q", path, got, want)
		}
	}
}

// A restricted key is refused outside its scope, a read-only key is refused every
// write, and an unrestricted key is refused nothing.
func TestARestrictedKeyIsRefusedOutsideItsScope(t *testing.T) {
	app := policyApp(t, map[string]string{
		"sk-all":        "",
		"sk-model":      "model:zen5",
		"sk-train":      "product:train,read:billing",
		"sk-readonly":   "read:*",
		"sk-everything": "product:*",
	}, nil)

	for _, tc := range []struct {
		key, method, path string
		want              int
		says              string
	}{
		{"sk-all", http.MethodPost, "/v1/chat/completions", http.StatusOK, ""},
		{"sk-all", http.MethodPost, "/v1/train/jobs", http.StatusOK, ""},
		// A model limit is not a capability limit: the boundary leaves it to the AI edge.
		{"sk-model", http.MethodPost, "/v1/chat/completions", http.StatusOK, ""},

		{"sk-train", http.MethodPost, "/v1/train/jobs", http.StatusOK, ""},
		{"sk-train", http.MethodGet, "/v1/train/jobs", http.StatusOK, ""},
		{"sk-train", http.MethodGet, "/v1/billing/balance", http.StatusOK, ""},
		{"sk-train", http.MethodPost, "/v1/billing/balance", http.StatusForbidden, "may only read billing"},
		{"sk-train", http.MethodPost, "/v1/chat/completions", http.StatusForbidden, "may not reach ai"},
		{"sk-train", http.MethodGet, "/v1/chat/completions", http.StatusForbidden, "may not reach ai"},

		{"sk-readonly", http.MethodGet, "/v1/chat/completions", http.StatusOK, ""},
		{"sk-readonly", http.MethodGet, "/v1/billing/balance", http.StatusOK, ""},
		{"sk-readonly", http.MethodPost, "/v1/chat/completions", http.StatusForbidden, "may only read ai"},
		{"sk-readonly", http.MethodPost, "/v1/train/jobs", http.StatusForbidden, "may only read train"},

		{"sk-everything", http.MethodPost, "/v1/billing/balance", http.StatusOK, ""},

		// The router folds case and a trailing slash, and so does the grant: a
		// spelling is not a way around it.
		{"sk-train", http.MethodPost, "/v1/BILLING/balance", http.StatusForbidden, "may only read billing"},
		{"sk-train", http.MethodPost, "/v1/Chat/Completions/", http.StatusForbidden, "may not reach ai"},
		{"sk-readonly", http.MethodPost, "/V1/TRAIN/JOBS", http.StatusForbidden, "may only read train"},
		{"sk-train", http.MethodPost, "/v1/TRAIN/jobs", http.StatusOK, ""},
	} {
		code, body := send(t, app, tc.method, tc.path, tc.key)
		if code != tc.want || !strings.Contains(body, tc.says) {
			t.Errorf("%s %s %s: got %d %s, want %d saying %q", tc.key, tc.method, tc.path, code, body, tc.want, tc.says)
		}
		if code == http.StatusOK && !strings.Contains(body, "acme/alice") {
			t.Errorf("%s %s %s: admitted without its principal: %s", tc.key, tc.method, tc.path, body)
		}
	}
}

// An expired or revoked key is refused at auth with a sentence saying so, and a
// sibling key of the same person keeps working.
func TestADeadKeyIsRefusedSayingWhy(t *testing.T) {
	app := policyApp(t, map[string]string{"sk-live": ""}, map[string]string{
		"sk-expired": "key_expired",
		"sk-revoked": "key_revoked",
	})
	for key, says := range map[string]string{"sk-expired": "expired", "sk-revoked": "revoked"} {
		code, body := send(t, app, http.MethodGet, "/v1/train/jobs", key)
		if code != http.StatusUnauthorized || !strings.Contains(body, says) {
			t.Errorf("%s: got %d %s, want 401 saying %q", key, code, body, says)
		}
	}
	if code, body := send(t, app, http.MethodGet, "/v1/train/jobs", "sk-live"); code != http.StatusOK || !strings.Contains(body, "acme/alice") {
		t.Fatalf("the sibling key stopped working: %d %s", code, body)
	}
	// An unknown key stays anonymous, as before: the handler sees nobody.
	if code, body := send(t, app, http.MethodGet, "/v1/train/jobs", "sk-unknown"); code != http.StatusOK || strings.Contains(body, "acme/alice") {
		t.Fatalf("an unknown key: %d %s", code, body)
	}
}

// Callers served one after another on ONE connection never see each other's
// verdict. fasthttp serves every request on a keep-alive connection from one
// RequestCtx and parses each request's headers into the same bytes, and a header
// read through zip aliases them. The key cache once kept the presented key by
// reference, so the next caller's key of the same length rewrote every entry
// stored from that connection into its own spelling, and an entry whose hash
// happened to agree answered the newcomer with another key's principal or
// refusal. Over a gateway's pooled connection that is one tenant served as
// another. Each key here resolves to a principal naming it, or to a refusal; with
// the cache keyed by reference, 512 same-length callers on one connection crossed
// about a dozen times a run.
func TestCallersOnOneConnectionNeverShareAVerdict(t *testing.T) {
	const callers = 512
	live, dead := map[string]string{}, map[string]string{}
	for i := range callers {
		if key := fmt.Sprintf("sk-%04d", i); i%2 == 0 {
			live[key] = ""
		} else {
			dead[key] = "key_revoked"
		}
	}
	app := policyApp(t, live, dead)
	ln := fasthttputil.NewInmemoryListener()
	srv := &fasthttp.Server{Handler: app.Fiber().Handler()}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	conn, err := ln.Dial()
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)

	for i := range callers {
		key := fmt.Sprintf("sk-%04d", i)
		if _, err := fmt.Fprintf(conn, "GET /v1/train/jobs HTTP/1.1\r\nHost: cloud\r\nAuthorization: Bearer %s\r\n\r\n", key); err != nil {
			t.Fatalf("%s: write: %v", key, err)
		}
		resp, err := http.ReadResponse(r, nil)
		if err != nil {
			t.Fatalf("%s: read: %v", key, err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		code, body := resp.StatusCode, string(b)
		if _, ok := live[key]; ok {
			if code != http.StatusOK || !strings.Contains(body, `"key":"acme/`+key+`-row"`) {
				t.Errorf("%s, a live key, was answered as someone else: %d %s", key, code, body)
			}
		} else if code != http.StatusUnauthorized || !strings.Contains(body, "revoked") {
			t.Errorf("%s, a revoked key, was answered as someone else: %d %s", key, code, body)
		}
	}
}

// A door names no capability, so a key's grant is asked about the operation the
// door reached: the call plane admits a restricted key to an operation it may
// reach and refuses it one it may not, exactly as REST does.
func TestARestrictedKeyIsAskedAboutTheOperationADoorReached(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	v := newIdentityValidator(testIssuer, jwksServer(t, &key.PublicKey).URL, 0)
	iam := iamPolicyServer(t, map[string]string{"sk-train": "product:train", "sk-all": ""}, nil)
	v.keys = &iamKeys{base: iam.URL, auth: "Basic test", http: iam.Client(), cache: NewCache[keyID, keyAnswer](time.Minute)}

	type unit struct {
		OK bool `json:"ok"`
	}
	run := func(context.Context, *unit) (*unit, error) { return &unit{OK: true}, nil }
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v))
	app.Use(Bridge())
	app.Authorize(Permit())
	app.Group("/v1/train").Post("/jobs", run)
	app.Group("/v1/billing").Post("/alerts", run)
	ids := map[string]string{}
	for _, o := range app.Registry() {
		ids[o.Path] = o.OperationID
	}

	for _, tc := range []struct {
		key, path string
		want      int
	}{
		{"sk-train", "/v1/train/jobs", http.StatusOK},
		{"sk-train", "/v1/billing/alerts", http.StatusForbidden},
		{"sk-all", "/v1/billing/alerts", http.StatusOK},
	} {
		id := ids[tc.path]
		if id == "" {
			t.Fatalf("no operation at %s: %v", tc.path, ids)
		}
		if code, body := send(t, app, http.MethodPost, zip.CallPath+id, tc.key); code != tc.want {
			t.Errorf("%s on the call plane to %s: got %d %s, want %d", tc.key, id, code, body, tc.want)
		}
		if code, body := send(t, app, http.MethodPost, tc.path, tc.key); code != tc.want {
			t.Errorf("%s over REST to %s: got %d %s, want %d", tc.key, tc.path, code, body, tc.want)
		}
	}
}

// The boundary names the API key a request arrived on, as IAM resolved it, and a
// caller cannot name one: a client copy of X-Key-Id never survives ingress.
func TestTheKeyARequestArrivedOnIsNamedByTheBoundaryAlone(t *testing.T) {
	app := policyApp(t, map[string]string{"sk-live": ""}, nil)
	req := func(key, forged string) string {
		r := httptest.NewRequest(http.MethodGet, "/v1/train/jobs", nil)
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		r.Header.Set(HeaderKey, forged)
		resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if body := req("sk-live", "someone-elses-key"); !strings.Contains(body, `"key":"acme/sk-live-row"`) {
		t.Fatalf("a key request must carry the key IAM named, got %s", body)
	}
	if body := req("", "someone-elses-key"); strings.Contains(body, "someone-elses-key") {
		t.Fatalf("a forged X-Key-Id survived ingress: %s", body)
	}
}

// A key sent the way Anthropic's clients send one, in X-Api-Key, meets the same
// policy as one sent as a bearer, and a request carrying two different credentials
// is refused rather than judged on one and served on the other.
func TestAKeyInXApiKeyMeetsTheSamePolicy(t *testing.T) {
	app := policyApp(t, map[string]string{"sk-train": "product:train", "sk-all": ""}, nil)
	send := func(h map[string]string) (int, string) {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		for k, v := range h {
			r.Header.Set(k, v)
		}
		resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := send(map[string]string{"X-Api-Key": "sk-train"}); code != http.StatusForbidden || !strings.Contains(body, "may not reach ai") {
		t.Fatalf("a restricted key in X-Api-Key: %d %s", code, body)
	}
	if code, body := send(map[string]string{"X-Api-Key": "sk-all"}); code != http.StatusOK || !strings.Contains(body, "acme/alice") {
		t.Fatalf("a key in X-Api-Key was not resolved: %d %s", code, body)
	}
	if code, _ := send(map[string]string{"Authorization": "Bearer sk-all", "X-Api-Key": "sk-train"}); code != http.StatusUnauthorized {
		t.Fatalf("two different credentials: want 401, got %d", code)
	}
	if code, _ := send(map[string]string{"Authorization": "Bearer sk-all", "X-Api-Key": "sk-all"}); code != http.StatusOK {
		t.Fatalf("the same key twice: want 200, got %d", code)
	}
}

// A KEY IS NEVER AN ADMIN. An API key resolves with no signed membership set, so
// authz reads it as a machine and the boundary mints neither X-User-IsOrgAdmin nor
// X-User-IsAdmin for it — even an unrestricted key whose holder IAM reports as an
// admin. Every surface gated on the org-admin bit (the gateway policy, spend caps)
// is therefore one an API key cannot write, and says so as its rule.
func TestAKeyIsNeverAnAdmin(t *testing.T) {
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"acme","name":"alice","org":"acme","isAdmin":true,"scope":"","key":"k-row"}}`))
	}))
	t.Cleanup(iam.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	v := newIdentityValidator(testIssuer, jwksServer(t, &key.PublicKey).URL, 0)
	v.keys = &iamKeys{base: iam.URL, auth: "Basic test", http: iam.Client(), cache: NewCache[keyID, keyAnswer](time.Minute)}
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v))
	app.Raw(http.MethodGet, "/v1/train/jobs", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]any{"org": c.Header("X-Org-Id"), "orgAdmin": principal.IsOrgAdmin(c), "super": principal.Super(c)})
	})
	code, body := send(t, app, http.MethodGet, "/v1/train/jobs", "sk-admin-held")
	if code != http.StatusOK || !strings.Contains(body, `"org":"acme"`) {
		t.Fatalf("the key did not resolve: %d %s", code, body)
	}
	if !strings.Contains(body, `"orgAdmin":false`) || !strings.Contains(body, `"super":false`) {
		t.Fatalf("an API key carried an admin bit: %s", body)
	}
}
