package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestDaemon(t *testing.T) *JITDaemon {
	t.Helper()
	return &JITDaemon{
		cfg: &Config{
			HostName: "test",
			Orgs:     []string{"hanzoai"},
			Labels:   []string{"self-hosted", "test"},
		},
		state: newDaemonState(),
	}
}

func TestV1StatusReturnsJSONWithRequiredFields(t *testing.T) {
	d := newTestDaemon(t)
	mux := http.NewServeMux()
	d.registerV1(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	var out v1Status
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Host != "test" {
		t.Fatalf("host: %s", out.Host)
	}
	if out.Paused {
		t.Fatal("default state is unpaused")
	}
	if out.GOOS == "" || out.GOARCH == "" {
		t.Fatalf("goos/goarch must be populated, got %q/%q", out.GOOS, out.GOARCH)
	}
}

func TestV1PauseResumeFlipsState(t *testing.T) {
	d := newTestDaemon(t)
	mux := http.NewServeMux()
	d.registerV1(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/pause", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("pause status: %d", rec.Code)
	}
	if !d.state.IsPaused() {
		t.Fatal("state not paused after /v1/pause")
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/resume", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("resume status: %d", rec.Code)
	}
	if d.state.IsPaused() {
		t.Fatal("state still paused after /v1/resume")
	}
}

func TestV1MethodGuard(t *testing.T) {
	d := newTestDaemon(t)
	mux := http.NewServeMux()
	d.registerV1(mux)

	for _, tt := range []struct {
		path   string
		method string
	}{
		{"/v1/status", http.MethodPost},
		{"/v1/jobs", http.MethodPost},
		{"/v1/pause", http.MethodGet},
		{"/v1/resume", http.MethodGet},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tt.method, "http://127.0.0.1"+tt.path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: code=%d want 405", tt.method, tt.path, rec.Code)
		}
	}
}

func TestV1JobsListsActiveAndRecent(t *testing.T) {
	d := newTestDaemon(t)
	d.state.StartJob(JobRecord{JobID: 1, Org: "a"})
	d.state.StartJob(JobRecord{JobID: 2, Org: "b"})
	d.state.EndJob(2, false)

	mux := http.NewServeMux()
	d.registerV1(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/jobs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var jobs []JobRecord
	if err := json.NewDecoder(rec.Body).Decode(&jobs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs (1 active + 1 recent), got %d: %+v", len(jobs), jobs)
	}
}
