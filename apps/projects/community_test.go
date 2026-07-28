package projects

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// adminPatchProject PATCHes a project as a SuperAdmin. Moderation is the only
// admin-gated field on the project body, and asserting it needs a caller the
// ordinary `do` helper cannot make — so this is the ONE place that builds one.
func adminPatchProject(t *testing.T, app *zip.App, org, slug string, in map[string]any) projectView {
	t.Helper()
	b, _ := json.Marshal(in)
	req := httptest.NewRequest(http.MethodPatch, "/v1/projects/"+slug, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u_admin")
	req.Header.Set("X-User-IsAdmin", "true")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("admin patch: %v", err)
	}
	rb, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin patch want 200, got %d (%s)", resp.StatusCode, rb)
	}
	var out projectView
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

// recorder captures what crossed the seam. Registration is process-global, so it
// restores the previous publisher on cleanup and guards the slice — subtests and
// any detached caller share it.
type recorder struct {
	mu     sync.Mutex
	events []cloud.Visibility
}

func record(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{}
	cloud.RegisterPublisher(func(_ context.Context, ev cloud.Visibility) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, ev)
		return nil
	})
	t.Cleanup(func() { cloud.RegisterPublisher(nil) })
	return r
}

// last returns the most recent event for a slug, and whether there was one.
func (r *recorder) last(slug string) (cloud.Visibility, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].Slug == slug {
			return r.events[i], true
		}
	}
	return cloud.Visibility{}, false
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
		var p projectView
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
	cloud.RegisterPublisher(nil)

	if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": "Alone", "slug": "alone"}); code != http.StatusCreated {
		t.Fatalf("create with no git plane want 201, got %d (%s)", code, body)
	}
}
