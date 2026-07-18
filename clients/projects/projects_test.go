package projects

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "projects.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkProject(org, slug, name string) Project {
	return Project{
		ID: "proj_" + org + "_" + slug, Org: org, Slug: slug, Name: name,
		Framework: "static", Status: "draft", Bucket: "hanzo-sites",
		CreatedAt: 100, UpdatedAt: 100,
	}
}

func TestProjectCRUD(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	p := mkProject("hanzo", "maxpower", "MaxPower")
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetProject(ctx, "hanzo", "maxpower")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "MaxPower" || got.Status != "draft" {
		t.Fatalf("unexpected project: %+v", got)
	}

	// Cross-org isolation: another org cannot see it.
	if _, err := s.GetProject(ctx, "acme", "maxpower"); !errors.Is(err, errNotFound) {
		t.Fatalf("expected notFound for other org, got %v", err)
	}

	// Duplicate (org,slug) is a conflict.
	if err := s.CreateProject(ctx, p); !errors.Is(err, errConflict) {
		t.Fatalf("expected conflict on dup, got %v", err)
	}
	// Same slug under a DIFFERENT org is allowed.
	if err := s.CreateProject(ctx, mkProject("acme", "maxpower", "Acme Max")); err != nil {
		t.Fatalf("create other-org same-slug: %v", err)
	}

	// Update mutable fields.
	got.Name = "Max Power v2"
	got.Status = "live"
	got.LiveURL = "https://s3.hanzo.ai/hanzo-sites/hanzo/maxpower/index.html"
	got.UpdatedAt = 200
	if err := s.UpdateProject(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	reread, _ := s.GetProject(ctx, "hanzo", "maxpower")
	if reread.Name != "Max Power v2" || reread.Status != "live" || reread.LiveURL == "" {
		t.Fatalf("update not persisted: %+v", reread)
	}
}

func TestListOrderingAndIsolation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	a := mkProject("hanzo", "alpha", "Alpha")
	a.UpdatedAt = 100
	b := mkProject("hanzo", "bravo", "Bravo")
	b.UpdatedAt = 300
	cc := mkProject("hanzo", "charlie", "Charlie")
	cc.UpdatedAt = 200
	other := mkProject("acme", "delta", "Delta")
	for _, p := range []Project{a, b, cc, other} {
		if err := s.CreateProject(ctx, p); err != nil {
			t.Fatalf("create %s: %v", p.Slug, err)
		}
	}

	list, err := s.ListProjects(ctx, "hanzo")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 hanzo projects, got %d", len(list))
	}
	// Most-recently-updated first: bravo(300), charlie(200), alpha(100).
	if list[0].Slug != "bravo" || list[1].Slug != "charlie" || list[2].Slug != "alpha" {
		t.Fatalf("bad order: %s,%s,%s", list[0].Slug, list[1].Slug, list[2].Slug)
	}
}

func TestDeleteCascadesDeployments(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := mkProject("hanzo", "maxpower", "MaxPower")
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.InsertDeployment(ctx, Deployment{
		ID: "dep_1", ProjectID: p.ID, Org: "hanzo", Version: 1, Status: "live",
		Source: "upload", CreatedAt: 100, UpdatedAt: 100,
	}); err != nil {
		t.Fatalf("insert deployment: %v", err)
	}

	deleted, ok, err := s.DeleteProject(ctx, "hanzo", "maxpower")
	if err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}
	if deleted.ID != p.ID {
		t.Fatalf("delete returned wrong project: %+v", deleted)
	}
	if _, err := s.GetDeployment(ctx, "hanzo", p.ID, "dep_1"); !errors.Is(err, errNotFound) {
		t.Fatalf("expected deployment gone, got %v", err)
	}
	// Deleting a missing project reports not-deleted, not an error.
	if _, ok, err := s.DeleteProject(ctx, "hanzo", "maxpower"); ok || err != nil {
		t.Fatalf("expected (false,nil) on missing delete, got (%v,%v)", ok, err)
	}
}

func TestDeploymentVersioning(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := mkProject("hanzo", "maxpower", "MaxPower")
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}

	v, err := s.NextVersion(ctx, p.ID)
	if err != nil || v != 1 {
		t.Fatalf("first version expected 1, got %d (err=%v)", v, err)
	}
	for i := 1; i <= 3; i++ {
		v, _ := s.NextVersion(ctx, p.ID)
		if v != i {
			t.Fatalf("version expected %d, got %d", i, v)
		}
		id, _ := genID("dep")
		if err := s.InsertDeployment(ctx, Deployment{
			ID: id, ProjectID: p.ID, Org: "hanzo", Version: v, Status: "live",
			Source: "upload", CreatedAt: int64(i), UpdatedAt: int64(i),
		}); err != nil {
			t.Fatalf("insert v%d: %v", v, err)
		}
	}
	deps, err := s.ListDeployments(ctx, "hanzo", p.ID)
	if err != nil {
		t.Fatalf("list deployments: %v", err)
	}
	if len(deps) != 3 || deps[0].Version != 3 {
		t.Fatalf("expected 3 deployments newest-first, got %d (first v=%d)", len(deps), deps[0].Version)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"MaxPower":             "maxpower",
		"Max Power":            "max-power",
		"  Dave's MaxPower!! ": "dave-s-maxpower",
		"a/b\\c":               "a-b-c",
		"---weird---":          "weird",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q)=%q want %q", in, got, want)
		}
	}
	// slugify output must satisfy the slug regex (when non-empty).
	for _, in := range []string{"MaxPower", "Max Power", "Dave's Site"} {
		if got := slugify(in); !slugRE.MatchString(got) {
			t.Errorf("slugify(%q)=%q does not match slugRE", in, got)
		}
	}
}

func TestProviderFromURL(t *testing.T) {
	cases := map[string]string{
		"":                             "",
		"https://github.com/hanzoai/x": "github",
		"git@github.com:hanzoai/x.git": "github",
		"https://gitlab.com/g/x":       "gitlab",
		"https://bitbucket.org/b/x":    "bitbucket",
		"https://git.example.com/x":    "git",
	}
	for in, want := range cases {
		if got := providerFromURL(in); got != want {
			t.Errorf("providerFromURL(%q)=%q want %q", in, got, want)
		}
	}
}

func TestSafeRel(t *testing.T) {
	bad := []string{"/etc/passwd", "../escape", "a/../../b", "../../x"}
	for _, p := range bad {
		if _, ok := safeRel(p); ok {
			t.Errorf("safeRel(%q) should be rejected", p)
		}
	}
	good := map[string]string{
		"index.html":        "index.html",
		"./assets/app.js":   "assets/app.js",
		"a/b/c.css":         "a/b/c.css",
		"dir/../index.html": "index.html",
	}
	for in, want := range good {
		got, ok := safeRel(in)
		if !ok || got != want {
			t.Errorf("safeRel(%q)=(%q,%v) want (%q,true)", in, got, ok, want)
		}
	}
}

// buildTar makes an (optionally gzipped) tar from a path→content map.
func buildTar(t *testing.T, gz bool, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	var tw *tar.Writer
	var zw *gzip.Writer
	if gz {
		zw = gzip.NewWriter(&buf)
		tw = tar.NewWriter(zw)
	} else {
		tw = tar.NewWriter(&buf)
	}
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if gz {
		if err := zw.Close(); err != nil {
			t.Fatalf("gzip close: %v", err)
		}
	}
	return buf.Bytes()
}

func TestWalkTarGz(t *testing.T) {
	files := map[string]string{
		"index.html":       "<!doctype html><title>MaxPower</title>",
		"assets/app.js":    "console.log('hi')",
		"assets/style.css": "body{}",
	}

	for _, gz := range []bool{false, true} {
		st, err := walkTarGz(bytes.NewReader(buildTar(t, gz, files)))
		if err != nil {
			t.Fatalf("walkTarGz(gz=%v): %v", gz, err)
		}
		if len(st.files) != 3 {
			t.Fatalf("gz=%v: expected 3 files, got %d", gz, len(st.files))
		}
		if string(st.files["index.html"]) != files["index.html"] {
			t.Fatalf("gz=%v: index.html content mismatch", gz)
		}
		if _, ok := st.files["assets/app.js"]; !ok {
			t.Fatalf("gz=%v: nested file missing", gz)
		}
		if st.bytes == 0 {
			t.Fatalf("gz=%v: bytes not counted", gz)
		}
	}
}

func TestWalkTarGzRejects(t *testing.T) {
	// Missing index.html at root.
	if _, err := walkTarGz(bytes.NewReader(buildTar(t, true, map[string]string{"about.html": "x"}))); err == nil {
		t.Fatal("expected error for missing index.html")
	}
	// Empty artifact.
	if _, err := walkTarGz(bytes.NewReader(buildTar(t, false, map[string]string{}))); err == nil {
		t.Fatal("expected error for empty artifact")
	}
	// Path traversal entry.
	if _, err := walkTarGz(bytes.NewReader(buildTar(t, false, map[string]string{
		"index.html":  "ok",
		"../../etc/x": "evil",
	}))); err == nil {
		t.Fatal("expected error for path traversal")
	}
}

// TestSitePrefixAndSiteURL pins the storage layout (sitePrefix) and the ONE
// canonical public URL of a deployed site: the pretty <slug>.<org>.<apex> host. The old
// raw-S3 liveURL form is gone (forward-perfection) — a published site is only ever
// addressed by its pretty host, which is stable across redeploys of the same slug.
func TestSitePrefixAndSiteURL(t *testing.T) {
	if got := sitePrefix("hanzo", "maxpower"); got != "hanzo/maxpower" {
		t.Fatalf("sitePrefix=%q", got)
	}
	s := &cloud.Service[state]{State: state{apex: "hanzo.app"}}
	// Org-scoped: the org lives in the hostname (<slug>.<org>.<apex>), so two orgs
	// can each own slug "myapp" and their public URLs never collide.
	if got := siteURL(s, "maxpower", "myapp"); got != "https://myapp.maxpower.hanzo.app" {
		t.Fatalf("siteURL=%q want %q", got, "https://myapp.maxpower.hanzo.app")
	}
	if siteURL(s, "acme", "myapp") == siteURL(s, "maxpower", "myapp") {
		t.Fatal("same slug in different orgs must yield DIFFERENT URLs (org-scoped)")
	}
	// Deterministic across calls — the redeploy URL-invariant at the URL layer.
	if siteURL(s, "maxpower", "myapp") != siteURL(s, "maxpower", "myapp") {
		t.Fatal("siteURL must be deterministic for a given (org,slug)")
	}
}
