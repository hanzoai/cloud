// Copyright © 2026 Hanzo AI. MIT License.

package organization

import "testing"

func TestIsSecretLikeName(t *testing.T) {
	// Real leaked-key shapes (incident 2026-07-02) must be rejected so they can
	// never become an org name / tenant id.
	secretLike := []string{
		"sk-ant-api03-cPXc0f", // Anthropic
		"sk-proj-7qGaB9rn",    // OpenAI
		"sk-do-u_6ppglS1w",    // DigitalOcean
		"sk-hz-0a2d6440-c",    // Hanzo
		"hk-feb5b4b27e2c0",    // Hanzo
		"SK-HZ-UPPER",         // case-insensitive
		"  sk-hz-trimmed",     // leading whitespace
	}
	for _, s := range secretLike {
		if !IsSecretLikeName(s) {
			t.Errorf("IsSecretLikeName(%q) = false, want true", s)
		}
	}

	// Real org identifiers must be allowed — including slugs that merely start
	// with the same letters as a key prefix.
	orgLike := []string{
		"hanzo", "adnexus", "maxpower", "iam-user", "system", "lux",
		"2d4d67ab-30f1-474e-b81f-f60461852259", // UUID
		"skunkworks",                           // starts with "sk" but not "sk-"
		"hkust",                                // starts with "hk" but not "hk-"
		"",                                     // empty is not secret-like (handled by callers)
	}
	for _, o := range orgLike {
		if IsSecretLikeName(o) {
			t.Errorf("IsSecretLikeName(%q) = true, want false", o)
		}
	}
}
