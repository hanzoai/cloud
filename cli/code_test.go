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

// TestAnthropicWirePinsZen5Tiers locks in the core fix for the Claude Code
// 403 deadlock: every CC model slot must resolve to a zen5 alias served by
// api.hanzo.ai, never a raw claude-* id. A raw claude-opus-4-8 / claude-haiku-*
// 403s on the Hanzo account (no Anthropic provider configured), which kills
// the permission classifier, every subagent, and /compact — the exact failure
// that left session cff690fc unresumable.
func TestAnthropicWirePinsZen5Tiers(t *testing.T) {
	env := anthropicWire("https://api.hanzo.ai", "hk-test", "best")

	want := map[string]string{
		"ANTHROPIC_BASE_URL":             "https://api.hanzo.ai",
		"ANTHROPIC_AUTH_TOKEN":           "hk-test",
		"ANTHROPIC_MODEL":                "best",
		"ANTHROPIC_SMALL_FAST_MODEL":     "zen5-flash",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "zen5-flash",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "zen5",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "zen5-pro",
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  "zen5-pro",
	}
	for k, v := range want {
		if got := env[k]; got != v {
			t.Errorf("%s: want %q, got %q", k, v, got)
		}
	}

	// Every model slot must be a served zen5 alias or the resolved main model —
	// none may be a raw claude-* id (those 403 on the Hanzo account).
	for k, v := range env {
		if !isAllowedModel(v) {
			t.Errorf("%s=%q must not be a raw claude-* model (403s on api.hanzo.ai)", k, v)
		}
	}
}

// TestAnthropicWireExplicitModel checks that an explicitly named model flows
// into the main + OPUS slots while the rest stay on the zen5 ladder.
func TestAnthropicWireExplicitModel(t *testing.T) {
	env := anthropicWire("https://api.hanzo.ai", "hk-test", "zen5-max")
	if env["ANTHROPIC_MODEL"] != "zen5-max" {
		t.Errorf("ANTHROPIC_MODEL: want zen5-max, got %q", env["ANTHROPIC_MODEL"])
	}
	// The tier slots are FIXED zen5 aliases (the stable contract) — they do
	// NOT track the main model. Only ANTHROPIC_MODEL carries the resolved id.
	if env["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "zen5-pro" {
		t.Errorf("OPUS tier is the fixed zen5-pro contract, not the main model: got %q", env["ANTHROPIC_DEFAULT_OPUS_MODEL"])
	}
	// The fast/classifier tier stays pinned to zen5-flash regardless of main.
	if env["ANTHROPIC_SMALL_FAST_MODEL"] != "zen5-flash" {
		t.Errorf("classifier must stay on zen5-flash: got %q", env["ANTHROPIC_SMALL_FAST_MODEL"])
	}
}

// isAllowedModel reports whether m is a model api.hanzo.ai serves. The wire
// may emit the resolved main model (any id the user passed) plus the fixed
// zen5 ladder aliases. The guard is: never a raw claude-* id.
func isAllowedModel(m string) bool {
	// The only forbidden shape is a raw claude-* id (claude-opus-4-8 etc.)
	// — everything else the wire emits is either a zen5 alias or the
	// resolved main model the caller explicitly chose.
	if len(m) >= 6 && m[:6] == "claude" {
		return false
	}
	return true
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
