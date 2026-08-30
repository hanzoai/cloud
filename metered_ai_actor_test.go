package cloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// underRequest runs fn inside a real request, with the identity headers a
// gateway mints, so the ctx fn receives is the one a handler actually gets.
// Nothing here reconstructs a context: Bridge parks the request the same way
// serve.go installs it for the whole binary.
func underRequest(t *testing.T, org, user string, fn func(ctx context.Context)) {
	t.Helper()
	app := zip.New(zip.Config{AppName: "actor-test"})
	app.Use(Bridge())
	app.Get("/probe", func(c *zip.Ctx) error {
		fn(c.Context())
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	if err := app.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	_ = resp.Body.Close()
}

// TestNamedReadsTheLiveCaller is the half of the fix that covers every surface
// that is NOT an agent run — the console assistant and every other app that buys
// a completion for whoever is on the wire. They all pass through this one
// wrapper, so none of them has a field to forget.
func TestNamedReadsTheLiveCaller(t *testing.T) {
	inner := &recordingAI{resp: &types.ChatResponse{Content: "hi", TotalTokens: 10}}
	m := passthroughMetered(inner)

	underRequest(t, "acme", "alice", func(ctx context.Context) {
		if _, err := m.ChatCompletion(ctx, &types.ChatRequest{Model: "x", Prompt: "yo", Org: "acme"}); err != nil {
			t.Fatalf("chat: %v", err)
		}
	})
	if got, want := inner.chatReq.Actor, "acme/alice"; got != want {
		t.Fatalf("actor = %q, want %q — a completion bought for a signed-in person "+
			"reached the gateway naming only the deployment's own application", got, want)
	}
}

// TestNamedNeverOverwritesAStatedActor pins the ORDERING, which is the whole
// rule. A run states the person it acts for because its context is detached; if
// a live request could overwrite that, a nested run inside somebody else's HTTP
// call would be billed to the wrong human.
func TestNamedNeverOverwritesAStatedActor(t *testing.T) {
	inner := &recordingAI{resp: &types.ChatResponse{Content: "hi", TotalTokens: 10}}
	m := passthroughMetered(inner)

	underRequest(t, "acme", "alice", func(ctx context.Context) {
		req := &types.ChatRequest{Model: "x", Prompt: "yo", Org: "acme", Actor: "acme/U-slack-123"}
		if _, err := m.ChatCompletion(ctx, req); err != nil {
			t.Fatalf("chat: %v", err)
		}
		if req.Actor != "acme/U-slack-123" {
			t.Fatalf("the caller's own request was mutated: %q", req.Actor)
		}
	})
	if got, want := inner.chatReq.Actor, "acme/U-slack-123"; got != want {
		t.Fatalf("actor = %q, want the STATED %q", got, want)
	}
}

// TestNamedInventsNobody: off the HTTP path, and for an unvalidated caller,
// there is no person and the honest answer is to say so. Naming the credential
// would be exactly the defect this change removes, one level down.
func TestNamedInventsNobody(t *testing.T) {
	cases := []struct{ name, org, user string }{
		{"no request at all", "", ""},
		{"org header but no validated user", "acme", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &recordingAI{resp: &types.ChatResponse{Content: "hi", TotalTokens: 10}}
			m := passthroughMetered(inner)
			call := func(ctx context.Context) {
				if _, err := m.ChatCompletion(ctx, &types.ChatRequest{Model: "x", Prompt: "yo", Org: "acme"}); err != nil {
					t.Fatalf("chat: %v", err)
				}
			}
			if tc.org == "" && tc.user == "" {
				call(context.Background())
			} else {
				underRequest(t, tc.org, tc.user, call)
			}
			if got := inner.chatReq.Actor; got != "" {
				t.Fatalf("actor = %q, want empty — nobody asked for this", got)
			}
		})
	}
}

// TestNamedCannotMoveTheDebit is the safety argument stated as a test. The actor
// is attribution; the money is `payer`, which reads BillingOrg/Org and nothing
// else. A caller naming a colleague must not shift one cent.
func TestNamedCannotMoveTheDebit(t *testing.T) {
	fc := &fakeCommerce{balanceBody: `{"available":100000}`}
	srv := fc.server(t)
	inner := &recordingAI{resp: &types.ChatResponse{Content: "hi", TotalTokens: 100}}
	m := &meteredAI{
		inner: inner,
		meter: NewMeter(Deps{Metering: mustClient(t, srv.URL, false)}, AIMeterProvider),
		rate:  defaultAIPriceUUSDPer1kTokens,
	}
	if _, err := m.ChatCompletion(context.Background(), &types.ChatRequest{
		Model: "x", Prompt: "yo", Org: "acme", Actor: "victim/bob",
	}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("debits = %d, want 1", fc.usages())
	}
	d, ok := fc.debits.last()
	if !ok {
		t.Fatal("no debit crossed the money plane")
	}
	if d.Org != "acme" {
		t.Fatalf("debited org %q, want acme — a stated actor moved the money", d.Org)
	}
	if d.In.Subject == "victim/bob" {
		t.Fatalf("the debit's WALLET became the stated actor (%q) — attribution reached the money", d.In.Subject)
	}
	// And the audit half IS carried, which is the point of stating it: a debit
	// was traceable to an org and never to a person.
	if got := d.In.Usage.Actor; got != "victim/bob" {
		t.Fatalf("Usage.Actor = %q, want the request's actor on the debit's audit trail", got)
	}
}
