//go:build dryrun

package platform

import (
	"context"
	"fmt"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
)

// dryrun_manual_test.go — the REAL write, against a REAL universe, to a SCRATCH
// branch. Build-tagged `dryrun` so it never runs in CI: it needs a git remote
// holding the actual fleet inventory, which no test machine has.
//
//	go test -tags dryrun ./apps/platform/ -run TestDryRun -v \
//	  -args   # UNIVERSE_DRYRUN_REMOTE=<a bare clone of hanzo/universe>
//
// It writes to `deploy/acme/dryrun/<tag>` and NEVER to main. What it
// proves that the fixtures cannot: the client works against the real 105-file
// inventory, the real directory layout, and a real git history.
func TestDryRunAgainstARealUniverse(t *testing.T) {
	bare := strings.TrimSpace(os.Getenv("UNIVERSE_DRYRUN_REMOTE"))
	if bare == "" {
		t.Skip("set UNIVERSE_DRYRUN_REMOTE to a scratch bare clone of hanzo/universe")
	}
	// Served over HTTP, not file://. pinGitEnv sets GIT_ALLOW_PROTOCOL=http:https
	// — a real control, and the reason a local path cannot be handed to the client.
	remote := serveBare(t, bare)
	defer swapUniverseRemote(remote)()

	k := newFakeKMS()
	if err := k.PutSecret(context.Background(), pinTokenRef, []byte("dryrun")); err != nil {
		t.Fatal(err)
	}
	s := &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("dryrun"), KMS: k}}

	spec := declareSpec{
		Name:       "dryrun",
		Org:        "acme",
		Repository: "ghcr.io/hanzoai/acme/dryrun",
		Tag:        "bld_dryrun1",
		Hosts:      []string{"dryrun.acme.hanzo.app"},
		// Seal-by-default: a value is a reference unless the caller marks it
		// public. NODE_ENV is configuration an operator must be able to read.
		Env:        []declareEnv{{Name: "NODE_ENV", Value: "production", Public: true}},
		Public:     []string{"NODE_ENV"},
		SecretKeys: []string{"DATABASE_PASSWORD"},
		Port:       3000,
		Replicas:   2,
		Automated:  true,
		Origin:     "https://git.hanzo.ai/acme/dryrun",
	}

	res, err := declare(s, context.Background(), spec, modeBranch)
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	fmt.Printf("\n=== DRY RUN RESULT ===\nref:      %s\ncommit:   %s\ncreated:  %t  changed: %t  live: %t\npath:     %s\napp:      %s (project %s)\nreview:   %s\n\n",
		res.Ref, res.Commit, res.Created, res.Changed, res.Live,
		res.Declaration.Path, res.Declaration.Application, res.Declaration.Project, res.Review)

	if res.Live {
		t.Fatal("a branch write reported itself live")
	}
	if res.Ref == universeBranch {
		t.Fatal("the dry run wrote main")
	}

	// Read the real inventory back through the same client the board uses.
	got, err := declarations(s, context.Background(), "hanzo")
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	fmt.Printf("=== REAL INVENTORY (values/hanzo): %d declarations ===\n", len(got))
	for _, d := range got[:min(8, len(got))] {
		fmt.Printf("  %-22s %-44s %-12s automated=%t hosts=%v\n",
			d.Application, d.Repository, d.Tag, d.Automated, d.Hosts)
	}
	if len(got) < 50 {
		t.Fatalf("the real inventory read only %d declarations; expected the whole fleet", len(got))
	}
}

// serveBare publishes a bare repository over git's smart-HTTP transport, which
// is the only shape the hardened git environment will speak to.
func serveBare(t *testing.T, bare string) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git is not installed: %v", err)
	}
	root, name := filepath.Dir(bare), filepath.Base(bare)
	srv := httptest.NewServer(&cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(srv.Close)
	return srv.URL + "/" + name
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
