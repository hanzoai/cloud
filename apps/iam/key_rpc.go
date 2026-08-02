// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/zap-proto/zip"
)

// Key resolution, published on the internal plane.
//
// The identity store has one writer and it is this process. Every OTHER process
// still has to turn an API key into the principal it authenticates, because the
// identity boundary runs in each of them — cloud.Serve installs it, and each app
// is its own composition root that calls cloud.Serve. So the question travels and
// the store does not.
//
// It travelled over HTTP to iam.hanzo.svc before this: a request that left the
// pod, crossed the cluster network and came back to a sibling container, carrying
// a confidential client credential so the caller could authenticate to its own
// deployment. On the plane it is a unix socket in this pod's runtime dir, so there
// is no credential to hold, no service to be down, and no JSON envelope around
// rows this fleet already owns.
//
// TWO OPS, because there are two questions and their answers must never be
// interchangeable — see plane.IAMResolveKey / plane.IAMResolveOrg.

// exposeKeys publishes both key doors. Mount calls it.
func exposeKeys() {
	zip.Post[plane.KeyRef, plane.KeyPrincipal](cloud.Plane(), "/iam/resolve-key",
		resolveKey,
		zip.WithOperationID(plane.IAMResolveKey),
		zip.WithSummary("Resolve a secret API key to the principal it authenticates"))

	zip.Post[plane.KeyRef, plane.KeyOrg](cloud.Plane(), "/iam/resolve-org",
		resolveOrg,
		zip.WithOperationID(plane.IAMResolveOrg),
		zip.WithSummary("Resolve a publishable API key to the org that holds it"))
}

// resolveKey answers which user a SECRET key (hk-/sk-) speaks for.
//
// It takes no caller org — it cannot. This op runs BEFORE a principal exists, on
// the way to establishing one, so there is no authenticated tenancy to key on. The
// key IS the subject, and the org it resolves to is the answer rather than an
// argument, which is the property that keeps a caller from naming a tenant it does
// not hold a credential for.
//
// A key that resolves to nobody is Owner == "" and a nil error. That is not
// leniency: an unknown credential is a fact, the caller's response to it is to stay
// anonymous, and returning an error instead would make an unknown key
// indistinguishable from an unreachable store — which is exactly the distinction
// the caller needs, because one is a bad key and the other is an outage.
//
// The refusal REASON rides along for the surface that must explain the failure to a
// person: iamstore.Reason reads "" for a real store fault, so an infrastructure
// failure is never reported as a bad credential.
//
// A pk- reaching here resolves to nobody. That is not this function's decision —
// UserByAccessKey refuses a publishable key at the store, on every caller — but it
// is worth saying out loud at the door, because the door is what a reader finds
// first: a public key never authenticates a read, anywhere.
func resolveKey(ctx context.Context, in *plane.KeyRef) (*plane.KeyPrincipal, error) {
	db := DB()
	if db == nil {
		// This process OWNS the store, so a nil handle is a boot-order fault here,
		// not an absence to shrug at. Answering "unresolved" would turn an outage
		// into a fleet-wide silent de-authentication of every API key.
		return nil, fmt.Errorf("resolve-key: identity store not open in the process that owns it")
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		return &plane.KeyPrincipal{}, nil
	}
	u, err := iamstore.UserByAccessKey(ctx, db, key)
	if err != nil || u == nil {
		return &plane.KeyPrincipal{Refusal: string(iamstore.Reason(err))}, nil
	}
	owner := strings.TrimSpace(u.Owner)
	if owner == "" {
		return &plane.KeyPrincipal{Refusal: string(iamstore.Reason(err))}, nil
	}
	return &plane.KeyPrincipal{
		Owner:   owner,
		Name:    strings.TrimSpace(u.Name),
		Email:   strings.TrimSpace(u.Email),
		IsAdmin: u.IsAdmin,
	}, nil
}

// resolveOrg answers which org a PUBLISHABLE key (pk-) belongs to, and nothing
// else. There is no principal on this path at all — not an empty one, not an
// optional one — which is what makes it safe for a key that ships in client JS.
//
// Fail-closed on every non-resolution (unknown, secret-scoped, expired): "" rather
// than a default tenant, so a bad browser key can never write into someone else's
// partition.
func resolveOrg(ctx context.Context, in *plane.KeyRef) (*plane.KeyOrg, error) {
	db := DB()
	if db == nil {
		return nil, fmt.Errorf("resolve-org: identity store not open in the process that owns it")
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		return &plane.KeyOrg{}, nil
	}
	k, err := iamstore.PublishableKeyByAccessKey(ctx, db, key, time.Now())
	if err != nil || k == nil {
		return &plane.KeyOrg{}, nil
	}
	return &plane.KeyOrg{Owner: strings.TrimSpace(k.Owner)}, nil
}
