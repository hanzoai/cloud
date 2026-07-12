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

import "testing"

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
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "best",
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
	if env["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "zen5-max" {
		t.Errorf("OPUS tier should track the main model: want zen5-max, got %q", env["ANTHROPIC_DEFAULT_OPUS_MODEL"])
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
