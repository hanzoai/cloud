package cloud

import "testing"

// TestUpstreamModel locks the brand boundary: every upstream family name we have
// actually served is recognised, and no Hanzo name is ever mistaken for one. The
// false-positive half matters as much as the true-positive half — a Hanzo model
// wrongly flagged would be silently rewritten to the default.
func TestUpstreamModel(t *testing.T) {
	// Every upstream-named id the live gateway catalog has served.
	for _, name := range []string{
		"deepseek-3.2", "deepseek-4-flash", "deepseek-chat", "deepseek-r1-distill-70b",
		"deepseek-reasoner", "deepseek-v3.2", "deepseek-v4-flash", "deepseek-v4-pro",
		"glm-5.1", "glm-5.2", "kimi-k2", "kimi-k2.6", "minimax-m2.5",
		"qwen3-32b", "qwen3-coder", "qwen3-coder-flash", "qwen3-tts-voicedesign",
		"qwen3.5-397b", "qwen3.5-397b-a17b",
		// Vendor-prefixed and cased forms must match too.
		"fireworks/deepseek-v3", "DeepSeek-V4-Flash", "together:qwen3-coder",
	} {
		if !UpstreamModel(name) {
			t.Errorf("UpstreamModel(%q) = false, want true — an upstream name would reach a customer", name)
		}
		if got := ZenModel(name); got != DefaultModel {
			t.Errorf("ZenModel(%q) = %q, want %q", name, got, DefaultModel)
		}
	}

	// Hanzo names, and third parties we resell under their own name, are ours to
	// show and must pass through untouched.
	for _, name := range []string{
		"enso", "enso-flash", "enso-pro", "enso-ultra",
		"zen5", "zen5-flash", "zen5-pro", "zen5-coder", "zen5-mini",
		"zen-image", "zen-vl", "zen-embedding", "zen-rerank", "zen-guard",
		"gpt-4o-mini", "claude-sonnet-4-5", "gemini-2.5-pro", "llama-3.3-70b", "mistral-small",
	} {
		if UpstreamModel(name) {
			t.Errorf("UpstreamModel(%q) = true, want false — a Hanzo/resold name must not be rewritten", name)
		}
		if got := ZenModel(name); got != name {
			t.Errorf("ZenModel(%q) = %q, want it unchanged", name, got)
		}
	}
}

// TestZenModel covers the edges around the pass-through: blanks, whitespace, and
// idempotence (running the migration twice must not move a row twice).
func TestZenModel(t *testing.T) {
	if got := ZenModel(""); got != "" {
		t.Errorf("ZenModel(\"\") = %q, want \"\" — an unset model stays unset", got)
	}
	if got := ZenModel("  enso-pro  "); got != "enso-pro" {
		t.Errorf("ZenModel trims, got %q", got)
	}
	if got := ZenModel(ZenModel("deepseek-v4-flash")); got != DefaultModel {
		t.Errorf("ZenModel is not idempotent: %q", got)
	}
	if DefaultModel != "enso" {
		t.Errorf("DefaultModel = %q, want the bare alias %q so the gateway resolves the tier per call",
			DefaultModel, "enso")
	}
	if UpstreamModel(DefaultModel) {
		t.Fatal("DefaultModel is itself an upstream name — the whole policy is inverted")
	}
}
