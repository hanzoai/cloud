package kms_test

// THE GRANT RULE, END TO END. A secret VALUE opens only for SuperAdmin holding an
// access token (audited) or for a holder a grant in git covers exactly. These
// probes run the production pipeline — a signed token, the JWKS the harness
// serves, SanitizeIdentity minting the principal and its holder, the real
// /v1/kms ops — so every verdict below is the one a credential in the fleet gets.
//
// The world is the one the migration grants (universe kms-grants.yaml): the
// operator reads what its charts declare, CI reads and writes what its
// workflows use, and nobody else — no member, no org admin, no stranger — reads
// anything. Every refusal is paired with a success from a credential that DOES
// hold the grant, so a fixture that broke the endpoint cannot pass as a gate.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/kms"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// grantDoc is a slice of the migration's declaration: the operator's charted
// reads, and CI's declared write.
const grantDoc = `
grants:
- holder: hanzo-operator
  org: hanzo
  from: charts
  read:
  - {path: /attendant, env: prod, keys: [HANZO_TOKEN]}
  - {path: /pkg, env: prod, keys: [htpasswd]}
- holder: hanzo-ci
  org: hanzo
  why: CI publishes with the deploy token
  read:  [{path: /deploy, env: prod, keys: [GITHUB_TOKEN]}]
  write: [{path: /deploy, env: prod, keys: [GITHUB_TOKEN]}]
`

// The world's plaintexts. Each is distinct, so a leak names the secret it is.
const (
	valHanzoToken = "grant-HANZO_TOKEN-of-hanzo"
	valHtpasswd   = "grant-htpasswd-of-hanzo"
	valOther      = "grant-OTHER-of-hanzo"
	valMaster     = "grant-CLOUD_KMS_MASTER_KEY-of-hanzo"
	valDeploy     = "grant-GITHUB_TOKEN-of-hanzo"
	valLux        = "grant-HANZO_TOKEN-of-lux"
)

var every = []string{valHanzoToken, valHtpasswd, valOther, valMaster, valDeploy, valLux}

type grantWorld struct {
	app   *zip.App
	key   *rsa.PrivateKey
	deps  cloud.Deps
	chain *audit.Recorder
}

// newGrantWorld stands up the production pipeline over doc ("" = no grant
// document mounted) with this process's own audit chain in deps.Audit, the way
// Serve hands it over. audited=false leaves the chain out. logs, when given,
// also receives the subsystem's log lines.
func newGrantWorld(t *testing.T, doc string, audited bool, logs ...io.Writer) grantWorld {
	t.Helper()
	kms.UseGrants(t, doc)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	cfg := e2eCfg(t, e2eJWKS(t, &key.PublicKey).URL)
	deps := cloud.BuildDeps(cfg) // sets the process's default logger
	if len(logs) > 0 {
		prev := luxlog.Default()
		luxlog.SetDefault(luxlog.NewWriter(io.MultiWriter(append(logs, os.Stderr)...)).Level(luxlog.InfoLevel))
		t.Cleanup(func() { luxlog.SetDefault(prev) })
	}
	w := grantWorld{key: key, deps: deps}
	if audited {
		if w.chain, err = audit.Open(t.TempDir(), "kms", nil); err != nil {
			t.Fatalf("audit.Open: %v", err)
		}
		t.Cleanup(func() { _ = w.chain.Close() })
		deps.Audit = w.chain
		w.deps = deps
	}
	w.app = zip.New(zip.Config{Logger: luxlog.Default()})
	w.app.Use(middleware.Recover())
	w.app.Use(cloud.IdentityMiddleware(cfg))
	if err := cloud.UseAll(w.app, mountSpecs(), cfg, deps); err != nil {
		t.Fatalf("UseAll: %v", err)
	}
	for ref, v := range map[string]string{
		"orgs/hanzo/attendant/HANZO_TOKEN@prod":      valHanzoToken,
		"orgs/hanzo/pkg/htpasswd@prod":               valHtpasswd,
		"orgs/hanzo/pkg/OTHER@prod":                  valOther,
		"orgs/hanzo/cloud/CLOUD_KMS_MASTER_KEY@prod": valMaster,
		"orgs/hanzo/deploy/GITHUB_TOKEN@prod":        valDeploy,
		"orgs/lux/attendant/HANZO_TOKEN@prod":        valLux,
	} {
		if err := deps.KMS.PutSecret(context.Background(), ref, []byte(v)); err != nil {
			t.Fatalf("seed %s: %v", ref, err)
		}
	}
	return w
}

// The four credential shapes the fleet holds.
func machine(clientID, org string) isoTok {
	return isoTok{owner: org, machine: true, azp: clientID, typ: authz.Program}
}

// noOrgScope is the refusal a plain member of an org the platform runs draws:
// such a membership opens nothing (authz.IsPlatformOrg), so the request carries
// no org and KMS is never asked.
const noOrgScope = "an org scope is required"

var (
	member   = isoTok{owner: "hanzo", username: "dev"}
	orgAdmin = isoTok{owner: "hanzo", username: "lead", isAdmin: true}
	// superAdmin is owner == "admin": a person whose own org is the reserved one,
	// who also belongs to hanzo. It acts in hanzo by selecting it (inHanzo).
	superAdmin = isoTok{owner: "hanzo", orgs: []string{authz.AdminOrg, "hanzo"}, username: "z"}
	inHanzo    = map[string]string{"X-Org-Id": "hanzo"}
)

// call issues one request as tok and returns the probe (status + body).
func (w grantWorld) call(t *testing.T, what, method, path string, tok isoTok, hdr map[string]string, body string) isoProbe {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+tok.mint(t, w.key))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := w.app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	p := isoProbe{what: what, status: resp.StatusCode, body: strings.TrimSpace(string(b))}
	t.Logf("PROBE %-60s → %d %s", what, p.status, p.body)
	return p
}

func (w grantWorld) read(t *testing.T, what, secret string, tok isoTok, hdr map[string]string) isoProbe {
	t.Helper()
	return w.call(t, what, "GET", "/v1/kms/secrets/"+secret, tok, hdr, "")
}

// refused asserts a 403 that NAMES the refusal and carries no plaintext at all.
func (p isoProbe) refused(t *testing.T, names string) isoProbe {
	t.Helper()
	p.noLeak(t, every...).isStatus(t, 403)
	if names != "" && !strings.Contains(p.body, names) {
		t.Fatalf("[%s]: the refusal must say %q: %s", p.what, names, p.body)
	}
	return p
}

// kmsRecords is every record the kms chain holds for one action.
func (w grantWorld) kmsRecords(t *testing.T, action string) []audit.Record {
	t.Helper()
	rows, _, err := w.chain.Query(context.Background(), audit.Filter{Action: action})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	return rows
}

// ── an app reads its grants ──────────────────────────────────────────────────

func TestGrant_AppReadsItsGrantedPaths(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newGrantWorld(t, grantDoc, true)
		op := machine("hanzo-operator", "hanzo")

		w.read(t, "operator reads /attendant/HANZO_TOKEN", "attendant/HANZO_TOKEN?env=prod", op, nil).isValue(t, valHanzoToken)
		w.read(t, "operator reads /pkg/htpasswd", "pkg/htpasswd?env=prod", op, nil).isValue(t, valHtpasswd)
		// The SDK spelling of the same address is the same grant.
		w.read(t, "operator reads the encoded spelling", "pkg%2Fhtpasswd?env=prod", op, nil).isValue(t, valHtpasswd)

		// A write grant covers put AND delete, and nothing it does not name.
		ci := machine("hanzo-ci", "hanzo")
		w.call(t, "CI rewrites its deploy token", "POST", "/v1/kms/secrets", ci, nil,
			`{"path":"/deploy","name":"GITHUB_TOKEN","env":"prod","value":"rotated"}`).isStatus(t, 200)
		w.read(t, "CI reads it back", "deploy/GITHUB_TOKEN?env=prod", ci, nil).isValue(t, "rotated")
		w.call(t, "CI deletes it", "DELETE", "/v1/kms/secrets/deploy/GITHUB_TOKEN?env=prod", ci, nil, "").isStatus(t, 200)
		w.read(t, "CI reads the deletion", "deploy/GITHUB_TOKEN?env=prod", ci, nil).isStatus(t, 404)

		// Every value read is on the trail /v1/admin/audit lists, a granted one as
		// the ordinary success it was: never a denial, never privileged.
		rows := w.kmsRecords(t, "kms.read")
		if len(rows) != 5 {
			t.Fatalf("want the five granted reads on the trail, got %s", mustJSON(t, rows))
		}
		for _, r := range rows {
			if r.Outcome.Result != "success" || r.Auth.IsAdmin || !strings.HasPrefix(r.Outcome.Reason, "grant covers holder ") {
				t.Fatalf("a granted read was recorded as %s", mustJSON(t, r))
			}
		}
	})
}

// ── an app is refused what it was not granted, in its own org ────────────────

func TestGrant_AppRefusedUngrantedPathInItsOwnOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newGrantWorld(t, grantDoc, true)
		op := machine("hanzo-operator", "hanzo")

		w.read(t, "operator reads the master key", "cloud/CLOUD_KMS_MASTER_KEY?env=prod", op, nil).
			refused(t, "kms: hanzo-operator may not read /cloud/CLOUD_KMS_MASTER_KEY@prod in org hanzo")
		w.read(t, "operator reads another key at a granted path", "pkg/OTHER?env=prod", op, nil).
			refused(t, "may not read /pkg/OTHER@prod")
		w.read(t, "operator reads a granted key in another env", "pkg/htpasswd?env=default", op, nil).
			refused(t, "may not read /pkg/htpasswd@default")
		// No existence oracle: an ungranted secret that exists and one that does not
		// answer identically.
		a := w.read(t, "operator reads an ungranted secret that exists", "cloud/CLOUD_KMS_MASTER_KEY?env=prod", op, nil)
		b := w.read(t, "operator reads an ungranted secret that does not", "cloud/NO_SUCH_KEY?env=prod", op, nil)
		if a.status != b.status {
			t.Fatalf("existence oracle: %d vs %d", a.status, b.status)
		}
		// A read grant is not a write grant.
		w.call(t, "operator overwrites a key it may read", "POST", "/v1/kms/secrets", op, nil,
			`{"path":"/pkg","name":"htpasswd","env":"prod","value":"planted"}`).
			refused(t, "kms: hanzo-operator may not write /pkg/htpasswd@prod in org hanzo")
		w.call(t, "operator deletes a key it may read", "DELETE", "/v1/kms/secrets/pkg/htpasswd?env=prod", op, nil, "").
			refused(t, "may not write /pkg/htpasswd@prod")
		w.read(t, "the refused write landed nowhere", "pkg/htpasswd?env=prod", op, nil).isValue(t, valHtpasswd)

		// A grant is (holder, ORG): the same clientId in another org holds nothing.
		w.read(t, "hanzo-operator minted by lux", "attendant/HANZO_TOKEN?env=prod", machine("hanzo-operator", "lux"), nil).
			refused(t, "in org lux")

		// Every denial is on the chain, naming the holder. The one granted read above
		// is there too, as the success it was.
		rows := w.kmsRecords(t, "kms.read")
		if len(rows) == 0 {
			t.Fatal("denials left no audit record")
		}
		for _, r := range rows {
			if r.Outcome.Result == "success" && strings.HasPrefix(r.Outcome.Reason, "grant covers holder ") {
				continue
			}
			if r.Outcome.Result != "deny" || r.Outcome.Status != 403 || r.Resource.Type != "kms.secret" {
				t.Fatalf("a denial recorded as %s", mustJSON(t, r))
			}
			for _, v := range every {
				if strings.Contains(mustJSON(t, r), v) {
					t.Fatalf("a denial record carries a plaintext: %s", mustJSON(t, r))
				}
			}
		}
		if !strings.Contains(mustJSON(t, rows), `"id":"hanzo:/cloud/CLOUD_KMS_MASTER_KEY@prod"`) ||
			!strings.Contains(mustJSON(t, rows), "hanzo-operator") {
			t.Fatalf("the denial record does not name the ref and holder: %s", mustJSON(t, rows))
		}
		verifyChain(t, w.chain)
	})
}

// ── people hold no values: membership and org admin open nothing ─────────────

func TestGrant_UserAndOrgAdminGetNoValues(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newGrantWorld(t, grantDoc, true)

		for _, who := range []struct {
			name string
			tok  isoTok
		}{{"member", member}, {"org admin", orgAdmin}} {
			// A plain member of hanzo has no org scope at all (authz.IsPlatformOrg), so
			// it is refused before the grant rule is asked; its admin reaches the rule.
			read, write := "kms: hanzo/"+who.tok.username+" may not read /attendant/HANZO_TOKEN@prod in org hanzo", "may not write"
			if who.tok.username == member.username {
				read, write = noOrgScope, noOrgScope
			}
			w.read(t, who.name+" reads a charted secret", "attendant/HANZO_TOKEN?env=prod", who.tok, nil).
				refused(t, read)
			w.call(t, who.name+" writes", "POST", "/v1/kms/secrets", who.tok, nil,
				`{"path":"/attendant","name":"HANZO_TOKEN","env":"prod","value":"planted"}`).refused(t, write)
			w.call(t, who.name+" deletes", "DELETE", "/v1/kms/secrets/attendant/HANZO_TOKEN?env=prod", who.tok, nil, "").
				refused(t, write)

		}
		// Neither lists the brand org's names (TestGrant_ListingTakesAdminOrAGrant): the
		// plain member has no org scope, and the admin of an org the platform runs is
		// not shown its names.
		for _, who := range []isoTok{member, orgAdmin} {
			says := "listing org hanzo's secrets takes an admin of the org or a grant there"
			if who.username == member.username {
				says = noOrgScope
			}
			w.call(t, "hanzo/"+who.username+" lists", "GET", "/v1/kms/secrets?path=/attendant&env=prod", who, nil, "").
				refused(t, says).noLeak(t, names...)
		}
		// The pair's control: the charted reader still reads it, unplanted.
		w.read(t, "operator reads the secret nobody planted over", "attendant/HANZO_TOKEN?env=prod",
			machine("hanzo-operator", "hanzo"), nil).isValue(t, valHanzoToken)
	})
}

// ── SuperAdmin reads, and every read is on the chain ─────────────────────────

func TestGrant_SuperAdminReadsWithAnAuditRecord(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newGrantWorld(t, grantDoc, true)

		w.read(t, "SuperAdmin reads the master key", "cloud/CLOUD_KMS_MASTER_KEY?env=prod", superAdmin, inHanzo).
			isValue(t, valMaster)
		w.read(t, "SuperAdmin switches into lux", "attendant/HANZO_TOKEN?env=prod", superAdmin,
			map[string]string{"X-Org-Id": "lux"}).isValue(t, valLux)
		w.call(t, "SuperAdmin writes", "POST", "/v1/kms/secrets", superAdmin, inHanzo,
			`{"path":"/cloud","name":"CLOUD_AI_API_KEY","env":"prod","value":"set-by-owner"}`).isStatus(t, 200)

		reads := w.kmsRecords(t, "kms.read")
		if len(reads) != 2 {
			t.Fatalf("want one record per SuperAdmin read, got %d: %s", len(reads), mustJSON(t, reads))
		}
		for _, want := range []struct{ org, id string }{
			{"hanzo", "hanzo:/cloud/CLOUD_KMS_MASTER_KEY@prod"},
			{"lux", "lux:/attendant/HANZO_TOKEN@prod"},
		} {
			found := false
			for _, r := range reads {
				found = found || (r.Actor.Org == want.org && r.Resource.Type == "kms.secret" && r.Resource.ID == want.id &&
					r.Outcome.Result == "success" && r.Auth.IsAdmin && strings.Contains(r.Outcome.Reason, "admin/z"))
			}
			if !found {
				t.Fatalf("no audit record names SuperAdmin admin/z reading %s: %s", want.id, mustJSON(t, reads))
			}
		}
		for _, v := range every {
			if strings.Contains(mustJSON(t, reads), v) {
				t.Fatalf("the audit record carries the value it audits: %s", mustJSON(t, reads))
			}
		}
		if writes := w.kmsRecords(t, "kms.write"); len(writes) != 1 || writes[0].Resource.ID != "hanzo:/cloud/CLOUD_AI_API_KEY@prod" {
			t.Fatalf("the SuperAdmin write is not on the chain: %s", mustJSON(t, writes))
		}
		verifyChain(t, w.chain)

		// The record names the CLIENT the token was issued to, so a SuperAdmin token
		// a confidential client minted is told apart from one obtained at the
		// owner's own keyboard.
		viaConsole := superAdmin
		viaConsole.azp, viaConsole.aud = "hanzo-console", []string{"hanzo-console"}
		w.read(t, "SuperAdmin through a console-minted token", "attendant/HANZO_TOKEN?env=prod", viaConsole, inHanzo).
			isValue(t, valHanzoToken)
		found := false
		for _, r := range w.kmsRecords(t, "kms.read") {
			found = found || (r.Resource.ID == "hanzo:/attendant/HANZO_TOKEN@prod" &&
				r.Outcome.Reason == "superadmin admin/z via hanzo-console")
		}
		if !found {
			t.Fatalf("the SuperAdmin record does not name the client that minted the token: %s", mustJSON(t, w.kmsRecords(t, "kms.read")))
		}

		// SuperAdmin needs an ACCESS token: an id_token names no holder.
		id := superAdmin
		id.idToken = true
		w.read(t, "SuperAdmin with an id_token", "cloud/CLOUD_KMS_MASTER_KEY?env=prod", id, inHanzo).
			refused(t, "a credential that names no holder may not read")
	})
}

// An access that cannot be recorded does not happen: a SuperAdmin's, and a
// granted application's too.
func TestGrant_SuperAdminWithNoChainIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newGrantWorld(t, grantDoc, false)
		w.read(t, "SuperAdmin, no audit trail", "cloud/CLOUD_KMS_MASTER_KEY?env=prod", superAdmin, inHanzo).
			noLeak(t, every...).isStatus(t, 503)
		w.read(t, "a granted operator, no audit trail", "attendant/HANZO_TOKEN?env=prod", machine("hanzo-operator", "hanzo"), nil).
			noLeak(t, every...).isStatus(t, 503)
	})
}

// ── default deny ─────────────────────────────────────────────────────────────

func TestGrant_DefaultDenyForUnknownIdentities(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newGrantWorld(t, grantDoc, true)
		for _, tc := range []struct {
			what string
			tok  isoTok
			says string
		}{
			{"a provisioned app no grant names", machine("hanzo-stranger", "hanzo"), "kms: hanzo-stranger may not read"},
			// An admin of hanzo reaches the rule (a plain member is refused at the org).
			{"a person's id_token", isoTok{owner: "hanzo", username: "lead", isAdmin: true, idToken: true}, "names no holder"},
			{"a person with no username", isoTok{owner: "hanzo", username: " ", isAdmin: true}, "names no holder"},
		} {
			w.read(t, tc.what, "attendant/HANZO_TOKEN?env=prod", tc.tok, nil).refused(t, tc.says)
		}

		// An org key acting for a member (IAM's as(), RFC 8693 `act`) carries the
		// member's claims and holds nothing: a person's grant is never reachable by
		// a key an org admin minted.
		acting := isoTok{owner: "hanzo", username: "lead", isAdmin: true, act: "hanzo/key-1"}
		w.read(t, "an org key acting for a member", "attendant/HANZO_TOKEN?env=prod", acting, nil).refused(t, "names no holder")
	})
}

// No document has ever loaded — the pod rolled before its mount existed, or
// every mount it saw was refused and nothing was kept. This release changes no
// verdict until a declaration says to, so the rule each caller had decides and
// the gap is measured on the chain rather than served as an outage.
func TestGrant_NoDeclarationMeasures(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		bare := newGrantWorld(t, "", true)
		op := machine("hanzo-operator", "hanzo")
		bare.read(t, "operator, no grant document", "attendant/HANZO_TOKEN?env=prod", op, nil).isValue(t, valHanzoToken)
		bare.read(t, "member, no grant document", "attendant/HANZO_TOKEN?env=prod", member, nil).refused(t, noOrgScope)
		bare.call(t, "member writes, no grant document (old rule)", "POST", "/v1/kms/secrets", member, nil,
			`{"name":"X","env":"prod","value":"v"}`).isStatus(t, 403)
		bare.read(t, "SuperAdmin, no grant document", "attendant/HANZO_TOKEN?env=prod", superAdmin, inHanzo).
			isValue(t, valHanzoToken)

		rows := bare.kmsRecords(t, "kms.read")
		var would, super int
		for _, r := range rows {
			switch {
			case strings.HasPrefix(r.Outcome.Reason, "would-deny: no grant covers holder "):
				would++
			case strings.HasPrefix(r.Outcome.Reason, "superadmin "):
				super++
			}
		}
		if would != 1 || super != 1 {
			t.Fatalf("want the operator's would-deny read and one SuperAdmin read on the chain, got %d and %d: %s", would, super, mustJSON(t, rows))
		}
	})
}

// The probe names the declaration that is live, and nothing when none is.
func TestGrant_HealthNamesTheLiveDeclaration(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newGrantWorld(t, grantDoc, true)
		req := httptest.NewRequest("GET", "/v1/kms/health", nil)
		resp, err := w.app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		body := decode(t, resp.Body)
		if d, _ := body["policy"].(string); len(d) != 64 || body["enforcing"] != true {
			t.Fatalf("health = %v, want the document's sha256 and enforcing", body)
		}
		if _, ok := body["refused"]; ok {
			t.Fatalf("health reports a refused mount when the mount is what decides: %v", body)
		}
		bare := newGrantWorld(t, "", false)
		resp, err = bare.app.Test(httptest.NewRequest("GET", "/v1/kms/health", nil), zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		body = decode(t, resp.Body)
		if _, ok := body["policy"]; ok || body["enforcing"] != false || body["refused"] != "absent" {
			t.Fatalf("health with no declaration = %v, want no policy, not enforcing, the mount absent", body)
		}
	})
}

// ── rollout: `enforce: false` keeps the old verdict and logs the new one ─────

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestGrant_EnforceFalseKeepsTheOldVerdictAndLogsTheNewOne(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var logs syncBuf
		w := newGrantWorld(t, "enforce: false\n"+grantDoc, true, &logs)
		op := machine("hanzo-operator", "hanzo")

		// The old rule decides for an application acting as itself. A person is not
		// measured: ungranted, an org admin reads and writes nothing, and a plain
		// member of hanzo is refused at the org before KMS is asked.
		w.read(t, "member reads", "cloud/CLOUD_KMS_MASTER_KEY?env=prod", member, nil).refused(t, noOrgScope)
		w.read(t, "org admin reads", "cloud/CLOUD_KMS_MASTER_KEY?env=prod", orgAdmin, nil).refused(t, "may not read")
		w.read(t, "operator reads outside its grant (old rule)", "cloud/CLOUD_KMS_MASTER_KEY?env=prod", op, nil).isValue(t, valMaster)
		w.read(t, "operator reads its grant", "pkg/htpasswd?env=prod", op, nil).isValue(t, valHtpasswd)
		w.call(t, "member writes (old rule)", "POST", "/v1/kms/secrets", member, nil,
			`{"name":"X","env":"prod","value":"v"}`).isStatus(t, 403)
		w.call(t, "org admin writes", "POST", "/v1/kms/secrets", orgAdmin, nil,
			`{"name":"X","env":"prod","value":"v"}`).isStatus(t, 403)

		out := logs.String()
		for _, want := range []string{
			`"holder":"hanzo-operator","org":"hanzo","verb":"read","ref":"/cloud/CLOUD_KMS_MASTER_KEY@prod","verdict":"would-deny"`,
			`"holder":"hanzo-operator","org":"hanzo","verb":"read","ref":"/pkg/htpasswd@prod","verdict":"grant"`,
			`"holder":"hanzo/lead","org":"hanzo","verb":"write","ref":"/X@prod","verdict":"deny"`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("the access log is missing %s", want)
			}
		}
		// would-deny is a measurement, and enforcement is decided on it, so it is
		// on the chain — it has to survive the restarts a log line does not — as an
		// act that SUCCEEDED, never as a denial. A person's refusal is a denial. A
		// granted read is the success it was.
		rows := w.kmsRecords(t, "kms.read")
		if len(rows) != 3 {
			t.Fatalf("want the operator's would-deny, its granted read and the org admin's denial on the chain, got %s", mustJSON(t, rows))
		}
		for _, r := range rows {
			if r.Resource.ID == "hanzo:/pkg/htpasswd@prod" {
				if r.Outcome.Result != "success" || r.Outcome.Reason != "grant covers holder hanzo-operator" {
					t.Fatalf("the operator's granted read recorded as %s", mustJSON(t, r))
				}
				continue
			}
			if r.Resource.ID != "hanzo:/cloud/CLOUD_KMS_MASTER_KEY@prod" {
				t.Fatalf("a read recorded as %s", mustJSON(t, r))
			}
			switch {
			case strings.Contains(r.Outcome.Reason, "holder hanzo-operator"):
				if r.Outcome.Result != "success" || r.Outcome.Status != 200 ||
					!strings.HasPrefix(r.Outcome.Reason, "would-deny: no grant covers holder ") {
					t.Fatalf("the operator's would-deny recorded as %s", mustJSON(t, r))
				}
			case r.Outcome.Result != "deny":
				t.Fatalf("the org admin's read recorded as %s", mustJSON(t, r))
			}
		}
		if writes := w.kmsRecords(t, "kms.write"); len(writes) != 1 || writes[0].Outcome.Result != "deny" {
			t.Fatalf("the org admin's refused write is not on the chain as a denial: %s", mustJSON(t, writes))
		}
		verifyChain(t, w.chain)
		for _, v := range every {
			if strings.Contains(out, v) {
				t.Fatalf("the access log carries a plaintext %q", v)
			}
		}
	})
}

// ── the custodian: cloud's own refs need no grant ────────────────────────────

// The process holding the master key reads through its in-process client with
// no grant at all, whatever the document says: a grant on the one process that
// can open the store would restrict nothing.
func TestGrant_TheCustodianKeepsItsInternalRefs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newGrantWorld(t, "grants: []\n", true)
		for ref, want := range map[string]string{
			"orgs/hanzo/cloud/CLOUD_KMS_MASTER_KEY@prod": valMaster,
			"orgs/lux/attendant/HANZO_TOKEN@prod":        valLux,
		} {
			got, err := w.deps.KMS.GetSecret(context.Background(), ref)
			if err != nil || string(got) != want {
				t.Fatalf("custodian read %s = %q %v", ref, got, err)
			}
		}
		w.read(t, "the same secret over REST, no grant", "cloud/CLOUD_KMS_MASTER_KEY?env=prod",
			machine("hanzo-cloud", "hanzo"), nil).refused(t, "kms: hanzo-cloud may not read")
	})
}
