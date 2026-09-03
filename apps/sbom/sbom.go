// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package sbom is what is inside a container image: every component, resolvable by
// digest or image ref.
//
// CI posts a CycloneDX software bill of materials keyed by image digest, and
// /v1/sbom resolves that component set back.
//
// GLOBAL BY DESIGN. Unlike the analytics lens (which is strictly per-org), an SBOM
// belongs to an image DIGEST, not a tenant — the digest is content-addressed, so
// any tenant deploying that image resolves the SAME component set. The store is
// therefore cross-tenant on purpose: ingest is gated to super-admin/CI (the build
// fleet), and resolve exposes only the immutable bill-of-materials of an image, no
// tenant data. This is why there is no org predicate here.
//
// ONE datastore client. Like clients/analytics, this package rides the SAME
// datastore-go client the ai subsystem opens in the shared Bootstrap
// (ai/object.DatastoreExec/DatastoreQuery). It never opens a second connection.
//
// Surface (/v1 only):
//
//	POST /v1/sbom          ingest a CycloneDX SBOM (super-admin / CI only)
//	GET  /v1/sbom/{ref}    resolve by image digest OR image ref (for the console)
//	GET  /v1/sbom/health   liveness + datastore connectivity (not JWT-gated)
//
// Registered as id "sbom" with cloud.HealthOwner + order 137: it serves its own
// /v1/sbom/health, so serve.go skips the generic liveness route. Order 137 binds
// /v1/sbom/* before the ai subsystem's /v1/* catch-all (150).
package sbom

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/datastore"
	"github.com/hanzoai/cloud/apps/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/sbom openapi` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ddlTimeout bounds the best-effort table bootstrap on Mount.
const ddlTimeout = 10 * time.Second

// createDatabase makes the target database idempotently. Analytics already reads
// hanzo.cloud_usage so it usually exists, but this keeps ensureTable robust on a
// fresh warehouse and is cheap when the DB is already present.
const createDatabase = `CREATE DATABASE IF NOT EXISTS hanzo`

// createTable is the GLOBAL, cross-tenant SBOM store. ReplacingMergeTree(ingested_at)
// dedupes a re-ingest by the component identity in ORDER BY, keeping the latest.
const createTable = `CREATE TABLE IF NOT EXISTS hanzo.sbom_component (
  image_digest      String,
  image_ref         String,
  source_repo       String,
  git_sha           String,
  component_name    String,
  component_version String,
  component_type    String,
  purl              String,
  license           String,
  ingested_at       DateTime DEFAULT now()
) ENGINE = ReplacingMergeTree(ingested_at)
ORDER BY (image_digest, component_name, component_version, purl)`

// state is sbom's own data: none — the global store rides the SAME shared
// datastore client the ai subsystem opens. Shared deps live in cloud.Base.
type state struct{}

// Mount wires the SBOM surface onto app and bootstraps the global table.
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "sbom", build, routes)
}

// build best-effort bootstraps the global table: create it now IF the datastore is
// already connected at boot. It usually is NOT — ai/object.InitDatastore connects
// ASYNCHRONOUSLY and flips DatastoreEnabled AFTER Mount returns, so a Mount-time
// attempt is skipped/failed here far more often than not. That is fine and
// NON-FATAL: ensureTable on the request path is the real guarantee, (re)creating the
// table on the first request that finds the store ready. Failing the mount here
// would wrongly abort the whole subsystem.
func build(b cloud.Base) (state, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ddlTimeout)
	defer cancel()
	if err := ensureTable(ctx); err != nil {
		b.Log.Warn("datastore not ready at mount; sbom table will be created lazily on first request", "err", err)
	} else {
		b.Log.Info("sbom table ready", "table", sbomTable)
	}
	b.Log.Info("sbom surface", "table", sbomTable, "brand", b.Brand)
	return state{}, nil
}

// ops binds the mounted Service so each op can be a method value — the only bound
// form cmd/zipdoc can lift prose from. It carries STATE and no logic.
type ops struct{ s *cloud.Service[state] }

// routes registers the SBOM surface. Health is a static route registered BEFORE the
// greedy resolve wildcard so it is never captured by it. Health is not JWT-gated
// (liveness must be probe-able).
//
// All three are TYPED ops — one registry entry from which the REST route, the
// OpenAPI operation, the MCP tool, the CLI command and every generated SDK method
// follow.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// The composer owns cloud.Bridge: the fused host installs it once at its root
	// and the plugin constructor does the same for a plugin program, so no
	// subsystem installs it.

	o := ops{s: s}
	g := app.Group("/v1/sbom")
	zip.Get(g, "/health", o.health)
	// Root route is declared on the /v1 PARENT with a non-empty leaf: zip.Post(g, "")
	// would normalise to "/v1/sbom/", and op.Path is the identity every projection
	// keys on, so the document, the operationId, the MCP tool and every generated SDK
	// would carry a trailing slash for a path this API has never served.
	zip.Post(app.Group("/v1"), "/sbom", o.ingest, zip.WithStatus(http.StatusCreated))
	// The greedy `*` is the address, not a shortcut: an image reference carries
	// slashes and a tag or a digest, and no named fiber segment matches across a
	// slash. zip declares that segment as the path parameter `wildcard1` and binds
	// it to the In field tagged with the router's own key for it, `url:"*1"`.
	zip.Get(g, "/*", o.resolve)
}

// requireDatastore returns the honest 503 when the datastore store is not
// connected, rather than fabricating a result. Mirrors the analytics lens.
func requireDatastore() error {
	if !datastore.Ready() {
		return zip.Errorf(http.StatusServiceUnavailable, "sbom store unavailable: datastore (datastore) not connected")
	}
	return nil
}

// tableMu guards the lazy, idempotent bootstrap of the global SBOM table. We latch
// ONLY success: a failed DDL (store still connecting) leaves tableReady false so a
// later request retries — sync.Once is wrong here because it would cache the
// failure forever.
var (
	tableMu    sync.Mutex
	tableReady bool
)

// ensureTable creates hanzo.sbom_component exactly once successfully, then becomes
// a cheap bool read. It exists because the datastore connects ASYNCHRONOUSLY: at
// Mount time DatastoreEnabled() is usually false, so the DDL was skipped and never
// retried — the init-order bug that 502'd resolve/ingest on a missing table in
// prod. Callers invoke it AFTER requireDatastore() passes; it (re)creates the
// database and table on the first request that finds the store ready. A transient
// error is returned, never latched, so the next call re-attempts.
func ensureTable(ctx context.Context) error {
	tableMu.Lock()
	defer tableMu.Unlock()
	if tableReady {
		return nil
	}
	if !datastore.Ready() {
		return fmt.Errorf("datastore not connected")
	}
	if err := datastore.Exec(ctx, createDatabase); err != nil {
		return fmt.Errorf("ensure database: %w", err)
	}
	if err := datastore.Exec(ctx, createTable); err != nil {
		return fmt.Errorf("ensure %s: %w", sbomTable, err)
	}
	tableReady = true
	return nil
}

// ── POST /v1/sbom — ingest (CI) ──────────────────────────────────────────────

// SbomIngested is the POST /v1/sbom receipt: which image was ingested and how many
// components were flattened out of its CycloneDX document.
//
// Field order is the order encoding/json emits a map's sorted keys in, which is what
// this response was before it had a type — so the receipt's BYTES did not move.
type SbomIngested struct {
	// ComponentCount is how many components the CycloneDX document yielded and this
	// call persisted.
	ComponentCount int `json:"componentCount"`
	// ImageDigest is the content-addressed digest the components were keyed under.
	ImageDigest string `json:"imageDigest"`
}

// Ingest persists a CycloneDX SBOM's components keyed by image digest. Gated to a
// validated SuperAdmin (owner == AdminOrg) — the canonical cloud super-admin
// check, which the build fleet / CI carries. Re-ingest is idempotent: rows share
// the (digest, name, version, purl) ORDER BY, so ReplacingMergeTree keeps the
// latest by ingested_at (and resolve reads FINAL).
//
// Example: {"imageDigest": "sha256:abc", "imageRef": "oci.hanzo.ai/hanzo/cloud:v1", "format": "cyclonedx", "document": {"components": []}}
func (o ops) ingest(ctx context.Context, in *SbomIngest) (*SbomIngested, error) {
	// The SuperAdmin gate needs more of the validated principal than the org — the
	// X-User-IsAdmin claim — which principal.OrgFrom does not carry. Fails closed off
	// the HTTP path: no request, no attested admin, no ingest.
	c, ok := cloud.Request(ctx)
	if !ok || !c.IsAdmin() {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	in.ImageDigest = strings.TrimSpace(in.ImageDigest)
	if in.ImageDigest == "" {
		return nil, zip.ErrBadRequest("imageDigest is required")
	}
	comps, err := parseComponents(in.Document)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	if err := requireDatastore(); err != nil {
		return nil, err
	}
	if err := ensureTable(ctx); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "sbom store initializing: %v", err)
	}

	if stmt, args := insertBatch(*in, comps); stmt != "" {
		if err := datastore.Exec(ctx, stmt, args...); err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "sbom insert: %v", err)
		}
	}
	o.s.Log.Info("sbom ingested", "imageDigest", in.ImageDigest, "components", len(comps), "sourceRepo", in.SourceRepo)
	return &SbomIngested{ComponentCount: len(comps), ImageDigest: in.ImageDigest}, nil
}

// ── GET /v1/sbom/{ref} — resolve (console) ───────────────────────────────────

// SbomRef names the image a resolve is asking about: the whole remainder of the
// path, as one value.
//
// The two tags on Ref are two ways in, and the pairing is deliberate. `url:"*1"`
// is the router's own key for the first greedy capture, which is what an HTTP
// caller fills. The wire name beside it is what a caller with no URL supplies
// instead — the op-call plane and an MCP tools/call both hand their arguments
// across as a body — and without it this operation would be a tool that cannot say
// which image it means. Where both arrive the address wins, because bindURL binds
// the body first, then the query, then the path.
type SbomRef struct {
	// Ref is the image to resolve, either its content-addressed digest
	// (`sha256:…`) or its full reference (`oci.hanzo.ai/hanzo/cloud:v1`). Both are
	// matched, so either spelling of one image answers the same components. It is
	// the greedy tail of the address, so slashes, a tag and a digest all travel in
	// it whole, and a percent-encoded ref is decoded before it is looked up. Empty
	// is a 400, never a scan of the store.
	Ref string `json:"ref" url:"*1"`
}

// Resolve returns everything inside one container image, addressed by its digest
// or by its image ref.
//
// Each component comes back with its name, version, type, package URL and license.
//
// This read is GLOBAL, not tenant-scoped, and deliberately so: a bill of materials
// belongs to a content-addressed digest rather than to an org, so every caller
// deploying the same image resolves the same components, and nothing tenant-owned
// is exposed by it. It still requires an attested caller — global is not public —
// and answers 403 without one. Ingest is the closed half of the pair.
//
// A miss is not the end of the lookup. The registry is the source of truth, so an
// unmaterialized ref is pulled from the SBOM attached to that image, persisted, and
// answered from the store — the first read of a freshly built image pays for the
// pull, later ones do not. The pull reads OUR registries and nothing else, which is
// what makes one shared answer trustworthy for every tenant: an attached document
// is whoever controls that repository speaking, so a ref outside them answers 404
// rather than a stranger's account of what is in their image. A bare digest with no
// repository is not pullable and answers an honest 404, as does a ref with no
// attached document. Repeated ingests collapse to the latest, components come back
// ordered by type then name, and a result over 5000 components is capped with
// `truncated` set. When the datastore is not connected the answer is 503 rather
// than a fabricated empty image.
func (o ops) resolve(ctx context.Context, in *SbomRef) (*SbomView, error) {
	// An attested caller, before anything else. GLOBAL is not PUBLIC: the read is
	// cross-tenant because a bill of materials belongs to a digest rather than to an
	// org, which says nothing about who may ask. And a miss does not merely read —
	// it fetches a document off the network and writes the shared table — so a
	// stranger must not be the one to start that. No org predicate follows, because
	// the answer is the same for every tenant; only the question needs an owner.
	//
	// The check is HERE and not on the group: the probe beside it shares that group
	// and has to stay answerable without a credential.
	if !principal.ValidatedFrom(ctx) {
		return nil, principal.RefusedFrom(ctx)
	}
	ref := strings.Trim(strings.TrimSpace(in.Ref), "/")
	if dec, err := url.PathUnescape(ref); err == nil {
		ref = dec
	}
	if ref == "" {
		return nil, zip.ErrBadRequest("image digest or ref is required")
	}
	if err := requireDatastore(); err != nil {
		return nil, err
	}
	if err := ensureTable(ctx); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "sbom store initializing: %v", err)
	}

	// component identity is the ORDER BY, so FINAL dedupes; type,name is the stable
	// display order; the cap is fetched +1 to detect truncation. ref binds to both
	// columns positionally — nothing is interpolated.
	q := "SELECT image_digest, image_ref, source_repo, git_sha, " +
		"component_name, component_version, component_type, purl, license, ingested_at " +
		"FROM " + sbomTable + " FINAL WHERE image_digest = ? OR image_ref = ? " +
		"ORDER BY component_type, component_name LIMIT " + fmt.Sprint(maxComponents+1)
	rows, err := datastore.Query(ctx, q, ref, ref)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "sbom query: %v", err)
	}
	if len(rows) == 0 {
		// Pull-on-miss: the registry is the source of truth. CI `cosign attach`es the
		// CycloneDX SBOM to the image digest; if this ref is a real image we haven't
		// materialized yet, pull the attached artifact, persist it, and re-read. A
		// pull failure is NON-FATAL — we fall through to the honest 404.
		if view, perr := pullAndStore(o.s, ctx, ref); perr == nil && view != nil {
			return view, nil
		} else if perr != nil {
			o.s.Log.Debug("sbom pull-on-miss failed", "ref", ref, "err", perr)
		}
		return nil, zip.ErrNotFound("no SBOM for " + ref)
	}
	view := buildView(rows)
	return &view, nil
}

// ── registry pull-on-miss (materialize) ──────────────────────────────────────

// pullAndStore fetches the SBOM `cosign attach`ed to ref's image digest from the
// registry, upserts its components into hanzo.sbom_component (reusing the SAME
// INSERT the CI POST path uses), and returns the resolved view. It is the lazy
// materializer behind resolve's pull-on-miss and the deploy-time prefetch. A bare
// `sha256:…` digest (no repo) is not pullable → returns an error the caller treats
// as a miss.
func pullAndStore(s *cloud.Service[state], ctx context.Context, ref string) (*SbomView, error) {
	res, err := pullSBOM(ctx, ref)
	if err != nil {
		return nil, err
	}
	in := SbomIngest{ImageDigest: res.Digest, ImageRef: res.Ref, Format: "cyclonedx"}
	if stmt, args := insertBatch(in, res.Components); stmt != "" {
		if err := datastore.Exec(ctx, stmt, args...); err != nil {
			return nil, fmt.Errorf("persist pulled sbom: %w", err)
		}
	}
	s.Log.Info("sbom pulled from registry", "imageRef", res.Ref, "imageDigest", res.Digest, "components", len(res.Components))

	// Re-read through FINAL so the response is byte-identical to a cache hit
	// (deduped, ordered, capped) rather than echoing the just-parsed slice.
	q := "SELECT image_digest, image_ref, source_repo, git_sha, " +
		"component_name, component_version, component_type, purl, license, ingested_at " +
		"FROM " + sbomTable + " FINAL WHERE image_digest = ? OR image_ref = ? " +
		"ORDER BY component_type, component_name LIMIT " + fmt.Sprint(maxComponents+1)
	rows, err := datastore.Query(ctx, q, res.Digest, res.Ref)
	if err != nil {
		return nil, fmt.Errorf("reread pulled sbom: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("pulled sbom for %s not visible after insert", res.Digest)
	}
	view := buildView(rows)
	return &view, nil
}

// Prefetch materializes the SBOM for a deployed image ref if it is not already in
// the datastore, on a deploy-digest signal from the platform. It is the deploy-time
// trigger: idempotent (a hit is a no-op), best-effort (a miss/pull-failure logs and
// returns, never blocks a deploy), and safe to call from a goroutine. Exported so
// clients/platform can fire it after a deployment goes live WITHOUT this package
// importing platform (dependency points platform → sbom, one direction).
func Prefetch(ctx context.Context, log luxlog.Logger, ref string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	if log == nil {
		log = luxlog.Default()
	}
	log = log.New("subsystem", "sbom")
	// Prefetch runs OUTSIDE Mount (no Deps), and pullAndStore only reads s.Log — so
	// a minimal Base carrying just the logger is the faithful analog of the old
	// per-package service value that held only the logger.
	s := &cloud.Service[state]{Base: cloud.Base{Log: log}}

	if err := requireDatastore(); err != nil {
		log.Debug("sbom prefetch skipped: datastore not ready", "ref", ref, "err", err)
		return
	}
	if err := ensureTable(ctx); err != nil {
		log.Debug("sbom prefetch skipped: table not ready", "ref", ref, "err", err)
		return
	}
	// Already materialized? Cheap existence check keyed by ref (a tag ref maps to the
	// same rows once resolved). Skip the network pull on a hit.
	rows, err := datastore.Query(ctx,
		"SELECT 1 FROM "+sbomTable+" WHERE image_ref = ? LIMIT 1", ref)
	if err == nil && len(rows) > 0 {
		return
	}
	if _, err := pullAndStore(s, ctx, ref); err != nil {
		log.Debug("sbom prefetch pull failed", "ref", ref, "err", err)
	}
}

// ── GET /v1/sbom/health — liveness ───────────────────────────────────────────

// SbomHealth is the GET /v1/sbom/health probe result.
//
// Field order is the order encoding/json emits a map's sorted keys in, which is what
// this response was before it had a type — so the probe's BYTES did not move.
type SbomHealth struct {
	// Datastore reports whether the shared datastore connection this subsystem reads
	// and writes through is established. False means the data endpoints answer 503.
	Datastore bool `json:"datastore"`
	// Service names the subsystem answering: always "sbom".
	Service string `json:"service"`
	// Status is the liveness verdict: always "ok" here, because the process answering
	// at all IS the liveness fact.
	Status string `json:"status"`
	// Table is the fully-qualified datastore table the components live in.
	Table string `json:"table"`
}

// Health is a pure liveness probe: the service is up; datastore reflects whether
// the datastore store is connected. Not JWT-gated, always 200 (a disconnected
// datastore is degraded-but-alive; the data endpoints report that as 503).
func (o ops) health(_ context.Context, _ *cloud.Unit) (*SbomHealth, error) {
	return &SbomHealth{
		Datastore: datastore.Ready(),
		Service:   "sbom",
		Status:    "ok",
		Table:     sbomTable,
	}, nil
}
