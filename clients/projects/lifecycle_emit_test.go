package projects

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
)

// TestDeployEmitsLifecycle proves the projects deploy transitions fan out onto the
// cloud lifecycle stream: a git deploy emits BuildStarted, and the CI completion
// emits DeployLive — each carrying the repo name (derived from the linked repo URL,
// the git subscription key) and the deployment id. This is the producer half the
// git Slack-notifier consumes.
func TestDeployEmitsLifecycle(t *testing.T) {
	cloud.ResetLifecycleSubscribers()
	t.Cleanup(cloud.ResetLifecycleSubscribers)

	var mu sync.Mutex
	var evs []cloud.LifecycleEvent
	sig := make(chan struct{}, 16)
	cloud.RegisterLifecycleSubscriber(func(_ context.Context, ev cloud.LifecycleEvent) {
		mu.Lock()
		evs = append(evs, ev)
		mu.Unlock()
		sig <- struct{}{}
	})

	app := mountApp(t)

	// A project linked to a native repo → repoFromURL("…/myapp.git") == "myapp".
	if code, b := do(t, app, http.MethodPost, "/v1/projects", "acme", map[string]any{
		"name": "Site", "slug": "myapp",
		"repo": map[string]any{"url": "https://api.hanzo.test/v1/git/acme/myapp.git", "branch": "main"},
	}); code != http.StatusCreated {
		t.Fatalf("create project: %d %s", code, b)
	}

	// Git deploy → queued deployment + BuildStarted.
	code, b := do(t, app, http.MethodPost, "/v1/projects/myapp/deploy", "acme", map[string]any{"source": "git", "commit": "abc123"})
	if code != http.StatusAccepted {
		t.Fatalf("git deploy want 202, got %d (%s)", code, b)
	}
	var dep struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &dep)
	if dep.ID == "" {
		t.Fatalf("no deployment id: %s", b)
	}

	build := waitEvent(t, sig, &mu, &evs, cloud.LifecycleBuildStarted)
	if build.Repo != "myapp" || build.Org != "acme" || build.Branch != "main" || build.DeployID != dep.ID {
		t.Fatalf("BuildStarted event wrong: %+v", build)
	}

	// CI completion (status live) → DeployLive.
	if code, b := do(t, app, http.MethodPost, "/v1/projects/myapp/deployments/"+dep.ID+"/complete", "acme",
		map[string]any{"status": "live", "liveUrl": "https://site.example"}); code != http.StatusOK {
		t.Fatalf("complete want 200, got %d (%s)", code, b)
	}
	live := waitEvent(t, sig, &mu, &evs, cloud.LifecycleDeployLive)
	if live.Repo != "myapp" || live.Org != "acme" || live.DeployID != dep.ID {
		t.Fatalf("DeployLive event wrong: %+v", live)
	}
}

// waitEvent waits for a lifecycle event of the given kind to be captured, or fails.
func waitEvent(t *testing.T, sig chan struct{}, mu *sync.Mutex, evs *[]cloud.LifecycleEvent, kind cloud.LifecycleKind) cloud.LifecycleEvent {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		for _, ev := range *evs {
			if ev.Kind == kind {
				mu.Unlock()
				return ev
			}
		}
		mu.Unlock()
		select {
		case <-sig:
		case <-deadline:
			t.Fatalf("no %s lifecycle event within 3s", kind)
		}
	}
}
