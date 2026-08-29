package team

import (
	"context"
	"github.com/hanzoai/cloud/internal/planetest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/team/token"
	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/plane"
)

// The IAM subject the fake validator answers for, and the account it must resolve
// to. accountID is the join establishSession stores rows under, so a lane that
// resolved anything else would address an account that flow never created.
const (
	iamSub      = "11111111-2222-4333-8444-555555555555"
	iamOtherSub = "99999999-2222-4333-8444-555555555555"
)

// openTestStore opens an isolated account store for one test.
func openTestStore(t *testing.T) *accountStore {
	t.Helper()
	planetest.ServeIdentity(t)
	s, err := openAccountStore(t.TempDir())
	if err != nil {
		t.Fatalf("openAccountStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// testIdent is the client a test drives, with NO IAM validator: the HS256 arm alone,
// which is what the services that never see an IAM token are built with.
func testIdent(accounts *accountStore) *identity {
	return &identity{secret: testSecret, accounts: accounts}
}

// identFor is the client with both lanes live, verifying against a REAL issuer: the
// tokens are signed, the JWKS is fetched, and the claim mapping under test is
// cloud's own. Nothing between a signed token and a caller is faked.
func identFor(t *testing.T, accounts *accountStore) (*identity, *iamtest.Issuer0) {
	t.Helper()
	iam := iamtest.New(t)
	verify := cloud.NewTokenValidator(iamtest.Issuer).Validate
	return &identity{
		verify: verify, secret: testSecret, accounts: accounts,
		audience: map[string]bool{iamtest.Audience: true},
	}, iam
}

// orgsOf is the signed membership set naming org as HOME — the first entry, which
// is the tenant rule the estate states once in idClaims.homeOrg.
func orgsOf(org string) []map[string]any {
	return []map[string]any{{"org": org, "role": "admin"}}
}

// homeIn is the ordinary token: this subject, at home in this org.
func homeIn(org, sub string) iamtest.Claims {
	return iamtest.Claims{Sub: sub, Owner: org, Orgs: orgsOf(org)}
}

// enrolled creates the account rows a login creates, and returns the account id
// the store will answer for that subject. The IAM lane resolves an account only
// when a login already made one — so a test about resolution has to enrol first.
func enrolled(t *testing.T, store *accountStore, org, subject, name string) string {
	t.Helper()
	if _, err := store.EnsureSpace(context.Background(), org, accountID(subject), name); err != nil {
		t.Fatalf("enrol %s in %s: %v", subject, org, err)
	}
	return accountID(subject)
}

// withReq drives one request carrying whichever credentials the case is about and
// runs fn against its live context. The context is pooled and recycled the moment
// the handler returns, so the assertion has to happen inside it.
func withReq(t *testing.T, bearerTok, iamCookie, acctCookie string, fn func(*zip.Ctx)) {
	t.Helper()
	app := zip.New(zip.Config{})
	ran := false
	app.Get("/probe", func(c *zip.Ctx) error {
		ran = true
		fn(c)
		return c.String(http.StatusOK, "ok")
	})
	r := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if bearerTok != "" {
		r.Header.Set("Authorization", "Bearer "+bearerTok)
	}
	if iamCookie != "" {
		r.AddCookie(&http.Cookie{Name: iamTokenCookie, Value: iamCookie})
	}
	if acctCookie != "" {
		r.AddCookie(&http.Cookie{Name: authCookie, Value: acctCookie})
	}
	if _, err := app.Test(r); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !ran {
		t.Fatal("probe handler never ran")
	}
}

// whoOn resolves a caller off a request carrying those credentials.
func whoOn(t *testing.T, id *identity, bearerTok, iamCookie, acctCookie string) (cl caller, err error) {
	t.Helper()
	withReq(t, bearerTok, iamCookie, acctCookie, func(c *zip.Ctx) { cl, err = id.who(c) })
	return cl, err
}

// hsToken mints an HS256 token exactly as this service does.
func hsToken(t *testing.T, account, space, org string) string {
	t.Helper()
	tok, err := token.Generate(account, space, map[string]any{"org": org}, expUnix(sessionTokenTTL), testSecret)
	if err != nil {
		t.Fatalf("token.Generate: %v", err)
	}
	return tok
}

// TestIAMLaneResolvesTheAccount proves the IAM lane addresses the account the
// OAuth callback's join creates, resolved through the STORE, with the tenant taken
// from the verified owner claim and NEVER from anything the caller wrote, on both
// carriers. The token is really signed and really verified.
func TestIAMLaneResolvesTheAccount(t *testing.T) {
	store := openTestStore(t)
	id, iam := identFor(t, store)
	want := enrolled(t, store, "acme", iamSub, "Ada")
	raw := iam.Sign(t, iamtest.Claims{
		Sub: iamSub, PreferredUsername: "ada", Owner: "acme",
		Orgs: []map[string]any{{"org": "acme", "role": "admin"}, {"org": "beta", "role": "member"}},
	})

	for name, carrier := range map[string][2]string{
		"bearer": {raw, ""},
		"cookie": {"", raw},
	} {
		cl, err := whoOn(t, id, carrier[0], carrier[1], "")
		if err != nil {
			t.Fatalf("%s: who: %v", name, err)
		}
		if !cl.iam {
			t.Fatalf("%s: resolved on the HS256 arm, want the IAM lane", name)
		}
		if cl.account != want {
			t.Fatalf("%s: account = %q, want the enrolled account %q", name, cl.account, want)
		}
		if cl.org != "acme" {
			t.Fatalf("%s: org = %q, want the verified owner", name, cl.org)
		}
		if cl.user != "acme/ada" {
			t.Fatalf("%s: user = %q, want <owner>/<name>", name, cl.user)
		}
		// The IAM lane pins NO space: what it may touch is decided per request
		// against the rows, never by a claim it carries.
		if cl.space != "" {
			t.Fatalf("%s: space = %q, want the IAM lane to pin none", name, cl.space)
		}
		// Home-safe: the verified membership set plus the home tenant.
		if len(cl.orgs) != 2 || cl.orgs[0].Org != "acme" || cl.orgs[1].Org != "beta" {
			t.Fatalf("%s: orgs = %v, want [acme beta]", name, cl.orgs)
		}
		// An IAM credential NEVER leaves the client: raw is empty on this lane, so no
		// echo site can hand a platform bearer back to page JS.
		if cl.raw != "" {
			t.Fatalf("%s: caller.raw carries the IAM credential", name)
		}
	}
}

// TestIAMLaneKeysOnTheSubjectNotTheUsername is the F2 regression, and it is the
// reason these tests sign real tokens.
//
// VerifiedIdentity.User falls back sub → preferred_username → name, and accountID
// returns a UUID-shaped input VERBATIM. So a token with NO `sub` whose
// preferred_username is a colleague's account uuid used to resolve to that
// colleague — and admit() then granted every space the two share. Nothing
// about that token is forged: IAM signs it, the issuer is trusted, the signature
// verifies. Only the claim the lane READS decides who it is.
//
// The attacker needs a token IAM will mint with no sub and a chosen username, so
// this is a privilege escalation gated on an IAM-side condition rather than an open
// one — which is exactly the kind that survives review by being called impossible.
func TestIAMLaneKeysOnTheSubjectNotTheUsername(t *testing.T) {
	store := openTestStore(t)
	id, iam := identFor(t, store)
	victim := enrolled(t, store, "acme", iamSub, "Ada")

	// The victim's ACCOUNT UUID worn as a username, on a token carrying no subject.
	attack := iam.Sign(t, iamtest.Claims{PreferredUsername: victim, Name: victim, Owner: "acme", Orgs: orgsOf("acme")})

	// The canonical user id really does resolve to the victim — the fallback fires,
	// so this test observes the actual hazard rather than assuming it away.
	v, err := id.verify(attack)
	if err != nil {
		t.Fatalf("the attack token did not verify, so this test proves nothing: %v", err)
	}
	if v.User != victim {
		t.Fatalf("setup: User = %q, want the fallback to resolve it to the victim %q", v.User, victim)
	}
	if v.Subject != "" {
		t.Fatalf("setup: Subject = %q, want no subject on this token", v.Subject)
	}

	// And the lane refuses it outright rather than resolving it to that account.
	cl, err := id.iam(context.Background(), attack)
	if err == nil {
		t.Fatalf("SECURITY: a token with no subject resolved to account %q — the victim's", cl.account)
	}
	if _, err := whoOn(t, id, attack, "", ""); err == nil {
		t.Fatal("SECURITY: the client admitted a subject-less token")
	}
	// The same token, now WITH its own subject, is a different person entirely and
	// resolves to no account here — so the refusal above is about the missing
	// subject, not about the token being unusable in general.
	own := iam.Sign(t, iamtest.Claims{Sub: iamOtherSub, PreferredUsername: victim, Owner: "acme", Orgs: orgsOf("acme")})
	if _, err := id.iam(context.Background(), own); err == nil {
		t.Fatal("SECURITY: a subject with no account row resolved to one anyway")
	}
	// And the victim's own token still works, so the gate discriminates.
	good := iam.Sign(t, homeIn("acme", iamSub))
	cl, err = id.iam(context.Background(), good)
	if err != nil || cl.account != victim {
		t.Fatalf("the victim's own token resolved (%+v, %v)", cl, err)
	}
}

// TestIAMLaneRefusesAnIDToken is the F4 regression. IAM's signer emits the same
// claim set into the access token and the id_token but for aud/tokenType/nonce, so
// signature and issuer cannot tell them apart — and the id_token is the one handed
// to a browser SPA to read. A session credential must be the access token.
func TestIAMLaneRefusesAnIDToken(t *testing.T) {
	store := openTestStore(t)
	id, iam := identFor(t, store)
	enrolled(t, store, "acme", iamSub, "Ada")

	idToken := iam.Sign(t, iamtest.Claims{Sub: iamSub, Owner: "acme", Orgs: orgsOf("acme"), TokenType: "id-token"})
	if _, err := id.iam(context.Background(), idToken); err == nil {
		t.Fatal("SECURITY: an id_token was accepted as a team session credential")
	}
	if _, err := whoOn(t, id, idToken, "", ""); err == nil {
		t.Fatal("SECURITY: the client admitted an id_token")
	}
	// An access token is admitted, and so is a token minted before IAM emitted the
	// claim at all — the one permissive branch, which must not sign existing users
	// out at deploy.
	for _, tt := range []string{"access-token", "-"} {
		if _, err := id.iam(context.Background(), iam.Sign(t, iamtest.Claims{Sub: iamSub, Owner: "acme", Orgs: orgsOf("acme"), TokenType: tt})); err != nil {
			t.Fatalf("tokenType %q was refused: %v", tt, err)
		}
	}
}

// TestIAMLaneRefusesWhatDoesNotVerify proves every failure of the IAM lane is a
// refusal, not a downgrade to a partially-trusted caller: a forged signature, an
// expired token, a verified one with no tenant to scope to, and one naming no
// subject. Each is a real signed token, so each failure is the real code path.
func TestIAMLaneRefusesWhatDoesNotVerify(t *testing.T) {
	store := openTestStore(t)
	id, iam := identFor(t, store)
	enrolled(t, store, "acme", iamSub, "Ada")

	// A DIFFERENT issuer, publishing a key this validator never fetches. It reuses
	// the same kid on purpose: the token names a key the validator does have, and
	// still fails, so the refusal is the signature check and not a missing key.
	other := iamtest.New(t)
	bad := map[string]string{
		"forged":      other.Sign(t, homeIn("acme", iamSub)),
		"expired":     iam.Sign(t, iamtest.Claims{Sub: iamSub, Owner: "acme", Orgs: orgsOf("acme"), Exp: time.Now().Add(-time.Hour)}),
		"no home org": iam.Sign(t, iamtest.Claims{Sub: iamSub, Owner: "acme"}),
		"no subject":  iam.Sign(t, iamtest.Claims{Owner: "acme", Orgs: orgsOf("acme")}),
		"garbage":     "not.a.token",
	}
	for name, raw := range bad {
		if _, err := id.iam(context.Background(), raw); err == nil {
			t.Fatalf("iam(%s) admitted a caller it must refuse", name)
		}
		// And through the whole client, with no HS256 credential to fall back to.
		if _, err := whoOn(t, id, raw, "", ""); err == nil {
			t.Fatalf("who(bearer=%s) admitted a caller it must refuse", name)
		}
	}
	if _, err := whoOn(t, id, iam.Sign(t, homeIn("acme", iamSub)), "", ""); err != nil {
		t.Fatalf("who(valid IAM bearer): %v", err)
	}
}

// TestIAMLaneSpaceIsMembership proves the IAM lane grants a space ONLY
// from the rows: a member is admitted, a non-member and a foreign tenant's
// space are refused, and the two refusals are indistinguishable.
func TestIAMLaneSpaceIsMembership(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	member := accountID(iamSub)
	stranger := accountID(iamOtherSub)
	ws, err := store.EnsureSpace(ctx, "acme", member, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	// A space in ANOTHER tenant, which the caller is a member of THERE.
	other, err := store.EnsureSpace(ctx, "rival", member, "Ada")
	if err != nil {
		t.Fatal(err)
	}

	id, iam := identFor(t, store)
	// The stranger holds an account in the SAME org (they logged in) but no row in
	// this space — the case membership has to answer, not existence.
	if _, err := store.EnsureSpace(ctx, "acme", stranger, "Bob"); err != nil {
		t.Fatal(err)
	}
	memberCl, err := id.iam(ctx, iam.Sign(t, homeIn("acme", iamSub)))
	if err != nil {
		t.Fatal(err)
	}
	strangerCl, err := id.iam(ctx, iam.Sign(t, homeIn("acme", iamOtherSub)))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := id.admit(ctx, memberCl, ws.UUID); err != nil {
		t.Fatalf("admit(member, own space): %v", err)
	}
	if _, err := id.admit(ctx, strangerCl, ws.UUID); err == nil {
		t.Fatalf("admit(non-member) was granted — membership is the authorization")
	}
	// Same person, same account row, a space they ARE a member of — but their
	// token names tenant acme, so the rival-owned space is not theirs to open
	// on this credential.
	if _, err := id.admit(ctx, memberCl, other.UUID); err == nil {
		t.Fatalf("admit crossed the tenant boundary into a foreign org's space")
	}
	if _, err := id.admit(ctx, memberCl, uuid.NewString()); err == nil {
		t.Fatalf("admit granted a space that does not exist")
	}
	// A caller with no tenant names nothing to be a member of.
	if _, err := id.admit(ctx, caller{account: member}, ws.UUID); err == nil {
		t.Fatalf("admit granted a caller carrying no org")
	}
	if _, err := id.admit(ctx, caller{org: "acme"}, ws.UUID); err == nil {
		t.Fatalf("admit granted a caller carrying no account")
	}
	if stranger == member {
		t.Fatal("test setup: the two subjects must resolve to different accounts")
	}
}

// TestHS256ArmIsUnchanged proves the fallback arm answers exactly what the
// pre-cutover decode answered, for the same fixtures, on both carriers and in the
// same precedence: bearer before cookie, an account claim required, expiry
// enforced, and the tenant + space read from the SIGNED claims.
func TestHS256ArmIsUnchanged(t *testing.T) {
	const acct = "550e8400-e29b-41d4-a716-446655440000"
	wsUUID := uuid.NewString()
	session := hsToken(t, acct, "", "acme")
	space := hsToken(t, acct, wsUUID, "acme")
	id := testIdent(nil)

	// Bearer.
	cl, err := whoOn(t, id, space, "", "")
	if err != nil {
		t.Fatalf("who(hs256 bearer): %v", err)
	}
	if cl.iam {
		t.Fatal("an HS256 token resolved on the IAM lane")
	}
	if cl.account != acct || cl.org != "acme" || cl.space != wsUUID || cl.raw != space {
		t.Fatalf("hs256 bearer = %+v", cl)
	}
	// Cookie, and the bearer still wins over it — the pre-cutover precedence.
	cl, err = whoOn(t, id, space, "", session)
	if err != nil {
		t.Fatalf("who(bearer + account cookie): %v", err)
	}
	if cl.space != wsUUID {
		t.Fatal("the account cookie displaced the bearer")
	}
	cl, err = whoOn(t, id, "", "", session)
	if err != nil {
		t.Fatalf("who(account cookie): %v", err)
	}
	if cl.account != acct || cl.space != "" {
		t.Fatalf("hs256 cookie = %+v", cl)
	}
	// No credential, a forged one, and one carrying no account are all refused.
	if _, err := whoOn(t, id, "", "", ""); err == nil {
		t.Fatal("who admitted a request carrying no credential")
	}
	if _, err := whoOn(t, id, session+"x", "", ""); err == nil {
		t.Fatal("who admitted a token whose signature does not check out")
	}
	expired, err := token.Generate(acct, "", map[string]any{"org": "acme"}, 1, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := whoOn(t, id, expired, "", ""); err == nil {
		t.Fatal("who admitted an expired token")
	}
}

// TestLanePrecedence pins the rule the whole cutover rests on: THE EXISTING
// CREDENTIAL IS ANSWERED FIRST, on both carriers, so this phase changes nothing
// for a client that has one.
//
// The two credentials are NOT interchangeable — an HS256 space token pins a
// space and an IAM token cannot — so preferring the IAM cookie did not merely
// pick a different lane, it silently WIDENED the collaborator planes from "the
// space this token names" to "any space you are a member of", and made
// getWorkspaceInfo answer SpaceNotFound where the pin used to answer. A phase
// that is supposed to be inert cannot do that, so the order is: bearer alone if
// there is a bearer; then account-token; then hanzo_iam_token for the browser that
// holds nothing else, which is exactly the post-cutover client.
func TestLanePrecedence(t *testing.T) {
	const acct = "550e8400-e29b-41d4-a716-446655440000"
	wsUUID := uuid.NewString()
	hs := hsToken(t, acct, wsUUID, "acme")
	store := openTestStore(t)
	id, iam := identFor(t, store)
	enrolled(t, store, "iam-org", iamSub, "Ada")
	iamTok := iam.Sign(t, homeIn("iam-org", iamSub))

	// BOTH cookies — the state every signed-in browser is in today. The HS256 one
	// wins, and it keeps its space pin.
	cl, err := whoOn(t, id, "", iamTok, hs)
	if err != nil {
		t.Fatalf("who(both cookies): %v", err)
	}
	if cl.iam {
		t.Fatal("the IAM cookie displaced a live account-token cookie — the phase is not inert")
	}
	if cl.org != "acme" || cl.space != wsUUID {
		t.Fatalf("both cookies resolved %+v, want the HS256 caller with its space pin", cl)
	}
	// A live HS256 bearer beside an IAM cookie stays on the HS256 arm too.
	cl, err = whoOn(t, id, hs, iamTok, "")
	if err != nil {
		t.Fatalf("who(hs256 bearer + iam cookie): %v", err)
	}
	if cl.iam || cl.org != "acme" || cl.space != wsUUID {
		t.Fatalf("hs256 bearer resolved %+v", cl)
	}
	// The IAM cookie alone — the post-cutover browser — resolves on the IAM lane.
	cl, err = whoOn(t, id, "", iamTok, "")
	if err != nil {
		t.Fatalf("who(iam cookie only): %v", err)
	}
	if !cl.iam || cl.org != "iam-org" {
		t.Fatalf("iam cookie alone resolved %+v, want the IAM lane", cl)
	}
	// An IAM bearer wins over the HS256 arm on the SAME carrier: a header that
	// verifies as IAM is never re-read as HS256.
	cl, err = whoOn(t, id, iamTok, "", hs)
	if err != nil {
		t.Fatalf("who(iam bearer): %v", err)
	}
	if !cl.iam {
		t.Fatal("an IAM bearer was not read as IAM")
	}
	// A stale IAM cookie beside a live account-token is simply never reached.
	cl, err = whoOn(t, id, "", "stale.iam.cookie", hs)
	if err != nil {
		t.Fatalf("who(stale iam cookie + account cookie): %v", err)
	}
	if cl.iam || cl.account != acct {
		t.Fatalf("stale IAM cookie did not fall through: %+v", cl)
	}
	// And the converse: a STALE account-token answers alone rather than falling
	// through to a live IAM cookie. Falling through would extend a session that used
	// to end in a 401, with a different reach — the widening this order exists to
	// prevent, arriving on a timer instead of on a deploy.
	stale, err := token.Generate(acct, wsUUID, map[string]any{"org": "acme"}, 1, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := whoOn(t, id, "", iamTok, stale); err == nil {
		t.Fatal("an expired account-token fell through to the IAM cookie — a session silently continued")
	}
}

// TestIAMCredentialNeverReachesTheWire is the C1 regression.
//
// getLoginInfoByToken echoes the caller's token back to the SPA as its session
// token, and the SPA is page JS. On the IAM lane that credential is the raw
// estate-wide RS256 bearer out of an HttpOnly cookie — HttpOnly precisely so script
// cannot read it. Echoing it hands it back to the script the flag exists to stop:
// one RPC with no bearer at all, and the caller's whole platform credential is in a
// variable. caller.raw is therefore empty on that lane structurally, so no echo
// site can reintroduce the leak by forgetting.
func TestIAMCredentialNeverReachesTheWire(t *testing.T) {
	store := openTestStore(t)
	id, iam := identFor(t, store)
	enrolled(t, store, "acme", iamSub, "Ada")
	iamTok := iam.Sign(t, homeIn("acme", iamSub))

	for name, carrier := range map[string][2]string{
		"bearer": {iamTok, ""},
		"cookie": {"", iamTok},
	} {
		cl, err := whoOn(t, id, carrier[0], carrier[1], "")
		if err != nil {
			t.Fatalf("%s: who: %v", name, err)
		}
		if cl.raw != "" {
			t.Fatalf("SECURITY (%s): caller.raw carries the IAM credential, which every echo site returns to page JS", name)
		}
		if strings.Contains(cl.raw, iamTok) {
			t.Fatalf("SECURITY (%s): the IAM credential leaked into the caller", name)
		}
	}
	// The HS256 arm still echoes its own token — that one IS the SPA's session
	// token, and the SPA is the party that presented it.
	hs := hsToken(t, "550e8400-e29b-41d4-a716-446655440000", "", "acme")
	cl, err := whoOn(t, id, hs, "", "")
	if err != nil || cl.raw != hs {
		t.Fatalf("the HS256 arm stopped echoing its own token: (%q, %v)", cl.raw, err)
	}
}

// TestIAMLaneTenantIsTheHomeOrgNotOwner is the F4 regression.
//
// `owner` carries the APPLICATION's org, so it is chosen by whichever app the
// caller authenticated through — the identity boundary refuses to derive a tenant
// from it for exactly that reason (idClaims.homeOrg). A lane that reads it scopes
// every store query to an org the caller SELECTED: sign in through an app owned by
// "lux" and team files your spaces, blobs and billing under lux.
func TestIAMLaneTenantIsTheHomeOrgNotOwner(t *testing.T) {
	store := openTestStore(t)
	id, iam := identFor(t, store)
	home := enrolled(t, store, "hanzo", iamSub, "Ada")

	// owner names one org, the SIGNED membership set names another as home.
	crossed := iam.Sign(t, iamtest.Claims{
		Sub: iamSub, Owner: "lux",
		Orgs: []map[string]any{{"org": "hanzo", "role": "admin"}},
	})
	cl, err := id.iam(context.Background(), crossed)
	if err != nil {
		t.Fatalf("a token whose owner differs from its home org was refused outright: %v", err)
	}
	if cl.org != "hanzo" {
		t.Fatalf("SECURITY: tenant = %q, want the home org \"hanzo\" — `owner` is caller-selectable", cl.org)
	}
	if cl.account != home {
		t.Fatalf("account = %q, want the home-org account %q", cl.account, home)
	}
	// A token with NO membership set has no home, and that is a refusal rather than
	// a fallback to owner — which is also every MACHINE credential (a
	// client_credentials app or an API key is a member of nothing).
	machine := iam.Sign(t, iamtest.Claims{Sub: "svc/robot", Owner: "hanzo"})
	if _, err := id.iam(context.Background(), machine); err == nil {
		t.Fatal("SECURITY: a token carrying no membership set was given a tenant")
	}
}

// TestIAMLaneRefusesAForeignAudience is the C2 regression.
//
// The identity boundary deliberately does not gate audience: for an API call, a
// valid signature from a trusted issuer already proves IAM minted the token for one
// of its own apps, and cloud kept no mirror of IAM's registry. A SESSION is a
// different question — this lane turns a bearer into a signed-in person on
// hanzo.team, and a token the user obtained for chat or the console is not consent
// to that. Without the gate, one app's token is every app's session.
func TestIAMLaneRefusesAForeignAudience(t *testing.T) {
	store := openTestStore(t)
	id, iam := identFor(t, store)
	enrolled(t, store, "acme", iamSub, "Ada")

	for _, aud := range []string{"hanzo-chat", "hanzo-console", "hanzo-cloud", ""} {
		c := homeIn("acme", iamSub)
		c.Aud = aud
		if aud == "" {
			c.Aud = " " // an audience that names no app
		}
		if _, err := id.iam(context.Background(), iam.Sign(t, c)); err == nil {
			t.Fatalf("SECURITY: a token minted for %q was accepted as a team session", aud)
		}
	}
	// Team's own audience is admitted, so the gate discriminates.
	if _, err := id.iam(context.Background(), iam.Sign(t, homeIn("acme", iamSub))); err != nil {
		t.Fatalf("team's own audience was refused: %v", err)
	}
	// And an operator-named additional SPA is admitted, because it was NAMED.
	id.audience["hanzo-front"] = true
	c := homeIn("acme", iamSub)
	c.Aud = "hanzo-front"
	if _, err := id.iam(context.Background(), iam.Sign(t, c)); err != nil {
		t.Fatalf("an explicitly named audience was refused: %v", err)
	}
}

// TestTransactorTakesNothingAmbient pins the decision that the transactor socket
// has NO IAM lane in this phase.
//
// A WebSocket is exempt from CORS, so a cookie-borne credential would make the
// Origin header the only access control on the entire space data plane — one
// permissive entry in that allowlist, or one first-party page running attacker
// script, and the stream is readable and writable. The credential therefore stays
// the path-borne space token, which a foreign page cannot produce, until the
// client can send it in-band the way collabws.go already does.
func TestTransactorTakesNothingAmbient(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	member := accountID(iamSub)
	ws, err := store.EnsureSpace(ctx, "acme", member, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	id, iam := identFor(t, store)
	srv := &transServer{ident: id}

	// The space token is the credential, as it has always been.
	wsTok := hsToken(t, member, ws.UUID, "acme")
	cl, got, err := srv.admitWS(wsTok)
	if err != nil || got != ws.UUID || cl.iam {
		t.Fatalf("admitWS(space token) = (%+v, %q, %v)", cl, got, err)
	}
	// A bare space UUID authorizes NOTHING, whatever the caller holds elsewhere:
	// it is not a credential, and there is no ambient lane to pair it with.
	if _, _, err := srv.admitWS(ws.UUID); err == nil {
		t.Fatal("SECURITY: a bare space uuid opened a socket")
	}
	// Nor does a valid IAM access token in that position — an estate-wide bearer
	// does not belong in a URL, so it is simply not a space token.
	if _, _, err := srv.admitWS(iam.Sign(t, homeIn("acme", iamSub))); err == nil {
		t.Fatal("SECURITY: an IAM access token was accepted as a space token")
	}
	// A session token (no space claim) resolves but names no space, which is
	// what lets the statistics read answer it with an empty session map while
	// serveWS refuses it.
	if _, got, err := srv.admitWS(hsToken(t, member, "", "acme")); err != nil || got != "" {
		t.Fatalf("admitWS(session token) = (%q, %v)", got, err)
	}
}

// TestOriginAllowlistHasNoWildcard pins the socket's Origin gate as a NAMED set.
//
// It used to admit any *.hanzo.ai host. Because a WebSocket is exempt from CORS,
// that check is the access control rather than a hint about it, so a wildcard over
// the registrable domain put every first-party host inside the space data
// plane's trust boundary.
func TestOriginAllowlistHasNoWildcard(t *testing.T) {
	const host = "api.hanzo.ai"
	for _, origin := range []string{
		"https://chat.hanzo.ai", "https://preview.hanzo.ai", "https://anything.hanzo.ai",
		"https://evil.com", "https://hanzo.ai.evil.com", "https://team.hanzo.ai.evil.com",
	} {
		if originAllowed(origin, host) {
			t.Errorf("SECURITY: origin %q was admitted to the space socket", origin)
		}
	}
	// The named team surfaces, the request's own host, and a non-browser client
	// (which sends no Origin, and which a browser cannot imitate) still pass.
	for _, origin := range []string{
		"", "https://hanzo.team", "https://team.hanzo.ai", "https://api.hanzo.team",
		"https://hanzo.ai", "http://localhost:3000", "https://" + host,
	} {
		if !originAllowed(origin, host) {
			t.Errorf("origin %q was refused; the gate is not discriminating", origin)
		}
	}
}

// TestMemberPlaneOpScopesToTheCaller proves the membership answer a PEER process
// gets is scoped to the org on the CALL and to nothing the caller wrote: the same
// (space, subject) pair answers "member" for the owning tenant and "not a
// member" for any other, so a peer cannot probe a foreign roster one space at
// a time. It also proves the IAM subject → account join stays here, where the rows
// were created: the peer sends a subject and is told the account.
func TestMemberPlaneOpScopesToTheCaller(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	ws, err := store.EnsureSpace(ctx, "acme", accountID(iamSub), "Ada")
	if err != nil {
		t.Fatal(err)
	}

	in := &plane.MemberIn{Space: ws.UUID, Subject: iamSub}
	got, err := memberOf(cloud.For(ctx, "acme"), store, in)
	if err != nil {
		t.Fatalf("memberOf(own org): %v", err)
	}
	if !got.Member || got.Role == "" {
		t.Fatalf("memberOf(own org) = %+v, want a member with a role", got)
	}
	if got.Account != accountID(iamSub) {
		t.Fatalf("account = %q, want accountID(subject) = %q", got.Account, accountID(iamSub))
	}

	// Another tenant asking about the SAME space uuid learns nothing.
	got, err = memberOf(cloud.For(ctx, "rival"), store, in)
	if err != nil {
		t.Fatalf("memberOf(foreign org): %v", err)
	}
	if got.Member || got.Role != "" || got.Account != "" {
		t.Fatalf("memberOf(foreign org) = %+v, want an empty answer", got)
	}

	// A stranger in the owning org is not a member either.
	got, err = memberOf(cloud.For(ctx, "acme"), store, &plane.MemberIn{Space: ws.UUID, Subject: iamOtherSub})
	if err != nil {
		t.Fatalf("memberOf(stranger): %v", err)
	}
	if got.Member {
		t.Fatal("memberOf admitted a subject with no row")
	}

	// A call carrying NO org is refused, not answered — a refusal is a fault an
	// operator sees, a false negative is a join that silently stops working.
	if _, err := memberOf(ctx, store, in); err == nil {
		t.Fatal("memberOf answered a call carrying no org")
	}
	// And a store this process does not have open fails closed.
	if _, err := memberOf(cloud.For(ctx, "acme"), nil, in); err == nil {
		t.Fatal("memberOf answered with no store open")
	}
}

// TestSessionAudienceIsNamedNotPatterned pins the SHAPE of the audience policy,
// which is the half a behavioural test cannot hold.
//
// The estate's boundary deliberately does not gate audience, and that posture was
// decided for an API surface: a valid signature from a trusted issuer proves IAM
// minted the token for one of its own apps, and cloud kept no mirror of IAM's
// registry because the mirror drifted and 401'd every new first-party app. Team is
// a SESSION surface and diverges — but the way that divergence rots is by growing
// back into the mirror, one pattern at a time ("any *-team app", "anything from our
// org"). So the set is enumerated: this deployment's own client id, plus entries an
// operator NAMED, and matching is exact.
func TestSessionAudienceIsNamedNotPatterned(t *testing.T) {
	aud := sessionAudience(config{iamClientID: "hanzo-team"})
	if len(aud) != 1 || !aud["hanzo-team"] {
		t.Fatalf("default audience = %v, want exactly this deployment's own client id", aud)
	}
	t.Setenv("TEAM_IAM_AUDIENCES", "hanzo-front, hanzo-desktop ,,")
	aud = sessionAudience(config{iamClientID: "hanzo-team"})
	for _, want := range []string{"hanzo-team", "hanzo-front", "hanzo-desktop"} {
		if !aud[want] {
			t.Fatalf("audience %v is missing the named entry %q", aud, want)
		}
	}
	if len(aud) != 3 {
		t.Fatalf("audience = %v, want exactly the three named entries", aud)
	}

	// Matching is EXACT. Nothing here may admit an audience by resemblance — a
	// prefix, a suffix, or a wildcard — because that is the registry mirror
	// returning under another name.
	id := &identity{audience: aud}
	for _, foreign := range []string{
		"hanzo-teamx", "xhanzo-team", "hanzo", "hanzo-team-staging",
		"*", "", " ", "HANZO-TEAM",
	} {
		if id.forThisDeployment([]string{foreign}) {
			t.Errorf("SECURITY: audience %q was admitted by resemblance", foreign)
		}
	}
	if !id.forThisDeployment([]string{"other", "hanzo-team"}) {
		t.Fatal("a token naming several audiences, one of them ours, was refused")
	}
	// A deployment with NO audience configured admits nothing, rather than
	// everything: an empty allowlist matches nothing.
	empty := &identity{audience: sessionAudience(config{})}
	if empty.forThisDeployment([]string{"hanzo-team"}) {
		t.Fatal("SECURITY: an unconfigured audience set admitted a token")
	}
}

// TestAudienceIsMatchedExactlyNotFolded is the NEW-1 regression.
//
// The audience gate used to TrimSpace the incoming `aud` claim before looking it
// up. That made the comparison non-injective: "hanzo-team " and "hanzo-team" are
// DISTINCT IAM applications — IAM refuses only an exact name collision, so the
// padded one is registrable by anyone through /v1/iam/add-application — and
// trimming collapses them onto one key. An attacker registers the lookalike, signs
// their own users in through it, and IAM hands them tokens this check accepts as
// sessions of the real app.
//
// It is the estate's identifier rule, which OrgHasUnsafeRune states for orgs:
// trimming would collapse "acme " onto "acme", and an injective boundary must
// never fold two distinct identifiers into one. The claim is signed, so it is not
// ours to rewrite; whitespace is settled where the SET is built instead.
func TestAudienceIsMatchedExactlyNotFolded(t *testing.T) {
	store := openTestStore(t)
	id, iam := identFor(t, store)
	enrolled(t, store, "acme", iamSub, "Ada")

	// Every registrable lookalike that a fold would admit.
	for _, lookalike := range []string{
		"hanzo-team ", " hanzo-team", "  hanzo-team  ", "hanzo-team\t", "\nhanzo-team",
	} {
		if id.forThisDeployment([]string{lookalike}) {
			t.Errorf("SECURITY: audience %q folded onto the real app's identifier", lookalike)
		}
		c := homeIn("acme", iamSub)
		c.Aud = lookalike
		if _, err := id.iam(context.Background(), iam.Sign(t, c)); err == nil {
			t.Errorf("SECURITY: a token minted for the lookalike app %q was accepted as a team session", lookalike)
		}
	}
	// The real audience is still accepted, so the gate discriminates rather than
	// refusing everything.
	if !id.forThisDeployment([]string{iamtest.Audience}) {
		t.Fatal("the deployment's own audience was refused; the test is not discriminating")
	}
	if _, err := id.iam(context.Background(), iam.Sign(t, homeIn("acme", iamSub))); err != nil {
		t.Fatalf("the deployment's own audience was refused: %v", err)
	}

	// And the tidying still happens where the SET is built: an operator's padded
	// config entry is theirs to normalise, and it admits the UNPADDED app — never
	// the other way round.
	t.Setenv("TEAM_IAM_AUDIENCES", "  hanzo-front  ")
	built := sessionAudience(config{iamClientID: "  hanzo-team  "})
	if !built["hanzo-team"] || !built["hanzo-front"] {
		t.Fatalf("sessionAudience did not trim its own config entries: %v", built)
	}
	if built["  hanzo-team  "] || built["hanzo-team "] {
		t.Fatalf("sessionAudience kept a padded key, which a padded claim would then match: %v", built)
	}
}
