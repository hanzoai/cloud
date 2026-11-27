// Copyright © 2026 Hanzo AI. MIT License.

package settings

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// The platform's own configuration of a product, published on the internal plane.
//
// A deployment knob used to be an environment variable, which means a value nobody
// can see and a rollout to change. This is the same engine every tenant's product
// config already goes through — the reserved platform org is just another (org,
// product) key — so an operator edits it at admin.hanzo.ai and the next request
// reads it. No second store, no second surface, no restart.

// exposeFleet publishes the platform's configuration read and its write. Mount
// calls it.
func exposeFleet(s *service) {
	o := settingsOps{s: s}
	cloud.Plane().Post("/settings/fleet",
		o.fleetConfig,
		zip.WithOperationID(client.SettingsFleet),
		zip.WithSummary("The platform's own configuration of a product"))
	cloud.Plane().Post("/settings/fleet/set",
		o.fleetSet,
		zip.WithOperationID(client.SettingsFleetSet),
		zip.WithSummary("Replace the platform's own configuration of a product"))
	cloud.Plane().Post("/settings/org",
		o.orgConfig,
		zip.WithOperationID(client.SettingsOrg),
		zip.WithSummary("The caller organization's non-secret configuration of a product"))
	cloud.Plane().Post("/settings/org/set",
		o.orgSet,
		zip.WithOperationID(client.SettingsOrgSet),
		zip.WithSummary("Update the caller organization's non-secret configuration and KMS secret-key index for a product"))
}

// orgSet updates the CALLER ORG's non-secret configuration document and KMS
// secret-key index for one product.
//
// Like orgConfig, the tenant is resolved strictly from cloud.Tenant(ctx) and
// NEVER from an In field. Secret values never cross the plane here: callers seal
// secret values directly into KMS at orgs/{org}/settings/{product}/{key} and
// pass only the key names to index alongside the non-secret JSON document.
func (o settingsOps) orgSet(ctx context.Context, in *client.OrgSettingsSet) (*client.Configured, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("settings: tenant required")
	}
	product, err := requireProduct(in.Product)
	if err != nil {
		return nil, err
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(in.Config), &config); err != nil || config == nil {
		return nil, zip.ErrBadRequest("settings: config must be a JSON object")
	}
	cfgJSON, mErr := json.Marshal(config)
	if mErr != nil {
		return nil, zip.ErrBadRequest("settings: config must be JSON-serializable")
	}
	if len(cfgJSON) > maxConfig {
		return nil, zip.ErrBadRequest("settings: config too large (max 64KiB)")
	}

	var secretKeys []string
	if prev, gErr := o.s.store.Get(ctx, org, product); gErr == nil {
		secretKeys = prev.SecretKeys
	} else if !errors.Is(gErr, errNotFound) {
		return nil, zip.Errorf(http.StatusInternalServerError, "load settings: %v", gErr)
	}
	for _, key := range in.SecretKeys {
		if !productRE.MatchString(key) {
			return nil, zip.ErrBadRequest("secret key must match ^[a-z0-9][a-z0-9._-]{0,62}$")
		}
		secretKeys = addStr(secretKeys, key)
	}
	if len(secretKeys) > maxSecretKeys {
		return nil, zip.ErrBadRequest("too many secret fields (max 64)")
	}

	st, pErr := o.s.store.Put(ctx, Settings{
		Org:        org,
		Product:    product,
		Config:     string(cfgJSON),
		SecretKeys: secretKeys,
		UpdatedAt:  time.Now().Unix(),
	})
	if pErr != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist settings: %v", pErr)
	}
	return &client.Configured{Config: st.Config}, nil
}

// orgConfig answers the CALLER ORG's non-secret configuration of one product.
//
// The org is resolved strictly from cloud.Tenant(ctx) — from outside, the
// validated principal; on the internal plane, the caller stated in-process — and
// NEVER from an In field. Only the non-secret JSON document is returned; secret
// values remain sealed in KMS under orgs/{org}/settings/{product}/{key}.
func (o settingsOps) orgConfig(ctx context.Context, in *client.Product) (*client.Configured, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("settings: tenant required")
	}
	product, err := requireProduct(in.Product)
	if err != nil {
		return nil, err
	}
	st, err := o.s.store.Get(ctx, org, product)
	if errors.Is(err, errNotFound) {
		return &client.Configured{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &client.Configured{Config: st.Config}, nil
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
func (o settingsOps) fleetConfig(ctx context.Context, in *client.Product) (*client.Configured, error) {
	product, err := requireProduct(in.Product)
	if err != nil {
		return nil, err
	}
	st, err := o.s.store.Get(ctx, authz.AdminOrg, product)
	if errors.Is(err, errNotFound) {
		return &client.Configured{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &client.Configured{Config: st.Config}, nil
}

// fleetSet replaces the platform's own configuration of one product with the
// document given, and answers it as stored.
//
// It is the write half of fleetConfig, for the app that owns a product's knobs and
// validates them: that app serves the SuperAdmin surface (websearch's PUT
// /v1/websearch/engines) and hands the checked document here, because this
// process is the one that holds the store. A read and its write are one
// conversion; without this the only way to change a platform knob was a store
// another process opened.
//
// SuperAdmin only, as the caller states it. The plane carries the caller's
// headers and verifies none of them, so this check catches an app writing
// platform configuration with no SuperAdmin behind the call — a bug worth
// failing on — rather than repelling a compromised sibling.
//
// The change is on the audit trail before it is made, as settings.fleet.set with
// the stored document before and the new one after; a trail that does not take it
// refuses the change (503) and nothing moves. The document must be a JSON object
// within the 64 KiB config bound, and secrets are never here: a credential a
// product reads lives in KMS under its own path.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o settingsOps) fleetSet(ctx context.Context, in *client.FleetConfig) (*client.Configured, error) {
	who := cloud.Who(ctx)
	if !who.Admin {
		return nil, zip.ErrForbidden("settings: SuperAdmin required")
	}
	product, err := requireProduct(in.Product)
	if err != nil {
		return nil, err
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(in.Config), &config); err != nil || config == nil {
		return nil, zip.ErrBadRequest("settings: config must be a JSON object")
	}
	if o.s.trail == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the change, so it was not made")
	}
	var before json.RawMessage
	switch st, err := o.s.store.Get(ctx, authz.AdminOrg, product); {
	case err == nil:
		before = audit.Redact(json.RawMessage(st.Config))
	case !errors.Is(err, errNotFound):
		return nil, zip.Errorf(http.StatusInternalServerError, "load settings: %v", err)
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: cmp.Or(who.Owner, who.Org), Sub: who.User, Email: who.Email},
		Action:    "settings.fleet.set",
		Resource:  audit.Resource{Type: "settings", ID: product},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: true},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		Before:    before,
		After:     audit.Redact(json.RawMessage(in.Config)),
	}
	if _, err := o.s.trail.Append(ctx, rec); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the change, so it was not made: %v", err)
	}
	v, err := o.s.write(ctx, authz.AdminOrg, product, config, nil)
	if err != nil {
		status := http.StatusInternalServerError
		var he *zip.HTTPError
		if errors.As(err, &he) {
			status = he.Status
		}
		rec.Outcome = audit.Outcome{Result: "error", Status: status, Reason: err.Error()}
		_, _ = o.s.trail.Append(ctx, rec)
		return nil, err
	}
	return &client.Configured{Config: string(v.Config)}, nil
}
