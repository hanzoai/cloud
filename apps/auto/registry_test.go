package auto

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/apps/provider"
	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// registry_test.go guards the ONE registry this subsystem's actions now live in.
// The second catalogue that used to sit here was merged into apps/provider, so an
// action arrives through provider.RegisterActions and leaves through
// provider.ActionFor — and the name the tool plane publishes for it is built from
// the provider's ID. These tests hold that round trip: what goes in comes back out,
// under the id and never under the display name.

// TestEveryRegisteredActionIsReachable: every action any provider registered
// resolves through ActionFor to the SAME value, and answers to the tool name the
// plane publishes it under. The walk is over c.Actions rather than SortedActions
// because the MAP KEY is what RegisterActions builds the tool name from: a key that
// disagrees with the Action's own Name would publish a tool that resolves to an
// action calling itself something else.
func TestEveryRegisteredActionIsReachable(t *testing.T) {
	providers := provider.WithActions()
	seen := 0
	for _, c := range providers {
		for name, a := range c.Actions {
			seen++
			if name != a.Name {
				t.Errorf("%s: action keyed %q calls itself %q — the tool name comes from the key", c.ID, name, a.Name)
			}
			gotP, gotA, err := provider.ActionFor(c.ID, name)
			if err != nil {
				t.Errorf("ActionFor(%q, %q): %v", c.ID, name, err)
				continue
			}
			if gotP != c || gotA != a {
				t.Errorf("ActionFor(%q, %q) resolved a different value than the registry holds", c.ID, name)
			}
			if a.Run == nil {
				t.Errorf("%s.%s is published with no body", c.ID, name)
			}
			if !ToolExists(c.ID + "_" + name) {
				t.Errorf("%s.%s does not answer to tool %q", c.ID, name, c.ID+"_"+name)
			}
		}
	}
	// A COUNT FLOOR CANNOT SEE A PROVIDER LEAVING. This asserted only
	// len(providers) >= 5 && seen >= 10 against a live 8 and 14, so neutering the
	// whole `x` provider — its init gone, verify_follow unpublished — left 7 and
	// 13 and passed. Every provider whose actions something depends on is named,
	// so losing one is a failure here rather than a quieter tool list.
	must := map[string][]string{
		"slack":        {"send_message"},
		"x":            {"verify_follow"},
		"google":       {"append_row", "list_files"},
		"core":         {"http_request", "code"},
		"content":      {"generate", "publish"},
		"destinations": {"connect"},
		"waitlist":     {"award_points"},
	}
	have := map[string]map[string]bool{}
	for _, c := range providers {
		have[c.ID] = map[string]bool{}
		for name := range c.Actions {
			have[c.ID][name] = true
		}
	}
	for id, acts := range must {
		if have[id] == nil {
			t.Errorf("provider %q left the registry, so its tools are unpublished", id)
			continue
		}
		for _, a := range acts {
			if !have[id][a] {
				t.Errorf("%s.%s left the registry", id, a)
			}
		}
	}
	if len(providers) < len(must) || seen < 10 {
		t.Fatalf("the action registry did not fill: %d providers, %d actions", len(providers), seen)
	}
}

// TestToolNamesCarryTheProviderIDNotItsDisplayName: the published set is EXACTLY
// {provider id}_{action name}, nothing more and nothing missing. The display name
// is a human label — it was briefly the address ("Slack_send_message"), which put a
// tool under a name no resolver would ever look up, so the id-only rule is asserted
// over the whole registry and the regression is named outright.
func TestToolNamesCarryTheProviderIDNotItsDisplayName(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newApp(t)
		list, err := connectorToolProvider{}.List(context.Background(), tools.Scope{Org: "acme"})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		published := map[string]bool{}
		for _, tl := range list {
			published[tl.Name] = true
		}
		want := map[string]bool{}
		for _, c := range provider.WithActions() {
			for name := range c.Actions {
				// An action whose address an MCP client cannot send ("Create Order",
				// or past 64 characters) runs in a flow and is not a tool.
				if !toolName.MatchString(c.ID + "_" + name) {
					if published[c.ID+"_"+name] {
						t.Errorf("tool %q is published outside the MCP name grammar", c.ID+"_"+name)
					}
					continue
				}
				want[c.ID+"_"+name] = true
				if c.Name != c.ID && published[c.Name+"_"+name] {
					t.Errorf("%q is published under its display name as %q", c.ID+"_"+name, c.Name+"_"+name)
				}
			}
		}
		for name := range want {
			if !published[name] {
				t.Errorf("tool %q is registered but not published", name)
			}
		}
		for name := range published {
			if !want[name] {
				t.Errorf("tool %q is published but no (provider, action) pair addresses it", name)
			}
		}
		if !published["slack_send_message"] || published["Slack_send_message"] {
			t.Errorf("slack must be published as slack_send_message, never Slack_send_message")
		}
		// The display-name form is not dispatchable either — but resolveTool is asked
		// DIRECTLY, because Dispatch has a second guard that refuses an unknown
		// provider id and answers ErrUnknownTool for its own reason. Asserting through
		// Dispatch passed even with resolveTool keyed on the display name, which is
		// the exact regression this test is named for.
		if _, _, ok := resolveTool("Slack_send_message"); ok {
			t.Fatalf("resolveTool must not resolve the display-name form")
		}
		if _, _, ok := resolveTool("slack_send_message"); !ok {
			t.Fatalf("resolveTool must resolve the id form")
		}
	})
}

// TestAProviderThatOffersNothingIsNotATool: WithActions is the tool list, so a
// provider registered for its consent flow alone must not appear in it. GitHub is
// that provider — it carries a credential half and no action — and ActionFor's error
// proves the distinction: it names the unknown ACTION, which only a registered
// provider can produce.
func TestAProviderThatOffersNothingIsNotATool(t *testing.T) {
	for _, c := range provider.WithActions() {
		if len(c.Actions) == 0 {
			t.Errorf("%q offers nothing to invoke yet is published as a tool", c.ID)
		}
	}
	for _, c := range provider.WithActions() {
		if c.ID == "github" {
			t.Fatalf("github registers no action and must not be in the tool list")
		}
	}
	_, _, err := provider.ActionFor("github", "send_message")
	if err == nil {
		t.Fatalf("github has no send_message action")
	}
	// The distinction is behavioural, not verbal: github IS registered, so an
	// unknown id must miss differently from an unknown action on a known id. This
	// used to match on the error's wording, which reds on a reworded message that
	// is still correct.
	if _, _, unknown := provider.ActionFor("no-such-provider-at-all", "send_message"); unknown == nil {
		t.Fatalf("an unregistered provider must not resolve")
	}
	if !provider.Registered("github") {
		t.Fatalf("github must be registered — that is what makes its miss an action miss")
	}
	if provider.Registered("no-such-provider-at-all") {
		t.Fatalf("an id nobody registered must not read as registered")
	}
	if ToolExists("github_send_message") {
		t.Fatalf("a provider with no actions publishes no tool")
	}
}

// TestRegisterActionsOnAnUnknownIDMakesAnActionOnlyProvider: an id with no
// registered provider gets one built from the call — actions and nothing else. That
// is how `core` and `waitlist` exist: they spend no customer credential, so they
// have no consent flow to declare, and the registry must not require one before it
// will hold their actions.
func TestRegisterActionsOnAnUnknownIDMakesAnActionOnlyProvider(t *testing.T) {
	const id, act = "fixture", "noop"
	// REGISTERING IS PERMANENT — the registry is process-global and has no
	// deregister — so this asserts the END STATE rather than the transition.
	// Demanding the id be unknown first made the test pass once and fail under
	// `go test -count=2` on its own precondition, and a second RegisterActions
	// would hit the duplicate-action panic.
	if _, _, err := provider.ActionFor(id, act); err != nil {
		provider.RegisterActions(id, "Fixture Provider", map[string]*provider.Action{
			act: {
				Name: act, DisplayName: "No Op", Description: "test: an action-only provider",
				Run: func(_ context.Context, rc provider.RunContext) (any, error) {
					return map[string]any{"org": rc.Org}, nil
				},
			},
		}, nil)
	}

	p, a, err := provider.ActionFor(id, act)
	if err != nil {
		t.Fatalf("ActionFor after RegisterActions: %v", err)
	}
	if p.ID != id || p.Name != "Fixture Provider" || a.Name != act {
		t.Fatalf("registered provider is %+v / action %q", p, a.Name)
	}
	// Action-only means exactly that: no credential half to consent to or custody.
	if p.Kind != "" || p.Scope != "" || p.RedirectPath != "" || len(p.Secrets) != 0 || len(p.Scopes) != 0 {
		t.Errorf("an action-only provider declares no credential flow, got %+v", p)
	}
	if len(p.Triggers) != 0 {
		t.Errorf("nil triggers must register none, got %d", len(p.Triggers))
	}

	found := false
	for _, c := range provider.WithActions() {
		if c.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("%q carries an action and must be in the tool list", id)
	}
	out, err := connectorToolProvider{}.Dispatch(context.Background(), tools.Principal{Org: "acme"}, id+"_"+act, nil)
	if err != nil {
		t.Fatalf("dispatch %s_%s: %v", id, act, err)
	}
	if m, ok := out.(map[string]any); !ok || m["org"] != "acme" {
		t.Fatalf("the action must run bound to the caller's org, got %v", out)
	}
}
