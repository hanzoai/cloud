package crm

import (
	"strings"
	"testing"
)

// The intro-call link is the brand's own booking page, opened on the lead, and
// only the brand's org gets one: a customer that runs the CRM for its own sales
// must never send its leads to Hanzo's calendar.
func TestIntroCallIsTheBrandsAlone(t *testing.T) {
	t.Setenv("CLOUD_BRAND", "hanzo")
	data := map[string]any{"contact_name": "Ada Lovelace", "email": "ada@analytical.co"}
	want := "https://cal.hanzo.ai/hanzo/intro?email=ada%40analytical.co&name=Ada+Lovelace&notes=Lead+lead_1"
	if got := IntroCall("hanzo", data, "lead_1"); got != want {
		t.Fatalf("brand org: %q, want %q", got, want)
	}
	if got := IntroCall("acme", data, "lead_1"); got != "" {
		t.Fatalf("another org's lead got %q, want none", got)
	}
	t.Setenv("CLOUD_BRAND", "lux")
	if got := IntroCall("lux", data, "lead_1"); got != "" {
		t.Fatalf("a brand with no booking page got %q, want none", got)
	}
	t.Setenv("CLOUD_BRAND", "acme")
	if got := IntroCall("acme", data, "lead_1"); got != "" {
		t.Fatalf("an unregistered brand got %q, want none", got)
	}
}

// A reference is its prefix and 96 random bits, never the same twice.
func TestARefIsOpaque(t *testing.T) {
	a, err := Ref("lead_")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Ref("lead_")
	if !strings.HasPrefix(a, "lead_") || len(a) != 29 || a == b {
		t.Fatalf("refs %q and %q", a, b)
	}
}
