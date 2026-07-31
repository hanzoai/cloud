package tasks

// Why this surface publishes operations and describes none of them.
//
// The refusal itself is recorded at the mount (tasks.go). These are the
// MEASUREMENTS it rests on — one test per blocker, so a claim in that record is
// checkable, and so the refusal expires by itself on the day the wire moves
// instead of outliving its reason as prose nobody re-ran.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// TestBareNounIsARedirect pins what /v1/tasks answers — 307, Location
// /v1/tasks/ — which the engine's own ServeMux derives from its /v1/tasks/
// subtree pattern (hanzoai/tasks pkg/tasks/embed.go, HTTPHandler) before any
// handler runs. Every method behaves the same way, because a redirect to the
// subtree is a routing fact and not a method's answer.
//
// That is the blocker for all seven operations at this address: a typed op
// answers 200, 204, or a 2xx it DECLARED — zip refuses a non-2xx at declaration
// (zip@v1.18.11 typed.go:112) and writes c.JSON(out) or a bare status and
// nothing else (typed.go:305-311), and the only codes cloud's Bridge carries are
// 201 and 202 (cloud/typed.go, Created/Accepted). No typed op can emit a 307 or
// the Location header that makes it mean anything.
func TestBareNounIsARedirect(t *testing.T) {
	mux := httpMux(testEngine(t))
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(m, "/v1/tasks", nil))
		if rec.Code != http.StatusTemporaryRedirect {
			t.Errorf("%s /v1/tasks = %d, want 307", m, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/v1/tasks/" {
			t.Errorf("%s /v1/tasks Location = %q, want %q", m, loc, "/v1/tasks/")
		}
	}
}

// TestOneWildcardCarriesFourContentTypes pins the four answer shapes that ONE
// route — app.All("/v1/tasks/*", …) in Mount — carries at once: the engine's
// JSON API, the text/plain 404 its ServeMux writes for a path or method it does
// not serve, the text/plain 405 the MCP endpoint writes for a non-POST, and the
// event stream.
//
// A typed op is one method at one path with one In and one Out, and it answers
// application/json (zip typed.go:311) or a bare status. It cannot be four
// content types, and it cannot be a stream at all: the handler returns *Out and
// zip encodes it once, after the handler is done. So the wildcard stays a raw
// relay, and the operations behind it stay outside the document.
func TestOneWildcardCarriesFourContentTypes(t *testing.T) {
	mux := httpMux(testEngine(t))
	for _, c := range []struct {
		method, path string
		code         int
		contentType  string
	}{
		{http.MethodGet, "/v1/tasks/namespaces", 200, "application/json"},
		{http.MethodPut, "/v1/tasks/namespaces", 404, "text/plain"},
		{http.MethodGet, "/v1/tasks/mcp", 405, "text/plain"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, validated(httptest.NewRequest(c.method, c.path, nil)))
		if rec.Code != c.code {
			t.Errorf("%s %s = %d, want %d (%s)", c.method, c.path, rec.Code, c.code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, c.contentType) {
			t.Errorf("%s %s content-type = %q, want %s", c.method, c.path, ct, c.contentType)
		}
	}

	// The stream, bounded: the handler holds the connection open until the client
	// goes away, which is the property that makes it untypable.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, validated(httptest.NewRequest(http.MethodGet, "/v1/tasks/events", nil)).WithContext(ctx))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("GET /v1/tasks/events content-type = %q, want text/event-stream", ct)
	}
}

// TestEngineErrorEnvelopeIsNotZips pins the two error envelopes side by side,
// because their difference is the blocker for typing any single leaf out of the
// wildcard.
//
// The engine writes {"error":…,"code":403} with code a JSON NUMBER (hanzoai/tasks
// pkg/tasks/embed.go writeErr; cloud's gate writes the same shape for the
// refusal). zip writes its HTTPError — {"status":403,"error":…}, with
// `code` a STRING that is omitted when empty (zip ctx.go:201-218). A typed op
// converted out of this subtree would answer errors in the second shape while
// every sibling path behind the same wildcard kept the first, so one product
// surface would report failures two ways depending on which path a client hit —
// and the field a client reads for the code would be missing from one of them.
func TestEngineErrorEnvelopeIsNotZips(t *testing.T) {
	mux := httpMux(testEngine(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/tasks/namespaces", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unvalidated GET /v1/tasks/namespaces = %d, want 403", rec.Code)
	}
	var engine map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &engine); err != nil {
		t.Fatalf("engine error body is not JSON: %v", err)
	}
	if _, ok := engine["code"].(float64); !ok {
		t.Errorf("engine error body code = %#v, want a JSON number", engine["code"])
	}
	if _, ok := engine["status"]; ok {
		t.Errorf("engine error body now carries status: %v", engine)
	}

	b, err := json.Marshal(zip.ErrForbidden("identity required"))
	if err != nil {
		t.Fatalf("marshal zip error: %v", err)
	}
	var typed map[string]any
	if err := json.Unmarshal(b, &typed); err != nil {
		t.Fatalf("zip error body is not JSON: %v", err)
	}
	if _, ok := typed["status"].(float64); !ok {
		t.Errorf("zip error body status = %#v, want a JSON number", typed["status"])
	}
	if v, ok := typed["code"]; ok {
		t.Errorf("zip error body now carries code = %#v — re-check the refusal", v)
	}
}

// TestCancelIgnoresAMalformedBody pins the twelfth-of-its-kind handler: the
// workflow verbs (cancel/terminate/signal and their batch siblings) DISCARD a
// body decode error — `_ = decode(r, &req)` in hanzoai/tasks pkg/tasks/embed.go
// — because the body is optional detail (reason, identity) on a verb the URL
// already fully addresses. Its strict sibling, the workflow START on the same
// subtree, refuses the same bytes with 400.
//
// A typed op cannot be tolerant: op.invoke returns ErrBadRequest on any body it
// cannot decode, before the handler runs (zip typed.go, op.invoke). So even a
// decomposition of the wildcard could not type these twelve without turning a
// tolerated request into a 400.
func TestCancelIgnoresAMalformedBody(t *testing.T) {
	mux := httpMux(testEngine(t))

	post := func(path, body string) (int, string) {
		r := validated(httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body))))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec.Code, rec.Body.String()
	}

	if code, body := post("/v1/tasks/namespaces",
		`{"namespaceInfo":{"name":"smoke","state":"NAMESPACE_STATE_REGISTERED"},`+
			`"config":{"workflowExecutionRetentionTtl":"24h"}}`); code != http.StatusOK {
		t.Fatalf("register namespace = %d: %s", code, body)
	}

	// Malformed body, tolerated: the verb runs and fails on the workflow it was
	// asked about, never on the bytes.
	code, body := post("/v1/tasks/namespaces/smoke/workflows/nope/cancel", `{`)
	if code == http.StatusBadRequest {
		t.Errorf("cancel now refuses a malformed body (%d %s) — re-check the refusal", code, body)
	}
	if !strings.Contains(body, "not found") {
		t.Errorf("cancel with a malformed body = %d %s, want the workflow lookup to have run", code, body)
	}

	// The strict sibling, same bytes, same subtree.
	if code, body := post("/v1/tasks/namespaces/smoke/workflows", `{`); code != http.StatusBadRequest {
		t.Errorf("workflow start with a malformed body = %d %s, want 400", code, body)
	}
}

// validated presents r as a caller cloud's identity boundary has already
// resolved: X-User-Id is minted only from a verified credential, and gate admits
// it only alongside an org (apps/principal.OrgOf decides both).
func validated(r *http.Request) *http.Request {
	r.Header.Set("X-Org-Id", "acme")
	r.Header.Set("X-User-Id", "u-acme")
	return r
}
