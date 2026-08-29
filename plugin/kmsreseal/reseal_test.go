package main

// reseal_test.go — the round-trip proof. The tool reads synthetic secrets from a
// fake standalone (in-memory, unsealed — mirroring the live standalone's no-seal
// store) and writes them into cloud's REAL embedded /v1/kms, which SEALS them with
// a test master key. The write path is exercised in-process via zip's
// app.Fiber().Test (no listener), so the tool's real URL/body mapping hits the real
// mount.go routes, real org-scope guard, and real AES-256-GCM Seal.
//
// It also proves the security properties: cloud stores no plaintext on disk, a
// wrong-org token is refused by cloud (403 → failed), and env is always explicit.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	luxlog "github.com/luxfi/log"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/kms"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// ── cloud harness (the REAL embedded /v1/kms, in-process) ───────────────────────

// masterKeyB64 is a FIXED 32-byte key for the whole test binary. Production cloud
// runs one stable master key for the process lifetime; a per-test random key
// instead diverges the process-global at-rest key across sequential tests, so the
// harness pins one deterministic key (files are still fresh per t.TempDir()).
func masterKeyB64(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(k)
}

func newCloudApp(t *testing.T) (*zip.App, string, cloud.Deps) {
	t.Helper()
	dir := t.TempDir()
	// One master key roots BOTH at-rest layers: the AES-256-GCM Seal envelope (from
	// cfg) and the per-org SQLite cek file encryption (read from this env on an
	// encryption-capable build). Setting both keeps the harness build-tag agnostic.
	key := masterKeyB64(t)
	t.Setenv("CLOUD_KMS_MASTER_KEY_REF", key)
	cfg := &cloud.Config{
		Brand: "hanzo", Domain: "api.hanzo.ai", IAMIssuer: "https://hanzo.id",
		DataDir: dir, Enable: []string{"kms"}, KMSMasterKeyRef: key,
	}
	deps := cloud.BuildDeps(cfg)
	app := zip.New(zip.Config{Logger: luxlog.Default()})
	app.Use(middleware.Recover())
	app.Use(middleware.RequestID())
	if err := cloud.UseAll(app, []cloud.Plugin{{Name: "kms", Use: kms.Use, OwnsHealth: true}}, cfg, deps); err != nil {
		t.Fatalf("UseAll: %v", err)
	}
	return app, dir, deps
}

// cloudDoer simulates the SanitizeIdentity boundary the gateway/serve.go establish
// in production: it maps the tool's org-bound bearer to the identity headers the
// in-process guard reads. Token grammar (tests only): "org:<org>".
//
// The bearer carries ADMIN AUTHORITY OVER THAT ORG, because a reseal WRITES and
// cloud's secret plane admits a member to read but requires an admin to write
// (apps/kms/mount.go). It is still org-bound — X-User-IsOrgAdmin says "admin of this
// org", never the cross-tenant sudo X-User-IsAdmin marks and the tool refuses to
// hold. A bearer without it is a member, and a member's write is refused 403; that
// refusal is what these tests hit before the authority was stated here.
type cloudDoer struct{ app *zip.App }

func (c cloudDoer) Do(r *http.Request) (*http.Response, error) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	r.Header.Del("Authorization")
	if org, ok := strings.CutPrefix(tok, "org:"); ok {
		r.Header.Set("X-Org-Id", org)
		r.Header.Set("X-User-Id", "u-"+org)
		r.Header.Set("X-User-IsOrgAdmin", "true")
	}
	return c.app.Test(r)
}

// ── fake standalone (in-memory, UNSEALED — like the live source) ─────────────────

type fakeStandalone struct {
	store map[Coord]string // (org,path,env,key) → plaintext
}

func newFakeStandalone() *fakeStandalone { return &fakeStandalone{store: map[Coord]string{}} }

func (f *fakeStandalone) seed(org, path, env, key, val string) {
	f.store[Coord{org, path, env, key}] = val
}

// Do serves the standalone's REAL routes — GET /v1/kms/orgs/{org}/secrets/{rest}
// (one value) and GET /v1/kms/orgs/{org}/secrets?path=&env= (names) — plus the
// login broker, enforcing owner==:org from the tool's org-bound token exactly like
// luxfi/kms cmd/kms requireOrgJWT. The org IS in this face's path; that is what
// makes it a different grammar from cloud's, and the reason a client carries the
// route of the face it talks to.
func (f *fakeStandalone) Do(r *http.Request) (*http.Response, error) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	tokenOrg := strings.TrimPrefix(tok, "org:")
	path := r.URL.Path

	// login broker (not used when tokens are injected, but present for parity)
	if strings.HasSuffix(path, "/v1/kms/auth/login") {
		return resp(200, `{"accessToken":"org:hanzo","expiresIn":3600,"tokenType":"Bearer"}`), nil
	}

	// /v1/kms/orgs/{org}/secrets(/{rest})
	const marker = "/v1/kms/orgs/"
	_, rest, under := strings.Cut(path, marker)
	if !under {
		return resp(404, `{"message":"no route"}`), nil
	}
	org, tail, _ := strings.Cut(rest, "/secrets")
	if tok == "" {
		return resp(403, `{"message":"no principal"}`), nil
	}
	if tokenOrg != org {
		return resp(403, `{"message":"org claim does not match URL"}`), nil
	}
	env := r.URL.Query().Get("env")
	if env == "" {
		env = "default"
	}
	// LIST: GET .../secrets (no trailing key) → the standalone's {"names":[…]}.
	if tail == "" || tail == "/" {
		return resp(200, `{"names":[`+f.listNames(org, r.URL.Query().Get("path"), env)+`]}`), nil
	}
	// GET one: tail = "/{path}/{key}" or "/{key}"
	sub := strings.Trim(tail, "/")
	p, k := splitLast(sub)
	v, ok := f.store[Coord{org, p, env, k}]
	if !ok {
		return resp(404, `{"message":"not found"}`), nil
	}
	return resp(200, `{"secret":{"value":`+jsonString(v)+`},"version":1}`), nil
}

func (f *fakeStandalone) listNames(org, qpath, env string) string {
	want := strings.Trim(qpath, "/")
	var names []string
	for c := range f.store {
		if c.Org == org && c.Env == env && c.Path == want {
			names = append(names, jsonString(c.Key))
		}
	}
	return strings.Join(names, ",")
}

func splitLast(sub string) (path, key string) {
	if i := strings.LastIndex(sub, "/"); i >= 0 {
		return sub[:i], sub[i+1:]
	}
	return "", sub
}

func resp(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: nopCloser(body), Header: http.Header{}}
}

// ── tests ────────────────────────────────────────────────────────────────────────

// injectToken returns a tokenFunc that mints the org-bound test token for a target.
func injectToken(_ context.Context, t Target) (string, error) { return "org:" + t.Org, nil }

func TestReseal_RoundTripRealSeal(t *testing.T) {
	app, dataDir, deps := newCloudApp(t)
	src := newKMSClient("http://kms.hanzo.svc", standalone, newFakeStandalone())
	fs := src.do.(*fakeStandalone)
	dst := newKMSClient("http://cloud.hanzo.svc", embedded, cloudDoer{app})

	// Synthetic secrets across two paths + envs (never real values).
	fs.seed("hanzo", "admin-guard-secrets", "prod", "GUARD_HMAC_KEY", "hmac-plain-1")
	fs.seed("hanzo", "admin-guard-secrets", "prod", "IAM_CLIENT_SECRET", "iam-plain-2")
	fs.seed("hanzo", "datastore", "prod", "DATASTORE_PASSWORD", "PLAINTEXT-MARKER-42")

	inv := Inventory{Targets: []Target{
		{Org: "hanzo", Path: "admin-guard-secrets", Env: "prod", Key: "GUARD_HMAC_KEY"},
		{Org: "hanzo", Path: "admin-guard-secrets", Env: "prod", Key: "IAM_CLIENT_SECRET"},
		{Org: "hanzo", Path: "datastore", Env: "prod", Key: "DATASTORE_PASSWORD"},
	}}

	rep := reseal(context.Background(), inv, src, dst, injectToken, injectToken, false)
	if rep.Migrated != 3 || rep.Failed != 0 {
		t.Fatalf("reseal: migrated=%d failed=%d, want 3/0: %+v", rep.Migrated, rep.Failed, rep.Results)
	}

	// VERIFY: every target byte-identical on cloud (real open) vs source.
	vrep := verify(context.Background(), inv, src, dst, injectToken, injectToken)
	if vrep.Match != 3 || vrep.Mismatch != 0 || vrep.AbsentD != 0 {
		t.Fatalf("verify: match=%d mismatch=%d absent-dst=%d, want 3/0/0: %+v", vrep.Match, vrep.Mismatch, vrep.AbsentD, vrep.Results)
	}

	// Close the store so per-org SQLite flushes to disk, then scan.
	if c, ok := deps.KMS.(*kms.Client); ok {
		if err := c.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}
	// SEAL PROOF: the plaintext marker must NOT appear anywhere on cloud's disk.
	assertNotOnDisk(t, dataDir, "PLAINTEXT-MARKER-42")
	assertNotOnDisk(t, dataDir, "iam-plain-2")
}

func TestReseal_WrongOrgTokenRefusedByCloud(t *testing.T) {
	app, _, _ := newCloudApp(t)
	src := newKMSClient("http://kms.hanzo.svc", standalone, newFakeStandalone())
	src.do.(*fakeStandalone).seed("hanzo", "p", "prod", "K", "v")
	dst := newKMSClient("http://cloud.hanzo.svc", embedded, cloudDoer{app})

	inv := Inventory{Targets: []Target{{Org: "hanzo", Path: "p", Env: "prod", Key: "K"}}}
	// A token scoped to the WRONG org: the fake source refuses the read (403), so the
	// migration fails closed — the value is never even read, let alone written cross-org.
	badToken := func(_ context.Context, _ Target) (string, error) { return "org:evil", nil }
	rep := reseal(context.Background(), inv, src, dst, badToken, badToken, false)
	if rep.Failed != 1 || rep.Migrated != 0 {
		t.Fatalf("wrong-org reseal: migrated=%d failed=%d, want 0/1", rep.Migrated, rep.Failed)
	}
}

func TestReseal_FolderSyncResolvedViaList(t *testing.T) {
	app, _, _ := newCloudApp(t)
	src := newKMSClient("http://kms.hanzo.svc", standalone, newFakeStandalone())
	fs := src.do.(*fakeStandalone)
	dst := newKMSClient("http://cloud.hanzo.svc", embedded, cloudDoer{app})

	// A folder-sync CR (empty keys[]) at a non-root path: LIST discovers the keys.
	fs.seed("hanzo", "commerce", "prod", "HUSD_TREASURY_KEY", "treasury-1")
	fs.seed("hanzo", "commerce", "prod", "STRIPE_KEY", "stripe-2")
	inv := Inventory{Folders: []Target{{Org: "hanzo", Path: "commerce", Env: "prod", Folder: true}}}

	rep := reseal(context.Background(), inv, src, dst, injectToken, injectToken, false)
	if rep.Migrated != 2 || rep.Failed != 0 {
		t.Fatalf("folder reseal: migrated=%d failed=%d, want 2/0: %+v", rep.Migrated, rep.Failed, rep.Results)
	}
	// Explicit-key verify of the discovered keys must match.
	explicit := Inventory{Targets: []Target{
		{Org: "hanzo", Path: "commerce", Env: "prod", Key: "HUSD_TREASURY_KEY"},
		{Org: "hanzo", Path: "commerce", Env: "prod", Key: "STRIPE_KEY"},
	}}
	if v := verify(context.Background(), explicit, src, dst, injectToken, injectToken); v.Match != 2 {
		t.Fatalf("folder verify match=%d, want 2", v.Match)
	}
}

func TestReseal_PlanDoesNoNetwork(t *testing.T) {
	// A nil-doer client would panic on any Do; --plan must never touch it.
	src := newKMSClient("http://kms.hanzo.svc", standalone, panicDoer{})
	dst := newKMSClient("http://cloud.hanzo.svc", embedded, panicDoer{})
	inv := Inventory{Targets: []Target{{Org: "hanzo", Path: "p", Env: "prod", Key: "K"}}, Folders: []Target{{Org: "hanzo", Path: "f", Env: "prod", Folder: true}}}
	rep := reseal(context.Background(), inv, src, dst, injectToken, injectToken, true)
	if rep.Planned != 2 || rep.Migrated != 0 || rep.Failed != 0 {
		t.Fatalf("plan: planned=%d migrated=%d failed=%d, want 2/0/0", rep.Planned, rep.Migrated, rep.Failed)
	}
}

type panicDoer struct{}

func (panicDoer) Do(*http.Request) (*http.Response, error) { panic("network touched during --plan") }

func assertNotOnDisk(t *testing.T, dir, marker string) {
	t.Helper()
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		if strings.Contains(string(b), marker) {
			t.Fatalf("plaintext %q found on disk at %s — NOT sealed", marker, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// ── test helpers ─────────────────────────────────────────────────────────────────

func nopCloser(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
