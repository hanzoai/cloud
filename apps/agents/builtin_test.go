package agents

import (
	"testing"
)

// An org that connected Slack and did nothing else has NO agent rows, and the
// bridges ask for the conventional ref — so without a built-in default @hanzo
// answers "the agent hit an error handling that" in every fresh workspace.
func TestBuiltinResolvesTheConventionalRef(t *testing.T) {
	a, ok := builtinAgent("acme", "hanzo", "zen-70b")
	if !ok {
		t.Fatal("the conventional ref must resolve to the built-in default")
	}
	if a.Org != "acme" {
		t.Errorf("the default must be scoped to the asking org, got %q", a.Org)
	}
	if a.Model != "zen-70b" {
		t.Errorf("the default must use the deployment's model, got %q", a.Model)
	}
	if len(a.Tools) != 0 {
		t.Errorf("the default carries no tools; the tool loop decides that, got %v", a.Tools)
	}
	if a.Instructions == "" {
		t.Error("the default must know what it is")
	}
}

// Case is not a reason to fail: Slack sends whatever the user typed.
func TestBuiltinIsCaseInsensitive(t *testing.T) {
	if _, ok := builtinAgent("acme", "Hanzo", "m"); !ok {
		t.Error("the ref must match case-insensitively")
	}
}

// An UNKNOWN ref stays unknown. Silently substituting the chat agent would make
// a typo in `code: repo` run the wrong thing and look like it worked.
func TestUnknownRefIsStillAMiss(t *testing.T) {
	for _, ref := range []string{"deployer", "hanzo-coder", "", "hanz"} {
		if _, ok := builtinAgent("acme", ref, "m"); ok {
			t.Errorf("%q must not resolve to the default", ref)
		}
	}
}

// No model configured is an honest miss, not a run that fails deeper in.
func TestNoModelIsAMiss(t *testing.T) {
	if _, ok := builtinAgent("acme", "hanzo", "  "); ok {
		t.Error("with no model configured the default must not resolve")
	}
}

// The chat brain is enso — Hanzo's own auto-routing SKU — and explicitly NOT
// cloud.FallbackModel ("best"), whose own doc says the interactive chat path
// never uses it. A Slack turn IS the interactive chat path.
func TestBuiltinModelIsEnso(t *testing.T) {
	t.Setenv("BRIDGE_AGENT_MODEL", "")
	if got := builtinAgentModel(); got != "enso" {
		t.Errorf("the chat brain must default to enso, got %q", got)
	}
	if got := builtinAgentModel(); got == "best" {
		t.Error(`"best" is the degraded fallback tier, never the interactive default`)
	}
}

// A deployment can name its own.
func TestBuiltinModelOverride(t *testing.T) {
	t.Setenv("BRIDGE_AGENT_MODEL", "enso-ultra")
	if got := builtinAgentModel(); got != "enso-ultra" {
		t.Errorf("BRIDGE_AGENT_MODEL must win, got %q", got)
	}
}
