package forge_test

// public_live_test.go answers the one question a stub cannot: does closing a
// repository actually stop people reading it?
//
// Everything else about this seam is our code and can be proved against a fake.
// "Private means nobody may read it" is a claim about FORGEJO, and the whole
// design — born private, opened by a write, closed by the same write — is worth
// nothing if that claim is false or if the fork answers the edit and keeps
// serving the repository. So this creates a repository on a real forge, walks it
// open and closed, and asks as a reader with NO CREDENTIAL AT ALL, both at the
// API and at the git endpoint a clone actually uses.
//
// It is SKIPPED unless FORGE_LIVE_TOKEN and FORGE_LIVE_HOST are both set, and
// the host has no default ON PURPOSE: this test creates and mutates
// repositories, so it must never be one forgotten variable away from doing that
// to the production forge. Point it at a disposable one:
//
//	docker run -d --name forge-live -p 3030:3000 \
//	  -e INSTALL_LOCK=true -e SECRET_KEY=… -e DISABLE_SSH=true \
//	  -e ROOT_URL=http://localhost:3030/ ghcr.io/hanzoai/git:latest
//	FORGE_LIVE_HOST=http://localhost:3030 FORGE_LIVE_TOKEN=… \
//	  FORGE_LIVE_OWNER=hanzo-community go test -run Live_ ./forge/
//
// Re-run it when the forge is upgraded. It is the only thing standing between
// this package and a fork that quietly stops honouring `private`.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/forge"
)

// live builds a machine client against a DISPOSABLE forge, plus the origin an
// anonymous reader would use.
func live(t *testing.T) (*forge.Client, string, string) {
	t.Helper()
	token := strings.TrimSpace(os.Getenv("FORGE_LIVE_TOKEN"))
	host := strings.TrimSpace(os.Getenv("FORGE_LIVE_HOST"))
	owner := strings.TrimSpace(os.Getenv("FORGE_LIVE_OWNER"))
	if token == "" || host == "" || owner == "" {
		t.Skip("set FORGE_LIVE_HOST (a disposable forge), FORGE_LIVE_TOKEN and FORGE_LIVE_OWNER")
	}
	c, err := forge.New(host, token)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	return c.Machine(), owner, strings.TrimSuffix(host, "/")
}

// anon reads as the world: no Authorization header, no cookie, nothing.
func anon(t *testing.T, url string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("anonymous read: %v", err)
	}
	// Never follow a redirect: a forge that bounces an anonymous reader to a login
	// page is REFUSING, and following it would score the login page as a 200.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("anonymous read %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// readable reports whether the world can read this repository, asked BOTH ways:
// the API a console reads and the endpoint `git clone` starts from. They are
// separate permissions in Forgejo's routing, and a repository that answered the
// API with 404 while still serving its refs would be closed only on paper.
func readable(t *testing.T, origin, owner, repo string) (api bool, clone bool) {
	t.Helper()
	a := anon(t, fmt.Sprintf("%s/v1/repos/%s/%s", origin, owner, repo))
	c := anon(t, fmt.Sprintf("%s/%s/%s.git/info/refs?service=git-upload-pack", origin, owner, repo))
	return a == http.StatusOK, c == http.StatusOK
}

// TestLive_ClosingARepositoryClosesReadAccess is the load-bearing check of the
// whole visibility seam: the retraction that must never silently fail.
func TestLive_ClosingARepositoryClosesReadAccess(t *testing.T) {
	c, owner, origin := live(t)
	ctx := t.Context()

	var b [6]byte
	_, _ = rand.Read(b[:])
	repo := "visibility-check-" + hex.EncodeToString(b[:])

	if _, err := c.Ensure(ctx, owner, repo, "created by forge/public_live_test.go"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(func() { drop(t, origin, owner, repo) })

	// BORN CLOSED. A repository that were briefly readable at creation would leak
	// every private project at the moment it is made.
	if api, clone := readable(t, origin, owner, repo); api || clone {
		t.Fatalf("a new repository was readable by the world: api=%v clone=%v", api, clone)
	}

	if err := c.SetPublic(ctx, owner, repo, true); err != nil {
		t.Fatalf("open: %v", err)
	}
	api, clone := readable(t, origin, owner, repo)
	if !api || !clone {
		// If this fails on a deployment with REQUIRE_SIGNIN_VIEW on, that is the
		// deployment refusing anonymous reads entirely — which is safe, and means
		// "public" there means "visible to anyone with an account".
		t.Fatalf("an opened repository is not readable: api=%v clone=%v "+
			"(is this forge refusing anonymous reads outright?)", api, clone)
	}

	// THE ASSERTION THIS FILE EXISTS FOR.
	if err := c.SetPublic(ctx, owner, repo, false); err != nil {
		t.Fatalf("close: %v", err)
	}
	if api, clone := readable(t, origin, owner, repo); api || clone {
		t.Fatalf("a CLOSED repository is still readable by the world: api=%v clone=%v", api, clone)
	}

	// Closing again is not an error and does not re-open it: the seam fires on
	// every write, so the second one has to be free.
	if err := c.SetPublic(ctx, owner, repo, false); err != nil {
		t.Fatalf("close again: %v", err)
	}
	if api, clone := readable(t, origin, owner, repo); api || clone {
		t.Fatalf("closing twice re-opened it: api=%v clone=%v", api, clone)
	}

	// And it comes back: the bit is a flip, not a one-way door, which is what
	// makes going private a decision somebody can take back.
	if err := c.SetPublic(ctx, owner, repo, true); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	if api, _ := readable(t, origin, owner, repo); !api {
		t.Fatal("a re-opened repository is not readable")
	}
}

// drop removes the repository this test made. It is a raw call rather than a
// method on the client on purpose: deleting repositories is not something this
// platform's code should be able to do, so the capability lives in the test that
// needs it and nowhere else.
func drop(t *testing.T, origin, owner, repo string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete,
		fmt.Sprintf("%s/v1/repos/%s/%s", origin, owner, repo), nil)
	if err != nil {
		t.Logf("cleanup: %v", err)
		return
	}
	req.Header.Set("Authorization", "token "+strings.TrimSpace(os.Getenv("FORGE_LIVE_TOKEN")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("cleanup %s/%s: %v", owner, repo, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		t.Logf("cleanup %s/%s: status %d (delete it by hand)", owner, repo, resp.StatusCode)
	}
}
