package principals

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/iam"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// agent defines an agent in org, spawned by parent when one is named.
func (e *env) agent(org, name, parent string) string {
	e.t.Helper()
	body := map[string]any{"name": name, "cap_micro_usd": 1_000_000, "max_task_micro_usd": 100_000, "period": "month"}
	if parent != "" {
		body["parent"] = parent
	}
	var a struct {
		ID      string   `json:"id"`
		Parent  string   `json:"parent"`
		Lineage []string `json:"lineage"`
	}
	e.must(201, http.MethodPost, "/v1/agent", member(org), body, &a)
	return a.ID
}

// A spawned agent answers to its parent's principal, the statement says so, and a
// verifier checks it offline against the published keys — and refuses one
// that was forged.
func TestAttestation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		root := e.agent("acme", "scout", "")
		child := e.agent("acme", "scout-1", root)
		grandchild := e.agent("acme", "scout-1a", child)

		// No key yet: nothing is signed, and the refusal says who provisions one.
		if code, b := e.raw(http.MethodGet, "/v1/principal/agents/"+grandchild, member("acme"), nil); code != http.StatusServiceUnavailable || !strings.Contains(string(b), "SuperAdmin") {
			t.Fatalf("attest with no key = %d %s, want 503 naming who provisions one", code, b)
		}
		// Only a platform reviewer rotates the key.
		if code, _ := e.raw(http.MethodPost, "/v1/principal/keys", admin("acme"), nil); code != http.StatusForbidden {
			t.Fatalf("an org admin rotated the attestation key: %d", code)
		}
		var keys iam.JWKS
		e.must(201, http.MethodPost, "/v1/principal/keys", reviewer("admin"), nil, &keys)
		if len(keys.Keys) != 1 || keys.Keys[0].Kty != "OKP" || keys.Keys[0].Crv != "Ed25519" {
			t.Fatalf("keys = %+v", keys)
		}

		var a iam.Attestation
		e.must(200, http.MethodGet, "/v1/principal/agents/"+grandchild, member("acme"), nil, &a)
		if a.Org != "acme" || len(a.Parents) != 2 || a.Parents[0].ID != child || a.Parents[1].ID != root {
			t.Fatalf("attestation = %+v, want acme and the chain child, root", a)
		}
		// Offline: the published set, fetched once without a credential.
		var published iam.JWKS
		e.must(200, http.MethodGet, "/v1/principal/keys", who{}, nil, &published)
		c, err := iam.Verify(a.Statement, published, time.Now())
		if err != nil {
			t.Fatalf("a genuine statement did not verify: %v", err)
		}
		if c.Org != "acme" || c.Sub != grandchild || !slices.Equal(c.Parents, []string{child, root}) {
			t.Fatalf("claims = %+v", c)
		}

		parts := strings.Split(a.Statement, ".")
		forge := func(what string, statement string) {
			t.Helper()
			if _, err := iam.Verify(statement, published, time.Now()); err == nil {
				t.Fatalf("a forged statement verified: %s", what)
			}
		}
		// Another org claimed, the signature kept.
		var claims iam.Claims
		raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
		_ = json.Unmarshal(raw, &claims)
		claims.Org = "globex"
		body, _ := json.Marshal(claims)
		forge("org rewritten", parts[0]+"."+b64(body)+"."+parts[2])
		// Signed by a key the set does not publish, under the published kid.
		_, other, _ := ed25519.GenerateKey(rand.Reader)
		forge("foreign key", parts[0]+"."+parts[1]+"."+b64(ed25519.Sign(other, []byte(parts[0]+"."+parts[1]))))
		// alg none, the oldest trick.
		none, _ := json.Marshal(iam.Header{Alg: "none", Typ: iam.Typ, Kid: a.Kid})
		forge("alg none", b64(none)+"."+parts[1]+".")
		// Past its validity.
		if _, err := iam.Verify(a.Statement, published, time.Now().Add(statementTTL+time.Minute)); err == nil {
			t.Fatal("an expired statement verified")
		}

		// A rotation retires the key and keeps it published: the old statement still
		// verifies, and new ones are signed by the new key.
		e.must(201, http.MethodPost, "/v1/principal/keys", reviewer("admin"), nil, &keys)
		if len(keys.Keys) != 2 {
			t.Fatalf("after rotation the set holds %d keys, want 2", len(keys.Keys))
		}
		if _, err := iam.Verify(a.Statement, keys, time.Now()); err != nil {
			t.Fatalf("a statement signed before the rotation no longer verifies: %v", err)
		}
		var b iam.Attestation
		e.must(200, http.MethodGet, "/v1/principal/agents/"+root, member("acme"), nil, &b)
		if b.Kid == a.Kid {
			t.Fatal("the retired key still signs")
		}
		if !slices.Contains(e.actions("admin"), "principal.key.rotated") {
			t.Fatal("the rotation is not on the trail")
		}

		// Another org's agent is not found, and an agent cannot be spawned from one.
		if code, _ := e.raw(http.MethodGet, "/v1/principal/agents/"+root, member("globex"), nil); code != http.StatusNotFound {
			t.Fatalf("globex attested acme's agent: %d", code)
		}
		if code, _ := e.raw(http.MethodPost, "/v1/agent", member("globex"), map[string]any{"name": "mole",
			"cap_micro_usd": 1_000_000, "max_task_micro_usd": 100_000, "period": "month", "parent": root}); code != http.StatusNotFound {
			t.Fatalf("globex spawned an agent under acme's: %d", code)
		}
	})
}
