// Package exec is the code interpreter: run a snippet in a sandbox and move
// files in and out.
//
// It owns FOUR top-level /v1 segments — /v1/exec,
// /v1/upload, /v1/download and /v1/files — because the upstream client's contract
// fixes them as siblings (below), so it publishes under four OpenAPI product tags
// rather than one.
//
// hanzo.chat (LibreChat fork) drives its execute_code agent tool against a
// code-interpreter API whose contract is fixed by the upstream client
// (@librechat/agents CodeExecutor): it POSTs {lang, code, files?} to
// `${LIBRECHAT_CODE_BASEURL}/exec` with header `X-API-Key`, and uses the sibling
// paths /exec/programmatic, /upload, /download/{id}, /files/{sid}. The response
// is {session_id, stdout, stderr, files:[{name}]}. cloud-api is the single edge
// that owns api.hanzo.ai/v1, so this subsystem mounts those paths and forwards
// each request UNCHANGED to a sandboxed executor upstream. No code runs here —
// this is a reverse proxy identical in shape to apps/o11y, so there is zero
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
	"github.com/hanzoai/cloud/apps/sandbox/wire"
	"github.com/hanzoai/cloud/openapi"
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
	wire.LibreChatExec, // covers /v1/exec and /v1/exec/programmatic
	"/v1/upload",       // multipart file upload into a session
	"/v1/download",     // /v1/download/{id}
	"/v1/files",        // /v1/files/{session_id}
}

func upstream() string {
	if v := strings.TrimSpace(os.Getenv("CODE_EXEC_UPSTREAM")); v != "" {
		return v
	}
	return defaultUpstream
}

// prose is what each owned prefix IS, for the exact path and for the subtree
// below it. Keyed by the same `prefixes` entries the mount reads, so the two
// cannot address different surfaces; the init below refuses a prefix with no
// entry, which is what keeps a route added to the mount from publishing an
// operationId and nothing else.
type prose struct {
	summary, description       string // the exact path, e.g. /v1/exec
	subSummary, subDescription string // the subtree, e.g. /v1/exec/*
}

// relay is the half that is true of all 56 operations, because all 56 are one
// reverse proxy: the wire is the executor's and the credential is a service key.
// Stated once and appended, rather than reworded eight times.
const relay = "\n\nNOTHING RUNS HERE. cloud forwards the request to the sandboxed executor byte " +
	"for byte and forwards its answer back the same way — the status, the Content-Type and " +
	"every field are the executor's, including fields this repo has never named and including " +
	"its own 4xx. There is no os/exec anywhere in this process: the sandbox is the isolation " +
	"boundary, and cloud adds only the credential check and the single public address.\n\n" +
	"AUTH is a shared SERVICE key on X-API-Key, compared in constant time — not a user JWT. The " +
	"chat server calls this server-side on a user's behalf, so this surface carries no org scope " +
	"and no per-user identity; separation between callers is the executor's session, not this " +
	"edge's. A wrong key is 401, and a deployment with no key configured is 503 rather than " +
	"open.\n\n" +
	"One registration owns this address for every method, so which methods actually answer is " +
	"the executor's decision, not this edge's."

// surfaces is the prose for the four prefixes, and the CLOSED source the init
// below reads. A prefix added to `prefixes` with no entry here panics at init
// rather than publishing a bare operationId.
var surfaces = map[string]prose{
	wire.LibreChatExec: {
		summary: "Run a code snippet in a sandboxed interpreter",
		description: "The code-interpreter entry point: a snippet with its language, plus any " +
			"files already uploaded to the session, runs in an isolated executor and comes back " +
			"as the session id, stdout, stderr and the files the run produced. This is what a " +
			"chat agent's code tool calls.",
		subSummary: "The interpreter's own execution subpaths",
		subDescription: "Whatever the executor serves below /exec, addressed verbatim — " +
			"/exec/programmatic is the one this repo names, and the rest of that tree is the " +
			"executor's to define. It is deliberately ONE greedy route: enumerating the " +
			"executor's subpaths here would 404 every one left out of the list, so cloud carries " +
			"the whole tree rather than a guess at it.",
	},
	"/v1/upload": {
		summary: "Upload a file into an execution session",
		description: "Takes a multipart upload and puts the file into the session the " +
			"interpreter runs against, so a later run can read it. The multipart envelope and " +
			"its content type reach the executor untouched — this address is not JSON and " +
			"nothing here parses it.",
		subSummary: "The upload surface's own subpaths",
		subDescription: "Whatever the executor serves below /upload, addressed verbatim. One " +
			"greedy route rather than an enumeration this repo has never made: a listed subtree " +
			"would 404 everything left out of it.",
	},
	"/v1/download": {
		summary: "The artifact download surface",
		description: "The root of the executor's download surface. The contract addresses an " +
			"artifact by id one segment down (/v1/download/{id}); this bare address is served " +
			"because one registration owns the whole prefix, and what it answers is the " +
			"executor's to decide.",
		subSummary: "Download a file a run produced",
		subDescription: "Fetches an artifact by id — a plot, a generated CSV, whatever a run " +
			"wrote. This is the ONE address whose success body is not JSON: the artifact's BYTES " +
			"come back under the executor's own Content-Type, so a client reads it as a stream " +
			"and must not try to decode it.",
	},
	"/v1/files": {
		summary: "The session file surface",
		description: "The root of the executor's session-file surface. The contract addresses a " +
			"session's files one segment down (/v1/files/{session_id}); this bare address is " +
			"served because one registration owns the whole prefix, and what it answers is the " +
			"executor's to decide.",
		subSummary: "List the files in an execution session",
		subDescription: "Lists what a session holds, addressed by session id — the uploads a run " +
			"can read and the artifacts it produced, each then fetched from /v1/download. The " +
			"listing's shape is the executor's own; this module names only that the address is " +
			"keyed by session id.",
	},
}

// The whole surface's prose, declared beside the wire facts that keep it untyped.
//
// None of these 56 operations can be a typed op (see the note in Mount and the
// closed ledger in typed_wire_test.go), so zipdoc has no doc comment to lift from
// any of them and the document would otherwise publish 56 operationIds and nothing
// else — 56 SDK methods that cannot explain themselves and 56 CLI commands with no
// help text. openapi.Describe is the seam for exactly that, and it keeps the
// drift-proof property: a description whose route is not in the router never
// renders, so this adds prose to operations that exist and cannot invent one.
//
// It loops over the SAME `prefixes` the mount reads and over exactly the methods
// the document publishes (openapi.Methods), so the prose covers the published
// surface exactly — no operation left bare, and none described that nobody serves.
func init() {
	for _, p := range prefixes {
		s, ok := surfaces[p]
		if !ok {
			panic("exec: no prose for prefix " + p + " — a published operation that states " +
				"nothing is an SDK method that cannot explain itself; add it to surfaces")
		}
		for _, m := range openapi.Methods() {
			openapi.Describe(p, m, s.summary, s.description+relay)
			openapi.Describe(p+"/*", m, s.subSummary, s.subDescription+relay)
		}
	}
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
		return nil, fmt.Errorf("exec: CODE_EXEC_UPSTREAM must be an absolute URL, got %q", rawURL)
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
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("exec.Mount: nil app")
	}
	logger := deps.Logger
	if logger == nil {
		return fmt.Errorf("exec.Mount: nil deps.Logger")
	}
	logger = logger.New("subsystem", "exec")

	proxy, err := newProxy(upstream())
	if err != nil {
		return err
	}
	h := zip.AdaptNetHTTP(guard(proxy))

	// Own each prefix for every method (POST /exec, POST /upload, GET
	// /download/{id}, GET /files/{sid}). Registered before ai (manifest/apps.go
	// row 142 vs ai's 182), so these specific paths win over ai's bare /v1/* glob.
	//
	// UNTYPED BY DESIGN — all 56 operations these 8 registrations publish. A typed
	// op (zip.Get/Post/...) is the only thing that carries schema, an MCP tool, a
	// CLI command and an SDK method, and NONE of these can be one: the request
	// shape, the response shape, the Content-Type and the status code all live in
	// the executor, and zip's typed path answers its own declared status with a
	// marshalled Go value. Typing any of them would move the wire, which a
	// description task may not do. PROSE is not part of that cost — openapi.Describe
	// declares it beside these wire facts (surfaces + init above), so all 56 explain
	// themselves in the document, the SDKs and the CLI without one byte of the wire
	// moving. The refusal is a GATE, not a promise:
	// typed_wire_test.go holds the closed ledger (untypedPaths x servedMethods)
	// plus the eight measurements that prove each wire fact, so a route added here
	// is typed by default and a stale reason goes red.
	for _, p := range prefixes {
		app.All(p, h)      // exact match, e.g. /v1/exec, /v1/upload
		app.All(p+"/*", h) // subpaths, e.g. /v1/exec/programmatic, /v1/files/{sid}
	}

	logger.Info("code interpreter surface mounted (reverse proxy)",
		"upstream", upstream(), "prefixes", strings.Join(prefixes, ","))
	return nil
}
