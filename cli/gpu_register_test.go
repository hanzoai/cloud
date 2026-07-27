package cli

// gpu_register_test.go — registration resilience. Ensuring the fleet/jobs namespaces
// is best-effort: those namespaces already exist in any live org, so a transient API
// failure there must NOT abort registration. It used to be fatal, and with systemd
// Restart=always/RestartSec=5 a single 503 became an infinite crash-loop (observed at
// 141 restarts with the machine offline throughout). The presence write is the step
// that actually matters, so a real failure there must still surface.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubRegisterCloud fails every namespace-ensure with the given status, and answers
// the presence-activity write with presenceStatus.
func stubRegisterCloud(t *testing.T, nsStatus, presenceStatus int, sawPresence *bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/tasks/namespaces"):
			w.WriteHeader(nsStatus)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/activities"):
			*sawPresence = true
			w.WriteHeader(presenceStatus)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
}

// A flaky namespace-ensure must not abort registration: the worker still writes its
// presence record and stays online. This is the crash-loop regression guard.
func TestRegisterSurvivesNamespaceEnsureFailure(t *testing.T) {
	var sawPresence bool
	srv := stubRegisterCloud(t, http.StatusServiceUnavailable, http.StatusOK, &sawPresence)
	defer srv.Close()

	if err := testWorker(t, srv.URL).register(context.Background()); err != nil {
		t.Fatalf("register() = %v, want nil (namespace-ensure is best-effort)", err)
	}
	if !sawPresence {
		t.Fatal("register() never wrote the presence record")
	}
}

// The presence write is the load-bearing step — when it fails, register must fail so
// the operator sees a real error instead of a silently-offline machine.
func TestRegisterFailsWhenPresenceWriteFails(t *testing.T) {
	var sawPresence bool
	srv := stubRegisterCloud(t, http.StatusOK, http.StatusInternalServerError, &sawPresence)
	defer srv.Close()

	if err := testWorker(t, srv.URL).register(context.Background()); err == nil {
		t.Fatal("register() = nil, want an error when the presence write fails")
	}
}
