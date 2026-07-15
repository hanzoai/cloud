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

// Package featuregate is the launch-control plane for Hanzo's hosted services:
// the ONE source of truth for whether each service (studio.hanzo.ai, hanzo.chat,
// console.hanzo.ai, hanzo.app, api.hanzo.ai, hanzo.team, …) is in WAITLIST MODE.
// It owns a small global SQLite registry {service, hosts, waitlistMode} and serves
// the control-plane admin.hanzo.ai calls to list services and flip a service's
// mode — the "remove the waitlist one service at a time" toggle.
//
// TWO knobs, ONE rule (decomplected):
//
//   - PER-SERVICE  waitlistMode on|off  — this registry (a row per hosted service).
//     OFF = open to any signed-up user. This is the admin's launch lever.
//   - PER-USER     approvalStatus pending|approved — owned by IAM (iam#104:
//     get-pending-users / approve-user / reject-user). REUSED, not rebuilt.
//
// THE RULE, applied at ONE native enforcement point (Enforce, middleware.go) and
// mirrored by the interim @file waitlist-guard (which reads the SAME registry over
// GET /v1/featuregate/mode so an admin toggle governs it WITHOUT an ingress edit):
//
//	if waitlistMode[host] AND NOT user.approved → bounce to the waitlist
//	if approved OR mode=off                     → allow
//	unauthenticated                             → login first
//
// Surface (all /v1/, never /api/):
//
//	GET  /v1/admin/services              (global-admin) list every service + mode — the board
//	POST /v1/admin/services/:service/mode (global-admin) flip waitlistMode {waitlistMode:bool}
//	GET  /v1/featuregate/mode?host=<h>   (public read)  the guard's runtime mode lookup
//
// The Pending-Users QUEUE (approve/reject/pending) is NOT re-served here — it is
// IAM's iam#104, reached by admin.hanzo.ai through its existing /admin/iam gated
// proxy. One approval API, one registry, one enforcement rule — no per-app copy.
//
// serve.go auto-registers GET /v1/featuregate/health.
package featuregate

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type svc struct {
	store *Store
	log   luxlog.Logger
}

// moduleStoreRef / moduleMu back moduleStore() — the lazy handle the native
// Enforce middleware (which serve.go wires BEFORE MountAll) reads per request.
var (
	moduleMu       sync.RWMutex
	moduleStoreRef *Store
	mounted        *svc
)

// Mount wires the feature-gate surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("featuregate.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("featuregate.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "featuregate")
	if deps.DataDir == "" {
		return fmt.Errorf("featuregate.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("featuregate.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "featuregate.db"))
	if err != nil {
		return fmt.Errorf("featuregate.Mount: open store: %w", err)
	}

	// Seed the live hosted services idempotently (INSERT OR IGNORE — never
	// clobbers a live admin toggle). Brand-scoped so a Lux/Zoo deployment seeds
	// its OWN hosts, not hanzo.ai's.
	created, serr := store.Seed(context.Background(), seedFor(deps.Brand), time.Now().Unix())
	if serr != nil {
		_ = store.Close()
		return fmt.Errorf("featuregate.Mount: seed: %w", serr)
	}

	s := &svc{store: store, log: log}
	mounted = s
	setModuleStore(store)

	// Control-plane — admin board + toggle + onboard (global-admin, fail-closed).
	app.Get("/v1/admin/services", s.guard(s.listServices))
	app.Post("/v1/admin/services", s.guard(s.upsertService))
	app.Post("/v1/admin/services/:service/mode", s.guard(s.setMode))
	// Runtime mode read for the @file waitlist-guard (cheap, cacheable, no auth —
	// it returns only a boolean for the queried host, never an enumeration).
	app.Get("/v1/featuregate/mode", s.modeForHost)

	log.Info("featuregate mounted", "brand", deps.Brand, "seeded", created)
	return nil
}

// Mount order is fixed by the apps.Wire() composition root (right after admin,
// before the AI /v1/* catch-all). All routes are specific (/v1/admin/services*,
// /v1/featuregate/*), so they bind ahead of the catch-all regardless; the static
// /v1/featuregate/mode and the /v1/admin/services list bind before the
// /:service/mode param route. This package no longer self-registers via an init().

// guard wraps a handler with the global-admin gate — the SAME predicate the rest
// of cloud uses (c.IsAdmin(), true only for a JWT-validated owner==AdminOrg after
// SanitizeIdentity). Fail-closed: a non-admin never reaches the handler.
func (s *svc) guard(h func(*zip.Ctx) error) zip.Handler {
	return func(c *zip.Ctx) error {
		if !c.IsAdmin() {
			return zip.ErrForbidden("global admin required")
		}
		return h(c)
	}
}

// ── control-plane handlers ─────────────────────────────────────────────────────

// listServices answers GET /v1/admin/services — the launch dashboard's board.
// Global-admin only. Envelope = { status:"ok", data:{ services:[...] } } (the
// console admin proxy unwraps { status, msg, data }).
func (s *svc) listServices(c *zip.Ctx) error {
	rows, err := s.store.List(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list services: %v", err)
	}
	return adminOK(c, map[string]any{"services": rows})
}

// upsertRequest is the POST /v1/admin/services body — onboard or edit a service.
type upsertRequest struct {
	Service      string   `json:"service"`
	DisplayName  string   `json:"displayName"`
	Description  string   `json:"description"`
	Hosts        []string `json:"hosts"`
	WaitlistMode bool     `json:"waitlistMode"`
}

// upsertService registers or edits a hosted service so a new host is governed
// WITHOUT a redeploy. Global-admin only. A re-register preserves the live mode of
// an existing service (see Store.Upsert) — never silently re-gating an opened one.
func (s *svc) upsertService(c *zip.Ctx) error {
	var body upsertRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Service) == "" {
		return zip.ErrBadRequest("service slug is required")
	}
	updated, err := s.store.Upsert(c.Context(), Service{
		Service:      body.Service,
		DisplayName:  body.DisplayName,
		Description:  body.Description,
		Hosts:        body.Hosts,
		WaitlistMode: body.WaitlistMode,
	}, strings.TrimSpace(c.User()), time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "upsert service: %v", err)
	}
	s.log.Info("service registered", "service", updated.Service, "hosts", updated.Hosts, "by", c.User())
	return adminOK(c, map[string]any{"service": updated})
}

// modeRequest is the POST /v1/admin/services/:service/mode body.
type modeRequest struct {
	WaitlistMode bool `json:"waitlistMode"`
}

// setMode flips one service's waitlist mode. Global-admin only. The toggle takes
// effect immediately (the native middleware reads the store per request; the guard
// re-reads within its short TTL) — no redeploy, no ingress edit. UpdatedBy records
// the admin who flipped it (the toggle's own audit).
func (s *svc) setMode(c *zip.Ctx) error {
	service := strings.TrimSpace(c.Param("service"))
	if service == "" {
		return zip.ErrBadRequest("service is required")
	}
	var body modeRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	by := strings.TrimSpace(c.User())
	updated, err := s.store.SetMode(c.Context(), service, body.WaitlistMode, by, time.Now().Unix())
	if err != nil {
		if err == errNotFound {
			return zip.ErrNotFound("service not found: " + service)
		}
		return zip.Errorf(http.StatusInternalServerError, "set mode: %v", err)
	}
	s.log.Info("waitlist mode toggled", "service", updated.Service, "waitlistMode", updated.WaitlistMode, "by", by)
	return adminOK(c, map[string]any{"service": updated})
}

// modeForHost answers GET /v1/featuregate/mode?host=<h> — the runtime lookup the
// @file waitlist-guard caches. It returns ONLY the boolean mode for the ONE queried
// host (no enumeration of the registry), so it is safe to serve without auth to an
// in-cluster caller. An un-governed host returns waitlistMode:false, known:false —
// the guard, attached only to gated hosts, fails SAFE (treats unknown as gated) on
// its own side; the native middleware treats unknown as pass.
func (s *svc) modeForHost(c *zip.Ctx) error {
	host := strings.TrimSpace(c.Query("host"))
	if host == "" {
		host = c.Fiber().Hostname()
	}
	mode, service, known, err := s.store.ModeForHost(c.Context(), host)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "mode for host: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"host":         NormalizeHost(host),
		"service":      service,
		"waitlistMode": mode,
		"known":        known,
	})
}

// adminOK writes the { status:"ok", msg, data } envelope the console admin surface
// (originGet/originPost via app/admin/aggregate) unwraps — identical to
// clients/admin's ok() and clients/authors' adminOK.
func adminOK(c *zip.Ctx, data any) error {
	return c.JSON(http.StatusOK, map[string]any{"status": "ok", "msg": "", "data": data})
}

// ── module store + shutdown ────────────────────────────────────────────────────

func setModuleStore(st *Store) {
	moduleMu.Lock()
	defer moduleMu.Unlock()
	moduleStoreRef = st
}

// Shutdown closes the featuregate store. Idempotent.
func Shutdown() error {
	moduleMu.Lock()
	st := moduleStoreRef
	moduleStoreRef = nil
	moduleMu.Unlock()
	mounted = nil
	if st == nil {
		return nil
	}
	return st.Close()
}
