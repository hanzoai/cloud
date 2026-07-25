package authors

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	sqlitedrv "github.com/hanzoai/sqlite"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// fakeCommerce is an in-memory commerce ledger: it records deposits per org (the
// wallet balance) and lets a test SET a deploying org's metered spend (the accrual
// base). It is the money-seam stand-in that lets the tests PROVE royalty accrues
// (spend × share) and a credits payout moves a wallet — without a live commerce.
type fakeCommerce struct {
	mu       sync.Mutex
	balance  map[string]int64
	spend    map[string]int64
	deposits int
	failDep  bool
	seq      int
}

func newFakeCommerce() *fakeCommerce {
	return &fakeCommerce{balance: map[string]int64{}, spend: map[string]int64{}}
}

func (f *fakeCommerce) configured() bool { return true }

func (f *fakeCommerce) deposit(_ context.Context, org, _ string, amountCents int64, _, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDep {
		return "", errUnconfigured
	}
	f.balance[org] += amountCents
	f.deposits++
	f.seq++
	return "txn_test_" + org + "_" + strconv.Itoa(f.seq), nil
}

func (f *fakeCommerce) spendCents(_ context.Context, org, _ string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spend[org], nil
}

func (f *fakeCommerce) setSpend(org string, cents int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spend[org] = cents
}

func (f *fakeCommerce) bal(org string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.balance[org]
}

func (f *fakeCommerce) depositCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deposits
}

// fakeGitHub is the identity stand-in. A test can register a LINKED account for an
// org (login + token), mark specific owner/repo pairs as admin-owned by a token, and
// stage hanzo.json file contents per repo+branch. This lets the tests PROVE both
// verification methods without touching IAM or GitHub.
type fakeGitHub struct {
	mu     sync.Mutex
	linked map[string]ghLink // org → linked account
	admin  map[string]bool   // "token|owner/repo" → admin
	files  map[string][]byte // "owner/repo@branch/path" → body
}

type ghLink struct {
	login string
	token string
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{linked: map[string]ghLink{}, admin: map[string]bool{}, files: map[string][]byte{}}
}

func (g *fakeGitHub) setLinked(org, login, token string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.linked[org] = ghLink{login: login, token: token}
}

func (g *fakeGitHub) setAdmin(token, owner, repo string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.admin[token+"|"+owner+"/"+repo] = true
}

func (g *fakeGitHub) setFile(owner, repo, branch, path string, body []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.files[owner+"/"+repo+"@"+branch+"/"+path] = body
}

// linkedAccount ignores the provider (the fake keys links by org) so the same fake
// serves both the github and gitlab flows.
func (g *fakeGitHub) linkedAccount(_ context.Context, _, org, _ string) (string, string, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	l, ok := g.linked[org]
	if !ok {
		return "", "", false, nil
	}
	return l.login, l.token, true, nil
}

func (g *fakeGitHub) repoAdmin(_ context.Context, _, token, owner, repo string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.admin[token+"|"+owner+"/"+repo], nil
}

func (g *fakeGitHub) fetchFile(_ context.Context, _, owner, repo, branch, path string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.files[owner+"/"+repo+"@"+branch+"/"+path], nil
}

// TestMain makes this store-backed suite build-tag agnostic, exactly as the root
// package's main_test.go does: an encryption-capable build refuses to open the data
// plane without a master key, a pure-Go build refuses a key at all. Supply a throwaway
// dev key ONLY when the build can encrypt and the environment did not already provide
// one, so CI's real key is never overridden.
func TestMain(m *testing.M) {
	if sqlitedrv.EncryptionAvailable() && os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" {
		_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") // 32 zero bytes, dev-only
	}
	os.Exit(m.Run())
}

// mount builds an authors app backed by a fresh store + injected fakes, returning the
// app, the service, and the fakes for assertions.
func mount(t *testing.T) (*zip.App, *cloud.Service[state], *fakeCommerce, *fakeGitHub) {
	t.Helper()
	store, err := openStore(t.TempDir() + "/authors.db")
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fc := newFakeCommerce()
	fg := newFakeGitHub()
	s := &cloud.Service[state]{
		Base: cloud.NewBase(cloud.Deps{Logger: luxlog.New("test")}, "authors"),
		State: state{
			store:         store,
			commerce:      fc,
			forge:         fg,
			badgeBase:     "https://hanzo.app",
			maintainerOrg: "hanzo",
		},
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, s)
	return app, s, fc, fg
}

// req drives one HTTP request. org sets a VALIDATED principal (X-Org-Id + X-User-Id,
// the Tenant() gate); admin additionally sets X-User-IsAdmin.
func req(t *testing.T, app *zip.App, method, path, org string, admin bool, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	hr := httptest.NewRequest(method, path, r)
	if body != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		hr.Header.Set("X-Org-Id", org)
		hr.Header.Set("X-User-Id", "u_"+org)
	}
	if admin {
		hr.Header.Set("X-User-IsAdmin", "true")
	}
	// Generous ceiling — the fiber default is 1s, which flakes under machine load.
	resp, err := app.Fiber().Test(hr, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// envData pulls the `data` object out of an admin envelope {status,msg,data}.
func envData(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var env struct {
		Status string                     `json:"status"`
		Data   map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, body)
	}
	return env.Data
}

// connectOrg connects `org` as an author (optionally with a supplied login), returning
// the author id + verify code.
func connectOrg(t *testing.T, app *zip.App, s *cloud.Service[state], org, login string) (id, verifyCode string) {
	t.Helper()
	code, body := req(t, app, http.MethodPost, "/v1/authors/connect", org, false, map[string]any{"githubLogin": login})
	if code != http.StatusCreated {
		t.Fatalf("connect(%s) want 201, got %d (%s)", org, code, body)
	}
	var r struct {
		ID         string `json:"id"`
		VerifyCode string `json:"verifyCode"`
		Status     string `json:"status"`
	}
	_ = json.Unmarshal(body, &r)
	if r.ID == "" || r.VerifyCode == "" || r.Status != StatusConnected {
		t.Fatalf("connect(%s) bad response: %s", org, body)
	}
	return r.ID, r.VerifyCode
}

// approve admits an author to earning via the admin route.
func approve(t *testing.T, app *zip.App, id string) {
	t.Helper()
	if st, body := req(t, app, http.MethodPost, "/v1/admin/authors/"+id+"/approve", "admin", true, nil); st != http.StatusOK {
		t.Fatalf("approve(%s) want 200, got %d (%s)", id, st, body)
	}
}

// TestNormalizeRepo: every cosmetic form of a GitHub/GitLab reference collapses to
// the ONE canonical <host>/owner/name; a bare owner/name defaults to github.com;
// junk normalizes to "".
func TestNormalizeRepo(t *testing.T) {
	want := "github.com/acme/widgets"
	for _, in := range []string{
		"https://github.com/acme/widgets",
		"https://github.com/acme/widgets.git",
		"http://www.github.com/Acme/Widgets/",
		"github.com/acme/widgets",
		"acme/widgets",
		"git@github.com:acme/widgets.git",
		"https://github.com/acme/widgets?tab=readme",
	} {
		if got := normalizeRepo(in); got != want {
			t.Fatalf("normalizeRepo(%q) = %q, want %q", in, got, want)
		}
	}
	// GitLab is a first-class forge: its references collapse to gitlab.com/owner/name.
	wantGL := "gitlab.com/acme/widgets"
	for _, in := range []string{
		"https://gitlab.com/acme/widgets",
		"https://gitlab.com/Acme/Widgets.git",
		"gitlab.com/acme/widgets",
		"git@gitlab.com:acme/widgets.git",
	} {
		if got := normalizeRepo(in); got != wantGL {
			t.Fatalf("normalizeRepo(%q) = %q, want %q", in, got, wantGL)
		}
	}
	for _, in := range []string{"", "  ", "not a repo", "https://bitbucket.org/a/b", "github.com/onlyowner"} {
		if got := normalizeRepo(in); got != "" {
			t.Fatalf("normalizeRepo(%q) = %q, want empty", in, got)
		}
	}
}

// TestFileProvesCode: hanzo.json JSON field OR a bare substring both prove the code;
// a wrong/empty code does not.
func TestFileProvesCode(t *testing.T) {
	code := "avc_abc123"
	if !fileProvesCode([]byte(`{"hanzoAuthorCode":"avc_abc123"}`), code) {
		t.Fatal("json field should prove")
	}
	if !fileProvesCode([]byte(`# verify\navc_abc123\n`), code) {
		t.Fatal("substring should prove")
	}
	if fileProvesCode([]byte(`{"hanzoAuthorCode":"other"}`), code) {
		t.Fatal("wrong code proved")
	}
	if fileProvesCode([]byte(``), code) || fileProvesCode([]byte(`avc_abc123`), "") {
		t.Fatal("empty proved")
	}
}

// TestConnectIdempotentAndLinkedIdentity: connect is one-author-per-org (first wins,
// re-link updates login); an IAM-linked account overrides the supplied login and marks
// the identity verified. No principal → 403.
func TestConnectIdempotentAndLinkedIdentity(t *testing.T) {
	app, s, _, fg := mount(t)
	ctx := context.Background()

	if code, _ := req(t, app, http.MethodPost, "/v1/authors/connect", "", false, map[string]any{}); code != http.StatusForbidden {
		t.Fatalf("no-principal connect want 403, got %d", code)
	}
	// No supplied login + no linked account → 400.
	if code, _ := req(t, app, http.MethodPost, "/v1/authors/connect", "orgA", false, map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("no-login connect want 400, got %d", code)
	}

	// First connect with a supplied login → 201 connected, unverified identity.
	id, _ := connectOrg(t, app, s, "orgA", "AcmeDev")
	a, _ := s.State.store.GetByID(ctx, id)
	if a.GithubLogin != "acmedev" || a.VerifiedAt != 0 {
		t.Fatalf("supplied login connect: login=%q verifiedAt=%d", a.GithubLogin, a.VerifiedAt)
	}

	// Re-connect with an IAM-linked account → 200, login re-linked to the verified one.
	fg.setLinked("orgA", "acme-official", "tok_a")
	code, body := req(t, app, http.MethodPost, "/v1/authors/connect", "orgA", false, map[string]any{"githubLogin": "ignored"})
	if code != http.StatusOK {
		t.Fatalf("re-connect want 200, got %d (%s)", code, body)
	}
	var re struct {
		Created  bool   `json:"created"`
		Verified bool   `json:"verified"`
		Login    string `json:"githubLogin"`
	}
	_ = json.Unmarshal(body, &re)
	if re.Created || !re.Verified || re.Login != "acme-official" {
		t.Fatalf("re-connect not idempotent-relink-verify: %+v (%s)", re, body)
	}
	a2, _ := s.State.store.GetByID(ctx, id)
	if a2.ID != a.ID || a2.VerifiedAt == 0 {
		t.Fatalf("re-connect broke identity: %+v", a2)
	}
}

// TestVerifyRepoBothMethods is the CORE verification proof: the OAuth admin-check and
// the hanzo.json file method each verify a repo; a repo we can't prove is 422; a repo
// owned by another author is 409.
func TestVerifyRepoBothMethods(t *testing.T) {
	app, s, _, fg := mount(t)

	// orgA connects and verifies via OAuth (linked token has admin on acme/widgets).
	idA, _ := connectOrg(t, app, s, "orgA", "acmedev")
	fg.setLinked("orgA", "acmedev", "tok_a")
	fg.setAdmin("tok_a", "acme", "widgets")
	st, body := req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgA", false, map[string]any{"repoUrl": "https://github.com/acme/widgets"})
	if st != http.StatusCreated {
		t.Fatalf("oauth verify want 201, got %d (%s)", st, body)
	}
	var vr struct {
		Repo struct {
			RepoURL       string `json:"repoUrl"`
			Verified      bool   `json:"verified"`
			Method        string `json:"method"`
			BadgeMarkdown string `json:"badgeMarkdown"`
		} `json:"repo"`
	}
	_ = json.Unmarshal(body, &vr)
	if vr.Repo.RepoURL != "github.com/acme/widgets" || !vr.Repo.Verified || vr.Repo.Method != MethodOAuth {
		t.Fatalf("oauth verify wrong: %+v", vr.Repo)
	}
	if vr.Repo.BadgeMarkdown != "[![Deploy on Hanzo](https://hanzo.app/deploy-badge.svg)](https://hanzo.app/new?template=https://github.com/acme/widgets)" {
		t.Fatalf("badge markdown wrong: %q", vr.Repo.BadgeMarkdown)
	}

	// orgB connects and verifies a DIFFERENT repo via the FILE method (hanzo.json on
	// main contains its verify code; no linked token).
	idB, codeB := connectOrg(t, app, s, "orgB", "bobdev")
	fg.setFile("bob", "tool", "main", verifyFile, []byte(`{"hanzoAuthorCode":"`+codeB+`"}`))
	st, body = req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgB", false, map[string]any{"repoUrl": "bob/tool"})
	if st != http.StatusCreated {
		t.Fatalf("file verify want 201, got %d (%s)", st, body)
	}
	_ = json.Unmarshal(body, &vr)
	if vr.Repo.Method != MethodFile || !vr.Repo.Verified {
		t.Fatalf("file verify wrong: %+v", vr.Repo)
	}

	// A repo orgB can't prove (no token, no file) → 422.
	if st, _ := req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgB", false, map[string]any{"repoUrl": "bob/secret"}); st != http.StatusUnprocessableEntity {
		t.Fatalf("unprovable verify want 422, got %d", st)
	}
	// orgB tries to claim orgA's already-verified repo → 409.
	fg.setFile("acme", "widgets", "main", verifyFile, []byte(codeB))
	if st, _ := req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgB", false, map[string]any{"repoUrl": "acme/widgets"}); st != http.StatusConflict {
		t.Fatalf("cross-author claim want 409, got %d", st)
	}
	// Verify-before-connect → 400.
	if st, _ := req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgZ", false, map[string]any{"repoUrl": "z/z"}); st != http.StatusBadRequest {
		t.Fatalf("verify-before-connect want 400, got %d", st)
	}
	// Malformed repo → 400.
	if st, _ := req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgA", false, map[string]any{"repoUrl": "not a repo"}); st != http.StatusBadRequest {
		t.Fatalf("malformed repo want 400, got %d", st)
	}
	_ = idA
	_ = idB
}

// TestRecordDeployAttribution: a deploy of a VERIFIED author repo records an edge
// (idempotent); a deploy of a non-author repo is a recorded:false no-op; a self-deploy
// is recorded but flagged self.
func TestRecordDeployAttribution(t *testing.T) {
	app, s, _, fg := mount(t)
	ctx := context.Background()
	idA, _ := connectOrg(t, app, s, "orgA", "acmedev")
	fg.setLinked("orgA", "acmedev", "tok_a")
	fg.setAdmin("tok_a", "acme", "widgets")
	req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgA", false, map[string]any{"repoUrl": "acme/widgets"})

	// No principal → 403.
	if st, _ := req(t, app, http.MethodPost, "/v1/authors/deploys/record", "", false, map[string]any{"repoUrl": "acme/widgets", "project": "p1"}); st != http.StatusForbidden {
		t.Fatalf("no-principal deploy want 403, got %d", st)
	}
	// Deploy of a NON-author repo → recorded:false, 200.
	st, body := req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgB", false, map[string]any{"repoUrl": "someone/else", "project": "p0"})
	if st != http.StatusOK {
		t.Fatalf("non-author deploy want 200, got %d", st)
	}
	if recorded(t, body) {
		t.Fatalf("non-author deploy reported recorded=true")
	}
	// Missing project → 400.
	if st, _ := req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgB", false, map[string]any{"repoUrl": "acme/widgets"}); st != http.StatusBadRequest {
		t.Fatalf("missing project want 400, got %d", st)
	}

	// orgB deploys acme/widgets → 201 recorded, not self.
	st, body = req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgB", false, map[string]any{"repoUrl": "https://github.com/acme/widgets", "project": "proj-b"})
	if st != http.StatusCreated || !recorded(t, body) {
		t.Fatalf("orgB deploy want 201 recorded, got %d (%s)", st, body)
	}
	var dv struct {
		Created bool `json:"created"`
		Self    bool `json:"self"`
	}
	_ = json.Unmarshal(body, &dv)
	if !dv.Created || dv.Self {
		t.Fatalf("orgB deploy view wrong: %+v", dv)
	}
	// Re-deploy same project/org → 200 idempotent (not created).
	st, body = req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgB", false, map[string]any{"repoUrl": "acme/widgets", "project": "proj-b"})
	_ = json.Unmarshal(body, &dv)
	if st != http.StatusOK || dv.Created {
		t.Fatalf("re-deploy want 200 not-created, got %d %+v", st, dv)
	}
	// Author's OWN org deploys → recorded but self=true.
	st, body = req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgA", false, map[string]any{"repoUrl": "acme/widgets", "project": "proj-a"})
	_ = json.Unmarshal(body, &dv)
	if st != http.StatusCreated || !dv.Self {
		t.Fatalf("self-deploy want 201 self=true, got %d %+v", st, dv)
	}
	// The author now has 2 deploy events; distinct earning orgs excludes self (orgA).
	orgs, _ := s.State.store.DistinctDeployingOrgs(ctx, idA, "orgA", 100)
	if len(orgs) != 1 || orgs[0] != "orgB" {
		t.Fatalf("distinct deploying orgs = %v, want [orgB]", orgs)
	}
}

// TestSweepAccruesSpendTimesShareIdempotent is the CORE earning proof: after a
// deploying org makes metered spend, the sweep accrues royalty = spend × share into
// the author's balance, at-most-once per period; a self-deploy never accrues.
func TestSweepAccruesSpendTimesShareIdempotent(t *testing.T) {
	app, s, fc, fg := mount(t)
	ctx := context.Background()
	idA, _ := connectOrg(t, app, s, "orgA", "acmedev")
	fg.setLinked("orgA", "acmedev", "tok_a")
	fg.setAdmin("tok_a", "acme", "widgets")
	req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgA", false, map[string]any{"repoUrl": "acme/widgets"})
	req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgB", false, map[string]any{"repoUrl": "acme/widgets", "project": "proj-b"})
	req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgA", false, map[string]any{"repoUrl": "acme/widgets", "project": "proj-a"}) // self

	// Not approved yet → sweep accrues nothing (sweep set is approved-only).
	code, body := req(t, app, http.MethodPost, "/v1/admin/authors/sweep", "admin", true, nil)
	if code != http.StatusOK || sweptAccrued(t, body) != 0 {
		t.Fatalf("pre-approve sweep want 0, got %d (%s)", code, body)
	}
	approve(t, app, idA)

	// Spend by orgB $100 (10000c); orgA (self) spends $999 but must NOT accrue.
	fc.setSpend("orgB", 10000)
	fc.setSpend("orgA", 99900)

	code, body = req(t, app, http.MethodPost, "/v1/admin/authors/sweep", "admin", true, nil)
	if code != http.StatusOK || sweptAccrued(t, body) != 1 {
		t.Fatalf("accrual sweep want 1, got %d (%s)", code, body)
	}
	a, _ := s.State.store.GetByID(ctx, idA)
	const want = 10000 * defaultShareBps / bpsDenom // 2000 (20% share)
	if a.AccruedCents != want || a.PendingCents() != want {
		t.Fatalf("accrued=%d pending=%d, want %d (orgB spend×share, self excluded)", a.AccruedCents, a.PendingCents(), want)
	}

	// Idempotent: a re-sweep in the SAME period accrues nothing more.
	req(t, app, http.MethodPost, "/v1/admin/authors/sweep", "admin", true, nil)
	a2, _ := s.State.store.GetByID(ctx, idA)
	if a2.AccruedCents != want {
		t.Fatalf("double-accrual! accrued=%d, want %d", a2.AccruedCents, want)
	}
	if fc.depositCount() != 0 {
		t.Fatalf("accrual issued a deposit (%d) — it must not", fc.depositCount())
	}
}

// TestLazyAccrualOnAuthorRead proves the author's OWN GET /v1/authors runs the accrual
// sweep (self-updating dashboard) and surfaces the badge snippet + repos/deploys.
func TestLazyAccrualOnAuthorRead(t *testing.T) {
	app, s, fc, fg := mount(t)
	// Link the GitHub identity BEFORE connect so the author is identity-verified.
	fg.setLinked("orgA", "acmedev", "tok_a")
	fg.setAdmin("tok_a", "acme", "widgets")
	idA, _ := connectOrg(t, app, s, "orgA", "acmedev")
	req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgA", false, map[string]any{"repoUrl": "acme/widgets"})
	req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgB", false, map[string]any{"repoUrl": "acme/widgets", "project": "proj-b"})
	approve(t, app, idA)
	fc.setSpend("orgB", 5000) // $50 × 20% share → royalty 1000c ($10.00)

	code, body := req(t, app, http.MethodGet, "/v1/authors", "orgA", false, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/authors want 200, got %d (%s)", code, body)
	}
	var v struct {
		IsAuthor     bool   `json:"isAuthor"`
		Status       string `json:"status"`
		GithubLogin  string `json:"githubLogin"`
		Verified     bool   `json:"verified"`
		AccruedCents int64  `json:"accruedCents"`
		PendingCents int64  `json:"pendingCents"`
		Repos        []struct {
			RepoURL       string `json:"repoUrl"`
			Verified      bool   `json:"verified"`
			BadgeMarkdown string `json:"badgeMarkdown"`
		} `json:"repos"`
		Deploys []struct {
			RepoURL      string `json:"repoUrl"`
			DeployingOrg string `json:"deployingOrg"`
		} `json:"deploys"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	const want = 5000 * defaultShareBps / bpsDenom // 1000
	if !v.IsAuthor || v.Status != StatusApproved || v.GithubLogin != "acmedev" || !v.Verified {
		t.Fatalf("dashboard head wrong: %+v", v)
	}
	if v.AccruedCents != want || v.PendingCents != want {
		t.Fatalf("lazy accrual not reflected: accrued=%d pending=%d, want %d", v.AccruedCents, v.PendingCents, want)
	}
	if len(v.Repos) != 1 || !v.Repos[0].Verified || v.Repos[0].BadgeMarkdown == "" {
		t.Fatalf("repos wrong: %+v", v.Repos)
	}
	if len(v.Deploys) != 1 || v.Deploys[0].DeployingOrg != "orgB" {
		t.Fatalf("deploys wrong: %+v", v.Deploys)
	}
	_ = idA
}

// TestPayoutCreditsOneGrantCashRecordOnlyAndPendingGuard: a credits payout issues
// exactly ONE commerce grant + moves paid; a cash payout is record-only; a payout can
// never exceed pending.
func TestPayoutCreditsOneGrantCashRecordOnlyAndPendingGuard(t *testing.T) {
	app, s, fc, fg := mount(t)
	ctx := context.Background()
	idA, _ := connectOrg(t, app, s, "orgA", "acmedev")
	fg.setLinked("orgA", "acmedev", "tok_a")
	fg.setAdmin("tok_a", "acme", "widgets")
	req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgA", false, map[string]any{"repoUrl": "acme/widgets"})
	req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgB", false, map[string]any{"repoUrl": "acme/widgets", "project": "proj-b"})
	approve(t, app, idA)
	fc.setSpend("orgB", 10000) // spend $100 × 20% share → accrue 2000c pending
	req(t, app, http.MethodPost, "/v1/admin/authors/sweep", "admin", true, nil)

	// Amounts are SHARE-DERIVED so this proof never drifts when the share changes:
	// accrued = spend × 20%; a credits payout takes 3/5, cash takes the rest.
	const accrued = 10000 * defaultShareBps / bpsDenom // 2000c @ 20%
	const firstPay = accrued * 3 / 5                   // 1200c credits
	const restPay = accrued - firstPay                 // 800c cash

	// Non-admin is refused on payout.
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/authors/"+idA+"/payout", "orgA", false, map[string]any{"amountCents": 100, "method": "credits"}); st != http.StatusForbidden {
		t.Fatalf("non-admin payout want 403, got %d", st)
	}
	// Over-pending → 400 (accrued available, ask accrued+500).
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/authors/"+idA+"/payout", "admin", true, map[string]any{"amountCents": accrued + 500, "method": "credits"}); st != http.StatusBadRequest {
		t.Fatalf("over-pending payout want 400, got %d", st)
	}

	// Credits payout of firstPay → ONE grant into orgA's wallet, paid moves.
	st, body := req(t, app, http.MethodPost, "/v1/admin/authors/"+idA+"/payout", "admin", true, map[string]any{"amountCents": firstPay, "method": "credits", "reference": "ledger-1"})
	if st != http.StatusOK {
		t.Fatalf("credits payout want 200, got %d (%s)", st, body)
	}
	if fc.bal("orgA") != firstPay || fc.depositCount() != 1 {
		t.Fatalf("credits payout wallet=%d deposits=%d, want %d/1", fc.bal("orgA"), fc.depositCount(), firstPay)
	}
	a, _ := s.State.store.GetByID(ctx, idA)
	if a.PaidCents != firstPay || a.PendingCents() != restPay {
		t.Fatalf("after credits payout: paid=%d pending=%d (want %d/%d)", a.PaidCents, a.PendingCents(), firstPay, restPay)
	}
	pd := envData(t, body)
	var payout struct {
		AmountCents int64  `json:"amountCents"`
		Method      string `json:"method"`
		Txn         string `json:"txn"`
	}
	_ = json.Unmarshal(pd["payout"], &payout)
	if payout.AmountCents != firstPay || payout.Method != "credits" || payout.Txn == "" {
		t.Fatalf("payout view wrong: %+v", payout)
	}

	// Cash payout of the rest via wire → RECORD-ONLY (no new grant).
	st, _ = req(t, app, http.MethodPost, "/v1/admin/authors/"+idA+"/payout", "admin", true, map[string]any{"amountCents": restPay, "method": "wire", "reference": "wire-xyz"})
	if st != http.StatusOK {
		t.Fatalf("cash payout want 200, got %d", st)
	}
	if fc.depositCount() != 1 || fc.bal("orgA") != firstPay {
		t.Fatalf("cash payout moved money: deposits=%d bal=%d", fc.depositCount(), fc.bal("orgA"))
	}
	a, _ = s.State.store.GetByID(ctx, idA)
	if a.PaidCents != accrued || a.PendingCents() != 0 {
		t.Fatalf("after cash payout: paid=%d pending=%d (want %d/0)", a.PaidCents, a.PendingCents(), accrued)
	}
	// Drained → any further payout is 400.
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/authors/"+idA+"/payout", "admin", true, map[string]any{"amountCents": 1, "method": "credits"}); st != http.StatusBadRequest {
		t.Fatalf("drained payout want 400, got %d", st)
	}
}

// TestAdminGateAndDirectory: every /v1/admin/authors* route is SuperAdmin
// fail-closed, and the directory exposes orgs + counts + a summary.
func TestAdminGateAndDirectory(t *testing.T) {
	app, s, fc, fg := mount(t)
	idA, _ := connectOrg(t, app, s, "orgA", "acmedev")
	fg.setLinked("orgA", "acmedev", "tok_a")
	fg.setAdmin("tok_a", "acme", "widgets")
	req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgA", false, map[string]any{"repoUrl": "acme/widgets"})
	req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgB", false, map[string]any{"repoUrl": "acme/widgets", "project": "proj-b"})
	approve(t, app, idA)
	fc.setSpend("orgB", 10000)
	req(t, app, http.MethodPost, "/v1/admin/authors/sweep", "admin", true, nil)

	// A non-admin tenant is refused 403 on EVERY admin route.
	for _, p := range []string{"/v1/admin/authors", "/v1/admin/authors/sweep",
		"/v1/admin/authors/" + idA + "/approve", "/v1/admin/authors/" + idA + "/suspend",
		"/v1/admin/authors/" + idA + "/payout"} {
		method := http.MethodPost
		if p == "/v1/admin/authors" {
			method = http.MethodGet
		}
		if code, _ := req(t, app, method, p, "orgA", false, map[string]any{"amountCents": 1, "method": "credits"}); code != http.StatusForbidden {
			t.Fatalf("non-admin %s want 403, got %d", p, code)
		}
	}

	// SuperAdmin sees the directory with the author + counts + a summary.
	code, body := req(t, app, http.MethodGet, "/v1/admin/authors", "admin", true, nil)
	if code != http.StatusOK {
		t.Fatalf("admin list want 200, got %d (%s)", code, body)
	}
	data := envData(t, body)
	var authors []adminAuthorView
	if err := json.Unmarshal(data["authors"], &authors); err != nil {
		t.Fatalf("decode authors: %v", err)
	}
	if len(authors) != 1 {
		t.Fatalf("admin authors len = %d, want 1", len(authors))
	}
	a0 := authors[0]
	const want = 10000 * defaultShareBps / bpsDenom
	if a0.Org != "orgA" || a0.GithubLogin != "acmedev" || a0.Status != StatusApproved || a0.RepoCount != 1 || a0.DeployCount != 1 || a0.AccruedCents != want {
		t.Fatalf("admin row wrong: %+v", a0)
	}
	var sum adminSummary
	if err := json.Unmarshal(data["summary"], &sum); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if sum.Total != 1 || sum.Approved != 1 || sum.AccruedCents != want {
		t.Fatalf("summary wrong: %+v", sum)
	}

	// Suspend flips status; a suspended author accrues nothing more on the next sweep.
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/authors/"+idA+"/suspend", "admin", true, nil); st != http.StatusOK {
		t.Fatalf("suspend want 200, got %d", st)
	}
	fc.setSpend("orgB", 20000) // more spend, but suspended → no accrual
	req(t, app, http.MethodPost, "/v1/admin/authors/sweep", "admin", true, nil)
	a, _ := s.State.store.GetByID(context.Background(), idA)
	if a.AccruedCents != want {
		t.Fatalf("suspended author accrued more: %d, want %d", a.AccruedCents, want)
	}
}

// TestGitLabVerifyAndLedger proves the GitLab forge works end to end (canonicalize →
// file-verify a gitlab.com repo → deploy → accrue @20% share) AND that each accrual
// appends an immutable ledger row whose compute_proof is NULL (the hanzod attestation
// is a follow-up, never fabricated).
func TestGitLabVerifyAndLedger(t *testing.T) {
	app, s, fc, fg := mount(t)
	ctx := context.Background()

	// orgG connects via the gitlab provider and file-verifies a gitlab.com repo.
	code, cbody := req(t, app, http.MethodPost, "/v1/authors/connect", "orgG", false, map[string]any{"provider": "gitlab", "login": "gldev"})
	if code != http.StatusCreated {
		t.Fatalf("gitlab connect want 201, got %d (%s)", code, cbody)
	}
	var cr struct {
		ID, VerifyCode string
	}
	_ = json.Unmarshal(cbody, &cr)
	fg.setFile("glorg", "gltool", "main", verifyFile, []byte(`{"hanzoAuthorCode":"`+cr.VerifyCode+`"}`))
	st, vbody := req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgG", false, map[string]any{"repoUrl": "https://gitlab.com/glorg/gltool"})
	if st != http.StatusCreated {
		t.Fatalf("gitlab file-verify want 201, got %d (%s)", st, vbody)
	}
	var vr struct {
		Repo struct {
			RepoURL  string `json:"repoUrl"`
			Verified bool   `json:"verified"`
			Method   string `json:"method"`
		} `json:"repo"`
	}
	_ = json.Unmarshal(vbody, &vr)
	if vr.Repo.RepoURL != "gitlab.com/glorg/gltool" || !vr.Repo.Verified || vr.Repo.Method != MethodFile {
		t.Fatalf("gitlab verify wrong: %+v", vr.Repo)
	}

	// orgH deploys it, orgG is approved, orgH spends $100 → royalty @20% = 2000c.
	req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgH", false, map[string]any{"repoUrl": "gitlab.com/glorg/gltool", "project": "proj-h"})
	approve(t, app, cr.ID)
	fc.setSpend("orgH", 10000)
	req(t, app, http.MethodPost, "/v1/admin/authors/sweep", "admin", true, nil)

	a, _ := s.State.store.GetByID(ctx, cr.ID)
	const want = 10000 * defaultShareBps / bpsDenom // 2000 @ 20%
	if a.AccruedCents != want {
		t.Fatalf("gitlab author accrued = %d, want %d", a.AccruedCents, want)
	}

	// The append-only ledger has exactly ONE row for this accrual: share 2500 bps,
	// earning 2500c, compute_proof NULL (attestation is a follow-up, not fabricated).
	rows, err := s.State.store.ListLedger(ctx, cr.ID, "", 100)
	if err != nil {
		t.Fatalf("ListLedger: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ledger rows = %d, want 1", len(rows))
	}
	lr := rows[0]
	if lr.DeployingOrg != "orgH" || lr.ShareBps != defaultShareBps || lr.EarningCents != want || lr.ComputeProof != nil {
		t.Fatalf("ledger row wrong: %+v (computeProof must be nil)", lr)
	}

	// Idempotent: a re-sweep in the same period appends NO new ledger row.
	req(t, app, http.MethodPost, "/v1/admin/authors/sweep", "admin", true, nil)
	rows2, _ := s.State.store.ListLedger(ctx, cr.ID, "", 100)
	if len(rows2) != 1 {
		t.Fatalf("re-sweep appended a ledger row: %d, want 1 (append-only, at-most-once)", len(rows2))
	}
}

// TestAutoPayoutDrainsPendingIdempotent is the AUTOMATION proof: the scheduler's
// sweepAndPayout accrues AND pays an approved author's pending royalty in one pass
// (no human sweep/payout call), and a second pass in the same period pays NOTHING more
// — the pending guard makes auto-payout at-most-once, never a double-pay.
func TestAutoPayoutDrainsPendingIdempotent(t *testing.T) {
	app, s, fc, fg := mount(t)
	ctx := context.Background()
	idA, _ := connectOrg(t, app, s, "orgA", "acmedev")
	fg.setLinked("orgA", "acmedev", "tok_a")
	fg.setAdmin("tok_a", "acme", "widgets")
	req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgA", false, map[string]any{"repoUrl": "acme/widgets"})
	req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgB", false, map[string]any{"repoUrl": "acme/widgets", "project": "proj-b"})
	approve(t, app, idA)
	fc.setSpend("orgB", 10000) // $100 × 20% → 2000c

	// The automatic loop accrues AND pays in one pass — the closed money loop.
	sweepAndPayout(s)
	const want = 10000 * defaultShareBps / bpsDenom // 2000
	a, _ := s.State.store.GetByID(ctx, idA)
	if a.AccruedCents != want || a.PaidCents != want || a.PendingCents() != 0 {
		t.Fatalf("auto loop: accrued=%d paid=%d pending=%d, want %d/%d/0", a.AccruedCents, a.PaidCents, a.PendingCents(), want, want)
	}
	// External author → the payout is a credits grant into their wallet, exactly once.
	if fc.bal("orgA") != want || fc.depositCount() != 1 {
		t.Fatalf("auto payout wallet=%d deposits=%d, want %d/1", fc.bal("orgA"), fc.depositCount(), want)
	}
	// IDEMPOTENT: a second automatic pass (same period) accrues nothing more and pays
	// nothing more — pending is 0, so RecordPayout's guard refuses. No double-pay.
	sweepAndPayout(s)
	a, _ = s.State.store.GetByID(ctx, idA)
	if a.PaidCents != want || a.AccruedCents != want || fc.depositCount() != 1 {
		t.Fatalf("double auto-pay! accrued=%d paid=%d deposits=%d, want %d/%d/1", a.AccruedCents, a.PaidCents, fc.depositCount(), want, want)
	}
}

// TestHanzoForkRoutesToTreasury is the ATTRIBUTION proof: an EXTERNAL org deploying a
// Hanzo-maintained template (owner ∈ hanzoai) auto-attributes to the treasury SYSTEM
// author (org=hanzo) with NO human verify step, accrues 20%, and the automatic payout
// routes that royalty to the Hanzo treasury ("pay ourselves") — it NEVER lands in an
// external commerce wallet. A self-deploy by the Hanzo org itself earns nothing.
func TestHanzoForkRoutesToTreasury(t *testing.T) {
	app, s, fc, _ := mount(t) // maintainerOrg = "hanzo"
	ctx := context.Background()

	// An external org deploys hanzoai/chat-starter — recordDeploy auto-attributes it.
	st, body := req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgB", false,
		map[string]any{"repoUrl": "https://github.com/hanzoai/chat-starter", "project": "proj-b"})
	if st != http.StatusCreated || !recorded(t, body) {
		t.Fatalf("hanzo-fork deploy want 201 recorded, got %d (%s)", st, body)
	}
	// The treasury system author now exists (org=hanzo), approved, 20%, owning the repo.
	sys, err := s.State.store.GetByOrg(ctx, "hanzo")
	if err != nil {
		t.Fatalf("treasury system author missing: %v", err)
	}
	if sys.Status != StatusApproved || sys.ShareBps != defaultShareBps {
		t.Fatalf("system author wrong: %+v", sys)
	}
	repos, _ := s.State.store.ListRepos(ctx, sys.ID, 10)
	if len(repos) != 1 || repos[0].RepoURL != "github.com/hanzoai/chat-starter" || repos[0].Method != MethodMaintainer {
		t.Fatalf("maintained repo attribution wrong: %+v", repos)
	}

	// orgB spends $100 → Hanzo earns 20% = 2000c, auto-paid INTO the treasury.
	fc.setSpend("orgB", 10000)
	sweepAndPayout(s)
	const want = 10000 * defaultShareBps / bpsDenom // 2000
	sys, _ = s.State.store.GetByID(ctx, sys.ID)
	if sys.AccruedCents != want || sys.PaidCents != want || sys.PendingCents() != 0 {
		t.Fatalf("treasury author accrued=%d paid=%d pending=%d, want %d/%d/0", sys.AccruedCents, sys.PaidCents, sys.PendingCents(), want, want)
	}
	// The royalty went to the treasury reserve, NOT a customer wallet: zero deposits.
	if fc.depositCount() != 0 {
		t.Fatalf("hanzo-fork royalty hit an external commerce wallet (%d deposits) — must credit the treasury", fc.depositCount())
	}

	// A self-deploy by the Hanzo org itself never earns (self excluded from accrual).
	req(t, app, http.MethodPost, "/v1/authors/deploys/record", "hanzo", false,
		map[string]any{"repoUrl": "hanzoai/chat-starter", "project": "proj-self"})
	fc.setSpend("hanzo", 50000)
	sweepAndPayout(s)
	sys, _ = s.State.store.GetByID(ctx, sys.ID)
	if sys.AccruedCents != want {
		t.Fatalf("self-deploy accrued to treasury: %d, want %d (self excluded)", sys.AccruedCents, want)
	}
}

// TestIsMaintainedRepo proves the Hanzo-fork detector: brand-owned repos (hanzoai/*,
// hanzo/*, hanzo-*) route to the treasury; everything else does not; white-label by
// brand (a lux deployment claims luxfi/*, never hanzoai/*).
func TestIsMaintainedRepo(t *testing.T) {
	for _, r := range []string{"github.com/hanzoai/chat", "github.com/hanzo/site", "github.com/hanzo-labs/x", "gitlab.com/hanzoai/y"} {
		if !isMaintainedRepo(r, "hanzo") {
			t.Fatalf("isMaintainedRepo(%q, hanzo) = false, want true", r)
		}
	}
	for _, r := range []string{"github.com/acme/widgets", "github.com/hanzoworld/x", "github.com/luxfi/node", ""} {
		if isMaintainedRepo(r, "hanzo") {
			t.Fatalf("isMaintainedRepo(%q, hanzo) = true, want false", r)
		}
	}
	// White-label: a lux deployment claims luxfi/*, not hanzoai/*.
	if !isMaintainedRepo("github.com/luxfi/node", "lux") || isMaintainedRepo("github.com/hanzoai/chat", "lux") {
		t.Fatal("white-label maintainer detection wrong for brand=lux")
	}
}

// recorded pulls the boolean "recorded" flag out of a deploy response.
func recorded(t *testing.T, body []byte) bool {
	t.Helper()
	var out struct {
		Recorded bool `json:"recorded"`
	}
	_ = json.Unmarshal(body, &out)
	return out.Recorded
}

// sweptAccrued pulls the "accrued" count out of an ENVELOPED sweep response.
func sweptAccrued(t *testing.T, body []byte) int {
	t.Helper()
	var out struct {
		Data struct {
			Accrued int `json:"accrued"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &out)
	return out.Data.Accrued
}

// TestMount exercises the real Mount wiring (store open + route registration) against
// a temp DataDir, proving the package boots as the binary loads it.
func TestMount(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), Brand: "hanzo"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	r := httptest.NewRequest(http.MethodGet, "/v1/authors", nil)
	resp, err := app.Fiber().Test(r, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mounted GET /v1/authors (no principal) want 403, got %d", resp.StatusCode)
	}
}

// TestNormalizeTarget: the ONE parser distinguishes a REPO (host/owner/name) from a
// whole OWNER/org (host/owner) from junk. An org REQUIRES an explicit forge host — a
// bare "owner/name" is still a github.com repo, a bare single word is neither.
func TestNormalizeTarget(t *testing.T) {
	// Repos → isOrg=false.
	for in, want := range map[string]string{
		"https://github.com/acme/widgets":     "github.com/acme/widgets",
		"acme/widgets":                        "github.com/acme/widgets",
		"github.com/acme/widgets/tree/main":   "github.com/acme/widgets", // extra segments dropped
		"https://gitlab.com/acme/Widgets.git": "gitlab.com/acme/widgets",
		"git@github.com:acme/widgets.git":     "github.com/acme/widgets",
	} {
		got, isOrg := normalizeTarget(in)
		if got != want || isOrg {
			t.Fatalf("normalizeTarget(%q) = (%q,%v), want (%q,false)", in, got, isOrg, want)
		}
	}
	// Orgs (explicit host) → isOrg=true.
	for in, want := range map[string]string{
		"https://github.com/luxfi":     "github.com/luxfi",
		"github.com/luxfi":             "github.com/luxfi",
		"http://www.github.com/LuxFi/": "github.com/luxfi",
		"gitlab.com/acme-group":        "gitlab.com/acme-group",
	} {
		got, isOrg := normalizeTarget(in)
		if got != want || !isOrg {
			t.Fatalf("normalizeTarget(%q) = (%q,%v), want (%q,true)", in, got, isOrg, want)
		}
	}
	// Neither: empty, a bare word (no host), a non-forge host (owner/name form).
	for _, in := range []string{"", "  ", "luxfi", "not a repo", "https://bitbucket.org/a/b"} {
		if got, isOrg := normalizeTarget(in); got != "" || isOrg {
			t.Fatalf("normalizeTarget(%q) = (%q,%v), want empty", in, got, isOrg)
		}
	}
}

// TestVerifyOrgUrlCoversOwner is the ORG-claim proof: a whole github.com/<owner> url
// (no repo segment) is ACCEPTED, its ownership proven the SAME OAuth way against the
// owner's .github control repo, recorded as an owner-wide claim — and then EVERY repo
// under that owner earns on deploy without a per-repo verify. Ownership is NOT weakened:
// an owner the caller can't prove is 422, an owner already claimed is 409, and a per-repo
// verify still works alongside. A bare word / non-forge host is still rejected.
func TestVerifyOrgUrlCoversOwner(t *testing.T) {
	app, s, fc, fg := mount(t)
	ctx := context.Background()

	// orgL connects; its linked token has admin on the owner's control repo luxfi/.github.
	idL, _ := connectOrg(t, app, s, "orgL", "luxdev")
	fg.setLinked("orgL", "luxdev", "tok_l")
	fg.setAdmin("tok_l", "luxfi", orgProofRepo)

	// An ORG url verifies via OAuth and records an owner-wide claim (201 created).
	st, body := req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgL", false, map[string]any{"repoUrl": "https://github.com/luxfi"})
	if st != http.StatusCreated {
		t.Fatalf("org verify want 201, got %d (%s)", st, body)
	}
	var ov struct {
		Org struct {
			OwnerURL      string `json:"ownerUrl"`
			Verified      bool   `json:"verified"`
			Method        string `json:"method"`
			BadgeMarkdown string `json:"badgeMarkdown"`
		} `json:"org"`
		Created bool `json:"created"`
	}
	_ = json.Unmarshal(body, &ov)
	if ov.Org.OwnerURL != "github.com/luxfi" || !ov.Org.Verified || ov.Org.Method != MethodOAuth || !ov.Created {
		t.Fatalf("org verify wrong: %+v", ov)
	}
	// The claim is stored as an owner-wide org record on the author.
	claims, _ := s.State.store.ListOrgs(ctx, idL, 10)
	if len(claims) != 1 || claims[0].OwnerURL != "github.com/luxfi" || !claims[0].Verified {
		t.Fatalf("org claim not recorded: %+v", claims)
	}

	// A deploy of ANY repo under the verified owner (no per-repo claim) attributes to the
	// org's author — proven for TWO distinct repos under the same owner.
	if st, dbody := req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgD", false, map[string]any{"repoUrl": "https://github.com/luxfi/node", "project": "proj-d"}); st != http.StatusCreated || !recorded(t, dbody) {
		t.Fatalf("org-covered deploy #1 want 201 recorded, got %d (%s)", st, dbody)
	}
	if st, dbody := req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgD", false, map[string]any{"repoUrl": "luxfi/cli", "project": "proj-cli"}); st != http.StatusCreated || !recorded(t, dbody) {
		t.Fatalf("org-covered deploy #2 want 201 recorded, got %d (%s)", st, dbody)
	}

	// Approve + spend → the org author earns spend × share on the covered deploys.
	approve(t, app, idL)
	fc.setSpend("orgD", 10000) // $100 × 20% share → 2000c
	code, sbody := req(t, app, http.MethodPost, "/v1/admin/authors/sweep", "admin", true, nil)
	if code != http.StatusOK || sweptAccrued(t, sbody) != 1 {
		t.Fatalf("org-covered accrual sweep want 1, got %d (%s)", code, sbody)
	}
	a, _ := s.State.store.GetByID(ctx, idL)
	const want = 10000 * defaultShareBps / bpsDenom
	if a.AccruedCents != want {
		t.Fatalf("org-covered author accrued = %d, want %d", a.AccruedCents, want)
	}

	// Ownership is REQUIRED: an owner the caller can't prove is 422 (not weakened).
	connectOrg(t, app, s, "orgX", "xdev")
	if st, _ := req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgX", false, map[string]any{"repoUrl": "github.com/luxfi"}); st != http.StatusUnprocessableEntity {
		t.Fatalf("unproven org claim want 422, got %d", st)
	}
	// Even WITH proof, an owner already verified by another author is 409 (first-verify wins).
	fg.setLinked("orgX", "xdev", "tok_x")
	fg.setAdmin("tok_x", "luxfi", orgProofRepo)
	if st, _ := req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgX", false, map[string]any{"repoUrl": "github.com/luxfi"}); st != http.StatusConflict {
		t.Fatalf("cross-author org claim want 409, got %d", st)
	}

	// A per-repo claim still works alongside org claims (both arms coexist).
	fg.setAdmin("tok_x", "acme", "widgets")
	if st, _ := req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgX", false, map[string]any{"repoUrl": "acme/widgets"}); st != http.StatusCreated {
		t.Fatalf("per-repo verify alongside org want 201, got %d", st)
	}

	// A bare word (no host) and a non-forge host are still rejected as malformed (400).
	for _, bad := range []string{"luxfi", "not a repo", "https://bitbucket.org/a/b"} {
		if st, _ := req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgX", false, map[string]any{"repoUrl": bad}); st != http.StatusBadRequest {
			t.Fatalf("malformed %q want 400, got %d", bad, st)
		}
	}
}

// TestVerifyOrgFileMethod proves the SECOND ownership proof works for orgs too: a
// hanzo.json carrying the verify code on the owner's .github default branch verifies the
// whole owner (no OAuth token), and a repo under it then earns on deploy.
func TestVerifyOrgFileMethod(t *testing.T) {
	app, s, _, fg := mount(t)
	ctx := context.Background()

	idF, codeF := connectOrg(t, app, s, "orgF", "fdev")
	// hanzo.json with the author's verify code on acme/.github's default branch.
	fg.setFile("acme", orgProofRepo, "main", verifyFile, []byte(`{"hanzoAuthorCode":"`+codeF+`"}`))

	st, body := req(t, app, http.MethodPost, "/v1/authors/repos/verify", "orgF", false, map[string]any{"repoUrl": "github.com/acme"})
	if st != http.StatusCreated {
		t.Fatalf("org file-verify want 201, got %d (%s)", st, body)
	}
	var ov struct {
		Org struct {
			OwnerURL string `json:"ownerUrl"`
			Verified bool   `json:"verified"`
			Method   string `json:"method"`
		} `json:"org"`
	}
	_ = json.Unmarshal(body, &ov)
	if ov.Org.OwnerURL != "github.com/acme" || !ov.Org.Verified || ov.Org.Method != MethodFile {
		t.Fatalf("org file-verify wrong: %+v", ov.Org)
	}

	// A repo under the file-verified owner earns on deploy (org arm of attribution).
	st, dbody := req(t, app, http.MethodPost, "/v1/authors/deploys/record", "orgZ", false, map[string]any{"repoUrl": "github.com/acme/anything", "project": "proj-z"})
	if st != http.StatusCreated || !recorded(t, dbody) {
		t.Fatalf("org-covered deploy want 201 recorded, got %d (%s)", st, dbody)
	}
	deploys, _ := s.State.store.ListDeploys(ctx, idF, 10)
	if len(deploys) != 1 || deploys[0].RepoURL != "github.com/acme/anything" {
		t.Fatalf("org-covered deploy not attributed to owner author: %+v", deploys)
	}
}
