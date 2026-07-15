// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCodeAgentsBypassPermissionsByDefault(t *testing.T) {
	tests := []struct {
		name string
		flag string
	}{
		{name: "claude", flag: "--dangerously-skip-permissions"},
		{name: "codex", flag: "--dangerously-bypass-approvals-and-sandbox"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := codeAgents[tt.name]
			argv := codeArgv(agent, "https://api.hanzo.ai", defaultCodeModel, false, nil)
			if !slices.Contains(argv, tt.flag) {
				t.Fatalf("default argv %q does not contain permission bypass %q", argv, tt.flag)
			}

			safeArgv := codeArgv(agent, "https://api.hanzo.ai", defaultCodeModel, true, nil)
			if slices.Contains(safeArgv, tt.flag) {
				t.Fatalf("--safe argv %q still contains permission bypass %q", safeArgv, tt.flag)
			}
		})
	}
}

func TestDefaultCodeModelIsToolCapableAlias(t *testing.T) {
	// zen5 is the flagship GLM-5.2-class alias — tool-capable (verified live: the
	// glm-5.2 upstream returns tool_use / stop_reason:tool_use) and the model the
	// user's "pop open with GLM-5.2" intent maps to. It must NOT be the virtual
	// `best` (a reserved word Claude Code rewrites to an unserved claude-* id).
	if defaultCodeModel != "zen5" {
		t.Fatalf("coding-agent default %q is not the flagship zen5 alias", defaultCodeModel)
	}
	if defaultCodeModel == "best" {
		t.Fatal("default must be a concrete served id, never the reserved word best")
	}
}

// TestAnthropicWirePinsCarrierTiers locks in the 1M-context fix: every CC tier
// slot is pinned to a Claude-Code-recognized CARRIER id (so CC grants the model
// its true, up-to-1M context budget instead of the 128K fallback it applies to
// ids it does not know), and settings.json modelOverrides rewrites each carrier
// back to a served zen alias before the request leaves the client — so the wire
// model that reaches api.hanzo.ai is always zen, never the carrier. The old
// deadlock (a raw claude-* id 403ing on the Hanzo account) cannot recur because
// the carrier never reaches the server; the override guarantees it (asserted in
// TestCarrierTiersAreOverridden).
func TestAnthropicWirePinsCarrierTiers(t *testing.T) {
	env := anthropicWire("https://api.hanzo.ai", "hk-test", "claude-opus-4-8[1m]")

	want := map[string]string{
		"ANTHROPIC_BASE_URL":             "https://api.hanzo.ai",
		"ANTHROPIC_AUTH_TOKEN":           "hk-test",
		"ANTHROPIC_MODEL":                "claude-opus-4-8[1m]",
		"ANTHROPIC_SMALL_FAST_MODEL":     "zen5-flash", // deprecated var: no override applies, so a direct served zen id
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "zen5-flash", // fast tier is carrier-less (never needs >128K)
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-4-6[1m]",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "claude-opus-4-8[1m]",
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  "claude-fable-5[1m]",
	}
	for k, v := range want {
		if got := env[k]; got != v {
			t.Errorf("%s: want %q, got %q", k, v, got)
		}
	}
	// Each tier slot carries a Hanzo-branded display name so the picker never
	// shows the underlying carrier (Opus/Sonnet/Haiku) — only the Zen brand.
	if env["ANTHROPIC_DEFAULT_OPUS_MODEL_NAME"] != "Zen5 Pro" {
		t.Errorf("OPUS tier must display the Zen brand, got %q", env["ANTHROPIC_DEFAULT_OPUS_MODEL_NAME"])
	}
}

// TestCarrierTiersAreOverridden is the safety net that replaces the old
// "no claude-* in the wire" rule: a claude-* carrier in the env is SAFE only
// because modelOverrides maps it to a served zen id. Every claude-* value the
// wire emits must have a modelOverrides entry (keyed by its suffix-stripped id)
// pointing at a zen alias — otherwise CC would send the raw claude-* id to
// api.hanzo.ai and reintroduce the 403.
func TestCarrierTiersAreOverridden(t *testing.T) {
	env := anthropicWire("https://api.hanzo.ai", "hk-test", "claude-opus-4-8[1m]")
	overrides := claudeModelOverrides()
	for k, v := range env {
		if len(v) < 6 || v[:6] != "claude" {
			continue // only carrier ids need an override
		}
		zen, ok := overrides[stripModelSuffix(v)]
		if !ok {
			t.Errorf("%s=%q is a claude-* carrier with NO modelOverrides entry — it would reach api.hanzo.ai and 403", k, v)
			continue
		}
		if len(zen) < 3 || zen[:3] != "zen" {
			t.Errorf("%s=%q overrides to %q, which is not a zen alias", k, v, zen)
		}
	}
}

// TestZenCarrierRoundTrips checks the two-way mapping the fix depends on: every
// tier's zen id maps to a carrier, and that carrier (suffix-stripped) maps back
// to a served zen id via modelOverrides. An unknown id passes through unchanged.
func TestZenCarrierRoundTrips(t *testing.T) {
	overrides := claudeModelOverrides()
	for _, tier := range zenTiers {
		carrier := zenCarrier(tier.zen)
		if tier.carrier == "" {
			// Direct tier: zenCarrier returns the served zen id itself, no override.
			if carrier != tier.zen {
				t.Errorf("direct tier %q must map to itself, got %q", tier.zen, carrier)
			}
			continue
		}
		if carrier == tier.zen {
			t.Errorf("zenCarrier(%q) did not map to a carrier", tier.zen)
		}
		if got := overrides[stripModelSuffix(carrier)]; got != tier.zen {
			t.Errorf("carrier %q for %q overrides to %q, want %q", carrier, tier.zen, got, tier.zen)
		}
	}
	if got := zenCarrier("some-raw-upstream"); got != "some-raw-upstream" {
		t.Errorf("unknown id must pass through unchanged, got %q", got)
	}
}

// TestClaudeArgvForcesModel locks in the fix for "everything shows up as best":
// the claude agent must pass --model <resolved> on argv so a persisted /model
// selection (the reserved word "best") cannot override the zen5 model the
// launcher chose. ANTHROPIC_MODEL alone is not enough — /model beats it.
func TestClaudeArgvForcesModel(t *testing.T) {
	agent := codeAgents["claude"]
	argv := codeArgv(agent, "https://api.hanzo.ai", "zen5-pro", false, nil)
	i := slices.Index(argv, "--model")
	if i < 0 {
		t.Fatalf("claude argv %q does not force --model (a persisted /model selection would win)", argv)
	}
	if i+1 >= len(argv) || argv[i+1] != "zen5-pro" {
		t.Fatalf("claude argv %q: --model must be followed by the resolved id zen5-pro", argv)
	}
	// --model is forced in --safe mode too (model pinning is independent of
	// the permission mode).
	safeArgv := codeArgv(agent, "https://api.hanzo.ai", "zen5-pro", true, nil)
	if i := slices.Index(safeArgv, "--model"); i < 0 || safeArgv[i+1] != "zen5-pro" {
		t.Fatalf("--safe argv %q must still force --model zen5-pro", safeArgv)
	}
}

// TestClaudeAppendsZenIdentityInAllModes locks in the identity fix: the claude
// agent appends --append-system-prompt <zenIdentityPrompt> so a Hanzo-served
// model self-identifies as a Hanzo Zen model. Identity is not a permission
// bypass, so it is present in --safe too (unlike --dangerously-skip-permissions).
// codex/dev (OpenAI wire) do not carry the Anthropic-only append.
func TestClaudeAppendsZenIdentityInAllModes(t *testing.T) {
	agent := codeAgents["claude"]

	check := func(argv []string) {
		t.Helper()
		i := slices.Index(argv, "--append-system-prompt")
		if i < 0 || i+1 >= len(argv) || argv[i+1] != zenIdentityPrompt {
			t.Fatalf("argv %q missing --append-system-prompt <zenIdentityPrompt>", argv)
		}
	}

	// full-auto (default)
	check(codeArgv(agent, "https://api.hanzo.ai", defaultCodeModel, false, nil))

	// --safe keeps the identity (identity != permission bypass) but drops the bypass
	safeArgv := codeArgv(agent, "https://api.hanzo.ai", defaultCodeModel, true, nil)
	check(safeArgv)
	if slices.Contains(safeArgv, "--dangerously-skip-permissions") {
		t.Fatalf("--safe must not carry the permission bypass: %v", safeArgv)
	}

	// codex/dev (OpenAI wire) do not carry the Anthropic-only identity append
	for _, name := range []string{"codex", "dev"} {
		argv := codeArgv(codeAgents[name], "https://api.hanzo.ai", defaultCodeModel, false, nil)
		if slices.Contains(argv, "--append-system-prompt") {
			t.Fatalf("%s must not carry the claude-only identity append: %v", name, argv)
		}
	}
}

// TestClaudeAutoWiresMCP locks in the fix for "hanzo code wires no tools": the
// claude agent must opt into MCP auto-wiring, and the resolver must produce an
// stdio server config that is layered STRICTLY (repo .mcp.json ignored — it could
// exfiltrate the session bearer).
func TestClaudeAutoWiresMCP(t *testing.T) {
	if !codeAgents["claude"].mcp {
		t.Fatal("claude agent must set mcp:true so `hanzo code claude` starts with the Hanzo tool lattice")
	}
	// codex/dev are wired additively by their own provider config, not this seam.
	for _, name := range []string{"codex", "dev"} {
		if codeAgents[name].mcp {
			t.Fatalf("%s must not use the claude MCP seam (it attaches Hanzo additively via -c)", name)
		}
	}

	// The --mcp-config document is a valid single-server stdio config.
	cfg := mcpConfigJSON("/usr/bin/hanzo-mcp", []string{"--project-dir", "/repo"})
	var doc struct {
		MCPServers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(cfg), &doc); err != nil {
		t.Fatalf("mcpConfigJSON is not valid JSON: %v", err)
	}
	h, ok := doc.MCPServers["hanzo"]
	if !ok || h.Type != "stdio" || h.Command != "/usr/bin/hanzo-mcp" {
		t.Fatalf("mcpConfigJSON: want one stdio server 'hanzo' → /usr/bin/hanzo-mcp, got %+v", doc.MCPServers)
	}
	if !slices.Contains(h.Args, "--project-dir") || !slices.Contains(h.Args, "/repo") {
		t.Fatalf("mcpConfigJSON: server must be scoped to the project dir, got args %v", h.Args)
	}

	// mcpArgs writes the config into the isolated dir and returns the strict flags.
	dir := t.TempDir()
	t.Setenv("PATH", "/usr/bin/hanzo-mcp-not-here") // force the not-found path deterministically
	flags, warn := mcpArgs(dir, "/repo")
	if warn == "" || len(flags) != 0 {
		t.Fatalf("with no hanzo-mcp on PATH, mcpArgs must warn and inject nothing, got flags=%v warn=%q", flags, warn)
	}
}

// TestClaudeAgentAppliesCarrier locks in the runCode wiring: the claude agent
// carries the zen→carrier map (so the resolved zen model is handed to CC as a
// recognized 1M id), while codex/dev have no carrier (they speak OpenAI directly
// and must NOT rewrite the model).
func TestClaudeAgentAppliesCarrier(t *testing.T) {
	if codeAgents["claude"].carrier == nil {
		t.Fatal("claude agent must set a carrier so CC budgets the full context window")
	}
	if got := codeAgents["claude"].carrier("zen5-pro"); got != "claude-opus-4-8[1m]" {
		t.Fatalf("claude carrier: zen5-pro must map to the opus carrier, got %q", got)
	}
	for _, name := range []string{"codex", "dev"} {
		if codeAgents[name].carrier != nil {
			t.Fatalf("%s speaks OpenAI directly and must not remap the model", name)
		}
	}
}

// servedZenIDs is the set of zen aliases api.hanzo.ai actually serves, confirmed
// live (2026-07). zen5-mini/zen5-max/zen5-ultra are catalog-listed but 404 or
// time out, so no tier may target them. Adding a tier forces confirming its id
// serves and listing it here — the guard below fails otherwise.
var servedZenIDs = map[string]bool{
	"zen5-flash": true,
	"zen5":       true,
	"zen5-pro":   true,
	"zen5-coder": true,
}

// TestZenTiersServeReal is the invariant a 1M-carrier is useless without: every
// tier's zen wire id must be one api.hanzo.ai serves. A carrier that budgets 1M
// but rewrites to a 404 id just fails later, opaquely.
func TestZenTiersServeReal(t *testing.T) {
	for _, tier := range zenTiers {
		if !servedZenIDs[tier.zen] {
			t.Errorf("tier %q → zen id %q is not in the confirmed-served set; a carrier to an unserved id 404s", tier.carrier, tier.zen)
		}
	}
}

// TestUpsertClaudeSettings checks the seed: a fresh dir gets base defaults plus
// the carrier→zen modelOverrides, and a re-seed REFRESHES modelOverrides (policy)
// while PRESERVING a user's own edits to other keys (preference).
func TestUpsertClaudeSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	if err := upsertClaudeSettings(path); err != nil {
		t.Fatalf("first seed failed: %v", err)
	}
	read := func() map[string]any {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read settings: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("settings.json is not valid JSON: %v", err)
		}
		return m
	}
	s := read()
	if s["effortLevel"] != "max" {
		t.Errorf("base defaults missing: effortLevel = %v", s["effortLevel"])
	}
	ov, ok := s["modelOverrides"].(map[string]any)
	if !ok {
		t.Fatalf("modelOverrides missing or wrong type: %T", s["modelOverrides"])
	}
	if ov["claude-opus-4-8"] != "zen5-pro" {
		t.Errorf("modelOverrides[claude-opus-4-8] = %v, want zen5-pro", ov["claude-opus-4-8"])
	}

	// User edits a preference and adds a key; re-seed must keep both.
	s["effortLevel"] = "low"
	s["userKey"] = "keepme"
	b, _ := json.Marshal(s)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := upsertClaudeSettings(path); err != nil {
		t.Fatalf("re-seed failed: %v", err)
	}
	s2 := read()
	if s2["effortLevel"] != "low" {
		t.Errorf("re-seed clobbered user edit: effortLevel = %v, want low", s2["effortLevel"])
	}
	if s2["userKey"] != "keepme" {
		t.Errorf("re-seed dropped user key: userKey = %v", s2["userKey"])
	}
	ov2, _ := s2["modelOverrides"].(map[string]any)
	if ov2["claude-opus-4-8"] != "zen5-pro" {
		t.Errorf("re-seed lost modelOverrides: %v", ov2)
	}
}
