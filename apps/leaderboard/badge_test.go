package leaderboard

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

func doBadge(t *testing.T, app *zip.App, path string, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func TestLeaderboardBadgeRendersListedOrgsAndHidesPrivateOnes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ladder44(t)
		app := mountApp(t)

		// Opt in builder40 (rank #1, 4000 pts) via org opt-in with display "Ada",
		// builder39 (rank #2, 3900 pts) via user opt-in with handle "Grace",
		// and explicitly withdraw builder38.
		seedOrgOptin(t, "builder40", "Ada", true)
		seedUserOptin(t, "builder39/grace", "builder39", "Grace", true)
		seedOrgOptin(t, "builder38", "Hidden", false)

		// Listed org resolves by org slug (case-insensitive) and by chosen display handle.
		for _, path := range []string{
			"/v1/leaderboard/badge/builder40.svg",
			"/v1/leaderboard/badge/BUILDER40.svg",
			"/v1/leaderboard/badge/ada.svg",
		} {
			code, h, body := doBadge(t, app, path, nil)
			if code != http.StatusOK {
				t.Fatalf("GET %s = %d: %s", path, code, body)
			}
			if ct := h.Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
				t.Errorf("GET %s Content-Type = %q", path, ct)
			}
			if cc := h.Get("Cache-Control"); cc != badgeCacheControl {
				t.Errorf("GET %s Cache-Control = %q, want %q", path, cc, badgeCacheControl)
			}
			if h.Get("ETag") == "" {
				t.Errorf("GET %s missing ETag", path)
			}
			svg := string(body)
			if !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, "#1") || !strings.Contains(svg, "4000 pts") {
				t.Errorf("GET %s unexpected svg: %s", path, svg)
			}
		}

		// Card style + light theme + custom label + user-listed handle.
		code, _, body := doBadge(t, app, "/v1/leaderboard/badge/grace.svg?theme=light&style=card&label=Hanzo+Rank", nil)
		if code != http.StatusOK {
			t.Fatalf("grace card = %d: %s", code, body)
		}
		svg := string(body)
		for _, want := range []string{"Grace", "#2", "3900 pts", "Hanzo Rank"} {
			if !strings.Contains(svg, want) {
				t.Errorf("grace card missing %q: %s", want, svg)
			}
		}

		// Privacy: unlisted org, explicitly withdrawn org, held org (admin), unknown handle,
		// and missing .svg extension all return the same 404.
		for _, path := range []string{
			"/v1/leaderboard/badge/builder37.svg",
			"/v1/leaderboard/badge/builder38.svg",
			"/v1/leaderboard/badge/hidden.svg",
			"/v1/leaderboard/badge/admin.svg",
			"/v1/leaderboard/badge/nobody.svg",
			"/v1/leaderboard/badge/builder40",
		} {
			if code, _, _ := doBadge(t, app, path, nil); code != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404", path, code)
			}
		}

		// Bad options return 400 before handle lookup (even for unknown handles).
		for _, path := range []string{
			"/v1/leaderboard/badge/nobody.svg?theme=neon",
			"/v1/leaderboard/badge/nobody.svg?style=poster",
			"/v1/leaderboard/badge/nobody.svg?label=" + strings.Repeat("x", 25),
		} {
			if code, _, _ := doBadge(t, app, path, nil); code != http.StatusBadRequest {
				t.Errorf("GET %s = %d, want 400", path, code)
			}
		}
	})
}

func TestLeaderboardBadgeCachesHourlyRevalidatesAndSurvivesBlip(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var dirCalls atomic.Int32
		var dirFail atomic.Bool
		var tokens atomic.Uint64
		tokens.Store(400_000_000) // 4000 pts

		t.Setenv("CLOUD_BRAND", "hanzo")
		origDir := directoryFn
		directoryFn = func(context.Context) (*client.Directory, error) {
			dirCalls.Add(1)
			if dirFail.Load() {
				return nil, errors.New("iam blip")
			}
			return &client.Directory{
				Orgs: []client.DirectoryOrg{
					{Name: "acme", Created: "2026-01-01T00:00:00Z"},
				},
			}, nil
		}
		t.Cleanup(func() { directoryFn = origDir })
		standIn(t, nil, nil, nil)
		installFakeDS(t, func(sql string, _ []any) []map[string]any {
			if sql != standingUsageSQL {
				return nil
			}
			return []map[string]any{{"organization": "acme", "total_tokens": tokens.Load()}}
		})

		clock := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
		origNow := nowFn
		nowFn = func() time.Time { return clock }
		t.Cleanup(func() { nowFn = origNow })

		app := mountApp(t)
		seedOrgOptin(t, "acme", "Acme", true)

		// First fetch computes and caches the badge.
		code, h1, b1 := doBadge(t, app, "/v1/leaderboard/badge/acme.svg", nil)
		if code != http.StatusOK || !strings.Contains(string(b1), "4000 pts") {
			t.Fatalf("first = %d %s", code, b1)
		}
		if dirCalls.Load() != 1 {
			t.Fatalf("dirCalls = %d, want 1", dirCalls.Load())
		}
		etag := h1.Get("ETag")

		// Even 30 minutes later (past standing's 1-minute lineTTL), the badge cache serves
		// the cached SVG without recomputing from IAM/warehouse.
		clock = clock.Add(30 * time.Minute)
		tokens.Store(900_000_000) // would be 9000 pts if recomputed
		code, h2, b2 := doBadge(t, app, "/v1/leaderboard/badge/acme.svg", nil)
		if code != http.StatusOK || string(b2) != string(b1) || h2.Get("ETag") != etag {
			t.Fatalf("cached hit drifted: %d etag=%s body=%s", code, h2.Get("ETag"), b2)
		}
		if dirCalls.Load() != 1 {
			t.Fatalf("dirCalls after 30m cache hit = %d, want 1 (must not recompute hourly cached badge)", dirCalls.Load())
		}

		// Conditional GET returns 304 Not Modified with empty body and zero recomputation.
		code, _, b304 := doBadge(t, app, "/v1/leaderboard/badge/acme.svg", map[string]string{"If-None-Match": etag})
		if code != http.StatusNotModified || len(b304) != 0 {
			t.Fatalf("If-None-Match = %d len=%d, want 304 empty", code, len(b304))
		}
		if dirCalls.Load() != 1 {
			t.Fatalf("dirCalls after 304 = %d, want 1", dirCalls.Load())
		}

		// Past 1 hour, if the backing source blips (and line memo has no copy), the badge cache
		// falls back to the last good SVG rather than breaking README embeds.
		clock = clock.Add(31 * time.Minute)
		lines.reset()
		dirFail.Store(true)
		code, _, bStale := doBadge(t, app, "/v1/leaderboard/badge/acme.svg", nil)
		if code != http.StatusOK || string(bStale) != string(b1) {
			t.Fatalf("stale fallback = %d %s", code, bStale)
		}

		// Once the source recovers past the hourly TTL, the badge recomputes with updated points.
		dirFail.Store(false)
		lines.reset()
		code, h3, b3 := doBadge(t, app, "/v1/leaderboard/badge/acme.svg", nil)
		if code != http.StatusOK || !strings.Contains(string(b3), "9000 pts") || h3.Get("ETag") == etag {
			t.Fatalf("post-TTL refresh = %d etag=%s body=%s", code, h3.Get("ETag"), b3)
		}

		// Opting out invalidates the cache immediately so private orgs stop serving at once.
		optOutHeaders := withHeader(principalHeaders("acme", "boss"), "X-User-IsOrgAdmin", "true")
		if code, body := doJSON(t, app, http.MethodPut, "/v1/leaderboard/optin/org", optOutHeaders, orgOptinReq{Listed: false}); code != http.StatusOK {
			t.Fatalf("opt-out = %d %s", code, body)
		}
		if code, _, _ := doBadge(t, app, "/v1/leaderboard/badge/acme.svg", nil); code != http.StatusNotFound {
			t.Fatalf("after opt-out = %d, want 404 immediately", code)
		}
	})
}
