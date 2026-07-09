package projects

import (
	"archive/zip"
	"bytes"
	"testing"
)

// buildZip builds an in-memory ZIP with the given path→content entries — the
// browser-upload artifact format, the counterpart to buildTar.
func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %q: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func siteKeys(st *site) []string {
	out := make([]string, 0, len(st.files))
	for k := range st.files {
		out = append(out, k)
	}
	return out
}

func TestWalkZip(t *testing.T) {
	files := map[string]string{
		"index.html":       "<!doctype html><title>Zip</title>",
		"assets/app.js":    "console.log('hi')",
		"assets/style.css": "body{}",
	}
	st, err := walkZip(buildZip(t, files))
	if err != nil {
		t.Fatalf("walkZip: %v", err)
	}
	if len(st.files) != 3 {
		t.Fatalf("want 3 files, got %d (%v)", len(st.files), siteKeys(st))
	}
	if string(st.files["index.html"]) != files["index.html"] {
		t.Fatalf("index.html content mismatch")
	}
	if _, ok := st.files["assets/app.js"]; !ok {
		t.Fatal("nested file missing")
	}
	if st.bytes == 0 {
		t.Fatal("bytes not counted")
	}
}

// TestWalkArtifactDispatch proves the ONE deploy contract holds across all three
// container formats: zip, tar.gz, plain tar — each yields the same file map.
func TestWalkArtifactDispatch(t *testing.T) {
	files := map[string]string{"index.html": "ok", "a.js": "x"}
	cases := map[string][]byte{
		"zip":    buildZip(t, files),
		"tar.gz": buildTar(t, true, files),
		"tar":    buildTar(t, false, files),
	}
	for name, raw := range cases {
		st, err := walkArtifact(raw)
		if err != nil {
			t.Fatalf("walkArtifact(%s): %v", name, err)
		}
		if len(st.files) != 2 {
			t.Fatalf("walkArtifact(%s): want 2 files, got %d", name, len(st.files))
		}
	}
}

// TestWalkArtifactStripsSingleRoot: a zip made from a project FOLDER (everything
// under "site/") is re-rooted so index.html lands at the top — the natural,
// forgiving UX for a drag-and-drop upload.
func TestWalkArtifactStripsSingleRoot(t *testing.T) {
	files := map[string]string{
		"site/index.html":    "root",
		"site/assets/app.js": "x",
		"site/css/style.css": "y",
	}
	st, err := walkArtifact(buildZip(t, files))
	if err != nil {
		t.Fatalf("walkArtifact: %v", err)
	}
	if _, ok := st.files["index.html"]; !ok {
		t.Fatalf("single root not stripped; keys=%v", siteKeys(st))
	}
	if _, ok := st.files["assets/app.js"]; !ok {
		t.Fatalf("nested file not re-rooted; keys=%v", siteKeys(st))
	}
	if len(st.files) != 3 {
		t.Fatalf("want 3 files after strip, got %d (%v)", len(st.files), siteKeys(st))
	}
}

// TestWalkArtifactNoStripMultiRoot: two top-level dirs and no root index.html →
// there is no single wrapper to strip → honest "missing index.html".
func TestWalkArtifactNoStripMultiRoot(t *testing.T) {
	files := map[string]string{"a/index.html": "x", "b/style.css": "y"}
	if _, err := walkArtifact(buildZip(t, files)); err == nil {
		t.Fatal("expected missing-index.html error for multi-root artifact")
	}
}

func TestWalkZipRejects(t *testing.T) {
	// Missing index.html anywhere.
	if _, err := walkZip(buildZip(t, map[string]string{"about.html": "x"})); err == nil {
		t.Fatal("expected error for missing index.html")
	}
	// Path traversal entry.
	if _, err := walkZip(buildZip(t, map[string]string{"index.html": "ok", "../../etc/x": "evil"})); err == nil {
		t.Fatal("expected error for path traversal")
	}
	// Empty.
	if _, err := walkZip(buildZip(t, map[string]string{})); err == nil {
		t.Fatal("expected error for empty artifact")
	}
}

func TestIsZip(t *testing.T) {
	if !isZip(buildZip(t, map[string]string{"index.html": "x"})) {
		t.Fatal("real zip not detected")
	}
	if isZip(buildTar(t, true, map[string]string{"index.html": "x"})) {
		t.Fatal("tar.gz mis-detected as zip")
	}
	if isZip([]byte{}) || isZip([]byte("PK")) || isZip([]byte("not a zip")) {
		t.Fatal("non-zip mis-detected as zip")
	}
}
