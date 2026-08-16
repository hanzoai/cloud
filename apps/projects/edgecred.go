package projects

import (
	"context"
	"os"
	"strings"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/integrations"
	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/apps/sites/cloudflare"
)

// edgeOrg is the org whose Cloudflare connection fronts the SITE PLANE. It is our
// own tenancy, not a customer's: the CDN in front of <slug>.hanzo.app is Hanzo's
// infrastructure, and a customer's connection governs a customer's zones through
// /v1/cloudflare and /v1/dns. Reading a tenant's token to purge our own edge
// would be the two concerns collapsing into one credential.
const edgeOrg = "hanzo"

// newEdge resolves the edge's credential from the INTEGRATION first and the
// environment second.
//
// The estate already holds a Cloudflare token: the integrations plane verifies
// one live against /user/tokens/verify and seals it per org at
// /orgs/{org}/integrations/cloudflare/api_token. The edge, meanwhile, read
// CF_API_TOKEN from the process environment — a second, unrelated store for one
// vendor, and nothing populated it, so every purge was a no-op while the
// integration sat connected. Two stores for one credential is how one of them
// ends up empty with everyone believing the other is the one that counts.
//
// So the integration is the source, and the environment is the fallback rather
// than the rule. The fallback stays for two honest reasons: a deployment that
// runs the site plane WITHOUT the integrations app still needs an edge, and a
// break-glass override should not require a connection flow.
//
// ZONE IS STILL ENVIRONMENT. The integration stores account id and label, never a
// zone id — a token spans an account and the site plane purges ONE zone, so the
// zone is a fact about this deployment rather than about the connection. It could
// be discovered (Zone:Read is in scope, GET /zones?name= would answer it), and
// that is the better end state; it is not done here because a lookup at
// construction turns a pure constructor into one that can block and fail.
//
// FAIL SOFT, ALWAYS. Every failure below returns an edge rather than an error:
// unmounted integrations, no connection, a KMS refusal. A missing purge credential
// degrades a publish to stale-until-TTL, which is survivable; a deploy path that
// refuses to start because a CDN token is unreadable is not.
func newEdge(ctx context.Context, log luxlog.Logger) sites.Edge {
	zone := strings.TrimSpace(os.Getenv("CF_ZONE_ID"))

	if tok, err := integrations.TokenFor(ctx, edgeOrg, "cloudflare", "api_token"); err == nil {
		if t := strings.TrimSpace(string(tok)); t != "" {
			log.Info("edge credential from the cloudflare integration", "org", edgeOrg, "zone", zone != "")
			return cloudflare.With(t, zone, log)
		}
	}

	// The environment, then — and say which it was, because "the purge does
	// nothing" is the one failure that leaves no other trace.
	env := strings.TrimSpace(os.Getenv("CF_API_TOKEN"))
	if env == "" {
		log.Warn("edge has no credential: the cloudflare integration is not readable and CF_API_TOKEN is unset; publishes are live only after the edge TTL")
	}
	return cloudflare.With(env, zone, log)
}
