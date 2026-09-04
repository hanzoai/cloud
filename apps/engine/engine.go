// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

// Package engine is Hanzo Engine: which models the serving runtime has loaded,
// and the GPUs under it.
//
// It is the serving runtime behind Hanzo's models — what it serves and what it
// runs on, read through /v1/engine.
//
// PRODUCT-REPO MODEL. The product lives in github.com/hanzoai/engine (Rust —
// the LLM inference engine: `hanzo-engine serve`, the OpenAI- and Anthropic-
// compatible server, quantization, multimodality). This subsystem does NOT
// reimplement any of it: every op is a TYPED PASSTHROUGH to the engine
// deployment over an HTTP client, the posture apps/flow takes for its Python
// product. cloud adds IAM auth and the unified surface (OpenAPI/MCP/CLI/SDK
// projection).
//
// THE HONEST SLICE. hanzoai/openapi once authored 22 paths for this product —
// GPU clusters, jobs, Ray, pipelines, fleet GPU inventory, serve endpoints —
// and deleted them as UNSERVED (nothing answered them anywhere). The engine is
// not a cluster manager and never served that surface; what it genuinely
// answers today is its own management plane, and that is what mounts here:
// the models the runtime serves with their load state, one model's state, the
// host's device inventory (the real GPUs under the runtime), and a
// reachability lens. Each op is proven against a live hanzo-server backend
// (live_test.go re-proves the loop on demand). Cluster/job/Ray/pipeline
// intent stays refused — those live on the cluster plane (/v1/compute/clusters,
// /v1/ml) where they are real — and the ledger is a measured gate
// (typed_wire_test.go intentRefused), not a comment.
//
// INFERENCE IS NOT HERE. The fleet's ONE inference endpoint is the OpenAI-
// compatible /v1 surface (apps/ai + the zen claim), where requests are
// metered and billed. This plane is the runtime's management lens; opening a
// second, unmetered completion endpoint under /v1/engine would split billing, so
// it deliberately does not exist.
//
// SHARED RUNTIME, READ-ONLY. The engine deployment is one shared runtime with
// no per-org primitive, so every read here is a platform fact and every
// MUTATION the product's server does expose (model load/unload/reload, tune,
// requantize, doctor) is refused: an org-scoped route onto a shared runtime
// would hand each tenant every other tenant's availability. Mutations arrive
// when engines are per-org instances, not before.
//
// FAIL-CLOSED. No validated principal → 403 before any upstream byte. An
// upstream that refuses the platform credential (its 401/403) is a deployment
// fault, not the caller's — reported 503. An unreachable upstream is 503.
package engine

import (
	"github.com/hanzoai/cloud/internal/environ"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// defaultUpstream is the in-cluster Service of the engine deployment, and the
// port is 36900 because that is the port the deployed Service actually exposes
// (svc/engine in namespace hanzo: name http, port 36900, target 36900).
//
// IT WAS 1234, WHICH IS A DIFFERENT ENGINE. 1234 is standalone `hanzo-engine serve`'s
// default; 36900 is the port the engine binds when it runs as the node's engine,
// and 36900 is what is deployed. The cloud Deployment sets no ENGINE_UPSTREAM, so
// the default WAS the production value, and it named a port the Service does not
// carry — every op on this plane dialled a refused connection, which this
// subsystem faithfully reported as reachable:false and 503. A plane that is
// honest about being unreachable is still unreachable.
//
// The same 1234-vs-36900 confusion is on record from the desktop build, where a
// frontend discovered models at one port while the engine that answers ran at the
// other. One value, resolved against what is deployed.
//
// Overridable via ENGINE_UPSTREAM — tests point it at an httptest server; a dev
// box points it at a local `hanzo-engine serve`, which is where 1234 is right.
const defaultUpstream = "http://engine.hanzo.svc.cluster.local:36900"

func upstream() string {
	if v := environ.Or("ENGINE_UPSTREAM", ""); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultUpstream
}

// key is the platform's service credential for the engine deployment,
// presented as a bearer token on every upstream call. KMS-synced into the pod
// env as ENGINE_API_KEY (the FLOW_API_KEY custody pattern). Empty is a valid
// dev posture: a bare `hanzo-engine serve` enforces no credential, and a locked
// deployment answers 401/403 which this subsystem reports as 503
// (misconfiguration, not caller auth).
func key() string { return environ.Or("ENGINE_API_KEY", "") }

const (
	// timeout bounds one management call. Every op here is metadata — model
	// tables and host inventory the server answers from memory.
	timeout = 30 * time.Second
	// maxBody bounds one upstream response read. The largest payload is the
	// device inventory of a many-GPU host; 4 MiB is far above anything
	// measured and still a bound.
	maxBody = 4 << 20
)

// httpClient is the ONE client for every engine call (connection-pooled).
var httpClient = &http.Client{Timeout: timeout + 5*time.Second}

// state is this subsystem's own data: none. The engine owns everything; this
// plane holds no store and runs no goroutine.
type state struct{}

// Mount wires /v1/engine/* onto app: a typed read lens over the engine
// deployment, resolved per request.
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "engine",
		func(cloud.Base) (state, error) { return state{}, nil },
		routes)
}

// ops binds the service to every op on this plane. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service —
// so it arrives as a RECEIVER and each op is a method value (o.models), the
// only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published
// document and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/engine generate` (a prerequisite of build).
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the served /v1/engine surface: four typed ops, nothing
// untyped. Ops are declared on the GROUP, so each op's path is the group's
// prefix composed with its leaf — the identity every projection (document,
// MCP tool, CLI command, SDK method) keys on.
//
// EVERY OP STATES ITS ID AND ITS SUMMARY, because neither has a usable default.
//
// An operation id is the generated SDK METHOD NAME and the CLI COMMAND. Left
// unstated, zip derives one from the path — `get_v1_engine_status` — and that
// is what an SDK user calls and what a model reads in the MCP tool list. The
// fleet's convention is the product prefix and the noun (the risk product's
// thirty-one operations are `riskScore`, `riskState`, `riskDatasets`), so these
// are `engineStatus`, `engineModels`, `engineModel`, `engineSystem`: the product
// this operation belongs to, then what it answers.
//
// A summary defaults to the first sentence of the Go doc comment, and a Go doc
// comment opens with the Go IDENTIFIER. So the published summaries read "Status
// reports whether…", "Models lists the models…" — a Go symbol name leaking into
// the CLI's help, the MCP tool list and every SDK's docstring. The summary is
// written for the person CALLING it, in the imperative, and the doc comment
// stays a Go doc comment: two audiences, two sentences, and zipdoc still lifts
// the comment as the description.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/engine")
	o := ops{s: s}

	// A typed op receives only a context, so the validated principal reaches it
	// parked there by cloud.Bridge — never as an In field. The composer owns
	// that install, once at its root; this group is a bare path prefix.
	zip.Get(g, "/status", o.status,
		zip.WithOperationID("engineStatus"),
		zip.WithSummary("Whether the serving runtime is reachable, and which build it runs"))
	zip.Get(g, "/models", o.models,
		zip.WithOperationID("engineModels"),
		zip.WithSummary("List the models the serving runtime holds, with each one's load state"))
	zip.Get(g, "/model", o.model,
		zip.WithOperationID("engineModel"),
		zip.WithSummary("Read one model's load state on the serving runtime"))
	zip.Get(g, "/system", o.system,
		zip.WithOperationID("engineSystem"),
		zip.WithSummary("The serving host's own inventory: devices, memory and build capabilities"))
}

// ── the shapes the ops take and give ────────────────────────────────────────
//
// A typed op's Go type name IS its schema name and the fleet's schema
// namespace is FLAT, so every name below carries the product prefix.

// engineNoInput is the In of an op that takes nothing off the wire. Its whole
// input is the caller's validated principal.
type engineNoInput struct{}

// engineStatus reports whether the engine deployment is reachable from this
// binary — the product's own /health probe plus the build revision it runs.
type engineStatus struct {
	// Reachable is true when the engine answered its health probe.
	Reachable bool `json:"reachable"`
	// Revision is the engine build's git revision, present only when
	// reachable (the server's own build identity — it publishes no semver).
	Revision string `json:"revision,omitempty"`
}

// engineResult is the engine's own response payload, relayed to the caller
// VERBATIM so the product's shape reaches the platform without field loss. It
// is opaque BY CONSTRUCTION: this plane proxies the product and deliberately
// does not remodel its shapes — a model list is the server's standard
// list envelope, the system report is its SystemInfo document. See
// github.com/hanzoai/engine for the shape behind each op.
type engineResult struct{ raw json.RawMessage }

// MarshalJSON emits the upstream payload as-is, which is what makes
// engineResult a relay rather than a model.
func (r engineResult) MarshalJSON() ([]byte, error) {
	if len(r.raw) == 0 {
		return []byte(`{}`), nil
	}
	return r.raw, nil
}

// engineModel addresses one model the engine knows. Engine model ids are
// Hugging Face repo paths ("Qwen/Qwen3-4B") or local paths — they carry
// slashes — so the id rides the query string, never a path segment.
type engineModel struct {
	// Model is the model id to inspect, exactly as the model list reports it.
	Model string `json:"model"`
}

// ── the ops ─────────────────────────────────────────────────────────────────

// Status reports whether the engine deployment is reachable and which build
// revision it runs — an honest lens for "is the serving runtime up", never a
// fabricated ok.
func (o ops) status(ctx context.Context, _ *engineNoInput) (*engineStatus, error) {
	if err := caller(ctx); err != nil {
		return nil, err
	}
	st, _, err := send(ctx, http.MethodGet, "/health", nil)
	if err != nil || st != http.StatusOK {
		return &engineStatus{Reachable: false}, nil
	}
	out := &engineStatus{Reachable: true}
	if st, body, err := send(ctx, http.MethodGet, "/v1/system/info", nil); err == nil && st == http.StatusOK {
		var v struct {
			Build struct {
				Revision string `json:"git_revision"`
			} `json:"build"`
		}
		if json.Unmarshal(body, &v) == nil {
			out.Revision = v.Build.Revision
		}
	}
	return out, nil
}

// Models lists the models the engine serves, each with its load state — the
// server's own model table (its standard list envelope, load status
// included), relayed verbatim.
func (o ops) models(ctx context.Context, _ *engineNoInput) (*engineResult, error) {
	if err := caller(ctx); err != nil {
		return nil, err
	}
	return relay(ctx, http.MethodGet, "/v1/models", nil)
}

// Model reads one model's load state — loaded, unloading, or not_found, as
// the engine itself reports it.
//
// Example: {"model": "Qwen/Qwen3-4B"}
func (o ops) model(ctx context.Context, in *engineModel) (*engineResult, error) {
	if err := caller(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Model) == "" {
		return nil, zip.ErrBadRequest("model is required")
	}
	// The product reads this through POST because its ids carry slashes; on
	// this plane the op is a READ and stays a GET — the id moves from our
	// query string into the upstream body here.
	return relay(ctx, http.MethodPost, "/v1/models/status", map[string]any{"model_id": in.Model})
}

// System reads the engine host's inventory: OS, CPU, memory, every accelerator
// device with its VRAM and compute capability, and the build's capabilities
// (CUDA/Metal/flash-attention) — the real hardware under the serving runtime,
// relayed verbatim.
func (o ops) system(ctx context.Context, _ *engineNoInput) (*engineResult, error) {
	if err := caller(ctx); err != nil {
		return nil, err
	}
	return relay(ctx, http.MethodGet, "/v1/system/info", nil)
}

// ── the gate ────────────────────────────────────────────────────────────────

// caller requires a validated principal — the ONE gate every op passes before
// any upstream byte. Every read here is a deployment-global platform fact, so
// no org scoping applies (there are no per-org rows to scope); the gate is
// AUTHENTICATION.
//
// It reads the bit cloud.Bridge PARKED, not the request. principal.OrgFrom is
// the wrong reader here — it answers with an org or refuses, so it would 403 a
// validated caller whose token names no home org (a machine token, or one minted
// before IAM's `orgs` claim), which on a plane with no tenant is exactly the
// operator this lens exists for. principal.ValidatedFrom is that one bit beside
// it, and it is the reason this gate does not take the pinned cloud.Request
// escape hatch to recompute principal.Validated(c) — the same answer by the
// longer way. FAILS CLOSED off the HTTP path: a CLI LocalInvoke parks nothing,
// so there is no validated principal and every op refuses.
func caller(ctx context.Context) error {
	if !principal.ValidatedFrom(ctx) {
		return zip.ErrForbidden("sign in to use Engine")
	}
	return nil
}

// ── client ──────────────────────────────────────────────────────────────────

// send is the ONE authed engine request: issue method+path with the platform
// credential (a bearer token riding ONLY the Authorization header — never a
// query, log, or error), bound the call by timeout, read at most maxBody
// bytes, and return the raw status + body. A transport failure is
// credential-free by construction.
func send(ctx context.Context, method, path string, body any) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, upstream()+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if k := key(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("engine request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return resp.StatusCode, data, nil
}

// relay is the ONE response path for a passthrough op: run the engine call
// and hand back its payload for verbatim relay, mapping failure statuses
// through engineErr.
func relay(ctx context.Context, method, path string, body any) (*engineResult, error) {
	st, data, err := send(ctx, method, path, body)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "engine unavailable")
	}
	if st < 200 || st > 299 {
		return nil, engineErr(st, data)
	}
	return &engineResult{raw: data}, nil
}

// engineErr maps an upstream failure to the caller's error. The product's own
// 401/403 mean the PLATFORM credential was refused — a deployment fault
// reported 503, never a caller-side auth bug (the caller's auth was already
// validated here). Its 5xx become 502 (the upstream broke, this plane did
// not). Everything else relays the product's status with its own detail.
func engineErr(status int, body []byte) error {
	msg := detail(body)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return zip.Errorf(http.StatusServiceUnavailable, "engine upstream refused the platform credential")
	case status >= 500:
		return zip.Errorf(http.StatusBadGateway, "engine upstream error: %s", msg)
	default:
		return zip.Errorf(status, "%s", msg)
	}
}

// detail extracts the engine's own failure message. axum answers some faults
// as JSON ({"error": …} or {"message": …}) and others as plain text (its
// extractor rejections), so try both shapes and fall back to the bounded raw
// body — the caller sees the product's own reason without this plane
// inventing one.
func detail(body []byte) string {
	var d struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &d); err == nil {
		if d.Error != "" {
			return d.Error
		}
		if d.Message != "" {
			return d.Message
		}
	}
	if len(body) == 0 {
		return "engine upstream error"
	}
	const limit = 512
	if len(body) > limit {
		body = body[:limit]
	}
	return strings.TrimSpace(string(body))
}
