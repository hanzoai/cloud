package scrub

import (
	"strings"
	"testing"
)

func TestPublicResourceIDsAreNotRedacted(t *testing.T) {
	for _, id := range []string{
		"sess_077ebdeb5444162f21dfc10b4306db68",
		"sess_0123456789abcdef0123456789abcdef",
		"m_2a8bb714a84c1c5b95b1a2fa",
	} {
		if got := Token(id); got != id {
			t.Errorf("Token(%q) = %q, want unchanged public resource id", id, got)
		}
	}

	msg := "coding: the run sess_077ebdeb5444162f21dfc10b4306db68 is running in m_2a8bb714a84c1c5b95b1a2fa"
	if got := Text(msg); got != msg {
		t.Errorf("Text(%q) = %q, want unchanged", msg, got)
	}
}

func TestSecretsDisguisedWithResourcePrefixAreStillRedacted(t *testing.T) {
	for _, sec := range []string{
		"sess_AbCdEfGhIjKlMnOpQrStUvWxYz012345",
		"sess_sk-live-secret0123456789abcdef",
		"sess_077ebdeb5444162f21dfc10b4306db68_extra_secret",
		"m_AbCdEfGhIjKlMnOpQrStUvWx",
		"m_0123456789abcdef0123456789abcdef0123456789abcdef",
		"deadbeefcafe0123456789abcdef0123456789abcdef0123",
		"sk-live-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789xy",
	} {
		if got := Token(sec); got != "[REDACTED-TOKEN]" {
			t.Errorf("Token(%q) = %q, want [REDACTED-TOKEN]", sec, got)
		}
	}

	mixed := "run sess_077ebdeb5444162f21dfc10b4306db68 leaked sk-live-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789xy"
	got := Text(mixed)
	if !strings.Contains(got, "sess_077ebdeb5444162f21dfc10b4306db68") {
		t.Fatalf("public session id was over-redacted: %q", got)
	}
	if strings.Contains(got, "sk-live-") || !strings.Contains(got, "[REDACTED-TOKEN]") {
		t.Fatalf("real secret was not redacted: %q", got)
	}
}
