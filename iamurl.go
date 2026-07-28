package cloud

import (
	"os"
	"strings"
)

// IAMBaseURL resolves the IAM base URL a SERVER-SIDE caller inside the cluster
// should use — the split-horizon policy, stated once.
//
// The public issuer host (e.g. https://hanzo.id) is fronted by Cloudflare, which
// 403s a server-side loopback POST with edge error 1006 — so an in-cluster
// exchange against the public issuer fails and whatever depended on it (the KMS
// login broker, AI M2M minting, per-tenant identity provisioning) silently stays
// down (root-caused 2026-07-04: in-cluster POST to https://hanzo.id/... → 403,
// while http://iam.hanzo.svc/... → 200).
//
// Precedence: the in-cluster IAM service base (IAM_URL — already wired for
// JWKS), then the public issuer as a last resort (single-process / no
// split-horizon deploys). Returns "" only when no IAM is resolvable at all.
// This existed as three inlined copies (ai M2M, the KMS login broker, and the
// per-tenant identity provisioner would have been the fourth); a copy of a
// policy does not disagree until one is edited, so there is exactly one now.
// Endpoint-specific overrides (CLOUD_AI_IAM_TOKEN_URL, CLOUD_KMS_IAM_TOKEN_URL)
// stay with their endpoints — they override a URL, not this policy.
func IAMBaseURL(publicIssuer string) string {
	if base := strings.TrimRight(strings.TrimSpace(os.Getenv("IAM_URL")), "/"); base != "" {
		return base
	}
	return strings.TrimRight(strings.TrimSpace(publicIssuer), "/")
}
