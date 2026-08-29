package cloud_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// ONE AUTHORITY. IAM mints; everything else asks.
//
// The estate's bearer surface is one issuer (hanzoai/iam) and one verification
// client (hanzoai/authz/edge, which the root middleware speaks; an op reads the
// result through principal). A second authority never announces itself — it
// arrives as one import and two hundred lines of "just a session token", and
// it fails permissively where the real one fails closed: the root's deleted
// copy accepted a token naming one key on a signature from another, and turned
// every machine principal into a human. apps/team/token is the live example,
// pinned below as condemned, together with every file that still reads it.
//
// So the raw material is pinned, and it is pinned TWO ways, because a file can
// depend on the second authority without naming it. Outside apps/iam — the
// authority — no app file imports what a bearer is hand-rolled from
// (crypto/hmac, a JWT library), no file joins the condemned package, and no file
// calls identity.hs256, the client the HS256 arm is reached through. The third
// selector is not symmetry: transactor.go imports nothing condemned and still
// cannot compile without the arm, so an import-only pin reported the reader set
// as one file when it was two, and the deletion this map exists to drive would
// have gone green while the data plane lost its only credential lane. Exactly
// two HMAC uses are legitimate in an app and every entry is one of them:
// speaking a FOREIGN service's own auth wire, or sealing a non-identity value
// against tampering. Neither mints anything a Hanzo surface accepts. Test
// files are out of scope — forging a bad token is how a verifier gets tested.
//
// If the flow has no IAM-issued credential to verify, that is a gap to name in
// hanzoai/iam, not a licence to mint one here.
var allowedTokenPrimitives = map[string]string{
	"apps/team/token/token.go": "CONDEMNED, not excused — the second bearer authority: hand-rolled " +
		"HS256 mint+verify under SERVER_SECRET for team sessions and spaces. IAM alone mints; the " +
		"cutover deletes this package (the session lane reads the hanzo_iam_token the browser already " +
		"holds, the space grant moves behind the authority). Do not add readers — the entries below " +
		"are the complete set and it only shrinks.",
	"apps/team/account.go": "where the HS256 arm is DEFINED (identity.hs256), and one of its two callers. " +
		"identity.who resolves an IAM access token first and falls back to that decode, so every other team " +
		"surface resolves a caller and touches no algorithm. collabws, typed and the files plane's helpers " +
		"left the reader set for good when the client landed — they authorize against the membership rows and " +
		"work on either lane. The transactor only stopped IMPORTING; it still calls the arm, and it is the " +
		"deletion's real blocker (see its entry). Deleting the arm here is the LAST step, not the first: it " +
		"waits on login minting IAM-only and on front/love/analytics-collector verifying IAM.\n\n" +
		"IT GATES AUDIENCE, and the divergence is deliberate. THIS file's verification is the boundary's " +
		"(cloud.NewTokenValidator), which does not gate `aud` — correctly, for an API endpoint: a signature from " +
		"a trusted issuer already proves IAM minted the token for one of its own apps, and the app-registry " +
		"mirror that once checked which was deleted for drifting. A SESSION endpoint is a different question. " +
		"This lane turns a bearer into a signed-in person, and a token the user obtained for another app is " +
		"not consent to that, so team narrows to a NAMED audience set at the resource server rather than at " +
		"the edge (identity.forThisDeployment; shape pinned by TestSessionAudienceIsNamedNotPatterned, " +
		"behaviour by TestIAMLaneRefusesAForeignAudience). A second session-issuing surface owes the same " +
		"gate — verification says IAM minted it, never that it was minted for you.",
	"apps/team/transactor.go": "THE SECOND READER, reached through the client rather than an import — it calls " +
		"identity.hs256 on the credential in its path segment — and it is what actually blocks the deletion. " +
		"Cut the arm and this is the one file left that will not compile.\n\n" +
		"IT HAS NO IAM LANE BY DECISION. A browser can put a credential on a WebSocket in two places. The URL " +
		"is where the HS256 space token already sits, survivable only because that token names one " +
		"space for twelve hours; an estate-wide IAM bearer in a path that proxies and access logs record " +
		"is not. The cookie is worse here than anywhere else: a WebSocket is EXEMPT FROM CORS, so a foreign " +
		"page may open one and read every frame, leaving an Origin check as the only access control. The lane " +
		"that works is the sibling socket's — upgrade first, take the credential in an in-band Auth frame, " +
		"authorize the space the client NAMES through admit — and the client that must send that frame is " +
		"the team front (hanzoai/team, live at team.hanzo.ai), a different repo. So this entry does not shrink " +
		"from inside cloud, and neither does the one above it.",
	"apps/event/team.go": "reader of the condemned team token — the ingest trust order already resolves " +
		"a validated IAM bearer ahead of it (eventTenant step 1), so this arm dies with the cutover and " +
		"needs no IAM lane of its own.",
	"apps/meet/meet.go": "ONE half now: MINTS, under the media server's own wire contract — an HS256 JWT " +
		"under the LiveKit key that server itself validates, granting no Hanzo surface. TWO tokens come " +
		"off the one signer and they are deliberately different grants: the browser's join token " +
		"(roomJoin into one room) and the credential this binary presents to LiveKit's own Egress API " +
		"(roomRecord, apps/meet/egress.go, which imports no primitive of its own). Neither can be " +
		"replayed as the other, because neither grant type can express the other's field. It no longer " +
		"READS anything: the second arm, which decoded the condemned team token and let a space " +
		"claim be its own authorization, is gone. A caller arrives with an IAM identity or is refused, " +
		"and what that identity may do is asked of the process that owns the membership rows.",
	"apps/wallet/safeclient.go": "speaks the mpc ring's CURRENT wire: the ring (iss=mpc.lux.network, " +
		"aud=mpc-api) accepts an HS256 bearer under a shared MPC_JWT_SECRET, so cloud signs what the " +
		"server demands. That authority contract is the ring's own debt — retiring it means the ring " +
		"verifying IAM tokens through authz/edge, a cross-repo cutover like team's.",
	"apps/destination/x.go": "OAuth 1.0a request signing — HMAC-SHA1 over the " +
		"method+URL+params base string under consumerSecret&accessSecret, X's contract. It " +
		"signs an OUTBOUND call under credentials the tenant configured; it mints nothing " +
		"this deployment would honour.",
	"apps/idv/webhook.go": "provider webhook verification — HMAC-SHA256 over the raw body under the " +
		"provider's signing secret (sha256= scheme). Verifies THEIR signature; grants nothing here.",
	"apps/integrations/github_webhook.go": "GitHub webhook verification — X-Hub-Signature-256 over " +
		"the raw body, GitHub's contract.",
	"apps/platform/hook.go": "forge webhook verification — the same shape one host over, under the " +
		"secret configured on git.hanzo.ai's system webhook (X-Git-/X-Gitea-/X-Hub-Signature-256 over " +
		"the raw body). It verifies THEIR signature and mints nothing: the delivery names a repository " +
		"and a ref, and no Hanzo surface accepts anything this file produces.",
	"apps/integrations/slack_verify.go": "Slack request verification — the v0 signing scheme over " +
		"timestamp+body, Slack's contract.",
	"apps/integrations/whatsapp_events.go": "WhatsApp webhook verification — Meta's " +
		"X-Hub-Signature-256, an HMAC over the raw body under the app secret, their contract. It " +
		"verifies THEIR signature and mints nothing: what the delivery buys is a reply route, and no " +
		"Hanzo surface accepts anything this file produces.",
	"apps/integrations/state.go": "OAuth state MAC — tamper-proofs the (org, nonce) binding across " +
		"the round trip so a callback cannot be bound to a foreign org. A CSRF seal, not a bearer.",
	"apps/integrations/channel_state.go": "the shared signed-state primitive the channel and Slack " +
		"flows compose — the same CSRF seal as state.go.",
	"apps/integrations/telegram_link.go": "Telegram login verification — HMAC under SHA256(bot " +
		"token), Telegram's published scheme.",
	"apps/knowledge/connectors.go": "connector OAuth state MAC — seals (org, provider, nonce, exp) " +
		"across the authorize round trip; the callback refuses a foreign or expired state.",
	"apps/marketing/suppress.go": "one-click-unsubscribe MAC over (org, channel, address) — an " +
		"unforgeable opt-out link, constant-time checked; opens no surface but the suppression it names.",
	"apps/share/client.go": "deterministic per-org zrok credential derivation — HMAC as a KDF so " +
		"provisioning is stateless and collision-free; nothing is signed or verified.",
	"apps/webhook/dispatch.go": "outbound delivery signatures — signs what WE deliver " +
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

// callsHS256 reports whether path CALLS identity.hs256 — the HS256 arm reached
// through the client. A caller imports nothing condemned, so this is the only way
// the pin sees it; the definition alone does not count, or every file would.
func callsHS256(t *testing.T, path string) bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Errorf("parse %s: %v", path, err)
		return false
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "hs256" {
			found = true
		}
		return true
	})
	return found
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
			// The client, checked second so a file that also imports keeps the
			// sharper label. This is what makes the reader set complete.
			if _, ok := found[f]; !ok && callsHS256(t, f) {
				found[f] = "identity.hs256 (the client, not an import)"
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
