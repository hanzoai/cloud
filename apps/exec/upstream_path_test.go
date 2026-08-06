package exec

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/sandbox/wire"
)

// The defect this file exists for.
//
// apps/exec and apps/functions both read CODE_EXEC_UPSTREAM and both talk to the
// same executor, and for as long as the executor did not exist they were free to
// disagree about the path — which they did. apps/exec preserves the incoming
// path (/v1/exec); apps/functions built upstream+"/exec". Neither test suite
// could see it, because each package only ever measured itself.
//
// These tests pin the SAME constant from both sides. apps/functions has the
// mirror (invoke_upstream_path_test.go). Move wire.LibreChatExec and both
// follow; move one consumer and one goes red.

// TestProxyAsksUpstreamForLibreChatExec measures the real proxy against a
// recording upstream, rather than reasoning about httputil's Director.
func TestProxyAsksUpstreamForLibreChatExec(t *testing.T) {
	var got string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		_, _ = io.WriteString(w, `{"session_id":"s","stdout":"","stderr":"","files":[]}`)
	}))
	defer up.Close()

	p, err := newProxy(up.URL)
	if err != nil {
		t.Fatalf("newProxy: %v", err)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, wire.LibreChatExec,
		strings.NewReader(`{"lang":"py","code":"print(1)"}`)))

	if got != wire.LibreChatExec {
		t.Fatalf("upstream saw %q, want %q — the two CODE_EXEC_UPSTREAM consumers "+
			"must ask for the SAME path or no single env value serves both", got, wire.LibreChatExec)
	}
}

// TestUpstreamValueNeedsNoPathSuffix is the operator-facing half: the value that
// goes in universe (charts/app/values/hanzo/cloud.yaml) is a bare host, and a
// well-meant "/v1" suffix must be visibly wrong rather than subtly wrong.
func TestUpstreamValueNeedsNoPathSuffix(t *testing.T) {
	var got string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
	}))
	defer up.Close()

	p, err := newProxy(up.URL + "/v1")
	if err != nil {
		t.Fatalf("newProxy: %v", err)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, wire.LibreChatExec, nil))

	if got != "/v1"+wire.LibreChatExec {
		t.Fatalf("got %q, want %q", got, "/v1"+wire.LibreChatExec)
	}
	// Documented so the failure mode is written down where an operator setting
	// the env var will meet it: a path on CODE_EXEC_UPSTREAM is doubled, not
	// merged. The correct value is scheme://host:port and nothing more.
	if !strings.HasPrefix(got, "/v1/v1/") {
		t.Fatalf("expected the doubled prefix that makes a path suffix wrong, got %q", got)
	}
}
