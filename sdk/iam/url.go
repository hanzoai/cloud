package iam

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/brand"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/hanzoai/cloud/sdk/env"
)

// IAMBaseURL resolves the IAM base URL a SERVER-SIDE caller inside the cluster
// should use — the split-horizon policy, stated once.
//
// The public issuer host (e.g. https://hanzo.id) is fronted by Cloudflare, which
// 403s a server-side loopback POST with edge error 1006 — so an in-cluster
// exchange against the public issuer fails and whatever depended on it (the KMS
// login broker, AI M2M minting, per-tenant identity provisioning) silently stays
// down. Measured: an in-cluster POST to https://hanzo.id/... answers 403,
// http://iam.hanzo.svc/... 200.
//
// Precedence: the in-cluster IAM service base (IAM_URL — already wired for
// JWKS), then the public issuer as a last resort (single-process / no
// split-horizon deploys). Returns "" only when no IAM is resolvable at all.
// This existed as three inlined copies (ai M2M, the KMS login broker, and the
// per-tenant identity provisioner would have been the fourth); a copy of a
// policy does not disagree until one is edited, so there is exactly one now.
// Endpoint-specific overrides (CLOUD_AI_IAM_TOKEN_URL, CLOUD_KMS_IAM_TOKEN_URL)
// stay with their endpoints — they override a URL, not this policy.
func BaseURL(publicIssuer string) string {
	if base := iamService(); base != "" {
		return base
	}
	return strings.TrimRight(strings.TrimSpace(publicIssuer), "/")
}

// issuerFor is THE resolution of the deployment's OIDC issuer: an explicit pin
// wins, else the brand's canonical issuer from the registry. Both callers — this
// file's IAMIssuer() and LoadConfig (config.go) — go through it, so the process
// cannot hold two answers to "which IAM signs my tokens".
//
// It used to be two answers. LoadConfig applied the brand fallback and this file
// did not, so on any deployment that let the brand supply the issuer — the
// documented white-label path, and how lux/zoo/pars are meant to run —
// Config.IAMIssuer was "https://lux.id" while IAMIssuer() was "". Empty is not a
// harmless disagreement here: it flows to IAMBase(), which is what the sk- API
// key resolver dials, and auth_apikey.go treats an empty base as "unresolved
// key" and returns nil — which middleware_identity.go reads as ANONYMOUS. Every
// API-key request on the fleet would authenticate as nobody, with no error and
// no log line, while the JWT boundary kept working perfectly.
//
// brand.IssuerFor never returns empty (an unknown brand folds to hanzo), so that
// failure is now unreachable rather than merely unlikely.
func IssuerFor(pinned, brandID string) string { return brand.Issuer(pinned, brandID) }

// IAMIssuer is the PUBLIC identity host this deployment presents (hanzo.id for
// the Hanzo brand, lux.id for Lux, ...). One name for one fact: the issuer
// stamped into a token and the issuer a validator checks are the same string, so
// they are read in one place.
//
// For a caller holding a Config, cfg.IAMIssuer is the same value by
// construction — see issuerFor. This form exists for the callers that have no
// Config in hand.
func Issuer() string {
	return IssuerFor(os.Getenv("CLOUD_IAM_ISSUER"), env.Or("CLOUD_BRAND", brand.Default))
}

// IAMBase is IAMBaseURL against this deployment's OWN issuer — what a caller
// with no Config value in hand uses. It exists so "which IAM do I call" has one
// answer whether or not the caller happens to be holding a Config: three callers
// resolved it themselves and grew three different fallbacks (the public issuer,
// nothing at all, and a hardcoded cluster address), which is the disagreement
// this file was written to prevent and did not.
func Base() string { return BaseURL(Issuer()) }

// Machine is this deployment's OWN IAM identity as a token source: the
// client_credentials grant for IAM_CLIENT_ID / IAM_CLIENT_SECRET at IAMBase, the
// token cached, asked for again when half its life has passed (so a caller rarely
// waits on IAM for one) and each call bounded at 30s (tokensource.go). On the Hanzo
// deployment its subject is admin/hanzo-cloud. Nil when the deployment holds no
// identity, so a caller reads "unconfigured" rather than an empty bearer that
// looks like one.
func Machine() oauth2.TokenSource { return MachineFor("") }

// MachineFor is Machine minted FOR one service (RFC 8707 `resource`): the token's
// audience is that service, so it is the one place the token is accepted, and the
// service verifies the caller against IAM's keys without holding anything of
// ours. Empty is Machine's own audience. Nil when the deployment holds no
// identity.
func MachineFor(resource string) oauth2.TokenSource {
	id, secret, endpoint, ok := MachineClient()
	if !ok {
		return nil
	}
	cc := &clientcredentials.Config{
		ClientID:     id,
		ClientSecret: secret,
		TokenURL:     endpoint,
		// IAM reads the credential from the form body, not Basic auth.
		AuthStyle: oauth2.AuthStyleInParams,
	}
	if resource != "" {
		cc.EndpointParams = url.Values{"resource": {resource}}
	}
	return newAhead(cc.Token)
}

// machineClient is this deployment's IAM client credential and the token endpoint
// it is spent at — the one reading Machine and Delegate share. ok is false when
// the deployment holds no identity.
func MachineClient() (id, secret, endpoint string, ok bool) {
	id = env.Or("IAM_CLIENT_ID", "")
	secret = env.Or("IAM_CLIENT_SECRET", "")
	base := Base()
	if id == "" || secret == "" || base == "" {
		return "", "", "", false
	}
	return id, secret, base + "/v1/iam/oauth/token", true
}

// iamClient bounds every call this deployment makes to IAM's token endpoint.
var Client = &http.Client{Timeout: 30 * time.Second}

// IAMExternal reports whether this deployment NAMES a separate IAM, as opposed
// to being the IAM itself (the embedded subsystem).
//
// It is a different question from IAMBaseURL — "is there another IAM" versus
// "what is its address" — and it is here because answering it separately is how
// the two disagree: a deployment with only a public issuer resolves a non-empty
// base yet names no external IAM, and a caller that inferred one from the other
// reached for a store that is not there.
func External() bool { return iamService() != "" }

// iamService is the in-cluster IAM address, the ONE env read behind all three.
func iamService() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("IAM_URL")), "/")
}
