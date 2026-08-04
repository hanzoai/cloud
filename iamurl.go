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
	if base := iamService(); base != "" {
		return base
	}
	return strings.TrimRight(strings.TrimSpace(publicIssuer), "/")
}

// IAMIssuer is the PUBLIC identity host this deployment presents (hanzo.id).
// One name for one fact: the issuer stamped into a token and the issuer a
// validator checks are the same string, so they are read in one place.
func IAMIssuer() string { return strings.TrimSpace(os.Getenv("CLOUD_IAM_ISSUER")) }

// IAMBase is IAMBaseURL against this deployment's OWN issuer — what a caller
// with no Config value in hand uses. It exists so "which IAM do I call" has one
// answer whether or not the caller happens to be holding a Config: three callers
// resolved it themselves and grew three different fallbacks (the public issuer,
// nothing at all, and a hardcoded cluster address), which is the disagreement
// this file was written to prevent and did not.
func IAMBase() string { return IAMBaseURL(IAMIssuer()) }

// IAMExternal reports whether this deployment NAMES a separate IAM, as opposed
// to being the IAM itself (the embedded subsystem).
//
// It is a different question from IAMBaseURL — "is there another IAM" versus
// "what is its address" — and it is here because answering it separately is how
// the two disagree: a deployment with only a public issuer resolves a non-empty
// base yet names no external IAM, and a caller that inferred one from the other
// reached for a store that is not there.
func IAMExternal() bool { return iamService() != "" }

// iamService is the in-cluster IAM address, the ONE env read behind all three.
func iamService() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("IAM_URL")), "/")
}
