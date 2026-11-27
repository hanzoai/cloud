package kms_test

// CUSTODY, END TO END. What an app seals on an org's behalf is read back by that
// app on the plane, and over REST only by SuperAdmin, a declared grant, and —
// beneath a sync subtree — the org's own KMS-sync identity. The declaration's
// `enforce: false` (production's, universe kms-grants.yaml) changes none of it:
// red's finding was a member reading the org's MCP server credential through
// exactly that line.

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// custodyDoc is production's shape: measuring, with one grant that reaches into
// a custodied subtree the way hanzo-operator's /integrations/cloudflare does.
const custodyDoc = "enforce: false\n" + `
grants:
- holder: hanzo-operator
  org: hanzo
  from: charts
  read:
  - {path: /integrations/cloudflare, env: prod, keys: [OAUTH_CLIENT_ID]}
`

// custodyEnforced is the same world with the declaration enforcing, and the
// operator also holding a WRITE grant on what it reads: under enforcement a
// write asks for membership, so the grant and the custody rule decide it alone.
const custodyEnforced = `
grants:
- holder: hanzo-operator
  org: hanzo
  why: rotates the Cloudflare OAuth client it reads
  read:  [{path: /integrations/cloudflare, env: prod, keys: [OAUTH_CLIENT_ID]}]
  write: [{path: /integrations/cloudflare, env: prod, keys: [OAUTH_CLIENT_ID]}]
`

const (
	valMCP      = "Bearer sk_live_ORG_SECRET"
	valAppEnv   = "custody-DATABASE_URL-of-hanzo-shop"
	valRootKey  = "custody-ROOT_KEY-of-hanzo"
	valCFClient = "custody-OAUTH_CLIENT_ID-of-hanzo"
	valOpen     = "custody-OPEN-of-hanzo"
	valWallet   = "custody-signing-key-of-hanzo-w1"
)

func newCustodyWorld(t *testing.T) grantWorld { return custodyWorldOver(t, custodyDoc) }

func custodyWorldOver(t *testing.T, doc string) grantWorld {
	t.Helper()
	w := newGrantWorld(t, doc, true)
	for ref, v := range map[string]string{
		"orgs/hanzo/tools-mcp/stripe-com":                         valMCP, // exactly authRef("hanzo", "stripe-com")
		"orgs/hanzo/platform/shop/DATABASE_URL":                   valAppEnv,
		"orgs/hanzo/platform/ROOT_KEY":                            valRootKey,
		"orgs/hanzo/integrations/cloudflare/OAUTH_CLIENT_ID@prod": valCFClient,
		"orgs/hanzo/team/OPEN":                                    valOpen,
		"orgs/hanzo/wallets/w1":                                   valWallet,
	} {
		if err := w.deps.KMS.PutSecret(context.Background(), ref, []byte(v)); err != nil {
			t.Fatalf("seed %s: %v", ref, err)
		}
	}
	return w
}

// Red's PoC, verbatim in its request: a plain member of hanzo reads
// GET /v1/kms/secrets/tools-mcp/<id> while the declaration measures.
func TestCustody_MemberCannotReadTheOrgsMCPCredential(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newCustodyWorld(t)
		for _, who := range []struct {
			name string
			tok  isoTok
		}{{"member", member}, {"org admin", orgAdmin}, {"an app of the org", machine("hanzo-operator", "hanzo")}} {
			p := w.read(t, who.name+" reads the org's MCP credential", "tools-mcp/stripe-com", who.tok, nil).
				noLeak(t, valMCP).isStatus(t, 403)
			// A plain member of hanzo acts in none of it (authz.IsPlatformOrg), so the
			// refusal comes at the org, before custody is asked.
			if who.tok.username == member.username {
				if !strings.Contains(p.body, "an org scope is required") {
					t.Fatalf("[%s] a plain member of the brand org reached custody: %s", p.what, p.body)
				}
				continue
			}
			if !strings.Contains(p.body, "tools holds it on the org's behalf") {
				t.Fatalf("[%s] the refusal must name the custodian: %s", p.what, p.body)
			}
		}
		// The pair's control: the custodian reads it on the plane, as the relay does.
		got, err := w.deps.KMS.GetSecret(cloud.For(context.Background(), "hanzo"), "orgs/hanzo/tools-mcp/stripe-com")
		if err != nil || string(got) != valMCP {
			t.Fatalf("the custodian reads its own credential = %q, %v", got, err)
		}
		// The measurement still decides what is not custodied, for an application
		// acting as itself: it reads an uncustodied value as a would-deny. A person
		// is not measured; with no grant they read nothing.
		w.read(t, "an app reads an uncustodied value", "team/OPEN", machine("hanzo-operator", "hanzo"), nil).isValue(t, valOpen)
		w.read(t, "member reads an uncustodied value", "team/OPEN", member, nil).noLeak(t, valOpen).isStatus(t, 403)
	})
}

// Red's second PoC: while the declaration measures, a write asks only for org
// admin, so an org admin replaced or deleted what an app holds on the org's
// behalf — DELETE /v1/kms/secrets/wallets/w1 destroyed a wallet's signing key.
// Every verb over REST is now refused as the read is, the refusal names the
// custodian, and the value stands.
func TestCustody_AnOrgAdminCannotReplaceOrDeleteWhatAnAppHolds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newCustodyWorld(t)
		for _, who := range []struct {
			name string
			tok  isoTok
		}{{"org admin", orgAdmin}, {"member", member}} {
			w.call(t, who.name+" replaces the org's MCP credential", "POST", "/v1/kms/secrets", who.tok, nil,
				`{"path":"/tools-mcp","name":"stripe-com","env":"default","value":"planted"}`).noLeak(t, valMCP).isStatus(t, 403)
			w.call(t, who.name+" deletes the org's MCP credential", "DELETE", "/v1/kms/secrets/tools-mcp/stripe-com", who.tok, nil, "").
				noLeak(t, valMCP).isStatus(t, 403)
			w.call(t, who.name+" deletes a wallet's signing key", "DELETE", "/v1/kms/secrets/wallets/w1", who.tok, nil, "").
				noLeak(t, valWallet).isStatus(t, 403)
			w.call(t, who.name+" replaces an app's secret env", "POST", "/v1/kms/secrets", who.tok, nil,
				`{"path":"/platform/shop","name":"DATABASE_URL","env":"default","value":"planted"}`).noLeak(t, valAppEnv).isStatus(t, 403)
		}
		p := w.call(t, "org admin deletes a wallet's signing key", "DELETE", "/v1/kms/secrets/wallets/w1", orgAdmin, nil, "")
		if !strings.Contains(p.body, "wallet holds it on the org's behalf") {
			t.Fatalf("[%s] the refusal must name the custodian: %s", p.what, p.body)
		}
		ctx := cloud.For(context.Background(), "hanzo")
		for ref, want := range map[string]string{
			"orgs/hanzo/tools-mcp/stripe-com":       valMCP,
			"orgs/hanzo/wallets/w1":                 valWallet,
			"orgs/hanzo/platform/shop/DATABASE_URL": valAppEnv,
		} {
			if got, err := w.deps.KMS.GetSecret(ctx, ref); err != nil || string(got) != want {
				t.Fatalf("%s after the refused writes = %q, %v; want it unchanged", ref, got, err)
			}
		}
		// Every refused write is on the chain as a denial, not a would-deny. (A
		// member's is refused before that, at admission: a write asks for org admin
		// while the declaration measures.)
		rows := w.kmsRecords(t, "kms.write")
		if len(rows) != 5 {
			t.Fatalf("the org admin's five custodied writes were recorded as %s", mustJSON(t, rows))
		}
		for _, r := range rows {
			if r.Outcome.Result != "deny" {
				t.Fatalf("a custodied write was recorded as %s", mustJSON(t, r))
			}
		}

		// The pair's control: the custodian replaces and removes what it holds on the
		// plane, as the tool app and the wallet app do.
		if err := w.deps.KMS.PutSecret(ctx, "orgs/hanzo/tools-mcp/stripe-com", []byte("rotated")); err != nil {
			t.Fatalf("the custodian rotates its credential: %v", err)
		}
		if got, err := w.deps.KMS.GetSecret(ctx, "orgs/hanzo/tools-mcp/stripe-com"); err != nil || string(got) != "rotated" {
			t.Fatalf("the custodian reads its rotation = %q, %v", got, err)
		}
		if err := w.deps.KMS.DeleteSecret(ctx, "orgs/hanzo/wallets/w1"); err != nil {
			t.Fatalf("the custodian removes a key it holds: %v", err)
		}
		// What no app holds is SuperAdmin's or a grant's to write, never an org
		// admin's: administering the org is not a grant, and the brand org's
		// members include every sign-up.
		w.call(t, "org admin writes an uncustodied value", "POST", "/v1/kms/secrets", orgAdmin, nil,
			`{"path":"/team","name":"OPEN","env":"default","value":"rotated"}`).isStatus(t, 403)
		w.call(t, "org admin deletes it", "DELETE", "/v1/kms/secrets/team/OPEN", orgAdmin, nil, "").isStatus(t, 403)
		w.call(t, "SuperAdmin writes an uncustodied value", "POST", "/v1/kms/secrets", superAdmin, inHanzo,
			`{"path":"/team","name":"OPEN","env":"default","value":"rotated"}`).isStatus(t, 200)
	})
}

// Under enforcement a write asks only for membership, so the grant and custody
// decide it alone: a declared write grant into a custodied subtree still writes,
// SuperAdmin still writes with a record, and the org's sync identity — which
// reads beneath a sync subtree — writes nothing there.
func TestCustody_AGrantAndSuperAdminStillWrite(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := custodyWorldOver(t, custodyEnforced)
		op := machine("hanzo-operator", "hanzo")
		w.call(t, "operator rotates its granted OAuth client", "POST", "/v1/kms/secrets", op, nil,
			`{"path":"/integrations/cloudflare","name":"OAUTH_CLIENT_ID","env":"prod","value":"rotated"}`).isStatus(t, 200)
		w.read(t, "operator reads the rotation", "integrations/cloudflare/OAUTH_CLIENT_ID?env=prod", op, nil).isValue(t, "rotated")
		w.call(t, "operator writes beside its grant", "POST", "/v1/kms/secrets", op, nil,
			`{"path":"/integrations/cloudflare","name":"OAUTH_CLIENT_SECRET","env":"prod","value":"planted"}`).isStatus(t, 403)

		sync := machine(cloud.KMSMachineClientID("hanzo"), "hanzo")
		w.read(t, "the sync identity reads an app's secret env", "platform/shop/DATABASE_URL", sync, nil).isValue(t, valAppEnv)
		w.call(t, "the sync identity replaces an app's secret env", "POST", "/v1/kms/secrets", sync, nil,
			`{"path":"/platform/shop","name":"DATABASE_URL","env":"default","value":"planted"}`).noLeak(t, valAppEnv).isStatus(t, 403)
		w.call(t, "the sync identity deletes an app's secret env", "DELETE", "/v1/kms/secrets/platform/shop/DATABASE_URL", sync, nil, "").
			noLeak(t, valAppEnv).isStatus(t, 403)
		w.read(t, "the value stands", "platform/shop/DATABASE_URL", sync, nil).isValue(t, valAppEnv)

		w.call(t, "SuperAdmin deletes a wallet's signing key", "DELETE", "/v1/kms/secrets/wallets/w1", superAdmin, inHanzo, "").isStatus(t, 200)
		var super bool
		for _, r := range w.kmsRecords(t, "kms.write") {
			super = super || strings.HasPrefix(r.Outcome.Reason, "superadmin ")
		}
		if !super {
			t.Fatal("SuperAdmin's custodied delete left no record")
		}
	})
}

// Every denial of a custodied read is on the chain, not a would-deny.
func TestCustody_ADenialIsAuditedAsOne(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newCustodyWorld(t)
		// An org admin reaches custody (a plain member of hanzo is refused at the org,
		// before KMS is asked) and is denied there, on the chain.
		w.read(t, "org admin reads the org's MCP credential", "tools-mcp/stripe-com", orgAdmin, nil).isStatus(t, 403)
		rows := w.kmsRecords(t, "kms.read")
		if len(rows) != 1 || rows[0].Outcome.Result != "deny" || rows[0].Resource.ID != "hanzo:/tools-mcp/stripe-com@default" {
			t.Fatalf("the custodied denial was recorded as %s", mustJSON(t, rows))
		}
	})
}

// A sync subtree is read by the org's own sync identity — beneath its root and
// nowhere else — and by nobody else in the org.
func TestCustody_TheOrgsSyncIdentityReadsItsDeployments(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newCustodyWorld(t)
		sync := machine(cloud.KMSMachineClientID("hanzo"), "hanzo")

		w.read(t, "the sync identity reads an app's secret env", "platform/shop/DATABASE_URL", sync, nil).isValue(t, valAppEnv)
		w.read(t, "member reads an app's secret env", "platform/shop/DATABASE_URL", member, nil).
			noLeak(t, valAppEnv).isStatus(t, 403)
		// A key at a sync subtree's root is not the custodian's: the declaration
		// decides it as it decides any hand-kept value — an application's read as a
		// would-deny, and a person's, ungranted, not at all.
		w.read(t, "an app reads a key at the subtree's root", "platform/ROOT_KEY", machine("hanzo-operator", "hanzo"), nil).isValue(t, valRootKey)
		w.read(t, "member reads a key at the subtree's root", "platform/ROOT_KEY", member, nil).noLeak(t, valRootKey).isStatus(t, 403)
		// Another org's sync identity is another org's: its token names lux.
		w.read(t, "lux's sync identity in hanzo", "platform/shop/DATABASE_URL", machine(cloud.KMSMachineClientID("lux"), "lux"), nil).
			noLeak(t, valAppEnv).isStatus(t, 404)
		// A sync identity writes nothing it was not granted.
		w.call(t, "the sync identity writes an app's secret env", "POST", "/v1/kms/secrets", sync, nil,
			`{"path":"/platform/shop","name":"DATABASE_URL","env":"default","value":"planted"}`).isStatus(t, 403)
	})
}

// A declared grant into a custodied subtree still opens what it names, and
// SuperAdmin still reads with a record.
func TestCustody_AGrantAndSuperAdminStillOpen(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newCustodyWorld(t)
		op := machine("hanzo-operator", "hanzo")
		w.read(t, "operator reads its granted OAuth client", "integrations/cloudflare/OAUTH_CLIENT_ID?env=prod", op, nil).isValue(t, valCFClient)
		w.read(t, "member reads the granted OAuth client", "integrations/cloudflare/OAUTH_CLIENT_ID?env=prod", member, nil).
			noLeak(t, valCFClient).isStatus(t, 403)

		w.read(t, "SuperAdmin reads the MCP credential", "tools-mcp/stripe-com", superAdmin, inHanzo).isValue(t, valMCP)
		var super bool
		for _, r := range w.kmsRecords(t, "kms.read") {
			super = super || strings.HasPrefix(r.Outcome.Reason, "superadmin ")
		}
		if !super {
			t.Fatal("SuperAdmin's custodied read left no record")
		}
	})
}

// Every app's ref that red named, and the ones like it, is custodied; the
// deployment's own and an org's hand-kept paths are not.
func TestCustody_TheRefsAppsSealAreCustodied(t *testing.T) {
	for _, ref := range []string{
		"orgs/acme/tools-mcp/stripe-com",
		"orgs/acme/environment/web/API_KEY",
		"orgs/acme/providers/github/default",
		"orgs/acme/books/plaid/items",
		"orgs/acme/validators/7/bls.key",
		"orgs/acme/platform/shop/DATABASE_URL",
		"orgs/acme/kms-auth/clientSecret",
		"orgs/acme/settings/crm/token",
		"orgs/acme/notify/twilio/auth_token",
		"/orgs/acme/integrations/github/access_token@prod",
		"/orgs/acme/users/u1/connectors/google/work/access_token@prod",
		"orgs/acme/wallets/w1",
		"orgs/acme/sql/main",
	} {
		if !kms.Custodied(ref) {
			t.Errorf("%s is sealed on the org's behalf and is not custodied", ref)
		}
	}
	for _, ref := range []string{"orgs/acme/ci/DEPLOY_TOKEN", "orgs/acme/DATABASE_URL", "principal/attestation", "books/teller/client-key"} {
		if kms.Custodied(ref) {
			t.Errorf("%s is not custodied, and is answered as it was", ref)
		}
	}
}

// The engine license signing seed is the licensing plugin's (plugin/licensing
// signingKey). A sign-up lands in the brand org and reads as a member of it, so
// every verb over REST is refused to a member, an org admin and an app of the
// org while the declaration measures; SuperAdmin reads it with a record, and
// the plugin reads it on the plane.
func TestCustody_TheLicenseSigningKeyIsTheLicensingPlugins(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const (
			ref  = "orgs/hanzo/engine/LICENSE_SIGNING_KEY@prod"
			seed = "custody-LICENSE_SIGNING_KEY-of-hanzo"
		)
		if !kms.Custodied(ref) {
			t.Fatalf("%s is not custodied", ref)
		}
		w := newGrantWorld(t, custodyDoc, true)
		ctx := cloud.For(context.Background(), "hanzo")
		if err := w.deps.KMS.PutSecret(ctx, ref, []byte(seed)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		for _, who := range []struct {
			name string
			tok  isoTok
		}{{"member", member}, {"org admin", orgAdmin}, {"an app of the org", machine("hanzo-operator", "hanzo")}} {
			p := w.read(t, who.name+" reads the license signing key", "engine/LICENSE_SIGNING_KEY?env=prod", who.tok, nil).
				noLeak(t, seed).isStatus(t, 403)
			if who.tok.username == member.username {
				// A plain member of hanzo has no org scope: refused before custody.
				if !strings.Contains(p.body, "an org scope is required") {
					t.Fatalf("[%s] a plain member of the brand org reached custody: %s", p.what, p.body)
				}
			} else if !strings.Contains(p.body, "licensing holds it on the org's behalf") {
				t.Fatalf("[%s] the refusal must name the custodian: %s", p.what, p.body)
			}
			w.call(t, who.name+" replaces the license signing key", "POST", "/v1/kms/secrets", who.tok, nil,
				`{"path":"/engine","name":"LICENSE_SIGNING_KEY","env":"prod","value":"planted"}`).noLeak(t, seed).isStatus(t, 403)
			w.call(t, who.name+" deletes the license signing key", "DELETE", "/v1/kms/secrets/engine/LICENSE_SIGNING_KEY?env=prod", who.tok, nil, "").
				noLeak(t, seed).isStatus(t, 403)
		}
		if got, err := w.deps.KMS.GetSecret(ctx, ref); err != nil || string(got) != seed {
			t.Fatalf("the plugin reads the key after the refused writes = %q, %v", got, err)
		}
		w.read(t, "SuperAdmin reads the license signing key", "engine/LICENSE_SIGNING_KEY?env=prod", superAdmin, inHanzo).isValue(t, seed)
	})
}
