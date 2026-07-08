// Package execsvc exposes the Code Interpreter ("Run Code") surface on the
// unified cloud-api /v1 plane, per HIP-0106.
//
// hanzo.chat (LibreChat fork) drives its execute_code agent tool against a
// code-interpreter API whose contract is fixed by the upstream client
// (@librechat/agents CodeExecutor): it POSTs {lang, code, files?} to
// `${LIBRECHAT_CODE_BASEURL}/exec` with header `X-API-Key`, and uses the sibling
// paths /exec/programmatic, /upload, /download/{id}, /files/{sid}. The response
// is {session_id, stdout, stderr, files:[{name}]}. cloud-api is the single edge
// that owns api.hanzo.ai/v1, so this subsystem mounts those paths and forwards
// each request UNCHANGED to a sandboxed executor upstream. No code runs here —
// this is a reverse proxy identical in shape to clients/o11y, so there is zero
// request/response drift from the contract.
//
// SANDBOX: the upstream MUST be an isolated executor (Hanzo Runtime / a
// per-call container sandbox). This binary NEVER shells out; there is no
// os/exec anywhere in this package. Point CODE_EXEC_UPSTREAM at the sandbox
// service's in-cluster DNS. The executor is the isolation boundary; cloud only
// adds auth + the unified surface.
//
// AUTH: the gateway (order 80) bypasses these paths (the credential is an opaque
// service key on X-API-Key, not a JWT), so this subsystem enforces the key
// itself with a constant-time compare against CODE_EXEC_API_KEY (KMS-sourced,
// synced into the pod env). Endpoints are never open: an unset key fails closed.
package exec

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// defaultUpstream is the in-cluster address of the sandboxed code executor.
// Overridable via CODE_EXEC_UPSTREAM. It must speak the LibreChat
// code-interpreter contract (/exec, /files/{sid}, /upload, /download/{id}).
const defaultUpstream = "http://code-exec.hanzo.svc.cluster.local:8000"

// prefixes are the code-interpreter path surfaces this subsystem owns on /v1.
// Each is forwarded verbatim to the executor (no path rewrite: the executor
// serves the same /exec, /upload, … paths the LibreChat client expects).
var prefixes = []string{
	"/v1/exec",     // covers /v1/exec and /v1/exec/programmatic
	"/v1/upload",   // multipart file upload into a session
	"/v1/download", // /v1/download/{id}
	"/v1/files",    // /v1/files/{session_id}
}

func upstream() string {
	if v := strings.TrimSpace(os.Getenv("CODE_EXEC_UPSTREAM")); v != "" {
		return v
	}
	return defaultUpstream
}

// apiKey is the shared service key the chat server presents on X-API-Key. It is
// KMS-sourced and synced into the pod env as CODE_EXEC_API_KEY (mirrors the
// per-key secretKeyRef pattern of every other cloud subsystem).
func apiKey() string { return strings.TrimSpace(os.Getenv("CODE_EXEC_API_KEY")) }

// newProxy builds the reverse proxy to the executor as a plain http.Handler
// (wrapped for zip via AdaptNetHTTP at mount). Pure (URL in, handler out) so it
// is unit-testable without a live upstream. The path is preserved verbatim;
// only scheme/host are rewritten to the upstream, and the upstream vhost is set
// so it is not addressed as api.hanzo.ai.
func newProxy(rawURL string) (http.Handler, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("execsvc: CODE_EXEC_UPSTREAM must be an absolute URL, got %q", rawURL)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(r *http.Request) {
		base(r)              // sets scheme/host to target; joins paths
		r.Host = target.Host // upstream vhost, not api.hanzo.ai
	}
	// Code execution can be slow (installs, compute) but must not hang a worker
	// forever; bound the wait on the executor's response headers.
	proxy.Transport = &http.Transport{
		ResponseHeaderTimeout: 120 * time.Second,
	}
	return proxy, nil
}

// guard wraps an http.Handler with the constant-time X-API-Key check. Unset key
// ⇒ 503 (fail closed, not open); wrong key ⇒ 401. Errors are emitted in the
// same {status,error} JSON shape zip uses so the surface is uniform.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := apiKey()
		if want == "" {
			writeErr(w, http.StatusServiceUnavailable, "code execution not configured")
			return
		}
		got := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			writeErr(w, http.StatusUnauthorized, "invalid api key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Minimal hand-rolled JSON to avoid a dependency; msg is a fixed literal.
	_, _ = fmt.Fprintf(w, `{"status":%d,"error":%q}`, status, msg)
}

// Mount registers the code-interpreter surface on app. The gateway terminates
// user auth for the chat UI, but code exec is called server-side by the chat
// node process with the shared service key, so we enforce that key here.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("execsvc.Mount: nil zip.App")
	}
	logger := deps.Logger
	if logger == nil {
		return fmt.Errorf("execsvc.Mount: nil deps.Logger")
	}
	logger = logger.New("subsystem", "exec")

	proxy, err := newProxy(upstream())
	if err != nil {
		return err
	}
	h := zip.AdaptNetHTTP(guard(proxy))

	// Own each prefix for every method (POST /exec, POST /upload, GET
	// /download/{id}, GET /files/{sid}). Registered before ai (order 150), so
	// these specific paths win over ai's bare /v1/* glob.
	for _, p := range prefixes {
		app.All(p, h)      // exact match, e.g. /v1/exec, /v1/upload
		app.All(p+"/*", h) // subpaths, e.g. /v1/exec/programmatic, /v1/files/{sid}
	}

	logger.Info("code interpreter surface mounted (reverse proxy)",
		"upstream", upstream(), "prefixes", strings.Join(prefixes, ","))
	return nil
}

func init() {
	// Order 140: before hanzoai/ai (150) so the specific /v1/exec, /v1/upload,
	// /v1/download, /v1/files paths take precedence over ai's /v1/* catch-all.
	cloud.Register("exec", 140, cloud.Typed(Mount))
}
