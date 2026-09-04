// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

// bases.go answers WHICH BASES A CALLER CAN REACH, which is the one question the
// per-org engine behind /v1/base/* cannot answer about itself: that wildcard
// hands every request to ONE org's Base, so a listing addressed there arrives at
// an engine that has no route for it and reports not-found. The space read
// that as "you have no Bases" and showed an empty account — the whole product,
// invisible, with every other part working.
//
// A Base is not a thing you create. It is what an org HAS, one each, provisioned
// the first time anything touches it — so there is no create verb here and never
// should be. `Exists` is that state named rather than hidden: an org whose store
// has not been opened yet is a Base you can walk into, not an error.

import (
	"context"
	"github.com/hanzoai/cloud"
	"os"
	"path/filepath"
	"strings"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/goja"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the prose below into zipdoc_gen.go, which is the only way it
// reaches the published document, the MCP tool list and the generated SDKs.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// baseRef addresses ONE org's Base.
type baseRef struct {
	// Org is the org whose Base to describe, from the path. An org the caller's
	// token does not carry is not found — the same answer a nonexistent one
	// gets, so the listing cannot be used to discover which orgs exist.
	Org string `json:"org"`
}

// baseView is one org's Base: the org it belongs to, and whether its store has
// been opened yet.
type baseView struct {
	// Org is the org this Base belongs to. It is the address every other Base
	// call is scoped by, and a Base has no name of its own.
	Org string `json:"org"`
	// Exists reports whether this Base's store has been provisioned. False is an
	// org nobody has stored anything for yet, which is a state to name rather
	// than an error: the store is created the first time anything writes.
	Exists bool `json:"exists"`
	// Bytes is the store's size on disk, present only once it exists. It is what
	// this Base occupies, not a quota.
	Bytes int64 `json:"bytes,omitempty"`
}

// baseList is the listing's answer, and a NAMED SLICE because the wire is a bare
// JSON array. An envelope struct is the shape a typed op reaches for by reflex
// and would be a silent wire break here: the space reads `Base[]`.
type baseList []baseView

// baseOps carries no state: it reads the mounted subsystem at REQUEST time, so
// the ops can be registered before the embed gate and still describe the pool
// once there is one. That ordering is what keeps them in the published document
// — a route registered behind a feature gate is absent from the projection the
// SDKs, the CLI and the tool list are cut from, which is how the hosting lane
// beside them has gone undocumented since it shipped.
type baseOps struct{}

// Lists every Base the caller can reach, one per org their token carries.
//
// The orgs come from IAM's signed membership set, so the list is exactly the
// orgs the caller is a member of and cannot be widened by asking. It is the
// account-wide view: a Base is per org, so this is one entry per org and there
// is nothing to page.
//
// A caller with no membership set — a machine credential, an API key — reaches
// no Base and receives an empty list rather than a refusal, because holding no
// membership is an answer and not a failure.
//
// Response: [{"org":"hanzo","exists":true,"bytes":430080}]
func (o baseOps) list(ctx context.Context, _ *cloud.Unit) (*baseList, error) {
	// A deployment with no Base engine hosts no Bases, so it lists none — the
	// truth about it, rather than a row per org claiming one that cannot exist.
	if mounted == nil {
		return &baseList{}, nil
	}
	orgs := principal.OrgsFrom(ctx)
	out := make(baseList, 0, len(orgs))
	for _, org := range orgs {
		out = append(out, o.describe(org))
	}
	return &out, nil
}

// Describes ONE org's Base — whether its store exists, and what it occupies.
//
// The org must be one the caller's token carries; any other is not found, so
// this cannot be used to learn which orgs exist. That check is the same
// membership set the listing is built from, which is why the two can never
// disagree about what a caller may see.
//
// Response: {"org":"hanzo","exists":true,"bytes":430080}
func (o baseOps) read(ctx context.Context, in *baseRef) (*baseView, error) {
	org := strings.TrimSpace(in.Org)
	if mounted == nil {
		return nil, zip.ErrNotFound("no such base")
	}
	for _, held := range principal.OrgsFrom(ctx) {
		if held == org {
			v := o.describe(org)
			return &v, nil
		}
	}
	return nil, zip.ErrNotFound("no such base")
}

// describe reports one org's Base from the pool's own directory rule, so the
// answer names the store the hosting lane would open rather than a second
// derivation of where it lives.
func (o baseOps) describe(org string) baseView {
	v := baseView{Org: org}
	s := mounted
	if s == nil || s.pool == nil {
		return v
	}
	dir := filepath.Join(s.pool.dir, goja.TenantSegment(org))
	fi, err := os.Stat(filepath.Join(dir, "data.db"))
	if err != nil {
		return v
	}
	v.Exists, v.Bytes = true, fi.Size()
	return v
}
