// Package ci mounts the CI fleet dashboard as a capability of this binary.
//
// It answers one question per service — is what we wrote what is running? —
// along one causal line rather than four opinions:
//
//	head ──build──▶ built ──pin──▶ declared ──reconcile──▶ running
//
// That line spans all three surfaces of the delivery plane, which is why it
// belongs beside the CD half rather than behind a door of its own. /v1/deploy
// already sits behind the gateway's IAM identity; this surface answered a valid
// hanzo.id bearer with a bare 401, because it sat behind a gate that reads an
// X-Org-Id header instead. Mounting it here is what makes one identity read the
// whole plane — and, since the CLI's command groups are generated from the
// document this binary emits, what gives `hanzo ci` its verbs.
//
// The surface itself stays in hanzo.ai/ci: it is deployed standalone too, and a
// fork of it here would be the second source HIP-0106 §4.2 names.
package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
	upstream "hanzo.ai/ci"
)

// state is the mounted handler. The pollers behind it are started by build and
// stopped with the context it was given.
type state struct{ h http.Handler }

// Use mounts the capability. The name is `ci` in every projection — the address
// /v1/ci, the tag, the plugin binary, the CLI group — as HIP-0139 §1 requires.
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "ci", build, routes)
}

// build starts the dashboard's pollers and takes its handler.
//
// FAIL-CLOSED, NOT FAIL-TO-MOUNT — the rule apps/deploy states for its k8s
// clients: when the configuration does not resolve the subsystem still mounts
// and every endpoint 503s honestly. Returning the error here instead would take
// the mount down, and with it `describe`, which has to emit this app's document
// on a machine that holds no secrets. A missing CI_GIT_TOKEN must read as "this
// surface cannot see the forge", not as "the host has no such surface".
func build(b cloud.Base) (state, error) {
	h, err := upstream.ServeFromEnv(context.Background(), slog.Default())
	if err != nil {
		b.Log.Warn("ci is not configured; /v1/ci will fail closed", "err", err)
		return state{h: unconfigured(err)}, nil
	}
	return state{h: h}, nil
}

// unconfigured answers every path with the reason, so a caller learns what is
// missing rather than that the address does not exist.
func unconfigured(cause error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":503,"title":"Service Unavailable",` +
			`"detail":"ci is not configured: ` + cause.Error() + `"}`))
	})
}

// routes declares the two operations as TYPED ops.
//
// Not a wildcard subtree and not an untyped route with a description. Either
// publishes an address and a tag and nothing a client can dispatch — the
// composition test names that "the undispatchable remainder" — so `hanzo ci`
// would have no verbs and every generated SDK an empty class.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{h: s.State.h}
	// On the *zip.App, not the scoped Router: zipdoc has to resolve the prefix an
	// op registers under, or its doc comment is filed against the wrong path and
	// dropped from the document and the MCP tool without saying so.
	r := cloud.ZipApp(app)
	zip.Get(r, "/v1/ci/runs", o.runs)
	zip.Get(r, "/v1/ci/fleet", o.fleet)
}

type ops struct{ h http.Handler }

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

type void = struct{}

// runs lists recent builds: the repo, the branch, the commit and how each run
// ended, newest first. A run names a repo, a branch and an actor, so the list is
// never wider than the caller — a SuperAdmin sees the fleet, an org member sees
// only its own org.
func (o ops) runs(ctx context.Context, _ *void) (*upstream.Executions, error) {
	return call[upstream.Executions](ctx, o.h, "/v1/ci/runs")
}

// fleet compares what was written with what is running, one row per service
// along a single causal line: head, the commit on the branch; built, the image
// that commit produced; declared, the tag pinned in the universe repository;
// running, what the cluster serves. A service whose four values disagree names
// the step that broke.
func (o ops) fleet(ctx context.Context, _ *void) (*upstream.Pipelines, error) {
	return call[upstream.Pipelines](ctx, o.h, "/v1/ci/fleet")
}

// call asks the mounted surface its own question and decodes the answer.
//
// THE SCOPE IS THIS BINARY'S, THE FILTER IS THE SURFACE'S. hanzo.ai/ci decides
// what a viewer may see from X-Org-Id and treats its absence as fatal, which is
// exactly right: absence means the request did not come through a gate. Here the
// gate is cloud's own — a validated principal, or the SuperAdmin predicate — and
// the header is written from it, never from anything the caller sent. So the
// visibility rules stay in one place and are not restated, and the identity they
// run on is the one the gateway attested.
func call[T any](ctx context.Context, h http.Handler, path string) (*T, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, principal.RefusedFrom(ctx)
	}
	org, ok := viewer(c)
	if !ok {
		return nil, principal.RefusedFrom(ctx)
	}

	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	req.Header.Set("X-Org-Id", org)
	if q := c.Fiber().Query("org"); q != "" {
		req.URL.RawQuery = "org=" + url.QueryEscape(q)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		return nil, fmt.Errorf("ci: %s answered %d", path, rec.Code)
	}
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("ci: decode %s: %w", path, err)
	}
	return &out, nil
}

// viewer is the org this request is answered as: the reserved admin org for a
// SuperAdmin, which is what the surface reads as "sees everything", otherwise
// the validated principal's own org.
func viewer(c *zip.Ctx) (string, bool) {
	if c.IsAdmin() {
		return "admin", true
	}
	if !principal.Validated(c) {
		return "", false
	}
	if org := namespace.Sanitize(c.Org()); org != "" {
		return org, true
	}
	return "", false
}
