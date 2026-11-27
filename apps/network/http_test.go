package network

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// cloudClient is the cloud's own IAM application, the one subject the fake
// controller administers for.
const cloudClient = "hanzo-cloud"

// subjectIn is the `sub` a bearer carries, as the controller reads it after
// verifying the signature against IAM.
func subjectIn(bearer string) string {
	parts := strings.Split(strings.TrimPrefix(bearer, "Bearer "), ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		Sub string `json:"sub"`
	}
	_ = json.Unmarshal(b, &c)
	return c.Sub
}

// subjectOf is the IAM subject of a client's own token.
func subjectOf(client string) string { return "admin/" + client }

// fakeIAM answers the client_credentials grant for exactly one client with a
// token it signs, its subject "admin/<client>" as IAM spells an application
// acting as itself, and refuses every other credential.
type fakeIAM struct {
	id, secret string
	mints      int
	last       string // the last access token minted
}

func (i *fakeIAM) server(t *testing.T) *httptest.Server {
	t.Helper()
	iss := iamtest.New(t)
	issuer := iss.Discover(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/iam/oauth/token" || r.ParseForm() != nil ||
			r.PostForm.Get("grant_type") != "client_credentials" ||
			r.PostForm.Get("client_id") != i.id || r.PostForm.Get("client_secret") != i.secret {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		i.mints++
		i.last = iss.Sign(t, iamtest.Claims{Sub: subjectOf(i.id), Owner: "admin", Iss: issuer})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": i.last, "token_type": "Bearer", "expires_in": 3600,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeZT is a stand-in for the real Hanzo Zero Trust controller's Edge Management
// API. It speaks the ZT ext-jwt login + {data,meta} envelope and, crucially,
// scopes NOTHING itself — it returns the FULL cross-org inventory so a test can
// prove that cloud (not the controller) enforces the "org-<org>" role-attribute
// tenant boundary. It records the auth it saw for assertions.
type fakeZT struct {
	attempts   int           // every POST /authenticate, admitted or not
	authCount  int           // sessions issued
	ttl        time.Duration // session lifetime; zero means 20 minutes
	services   []map[string]any
	routers    []map[string]any
	identities []map[string]any

	// refuse makes the controller refuse every identity write, as it does one
	// naming an auth policy or a name it will not take.
	refuse bool

	// created is every write the controller accepted, in order, by resource path
	// — the bodies exactly as cloud sent them, so a test can assert what was
	// WRITTEN and not merely what came back. deleted is every id removed.
	created map[string][]map[string]any
	deleted []string

	// configs and policies are what publishing made, listable per service;
	// removed is every non-identity path deleted, in order.
	configs  []map[string]any
	policies []map[string]any
	removed  []string

	// list401Once makes the FIRST /services list answer 401 (a stale session), to
	// exercise the client's re-auth-and-retry-once path.
	list401Once bool
	listCalls   int
}

// accept records one write as the controller saw it. A nil return means the
// request was refused (no session, or an unreadable body) and answered already.
func (f *fakeZT) accept(w http.ResponseWriter, r *http.Request, kind string) map[string]any {
	if r.Header.Get(sessionHeader) == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return nil
	}
	var body map[string]any
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}
	if f.created == nil {
		f.created = map[string][]map[string]any{}
	}
	f.created[kind] = append(f.created[kind], body)
	return body
}

func writeCreated(w http.ResponseWriter, id string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": id}})
}

func writePage(w http.ResponseWriter, items []map[string]any) {
	if items == nil {
		items = []map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": items,
		"meta": map[string]any{"pagination": map[string]any{
			"limit": perPage, "offset": 0, "totalCount": len(items),
		}},
	})
}

func (f *fakeZT) server(t *testing.T) *httptest.Server {
	t.Helper()
	// The cloud logs in as an identity the controller's bootstrap made for its
	// subject, already carrying its dial role unless a test says otherwise.
	if !slices.ContainsFunc(f.identities, func(m map[string]any) bool { return m["externalId"] == subjectOf(cloudClient) }) {
		f.identities = append(f.identities, map[string]any{
			"id": "i-cloud", "name": subjectOf(cloudClient), "externalId": subjectOf(cloudClient),
			"isAdmin": true, "roleAttributes": []string{"org-admin"},
		})
	}
	mux := http.NewServeMux()

	// IAM is the controller's only signer: ext-jwt with the admin subject's IAM
	// bearer opens a session, and anything else — the password method, no bearer,
	// another subject's — is refused.
	mux.HandleFunc(mgmtBase+"/authenticate", func(w http.ResponseWriter, r *http.Request) {
		f.attempts++
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Query().Get("method") != "ext-jwt" ||
			subjectIn(r.Header.Get("Authorization")) != subjectOf(cloudClient) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"UNAUTHORIZED","message":"invalid auth"}}`))
			return
		}
		f.authCount++
		ttl := f.ttl
		if ttl == 0 {
			ttl = 20 * time.Minute
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"token":     fmt.Sprintf("sess-%d", f.authCount),
			"expiresAt": time.Now().Add(ttl).Format(time.RFC3339),
		}})
	})

	mux.HandleFunc(mgmtBase+"/services", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body := f.accept(w, r, "services")
			if body == nil {
				return
			}
			id := fmt.Sprintf("svc-%d", len(f.created["services"]))
			// A created service is thereafter listable, roles and all — which is
			// how the identity route's "<service>-host" validation sees it.
			f.services = append(f.services, map[string]any{
				"id": id, "name": body["name"],
				"roleAttributes":     body["roleAttributes"],
				"encryptionRequired": body["encryptionRequired"],
				"configs":            body["configs"],
			})
			writeCreated(w, id)
			return
		}
		if r.Header.Get(sessionHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.list401Once && f.listCalls == 0 {
			f.listCalls++
			w.WriteHeader(http.StatusUnauthorized) // stale session on first read
			return
		}
		f.listCalls++
		writePage(w, f.services)
	})

	mux.HandleFunc(mgmtBase+"/identities", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if f.refuse && r.Header.Get(sessionHeader) != "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":"COULD_NOT_VALIDATE","message":"authPolicyId: not found"}}`))
				return
			}
			body := f.accept(w, r, "identities")
			if body == nil {
				return
			}
			id := fmt.Sprintf("ident-%d", len(f.created["identities"]))
			f.identities = append(f.identities, map[string]any{
				"id": id, "name": body["name"], "externalId": body["externalId"],
				"isAdmin": body["isAdmin"], "roleAttributes": body["roleAttributes"],
			})
			writeCreated(w, id)
			return
		}
		if r.Header.Get(sessionHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// The one filter cloud sends: externalId="<sub>", a JSON string literal.
		filter := r.URL.Query().Get("filter")
		if filter == "" {
			writePage(w, f.identities)
			return
		}
		lit, ok := strings.CutPrefix(filter, "externalId=")
		var sub string
		if !ok || json.Unmarshal([]byte(lit), &sub) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var match []map[string]any
		for _, m := range f.identities {
			if m["externalId"] == sub {
				match = append(match, m)
			}
		}
		writePage(w, match)
	})

	mux.HandleFunc(mgmtBase+"/identities/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(sessionHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, mgmtBase+"/identities/")
		idx := slices.IndexFunc(f.identities, func(m map[string]any) bool { return m["id"] == id })
		if idx < 0 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"identity not found"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodDelete:
			f.deleted = append(f.deleted, id)
			f.identities = slices.Delete(f.identities, idx, idx+1)
		case http.MethodPatch:
			body := f.accept(w, r, "identity-patches")
			if body == nil {
				return
			}
			f.identities[idx]["roleAttributes"] = body["roleAttributes"]
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write([]byte(`{"data":{}}`))
	})

	mux.HandleFunc(mgmtBase+"/configs", func(w http.ResponseWriter, r *http.Request) {
		if body := f.accept(w, r, "configs"); body != nil {
			id := fmt.Sprintf("cfg-%d", len(f.created["configs"]))
			f.configs = append(f.configs, map[string]any{"id": id, "name": body["name"]})
			writeCreated(w, id)
		}
	})

	mux.HandleFunc(mgmtBase+"/service-policies", func(w http.ResponseWriter, r *http.Request) {
		if body := f.accept(w, r, "service-policies"); body != nil {
			id := fmt.Sprintf("pol-%d", len(f.created["service-policies"]))
			f.policies = append(f.policies, map[string]any{"id": id, "name": body["name"], "serviceRoles": body["serviceRoles"]})
			writeCreated(w, id)
		}
	})

	// One service's configs and policies, and the deletes of all three kinds.
	// A policy belongs to a service it names by id or selects by "#all".
	mux.HandleFunc(mgmtBase+"/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(sessionHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, mgmtBase+"/"), "/")
		switch {
		case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "services":
			i := slices.IndexFunc(f.services, func(m map[string]any) bool { return m["id"] == parts[1] })
			if i < 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var out []map[string]any
			switch parts[2] {
			case "configs":
				used, _ := f.services[i]["configs"].([]any)
				for _, c := range f.configs {
					if slices.Contains(used, c["id"]) {
						out = append(out, c)
					}
				}
			case "service-policies":
				for _, p := range f.policies {
					roles, _ := p["serviceRoles"].([]any)
					if slices.Contains(roles, any("@"+parts[1])) || slices.Contains(roles, any("#all")) {
						out = append(out, p)
					}
				}
			}
			writePage(w, out)
		case r.Method == http.MethodDelete && len(parts) == 2:
			byID := func(m map[string]any) bool { return m["id"] == parts[1] }
			switch parts[0] {
			case "services":
				f.services = slices.DeleteFunc(f.services, byID)
			case "configs":
				f.configs = slices.DeleteFunc(f.configs, byID)
			case "service-policies":
				f.policies = slices.DeleteFunc(f.policies, byID)
			}
			f.removed = append(f.removed, r.URL.Path[len(mgmtBase):])
			_, _ = w.Write([]byte(`{"data":{}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mux.HandleFunc(mgmtBase+"/edge-routers", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(sessionHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writePage(w, f.routers)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (routes() says why): the program's composer installs it once at the root, after
// the identity check that mints the validated org and before any subsystem
// registers a route. In production that composer is serve.go. In a test the test
// IS the composer, so it owes the same thing.
func compose(app *zip.App) { app.Use(cloud.Bridge(), zip.Handler(asSubject)) }

// asSubject is the test composer's stand-in for the identity boundary's one job
// the header harness cannot do: mint a principal WITH a subject. In the program
// the subject is a verified token's `sub`; here it is X-Test-Subject. The kind is
// a person's token, an application's where X-User-IsApp says so, and whatever
// X-Test-Kind names over both.
func asSubject(c *zip.Ctx) error {
	if sub := c.Header("X-Test-Subject"); sub != "" {
		kind := principal.Person
		if c.Header("X-User-IsApp") == "true" {
			kind = principal.App
		}
		if k := c.Header("X-Test-Kind"); k != "" {
			kind = principal.Kind(k)
		}
		principal.Mint(c, principal.Principal{Org: c.Org(), User: c.User(), Subject: sub, Kind: kind})
	}
	return c.Next()
}

// mountApp mounts the network surface against the fake controller, the cloud
// holding its own IAM identity.
func mountApp(t *testing.T, f *fakeZT) *zip.App {
	t.Helper()
	return mountWith(t, f, &fakeIAM{id: cloudClient, secret: "s3cr3t"}, cloudClient, "s3cr3t")
}

// mountWith mounts the surface with the cloud presenting id/secret to iam.
func mountWith(t *testing.T, f *fakeZT, iam *fakeIAM, id, secret string) *zip.App {
	t.Helper()
	t.Setenv("ZT_CONTROLLER_URL", f.server(t).URL)
	t.Setenv("IAM_URL", iam.server(t).URL)
	t.Setenv("IAM_CLIENT_ID", id)
	t.Setenv("IAM_CLIENT_SECRET", secret)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app
}

func do(t *testing.T, app *zip.App, method, path, org string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		// A validated principal: principal.Org requires a non-empty X-User-Id,
		// which the gateway sets ONLY from a verified credential. Inject it directly
		// (empty org → no user → the 401 path).
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestMeshServicesTenantScopedAndShape(t *testing.T) {
	f := &fakeZT{services: []map[string]any{
		{"id": "svc-a", "name": "checkout", "roleAttributes": []string{"org-acme"}, "encryptionRequired": true},
		{"id": "svc-b", "name": "reports", "roleAttributes": []string{"org-other"}, "encryptionRequired": false},
		{"id": "svc-c", "name": "shared", "roleAttributes": []string{"env-prod"}}, // no org → invisible to all
	}}
	app := mountApp(t, f)

	// No validated principal → 401, and the request never reaches the controller.
	if code, _ := do(t, app, http.MethodGet, "/v1/network/services", ""); code != http.StatusUnauthorized {
		t.Fatalf("no-org list want 401, got %d", code)
	}

	code, body := do(t, app, http.MethodGet, "/v1/network/services", "acme")
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	if f.authCount != 1 {
		t.Fatalf("cloud must log in once by ext-jwt with its IAM token, got %d sessions", f.authCount)
	}
	var listed struct {
		Services []meshView `json:"services"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	// acme sees ONLY its own service; the org-other and untagged services are filtered.
	if len(listed.Services) != 1 {
		t.Fatalf("acme want 1 mesh service, got %d (%+v)", len(listed.Services), listed.Services)
	}
	m := listed.Services[0]
	if m.ID != "svc-a" || m.Service != "checkout" || m.Mtls != "required" || m.Status != "active" {
		t.Fatalf("mesh view mismatch: %+v", m)
	}

	// Cross-tenant isolation: "other" sees only its own (encryptionRequired=false → "enabled").
	code, body = do(t, app, http.MethodGet, "/v1/network/services", "other")
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Services) != 1 || listed.Services[0].Mtls != "enabled" {
		t.Fatalf("other must see exactly its own service, got %d %+v", code, listed.Services)
	}

	// A tenant with no ZT footprint gets an honest empty list.
	code, body = do(t, app, http.MethodGet, "/v1/network/services", "nobody")
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Services) != 0 {
		t.Fatalf("nobody must see zero services, got %d %+v", code, listed.Services)
	}
}

func TestEdgeNodesTenantScopedShapeAndHealth(t *testing.T) {
	f := &fakeZT{routers: []map[string]any{
		{"id": "er-1", "name": "sfo-1", "roleAttributes": []string{"org-acme", "region-sfo3"}, "isOnline": true},
		{"id": "er-2", "name": "sfo-2", "roleAttributes": []string{"org-acme"}, "isOnline": false, "disabled": true},
		{"id": "er-3", "name": "nyc-1", "roleAttributes": []string{"org-acme"}, "isOnline": false},
		{"id": "er-9", "name": "theirs", "roleAttributes": []string{"org-other"}, "isOnline": true},
	}}
	app := mountApp(t, f)

	code, body := do(t, app, http.MethodGet, "/v1/network/routers", "acme")
	if code != http.StatusOK {
		t.Fatalf("routers want 200, got %d (%s)", code, body)
	}
	var out struct {
		Routers []routerView `json:"routers"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("shape: %v", err)
	}
	// acme's three routers, never org-other's.
	if len(out.Routers) != 3 {
		t.Fatalf("acme want 3 routers, got %d (%+v)", len(out.Routers), out.Routers)
	}
	byID := map[string]routerView{}
	for _, n := range out.Routers {
		byID[n.ID] = n
	}
	if n := byID["er-1"]; n.Status != "online" || n.Region != "sfo3" {
		t.Fatalf("er-1 want online/sfo3, got %+v", n)
	}
	if n := byID["er-2"]; n.Status != "disabled" || n.Region != "" {
		t.Fatalf("er-2 want disabled/no-region, got %+v", n)
	}
	if n := byID["er-3"]; n.Status != "offline" {
		t.Fatalf("er-3 want offline, got %+v", n)
	}
	if _, leaked := byID["er-9"]; leaked {
		t.Fatalf("cross-tenant leak: acme saw org-other's router er-9")
	}
}

func TestNetworksDerivedFromRouters(t *testing.T) {
	f := &fakeZT{routers: []map[string]any{
		{"id": "er-1", "name": "sfo-1", "roleAttributes": []string{"org-acme"}, "isOnline": true},
		{"id": "er-2", "name": "sfo-2", "roleAttributes": []string{"org-acme"}, "isOnline": false},
		{"id": "er-9", "name": "theirs", "roleAttributes": []string{"org-other"}, "isOnline": false},
	}}
	app := mountApp(t, f)

	// acme: one overlay network, 2 nodes, connected (≥1 online).
	code, body := do(t, app, http.MethodGet, "/v1/network", "acme")
	if code != http.StatusOK {
		t.Fatalf("networks want 200, got %d (%s)", code, body)
	}
	var listed struct {
		Networks []networkView `json:"networks"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("shape: %v", err)
	}
	if len(listed.Networks) != 1 {
		t.Fatalf("acme want 1 network, got %d", len(listed.Networks))
	}
	nw := listed.Networks[0]
	if nw.ID != "org-acme" || nw.Name != "acme" || nw.Nodes != 2 || nw.Status != "connected" {
		t.Fatalf("network view mismatch: %+v", nw)
	}

	// other: routers exist but none online → provisioning.
	code, body = do(t, app, http.MethodGet, "/v1/network", "other")
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Networks) != 1 || listed.Networks[0].Status != "provisioning" {
		t.Fatalf("other want 1 provisioning network, got %d %+v", code, listed.Networks)
	}

	// nobody: no routers → honest empty (no fabricated overlay).
	code, body = do(t, app, http.MethodGet, "/v1/network", "nobody")
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Networks) != 0 {
		t.Fatalf("nobody want zero networks, got %d %+v", code, listed.Networks)
	}

	// networks/:id round-trips for the org's own id, and 404s another id.
	code, body = do(t, app, http.MethodGet, "/v1/network/org-acme", "acme")
	if code != http.StatusOK {
		t.Fatalf("get own network want 200, got %d (%s)", code, body)
	}
	var one networkView
	if err := json.Unmarshal(body, &one); err != nil || one.ID != "org-acme" || one.Nodes != 2 {
		t.Fatalf("get network mismatch: %+v (err %v)", one, err)
	}
	// acme cannot address org-other's network id → 404 (an existence guard, not a peek).
	if code, _ := do(t, app, http.MethodGet, "/v1/network/org-other", "acme"); code != http.StatusNotFound {
		t.Fatalf("cross-tenant network id want 404, got %d", code)
	}
}

func TestReauthRetryOn401(t *testing.T) {
	f := &fakeZT{
		list401Once: true,
		services:    []map[string]any{{"id": "svc-a", "name": "checkout", "roleAttributes": []string{"org-acme"}}},
	}
	app := mountApp(t, f)

	code, body := do(t, app, http.MethodGet, "/v1/network/services", "acme")
	if code != http.StatusOK {
		t.Fatalf("after stale-session retry want 200, got %d (%s)", code, body)
	}
	if f.authCount < 2 {
		t.Fatalf("expected a re-auth after 401 (authCount>=2), got %d", f.authCount)
	}
	if !strings.Contains(string(body), `"svc-a"`) {
		t.Fatalf("retry must return the real data, got %s", body)
	}
}

// One IAM token, one session, every call after rides it.
func TestSessionReused(t *testing.T) {
	f := &fakeZT{}
	iam := &fakeIAM{id: cloudClient, secret: "s3cr3t"}
	app := mountWith(t, f, iam, cloudClient, "s3cr3t")
	for range 3 {
		if code, body := do(t, app, http.MethodGet, "/v1/network/services", "acme"); code != http.StatusOK {
			t.Fatalf("list want 200, got %d (%s)", code, body)
		}
	}
	if f.authCount != 1 || iam.mints != 1 {
		t.Fatalf("want 1 session and 1 IAM mint, got %d sessions, %d mints", f.authCount, iam.mints)
	}
}

// A session inside its renewal window is replaced before use, with the IAM
// token it was opened by.
func TestSessionRenewedBeforeExpiry(t *testing.T) {
	f := &fakeZT{ttl: tokenSkew / 3}
	iam := &fakeIAM{id: cloudClient, secret: "s3cr3t"}
	app := mountWith(t, f, iam, cloudClient, "s3cr3t")
	// The first call also runs the cloud's own ensure, a request of its own.
	if code, body := do(t, app, http.MethodGet, "/v1/network/services", "acme"); code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	before := f.authCount
	if code, body := do(t, app, http.MethodGet, "/v1/network/services", "acme"); code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	if f.authCount != before+1 || iam.mints != 1 {
		t.Fatalf("want a new session from the same IAM mint, got %d sessions after %d, %d mints", f.authCount, before, iam.mints)
	}
}

// The cloud's own identity is given the role the dial policies admit it by,
// through the same ensure the endpoint uses, once per process: the first call
// patches it, and no later call looks again.
func TestCloudEnsuresItsOwnDialRole(t *testing.T) {
	f := &fakeZT{identities: []map[string]any{{
		"id": "i-cloud", "name": subjectOf(cloudClient), "externalId": subjectOf(cloudClient),
		"isAdmin": true, "roleAttributes": []string{"env-prod"},
	}}}
	app := mountApp(t, f)
	for range 3 {
		if code, body := do(t, app, http.MethodGet, "/v1/network/services", "acme"); code != http.StatusOK {
			t.Fatalf("list want 200, got %d (%s)", code, body)
		}
	}
	patches := f.created["identity-patches"]
	if len(patches) != 1 || len(f.created["identities"]) != 0 {
		t.Fatalf("want exactly one patch and no create, got %v", f.created)
	}
	roles, _ := patches[0]["roleAttributes"].([]any)
	if len(roles) != 2 || roles[0] != "env-prod" || roles[1] != "org-admin" {
		t.Fatalf("roles = %v, want [env-prod org-admin]", roles)
	}

	// A process whose identity already carries the role writes nothing.
	f2 := &fakeZT{}
	app2 := mountApp(t, f2)
	if code, body := do(t, app2, http.MethodGet, "/v1/network/services", "acme"); code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	if len(f2.created) != 0 {
		t.Fatalf("an identity already carrying the role was written: %v", f2.created)
	}
}

// A subject the controller does not administer for is refused: reads that
// degrade answer empty, everything else answers 502, and the bearer is never
// echoed.
func TestControllerRefusesAnotherSubject(t *testing.T) {
	f := &fakeZT{services: []map[string]any{{"id": "svc-a", "name": "checkout", "roleAttributes": []string{"org-acme"}}}}
	iam := &fakeIAM{id: "stranger", secret: "s3cr3t"}
	app := mountWith(t, f, iam, "stranger", "s3cr3t")

	code, body := do(t, app, http.MethodGet, "/v1/network/services", "acme")
	if code != http.StatusBadGateway {
		t.Fatalf("refused login want 502, got %d (%s)", code, body)
	}
	if iam.last == "" || strings.Contains(string(body), iam.last) {
		t.Fatalf("the refusal echoed the bearer: %s", body)
	}
	if code, body := do(t, app, http.MethodGet, "/v1/network", "acme"); code != http.StatusOK || strings.Contains(string(body), `"id"`) {
		t.Fatalf("refused login must degrade the network read to empty, got %d %s", code, body)
	}
	if f.attempts == 0 || f.authCount != 0 {
		t.Fatalf("want refused attempts and no session, got %d attempts, %d sessions", f.attempts, f.authCount)
	}
}

// IAM refusing the cloud's own credential stops before the controller.
func TestIAMRefusalNeverReachesController(t *testing.T) {
	f := &fakeZT{}
	app := mountWith(t, f, &fakeIAM{id: cloudClient, secret: "s3cr3t"}, cloudClient, "not-the-secret-7f3a")

	code, body := do(t, app, http.MethodGet, "/v1/network/services", "acme")
	if code != http.StatusBadGateway {
		t.Fatalf("IAM refusal want 502, got %d (%s)", code, body)
	}
	if strings.Contains(string(body), "not-the-secret-7f3a") {
		t.Fatalf("the refusal echoed the client secret: %s", body)
	}
	if f.attempts != 0 {
		t.Fatalf("the controller saw %d logins without an IAM token", f.attempts)
	}
}

func TestUnconfiguredFailsClosedExceptEmptyProjections(t *testing.T) {
	t.Setenv("ZT_CONTROLLER_URL", "https://zt-controller.invalid")
	t.Setenv("IAM_CLIENT_ID", "")
	t.Setenv("IAM_CLIENT_SECRET", "")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	// COLLECTION reads degrade to an honest-empty 200: an unconfigured deployment
	// genuinely has no networks, and 503-ing every list turns a clean "nothing here
	// yet" console into an error on every page load. Nothing is disclosed by an
	// empty list, so this is presentation, not a relaxed gate.
	for _, path := range []string{"/v1/network", "/v1/network/routers"} {
		code, body := do(t, app, http.MethodGet, path, "acme")
		if code != http.StatusOK {
			t.Fatalf("unconfigured GET %s: want 200 empty, got %d", path, code)
		}
		if strings.Contains(string(body), `"id"`) {
			t.Fatalf("unconfigured GET %s must disclose nothing, got %s", path, body)
		}
	}
	// Everything else still fails closed through gate(). Mesh services are a real
	// inventory rather than a per-org projection, and a specific network lookup
	// cannot be answered honestly as "empty" — so neither degrades. If either ever
	// returns 200 here, the gate has gone, not just the list presentation.
	for _, path := range []string{"/v1/network/services", "/v1/network/zt-acme"} {
		if code, _ := do(t, app, http.MethodGet, path, "acme"); code != http.StatusServiceUnavailable {
			t.Fatalf("unconfigured GET %s: want 503, got %d", path, code)
		}
	}
}
