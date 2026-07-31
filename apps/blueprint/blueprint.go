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

// Package blueprint mounts the Hanzo Cloud /v1/blueprint/* surface: the
// compute-cost basis for the OSS-template economy. Each deployable blueprint is a
// docker-compose stack (templates.hanzo.ai/blueprints/<id>/docker-compose.yml);
// this subsystem turns one into two things a deploying org and the console need:
//
//  1. its SBOM — the bill of container images/services the stack runs, and
//  2. a COMPUTE COST estimate — a per-hour rate derived from the services' summed
//     CPU/memory footprint through a documented rate card (estimate.go).
//
// That rate is what the platform SHOWS as "~$X/mo to run" per template, AND the
// basis the deploy path meters the deploying org on. It is therefore the cost the
// 20% author royalty (clients/authors, defaultShareBps=2000) is taken from: the
// author-royalty sweep already accrues 20% of a deploying org's metered platform
// spend; this package defines, from a real rate card rather than a fabricated
// number, the compute component of that spend.
//
// DISTINCT FROM clients/sbom. That package stores a CycloneDX dependency SBOM
// keyed by image DIGEST — the packages INSIDE one image. This one derives the bill
// of IMAGES a compose stack runs and prices the stack's footprint. Different
// granularity, different concern; kept orthogonal.
//
// Reference content, no store. The blueprints are embedded and validated once at
// mount (a malformed fixture fails the mount closed); there is no per-tenant state.
//
// Surface (/v1 only):
//
//	GET /v1/blueprint                 list blueprint ids + a cost summary   -> {data:[…]}
//	GET /v1/blueprint/sbom?template=<id>  one blueprint's SBOM + cost        -> Estimate
//	GET /v1/blueprint/sbom            batch: every blueprint's SBOM + cost   -> {data:[Estimate]}
//	GET /v1/blueprint/health          liveness + the active rate card       (not JWT-gated)
//
// Its plugin declares OwnsHealth, so it serves its own /v1/blueprint/health and the
// host skips the generic liveness route.
package blueprint

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

//go:embed blueprints
var blueprintsFS embed.FS

// composeName is the compose file each blueprint dir carries. We accept the short
// name; the public path is templates.hanzo.ai/blueprints/<id>/docker-compose.yml.
const composeName = "compose.yml"

// state is blueprint's own data: none — blueprints are embedded reference content,
// resolved and priced on demand from the embed FS. Shared deps live in cloud.Base.
type state struct{}

// Mount wires the blueprint surface and validates every embedded blueprint at boot.
func Mount(app cloud.Router, deps cloud.Deps) error {
	rates = rateCardFromEnv() // overlay operator rate-card knobs once, at mount
	return cloud.Mount(app, deps, "blueprint", build, routes)
}

// build fails the mount closed if any embedded blueprint cannot be parsed and
// priced — a broken fixture must never reach a deploy sizing or a "$X/mo" card.
func build(b cloud.Base) (state, error) {
	ids, err := catalogIDs()
	if err != nil {
		return state{}, fmt.Errorf("read embedded blueprints: %w", err)
	}
	if len(ids) == 0 {
		return state{}, fmt.Errorf("no embedded blueprints")
	}
	for _, id := range ids {
		if _, ok, err := estimateTemplate(id); err != nil {
			return state{}, fmt.Errorf("blueprint %q: %w", id, err)
		} else if !ok {
			return state{}, fmt.Errorf("blueprint %q: missing %s", id, composeName)
		}
	}
	b.Log.Info("blueprint estimator",
		"blueprints", len(ids),
		"uUSDPerVCPUHr", rates.MicroUSDPerVCPUHour,
		"uUSDPerGBHr", rates.MicroUSDPerGBHour,
		"brand", b.Brand)
	return state{}, nil
}

// routes registers the read surface. Health is registered before nothing greedy
// (the paths are exact), and is not JWT-gated (liveness must be probe-able).
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := blueprintOps{s: s}
	zapp := cloud.ZipApp(app)
	g := app.Group("/v1/blueprint")
	zip.Get(g, "/health", o.health)
	// UNTYPED, and it has to be: ONE address answers with TWO shapes at 200 —
	// `?template=<id>` returns a bare Estimate, no template returns the
	// `{data:[Estimate]}` batch. A typed op declares one Out, so either shape
	// would publish the other as a lie. Convertible the day zip can declare a
	// polymorphic response (the #78 family).
	g.Get("/sbom", cloud.Handle(s, sbomRead))
	// Collection root stays flat — Group("/v1/blueprint").Get("") yields "/v1/blueprint/".
	zip.Get(zapp, "/v1/blueprint", o.list)
}

// blueprintOps binds the service to the typed blueprint ops. A TypedHandler takes
// no service parameter, so the service arrives as a RECEIVER and every op is a
// method value — also the only bound form cmd/zipdoc can lift prose from.
type blueprintOps struct{ s *cloud.Service[state] }

// noIn is the input of an op that takes nothing: no body, no path parameter, no
// query. GET carries no request body (zip's hasBody), so this publishes nothing.
type noIn struct{}

// ── in-process seam (deploy / metering / authors) ────────────────────────────

// EstimateTemplate prices an embedded blueprint by id, returning ok=false for an
// unknown id. This is the ONE in-process entrypoint the deploy path calls to learn
// a template's compute rate: est.MicroUSDPerHour is the exact per-hour figure to
// meter the deploying org on (via the same commerce metering spine resource_billing
// uses), and est.CentsPerMonth is the "~$X/mo to run" the console renders. The
// author-royalty sweep (clients/authors) then accrues its 20% off that metered
// spend — so this function is the cost basis the royalty is taken from.
func EstimateTemplate(id string) (Estimate, bool) {
	est, ok, err := estimateTemplate(id)
	if !ok || err != nil {
		return Estimate{}, false
	}
	return est, true
}

// estimateTemplate is the internal form that surfaces a parse error (build uses it
// to fail closed; EstimateTemplate collapses it to ok=false for callers).
func estimateTemplate(id string) (Estimate, bool, error) {
	doc, ok := loadCompose(id)
	if !ok {
		return Estimate{}, false, nil
	}
	est, err := EstimateCompose(id, doc)
	if err != nil {
		return Estimate{}, true, err
	}
	return est, true, nil
}

// loadCompose reads an embedded blueprint's compose by id. id must be a bare slug
// (no path separators or dots) so it can never traverse out of the embed root.
func loadCompose(id string) ([]byte, bool) {
	if id == "" || strings.ContainsAny(id, "/\\.") {
		return nil, false
	}
	b, err := blueprintsFS.ReadFile(path.Join("blueprints", id, composeName))
	if err != nil {
		return nil, false
	}
	return b, true
}

// catalogIDs lists the embedded blueprint ids, sorted for deterministic output.
func catalogIDs() ([]string, error) {
	entries, err := fs.ReadDir(blueprintsFS, "blueprints")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// ── handlers ─────────────────────────────────────────────────────────────────

// sbomRead answers GET /v1/blueprint/sbom. With ?template=<id> it returns that
// blueprint's SBOM + cost (404 on an unknown id); with no template it returns the
// batch of every blueprint's estimate for a gallery grid.
func sbomRead(s *cloud.Service[state], c *zip.Ctx) error {
	id := strings.TrimSpace(c.Query("template"))
	if id == "" {
		ids, err := catalogIDs()
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "blueprint: %v", err)
		}
		out := make([]Estimate, 0, len(ids))
		for _, bid := range ids {
			if est, ok := EstimateTemplate(bid); ok {
				out = append(out, est)
			}
		}
		return c.JSON(http.StatusOK, map[string]any{"data": out})
	}
	est, ok := EstimateTemplate(id)
	if !ok {
		return zip.ErrNotFound("no blueprint " + id)
	}
	return c.JSON(http.StatusOK, est)
}

// blueprintRow is one entry of the blueprint index: which stack it is, how many
// services it runs, and what a month of that footprint costs at the active card.
type blueprintRow struct {
	// TemplateID is the blueprint slug — the id GET /v1/blueprint/sbom takes as
	// ?template= and the path under templates.hanzo.ai/blueprints/<id>/.
	TemplateID string `json:"templateId"`
	// Services is how many compose services the stack runs.
	Services int `json:"services"`
	// CentsPerMonth is the estimated compute cost of running the whole stack for
	// one month, in USD cents, from the rate card GET /v1/blueprint/health echoes.
	CentsPerMonth int64 `json:"estCentsPerMonth"`
}

// blueprintIndex is the GET /v1/blueprint envelope: every priced blueprint, id-sorted.
type blueprintIndex struct {
	// Data is one row per embedded blueprint, sorted by template id.
	Data []blueprintRow `json:"data"`
}

// list returns every deployable blueprint with its service count and estimated
// monthly compute cost.
//
// It is the lightweight index the console renders as a template gallery before
// drilling into one stack's bill of images — GET /v1/blueprint/sbom?template=<id>
// is the detail view. The cost is the same figure the deploy path meters the
// deploying org on and the 20% author royalty is taken from, priced from the
// active rate card (GET /v1/blueprint/health echoes that card).
func (o blueprintOps) list(ctx context.Context, _ *noIn) (*blueprintIndex, error) {
	ids, err := catalogIDs()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "blueprint: %v", err)
	}
	out := make([]blueprintRow, 0, len(ids))
	for _, id := range ids {
		est, ok := EstimateTemplate(id)
		if !ok {
			continue
		}
		out = append(out, blueprintRow{TemplateID: id, Services: len(est.SBOM), CentsPerMonth: est.CentsPerMonth})
	}
	return &blueprintIndex{Data: out}, nil
}

// blueprintHealth is the GET /v1/blueprint/health body. Field order is the
// alphabetical key order the map it replaced marshalled in, so the bytes on the
// wire did not move when this route became a typed op.
type blueprintHealth struct {
	// Blueprints is how many blueprints this build has embedded and priced.
	Blueprints int `json:"blueprints"`
	// RateCard is the rate card actually in force after the operator env overlay
	// (CLOUD_BLUEPRINT_UCPU_HR / CLOUD_BLUEPRINT_UGB_HR), not the shipped default.
	RateCard RateCard `json:"rateCard"`
	// Service names the subsystem answering — always "blueprint".
	Service string `json:"service"`
	// Status is "ok"; the route answers 200 whenever the subsystem is mounted.
	Status string `json:"status"`
}

// health reports blueprint liveness and echoes the compute rate card in force.
//
// The rate card is the one the estimator actually applies after the operator env
// overlay, so an operator can confirm a tuned knob took effect rather than
// inferring it from a price. Not JWT-gated — a liveness probe must be reachable —
// and it always answers 200 while the subsystem is mounted.
func (o blueprintOps) health(ctx context.Context, _ *noIn) (*blueprintHealth, error) {
	ids, _ := catalogIDs()
	return &blueprintHealth{
		Blueprints: len(ids),
		RateCard:   rates,
		Service:    "blueprint",
		Status:     "ok",
	}, nil
}

// ── rate-card env overlay ────────────────────────────────────────────────────

// rateCardFromEnv overlays the two operator knobs on the shipped rate card. A
// non-positive or unset value keeps the default, so a partial or absent config
// never zeroes a rate (which would make every deployment appear free).
func rateCardFromEnv() RateCard {
	rc := DefaultRateCard()
	if v := envInt("CLOUD_BLUEPRINT_UCPU_HR"); v > 0 {
		rc.MicroUSDPerVCPUHour = v
	}
	if v := envInt("CLOUD_BLUEPRINT_UGB_HR"); v > 0 {
		rc.MicroUSDPerGBHour = v
	}
	return rc
}

func envInt(k string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(k)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
