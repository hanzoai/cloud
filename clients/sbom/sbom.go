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

// Package sbom mounts the Hanzo Cloud /v1/sbom/* surface: the backend half of
// "SBOM visible in console on deployments + tracked in the datastore globally".
// CI POSTs a CycloneDX SBOM keyed by image digest; the console GETs it back by
// digest or image ref.
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

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

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
func Mount(app *zip.App, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "sbom", build, routes)
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

// routes registers the SBOM surface. Health is a static route registered BEFORE the
// greedy resolve wildcard so it is never captured by it. Health is not JWT-gated
// (liveness must be probe-able).
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/sbom/health", cloud.Handle(s, health))
	app.Post("/v1/sbom", cloud.Handle(s, ingest))
	app.Get("/v1/sbom/*", cloud.Handle(s, resolve))
}

// requireDatastore returns the honest 503 when the datastore store is not
// connected, rather than fabricating a result. Mirrors the analytics lens.
func requireDatastore() error {
	if !aiobject.DatastoreEnabled() {
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
	if !aiobject.DatastoreEnabled() {
		return fmt.Errorf("datastore not connected")
	}
	if err := aiobject.DatastoreExec(ctx, createDatabase); err != nil {
		return fmt.Errorf("ensure database: %w", err)
	}
	if err := aiobject.DatastoreExec(ctx, createTable); err != nil {
		return fmt.Errorf("ensure %s: %w", sbomTable, err)
	}
	tableReady = true
	return nil
}

// ── POST /v1/sbom — ingest (CI) ──────────────────────────────────────────────

// ingest persists a CycloneDX SBOM's components keyed by image digest. Gated to a
// validated SuperAdmin (owner == AdminOrg) — the canonical cloud super-admin
// check, which the build fleet / CI carries. Re-ingest is idempotent: rows share
// the (digest, name, version, purl) ORDER BY, so ReplacingMergeTree keeps the
// latest by ingested_at (and resolve reads FINAL).
func ingest(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	var in SbomIngest
	if err := c.Bind(&in); err != nil {
		return err
	}
	in.ImageDigest = strings.TrimSpace(in.ImageDigest)
	if in.ImageDigest == "" {
		return zip.ErrBadRequest("imageDigest is required")
	}
	comps, err := parseComponents(in.Document)
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	if err := requireDatastore(); err != nil {
		return err
	}
	if err := ensureTable(c.Context()); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "sbom store initializing: %v", err)
	}

	if stmt, args := insertBatch(in, comps); stmt != "" {
		if err := aiobject.DatastoreExec(c.Context(), stmt, args...); err != nil {
			return zip.Errorf(http.StatusBadGateway, "sbom insert: %v", err)
		}
	}
	s.Log.Info("sbom ingested", "imageDigest", in.ImageDigest, "components", len(comps), "sourceRepo", in.SourceRepo)
	return c.JSON(http.StatusCreated, map[string]any{
		"imageDigest":    in.ImageDigest,
		"componentCount": len(comps),
	})
}

// ── GET /v1/sbom/{ref} — resolve (console) ───────────────────────────────────

// resolve returns the SBOM for an image digest OR image ref. The greedy `*` param
// carries the (possibly slash-bearing, possibly percent-encoded) ref; we decode it
// and bind it to BOTH columns. FINAL collapses ReplacingMergeTree duplicates from
// repeated ingests. 404 when nothing matches (honest empty, never fabricated).
func resolve(s *cloud.Service[state], c *zip.Ctx) error {
	ref := strings.Trim(strings.TrimSpace(c.Fiber().Params("*")), "/")
	if dec, err := url.PathUnescape(ref); err == nil {
		ref = dec
	}
	if ref == "" {
		return zip.ErrBadRequest("image digest or ref is required")
	}
	if err := requireDatastore(); err != nil {
		return err
	}
	if err := ensureTable(c.Context()); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "sbom store initializing: %v", err)
	}

	// component identity is the ORDER BY, so FINAL dedupes; type,name is the stable
	// display order; the cap is fetched +1 to detect truncation. ref binds to both
	// columns positionally — nothing is interpolated.
	q := "SELECT image_digest, image_ref, source_repo, git_sha, " +
		"component_name, component_version, component_type, purl, license, ingested_at " +
		"FROM " + sbomTable + " FINAL WHERE image_digest = ? OR image_ref = ? " +
		"ORDER BY component_type, component_name LIMIT " + fmt.Sprint(maxComponents+1)
	rows, err := aiobject.DatastoreQuery(c.Context(), q, ref, ref)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "sbom query: %v", err)
	}
	if len(rows) == 0 {
		// Pull-on-miss: the registry is the source of truth. CI `cosign attach`es the
		// CycloneDX SBOM to the image digest; if this ref is a real image we haven't
		// materialized yet, pull the attached artifact, persist it, and re-read. A
		// pull failure is NON-FATAL — we fall through to the honest 404.
		if view, perr := pullAndStore(s, c.Context(), ref); perr == nil && view != nil {
			return c.JSON(http.StatusOK, *view)
		} else if perr != nil {
			s.Log.Debug("sbom pull-on-miss failed", "ref", ref, "err", perr)
		}
		return zip.ErrNotFound("no SBOM for " + ref)
	}
	return c.JSON(http.StatusOK, buildView(rows))
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
		if err := aiobject.DatastoreExec(ctx, stmt, args...); err != nil {
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
	rows, err := aiobject.DatastoreQuery(ctx, q, res.Digest, res.Ref)
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
		log = luxlog.Noop()
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
	rows, err := aiobject.DatastoreQuery(ctx,
		"SELECT 1 FROM "+sbomTable+" WHERE image_ref = ? LIMIT 1", ref)
	if err == nil && len(rows) > 0 {
		return
	}
	if _, err := pullAndStore(s, ctx, ref); err != nil {
		log.Debug("sbom prefetch pull failed", "ref", ref, "err", err)
	}
}

// ── GET /v1/sbom/health — liveness ───────────────────────────────────────────

// health is a pure liveness probe: the service is up; datastore reflects whether
// the datastore store is connected. Not JWT-gated, always 200 (a disconnected
// datastore is degraded-but-alive; the data endpoints report that as 503).
func health(s *cloud.Service[state], c *zip.Ctx) error {
	return c.JSON(http.StatusOK, map[string]any{
		"service":   "sbom",
		"status":    "ok",
		"datastore": aiobject.DatastoreEnabled(),
		"table":     sbomTable,
	})
}
