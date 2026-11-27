package marketplace

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
)

// TestInstallActivates: marketplace "install" IS the tool-plane activation write —
// after install, the registry reports the tool activated for that (org, project),
// and uninstall reverses it. The marketplace reaches the registry over the plane,
// as it must in the fleet, where the registry is never in its process.
func TestInstallActivates(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t, "conn_alpha")

		e.install("acme", "conn_alpha")
		enabled, _ := tools.Default().Activated(context.Background(), "acme", "")
		if len(enabled) != 1 || enabled[0] != "conn_alpha" {
			t.Fatalf("install must activate conn_alpha, got %v", enabled)
		}
		// Cross-org isolation: org "evil" did not install it.
		if other, _ := tools.Default().Activated(context.Background(), "evil", ""); len(other) != 0 {
			t.Fatalf("cross-org must not see acme's install, got %v", other)
		}
		e.must(200, http.MethodPost, "/v1/marketplace/uninstall", member("acme"), map[string]any{"tool": "conn_alpha"}, nil)
		if enabled, _ = tools.Default().Activated(context.Background(), "acme", ""); len(enabled) != 0 {
			t.Fatalf("uninstall must deactivate, got %v", enabled)
		}
	})
}

// TestInstallPhantom: installing a tool no source offers is 422 — no phantom installs.
func TestInstallPhantom(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t, "conn_beta")
		if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/install", member("acme"), "", map[string]any{"tool": "does_not_exist"}); code != 422 {
			t.Fatalf("phantom install want 422, got %d %s", code, b)
		}
	})
}

// TestPublishValidation: a monetized listing MUST name a recipient wallet; a listing
// for a phantom tool is refused; only an org admin publishes; and a tool every org
// reaches is the platform's alone to sell.
func TestPublishValidation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t, "conn_gamma")

		for _, tc := range []struct {
			name string
			as   who
			in   publishReq
			want int
		}{
			{"monetized with no recipient", admin("acme"), publishReq{Tool: "conn_gamma", Title: "Gamma", Price: "5.00"}, 400},
			{"a phantom tool", admin("acme"), publishReq{Tool: "ghost", Title: "Ghost"}, 422},
			{"a kind that does not exist", admin("acme"), publishReq{Kind: "planet", Tool: "conn_gamma", Title: "Gamma"}, 400},
			{"a script for docs", admin("acme"), publishReq{Tool: "conn_gamma", Title: "Gamma", Docs: "javascript:alert(1)"}, 400},
			{"a protocol-relative docs path", admin("acme"), publishReq{Tool: "conn_gamma", Title: "Gamma", Docs: "//evil.example/x"}, 400},
			{"a member who is not an admin", member("acme"), publishReq{Tool: "conn_gamma", Title: "Gamma"}, 403},
			{"no one at all", anyone, publishReq{Tool: "conn_gamma", Title: "Gamma"}, 401},
			{"a platform tool, by an org", admin("acme"), publishReq{Tool: "conn_gamma", Title: "Gamma"}, 403},
			{"a platform tool, by a SuperAdmin inspecting an org", who{org: "acme", admin: true, super: true},
				publishReq{Tool: "conn_gamma", Title: "Gamma"}, 403},
		} {
			if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/listings", tc.as, "", tc.in); code != tc.want {
				t.Errorf("%s = %d (%s), want %d", tc.name, code, b, tc.want)
			}
		}
		l := e.shelve(publishReq{Tool: "conn_gamma", Title: "Gamma", Public: true, Docs: "/docs/tools/gamma"})
		if l.Kind != KindTool || l.PublisherOrg != platform || l.Ref != client.ToolResource("connector", "conn_gamma") ||
			l.Docs != "/docs/tools/gamma" || l.UpdatedAt == 0 {
			t.Fatalf("published %+v, want the platform's tool listing, priced by its resource, with its docs", l)
		}
	})
}

// TestMonetizedListingIsPriced: publishing a monetized listing puts a real price
// into the x402 table under the tool's own resource id, with the payee taken from
// the ROW — the publisher org and the wallet it named. This is the price every
// enforcement path reads; that it settles is proved end to end in payments_test.go.
// The same name from another source is another tool, and costs nothing.
func TestMonetizedListingIsPriced(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t, "conn_premium")
		wallet := e.createWallet(platform)
		e.shelve(publishReq{Tool: "conn_premium", Title: "Premium", Price: "0.0025", Currency: "USD",
			Recipient: wallet, Public: true})

		terms, priced, err := (&registry{store: mounted.State.store}).Price(context.Background(), client.ToolResource("connector", "conn_premium"))
		if err != nil || !priced {
			t.Fatalf("published listing must be priced: priced=%v err=%v", priced, err)
		}
		want, _ := money.ParseUSD("0.0025")
		if terms.Amount.Cmp(want) != 0 {
			t.Fatalf("price = %s, want exactly 0.0025 (a sub-cent price is a price)", terms.Amount.String())
		}
		if terms.RecipientOrg != platform || terms.RecipientWalletID != wallet || terms.Category != client.CategoryService {
			t.Fatalf("payee and category must come from the row: %+v", terms)
		}

		// A tool nobody listed, the same name run from an org's own row, and any
		// resource that is neither a tool nor a job, are free.
		for _, resource := range []string{client.ToolResource("connector", "conn_unlisted"), client.ToolResource("function", "conn_premium"),
			client.ToolResource("mcp", "conn_premium"), "tool:conn_premium", "/v1/marketplace", client.JobResource("job_none")} {
			if _, priced, err := (&registry{store: mounted.State.store}).Price(context.Background(), resource); priced || err != nil {
				t.Fatalf("%s must be unpriced, got priced=%v err=%v", resource, priced, err)
			}
		}
	})
}

// TestDiscovery: discovery returns the catalog with the listing overlay + installed flag.
func TestDiscovery(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t, "conn_delta")
		e.shelve(publishReq{Tool: "conn_delta", Title: "Delta Tool", Category: "search", Public: true})
		e.install("acme", "conn_delta")

		var out struct {
			Items []struct {
				Name      string `json:"name"`
				Title     string `json:"title"`
				Category  string `json:"category"`
				Installed bool   `json:"installed"`
			} `json:"items"`
		}
		e.must(200, http.MethodGet, "/v1/marketplace", member("acme"), nil, &out)
		found := false
		for _, it := range out.Items {
			if it.Name == "conn_delta" {
				found = true
				if it.Title != "Delta Tool" || it.Category != "search" || !it.Installed {
					t.Fatalf("discovery overlay/installed wrong: %+v", it)
				}
			}
		}
		if !found {
			b, _ := json.Marshal(out.Items)
			t.Fatalf("discovery must include conn_delta, got %s", b)
		}
	})
}
