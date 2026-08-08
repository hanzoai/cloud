// Copyright © 2026 Hanzo AI. MIT License.

package settings

import (
	"context"
	"errors"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// The platform's own configuration of a product, published on the internal plane.
//
// A deployment knob used to be an environment variable, which means a value nobody
// can see and a rollout to change. This is the same engine every tenant's product
// config already goes through — the reserved platform org is just another (org,
// product) key — so an operator edits it at admin.hanzo.ai and the next request
// reads it. No second store, no second surface, no restart.

// exposeFleet publishes the platform's configuration read. Mount calls it.
func exposeFleet(s *service) {
	o := settingsOps{s: s}
	zip.Post[plane.Product, plane.Configured](cloud.Plane(), "/settings/fleet",
		o.fleetConfig,
		zip.WithOperationID(plane.SettingsFleet),
		zip.WithSummary("The platform's own configuration of a product"))
}

// fleetConfig answers for the RESERVED PLATFORM ORG and takes no org argument.
//
// That is the whole of its safety. This store holds every tenant's product config,
// and the internal plane carries no principal — so an op with an org parameter
// would be a cross-tenant read available to any app in the pod. The org here is a
// constant, and the constant is authz.AdminOrg: the same predicate admin-guard and
// the audit trail read, so there is no second notion of "the platform" to drift.
//
// SECRETS ARE NOT HERE. Only the non-secret document is returned; a secret field's
// value lives in KMS and a caller that needs one asks KMS with its own identity.
//
// An unconfigured product answers with an empty document rather than an error,
// because a reader has to do the same thing in both cases — take its default — and
// a reader that must distinguish "never set" from "cannot ask" is a reader with two
// behaviours where one will do.
func (o settingsOps) fleetConfig(ctx context.Context, in *plane.Product) (*plane.Configured, error) {
	product, err := requireProduct(in.Product)
	if err != nil {
		return nil, err
	}
	st, err := o.s.store.Get(ctx, authz.AdminOrg, product)
	if errors.Is(err, errNotFound) {
		return &plane.Configured{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &plane.Configured{Config: st.Config}, nil
}
