package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMirrorRenders verifies the render mirror: it scans the local studio output
// tree and POSTs every image (new or changed) to /v1/library/upload with the node's
// identity + subfolder + bearer, skips unchanged files across scans, and re-uploads
// a changed file. This is the path that lands EVERY render in studio.hanzo.ai even
// when it was produced outside the job/claim path.
func TestMirrorRenders(t *testing.T) {
	t.Setenv("HANZO_TOKEN", "test-bearer")

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "renders"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("renders/a.png", "\x89PNG-a")
	write("top.jpg", "jpg-top")
	write("notes.txt", "not an image")

	type up struct{ name, node, subpath, auth string }
	var got []up
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/library/upload" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_ = r.ParseMultipartForm(1 << 20)
		name := ""
		if r.MultipartForm != nil {
			for _, fh := range r.MultipartForm.File["image"] {
				name = fh.Filename
			}
		}
		got = append(got, up{
			name:    name,
			node:    r.URL.Query().Get("node"),
			subpath: r.URL.Query().Get("subpath"),
			auth:    r.Header.Get("Authorization"),
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"path":"x"}`))
	}))
	defer srv.Close()

	w := &worker{env: &Env{}, http: &http.Client{Timeout: 10 * time.Second}, identity: "spark"}
	seen := map[string]int64{}
	var buf bytes.Buffer

	w.mirrorRenders(context.Background(), &buf, dir, srv.URL, seen)
	if len(got) != 2 {
		t.Fatalf("uploaded %d files, want 2 images (the .txt is skipped): %+v", len(got), got)
	}
	var a *up
	for i := range got {
		if got[i].name == "a.png" {
			a = &got[i]
		}
	}
	if a == nil || a.subpath != "renders" || a.node != "spark" || a.auth != "Bearer test-bearer" {
		t.Fatalf("renders/a.png upload = %+v, want subpath=renders node=spark bearer set", a)
	}

	// A second scan re-uploads nothing (seen matches every size).
	got = nil
	w.mirrorRenders(context.Background(), &buf, dir, srv.URL, seen)
	if len(got) != 0 {
		t.Fatalf("second scan uploaded %d files, want 0 (all unchanged): %+v", len(got), got)
	}

	// A changed file is re-uploaded on the next scan.
	write("renders/a.png", "\x89PNG-a-grew")
	got = nil
	w.mirrorRenders(context.Background(), &buf, dir, srv.URL, seen)
	if len(got) != 1 || got[0].name != "a.png" {
		t.Fatalf("after change, uploaded %+v, want just renders/a.png", got)
	}
}
