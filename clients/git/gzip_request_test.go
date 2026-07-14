package git

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud"
)

// TestUploadPackAcceptsGzippedRequest guards the clone/fetch serving path against
// a double-decode regression. A real git client gzips the ref-negotiation request
// once a repo carries enough refs — below that, gzip overhead loses and git sends
// it plain, which is exactly why the small-repo clone round-trip tests never
// exercised the compressed path. The HTTP framework inflates a
// Content-Encoding: gzip body transparently, so the pack handler reads c.Body()
// verbatim; an earlier revision inflated it a second time and returned 400
// "invalid gzip request body" for every compressed clone (all but trivially small
// repos). This posts a genuinely gzipped upload-pack request through the live
// Fiber server and asserts a packfile comes back.
func TestUploadPackAcceptsGzippedRequest(t *testing.T) {
	cloud.RegisterPushBuilder(func(context.Context, cloud.GitPushEvent) error { return nil })
	t.Cleanup(func() { cloud.RegisterPushBuilder(nil) })

	app := mountApp(t)
	base := liveServer(t, app)
	if code, b := do(t, app, "POST", "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}

	// Seed one commit so there is a tip to negotiate against.
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# gzip req\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "first")
	commit := gitOut(t, work, "rev-parse", "HEAD")
	gitRun(t, work, "remote", "add", "origin", base+"/v1/git/acme/code.git")
	gitRun(t, work, append(orgHeaderArgs("acme"), "push", "origin", "main")...)

	// A minimal protocol-v0 upload-pack request (want <tip> / flush / done),
	// gzip-compressed with Content-Encoding: gzip — what a real git client sends
	// for a repo with enough refs.
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	reqBody := pkt("want "+commit+"\n") + "0000" + pkt("done\n")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write([]byte(reqBody)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest("POST", base+"/v1/git/acme/code.git/git-upload-pack", bytes.NewReader(gz.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "u_acme")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload-pack POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("gzipped upload-pack: status %d, body %q", resp.StatusCode, out)
	}
	if !bytes.Contains(out, []byte("PACK")) {
		head := out
		if len(head) > 80 {
			head = head[:80]
		}
		t.Fatalf("gzipped upload-pack: response is not a packfile: %q", head)
	}
}
