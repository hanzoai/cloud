package engine

import (
	"encoding/json"
	"github.com/zap-proto/zip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReplicaControlRequiresOperatorAndUsesNativeRouter(t *testing.T) {
	app, _ := harness(t)
	var calls atomic.Int32
	var seen engineReplicaUpdate
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/replicas" || r.Header.Get("Authorization") != "Bearer router-test-key" {
			t.Error("wrong router path or service credential")
		}
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
				t.Error(err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"zen-coder":[{"id":"spark","url":"http://spark.local:30000","healthy":true,"inflight":2,"capacity":4,"weight":300,"roles":["main"],"upstream_model":"local-model","ttft_ewma_ms":125.5}]}`)
	}))
	defer srv.Close()
	t.Setenv("ENGINE_ROUTER_UPSTREAM", srv.URL)
	t.Setenv("ENGINE_ROUTER_API_KEY", "router-test-key")
	body := `{"model":"zen-coder","id":"spark","capacity":4,"weight":300,"roles":["main"]}`
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		for _, user := range []string{"", "org-admin"} {
			// Nobody identified is 401; a known caller who is not the operator, 403.
			want := 403
			if user == "" {
				want = 401
			}
			status, _ := do(t, app, method, "/v1/engine/replicas", user, "acme", body)
			if status != want {
				t.Fatalf("non-operator %s as %q = %d, want %d", method, user, status, want)
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized request reached worker management")
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		req := httptest.NewRequest(method, "/v1/engine/replicas", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-Id", "operator")
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-IsAdmin", "true")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(raw), "spark.local") {
			t.Fatalf("operator %s = %d %s", method, resp.StatusCode, raw)
		}
	}
	if seen.ID != "spark" || seen.Capacity != 4 || seen.Weight != 300 || len(seen.Roles) != 1 {
		t.Fatalf("wrong upstream mutation: %+v", seen)
	}
}

func TestReplicaControlRefusesMissingAndLegacyRouter(t *testing.T) {
	app, _ := harness(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"default":[{"id":"old","url":"http://spark.local:1234","healthy":false,"inflight":0}]}`)
	}))
	defer srv.Close()
	for _, tc := range []struct {
		url    string
		status int
	}{{"", 503}, {srv.URL, 502}} {
		t.Setenv("ENGINE_ROUTER_UPSTREAM", tc.url)
		req := httptest.NewRequest(http.MethodGet, "/v1/engine/replicas", nil)
		req.Header.Set("X-User-Id", "operator")
		req.Header.Set("X-User-IsAdmin", "true")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatalf("status=%d want=%d", resp.StatusCode, tc.status)
		}
	}
}
