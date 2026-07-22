// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hanzoai/base/core"
)

// ErrNotEmbedded is returned by EnsureSpace when the base embed is disabled
// (CLOUD_BASE_EMBED off). It is a fail-soft sentinel: a caller provisioning a
// project's data space treats it as "deferred", never as a failure — the space
// is re-ensured on the next call once the embed is on.
var ErrNotEmbedded = errors.New("base: embed disabled")

// SubmissionsCollection is the ONE collection every project's Base space carries
// for generic data collection — the form, forum, and data submissions a deployed
// site POSTs to /v1/base/collections/submissions/records. Anyone may CREATE (so
// an anonymous published page can submit out of the box); reads stay
// superuser-only (submissions are private by default).
const SubmissionsCollection = "submissions"

// EnsureSpace provisions an org's Base data space idempotently and in-process
// (no HTTP): it opens+migrates the org's per-org Base app so its SQLite exists,
// then ensures the default submissions collection exists. Safe to call
// repeatedly — an existing collection is left untouched. Returns ErrNotEmbedded
// when the embed is off so the caller can fail soft. This is the ONE entrypoint
// other subsystems (projects) use to wire a project's data space by default.
func EnsureSpace(_ context.Context, org string) error {
	s := mounted
	if s == nil || s.pool == nil {
		return ErrNotEmbedded
	}
	org = strings.TrimSpace(org)
	if org == "" {
		return fmt.Errorf("base: empty org")
	}
	app, err := s.pool.appFor(org)
	if err != nil {
		return fmt.Errorf("base: open org app: %w", err)
	}
	if _, err := app.FindCollectionByNameOrId(SubmissionsCollection); err == nil {
		return nil // already provisioned — idempotent
	}
	col := core.NewBaseCollection(SubmissionsCollection)
	// Empty create rule = public: anyone (incl. an anonymous deployed page) may
	// submit. List/View/Update/Delete rules stay nil (superuser-only), so
	// submissions are readable only by the space owner.
	open := ""
	col.CreateRule = &open
	col.Fields.Add(
		&core.TextField{Name: "form", Max: 128},         // which form/forum/namespace posted
		&core.JSONField{Name: "data", MaxSize: 1 << 20}, // the submission payload
		&core.AutodateField{Name: "created", OnCreate: true},
	)
	if err := app.Save(col); err != nil {
		return fmt.Errorf("base: create %q collection: %w", SubmissionsCollection, err)
	}
	return nil
}
