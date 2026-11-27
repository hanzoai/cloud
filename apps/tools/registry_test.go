package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
)

// fakeProvider is a test source: a fixed tool list and a dispatch that echoes which
// source ran, so precedence + routing are observable.
type fakeProvider struct {
	src   Source
	tools []Tool
}

func (f *fakeProvider) Source() Source { return f.src }
func (f *fakeProvider) List(_ context.Context, _ Scope) ([]Tool, error) {
	return f.tools, nil
}
func (f *fakeProvider) Dispatch(_ context.Context, _ Principal, name string, _ map[string]any) (any, error) {
	return map[string]any{"by": string(f.src), "name": name}, nil
}

func freshRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	act, err := OpenActivationStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenActivationStore: %v", err)
	}
	t.Cleanup(func() { _ = act.Close() })
	r.SetActivation(act)
	return r
}

func tool(name string, src Source) Tool {
	return Tool{Name: name, Source: src, Dispatchable: true, Schema: json.RawMessage(`{"type":"object"}`)}
}

// TestPrecedence: when two sources offer the same tool name, the lowest-rank source
// wins in BOTH List (dedup) and Dispatch (routing). A connector outranks an org's
// external MCP server — an external tool can never shadow a first-party one.
func TestPrecedence(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := freshRegistry(t)
		r.Register(&fakeProvider{src: SourceMCP, tools: []Tool{tool("dup", SourceMCP), tool("only_mcp", SourceMCP)}})
		r.Register(&fakeProvider{src: SourceConnector, tools: []Tool{tool("dup", SourceConnector)}})

		got := r.List(context.Background(), Scope{Org: "acme"})
		bySource := map[string]Source{}
		for _, tl := range got {
			bySource[tl.Name] = tl.Source
		}
		if bySource["dup"] != SourceConnector {
			t.Fatalf("precedence: dup should resolve to connector, got %q", bySource["dup"])
		}
		if bySource["only_mcp"] != SourceMCP {
			t.Fatalf("only_mcp should be present from mcp, got %q", bySource["only_mcp"])
		}
		if len(got) != 2 {
			t.Fatalf("List should dedup to 2 tools, got %d: %+v", len(got), got)
		}

		// Dispatch of the collided name routes to the winning (connector) source.
		act := r.activation
		if err := act.Activate(context.Background(), "acme", "", "dup", SourceConnector, "u", false); err != nil {
			t.Fatalf("activate: %v", err)
		}
		out, err := r.Dispatch(context.Background(), Principal{Org: "acme"}, "dup", nil)
		if err != nil {
			t.Fatalf("dispatch dup: %v", err)
		}
		if m, _ := out.(map[string]any); m["by"] != string(SourceConnector) {
			t.Fatalf("dispatch dup should run on connector, ran on %v", m["by"])
		}
	})
}

// TestActivationGate: a tool that is NOT activated for the scope is refused with
// ErrNotActivated; activating it for (org,project) lets the SAME call through. This
// is the "403 on unactivated tools" contract at the registry client.
func TestActivationGate(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := freshRegistry(t)
		r.Register(&fakeProvider{src: SourceConnector, tools: []Tool{tool("slack_send", SourceConnector)}})

		_, err := r.Dispatch(context.Background(), Principal{Org: "acme"}, "slack_send", nil)
		if !errors.Is(err, ErrNotActivated) {
			t.Fatalf("unactivated tool must be ErrNotActivated, got %v", err)
		}

		// Activation is scoped: activating in project "p1" does NOT admit a default-scope call.
		if err := r.activation.Activate(context.Background(), "acme", "p1", "slack_send", SourceConnector, "u", false); err != nil {
			t.Fatalf("activate p1: %v", err)
		}
		_, err = r.Dispatch(context.Background(), Principal{Org: "acme"}, "slack_send", nil)
		if !errors.Is(err, ErrNotActivated) {
			t.Fatalf("project-scoped activation must not admit default scope, got %v", err)
		}

		// Activate in the default scope → the call goes through.
		if err := r.activation.Activate(context.Background(), "acme", "", "slack_send", SourceConnector, "u", false); err != nil {
			t.Fatalf("activate default: %v", err)
		}
		if _, err := r.Dispatch(context.Background(), Principal{Org: "acme"}, "slack_send", nil); err != nil {
			t.Fatalf("activated tool must dispatch, got %v", err)
		}

		// Cross-org isolation: org "evil" never sees acme's activation.
		_, err = r.Dispatch(context.Background(), Principal{Org: "evil"}, "slack_send", nil)
		if !errors.Is(err, ErrNotActivated) {
			t.Fatalf("cross-org must be ErrNotActivated, got %v", err)
		}
	})
}

// fakeCharger records the resources it was asked to settle and returns a programmed error.
type fakeCharger struct {
	err     error
	charged []string
}

func (f *fakeCharger) Charge(_ context.Context, resource string) error {
	f.charged = append(f.charged, resource)
	return f.err
}

// TestPricedFailsClosed: a tool that DECLARES a price with no charger wired fails
// closed (ErrChargerUnset) — a paid tool is never served free. With a charger every
// dispatch is offered to the client by name, and a payment failure surfaces as
// ErrPaymentRequired without dispatching.
func TestPricedFailsClosed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := freshRegistry(t)
		priced := tool("premium_search", SourceMCP)
		priced.Price = &Price{Amount: money.FromCents(250), Currency: "USD", Recipient: "wal_seller"}
		r.Register(&fakeProvider{src: SourceMCP, tools: []Tool{priced}})
		if err := r.activation.Activate(context.Background(), "acme", "", "premium_search", SourceMCP, "u", false); err != nil {
			t.Fatalf("activate: %v", err)
		}

		// No charger → fail closed.
		_, err := r.Dispatch(context.Background(), Principal{Org: "acme", Owner: "acme"}, "premium_search", nil)
		if !errors.Is(err, ErrChargerUnset) {
			t.Fatalf("priced tool with no charger must fail closed, got %v", err)
		}

		// Payment declined → ErrPaymentRequired, no dispatch.
		decline := &fakeCharger{err: ErrPaymentRequired}
		r.SetCharger(decline)
		_, err = r.Dispatch(context.Background(), Principal{Org: "acme", Owner: "acme"}, "premium_search", nil)
		if !errors.Is(err, ErrPaymentRequired) {
			t.Fatalf("declined payment must be ErrPaymentRequired, got %v", err)
		}

		// Payment settled → dispatch runs, and the client was asked about THIS tool.
		ok := &fakeCharger{}
		r.SetCharger(ok)
		if _, err := r.Dispatch(context.Background(), Principal{Org: "acme", Owner: "payer-org"}, "premium_search", nil); err != nil {
			t.Fatalf("settled payment must dispatch, got %v", err)
		}
		if want := client.ToolResource("mcp", "premium_search"); len(ok.charged) != 1 || ok.charged[0] != want {
			t.Fatalf("charger must be asked once, for %s; got %v", want, ok.charged)
		}
	})
}

// TestUnpricedToolStillSettles: a tool that declares NO price is still offered to
// the charger — "is this priced" is the payment layer's question, answered from its
// own table, and asking it twice is how a gate and a settlement come to disagree.
// A charger that settles it for nothing lets the call run.
func TestUnpricedToolStillSettles(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := freshRegistry(t)
		r.Register(&fakeProvider{src: SourceMCP, tools: []Tool{tool("free_search", SourceMCP)}})
		if err := r.activation.Activate(context.Background(), "acme", "", "free_search", SourceMCP, "u", false); err != nil {
			t.Fatalf("activate: %v", err)
		}

		// No charger, no declared price → runs free.
		if _, err := r.Dispatch(context.Background(), Principal{Org: "acme"}, "free_search", nil); err != nil {
			t.Fatalf("unpriced tool with no charger must run, got %v", err)
		}

		// With a charger, it is still consulted — and settling for nothing serves.
		ok := &fakeCharger{}
		r.SetCharger(ok)
		if _, err := r.Dispatch(context.Background(), Principal{Org: "acme"}, "free_search", nil); err != nil {
			t.Fatalf("unpriced tool must run once settled, got %v", err)
		}
		if len(ok.charged) != 1 || ok.charged[0] != client.ToolResource("mcp", "free_search") {
			t.Fatalf("every dispatch is offered to the client; got %v", ok.charged)
		}
	})
}

// failingProvider is a source that cannot answer a listing.
type failingProvider struct{ src Source }

func (f failingProvider) Source() Source { return f.src }
func (failingProvider) List(context.Context, Scope) ([]Tool, error) {
	return nil, errors.New("peer unreachable")
}
func (failingProvider) Dispatch(context.Context, Principal, string, map[string]any) (any, error) {
	return nil, errors.New("peer unreachable")
}

// TestListingNamesTheSourcesThatDidNotAnswer: a tool activated from a source that
// cannot answer is absent from the listing, and the listing must say that rather
// than read as "no such tool" beside an activation set that still names it.
func TestListingNamesTheSourcesThatDidNotAnswer(t *testing.T) {
	r := freshRegistry(t)
	r.Register(&fakeProvider{src: SourceConnector, tools: []Tool{tool("fetch", SourceConnector)}})
	r.Register(failingProvider{src: SourceSkill})
	ctx := context.Background()
	if err := r.Activate(ctx, "acme", "", "skill_dev", "u1", true); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	tools, incomplete := r.Listing(ctx, Scope{Org: "acme"})
	if len(tools) != 1 || tools[0].Name != "fetch" {
		t.Fatalf("tools = %+v, want only fetch", tools)
	}
	if len(incomplete) != 1 || incomplete[0] != SourceSkill {
		t.Fatalf("incomplete = %v, want [skill]", incomplete)
	}
	// The activation set still names the tool the listing cannot show.
	names, err := r.Activated(ctx, "acme", "")
	if err != nil || len(names) != 1 || names[0] != "skill_dev" {
		t.Fatalf("Activated = %v, %v", names, err)
	}

	// Every source answering reports nothing missing.
	ok := freshRegistry(t)
	ok.Register(&fakeProvider{src: SourceConnector, tools: []Tool{tool("fetch", SourceConnector)}})
	if _, incomplete := ok.Listing(ctx, Scope{Org: "acme"}); len(incomplete) != 0 {
		t.Fatalf("incomplete = %v, want none", incomplete)
	}
}
