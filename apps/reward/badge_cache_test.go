package reward

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

func doRawGet(t *testing.T, app *zip.App, path string, headers map[string]string) (int, http.Header, []byte) {
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

func TestRewardBadgeIsCachedHourlyAndInvalidatesOnProfileEdit(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		clock := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
		origNow := rewardNowFn
		rewardNowFn = func() time.Time { return clock }
		t.Cleanup(func() { rewardNowFn = origNow })

		r := newRig(t)

		// Turn on a public profile for u1 under handle "ada".
		if code, body := r.do(http.MethodPut, "/v1/reward/me/profile", memberHeaders("u1", "acme"),
			map[string]any{"handle": "ada", "public": true}); code != http.StatusOK {
			t.Fatalf("save profile = %d %s", code, body)
		}

		// First fetch returns 200 SVG with hourly Cache-Control and ETag.
		code, h1, b1 := doRawGet(t, r.app, "/v1/reward/badge/ada.svg?style=flat&theme=dark", nil)
		if code != http.StatusOK {
			t.Fatalf("badge = %d %s", code, b1)
		}
		if ct := h1.Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
			t.Errorf("Content-Type = %q", ct)
		}
		if cc := h1.Get("Cache-Control"); cc != rewardBadgeCacheControl {
			t.Errorf("Cache-Control = %q, want %q", cc, rewardBadgeCacheControl)
		}
		etag := h1.Get("ETag")
		if etag == "" || !strings.HasPrefix(string(b1), "<svg") {
			t.Fatalf("unexpected badge etag=%q body=%s", etag, b1)
		}

		// Reordered query parameters hit the same cached entry; If-None-Match gets 304.
		code, h304, b304 := doRawGet(t, r.app, "/v1/reward/badge/ada.svg?theme=dark&style=flat", map[string]string{"If-None-Match": etag})
		if code != http.StatusNotModified || len(b304) != 0 {
			t.Fatalf("304 check = %d len=%d", code, len(b304))
		}
		if cc := h304.Get("Cache-Control"); cc != rewardBadgeCacheControl {
			t.Errorf("304 Cache-Control = %q, want %q", cc, rewardBadgeCacheControl)
		}

		// Awarding new points directly in the engine stays cached within the 1-hour window.
		if _, err := r.s.emit(t.Context(), catalogEvent("tok-1", "usage.tokens", "u1", map[string]any{"tokens": 100000, "model": ""})); err != nil {
			t.Fatalf("emit: %v", err)
		}
		for _, c := range r.queue("u1") {
			id, _ := c["id"].(string)
			if code, body := r.do(http.MethodPost, "/v1/admin/reward/claims/"+id+"/approve", platformHeaders, nil); code != http.StatusOK {
				t.Fatalf("approve %s: %d %s", id, code, body)
			}
		}

		clock = clock.Add(30 * time.Minute)
		code, _, bMid := doRawGet(t, r.app, "/v1/reward/badge/ada.svg?theme=dark&style=flat", nil)
		if code != http.StatusOK || string(bMid) != string(b1) {
			t.Fatalf("within 1h TTL want cached bytes; got %d %s", code, bMid)
		}

		// After 1 hour, cache expires and serves updated standing.
		clock = clock.Add(31 * time.Minute)
		code, hAfter, bAfter := doRawGet(t, r.app, "/v1/reward/badge/ada.svg?theme=dark&style=flat", nil)
		if code != http.StatusOK || string(bAfter) == string(b1) || hAfter.Get("ETag") == etag {
			t.Fatalf("after 1h TTL want refreshed SVG; got %d etag=%s body=%s", code, hAfter.Get("ETag"), bAfter)
		}

		// Turning the profile private clears the cache immediately -> 404.
		if code, body := r.do(http.MethodPut, "/v1/reward/me/profile", memberHeaders("u1", "acme"),
			map[string]any{"handle": "ada", "public": false}); code != http.StatusOK {
			t.Fatalf("hide profile = %d %s", code, body)
		}
		if code, _, _ := doRawGet(t, r.app, "/v1/reward/badge/ada.svg?theme=dark&style=flat", nil); code != http.StatusNotFound {
			t.Fatalf("after hiding profile = %d, want 404 immediately", code)
		}

		// Invalid options and unknown handles are never cached (400 / 404).
		if code, _, _ := doRawGet(t, r.app, "/v1/reward/badge/unknown.svg?theme=neon", nil); code != http.StatusBadRequest {
			t.Fatalf("bad theme = %d, want 400", code)
		}
		if code, _, _ := doRawGet(t, r.app, "/v1/reward/badge/unknown.svg", nil); code != http.StatusNotFound {
			t.Fatalf("unknown = %d, want 404", code)
		}
	})
}
