package git

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
)

// index_on_import_test.go proves the /v1/code-covers-every-mirrored-repo fix: a
// one-shot import emits the SAME push.landed the push + inbound-sync paths emit, so
// the index_on_push reactor indexes the repo ON IMPORT — not only after its next
// push. Origin is the source host (the outbound mirror's echo-suppression key).

func TestImportEmitsPushLandedForIndex(t *testing.T) {
	app := mountApp(t)
	_ = liveServer(t, app)
	root := t.TempDir()
	srcBase := serveGitHTTP(t, root)
	src := newSource(t, root, srcBase, "widgets", "v1")

	ch := captureLifecycle(t)

	// No MirrorURL → no outbound target, so the emit drives the index only (no push
	// attempt to a real host in the test).
	req := cloud.GitImportReq{Org: "acme", Repo: "widgets", CloneURL: src.url}
	if err := (githubImporter{}).ImportRepo(context.Background(), req); err != nil {
		t.Fatalf("import: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Org != "acme" || ev.Repo != "widgets" || ev.Branch != "main" {
			t.Fatalf("event coords wrong: %+v", ev)
		}
		if ev.After != src.tip() {
			t.Fatalf("event After %s != source tip %s", ev.After, src.tip())
		}
		if ev.Origin != hostFromURL(t, src.url) {
			t.Fatalf("Origin %q != source host %q (echo-suppression key)", ev.Origin, hostFromURL(t, src.url))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("import emitted no push.landed — /v1/code would not cover the repo until its next push")
	}
}

func TestImportHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/widgets.git": "github.com",
		"https://GitHub.com/a/b":              "github.com",
		"":                                    "",
		"::not a url":                         "",
	} {
		if got := importHost(in); got != want {
			t.Fatalf("importHost(%q)=%q want %q", in, got, want)
		}
	}
}
