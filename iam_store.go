// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package cloud

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/orm"

	"github.com/hanzoai/cloud/plane"
	iamstore "github.com/hanzoai/iam/pkg/store"
)

// Reading a key out of the identity store — the ONE implementation, and the seam
// that lets the process holding the store hand it over.
//
// An API key has to become a principal in EVERY process, because the identity
// boundary runs in every process: each app is its own composition root and calls
// cloud.Serve, which installs it. The store, meanwhile, has one writer and it is
// the iam app. So the question travels and the store does not.
//
// It used to travel over HTTP to iam.hanzo.svc — a request that left the pod,
// crossed the cluster network and came back to a sibling container, carrying a
// confidential client credential so the caller could authenticate to its own
// deployment. Everything around that hop was scaffolding for it: a 5-second
// timeout on the hot auth path, JSON envelopes re-decoding rows this fleet already
// owns, and an init() that panicked when iam.hanzo.svc was unreachable, taking
// api.hanzo.ai down over a dependency the pod contains.
//
// Now there is one rule — READ THE STORE WHERE IT LIVES — with the transport
// following from where that is:
//
//   - the iam app publishes the store to this package when it mounts, so a process
//     that HAS the store reads it directly, with no hop at all;
//   - every other process asks the iam app over the internal plane, a unix socket
//     in this pod's own runtime dir (plane.IAMResolveKey / plane.IAMResolveOrg).
//
// Both paths run the SAME two functions below, so the two transports cannot answer
// differently — the only thing that varies is how far the question travels.

var (
	iamStoreMu sync.RWMutex
	iamStoreDB orm.DB
)

// SetIAMStore publishes the embedded IAM's store to this package. apps/iam.Mount
// calls it once, after the store opens and before any route is served. Passing nil
// is how a subsystem that failed closed says so.
func SetIAMStore(db orm.DB) {
	iamStoreMu.Lock()
	defer iamStoreMu.Unlock()
	iamStoreDB = db
}

// iamStore returns the embedded IAM's store, or nil when this process does not hold
// it — which is the ordinary case for every app that is not iam.
func iamStore() orm.DB {
	iamStoreMu.RLock()
	defer iamStoreMu.RUnlock()
	return iamStoreDB
}

// PrincipalFromStore resolves a SECRET key (hk-/sk-) to the four facts a JWT for
// that user carries, so one minting path serves a key and a session identically.
//
// A key that resolves to nobody is Owner == "", never an error: an unknown
// credential is an ANSWER, and the caller's response to it — stay anonymous — is
// settled. Returning an error instead would make an unknown key indistinguishable
// from an unreachable store, and those call for opposite responses.
//
// Refusal says WHY, for the surface that has to explain it to a person.
// iamstore.Reason reads "" for a real store fault, so an infrastructure failure is
// never reported as a bad credential.
//
// A pk- resolves to nobody here. That is the store's decision, on every caller
// (UserByAccessKey refuses a publishable key), and it is what keeps a key shipped
// in a browser bundle from ever becoming a read grant.
func PrincipalFromStore(ctx context.Context, db orm.DB, key string) *plane.KeyPrincipal {
	u, err := iamstore.UserByAccessKey(ctx, db, strings.TrimSpace(key))
	if err != nil || u == nil {
		return &plane.KeyPrincipal{Refusal: string(iamstore.Reason(err))}
	}
	owner := strings.TrimSpace(u.Owner)
	if owner == "" {
		return &plane.KeyPrincipal{Refusal: string(iamstore.Reason(err))}
	}
	return &plane.KeyPrincipal{
		Owner:   owner,
		Name:    strings.TrimSpace(u.Name),
		Email:   strings.TrimSpace(u.Email),
		IsAdmin: u.IsAdmin,
	}
}

// OrgFromStore resolves a PUBLISHABLE key (pk-) to the org that holds it, and
// deliberately to nothing else — there is no principal on this path at all, which
// is the property that makes a browser key safe to ship.
//
// Fail-closed on every non-resolution (unknown, secret-scoped, expired): "" rather
// than a default tenant, so a bad browser key can never write into another org's
// partition.
func OrgFromStore(ctx context.Context, db orm.DB, key string) string {
	k, err := iamstore.PublishableKeyByAccessKey(ctx, db, strings.TrimSpace(key), time.Now())
	if err != nil || k == nil {
		return ""
	}
	return strings.TrimSpace(k.Owner)
}

// askPrincipal and askOrg are the same two questions, asked of the process that
// holds the store (Ask is the client half — plane.go).
//
// A failure resolves NOTHING rather than failing the request: an unresolvable key
// is anonymous, which is what an unconfigured resolver has always meant, and a bad
// key has never granted trust. That holds for a fleet with no iam app (ErrNoPeer)
// and for an iam app that is down — in both, the honest answer is "not resolved",
// and the caller's response to it is identical.
func askPrincipal(ctx context.Context, key string) *plane.KeyPrincipal {
	out, err := Ask[plane.KeyRef, plane.KeyPrincipal](ctx, iamApp, plane.IAMResolveKey, &plane.KeyRef{Key: key})
	if err != nil || out == nil {
		return nil
	}
	return out
}

func askOrg(ctx context.Context, key string) string {
	out, err := Ask[plane.KeyRef, plane.KeyOrg](ctx, iamApp, plane.IAMResolveOrg, &plane.KeyRef{Key: key})
	if err != nil || out == nil {
		return ""
	}
	return strings.TrimSpace(out.Owner)
}

// iamApp is the plane name of the app that owns the identity store. It is the same
// string manifest.Apps registers, and naming it once keeps a typo from becoming a
// silent de-authentication of every API key in the fleet.
const iamApp = "iam"
