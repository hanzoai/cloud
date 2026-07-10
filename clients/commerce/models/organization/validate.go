// Copyright © 2026 Hanzo AI. MIT License.

package organization

import (
	"errors"
	"strings"
)

// ErrSecretLikeName is returned when an untrusted, gateway-supplied org name is
// actually a raw API key / bearer token rather than a real org identifier.
var ErrSecretLikeName = errors.New("organization: refusing to provision org from a bearer-shaped name")

// IsSecretLikeName reports whether an org identifier looks like a raw API key /
// bearer token rather than a real organization slug. Real orgs are short slugs
// ("hanzo", "adnexus"), UUIDs, or numeric ids; leaked provider keys carry a
// recognizable prefix — Anthropic (sk-ant-), OpenAI (sk-proj-), DigitalOcean
// (sk-do-), and Hanzo (sk-hz- / hk-).
//
// Any code that provisions an org via GetOrCreate from an untrusted,
// gateway-supplied name MUST reject these first: otherwise a caller who
// presents a raw key as their bearer causes the key to be persisted as an org
// name and tenant id, leaking the secret into the datastore (incident
// 2026-07-02). This mirrors the DB backstop trigger trg_reject_bearer_tenant on
// _entities.
//
// The check is intentionally prefix-based (matching the DB trigger) so it never
// rejects a real slug: "skunkworks" and "hkust" do not start with "sk-"/"hk-".
func IsSecretLikeName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(n, "sk-") || strings.HasPrefix(n, "hk-")
}
