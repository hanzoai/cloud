// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hanzoai/cloud/clients/analytics"
	"github.com/hanzoai/o11y/pkg/modules/errortracking/implerrortracking"
	"github.com/hanzoai/o11y/pkg/modules/sentry"
	"github.com/hanzoai/o11y/pkg/types/sentrytypes"
	"github.com/hanzoai/o11y/pkg/valuer"
)

// TestDeriveOrgUUID pins the org-slug → o11y-org-UUID mapping to the EXACT formula the
// o11y read side (iamidentn.toUUID) uses. If either side's namespace/template drifts, an
// error would land under an org the console does not resolve — so this is a load-bearing
// contract, locked two ways: the literal for "hanzo" AND the formula equality.
func TestDeriveOrgUUID(t *testing.T) {
	// Literal pin (recomputed independently): UUIDv5(URL, "hanzo:o11y:org:hanzo").
	const hanzoOrgUUID = "cd35a51b-f7e7-5412-b67f-58b8703e5219"
	if got := deriveOrgUUID("hanzo").String(); got != hanzoOrgUUID {
		t.Fatalf("deriveOrgUUID(hanzo) = %s, want the pinned %s (o11y iamidentn contract drift?)", got, hanzoOrgUUID)
	}

	// Formula equality — the derivation IS UUIDv5 over the URL namespace with the
	// "hanzo:o11y:org:<slug>" name, for every slug.
	for _, org := range []string{"hanzo", "lux", "zoo", "acme-co"} {
		want := uuid.NewSHA1(uuid.NameSpaceURL, []byte("hanzo:o11y:org:"+org)).String()
		if got := deriveOrgUUID(org).String(); got != want {
			t.Errorf("deriveOrgUUID(%q) = %s, want %s", org, got, want)
		}
	}

	// A value that already IS a UUID passes through unchanged (matches iamidentn).
	raw := "11111111-2222-3333-4444-555555555555"
	if got := deriveOrgUUID(raw).String(); got != raw {
		t.Errorf("a UUID input must pass through: got %s", got)
	}

	// Distinct slugs never collide.
	if deriveOrgUUID("lux") == deriveOrgUUID("zoo") {
		t.Error("distinct orgs must map to distinct UUIDs")
	}
}

// TestBuildSentryEventNormalizes proves the ONE translate: an analytics.ErrorEvent maps
// onto the Sentry wire and through o11y's normalizer to a fingerprinted, correctly-scoped
// Occurrence — the value the Sentry module ingests.
func TestBuildSentryEventNormalizes(t *testing.T) {
	handled := false
	e := analytics.ErrorEvent{
		MessageID: "evt-1", Time: time.Unix(1700000000, 0).UTC(),
		ExceptionType: "TypeError", Message: "x is not a function",
		Stack: "at f (app.js:1:1)", Handled: &handled, Level: "error",
		Transaction: "/checkout", URL: "https://acme.ai/checkout",
		DistinctID: "u1", SessionID: "s1", Product: "app", Library: "@hanzo/event",
		TraceID: "trace-1", SpanID: "span-1",
	}
	occ := implerrortracking.NormalizeEvent(buildSentryEvent(e), false)
	if occ == nil {
		t.Fatal("nil occurrence")
	}
	if occ.Type != "TypeError" || occ.Value != "x is not a function" {
		t.Errorf("type/value = %q/%q", occ.Type, occ.Value)
	}
	if occ.Level != "error" {
		t.Errorf("level = %q", occ.Level)
	}
	if occ.Fingerprint == "" {
		t.Error("fingerprint must be set (grouping key)")
	}
	if occ.EventID != "evt-1" {
		t.Errorf("eventID = %q", occ.EventID)
	}
	if occ.Transaction != "/checkout" {
		t.Errorf("transaction = %q", occ.Transaction)
	}
	if occ.ServiceName != "app" {
		t.Errorf("serviceName must come from the surface tag: %q", occ.ServiceName)
	}
	if occ.User == nil || occ.User.ID != "u1" {
		t.Errorf("user id (not PII) must survive the scrub: %+v", occ.User)
	}
	if occ.TraceID != "trace-1" || occ.SpanID != "span-1" {
		t.Errorf("trace linkage = %q/%q", occ.TraceID, occ.SpanID)
	}
	if occ.Tags["stack"] == "" {
		t.Errorf("raw stack must be preserved as a tag: %+v", occ.Tags)
	}
	if occ.Tags["session"] != "s1" {
		t.Errorf("session tag = %q", occ.Tags["session"])
	}
}

// TestBuildSentryEventBareError proves a bare error-typed event (no exception) still
// normalizes to a fingerprinted occurrence rather than being dropped.
func TestBuildSentryEventBareError(t *testing.T) {
	occ := implerrortracking.NormalizeEvent(buildSentryEvent(analytics.ErrorEvent{
		MessageID: "e", Time: time.Now().UTC(), Level: "error", Transaction: "/x",
	}), false)
	if occ == nil || occ.Fingerprint == "" {
		t.Fatalf("bare error must still fingerprint: %+v", occ)
	}
}

// fakeSentry is a minimal sentry.Module: it implements ONLY the two project methods
// ensureProject uses; every other method dispatches to the nil embedded interface and
// would panic if called (the tests never do).
type fakeSentry struct {
	sentry.Module
	mu          sync.Mutex
	projects    []*sentrytypes.GettableProject
	createCalls int
}

func (f *fakeSentry) ListProjects(_ context.Context, _ valuer.UUID) (*sentrytypes.GettableProjects, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	items := make([]*sentrytypes.GettableProject, len(f.projects))
	copy(items, f.projects)
	return &sentrytypes.GettableProjects{Items: items, Total: len(items)}, nil
}

func (f *fakeSentry) CreateProject(_ context.Context, _ valuer.UUID, in *sentrytypes.PostableProject) (*sentrytypes.GettableProject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	p := &sentrytypes.Project{Name: in.Name, Slug: in.Slug}
	p.ID = valuer.GenerateUUID()
	gp := &sentrytypes.GettableProject{Project: p}
	f.projects = append(f.projects, gp)
	return gp, nil
}

// TestEnsureProjectGetOrCreate: first call creates the canonical project and caches it;
// the second call is served from cache (no second create).
func TestEnsureProjectGetOrCreate(t *testing.T) {
	f := &fakeSentry{}
	orgID := deriveOrgUUID("acme-ensure-create")
	projectCache.Delete(orgID.String())
	ctx := context.Background()

	id1, ok := ensureProject(ctx, f, orgID)
	if !ok || id1.IsZero() {
		t.Fatal("first ensure must create+return a project")
	}
	if f.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", f.createCalls)
	}
	id2, ok := ensureProject(ctx, f, orgID)
	if !ok || id2 != id1 {
		t.Fatalf("cached id mismatch: %v vs %v", id1, id2)
	}
	if f.createCalls != 1 {
		t.Fatalf("second ensure created again: createCalls = %d", f.createCalls)
	}
}

// TestEnsureProjectFindsExisting: when a canonical project already exists, ensureProject
// reuses it and never creates a second.
func TestEnsureProjectFindsExisting(t *testing.T) {
	f := &fakeSentry{}
	orgID := deriveOrgUUID("acme-ensure-existing")
	projectCache.Delete(orgID.String())

	pre := &sentrytypes.Project{Slug: canonicalProjectSlug}
	pre.ID = valuer.GenerateUUID()
	f.projects = append(f.projects, &sentrytypes.GettableProject{Project: pre})

	id, ok := ensureProject(context.Background(), f, orgID)
	if !ok || id != pre.ID {
		t.Fatalf("must reuse existing project: %v vs %v", id, pre.ID)
	}
	if f.createCalls != 0 {
		t.Fatalf("must not create when one exists: createCalls = %d", f.createCalls)
	}
}

// TestSentryLensEnabled covers the default-ON flag semantics.
func TestSentryLensEnabled(t *testing.T) {
	t.Setenv(sentryLensEnv, "")
	if !sentryLensEnabled() {
		t.Error("default must be ON")
	}
	for _, off := range []string{"0", "false", "no", "off", "OFF"} {
		t.Setenv(sentryLensEnv, off)
		if sentryLensEnabled() {
			t.Errorf("%q must disable the lens", off)
		}
	}
	t.Setenv(sentryLensEnv, "1")
	if !sentryLensEnabled() {
		t.Error("1 must enable the lens")
	}
}

// TestClearErrorSinkIsSafe: clearing when never installed is a no-op (idempotent).
func TestClearErrorSinkIsSafe(t *testing.T) {
	clearErrorSink()
	clearErrorSink()
}
