// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"crypto/subtle"
	"strings"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/environ"
	"github.com/zap-proto/zip"
)

// Who a money READ is for, where the two facts it reads already live.
//
// This was exported from apps/account and billing and commerce imported that app
// to reach it. Neither function touches account: one compares a bearer to an
// environment value, the other composes that with [principal.Org]. The root
// already imports principal, so the import edge bought an indirection and nothing
// else.
//
// It is NOT a call to another subsystem and must not become one. Both answers are
// derived from the request in hand — a header and a validated claim — so asking a
// peer would be a network round trip to re-read what the caller already sent.

// IsServiceToken reports whether the request bears the trusted service credential.
//
// Constant-time, and false whenever the credential is unset, so a deployment that
// configures none admits nobody by this route rather than everybody.
func IsServiceToken(c *zip.Ctx) bool {
	token := strings.TrimSpace(environ.Or("COMMERCE_SERVICE_TOKEN", ""))
	if token == "" {
		return false
	}
	bearer := strings.TrimSpace(strings.TrimPrefix(c.Header("Authorization"), "Bearer "))
	return bearer != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(token)) == 1
}

// ReaderOrg answers which tenant a money read is scoped to: the validated
// principal's org, or — for a trusted service — the org it names in X-Org-Id.
//
// ONE rule, in one place, because it drifted twice and both drifts were outages.
// Every app is its own process, so an in-process reader hook cannot reach ai; it
// asks bearing the service credential and no session, and a handler consulting
// only the validated principal can answer nothing but "who are you".
// /v1/billing/balance learned that, /v1/billing/tier did not, and neither did the
// plane op behind it — one host, one token, three answers.
//
// It widens nothing. A validated principal still wins, and a service is believed
// about the org only because it holds a credential no customer has.
func ReaderOrg(c *zip.Ctx) (string, bool) {
	if org, ok := principal.Org(c); ok {
		return org, true
	}
	if IsServiceToken(c) {
		if org := strings.TrimSpace(c.Org()); org != "" {
			return org, true
		}
	}
	return "", false
}
