package main

import (
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/credz/launch"
)

// devKey is a 32-zero-byte base64 key — the dev posture the suite runs under. It
// stands in here for the production CLOUD_KMS_MASTER_KEY_REF the host must never
// leak to a generic child.
const devKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// childFullEnv reconstructs the environment zip actually hands a spawned child —
// append(os.Environ(), Plugin.Env...) — AFTER the host's boot scrub has run, so
// the assertions are about the exact bytes the child will read, not a proxy for
// them. (zip/load.go: cmd.Env = append(append(os.Environ(), spec.Env...), …).)
func childFullEnv(app, secret, rootKey string) []string {
	return append(os.Environ(), childEnv(app, secret, rootKey)...)
}

// lookup returns the value of the FIRST NAME= entry for key, mirroring how the
// child's own os.Getenv resolves a duplicated variable (first occurrence wins).
func lookup(env []string, key string) (string, bool) {
	for _, e := range env {
		if name, val, ok := strings.Cut(e, "="); ok && name == key {
			return val, true
		}
	}
	return "", false
}

// TestHostScopesCredentials_RootKeyNeverReachesAGenericChild is the security
// property the peer flagged ACTIVE: because zip builds every child's environment
// from the host's os.Environ(), a host that keeps CLOUD_KMS_MASTER_KEY_REF hands
// the root key to EVERY child — each then resolves the Root posture and can open
// any store, the exact exposure credz was built to prevent. The host must instead
// scrub the key from its own environment and hand it to the kms broker alone.
func TestHostScopesCredentials_RootKeyNeverReachesAGenericChild(t *testing.T) {
	t.Setenv(launch.RootEnv, devKey)

	secret, rootKey := stampAndScrub()

	// The host captured the key for the broker, and then removed it from its OWN
	// environment — so os.Environ(), which zip copies into every child, no longer
	// carries it.
	if rootKey != devKey {
		t.Fatalf("stampAndScrub captured rootKey=%q, want %q", rootKey, devKey)
	}
	if v := os.Getenv(launch.RootEnv); v != "" {
		t.Fatalf("root key still in the host's own environment after the scrub: %q — every child would inherit it", v)
	}

	// A GENERIC child (dns): its full spawn environment carries a scoped token and
	// NO root key.
	dns := childFullEnv("dns", secret, rootKey)
	if v, ok := lookup(dns, launch.RootEnv); ok {
		t.Fatalf("a spawned dns child's environment contains %s=%q — the root-key leak is ACTIVE", launch.RootEnv, v)
	}
	tok, ok := lookup(dns, launch.TokenEnv)
	if !ok {
		t.Fatalf("a spawned dns child's environment has no %s — it cannot prove its identity to the broker", launch.TokenEnv)
	}
	if app := launch.Open(secret, tok); app != "dns" {
		t.Fatalf("dns child %s = %q opens to %q under the launcher secret, want \"dns\"", launch.TokenEnv, tok, app)
	}

	// The BROKER child (kms): carries the root key it needs to be Root, the launch
	// secret it verifies tokens with, and its own scoped token — the ONE child the
	// root key reaches.
	kms := childFullEnv(launch.Broker, secret, rootKey)
	if v, _ := lookup(kms, launch.RootEnv); v != devKey {
		t.Fatalf("the kms broker child is missing the root key (got %q) — it cannot unseal the store and broker credentials", v)
	}
	if v, _ := lookup(kms, launch.SecretEnv); v != secret {
		t.Fatalf("the kms broker child is missing the launch secret — it cannot verify any child's token")
	}
	if v, _ := lookup(kms, launch.TokenEnv); launch.Open(secret, v) != launch.Broker {
		t.Fatalf("the kms broker child token %q does not open to %q", v, launch.Broker)
	}
}

// TestForward_OnlyNonEmptyFlagsOverrideEnv pins the flag-forwarding contract: a
// non-empty operator flag becomes the CLOUD_* env the children read, but an empty
// one (an unset helm value rendered as `--iam-issuer=`) must NOT clobber a value
// already in the environment — otherwise the host would blank a deployment's
// pinned issuer and every token would fail validation.
func TestForward_OnlyNonEmptyFlagsOverrideEnv(t *testing.T) {
	t.Setenv("CLOUD_BRAND", "")
	t.Setenv("CLOUD_IAM_ISSUER", "https://lux.id") // pinned by the environment

	forward(map[string]string{
		"CLOUD_BRAND":      "lux", // non-empty flag → published
		"CLOUD_IAM_ISSUER": "",    // empty flag → must not clobber the pinned value
	})

	if got := os.Getenv("CLOUD_BRAND"); got != "lux" {
		t.Fatalf("CLOUD_BRAND = %q, want \"lux\" — a non-empty --brand must reach the children", got)
	}
	if got := os.Getenv("CLOUD_IAM_ISSUER"); got != "https://lux.id" {
		t.Fatalf("CLOUD_IAM_ISSUER = %q, want it untouched — an empty --iam-issuer must not blank a pinned issuer", got)
	}
}
