package cloud_test

import (
	"sort"
	"strings"
	"testing"
)

// ONE AUTHORITY. IAM mints; everything else asks.
//
// The estate's bearer surface is one issuer (hanzoai/iam) and one verification
// seam (hanzoai/authz/edge, which the root middleware speaks; an op reads the
// result through principal). A second authority never announces itself — it
// arrives as one import and two hundred lines of "just a session token", and
// it fails permissively where the real one fails closed: the root's deleted
// copy accepted a token naming one key on a signature from another, and turned
// every machine principal into a human. apps/team/token is the live example,
// pinned below as condemned, together with every file that still reads it.
//
// So the raw material is pinned. Outside apps/iam — the authority — no app
// file imports what a bearer is hand-rolled from (crypto/hmac, a JWT library)
// and no file joins the condemned package, except the entries below. Exactly
// two HMAC uses are legitimate in an app and every entry is one of them:
// speaking a FOREIGN service's own auth wire, or sealing a non-identity value
// against tampering. Neither mints anything a Hanzo surface accepts. Test
// files are out of scope — forging a bad token is how a verifier gets tested.
//
// If the flow has no IAM-issued credential to verify, that is a gap to name in
// hanzoai/iam, not a licence to mint one here.
var allowedTokenPrimitives = map[string]string{
	"apps/team/token/token.go": "CONDEMNED, not excused — the second bearer authority: hand-rolled " +
		"HS256 mint+verify under SERVER_SECRET for team sessions and workspaces. IAM alone mints; the " +
		"cutover deletes this package (the session lane reads the hanzo_iam_token the browser already " +
		"holds, the workspace grant moves behind the authority). Do not add readers — the entries below " +
		"are the complete set and it only shrinks.",
	"apps/team/account.go":     "reader of the condemned team token — dies with the cutover.",
	"apps/team/collabws.go":    "reader of the condemned team token — dies with the cutover.",
	"apps/team/transactor.go":  "reader of the condemned team token — dies with the cutover.",
	"apps/team/typed.go":       "reader of the condemned team token — dies with the cutover.",
	"apps/analytics/team.go":   "reader of the condemned team token — dies with the cutover.",
	"apps/meet/meet.go": "two halves: mints the LiveKit room-join token — the media server's own wire " +
		"contract, an HS256 JWT under the LiveKit key the server itself validates, granting no Hanzo " +
		"surface — and reads the condemned team token, which dies with the cutover.",
	"apps/wallets/safeclient.go": "speaks the mpc ring's CURRENT wire: the ring (iss=mpc.lux.network, " +
		"aud=mpc-api) accepts an HS256 bearer under a shared MPC_JWT_SECRET, so cloud signs what the " +
		"server demands. That authority contract is the ring's own debt — retiring it means the ring " +
		"verifying IAM tokens through authz/edge, a cross-repo cutover like team's.",
	"apps/idv/webhook.go": "provider webhook verification — HMAC-SHA256 over the raw body under the " +
		"provider's signing secret (sha256= scheme). Verifies THEIR signature; grants nothing here.",
	"apps/integrations/github_webhook.go": "GitHub webhook verification — X-Hub-Signature-256 over " +
		"the raw body, GitHub's contract.",
	"apps/integrations/slack_verify.go": "Slack request verification — the v0 signing scheme over " +
		"timestamp+body, Slack's contract.",
	"apps/integrations/state.go": "OAuth state MAC — tamper-proofs the (org, nonce) binding across " +
		"the round trip so a callback cannot be bound to a foreign org. A CSRF seal, not a bearer.",
	"apps/integrations/bridge_state.go": "the shared signed-state primitive the bridge and Slack " +
		"flows compose — the same CSRF seal as state.go.",
	"apps/integrations/telegram_link.go": "Telegram login verification — HMAC under SHA256(bot " +
		"token), Telegram's published scheme.",
	"apps/knowledge/connectors.go": "connector OAuth state MAC — seals (org, provider, nonce, exp) " +
		"across the authorize round trip; the callback refuses a foreign or expired state.",
	"apps/marketing/suppress.go": "one-click-unsubscribe MAC over (org, channel, address) — an " +
		"unforgeable opt-out link, constant-time checked; opens no surface but the suppression it names.",
	"apps/share/client.go": "deterministic per-org zrok credential derivation — HMAC as a KDF so " +
		"provisioning is stateless and collision-free; nothing is signed or verified.",
	"apps/venue/aws_sigv4.go": "AWS SigV4 request signing — the S3 wire protocol is an HMAC chain; " +
		"authenticates us TO the store.",
	"apps/webhooks/dispatch.go": "outbound delivery signatures — signs what WE deliver " +
		"(Stripe-style t=,v1= over timestamp+body) so subscribers can verify us.",
}

// tokenPrimitives is the material a bearer gets hand-rolled from, plus the
// condemned package itself so its reader set only shrinks.
var tokenPrimitives = []string{
	"crypto/hmac",
	"github.com/golang-jwt/jwt",
	"github.com/dgrijalva/jwt-go",
	"github.com/lestrrat-go/jwx",
	"github.com/go-jose/go-jose",
	"gopkg.in/square/go-jose",
	"github.com/hanzoai/cloud/apps/team/token",
}

func TestOnlyIAMMintsTokens(t *testing.T) {
	found := map[string]string{}
	for name, ff := range appFiles(t) {
		if name == "iam" {
			continue // the authority
		}
		for _, f := range ff {
			for _, imp := range fileImports(t, f) {
				for _, p := range tokenPrimitives {
					if imp == p || strings.HasPrefix(imp, p+"/") || strings.HasPrefix(imp, p+".") {
						found[f] = imp
					}
				}
			}
		}
	}

	for file, imp := range found {
		if _, ok := allowedTokenPrimitives[file]; !ok {
			t.Errorf("%s imports %s — an app is reaching for bearer material.\n"+
				"IAM alone mints. Verification is the root middleware speaking hanzoai/authz/edge, and an "+
				"op reads the result through principal — neither needs these imports. If this file speaks "+
				"a FOREIGN service's own auth wire, or seals a non-identity value against tampering, add "+
				"it to allowedTokenPrimitives naming that contract. If it mints or verifies anything a "+
				"Hanzo surface accepts, stop: the missing credential is a gap to name in hanzoai/iam.",
				file, imp)
		}
	}
	for file := range allowedTokenPrimitives {
		if _, ok := found[file]; !ok {
			t.Errorf("%s no longer touches a token primitive — remove it from allowedTokenPrimitives "+
				"so the pin keeps describing the code that exists.", file)
		}
	}

	if t.Failed() {
		var have []string
		for f, imp := range found {
			have = append(have, f+" ("+imp+")")
		}
		sort.Strings(have)
		t.Logf("primitive imports now: %s", strings.Join(have, ", "))
	}
}
