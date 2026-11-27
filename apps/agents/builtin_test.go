package agents

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/aimodel"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/manifest"
)

// An org that connected Slack and did nothing else has NO agent rows, and the
// bridges ask for the conventional ref — so without a built-in default @hanzo
// answers "the agent hit an error handling that" in every fresh space.
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
	// The default declares the fleet's whole tool surface. The tool loop still
	// decides what is OFFERED per run, but an agent that declares nothing is
	// offered nothing — which is how the assistant came to report it could not
	// reach a cloud that was one socket away.
	if len(a.Tools) != 1 || a.Tools[0] != ToolsAll {
		t.Errorf("the default must declare the whole tool surface (%q), got %v", ToolsAll, a.Tools)
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

// The chat brain is aimodel.ChatModel — one constant in the file that owns model
// policy, not a literal here plus a BRIDGE_AGENT_MODEL knob beside it.
//
// The knob was never set in any deployment, and the literal was justified by the
// claim that enso auto-routes per query, which it does not. This test is the guard
// against a second place regrowing: there is exactly one line that names the tier
// and it is not in this package.
func TestBuiltinModelIsTheChatConstant(t *testing.T) {
	a, ok := builtinAgent("acme", "hanzo", aimodel.ChatModel)
	if !ok {
		t.Fatal("the conventional ref must resolve to the built-in")
	}
	if a.Model != aimodel.ChatModel {
		t.Errorf("the chat brain must be aimodel.ChatModel (%q), got %q", aimodel.ChatModel, a.Model)
	}
	if a.Model == aimodel.FallbackModel {
		t.Errorf("%q is the degraded fallback tier, never the interactive default", aimodel.FallbackModel)
	}
	// The menu must be able to express the default, or a person who opens App Home
	// sees a blank selector and their own model looks lost.
	if !knownChatModel(aimodel.ChatModel) {
		t.Errorf("the App Home menu must offer the default tier %q", aimodel.ChatModel)
	}
}

// The App Home pin still wins over the default — a person's explicit choice is
// the one thing that may override it, and only for the built-in.
func TestAppHomePinBeatsTheDefault(t *testing.T) {
	for _, m := range []string{
		"zen6", "zen6-flash", "zen5-flash", "enso", "enso-flash", "enso-ultra",
	} {
		if !knownChatModel(m) {
			t.Errorf("App Home offers %q, so the turn must accept it", m)
		}
	}
	if knownChatModel("gpt-4o") || knownChatModel("claude-3-opus") {
		t.Error("third-party models may not be pinned from a client")
	}
}

// A tool the instructions name and the surface does not offer is a tool the model
// looks for and cannot call. Every backticked name in the built-in's instructions
// must be one the real surface offers it — a tool, an op in a tool's enum, or an
// argument a tool takes — enumerated from the MCP server over every app in the
// manifest, listed from the catalog this binary embeds.
func TestThePromptNamesOnlyToolsThatExist(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		at := map[string]string{}
		for _, a := range manifest.Apps {
			if manifest.Coresident(a.Name) {
				continue
			}
			at[a.Name] = "" // listed from its catalog, never reached
			declared[a.Name] = client.Published(a.Name)
		}
		t.Cleanup(func() {
			for name := range at {
				delete(declared, name)
			}
		})
		fleetEndpoint(t, at, nil)

		defs := surfaceTools{}.catalog(context.Background(), "acme", "acme/u1", []string{ToolsAll})
		if len(defs) < 2 {
			t.Fatalf("the surface offered %d tools", len(defs))
		}
		// ask_user is offered by a turn's own loop rather than by the surface (ask.go),
		// and a chat turn is the only place these instructions are read.
		offered := map[string]bool{AskTool.Name: true}
		for _, d := range defs {
			offered[d.Name] = true
			for _, op := range opsOf(d.Schema) {
				offered[op] = true
			}
			var s struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			_ = json.Unmarshal(d.Schema, &s)
			for arg := range s.Properties {
				offered[arg] = true
			}
		}
		for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(builtinAgentInstructions, -1) {
			if !offered[m[1]] {
				t.Errorf("the instructions name `%s`, which the surface does not offer", m[1])
			}
		}

	})
}
