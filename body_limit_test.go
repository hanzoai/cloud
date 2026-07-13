package cloud

import "testing"

// The context window is a BODY-SIZE fact, not just a model fact.
//
// A chat request carries its whole prompt in the request body, so the edge's
// BodyLimit is a hard ceiling on the context window no matter what the model
// catalog advertises. The zip/fiber framework default is 4 MiB; a 1M-token
// prompt serializes to roughly 4.3 MB of JSON (measured on prod: 2 MB of body
// carried 466,148 prompt tokens, ~4.5 bytes/token). Left at the default, every
// 1M-context route — deepseek-v4-pro, and anything glm-5.2 overflows into once
// a prompt passes its 262,144 cap — was unreachable: fasthttp rejected the body
// before any handler ran and answered the opaque 400 "Error when parsing
// request", which reads like a malformed payload rather than a size cap.
//
// This pins the ceiling above a real 1M-token prompt so the regression cannot
// come back silently.

// oneMillionTokenBody is a conservative byte estimate for a 1M-token prompt.
// Measured ~4.5 bytes/token; 4.3 keeps the floor conservative.
const oneMillionTokenBody = 1_000_000 * 43 / 10 // ~4.3 MB

// zipFrameworkDefault is the zip/fiber BodyLimit applied when Config.BodyLimit
// is left at 0 (zip.go: `if cfg.BodyLimit == 0 { cfg.BodyLimit = 4 << 20 }`) —
// the exact cap that made 1M context unreachable.
const zipFrameworkDefault = 4 << 20

// LoadConfig registers process flags, so it may only be called ONCE per test
// binary (a second call panics with "flag redefined"). Both invariants are
// therefore asserted against a single load, and the env-override path is
// exercised through the getenvInt helper that LoadConfig itself uses.
func TestBodyLimit_FitsAMillionTokenPrompt(t *testing.T) {
	cfg := LoadConfig()

	if cfg.BodyLimit == 0 || cfg.BodyLimit == zipFrameworkDefault {
		t.Fatalf("BodyLimit = %d — this is the zip/fiber 4 MiB default, the exact cap "+
			"that made 1M context unreachable", cfg.BodyLimit)
	}

	if cfg.BodyLimit <= oneMillionTokenBody {
		t.Fatalf("BodyLimit = %d bytes, which cannot hold a 1M-token prompt (~%d bytes); "+
			"the 1M-context models would 400 with fasthttp's opaque "+
			"%q", cfg.BodyLimit, oneMillionTokenBody, "Error when parsing request")
	}
}

func TestBodyLimit_EnvOverride(t *testing.T) {
	t.Setenv("GATEWAY_BODY_LIMIT", "33554432") // 32 MiB

	if got := getenvInt("GATEWAY_BODY_LIMIT", 16<<20); got != 32<<20 {
		t.Fatalf("getenvInt(GATEWAY_BODY_LIMIT) = %d, want %d — the limit must stay tunable", got, 32<<20)
	}
}

func TestBodyLimit_DefaultsWhenEnvUnset(t *testing.T) {
	t.Setenv("GATEWAY_BODY_LIMIT", "")

	if got := getenvInt("GATEWAY_BODY_LIMIT", 16<<20); got != 16<<20 {
		t.Fatalf("getenvInt fallback = %d, want the 16 MiB default", got)
	}
}
