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
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zap-proto/zip"
)

// approvalStatusPending is the ONE value that gates a user. This mirrors IAM's
// object.ApprovalPending (hanzoai/iam object/user.go) and User.IsApproved() —
// approval is FAIL-OPEN: a user is approved unless properties.approvalStatus is
// EXACTLY "pending" (absent / "approved" / "rejected" all read approved via
// IsApproved). Only "pending" holds a user on the waitlist. Keeping the literal
// here (not importing IAM) keeps admission self-contained.
const approvalStatusPending = "pending"

// approvedHeader is the FORWARD-PERFECT path: once IAM carries approvalStatus in
// the token and the gateway mints it as a validated header (the same trust model
// as X-User-IsAdmin), the enforcement points read approval for FREE with no IAM
// round-trip. Until then the resolver falls back to an IAM get-account lookup.
// Values: "true" (approved) / "false" (pending). Any other value → fall through.
const approvedHeader = "X-User-Approved"

// accountLookup fetches a caller's approvalStatus by replaying the caller's own
// credentials to IAM get-account. Injected so the resolver is unit-testable
// without a live IAM. It returns (status, ok): ok=false on any IAM error, which
// the resolver treats FAIL-OPEN (approved) — the documented guard behavior
// (availability over a hard gate when IAM is unreachable).
type accountLookup func(ctx context.Context, cookie, auth string) (status string, ok bool)

// Approvals resolves whether the current caller is off the waitlist. It is the ONE
// approval predicate the native middleware uses, DRY with the @file waitlist-guard
// (both read properties.approvalStatus == "pending"). Resolution order:
//
//  1. global admin (c.IsAdmin())            → approved (admins are never gated)
//  2. validated header X-User-Approved      → its bit (forward-perfect, no lookup)
//  3. IAM get-account (caller's creds)       → approved unless approvalStatus=="pending"
//     — cached per user for ttl; FAIL-OPEN on any IAM error.
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

// NewApprovals builds a resolver. iamBase is the in-cluster IAM base
// (e.g. http://iam.hanzo.svc.cluster.local:8000); ttl bounds the per-user cache.
// A zero iamBase yields a resolver whose lookup always fails-open (approved) —
// safe for a deployment where approval is enforced elsewhere (the guard).
func NewApprovals(iamBase string, ttl time.Duration) *Approvals {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Approvals{
		lookup: httpAccountLookup(strings.TrimRight(iamBase, "/")),
		ttl:    ttl,
		cache:  map[string]approvalEntry{},
	}
}

// newApprovalsWithLookup is the test seam: a resolver over an injected lookup.
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
	// (2) Forward-perfect validated header — no IAM round-trip when present.
	switch strings.ToLower(strings.TrimSpace(c.Header(approvedHeader))) {
	case "true", "1", "approved":
		return true
	case "false", "0", "pending":
		return false
	}
	// (3) IAM get-account lookup, cached per user, fail-open on error.
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
	status, ok := a.lookup(c.Context(),
		c.Header("Cookie"), c.Header("Authorization"))
	if !ok {
		// IAM unreachable → FAIL-OPEN (approved). Do NOT cache a fail-open so the
		// next request re-probes and a recovered IAM re-gates promptly.
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

// httpAccountLookup builds the real IAM get-account lookup. It replays the
// caller's Cookie / Authorization to IAM and reads data.properties.approvalStatus
// (the field GetAccount returns via GetMaskedUser). Bounded read + timeout mirror
// the guard's iamGet. Any non-200 / decode error → ok=false (fail-open upstream).
func httpAccountLookup(iamBase string) accountLookup {
	if iamBase == "" {
		return func(context.Context, string, string) (string, bool) { return "", false }
	}
	url := iamBase + "/v1/iam/get-account"
	return func(ctx context.Context, cookie, auth string) (string, bool) {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", false
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", false
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return "", false
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return "", false
		}
		return approvalStatusFromAccount(body)
	}
}

// approvalStatusFromAccount extracts properties.approvalStatus from an IAM
// get-account response. The user object is at the top level or under `data`
// (the casibase { status, data } envelope). Returns ("", false) on an error
// envelope or a missing user (fail-open upstream). An ABSENT approvalStatus is
// returned as "" (ok=true) — which the resolver reads as approved (fail-open,
// matching IsApproved()).
func approvalStatusFromAccount(body []byte) (string, bool) {
	type acct struct {
		Owner      string            `json:"owner"`
		Properties map[string]string `json:"properties"`
	}
	var top struct {
		Status string `json:"status"`
		acct
		Data acct `json:"data"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return "", false
	}
	if top.Status == "error" {
		return "", false
	}
	a := top.acct
	if a.Owner == "" && top.Data.Owner != "" {
		a = top.Data
	}
	if a.Owner == "" {
		return "", false
	}
	if a.Properties == nil {
		return "", true // no properties → approved (fail-open)
	}
	return a.Properties["approvalStatus"], true
}
