package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
	upstream "hanzo.ai/ci"
)

// state is the mounted handler. The pollers behind it are started by build and
// stopped by Shutdown.
type state struct{ h http.Handler }

// stop ends the pollers. It is package-level because the host calls Shutdown
// holding the Plugin, not the state — the same shape platform and projects use
// for a mount whose teardown outlives its handler value.
var stop context.CancelFunc

// Shutdown stops the pollers. Registered as this app's teardown in
// plugin/ci/main.go, so the reads against git.hanzo.ai end when the process
// stops serving rather than at process exit.
func Shutdown() error {
	if stop != nil {
		stop()
		stop = nil
	}
	return nil
}

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
// on a machine that holds no secrets. A missing workload identity must read as "this
// surface cannot see the forge", not as "the host has no such surface".
func build(b cloud.Base) (state, error) {
	ctx, cancel := context.WithCancel(context.Background())
	h, err := upstream.ServeFromEnv(ctx, slog.Default())
	if err != nil {
		cancel() // nothing was started; release the context with the failure
		b.Log.Warn("ci is not configured; /v1/ci will fail closed", "err", err)
		return state{h: unconfigured(err)}, nil
	}
	stop = cancel
	return state{h: h}, nil
}

// unconfigured answers every path with the reason, so a caller learns what is
// missing rather than that the address does not exist.
//
// The reason is an error, which is to say arbitrary text: today's is a fixed
// string, but a wrapped %w or a quoted path is the ordinary next edit to the
// function that produces it, and pasting one between quotes writes a body that
// will not parse. Marshalled, the body is a body whatever the error says.
func unconfigured(cause error) http.Handler {
	body, err := json.Marshal(map[string]any{
		"status": http.StatusServiceUnavailable,
		"title":  "Service Unavailable",
		"detail": "ci is not configured: " + cause.Error(),
	})
	if err != nil { // a string map cannot fail to marshal; answer rather than panic
		body = []byte(`{"status":503,"title":"Service Unavailable"}`)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write(body)
	})
}

// routes declares the operations as TYPED ops.
//
// Not a wildcard subtree and not an untyped route with a description. Either
// publishes an address and a tag and nothing a client can dispatch — the
// composition test names that "the undispatchable remainder" — so `hanzo ci`
// would have no verbs and every generated SDK an empty class.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{h: s.State.h}
	g := app.Group("/v1/ci")
	g.Get("/runs", o.runs)
}

type ops struct{ h http.Handler }

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// estateNamespaces maps an estate IAM org to the forge namespaces it owns on
// git.hanzo.ai (mirrors forge/workspace.go owners). A non-estate org owns only
// its own namespace verbatim.
var estateNamespaces = map[string][]string{
	"hanzo": {"hanzo", "hanzoai", "hanzo-inc"},
	"lux":   {"lux", "luxfi"},
	"zoo":   {"zoo", "zooai"},
}

// coldWait bounds how long a first request after lazy plugin startup waits for
// the background poller's initial fetch to populate the run cache.
const coldWait = 8 * time.Second

// runs lists recent builds: the repo, the branch, the commit and how each run
// ended, newest first. A run names a repo, a branch and an actor, so the list is
// never wider than the caller — a SuperAdmin sees the fleet, an org member sees
// only its own org.
func (o ops) runs(ctx context.Context, _ *cloud.Unit) (*upstream.Executions, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, principal.RefusedFrom(ctx)
	}
	org, ok := viewer(c)
	if !ok {
		return nil, principal.RefusedFrom(ctx)
	}
	wantOrg := c.Fiber().Query("org")
	namespaces := []string{org}
	if org != authz.AdminOrg {
		if owned, ok := estateNamespaces[org]; ok {
			namespaces = owned
		}
		if wantOrg != "" {
			if !slices.Contains(namespaces, wantOrg) {
				return &upstream.Executions{Executions: []upstream.Execution{}, FetchedAt: time.Now().UTC()}, nil
			}
			namespaces = []string{wantOrg}
		}
	}

	deadline := time.Now().Add(coldWait)
	for {
		var merged *upstream.Executions
		seen := map[string]bool{}
		for _, ns := range namespaces {
			q := wantOrg
			if org != authz.AdminOrg {
				q = ""
			}
			part, err := callAs[upstream.Executions](ctx, o.h, "/v1/ci/runs", ns, q)
			if err != nil {
				return nil, err
			}
			if merged == nil {
				merged = &upstream.Executions{
					Executions: []upstream.Execution{},
					FetchedAt:  part.FetchedAt,
					Stale:      part.Stale,
				}
			}
			for _, ex := range part.Executions {
				key := fmt.Sprintf("%s/%s#%d", ex.Org, ex.Repo, ex.ID)
				if !seen[key] {
					seen[key] = true
					merged.Executions = append(merged.Executions, ex)
				}
			}
		}
		if merged != nil && (!merged.FetchedAt.IsZero() || time.Now().After(deadline) || ctx.Err() != nil) {
			sort.Slice(merged.Executions, func(i, j int) bool {
				if !merged.Executions[i].StartedAt.Equal(merged.Executions[j].StartedAt) {
					return merged.Executions[i].StartedAt.After(merged.Executions[j].StartedAt)
				}
				return merged.Executions[i].ID > merged.Executions[j].ID
			})
			return merged, nil
		}
		select {
		case <-ctx.Done():
			return merged, nil
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// callAs asks the mounted surface its own question as org and decodes the answer.
//
// THE SCOPE IS THIS BINARY'S, THE FILTER IS THE SURFACE'S. hanzo.ai/ci decides
// what a viewer may see from X-Org-Id and treats its absence as fatal, which is
// exactly right: absence means the request did not come through a gate. Here the
// gate is cloud's own — a validated principal, or the SuperAdmin predicate — and
// the header is written from it, never from anything the caller sent. So the
// visibility rules stay in one place and are not restated, and the identity they
// run on is the one the gateway attested.
func callAs[T any](ctx context.Context, h http.Handler, path, org, queryOrg string) (*T, error) {
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	req.Header.Set("X-Org-Id", org)
	if queryOrg != "" {
		req.URL.RawQuery = "org=" + url.QueryEscape(queryOrg)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		// The 503 above is this binary's own diagnostic and says WHICH provider of
		// configuration is missing. Reporting the status alone threw away the
		// only sentence that distinguishes "no workload identity" from "cannot reach
		// the forge", which is the whole reason that handler answers instead of
		// 404ing. A tenant-scoped answer is not at stake: nothing but this
		// handler produces a non-200 here.
		if detail := unconfiguredDetail(rec.Body.Bytes()); detail != "" {
			return nil, fmt.Errorf("ci: %s answered %d: %s", path, rec.Code, detail)
		}
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
//
// The reserved org is authz.AdminOrg, the constant IAM signs and every reader
// of that claim spells. Written here as a literal it was a second copy of a
// value hanzo.ai/ci also held, agreeing only while both said the same word.
//
// AN ISOLATION KEY, NOT A RESOURCE NAME — which is why this reads principal and
// does not fold. Two shapes are in use in this binary and they are not
// interchangeable. deploy, platform, provisioning and fare pass c.Org() through
// namespace.Sanitize because there the org BECOMES something whose grammar is
// fixed elsewhere: a k8s namespace, a DNS label. A fold is mandatory there.
// Here the org answers "whose runs are these", so its only job is to equal the
// key every other surface uses for the same caller. Folding it breaks exactly
// that: "Acme" becomes acme-37036cd8f9746d33, and a claim past MaxOrgLen, which
// the isolation rule refuses outright, folds into a well-formed key. Both fail
// silently — a folded key still looks like an org, so the surface answers 200
// with the runs of a tenant that does not exist.
func viewer(c *zip.Ctx) (string, bool) {
	if principal.Super(c) {
		return authz.AdminOrg, true
	}
	return principal.Org(c)
}

// unconfiguredDetail reads back the sentence unconfigured wrote, and nothing
// else: an answer this binary did not compose has no detail to report.
func unconfiguredDetail(body []byte) string {
	var out struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return ""
	}
	return out.Detail
}
