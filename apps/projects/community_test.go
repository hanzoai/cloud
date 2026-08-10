package projects

import (
	"github.com/hanzoai/cloud/internal/planetest"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// adminPatchProject PATCHes a project as a SuperAdmin. Moderation is the only
// admin-gated field on the project body, and asserting it needs a caller the
// ordinary `do` helper cannot make — so this is the ONE place that builds one.
func adminPatchProject(t *testing.T, app *zip.App, org, slug string, in map[string]any) projectsProject {
	t.Helper()
	b, _ := json.Marshal(in)
	req := httptest.NewRequest(http.MethodPatch, "/v1/projects/"+slug, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u_admin")
	req.Header.Set("X-User-IsAdmin", "true")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("admin patch: %v", err)
	}
	rb, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin patch want 200, got %d (%s)", resp.StatusCode, rb)
	}
	var out projectsProject
	_ = json.Unmarshal(rb, &out)
	return out
}

// community_test.go proves the visibility seam carries the RESOLVED answer, and
// that every path which can change it fires.
//
// The failure this guards against is asymmetric: a publish that fails to reach
// git leaves a project un-browsable, which is annoying. A RETRACTION that fails
// to reach git leaves a private or moderated project's source world-readable,
// which cannot be taken back. So the retraction cases are the ones with teeth.

// recorder is a stand-in for the git app: it serves git.publish on git's own
// socket and records what crossed. Registration is process-global, so it also
// restores the run dir per test.
//
// It exercises the REAL path — frames over a unix socket — because the failure
// this guards against is precisely a call that resolves to nothing. The seam it
// replaced did exactly that: projects and git are separate processes, so an
// in-process publisher was never registered in projects' binary and every
// visibility change was dropped in silence.
type recorder struct {
	mu     sync.Mutex
	events []published
}

// published is one event AND the tenant it arrived for. The org is no longer a
// field of the event — it rides the caller — so capturing it here is what proves
// it crossed at all.
type published struct {
	plane.Visibility
	Org string
}

func record(t *testing.T) *recorder {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	r := &recorder{}
	app := zip.New(zip.Config{AppName: "git"})
	zip.Post[plane.Visibility, struct{}](app, "/git/publish",
		func(ctx context.Context, ev *plane.Visibility) (*struct{}, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.events = append(r.events, published{Visibility: *ev, Org: cloud.Who(ctx).Org})
			return nil, nil
		}, zip.WithOperationID(plane.GitPublish))
	go func() { _ = app.Listen(zip.SocketPath("git")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for i := 0; i < 200; i++ {
		if c, derr := net.Dial("unix", zip.SocketPath("git")); derr == nil {
			_ = c.Close()
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("git stand-in never began listening at %s", zip.SocketPath("git"))
	return r
}

// last returns the most recent event for a slug, and whether there was one.
func (r *recorder) last(slug string) (published, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].Slug == slug {
			return r.events[i], true
		}
	}
	return published{}, false
}

// TestPublishingReachesTheCanonicalRepo: creating a project emits its visibility,
// so a public project has a world-readable repo at git.hanzo.ai from the moment
// it exists — no second "share" step that can be forgotten.
func TestPublishingReachesTheCanonicalRepo(t *testing.T) {
	app := mountApp(t)
	rec := record(t)

	if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": "Board", "slug": "board", "description": "a board"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	ev, ok := rec.last("board")
	if !ok {
		t.Fatal("creating a project emitted no visibility event")
	}
	if ev.Org != "acme" || !ev.Listed {
		t.Fatalf("event = org %q listed %v, want acme/true", ev.Org, ev.Listed)
	}
	if ev.Name != "Board" || ev.Description != "a board" {
		t.Fatalf("the repo seed must carry the project's own name and description: %+v", ev)
	}
}

// TestRetractionReachesTheCanonicalRepo is the one with teeth: both ways a
// project can stop being visible — the publisher going private, and the platform
// moderating it — must reach the source, or the code stays readable after the
// listing is gone.
func TestRetractionReachesTheCanonicalRepo(t *testing.T) {
	t.Run("moderation", func(t *testing.T) {
		app := mountApp(t)
		rec := record(t)
		if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
			map[string]any{"name": "Spam", "slug": "spam"}); code != http.StatusCreated {
			t.Fatalf("create want 201, got %d (%s)", code, body)
		}
		if ev, _ := rec.last("spam"); !ev.Listed {
			t.Fatal("a new public project must be listed")
		}

		if p := adminPatchProject(t, app, "acme", "spam",
			map[string]any{"hidden": true, "hiddenReason": "spam"}); !p.Hidden {
			t.Fatal("admin hide did not take")
		}
		ev, ok := rec.last("spam")
		if !ok {
			t.Fatal("moderation emitted no event")
		}
		if ev.Listed {
			t.Fatal("a moderated project's source stayed world-readable")
		}

		// And lifting it restores the publisher's own choice, in one write.
		if p := adminPatchProject(t, app, "acme", "spam", map[string]any{"hidden": false}); p.Hidden {
			t.Fatal("admin lift did not take")
		}
		if ev, _ := rec.last("spam"); !ev.Listed {
			t.Fatal("lifting a moderation must restore the listing")
		}
	})

	t.Run("publisher goes private", func(t *testing.T) {
		app := mountApp(t)
		rec := record(t)
		if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
			map[string]any{"name": "Secret", "slug": "secret"}); code != http.StatusCreated {
			t.Fatalf("create want 201, got %d (%s)", code, body)
		}
		// Private is metered like every other paid surface. With no fee configured
		// the gate is open, which is the free-tier operator default — so this
		// asserts the SEAM, not the price.
		code, body := do(t, app, http.MethodPatch, "/v1/projects/secret", "acme",
			map[string]any{"visibility": "private"})
		if code != http.StatusOK {
			t.Fatalf("go private want 200, got %d (%s)", code, body)
		}
		var p projectsProject
		_ = json.Unmarshal(body, &p)
		if p.Visibility != Private {
			t.Fatalf("visibility = %q, want private", p.Visibility)
		}
		ev, ok := rec.last("secret")
		if !ok {
			t.Fatal("going private emitted no event")
		}
		if ev.Listed {
			t.Fatal("a private project's source stayed world-readable")
		}
	})
}

// TestPublishSurvivesAnUnmountedGitPlane: the project row is the source of truth
// and a binary that does not host git must still be able to publish. The seam is
// best-effort by design, so an absent (or failing) subscriber cannot fail a
// create — the next update reconciles.
func TestPublishSurvivesAnUnmountedGitPlane(t *testing.T) {
	app := mountApp(t)
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t)) // no git socket here

	if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": "Alone", "slug": "alone"}); code != http.StatusCreated {
		t.Fatalf("create with no git plane want 201, got %d (%s)", code, body)
	}
}
