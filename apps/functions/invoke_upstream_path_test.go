package functions

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/apps/exec"
)

// The mirror of apps/exec/upstream_path_test.go, from the other consumer.
//
// Both packages read CODE_EXEC_UPSTREAM and both must ask the executor for the
// SAME path, or no single value of that env var serves both. The two tests
// reference one constant so the agreement is checkable rather than claimed in
// two comments that cannot see each other.

func TestExecClientAsksUpstreamForLibreChatExec(t *testing.T) {
	var got string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		_, _ = io.WriteString(w, `{"stdout":"ok","stderr":""}`)
	}))
	defer up.Close()

	e := &execClient{upstream: up.URL, http: &http.Client{}}
	res, err := e.run(context.Background(), Function{Runtime: "python", Code: "print(1)"}, "", 5)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Ok {
		t.Fatalf("run not ok: %+v", res)
	}
	if got != exec.Path {
		t.Fatalf("upstream saw %q, want %q — this is the defect: apps/exec is a "+
			"path-preserving proxy that asks for %q, so this client asking for "+
			"anything else means no CODE_EXEC_UPSTREAM value serves both",
			got, exec.Path, exec.Path)
	}
}

// A trailing slash on the operator's value must not produce a doubled slash —
// the client trims it, and this pins that it keeps doing so.
func TestExecClientTrimsTrailingSlash(t *testing.T) {
	var got string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		_, _ = io.WriteString(w, `{"stdout":"","stderr":""}`)
	}))
	defer up.Close()

	t.Setenv("CODE_EXEC_UPSTREAM", up.URL+"/")
	e := newExecClient()
	if _, err := e.run(context.Background(), Function{Runtime: "python", Code: "x"}, "", 5); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != exec.Path {
		t.Fatalf("upstream saw %q, want %q", got, exec.Path)
	}
}
