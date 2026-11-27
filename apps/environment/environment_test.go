package environment

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/secret"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	// The per-org files are sealed under a key derived from the process master;
	// a test process has none, so it takes the dev one.
	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

// vault is an in-memory KMS, so a test can see a value went to KMS and nowhere
// else.
type vault struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (k *vault) GetSecret(_ context.Context, ref string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[ref]
	if !ok {
		return nil, secret.ErrNone
	}
	return v, nil
}

func (k *vault) PutSecret(_ context.Context, ref string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[ref] = append([]byte(nil), value...)
	return nil
}

func (k *vault) DeleteSecret(_ context.Context, ref string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, ref)
	return nil
}

func (k *vault) Sign(context.Context, string, []byte) ([]byte, error) { return nil, nil }

// mount stands the app up as the composer would: the principal bridge at the
// root, then Use.
func mount(t *testing.T) (*zip.App, *vault, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLOUD_DATA_DIR", dir)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	kms := &vault{m: map[string][]byte{}}
	if err := Use(app, cloud.Deps{KMS: kms}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app, kms, dir
}

// as drives one request as a member of org, or as its admin.
func as(t *testing.T, app *zip.App, method, path, org string, admin bool, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	if admin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

// stores is every store that belongs to an environment: the files under dir, or on
// hanzo-sql the namespaces the registry names.
func stores(t *testing.T, dir string) []string {
	t.Helper()
	if locus.DSN() != "" {
		named, err := locus.Stores("environment")
		if err != nil {
			t.Fatalf("the registry could not be read: %v", err)
		}
		return named
	}
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), "environment.db") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// A codebase with nothing answers `none`, so a surface can always draw its editor,
// and is not listed.
func TestNothingIsNone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, _ := mount(t)
		code, e := as(t, app, http.MethodGet, "/v1/environment/site", "acme", false, nil)
		if code != http.StatusOK || e["state"] != stateNone || e["install"] != "" {
			t.Fatalf("an empty codebase answered %d %v", code, e)
		}
		code, l := as(t, app, http.MethodGet, "/v1/environment", "acme", false, nil)
		if code != http.StatusOK || len(l["data"].([]any)) != 0 {
			t.Fatalf("the list answered %d %v", code, l)
		}
	})
}

// A member reads and may not write; an org admin writes. A script runs in every
// later run on the codebase, and a secret reaches every one of their agents.
func TestAMemberReadsAndAnAdminWrites(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, _ := mount(t)
		save := map[string]string{"install": "pnpm install --frozen-lockfile", "start": "pnpm dev"}
		if code, _ := as(t, app, http.MethodPut, "/v1/environment/site", "acme", false, save); code != http.StatusForbidden {
			t.Fatalf("a member saved scripts: %d", code)
		}
		if code, _ := as(t, app, http.MethodPut, "/v1/environment/site/secrets/DATABASE_URL", "acme", false, map[string]string{"value": "x"}); code != http.StatusForbidden {
			t.Fatalf("a member set a secret: %d", code)
		}
		if code, _ := as(t, app, http.MethodGet, "/v1/environment/site", "", false, nil); code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Fatalf("nobody read an environment: %d", code)
		}
		code, e := as(t, app, http.MethodPut, "/v1/environment/site", "acme", true, save)
		if code != http.StatusOK || e["install"] != save["install"] || e["start"] != save["start"] || e["state"] != stateReady {
			t.Fatalf("an admin's save answered %d %v", code, e)
		}
		if code, e := as(t, app, http.MethodGet, "/v1/environment/site", "acme", false, nil); code != http.StatusOK || e["install"] != save["install"] {
			t.Fatalf("a member could not read what was saved: %d %v", code, e)
		}
	})
}

// One org's environment is another org's nothing: the org is the file.
func TestAnOrgSeesOnlyItsOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, dir := mount(t)
		as(t, app, http.MethodPut, "/v1/environment/site", "acme", true, map[string]string{"install": "make"})
		if _, e := as(t, app, http.MethodGet, "/v1/environment/site", "beta", false, nil); e["state"] != stateNone {
			t.Fatalf("beta read acme's environment: %v", e)
		}
		files := strings.Join(stores(t, dir), "\n")
		want := [2]string{filepath.Join("orgs", "acme", "environment.db"), filepath.Join("orgs", "beta", "environment.db")}
		if locus.DSN() != "" {
			want = [2]string{"org/acme", "org/beta"} // each org's own schema, as the registry names it
		}
		if !strings.Contains(files, want[0]) || !strings.Contains(files, want[1]) {
			t.Fatalf("want each org's environment in its own store, have:\n%s", files)
		}
	})
}

// A secret's value goes to KMS at the codebase's ref and never comes back out of
// any route; its name is what the environment lists.
func TestASecretIsANameHereAndAValueInKMS(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, kms, _ := mount(t)
		const value = "postgres://u:hunter2-very-secret@db/app"
		code, e := as(t, app, http.MethodPut, "/v1/environment/site/secrets/DATABASE_URL", "acme", true, map[string]string{"value": value})
		if code != http.StatusOK {
			t.Fatalf("set answered %d %v", code, e)
		}
		if got := string(kms.m["orgs/acme/environment/site/DATABASE_URL"]); got != value {
			t.Fatalf("KMS holds %q at the codebase's ref", got)
		}
		if s := e["secrets"].([]any); len(s) != 1 || s[0] != "DATABASE_URL" {
			t.Fatalf("the environment lists %v", s)
		}
		_, read := as(t, app, http.MethodGet, "/v1/environment/site", "acme", false, nil)
		_, list := as(t, app, http.MethodGet, "/v1/environment", "acme", false, nil)
		for _, body := range []map[string]any{e, read, list} {
			if b, _ := json.Marshal(body); strings.Contains(string(b), "hunter2") {
				t.Fatalf("a route answered the value: %s", b)
			}
		}
		ns, err := principal.OrgNamespace("acme", "")
		if err != nil {
			t.Fatal(err)
		}
		own, err := mounted.State.stores.For(ns)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := own.db.Query(`SELECT repo, install, start, secrets, proposal, session FROM environments`)
		if err != nil {
			t.Fatalf("read the environment's own store: %v", err)
		}
		held := 0
		for rows.Next() {
			var repo, install, start, secrets, proposal, session string
			if err := rows.Scan(&repo, &install, &start, &secrets, &proposal, &session); err != nil {
				t.Fatal(err)
			}
			held++
			if strings.Contains(strings.Join([]string{repo, install, start, secrets, proposal, session}, "\n"), "hunter2") {
				t.Fatal("the value reached the environment's own store")
			}
		}
		_ = rows.Close()
		if held == 0 {
			t.Fatal("the store holds no row, so the scan proved nothing")
		}

		code, e = as(t, app, http.MethodDelete, "/v1/environment/site/secrets/DATABASE_URL", "acme", true, nil)
		if code != http.StatusOK || len(e["secrets"].([]any)) != 0 {
			t.Fatalf("remove answered %d %v", code, e)
		}
		if _, held := kms.m["orgs/acme/environment/site/DATABASE_URL"]; held {
			t.Fatal("the value outlived its name")
		}
		if code, _ := as(t, app, http.MethodDelete, "/v1/environment/site/secrets/DATABASE_URL", "acme", true, nil); code != http.StatusNotFound {
			t.Fatalf("removing a name that is not set answered %d", code)
		}
	})
}

// Names the run's shell could not export, or that are the run's own, are refused.
func TestASecretNameIsOneTheRunCanExport(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, _ := mount(t)
		for _, name := range []string{"HANZO_API_KEY", "hanzo_run", "PATH", "1ABC", "A-B", "BASH_ENV"} {
			if code, _ := as(t, app, http.MethodPut, "/v1/environment/site/secrets/"+name, "acme", true, map[string]string{"value": "v"}); code != http.StatusBadRequest {
				t.Errorf("%s answered %d, want 400", name, code)
			}
		}
		if code, _ := as(t, app, http.MethodPut, "/v1/environment/site/secrets/OK_NAME", "acme", true, map[string]string{"value": ""}); code != http.StatusBadRequest {
			t.Errorf("an empty value answered %d, want 400", code)
		}
		if code, _ := as(t, app, http.MethodGet, "/v1/environment/a.b.c%2Fd", "acme", false, nil); code == http.StatusOK {
			t.Error("a repository name holding a slash was read")
		}
	})
}

// Forgetting an environment takes its secrets' values out of KMS with it.
func TestForgetTakesTheSecretsWithIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, kms, _ := mount(t)
		as(t, app, http.MethodPut, "/v1/environment/site", "acme", true, map[string]string{"install": "make"})
		as(t, app, http.MethodPut, "/v1/environment/site/secrets/TOKEN", "acme", true, map[string]string{"value": "t0ken-value"})
		if code, _ := as(t, app, http.MethodDelete, "/v1/environment/site", "acme", true, nil); code != http.StatusNoContent {
			t.Fatalf("forget answered %d", code)
		}
		if len(kms.m) != 0 {
			t.Fatalf("KMS still holds %v", kms.m)
		}
		if _, e := as(t, app, http.MethodGet, "/v1/environment/site", "acme", false, nil); e["state"] != stateNone {
			t.Fatalf("a forgotten environment reads %v", e)
		}
	})
}

// A run asks over the plane, as its org: it is told the scripts and each secret's
// ref, never a value. A setup run is told the secrets and not the scripts.
func TestARunIsToldScriptsAndRefs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, _ := mount(t)
		as(t, app, http.MethodPut, "/v1/environment/site", "acme", true, map[string]string{"install": "make deps", "start": "make serve"})
		as(t, app, http.MethodPut, "/v1/environment/site/secrets/TOKEN", "acme", true, map[string]string{"value": "t0ken-value"})
		o := ops{s: mounted}

		got, err := o.prepare(cloud.For(context.Background(), "acme"), &client.Codebase{Repo: "site"})
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if got.Install != "make deps" || got.Start != "make serve" || len(got.Secrets) != 1 ||
			got.Secrets[0] != (client.Carried{Name: "TOKEN", Ref: "orgs/acme/environment/site/TOKEN"}) {
			t.Fatalf("a run was told %+v", got)
		}
		if b, _ := json.Marshal(got); strings.Contains(string(b), "t0ken-value") {
			t.Fatalf("a value crossed the plane: %s", b)
		}
		setup, _ := o.prepare(cloud.For(context.Background(), "acme"), &client.Codebase{Repo: "site", Setup: true})
		if setup.Install != "" || setup.Start != "" || len(setup.Secrets) != 1 {
			t.Fatalf("a setup run was told %+v", setup)
		}
		other, _ := o.prepare(cloud.For(context.Background(), "beta"), &client.Codebase{Repo: "site"})
		if other.Install != "" || len(other.Secrets) != 0 {
			t.Fatalf("beta's run was told acme's environment: %+v", other)
		}
		if _, err := o.prepare(context.Background(), &client.Codebase{Repo: "site"}); err == nil {
			t.Fatal("a call acting for no org was answered")
		}
	})
}

// A setup run's answer is kept as a proposal beside what is saved, with what does
// not hold dropped, and saving is the review that clears it.
func TestASuggestionIsProposedUntilSaved(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, _ := mount(t)
		as(t, app, http.MethodPut, "/v1/environment/site", "acme", true, map[string]string{"install": "make"})
		o := ops{s: mounted}
		kept, err := o.suggest(cloud.For(context.Background(), "acme"), &client.Suggestion{
			Repo: "site", Session: "sess_1", Install: "pnpm install", Start: "pnpm dev",
			Secrets: []string{"DATABASE_URL", "HANZO_API_KEY", "bad-name", "DATABASE_URL"}, Note: "Tests pass.",
		})
		if err != nil {
			t.Fatalf("suggest: %v", err)
		}
		if strings.Join(kept.Secrets, ",") != "DATABASE_URL" {
			t.Fatalf("the proposal kept secrets %v", kept.Secrets)
		}
		_, e := as(t, app, http.MethodGet, "/v1/environment/site", "acme", false, nil)
		p, _ := e["proposal"].(map[string]any)
		if e["state"] != stateProposed || e["install"] != "make" || p["install"] != "pnpm install" || e["session"] != "sess_1" {
			t.Fatalf("the proposal reads %v", e)
		}
		_, e = as(t, app, http.MethodPut, "/v1/environment/site", "acme", true, map[string]string{"install": p["install"].(string), "start": p["start"].(string)})
		if e["state"] != stateReady || e["proposal"] != nil || e["install"] != "pnpm install" {
			t.Fatalf("saving did not clear the proposal: %v", e)
		}
	})
}
