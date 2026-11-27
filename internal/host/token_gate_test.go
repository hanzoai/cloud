package host_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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
	"apps/email/ses.go": "Amazon SES v2 request signing — AWS Signature Version 4, HMAC-SHA256 over the " +
		"canonical request under the org's own SES secret from KMS. It speaks Amazon's auth wire to " +
		"send one message and mints nothing a Hanzo surface accepts.",
	"apps/destination/x.go": "OAuth 1.0a request signing — HMAC-SHA1 over the " +
		"method+URL+params base string under consumerSecret&accessSecret, X's contract. It " +
		"signs an OUTBOUND call under credentials the tenant configured; it mints nothing " +
		"this deployment would honour.",
	"internal/iamtest/iamtest.go": "a REAL IAM issuer, for TESTS — a keypair, a JWKS endpoint and tokens " +
		"signed with it, so a suite drives the claim mapping rather than stubbing past it. Imported by " +
		"_test.go files alone and on no serving path; the walk sees it only because it is not itself a " +
		"_test.go file.",

	"sdk/tenure/field.go": "seals NON-IDENTITY values: a store's sealed columns (XChaCha20-Poly1305 " +
		"under a key derived from the data-plane master) and their blind index, a keyed HMAC of a sealed " +
		"value so a store can find a row by it without holding the value in the clear. Neither is a " +
		"credential nor accepted by any Hanzo surface.",
	"sdk/tenure/cipher.go": "seals a NON-IDENTITY value: the per-org snapshot envelope, AES-GCM under a " +
		"key derived from the data-plane master, with HMAC deriving that key rather than authenticating a " +
		"caller. This is the 'seals a non-identity value' case the refusal names.",

	"sdk/coord/coord.go": "derives one key per purpose from the data-plane master, HMAC as the " +
		"KDF exactly as cipher.go uses it: the key the live set is signed under and the seed of the " +
		"authority pods present to each other on the peer listener. Neither is a credential a caller " +
		"holds; a forwarded request is still admitted by its own IAM token and nothing else.",

	"sdk/coord/members.go": "seals a NON-IDENTITY value: a pod's record in the deployment's live " +
		"set (id, address, instance, the time it was written) carries an HMAC-SHA256 under a key derived " +
		"from the master, so a record planted in the KV names no member. It authenticates a membership " +
		"record, never a caller, and grants nothing a Hanzo surface accepts.",

	"sdk/object/sign.go": "seals a NON-IDENTITY value: a signed link into the object store on local disk " +
		"(sdk/object), an HMAC-SHA256 over the method, bucket, key, expiry and signed query under a key " +
		"minted beside the objects. It authorizes one read or write of one object until its expiry and " +
		"names no caller; it is the local engine's counterpart to the presigned URL the S3 driver returns, " +
		"and grants nothing any other Hanzo surface accepts.",

	"internal/authztest/authztest.go": "mints TEST tokens only: each process generates a throwaway " +
		"RSA key, serves its JWKS from an httptest server, and signs claims for the authorization " +
		"contract's callers (member, org admin, SuperAdmin). Only _test.go files import it, no binary " +
		"links it, and nothing outside the test trusts that issuer.",

	"internal/driver/ledger/bus.go": "seals a NON-IDENTITY value: each debit on the USAGE stream carries an " +
		"HMAC-SHA256 under a key derived from the KMS master, and Drain posts only debits a cloud " +
		"process wrote. It authenticates a ledger entry, never a caller, and grants nothing a Hanzo " +
		"surface accepts.",

	// The identity boundary itself. These four sat at the module root, which this
	// walk never read, and are named now that the package lives under internal/:
	// they VERIFY what IAM signed (and fingerprint what a caller presented); they
	// mint nothing.
	"sdk/middleware_identity.go": "the identity boundary — verifies an IAM-signed bearer (RS256 against the issuer JWKS) before any handler runs; mints nothing.",
	"sdk/confined.go":            "the identity boundary — verifies a delegated (confined) token against IAM's delegation key; mints nothing.",
	"sdk/delegation.go":          "the identity boundary — reads IAM's delegation key and verifies a delegated token's claims; mints nothing.",
	"internal/host/agency.go":    "the abuse sensor — a keyed per-process HMAC fingerprint of a presented credential, so a published fingerprint cannot be tested against a candidate key; grants nothing.",
	"idv/webhook.go": "provider webhook verification — HMAC-SHA256 over the raw body under the " +
		"provider's signing secret (sha256= scheme). Verifies THEIR signature; grants nothing here.",
	"apps/provider/signed.go": "webhook verification, for every provider that signs a delivery " +
		"the same way — GitHub's X-Hub-Signature-256, Linear-Signature, and the forge's X-Git- and " +
		"X-Gitea- spellings all being hex(HMAC_SHA256(secret, raw body)). One constant-time verifier " +
		"where there were three; each receiver now hands over the headers it reads and nothing else " +
		"about the wire. It verifies THEIR signature and mints nothing: a delivery names a repository, " +
		"an issue or a ref, and no Hanzo surface accepts anything this file produces.",
	"apps/social/ingest_postiz.go": "Postiz replication webhook verification — the path token " +
		"Postiz posts to /v1/social/ingest/postiz/:token, compared in constant time (hmac.Equal) with " +
		"the KMS secret social/postiz POSTIZ_INGEST_TOKEN. It verifies THEIR shared secret and mints " +
		"nothing: what a delivery buys is a mirrored post in the org its integration names, and no " +
		"Hanzo surface accepts anything this file produces.",
	"apps/provider/slack_verify.go": "Slack request verification — the v0 signing scheme over " +
		"timestamp+body, Slack's contract.",
	"apps/provider/whatsapp_events.go": "WhatsApp webhook verification — Meta's " +
		"X-Hub-Signature-256, an HMAC over the raw body under the app secret, their contract. It " +
		"verifies THEIR signature and mints nothing: what the delivery buys is a reply route, and no " +
		"Hanzo surface accepts anything this file produces.",
	"apps/provider/state.go": "OAuth state MAC — tamper-proofs the (org, nonce) binding across " +
		"the round trip so a callback cannot be bound to a foreign org. A CSRF seal, not a bearer.",
	"apps/provider/channel_state.go": "the shared signed-state primitive the channel and Slack " +
		"flows compose — the same CSRF seal as state.go.",
	"apps/provider/telegram_link.go": "Telegram login verification — HMAC under SHA256(bot " +
		"token), Telegram's published scheme.",
	"apps/knowledge/connectors.go": "connector OAuth state MAC — seals (org, provider, nonce, exp) " +
		"across the authorize round trip; the callback refuses a foreign or expired state.",
	"apps/marketing/suppress.go": "one-click-unsubscribe MAC over (org, channel, address) — an " +
		"unforgeable opt-out link, constant-time checked; opens no surface but the suppression it names.",
	"apps/marketing/clicks.go": "tracked-link MAC over (org, sequence, step, enrollment, link, " +
		"destination, send time) under the KMS key marketing/click-hmac — it binds one email link to " +
		"the one destination it redirects to, constant-time checked; it names no person to any Hanzo " +
		"surface and grants nothing but a 302 to that destination and a click count.",
	"apps/marketing/newsletter.go": "newsletter confirmation-link MAC over (time asked, nonce, brand, " +
		"address, source, locale) under the KMS key marketing/newsletter-hmac — it binds one link to the one " +
		"address it was mailed to, constant-time checked, and grants nothing but that address's place " +
		"on the brand's newsletter.",
	"apps/share/client.go": "deterministic per-org zrok credential derivation — HMAC as a KDF so " +
		"provisioning is stateless and collision-free; nothing is signed or verified.",
	"apps/webhook/dispatch.go": "outbound delivery signatures — signs what WE deliver " +
		"(Stripe-style t=,v1= over timestamp+body) so subscribers can verify us.",
	"apps/leaderboard/galaxy.go": "per-answer pseudonym — the first bytes of an HMAC of an org under a key " +
		"drawn at random for ONE answer and then dropped, so the ids of a galaxy agree within it and cannot " +
		"be matched across two. It is a keyed hash for naming stars and nothing else: never sent, never " +
		"verified, never stored, and it names no person to any Hanzo surface.",
	"apps/patrol/subsystem.go": "camera-stream ticket — MAC over (org, camera, expiry) under a " +
		"per-org key kept in KMS, so a stream address is watchable for a bounded time and for one " +
		"camera. It names no person and opens no Hanzo surface; a viewer still arrives at the " +
		"camera's own credential, which never leaves the deployment.",
	"apps/sandbox/autofill.go": "RFC 6238 / RFC 4226 TOTP 6-digit code computation — HMAC-SHA1 over " +
		"the 30-second counter under a customer's KMS-sealed TOTP seed (`orgs/{org}/settings/vault/*`), " +
		"typed into a foreign website login inside the sandbox X11 display via stdin. It speaks the " +
		"foreign website's 2FA wire and mints nothing a Hanzo surface accepts.",
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
	// EVERY package in the module, not only apps/. The claim is about the
	// REPOSITORY — "outside apps/iam, nothing mints a bearer" — and scanning one
	// directory was always narrower than that. Thirteen libraries moving up a
	// level made the gap visible: idv/webhook.go still verifies an HMAC and the
	// walk had stopped seeing it, so shrinking the ledger would have retired a pin
	// on live code.
	for name, ff := range moduleFiles(t) {
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

// moduleFiles is every Go file in the module, grouped by top-level directory, so a
// gate asking a question about the REPOSITORY is not answered by one directory.
func moduleFiles(t *testing.T) map[string][]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read module root: %v", err)
	}
	files := map[string][]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		if err := filepath.WalkDir(name, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				files[name] = append(files[name], path)
			}
			return nil
		}); err != nil {
			t.Fatalf("walk %s: %v", name, err)
		}
	}
	return files
}
