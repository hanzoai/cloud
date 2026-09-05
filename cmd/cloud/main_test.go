package main

import (
	"os"
	"testing"
)

// devKey is a 32-zero-byte base64 key — the dev posture the suite runs under. It
// stands in here for the production CLOUD_KMS_MASTER_KEY_REF the host must never
// leak to a generic child.

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

func TestServesConsole(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"", false},
		{"localhost", true},
		{"127.0.0.1", true},
		{"[::1]", true},
		{"app.local", true},
		{"app.localhost", true},
		{"console.hanzo.ai", true},
		{"cloud.hanzo.ai", true},
		{"platform.hanzo.ai", true},
		{"platform2.hanzo.ai", true},
		{"console2.hanzo.ai", true},
		{"platform.lux.network", true},
		{"oci.hanzo.ai", false},
		{"pkg.hanzo.ai", false},
		{"ci.hanzo.ai", false},
		{"example.com", false},
	} {
		if got := servesConsole(tc.host); got != tc.want {
			t.Errorf("servesConsole(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}
