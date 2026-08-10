package account

import "testing"

// The accent is stored into a properties value a surface later renders into a
// <style> body, and the body is chosen by its owner — so the boundary that keeps
// an accent from breaking out of that <style> is a security property, not a
// tidiness one. These pin it, and the type clamp, and the density allow-list.

func TestCleanAppearance_rejectsAccentInjection(t *testing.T) {
	for _, accent := range []string{
		"#fff;}html{display:none}",
		"red; } body { visibility: hidden",
		"url(https://evil.example/x)",
		`#fff"><script>`,
		"blue",
		"expression(alert(1))",
	} {
		if got := cleanAppearance(appearance{Accent: accent}); got.Accent != "" {
			t.Errorf("accent %q survived cleaning as %q; want dropped", accent, got.Accent)
		}
	}
}

func TestCleanAppearance_acceptsRealColours(t *testing.T) {
	for _, accent := range []string{"#8b5cf6", "#abc", "rgb(139 92 246 / 0.8)", "oklch(62% 0.2 280)"} {
		if got := cleanAppearance(appearance{Accent: accent}); got.Accent != accent {
			t.Errorf("accent %q was dropped; want kept", accent)
		}
	}
}

func TestCleanAppearance_clampsTypeAndFiltersAxes(t *testing.T) {
	if got := cleanAppearance(appearance{Type: 99}); got.Type != 1.4 {
		t.Errorf("type 99 clamped to %v; want 1.4", got.Type)
	}
	if got := cleanAppearance(appearance{Type: 0.1}); got.Type != 0.85 {
		t.Errorf("type 0.1 clamped to %v; want 0.85", got.Type)
	}
	if got := cleanAppearance(appearance{Type: 1.15}); got.Type != 1.15 {
		t.Errorf("valid type dropped: %v", got.Type)
	}
	if got := cleanAppearance(appearance{Density: "roomy"}); got.Density != "" {
		t.Errorf("invented density survived as %q; want dropped", got.Density)
	}
	if got := cleanAppearance(appearance{Density: "comfortable"}); got.Density != "comfortable" {
		t.Errorf("valid density dropped: %q", got.Density)
	}
	// An empty preference stays empty — an unset axis is absent, never neutral.
	if got := cleanAppearance(appearance{}); got != (appearance{}) {
		t.Errorf("empty preference became %+v; want empty", got)
	}
}
