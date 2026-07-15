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

package featuregate

import (
	"net/http"
	"strings"

	"github.com/zap-proto/zip"
)

// Enforce is the NATIVE, forward-perfect enforcement point for the waitlist — the
// single in-binary middleware that reads the registry (in-process, no HTTP hop)
// and the caller's approval, and applies THE RULE for every request whose Host is
// a governed service. As product hosts fold into the one-binary cloud, this is the
// ONE enforcement point (the @file waitlist-guard is the interim gate for hosts
// not yet cloud-fronted; both read the SAME registry so a toggle governs both).
//
// THE RULE (per request, on a governed host in waitlist mode):
//
//	carries a Hanzo API key (hk-/sk-/…)   → allow (paid inference; possession-gated)
//	exempt path (health/iam/waitlist)     → allow
//	unauthenticated                       → 302 waitlist (browser) / 401 (API)
//	waitlist mode OFF (or host un-governed) → allow (c.Next)
//	waitlist mode ON  AND approved         → allow (c.Next)
//	waitlist mode ON  AND NOT approved     → 302 waitlist (browser) / 403 (API)
//
// INTEGRATION POINT — wire in serve.go RIGHT AFTER SanitizeIdentity:
//
//	app.Use(IdentityMiddleware(cfg))          // establishes the validated principal
//	app.Use(featuregate.Enforce(featuregate.EnforceConfig{ WaitlistURL: … }))  // ← here
//
// It reads the sanitized X-User-Id / X-User-IsAdmin / X-User-Approved that
// IdentityMiddleware minted, so it MUST run after it and (like BillingGate) before
// the subsystem handlers. It is deliberately NOT wired here — the unified-binary
// agent owns serve.go's boot chain; this package exposes Enforce + the store so the
// one-line app.Use lands without a merge collision. The store is resolved lazily
// (moduleStore()) so Enforce can be constructed before Mount runs.
//
// WHY NATIVE IS CANONICAL (in-cluster-bypass). The @file edge guard only gates
// traffic arriving THROUGH the ingress — a pod reaching another service's pod
// directly in-cluster bypasses it (a cluster-wide baseline NetworkPolicy + Cilium
// broad-allow union means additive netpols can't seal that). For a waitlist (threat
// model = external users) edge-only is acceptable, but this native middleware is
// FORWARD-PERFECT: when the app IS cloud, the gate is IN the request path, so
// reaching the pod directly STILL hits it — there is no edge to go around. That is
// the reason the native middleware is the canonical enforcement and the @file guard
// is purely interim. It is also STATELESS (it reads the sanitized X-User-* headers,
// sets no cookie), so the multi-apex cookie-domain concern the @file guard must
// handle does not exist here at all.
//
// Paths that must NEVER be gated (health, the waitlist page's own API, auth
// callbacks) are skipped via ExemptPrefixes so enforcement can't lock the platform
// out of its own recovery/observability surface.
type EnforceConfig struct {
	// WaitlistURL is where an unapproved / unauthenticated browser is bounced
	// (per-brand, e.g. https://waitlist.hanzo.ai). Empty → API-style 403/401 for
	// everyone (no redirect target), so enforcement still holds.
	WaitlistURL string

	// Approvals resolves whether the caller is off the waitlist. When nil, Enforce
	// builds one from IAMBase.
	Approvals *Approvals

	// IAMBase is the in-cluster IAM base used to build Approvals when it is nil.
	IAMBase string

	// ExemptPrefixes are request-path prefixes never gated (health/metrics/auth).
	// A sensible default set is used when empty.
	ExemptPrefixes []string
}

// defaultExemptPrefixes are the paths enforcement must never touch — HIP-0106
// health, the auth/OIDC handshake, and the waitlist join API itself (so a gated
// user can still submit the waitlist form).
var defaultExemptPrefixes = []string{
	"/v1/featuregate/", // the mode read + the health route
	"/v1/iam/",         // auth / OIDC / approval-status / get-account handshake
	"/v1/waitlist",     // the waitlist join API (a gated user must reach it)
	"/health",
	"/healthz",
	"/__guard/", // the @file guard's own callback surface (defense in depth)
}

// Enforce builds the native enforcement middleware. It is a no-op passthrough when
// no registry store is resolved yet (moduleStore() nil, i.e. Mount hasn't run) —
// so a request before boot completes is never wrongly gated.
func Enforce(cfg EnforceConfig) zip.Handler {
	approvals := cfg.Approvals
	if approvals == nil {
		approvals = NewApprovals(cfg.IAMBase, 0)
	}
	exempt := cfg.ExemptPrefixes
	if len(exempt) == 0 {
		exempt = defaultExemptPrefixes
	}
	waitlistURL := strings.TrimRight(strings.TrimSpace(cfg.WaitlistURL), "/")

	return func(c *zip.Ctx) error {
		store := moduleStore()
		if store == nil {
			return c.Next() // registry not mounted yet — never gate pre-boot
		}
		path := c.Path()
		for _, p := range exempt {
			if strings.HasPrefix(path, p) {
				return c.Next()
			}
		}

		// MONEY-CRITICAL EXEMPTION: a request bearing a Hanzo API KEY (hk-/sk-/pk-/…)
		// is NEVER waitlist-gated. Paid inference on api.hanzo.ai authenticates by KEY
		// POSSESSION + is metered downstream in the `ai` subsystem — SanitizeIdentity
		// does NOT mint a session principal for an API key (auth_identity.go isAPIKey →
		// validatedPrincipal returns nil), so an API-key request arrives here with an
		// empty c.User(); without this exemption THE RULE would misclassify it as
		// "unauthenticated API → 401" and break every paid inference call. The gate is
		// redundant anyway: an API key is minted ONLY in the (gated) console, so an
		// unapproved user can never obtain one. Session/JWT access to a gated host is
		// still gated normally — only key possession is exempt.
		if carriesAPIKey(c) {
			return c.Next()
		}

		host := c.Fiber().Hostname()
		mode, _, known, err := store.ModeForHost(c.Context(), host)
		if err != nil || !known || !mode {
			// Un-governed host, mode OFF, or a registry read error → allow. A
			// governed host is opened by flipping mode OFF; an unknown host is
			// not ours to gate at the shared cloud edge (fail-open on a read
			// error keeps the API available — the guard is the belt-and-braces
			// gate for the hosts that must stay closed).
			return c.Next()
		}

		// Governed host in waitlist mode. Approved (incl. admins) pass; everyone
		// else is bounced.
		if approvals.Approved(c) {
			return c.Next()
		}
		return bounce(c, waitlistURL)
	}
}

// bounce renders the not-approved verdict: a browser navigation is redirected to
// the waitlist (302); an API client gets a 403 (401 when there is no principal at
// all). Content-negotiated so a fetch/XHR never eats an opaque HTML redirect.
func bounce(c *zip.Ctx, waitlistURL string) error {
	if isAPIClient(c) || waitlistURL == "" {
		if strings.TrimSpace(c.User()) == "" {
			return c.JSON(http.StatusUnauthorized, map[string]any{
				"error": "authentication required — sign in to continue",
			})
		}
		return c.JSON(http.StatusForbidden, map[string]any{
			"error": "account pending approval — join the waitlist",
		})
	}
	c.SetHeader("Location", waitlistURL)
	return c.NoContent(http.StatusFound)
}

// apiKeyPrefixes are the Hanzo API-key prefixes. This MIRRORS cloud
// auth_identity.go isAPIKey (the ONE authority) — kept local so featuregate stays
// self-contained (no cloud-internal import) while agreeing on the exact contract:
// a token with one of these prefixes is a possession-gated API key, not a session
// principal. If cloud adds a prefix there, add it here.
var apiKeyPrefixes = []string{"hk-", "sk-", "pk-", "fw_", "hz_"}

// carriesAPIKey reports whether the request authenticates with a Hanzo API key —
// in the Authorization header (Bearer or Basic-username) or the common api-key /
// x-api-key headers. GENEROUS by design: over-exempting only skips the redundant
// waitlist gate (key validation + billing still run downstream); under-exempting
// would break paid inference. So any recognized key signal exempts the request.
func carriesAPIKey(c *zip.Ctx) bool {
	auth := strings.TrimSpace(c.Header("Authorization"))
	if tok, ok := cut(auth, "Bearer "); ok && hasAPIKeyPrefix(tok) {
		return true
	}
	// Basic auth carries the key as the username (key:) — OpenAI-compat clients do
	// this. The raw base64 is not decoded here; instead accept the header-form keys
	// callers commonly send, which is where inference SDKs put the key.
	if hasAPIKeyPrefix(strings.TrimSpace(c.Header("api-key"))) ||
		hasAPIKeyPrefix(strings.TrimSpace(c.Header("x-api-key"))) {
		return true
	}
	return false
}

func hasAPIKeyPrefix(tok string) bool {
	for _, p := range apiKeyPrefixes {
		if strings.HasPrefix(tok, p) {
			return true
		}
	}
	return false
}

// cut splits s on the first occurrence of prefix at the START (case-insensitive on
// the scheme word), returning the remainder.
func cut(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return strings.TrimSpace(s[len(prefix):]), true
	}
	return "", false
}

// isAPIClient reports whether the caller is a non-browser API client, so we fail
// closed with a status code instead of an interactive redirect. A Bearer/Basic
// Authorization OR an Accept without text/html is an API call; a browser
// navigation sends Accept: text/html.
func isAPIClient(c *zip.Ctx) bool {
	if auth := strings.TrimSpace(c.Header("Authorization")); auth != "" {
		return true
	}
	accept := c.Header("Accept")
	if accept != "" && !strings.Contains(accept, "text/html") {
		return true
	}
	return false
}

// ── package-level store resolution (lazy, for Enforce constructed before Mount) ──

// moduleStore returns the registry store once Mount has opened it, else nil. The
// Enforce closure calls it PER REQUEST, so serve.go can construct Enforce before
// MountAll runs (the store is set during Mount, well before the first request).
func moduleStore() *Store {
	moduleMu.RLock()
	defer moduleMu.RUnlock()
	return moduleStoreRef
}
