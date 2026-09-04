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

package admission

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	iampeer "github.com/hanzoai/cloud/client/iam"
	"github.com/zap-proto/zip"
)

// approvalStatusPending is the ONE value that gates a user. This mirrors IAM's
// approval semantics — approval is FAIL-OPEN: a user is approved unless
// approvalStatus is EXACTLY "pending" (absent / "approved" / "rejected" all read
// approved). Only "pending" holds a user on the waitlist. The literal lives HERE,
// with the gate that acts on it, rather than in the identity store: iam answers
// what it recorded, admission decides what that means, and two places can never
// disagree about who is on a waitlist.
const approvalStatusPending = "pending"

// approvedHeader is the FORWARD-PERFECT path: once IAM carries approvalStatus in
// the token and the gateway mints it as a validated header (the same trust model
// as X-User-IsAdmin), the enforcement points read approval for FREE with no IAM
// round-trip. Until then the resolver falls back to asking iam over the plane.
// Values: "true" (approved) / "false" (pending). Any other value → fall through.
const approvedHeader = "X-User-Approved"

// accountLookup fetches the CALLER'S approvalStatus from iam. Injected so the
// resolver is unit-testable without a live peer. It returns (status, ok): ok=false
// on any failure, which the resolver treats FAIL-OPEN (approved) — the documented
// guard behavior, availability over a hard gate when iam is unreachable.
//
// It takes only a context, and that is the change: it used to take the caller's
// Cookie and Authorization header, because HTTP gave it no way to say who was
// asking and REPLAYING the caller's own credential to iam was the way to make iam
// answer about them. The plane carries the validated principal, so the credential
// is no longer handled here — or anywhere between here and the store.
type accountLookup func(ctx context.Context) (status string, ok bool)

// Approvals resolves whether the current caller is off the waitlist. It is the ONE
// approval predicate the native middleware uses, DRY with the @file waitlist-guard
// (both read approvalStatus == "pending"). Resolution order:
//
//  1. global admin (c.IsAdmin())            → approved (admins are never gated)
//  2. validated header X-User-Approved      → its bit (forward-perfect, no lookup)
//  3. the iam peer (client.IAMApproval)      → approved unless approvalStatus=="pending"
//     — cached per user for ttl; FAIL-OPEN on any error.
type Approvals struct {
	lookup accountLookup
	ttl    time.Duration

	mu    sync.Mutex
	cache map[string]approvalEntry
}

type approvalEntry struct {
	approved bool
	at       time.Time
}

// NewApprovals builds a resolver over the iam peer; ttl bounds the per-user cache.
//
// It takes no address. iam is reached by NAME over its own socket, so there is no
// base URL for a deployment to supply and no way for one to be wrong — the
// zero-iamBase case this used to carry (a resolver that always failed open
// because nobody had configured a URL) is not expressible any more.
func NewApprovals(ttl time.Duration) *Approvals {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Approvals{lookup: planeApproval, ttl: ttl, cache: map[string]approvalEntry{}}
}

// newApprovalsWithLookup is the test client: a resolver over an injected lookup.
func newApprovalsWithLookup(lookup accountLookup, ttl time.Duration) *Approvals {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Approvals{lookup: lookup, ttl: ttl, cache: map[string]approvalEntry{}}
}

// Approved reports whether the caller is off the waitlist.
func (a *Approvals) Approved(c *zip.Ctx) bool {
	// (1) Global admins are ALWAYS approved.
	if c.IsAdmin() {
		return true
	}
	// (2) Forward-perfect validated header — no round-trip when present.
	switch strings.ToLower(strings.TrimSpace(c.Header(approvedHeader))) {
	case "true", "1", "approved":
		return true
	case "false", "0", "pending":
		return false
	}
	// (3) Ask iam, cached per user, fail-open on error.
	user := strings.TrimSpace(c.User())
	if user == "" {
		// No validated principal — an unauthenticated caller. The middleware
		// resolves login separately; treat as not-approved so an anonymous
		// request to a gated host is bounced (never allowed through as approved).
		return false
	}
	if a.lookup == nil {
		return true // no lookup wired → fail-open (approval enforced elsewhere)
	}
	if e, ok := a.get(user); ok {
		return e.approved
	}
	// As(c, "") delegates THIS request's principal unchanged — the caller's own
	// authority, and nothing more. It is what replaces forwarding the raw bearer:
	// iam learns who is asking from the assertion the gateway already minted.
	status, ok := a.lookup(cloud.As(c, ""))
	if !ok {
		// iam unreachable → FAIL-OPEN (approved). Do NOT cache a fail-open so the
		// next request re-probes and a recovered iam re-gates promptly.
		return true
	}
	approved := strings.TrimSpace(strings.ToLower(status)) != approvalStatusPending
	a.put(user, approved)
	return approved
}

func (a *Approvals) get(user string) (approvalEntry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.cache[user]
	if !ok || time.Since(e.at) > a.ttl {
		return approvalEntry{}, false
	}
	return e, true
}

func (a *Approvals) put(user string, approved bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache[user] = approvalEntry{approved: approved, at: time.Now()}
}

// planeApproval is the real lookup: one ZAP call to iam, by name.
//
// Every failure is ok=false and the resolver fails open — an absent iam, a
// refused call, an unknown subject. That is unchanged behavior, deliberately: the
// gate's availability rule is a property of the gate, and moving the transport
// underneath it must not quietly turn a fail-open into a fail-closed.
func planeApproval(ctx context.Context) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, approvalTimeout)
	defer cancel()
	out, err := iampeer.IAMApproval(ctx)
	if err != nil || out == nil {
		return "", false
	}
	return out.Status, true
}

// approvalTimeout bounds the lookup. It sits in front of a request, so a slow iam
// must resolve to the fail-open decision rather than holding the caller.
const approvalTimeout = 8 * time.Second
