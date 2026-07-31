package tools

import (
	"strings"
	"testing"
)

// The build gate is the whole value of the builder: a plugin in the store is one
// the runtime has already loaded. These exercise it directly (no HTTP, no store)
// so they run anywhere — the handler around it adds auth and persistence, not
// build semantics.

func TestBundleSourceAcceptsAConnector(t *testing.T) {
	// Minimal shape the shim can load: a CommonJS export with one action. Kept
	// framework-free so the test proves the PIPELINE works without pinning the
	// ActivePieces surface, which the shim owns.
	src := `
export const ping = {
  name: 'ping',
  async run(ctx: any) {
    return { ok: true, who: ctx?.propsValue?.who ?? 'nobody' };
  },
};
`
	bundled, err := bundleSource("ping", src)
	if err != nil {
		t.Fatalf("bundleSource: %v", err)
	}
	if len(bundled) == 0 {
		t.Fatal("bundleSource returned no bytes")
	}
	if !strings.Contains(string(bundled), "ping") {
		t.Errorf("bundle does not mention the action; got %.200q", bundled)
	}
}

func TestBundleSourceRejectsSyntaxError(t *testing.T) {
	if _, err := bundleSource("broken", `export const x = {`); err == nil {
		t.Fatal("expected a build failure for unbalanced source, got nil")
	}
}

func TestBundleSourceRejectsEmptyName(t *testing.T) {
	if _, err := bundleSource("", `export const x = 1;`); err == nil {
		t.Fatal("expected compile to refuse an empty connector name")
	}
}

// A pasted credential must be REFUSED, not stored and not scrubbed: a key that
// silently disappears looks like it worked, and the caller never learns it put a
// secret somewhere it does not belong.
func TestSecretishCatchesPastedCredentials(t *testing.T) {
	for _, s := range []string{
		`const key = "sk-abcdefghijklmnopqrstuvwx";`,
		`token: 'ghp_abcdefghijklmnopqrstuvwxyz12'`,
		`"xoxb-1234567890-abcdefghij"`,
		`AKIAIOSFODNN7EXAMPLE`,
		"-----BEGIN RSA PRIVATE KEY-----",
	} {
		if !secretish.MatchString(s) {
			t.Errorf("secretish missed a credential shape: %q", s)
		}
	}
	for _, s := range []string{
		`const url = "https://api.example.com/v1/items";`,
		`ctx.auth`,
		`propsValue.token`, // naming a field is not carrying a secret
	} {
		if secretish.MatchString(s) {
			t.Errorf("secretish false-positived on ordinary source: %q", s)
		}
	}
}

func TestPluginNameShape(t *testing.T) {
	for _, ok := range []string{"notion", "brave-search", "my_api", "a", "x9"} {
		if !pluginName.MatchString(ok) {
			t.Errorf("pluginName rejected a legal name: %q", ok)
		}
	}
	for _, bad := range []string{"", "Notion", "../etc/passwd", "a/b", "-lead", "has space", strings.Repeat("x", 65)} {
		if pluginName.MatchString(bad) {
			t.Errorf("pluginName accepted an illegal name: %q", bad)
		}
	}
}

func TestStripFencesRemovesModelPunctuation(t *testing.T) {
	got := stripFences("```typescript\nexport const x = 1;\n```")
	if got != "export const x = 1;" {
		t.Errorf("stripFences = %q", got)
	}
	// Unfenced source must survive byte-for-byte.
	const plain = "export const x = 1;"
	if stripFences(plain) != plain {
		t.Errorf("stripFences altered unfenced source: %q", stripFences(plain))
	}
}
