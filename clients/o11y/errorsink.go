// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// errorsink.go is the Sentry projection of the canonical event plane: it consumes the
// analytics error fan-out (analytics.SetErrorSink) and lands each type:'error' event on
// the o11y Sentry plane, so /v1/event errors surface on sentry.hanzo.ai alongside the
// errors a Sentry SDK posts to /v1/sentry directly.
//
// WHY clients/o11y owns this (not clients/analytics): o11y is the ONE owner of the
// embedded runtime (embed.go), which holds Modules.Sentry — the tested ingest that
// writes BOTH the columnar events plane (o11y_sentry_events) AND the grouped-issue
// lifecycle (o11y_issues) from ONE normalize+fingerprint translate. clients/analytics
// stays orthogonal: it knows only "there is an error sink", handing over an o11y-FREE
// analytics.ErrorEvent. One coupling boundary, on the side that already couples to o11y.
//
// TENANCY (fail-closed, no state): the org is the analytics tenant slug (the validated
// principal/host-derived owner, never client input). It is mapped to the o11y org UUID
// by the SAME deterministic UUIDv5 the o11y read side uses (iamidentn.toUUID), so an
// error lands under the EXACT org the console resolves for that tenant — no lookup, no
// drift, no cross-tenant path. Each org gets one canonical Sentry project (get-or-create,
// cached), the isolation unit the events plane and the project selector key on.
//
// FAIL-SOFT (additive projection): the primary hanzo.events write already committed and
// the honest receipt already returned before this runs, on a detached goroutine. Every
// failure here — embed disabled, project unresolved, datastore hiccup — is logged and
// swallowed; it can never fail or slow an ingest.
package o11y

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/clients/analytics"
	"github.com/hanzoai/o11y/pkg/modules/errortracking/implerrortracking"
	"github.com/hanzoai/o11y/pkg/modules/sentry"
	"github.com/hanzoai/o11y/pkg/types/errortrackingtypes"
	"github.com/hanzoai/o11y/pkg/types/sentrytypes"
	"github.com/hanzoai/o11y/pkg/valuer"
)

const (
	// sentryLensEnv turns the Sentry error projection OFF when falsey. Default ON: a
	// live embedded runtime projects every org's /v1/event errors to its Sentry plane.
	sentryLensEnv = "CLOUD_SENTRY_LENS"

	// canonicalProjectSlug/Name is the ONE Sentry project a tenant's web/product errors
	// land in by default. Slug is stable (so get-or-create is idempotent) and outside
	// the reserved route words. Per-product projects are a clean follow-on (key the
	// cache on (org,product)); one project per org is the complete default.
	canonicalProjectSlug = "web"
	canonicalProjectName = "Web"

	// maxErrorFanout bounds concurrent projection work so an error burst cannot spawn
	// unbounded datastore writes; excess batches are dropped (fail-soft), mirroring the
	// destinations fan-out bound.
	maxErrorFanout = 32
	// errorSinkTimeout bounds one batch's project-resolve + dual-write.
	errorSinkTimeout = 15 * time.Second

	// tag value caps — a tag is a small indexed value, so the descriptive url/stack we
	// preserve are bounded before they enter the tags map.
	maxTagURL   = 512
	maxTagStack = 2048
)

// errorSinkSem bounds concurrent projection work (drop-on-saturation, fail-soft).
var errorSinkSem = make(chan struct{}, maxErrorFanout)

// projectCache memoizes org-UUID → canonical project UUID so the control-plane
// get-or-create runs once per org per process (both underlying ops are idempotent, so
// this is a fast path, not a correctness gate) — mirroring iamidentn's provisioned map.
var projectCache sync.Map // string(orgUUID) -> valuer.UUID

// sentryLensEnabled reports whether the error projection is on (default ON).
func sentryLensEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(sentryLensEnv))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// installErrorSink wires the analytics error fan-out to the embedded Sentry module. It
// is a no-op (and installs NO sink) unless the in-process runtime is up — the fallback
// reverse-proxy has no in-process module to call, so errors then simply stay in
// hanzo.events + the /v1/errors lens (honest degradation, never a failure). Called from
// mountRuntime AFTER embeddedRuntime is set.
func installErrorSink(log luxlog.Logger) {
	if !sentryLensEnabled() {
		log.Info("sentry error lens disabled", "flag", sentryLensEnv)
		return
	}
	if embeddedRuntime == nil {
		log.Info("sentry error lens inactive (no in-process runtime; errors stay in hanzo.events)")
		return
	}
	analytics.SetErrorSink(func(org string, errs []analytics.ErrorEvent) { consumeErrors(log, org, errs) })
	log.Info("sentry error lens installed (in-process runtime)")
}

// clearErrorSink detaches the error fan-out. Idempotent; safe when never installed.
func clearErrorSink() { analytics.SetErrorSink(nil) }

// consumeErrors is the analytics.SetErrorSink handler. It runs on the goroutine
// analytics detached, so it may do bounded synchronous work here. Every path is
// fail-soft: it NEVER returns to a caller and NEVER propagates an error to the ingest.
func consumeErrors(log luxlog.Logger, org string, errs []analytics.ErrorEvent) {
	defer func() {
		if r := recover(); r != nil {
			log.Warn("sentry error-sink panic", "org", org, "err", r)
		}
	}()
	rt := embeddedRuntime
	if rt == nil || len(errs) == 0 {
		return
	}
	module := rt.Modules.Sentry
	if module == nil {
		return
	}

	// Bound concurrent projection; drop (fail-soft) when saturated.
	select {
	case errorSinkSem <- struct{}{}:
		defer func() { <-errorSinkSem }()
	default:
		log.Warn("sentry error-sink saturated; dropping batch", "org", org, "events", len(errs))
		return
	}

	orgID := deriveOrgUUID(org)
	if orgID.IsZero() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), errorSinkTimeout)
	defer cancel()

	projectID, ok := ensureProject(ctx, module, orgID)
	if !ok {
		log.Debug("sentry error-sink: no project resolved for org (skipped)", "org", org)
		return
	}

	// ONE translate: analytics.ErrorEvent → the Sentry wire → o11y's normalize+fingerprint.
	occs := make([]*errortrackingtypes.Occurrence, 0, len(errs))
	for _, e := range errs {
		occ := implerrortracking.NormalizeEvent(buildSentryEvent(e), false)
		if occ == nil || occ.Fingerprint == "" {
			continue
		}
		occs = append(occs, occ)
	}
	if len(occs) == 0 {
		return
	}
	if err := module.Ingest(ctx, orgID, projectID, occs); err != nil {
		log.Warn("sentry error-sink ingest failed", "org", org, "events", len(occs), "err", err)
	}
}

// deriveOrgUUID maps the analytics tenant slug to the o11y org UUID. It REPLICATES the
// o11y read side's resolver EXACTLY (iamidentn.toUUID, kind "org"): a value that already
// parses as a UUID is used as-is; a slug is mapped via UUIDv5 over the URL namespace with
// name "hanzo:o11y:org:<slug>". This is the load-bearing coupling — the same pure formula
// on both sides is what makes a written error land under the org the console resolves.
// If o11y ever changes that formula, its READ side changes too and this must move with it
// (a bump re-verification point; pinned by TestDeriveOrgUUID).
func deriveOrgUUID(org string) valuer.UUID {
	if u, err := valuer.NewUUID(org); err == nil {
		return u
	}
	derived := uuid.NewSHA1(uuid.NameSpaceURL, []byte("hanzo:o11y:org:"+org))
	return valuer.MustNewUUID(derived.String())
}

// ensureProject resolves the org's canonical Sentry project (get-or-create), cached. The
// project is the isolation unit the events plane and the console project selector key on,
// so an error must be filed under one. Fail-soft: any control-plane error returns
// (zero,false) and the batch is skipped — never a crash, never another org's project.
func ensureProject(ctx context.Context, module sentry.Module, orgID valuer.UUID) (valuer.UUID, bool) {
	key := orgID.String()
	if v, ok := projectCache.Load(key); ok {
		return v.(valuer.UUID), true
	}
	if id, ok := findCanonicalProject(ctx, module, orgID); ok {
		projectCache.Store(key, id)
		return id, true
	}
	// None yet — create it (zero-onboarding, mirroring o11y's org auto-provisioning).
	created, err := module.CreateProject(ctx, orgID, &sentrytypes.PostableProject{
		Name: canonicalProjectName,
		Slug: canonicalProjectSlug,
	})
	if err == nil && created != nil && created.Project != nil {
		projectCache.Store(key, created.ID)
		return created.ID, true
	}
	// Create lost a race (slug already taken by a concurrent create) — re-resolve it.
	if id, ok := findCanonicalProject(ctx, module, orgID); ok {
		projectCache.Store(key, id)
		return id, true
	}
	return valuer.UUID{}, false
}

// findCanonicalProject returns the org's project whose slug is canonicalProjectSlug, if
// present. Org-scoped by the module (ListProjects filters org_id), so it can only ever
// see the caller's org.
func findCanonicalProject(ctx context.Context, module sentry.Module, orgID valuer.UUID) (valuer.UUID, bool) {
	list, err := module.ListProjects(ctx, orgID)
	if err != nil || list == nil {
		return valuer.UUID{}, false
	}
	for _, p := range list.Items {
		if p != nil && p.Project != nil && p.Slug == canonicalProjectSlug {
			return p.ID, true
		}
	}
	return valuer.UUID{}, false
}

// buildSentryEvent maps an analytics.ErrorEvent onto the Sentry wire event the o11y
// normalizer consumes. The folded /v1/event wire carries a raw stack STRING (not the
// structured frames a native Sentry envelope has), so grouping is by exception
// type+message (the normalizer's documented fallback) and the raw stack is preserved as
// a bounded tag rather than dropped. Pure — no I/O — so the mapping is unit-tested.
func buildSentryEvent(e analytics.ErrorEvent) *errortrackingtypes.SentryEvent {
	se := &errortrackingtypes.SentryEvent{
		EventID:     e.MessageID,
		Timestamp:   json.RawMessage(strconv.FormatInt(e.Time.UTC().Unix(), 10)),
		Platform:    e.Platform,
		Level:       firstNonEmpty(e.Level, "error"),
		Environment: e.Environment,
		Release:     e.Release,
		Transaction: e.Transaction,
		Tags:        buildTags(e),
	}
	if e.ExceptionType != "" || e.Message != "" {
		se.Exception = &errortrackingtypes.SentryException{
			Values: []errortrackingtypes.SentryExceptionValue{{
				Type:  firstNonEmpty(e.ExceptionType, "Error"),
				Value: e.Message,
			}},
		}
	}
	if e.DistinctID != "" {
		se.User = &errortrackingtypes.SentryUser{ID: e.DistinctID}
	}
	if e.TraceID != "" || e.SpanID != "" {
		if raw, err := json.Marshal(map[string]string{"trace_id": e.TraceID, "span_id": e.SpanID}); err == nil {
			se.Contexts = map[string]json.RawMessage{"trace": raw}
		}
	}
	return se
}

// buildTags renders the descriptive tags the projection preserves: the emitting surface
// (as service_name, so it populates the events-plane service column), the session, the
// library, the bounded url, whether the error was handled, and the bounded raw stack. A
// tag never affects grouping (the fingerprint ignores tags), so preserving the stack here
// keeps it for debugging without collapsing distinct errors. "" (nil) when empty.
func buildTags(e analytics.ErrorEvent) json.RawMessage {
	t := map[string]string{}
	if e.Product != "" {
		t["service_name"] = e.Product
	}
	if e.SessionID != "" {
		t["session"] = e.SessionID
	}
	if e.Library != "" {
		t["library"] = e.Library
	}
	if e.URL != "" {
		t["url"] = capString(e.URL, maxTagURL)
	}
	if e.Handled != nil {
		t["handled"] = strconv.FormatBool(*e.Handled)
	}
	if e.Stack != "" {
		t["stack"] = capString(e.Stack, maxTagStack)
	}
	if len(t) == 0 {
		return nil
	}
	b, err := json.Marshal(t)
	if err != nil {
		return nil
	}
	return b
}

// capString bounds a descriptive tag value.
func capString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
