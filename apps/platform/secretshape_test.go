package platform

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ── the values a key-NAME regex missed ───────────────────────────────────────

// Every one of these reached the EnvJSON column and the pod spec in PLAINTEXT
// because the client-side key pattern did not match. The server decides now.
func TestMustSealCatchesTheCredentialsTheClientPatternMissed(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"STRIPE_SK", "sk_live_51H8xQ2LkdIwHu7ixR0Nn7Yq2vB3mZ9pK"},
		{"SK_LIVE", "sk_live_51H8xQ2LkdIwHu7ixR0Nn7Yq2vB3mZ9pK"},
		{"GH_PAT", "ghp_16C7e42F292c6912E7710c838347Ae178B4a"},
		{"GH_PAT_FINE", "github_pat_11ABCDEFG0abcdefghijkl_ABCDEFGHIJKLMNOP"},
		{"DB_PASS", "hunter2"},
		{"PGPASS", "hunter2"},
		{"SMTP_PASS", "s3cret"},
		{"HMAC", "0123456789abcdef0123456789abcdef"},
		{"TLS_CERT", "-----BEGIN CERTIFICATE-----\nMIIB..."},
		{"TLS_KEY", "-----BEGIN RSA PRIVATE KEY-----\nMIIE..."},
		{"GCP_SA_JSON", `{"type":"service_account","private_key":"-----BEGIN PRIVATE KEY-----"}`},
		{"SLACK_BOT", "xoxb-2154537400-2158532783-A1b2C3d4E5f6G7h8I9j0K1l2"},
		{"AWS_ACCESS", "AKIAIOSFODNN7EXAMPLE"},
		{"GOOGLE", "AIzaSyD-abcdefghijklmnopqrstuvwxyz12345"},
		{"DATABASE_URL", "postgres://app:sup3rs3cret@db.internal:5432/prod"},
		{"MONGO", "mongodb+srv://u:p4ssw0rd@cluster0.abcd.mongodb.net/db"},
		{"OPENAI", "sk-proj-aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789"},
		{"SENDGRID", "SG.aBcDeFgHiJkLmNoPqRs.tUvWxYz0123456789abcdefghij"},
		{"DIGITALOCEAN", "dop_v1_0123456789abcdef0123456789abcdef0123456789abcdef"},
		{"HF", "hf_AbCdEfGhIjKlMnOpQrStUvWxYz0123"},
		{"NPM", "npm_AbCdEfGhIjKlMnOpQrStUvWxYz0123456"},
		// No recognisable issuer prefix and an innocuous name: the entropy tier.
		{"SESSION_ROTATION", "Xk7mQ2pR9vT4wY6zA1bC3dE5fG8hJ0kL"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			if !mustSeal(tc.key, tc.value) {
				t.Errorf("PLAINTEXT CREDENTIAL: %s would be stored in the DB and inlined into the pod spec", tc.key)
			}
		})
	}
}

// ── configuration must stay readable ────────────────────────────────────────

// Over-sealing is safe but not free: a sealed value reads back masked, so an
// operator can no longer see it. Ordinary configuration must not trip the rule.
func TestMustSealLeavesOrdinaryConfigurationAlone(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"PORT", "3000"},
		{"NODE_ENV", "production"},
		{"LOG_LEVEL", "debug"},
		{"REPLICAS", "3"},
		{"TZ", "America/Los_Angeles"},
		{"PUBLIC_URL", "https://app.acme.hanzo.app"},
		{"API_BASE", "https://api.hanzo.ai/v1"},
		{"CORS_ORIGINS", "https://a.example.com,https://b.example.com"},
		{"SORT_KEY", "created_at"},
		{"PARTITION_KEY", "tenant_id"},
		{"IDEMPOTENCY_KEY_HEADER", "X-Idempotency-Key"},
		{"CONFIG_PATH", "/etc/app/config.yaml"},
		{"FEATURE_FLAGS", "newnav.checkout.search"},
		{"GOMEMLIMIT", "9GiB"},
		{"IMAGE", "ghcr.io/hanzoai/web"},
		{"DATABASE_URL", "postgres://app@db.internal:5432/prod"}, // no password
		{"VERSION", "v1.801.485"},
		{"DESCRIPTION", "the customer facing storefront"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			if mustSeal(tc.key, tc.value) {
				t.Errorf("ordinary configuration %s=%q was sealed; it now reads back masked", tc.key, tc.value)
			}
		})
	}
}

// ── the seal actually happens, end to end ───────────────────────────────────

// The proof that matters: an UNMARKED credential must leave sealSecretEnv with
// no value to persist, and its plaintext must be in KMS instead.
func TestUnmarkedCredentialIsSealedToKMSAndNeverPersisted(t *testing.T) {
	k := newFakeKMS()
	s := serviceWithKMS(k)
	const plaintext = "sk_live_51H8xQ2LkdIwHu7ixR0Nn7Yq2vB3mZ9pK"

	out, err := sealSecretEnv(s, context.Background(), "acme", "web", []EnvVarJSON{
		{Key: "STRIPE_SK", Value: plaintext, Secret: false}, // NOT marked by the client
		{Key: "PORT", Value: "3000", Secret: false},
	})
	if err != nil {
		t.Fatalf("sealSecretEnv: %v", err)
	}
	var sealed, plain int
	for _, e := range out {
		switch e.Key {
		case "STRIPE_SK":
			sealed++
			if !e.Secret {
				t.Error("the credential was not promoted to secret, so it renders inline in the pod spec")
			}
			if e.Value != "" {
				t.Errorf("PLAINTEXT PERSISTED: STRIPE_SK still carries %q for the EnvJSON column", e.Value)
			}
		case "PORT":
			plain++
			if e.Secret || e.Value != "3000" {
				t.Errorf("ordinary configuration was altered: %+v", e)
			}
		}
	}
	if sealed != 1 || plain != 1 {
		t.Fatalf("unexpected output: %+v", out)
	}
	// And the value is in KMS, under this org and app.
	got, err := k.GetSecret(context.Background(), kmsSecretRef("acme", "web", "STRIPE_SK"))
	if err != nil {
		t.Fatalf("the credential did not reach KMS: %v", err)
	}
	if string(got) != plaintext {
		t.Fatalf("KMS holds %q, want the plaintext", got)
	}
	// It is now a secretKeyRef key, so the pod reads it from the managed Secret.
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	keys := secretEnvKeys(string(b))
	if len(keys) != 1 || keys[0] != "STRIPE_SK" {
		t.Fatalf("secretEnvKeys = %v, want [STRIPE_SK]", keys)
	}
}

// Fail-closed holds for a promoted secret too: with no KMS, the whole set is
// refused and NOTHING is stored — a detected credential must never fall back to
// plaintext, which was the entire defect.
func TestAPromotedSecretFailsClosedWithoutKMS(t *testing.T) {
	s := serviceWithKMS(nil)
	_, err := sealSecretEnv(s, context.Background(), "acme", "web", []EnvVarJSON{
		{Key: "GCP_SA_JSON", Value: `{"private_key":"-----BEGIN PRIVATE KEY-----"}`, Secret: false},
	})
	if err == nil {
		t.Fatal("a detected credential was accepted with no KMS to seal it into")
	}
	if strings.Contains(err.Error(), "BEGIN PRIVATE KEY") {
		t.Fatal("the refusal echoed the credential")
	}
}

// A client marking something secret is still honoured — the flag may only ADD.
func TestTheClientFlagStillAddsSecrecy(t *testing.T) {
	k := newFakeKMS()
	out, err := sealSecretEnv(serviceWithKMS(k), context.Background(), "acme", "web", []EnvVarJSON{
		{Key: "LOG_LEVEL", Value: "debug", Secret: true}, // ordinary, but marked
	})
	if err != nil {
		t.Fatalf("sealSecretEnv: %v", err)
	}
	if len(out) != 1 || !out[0].Secret || out[0].Value != "" {
		t.Fatalf("a client-marked entry was not sealed: %+v", out)
	}
}

// An empty value is the write-only round trip ("keep what is already sealed")
// and must not be re-classified by shape — there is no value to inspect.
func TestAnEmptyValueIsNeverPromoted(t *testing.T) {
	out, err := sealSecretEnv(serviceWithKMS(nil), context.Background(), "acme", "web", []EnvVarJSON{
		{Key: "API_KEY", Value: "", Secret: false},
	})
	if err != nil {
		t.Fatalf("sealSecretEnv: %v", err)
	}
	if len(out) != 1 || out[0].Secret {
		t.Fatalf("an empty value was promoted, which would require a KMS seal of nothing: %+v", out)
	}
}

// Red's eight, against the OPERATOR lane's classifier. That lane writes a
// database column it can rewrite, so a heuristic is defensible there — but the
// named misses are closed regardless, since they cost nothing.
func TestOperatorLaneCatchesRedsEight(t *testing.T) {
	for _, tc := range []struct{ key, value, why string }{
		{"PGPASSWORD", "s3cr3t", "libpq's own variable; one token to any splitter"},
		{"DB_PW", "s3cr3t", "`pw` was not a token"},
		{"ADMIN_PW", "Tr0ub4dor&3", "symbol-rich: a stronger password was MORE likely plaintext"},
		{"APP_PASSPHRASE", "correct horse battery staple", "spaces, so no token shape"},
		{"KUBECONFIG", "apiVersion: v1\nclusters: []", "cluster-admin, no PEM armour"},
		{"MFA_SEED", "JBSWY3DPEHPK3PXP", "base32, short, low entropy"},
		{"SESSION_TOKEN", "abc", "short but unambiguously named"},
		{"USER_PASSWORD_HASH", "x", "substring, not a token"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			if !mustSeal(tc.key, tc.value) {
				t.Errorf("MISSED (%s): %s would be stored in plaintext", tc.why, tc.key)
			}
		})
	}
	// The symbol rule must not swallow ordinary configuration.
	for _, tc := range []struct{ key, value string }{
		{"TZ", "America/Los_Angeles"},
		{"IMAGE", "ghcr.io/hanzoai/web"},
		{"HEADER", "X-Idempotency-Key"},
		{"CSP", "default-src 'self'; script-src 'self'"},
		{"FLAGS", "newnav.checkout.search"},
	} {
		if mustSeal(tc.key, tc.value) {
			t.Errorf("over-seal: %s=%q now reads back masked", tc.key, tc.value)
		}
	}
}
