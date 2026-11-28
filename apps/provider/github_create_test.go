package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// newRepoWriteHub builds a test GitHub server supporting OAuth user connect,
// App installation lookup/token minting, repo creation (POST /user/repos and
// POST /orgs/{org}/repos), existing repo lookup (GET /repos/{owner}/{name}),
// and forking (POST /repos/{owner}/{name}/forks).
type repoWriteHub struct {
	mu           sync.Mutex
	base         *fakeHub
	installTypes map[int64]string
	installLogin map[int64]string
	created      map[string]githubRepoPayload // lower(full_name) -> repo
	lastForkPath string
	lastForkBody map[string]string
	lastForkAuth string
	lastCreateEp string
	lastCreateBy string
}

func withRepoWriteHub(t *testing.T) (*repoWriteHub, *zip.App) {
	t.Helper()
	h, baseSrv := newFakeHub(t)
	rw := &repoWriteHub{
		base:         h,
		installTypes: map[int64]string{},
		installLogin: map[int64]string{},
		created:      map[string]githubRepoPayload{},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

		// GET /app/installations/{id}
		if r.Method == http.MethodGet && strings.HasPrefix(p, "/app/installations/") && strings.Count(strings.TrimPrefix(p, "/app/installations/"), "/") == 0 {
			idStr := strings.TrimPrefix(p, "/app/installations/")
			var id int64
			for _, c := range idStr {
				if c >= '0' && c <= '9' {
					id = id*10 + int64(c-'0')
				}
			}
			rw.mu.Lock()
			typ := rw.installTypes[id]
			login := rw.installLogin[id]
			rw.mu.Unlock()
			if typ == "" {
				typ = "Organization"
			}
			if login == "" {
				login = "acme-gh"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": id,
				"account": map[string]any{
					"login":    login,
					"type":     typ,
					"html_url": "https://github.com/" + login,
				},
				"repository_selection": "all",
			})
			return
		}

		// POST /user/repos
		if r.Method == http.MethodPost && p == "/user/repos" {
			h.mu.Lock()
			login := h.access[auth]
			h.mu.Unlock()
			if login == "" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
				return
			}
			var in struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Private     bool   `json:"private"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &in)
			full := login + "/" + in.Name
			key := strings.ToLower(full)
			rw.mu.Lock()
			rw.lastCreateEp = p
			rw.lastCreateBy = auth
			if _, exists := rw.created[key]; exists {
				rw.mu.Unlock()
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"Repository creation failed.","errors":[{"resource":"Repository","code":"custom","field":"name","message":"name already exists on this account"}]}`))
				return
			}
			payload := githubRepoPayload{
				FullName:      full,
				HTMLURL:       "https://github.com/" + full,
				CloneURL:      "https://github.com/" + full + ".git",
				DefaultBranch: "main",
				Private:       in.Private,
			}
			rw.created[key] = payload
			rw.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(payload)
			return
		}

		// POST /orgs/{org}/repos
		if r.Method == http.MethodPost && strings.HasPrefix(p, "/orgs/") && strings.HasSuffix(p, "/repos") {
			ghOrg := strings.TrimSuffix(strings.TrimPrefix(p, "/orgs/"), "/repos")
			h.mu.Lock()
			login := h.access[auth]
			h.mu.Unlock()
			isInst := strings.HasPrefix(auth, "ghs-inst-")
			if login == "" && !isInst {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
				return
			}
			var in struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Private     bool   `json:"private"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &in)
			full := ghOrg + "/" + in.Name
			key := strings.ToLower(full)
			rw.mu.Lock()
			rw.lastCreateEp = p
			rw.lastCreateBy = auth
			if _, exists := rw.created[key]; exists {
				rw.mu.Unlock()
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"Repository creation failed.","errors":[{"resource":"Repository","code":"custom","field":"name","message":"name already exists on this account"}]}`))
				return
			}
			payload := githubRepoPayload{
				FullName:      full,
				HTMLURL:       "https://github.com/" + full,
				CloneURL:      "https://github.com/" + full + ".git",
				DefaultBranch: "main",
				Private:       in.Private,
			}
			rw.created[key] = payload
			rw.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(payload)
			return
		}

		// POST /repos/{owner}/{name}/forks
		if r.Method == http.MethodPost && strings.HasPrefix(p, "/repos/") && strings.HasSuffix(p, "/forks") {
			h.mu.Lock()
			login := h.access[auth]
			h.mu.Unlock()
			isInst := strings.HasPrefix(auth, "ghs-inst-")
			if login == "" && !isInst {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
				return
			}
			trimmed := strings.TrimSuffix(strings.TrimPrefix(p, "/repos/"), "/forks")
			_, origName, _ := strings.Cut(trimmed, "/")
			var body map[string]string
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &body)
			}
			dstOwner := login
			if dstOwner == "" {
				dstOwner = "acme-gh"
			}
			if o := body["organization"]; o != "" {
				dstOwner = o
			}
			dstName := origName
			if n := body["name"]; n != "" {
				dstName = n
			}
			rw.mu.Lock()
			rw.lastForkPath = p
			rw.lastForkBody = body
			rw.lastForkAuth = auth
			rw.mu.Unlock()
			full := dstOwner + "/" + dstName
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"full_name":      full,
				"html_url":       "https://github.com/" + full,
				"clone_url":      "https://github.com/" + full + ".git",
				"default_branch": "main",
				"created_at":     "2026-11-28T00:00:00Z",
				"updated_at":     "2026-11-28T00:00:00Z",
			})
			return
		}

		// GET /repos/{owner}/{name} for created repos
		if r.Method == http.MethodGet && strings.HasPrefix(p, "/repos/") && strings.Count(strings.TrimPrefix(p, "/repos/"), "/") == 1 {
			full := strings.TrimPrefix(p, "/repos/")
			rw.mu.Lock()
			payload, ok := rw.created[strings.ToLower(full)]
			rw.mu.Unlock()
			if ok {
				_ = json.NewEncoder(w).Encode(payload)
				return
			}
		}

		// Delegate everything else (OAuth, installations, repo listing) to fakeHub.
		h.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	baseSrv.Close()

	withGithubApp(t, srv)
	t.Setenv(githubClientIDEnv, "Iv23test")
	t.Setenv(githubClientSecretEnv, "shh")
	prevWeb := githubWebBase
	githubWebBase = srv.URL
	t.Cleanup(func() { githubWebBase = prevWeb })
	app := newApp(t, newKMS(t))
	repoIndex = newIndexCache()
	t.Cleanup(func() { repoIndex = newIndexCache() })
	return rw, app
}

func TestGitHubCreateRepoPersonalAndIdempotent(t *testing.T) {
	rw, app := withRepoWriteHub(t)

	// Without personal GitHub connected → 412 Precondition Failed with clear guidance.
	res := asPerson(t, app, http.MethodPost, "/v1/provider/github/repos", "acme", "alice", false,
		map[string]any{"name": "starter"})
	if res.Code != http.StatusPreconditionFailed || !strings.Contains(string(res.Body), "/v1/provider/github/user/connect") {
		t.Fatalf("unconnected personal create want 412 with connect hint, got %d (%s)", res.Code, res.Body)
	}

	// Connect alice's personal GitHub account.
	connectAs(t, app, "acme", "alice", "octo-alice")

	// Capture link_repo outbound mirror registration.
	var mirrored []*client.MirrorIn
	prevMirror := linkGitMirror
	linkGitMirror = func(_ context.Context, in *client.MirrorIn) (*client.Mirrored, error) {
		cp := *in
		mirrored = append(mirrored, &cp)
		return &client.Mirrored{Repo: in.Repo}, nil
	}
	t.Cleanup(func() { linkGitMirror = prevMirror })

	// Create personal repo with link_repo.
	res = asPerson(t, app, http.MethodPost, "/v1/provider/github/repos", "acme", "alice", false,
		map[string]any{
			"name":        "starter",
			"description": "My starter",
			"private":     true,
			"link_repo":   "starter-local",
		})
	if res.Code != http.StatusCreated {
		t.Fatalf("personal create want 201, got %d (%s)", res.Code, res.Body)
	}
	var out githubCreateRepoOut
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.FullName != "octo-alice/starter" || out.CloneURL != "https://github.com/octo-alice/starter.git" ||
		out.DefaultBranch != "main" || !out.Private || out.Existing || !out.Linked {
		t.Fatalf("unexpected create output: %+v", out)
	}
	if rw.lastCreateEp != "/user/repos" {
		t.Fatalf("endpoint = %q, want /user/repos", rw.lastCreateEp)
	}
	if len(mirrored) != 1 || mirrored[0].Repo != "starter-local" || mirrored[0].URL != out.CloneURL || !mirrored[0].Enabled {
		t.Fatalf("mirror link calls = %+v", mirrored)
	}

	// Re-creating the same repo triggers GitHub 422 → idempotent GET returns existing:true.
	res = asPerson(t, app, http.MethodPost, "/v1/provider/github/repos", "acme", "alice", false,
		map[string]any{"name": "starter", "linkRepo": "starter-camel"})
	if res.Code != http.StatusCreated {
		t.Fatalf("idempotent create want 201, got %d (%s)", res.Code, res.Body)
	}
	var again githubCreateRepoOut
	if err := json.Unmarshal(res.Body, &again); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !again.Existing || again.FullName != "octo-alice/starter" || again.CloneURL != "https://github.com/octo-alice/starter.git" || !again.Linked {
		t.Fatalf("idempotent output want existing=true, got %+v", again)
	}
	if len(mirrored) != 2 || mirrored[1].Repo != "starter-camel" {
		t.Fatalf("camelCase linkRepo not honored: %+v", mirrored)
	}
}

func TestGitHubCreateRepoOrgAuthResolution(t *testing.T) {
	rw, app := withRepoWriteHub(t)

	// Bind two installations to acme: one Organization ("acme-org") and one User ("bob-user").
	rw.installTypes[101] = "Organization"
	rw.installLogin[101] = "acme-org"
	rw.installTypes[102] = "User"
	rw.installLogin[102] = "bob-user"
	if err := mounted.State.store.Upsert(context.Background(), Connection{
		Org: "acme", Provider: "github", Label: "acme-org", ExternalID: "101", AccountLabel: "acme-org",
	}); err != nil {
		t.Fatal(err)
	}
	if err := mounted.State.store.Upsert(context.Background(), Connection{
		Org: "acme", Provider: "github", Label: "bob-user", ExternalID: "102", AccountLabel: "bob-user",
	}); err != nil {
		t.Fatal(err)
	}

	// No user token, target is an Organization installation → uses installation token!
	res := asPerson(t, app, http.MethodPost, "/v1/provider/github/repos", "acme", "carol", true,
		map[string]any{"name": "org-repo", "org": "acme-org"})
	if res.Code != http.StatusCreated {
		t.Fatalf("org install create want 201, got %d (%s)", res.Code, res.Body)
	}
	var out githubCreateRepoOut
	_ = json.Unmarshal(res.Body, &out)
	if out.FullName != "acme-org/org-repo" || rw.lastCreateEp != "/orgs/acme-org/repos" || !strings.HasPrefix(rw.lastCreateBy, "ghs-inst-") {
		t.Fatalf("unexpected org create: out=%+v ep=%q auth=%q", out, rw.lastCreateEp, rw.lastCreateBy)
	}

	// No user token, target installation is Type="User" → refused 412!
	res = asPerson(t, app, http.MethodPost, "/v1/provider/github/repos", "acme", "carol", true,
		map[string]any{"name": "bad-repo", "org": "bob-user"})
	if res.Code != http.StatusPreconditionFailed {
		t.Fatalf("user-type installation create want 412, got %d (%s)", res.Code, res.Body)
	}

	// Invalid names → 400.
	if r := asPerson(t, app, http.MethodPost, "/v1/provider/github/repos", "acme", "carol", true,
		map[string]any{"name": "../evil"}); r.Code != http.StatusBadRequest {
		t.Fatalf("invalid name want 400, got %d", r.Code)
	}
}

func TestGitHubForkPublicOwnerRepoAndBareRepo(t *testing.T) {
	rw, app := withRepoWriteHub(t)

	// Seed a granted repo under installation 201 for acme.
	rw.base.seed(201, "acme-gh", 1, "widget")
	if err := mounted.State.store.Upsert(context.Background(), Connection{
		Org: "acme", Provider: "github", Label: "acme-gh", ExternalID: "201", AccountLabel: "acme-gh",
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Public owner/repo WITHOUT user token and NOT granted → 412.
	res := asPerson(t, app, http.MethodPost, "/v1/provider/github/fork", "acme", "alice", false,
		map[string]any{"repo": "torvalds/linux"})
	if res.Code != http.StatusPreconditionFailed {
		t.Fatalf("unconnected public fork want 412, got %d (%s)", res.Code, res.Body)
	}

	// 2. Connect alice's personal GitHub → fork public owner/repo with optional org & name!
	connectAs(t, app, "acme", "alice", "octo-alice")
	res = asPerson(t, app, http.MethodPost, "/v1/provider/github/fork", "acme", "alice", false,
		map[string]any{"repo": "hanzoai/cloud", "org": "alice-org", "name": "cloud-fork"})
	if res.Code != http.StatusOK {
		t.Fatalf("public owner/repo fork want 200, got %d (%s)", res.Code, res.Body)
	}
	var out githubForkOut
	_ = json.Unmarshal(res.Body, &out)
	if out.FullName != "alice-org/cloud-fork" || out.CloneURL != "https://github.com/alice-org/cloud-fork.git" || out.Existing {
		t.Fatalf("unexpected public fork output: %+v", out)
	}
	if rw.lastForkPath != "/repos/hanzoai/cloud/forks" || rw.lastForkBody["organization"] != "alice-org" || rw.lastForkBody["name"] != "cloud-fork" {
		t.Fatalf("unexpected fork call: path=%q body=%+v", rw.lastForkPath, rw.lastForkBody)
	}

	// 3. owner/repo WITHOUT user token, when granted by org installation → falls back cleanly.
	res = asPerson(t, app, http.MethodPost, "/v1/provider/github/fork", "acme", "carol", true,
		map[string]any{"repo": "acme-gh/widget000"})
	if res.Code != http.StatusOK {
		t.Fatalf("granted owner/repo fallback fork want 200, got %d (%s)", res.Code, res.Body)
	}
	if !strings.HasPrefix(rw.lastForkAuth, "ghs-inst-") {
		t.Fatalf("expected installation token fallback, got auth %q", rw.lastForkAuth)
	}

	// 4. Bare repo name keeps exact existing resolveGrantedRepo behavior.
	res = asPerson(t, app, http.MethodPost, "/v1/provider/github/fork", "acme", "carol", true,
		map[string]any{"repo": "widget000"})
	if res.Code != http.StatusOK {
		t.Fatalf("bare granted fork want 200, got %d (%s)", res.Code, res.Body)
	}
	if rw.lastForkPath != "/repos/acme-gh/widget000/forks" {
		t.Fatalf("bare fork path = %q, want /repos/acme-gh/widget000/forks", rw.lastForkPath)
	}

	// 5. Invalid owner/repo shapes → 400.
	for _, bad := range []string{"a/b/c", "../evil", "owner/", "/repo", ""} {
		if r := asPerson(t, app, http.MethodPost, "/v1/provider/github/fork", "acme", "alice", false,
			map[string]any{"repo": bad}); r.Code != http.StatusBadRequest {
			t.Fatalf("repo=%q want 400, got %d (%s)", bad, r.Code, r.Body)
		}
	}
}

// TestReposConnectedTrueWhenOrgInstallationHasZeroRepos pins the fix for
// org=mcphun: an org with an active connected GitHub App installation (even
// with 0 repos yet) must report connected:true to its org admin without leaking
// across orgs or to unconnected non-admin members.
func TestReposConnectedTrueWhenOrgInstallationHasZeroRepos(t *testing.T) {
	_, app := withRepoWriteHub(t)

	// Bind installation 301 ("mcphun-spec") with ZERO repos to org "mcphun".
	if err := mounted.State.store.Upsert(context.Background(), Connection{
		Org: "mcphun", Provider: "github", Label: "mcphun-spec", ExternalID: "301", AccountLabel: "mcphun-spec",
	}); err != nil {
		t.Fatal(err)
	}

	// Org admin in mcphun sees repos:[] and connected:true.
	p := listRepos(t, app, "mcphun", "founder", true, "")
	if !p.Connected || len(p.Repos) != 0 || p.Total != 0 {
		t.Fatalf("mcphun admin with empty active installation want connected=true repos=[], got %+v", p)
	}

	// Unconnected org admin in another org ("other") sees connected:false.
	pOther := listRepos(t, app, "other", "founder", true, "")
	if pOther.Connected || len(pOther.Repos) != 0 {
		t.Fatalf("other org with no installation want connected=false, got %+v", pOther)
	}

	// Unconnected non-admin member in mcphun sees repos:[] and connected:false.
	pMember := listRepos(t, app, "mcphun", "member", false, "")
	if pMember.Connected || len(pMember.Repos) != 0 {
		t.Fatalf("unconnected non-admin member want connected=false, got %+v", pMember)
	}

	// Connected user with 0 repos sees connected:true.
	connectAs(t, app, "mcphun", "member", "octo-member")
	pConn := listRepos(t, app, "mcphun", "member", false, "")
	if !pConn.Connected || len(pConn.Repos) != 0 {
		t.Fatalf("connected user with 0 repos want connected=true, got %+v", pConn)
	}
}
