package platform

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zap-proto/zip"
)

// postApp creates an app under project "web" for org and returns (status, view).
func postApp(t *testing.T, app *zip.App, org string, body map[string]any) (int, appView) {
	t.Helper()
	code, raw := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", org, body)
	var v appView
	if code == http.StatusCreated {
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("decode appView: %v (%s)", err, raw)
		}
	}
	return code, v
}

// TestBuildTypeSurface pins the ONE-way builder surface: a git app defaults to
// hanzoai/pack (never the retired nixpacks), accepts only pack|dockerfile, and an
// image app is always buildType "image" regardless of what the client sends.
func TestBuildTypeSurface(t *testing.T) {
	app := mountApp(t)
	const org = "acme"
	seedProject(t, app, org, "web")
	const repo = "https://github.com/hanzoai/app"

	// git source, no buildType → default is pack (NOT nixpacks).
	if code, v := postApp(t, app, org, map[string]any{
		"name": "a1", "slug": "a1", "source": "git", "repo": map[string]any{"url": repo},
	}); code != http.StatusCreated || v.BuildType != "pack" {
		t.Fatalf("git default: want 201 buildType=pack, got %d %q", code, v.BuildType)
	}

	// git source, explicit dockerfile escape hatch → accepted, preserved.
	if code, v := postApp(t, app, org, map[string]any{
		"name": "a2", "slug": "a2", "source": "git", "repo": map[string]any{"url": repo}, "buildType": "dockerfile",
	}); code != http.StatusCreated || v.BuildType != "dockerfile" {
		t.Fatalf("git dockerfile: want 201 buildType=dockerfile, got %d %q", code, v.BuildType)
	}

	// git source, retired buildType → 400 (nixpacks/buildpacks/static are gone).
	for _, dead := range []string{"nixpacks", "buildpacks", "static", "image", "railpack"} {
		if code, _ := postApp(t, app, org, map[string]any{
			"name": "x", "slug": "x-" + dead, "source": "git", "repo": map[string]any{"url": repo}, "buildType": dead,
		}); code != http.StatusBadRequest {
			t.Fatalf("git buildType=%q: want 400, got %d", dead, code)
		}
	}

	// image source → buildType forced to "image", ignoring a client-sent value.
	if code, v := postApp(t, app, org, map[string]any{
		"name": "a3", "slug": "a3", "source": "image",
		"image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"}, "buildType": "pack",
	}); code != http.StatusCreated || v.BuildType != "image" {
		t.Fatalf("image source: want 201 buildType=image, got %d %q", code, v.BuildType)
	}
}
