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
// ONE ClickHouse client. Like clients/analytics, this package rides the SAME
// clickhouse-go client the ai subsystem opens in the shared Bootstrap
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
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ddlTimeout bounds the one-shot table bootstrap on Mount.
const ddlTimeout = 10 * time.Second

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

type svc struct {
	log luxlog.Logger
}

// Mount wires the SBOM surface onto app and bootstraps the global table.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("sbom.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("sbom.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "sbom")
	s := &svc{log: log}

	// Bootstrap the global table idempotently when the datastore is connected. If
	// it is not, skip honestly — the data endpoints return 503 (mirrors analytics).
	if aiobject.DatastoreEnabled() {
		ctx, cancel := context.WithTimeout(context.Background(), ddlTimeout)
		defer cancel()
		if err := aiobject.DatastoreExec(ctx, createTable); err != nil {
			return fmt.Errorf("sbom.Mount: bootstrap %s: %w", sbomTable, err)
		}
		log.Info("sbom table ready", "table", sbomTable)
	} else {
		log.Warn("datastore not connected; skipping sbom DDL (endpoints will 503)")
	}

	// Health is a static route registered BEFORE the greedy resolve wildcard so it
	// is never captured by it. Health is not JWT-gated (liveness must be probe-able).
	app.Get("/v1/sbom/health", s.health)
	app.Post("/v1/sbom", s.ingest)
	app.Get("/v1/sbom/*", s.resolve)

	log.Info("sbom mounted", "table", sbomTable, "brand", deps.Brand)
	return nil
}

func init() {
	cloud.Register("sbom", 137, cloud.Typed(Mount), cloud.HealthOwner)
}

// requireDatastore returns the honest 503 when the ClickHouse store is not
// connected, rather than fabricating a result. Mirrors the analytics lens.
func requireDatastore() error {
	if !aiobject.DatastoreEnabled() {
		return zip.Errorf(http.StatusServiceUnavailable, "sbom store unavailable: datastore (ClickHouse) not connected")
	}
	return nil
}

// ── POST /v1/sbom — ingest (CI) ──────────────────────────────────────────────

// ingest persists a CycloneDX SBOM's components keyed by image digest. Gated to a
// validated GLOBAL admin (owner == AdminOrg) — the canonical cloud super-admin
// check, which the build fleet / CI carries. Re-ingest is idempotent: rows share
// the (digest, name, version, purl) ORDER BY, so ReplacingMergeTree keeps the
// latest by ingested_at (and resolve reads FINAL).
func (s *svc) ingest(c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
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

	if stmt, args := insertBatch(in, comps); stmt != "" {
		if err := aiobject.DatastoreExec(c.Context(), stmt, args...); err != nil {
			return zip.Errorf(http.StatusBadGateway, "sbom insert: %v", err)
		}
	}
	s.log.Info("sbom ingested", "imageDigest", in.ImageDigest, "components", len(comps), "sourceRepo", in.SourceRepo)
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
func (s *svc) resolve(c *zip.Ctx) error {
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
		return zip.ErrNotFound("no SBOM for " + ref)
	}
	return c.JSON(http.StatusOK, buildView(rows))
}

// ── GET /v1/sbom/health — liveness ───────────────────────────────────────────

// health is a pure liveness probe: the service is up; datastore reflects whether
// the ClickHouse store is connected. Not JWT-gated, always 200 (a disconnected
// datastore is degraded-but-alive; the data endpoints report that as 503).
func (s *svc) health(c *zip.Ctx) error {
	return c.JSON(http.StatusOK, map[string]any{
		"service":   "sbom",
		"status":    "ok",
		"datastore": aiobject.DatastoreEnabled(),
		"table":     sbomTable,
	})
}
