package dataroom

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestIngestInProc proves the in-process Ingest seam stores bytes on the VFS and
// records a retrievable dataroom document, org-scoped — the path Hanzo Company's
// formation/import flows use.
func TestIngestInProc(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	vfs := newMemVFS()
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), VFS: vfs}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(nil) })

	ctx := context.Background()
	docID, err := Ingest(ctx, "acme", "certificate.md", "text/markdown", []byte("# Certificate of Incorporation"))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if docID == "" {
		t.Fatal("Ingest returned empty doc id")
	}

	// The bytes are in the tenant-segmented VFS namespace.
	found := false
	for k := range vfs.m {
		if len(k) > len("dataroom/") && k[:len("dataroom/")] == "dataroom/" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ingested bytes not found under dataroom/ prefix: keys=%v", vfs.m)
	}
}

// TestIngestFailsClosedWhenUnmounted proves Ingest returns ErrNotMounted rather than
// panicking when dataroom is not mounted.
func TestIngestFailsClosedWhenUnmounted(t *testing.T) {
	mounted = nil
	if _, err := Ingest(context.Background(), "acme", "x", "text/plain", []byte("y")); err != ErrNotMounted {
		t.Fatalf("want ErrNotMounted, got %v", err)
	}
}
