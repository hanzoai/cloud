package marketplace

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/tools"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// fakeProvider offers a fixed set of tools so the marketplace can list/price them.
type fakeProvider struct {
	tools []tools.Tool
}

func (f *fakeProvider) Source() tools.Source { return tools.SourceConnector }
func (f *fakeProvider) List(context.Context, tools.Scope) ([]tools.Tool, error) {
	return f.tools, nil
}
func (f *fakeProvider) Dispatch(context.Context, tools.Principal, string, map[string]any) (any, error) {
	return map[string]any{"ran": true}, nil
}

// fakeCharger records x402 settlements.
type fakeCharger struct {
	err     error
	charged []tools.Charge
}

func (f *fakeCharger) Charge(_ context.Context, ch tools.Charge) error {
	f.charged = append(f.charged, ch)
	return f.err
}

// setup wires a fresh activation store + a fake tool provider into the shared
// registry and mounts the marketplace (which installs its Pricer).
func setup(t *testing.T, offered ...string) *zip.App {
	t.Helper()
	act, err := tools.OpenActivationStore(t.TempDir() + "/act.db")
	if err != nil {
		t.Fatalf("OpenActivationStore: %v", err)
	}
	t.Cleanup(func() { _ = act.Close() })
	tools.Default().SetActivation(act)
	tools.Default().SetCharger(nil) // reset any prior test's charger.

	var offer []tools.Tool
	for _, name := range offered {
		offer = append(offer, tools.Tool{Name: name, Source: tools.SourceConnector, Dispatchable: true})
	}
	tools.Default().Register(&fakeProvider{tools: offer})

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app
}

func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestInstallActivates: marketplace "install" IS the tool-plane activation write —
// after install, the registry reports the tool activated for that (org, project),
// and uninstall reverses it.
func TestInstallActivates(t *testing.T) {
	app := setup(t, "conn_alpha")

	if tools.Default().Exists(context.Background(), tools.Scope{Org: "acme"}, "conn_alpha") != true {
		t.Fatal("fixture tool must exist")
	}
	code, _ := do(t, app, http.MethodPost, "/v1/marketplace/install", "acme", map[string]any{"tool": "conn_alpha"})
	if code != 200 {
		t.Fatalf("install want 200, got %d", code)
	}
	enabled, _ := tools.Default().Activated(context.Background(), "acme", "")
	if len(enabled) != 1 || enabled[0] != "conn_alpha" {
		t.Fatalf("install must activate conn_alpha, got %v", enabled)
	}
	// Cross-org isolation: org "evil" did not install it.
	other, _ := tools.Default().Activated(context.Background(), "evil", "")
	if len(other) != 0 {
		t.Fatalf("cross-org must not see acme's install, got %v", other)
	}
	// Uninstall reverses.
	code, _ = do(t, app, http.MethodPost, "/v1/marketplace/uninstall", "acme", map[string]any{"tool": "conn_alpha"})
	if code != 200 {
		t.Fatalf("uninstall want 200, got %d", code)
	}
	enabled, _ = tools.Default().Activated(context.Background(), "acme", "")
	if len(enabled) != 0 {
		t.Fatalf("uninstall must deactivate, got %v", enabled)
	}
}

// TestInstallPhantom: installing a tool no source offers is 422 — no phantom installs.
func TestInstallPhantom(t *testing.T) {
	app := setup(t, "conn_beta")
	code, _ := do(t, app, http.MethodPost, "/v1/marketplace/install", "acme", map[string]any{"tool": "does_not_exist"})
	if code != 422 {
		t.Fatalf("phantom install want 422, got %d", code)
	}
}

// TestPublishValidation: a monetized listing MUST name a recipient wallet; a listing
// for a phantom tool is refused.
func TestPublishValidation(t *testing.T) {
	app := setup(t, "conn_gamma")

	// Monetized with no recipient → 400.
	code, _ := do(t, app, http.MethodPost, "/v1/marketplace/listings", "acme", publishReq{
		Tool: "conn_gamma", Title: "Gamma", PriceCents: 500,
	})
	if code != 400 {
		t.Fatalf("monetized listing without recipient want 400, got %d", code)
	}
	// Phantom tool → 422.
	code, _ = do(t, app, http.MethodPost, "/v1/marketplace/listings", "acme", publishReq{
		Tool: "ghost", Title: "Ghost",
	})
	if code != 422 {
		t.Fatalf("phantom listing want 422, got %d", code)
	}
	// Valid free listing → 201.
	code, body := do(t, app, http.MethodPost, "/v1/marketplace/listings", "acme", publishReq{
		Tool: "conn_gamma", Title: "Gamma", Public: true,
	})
	if code != 201 {
		t.Fatalf("valid listing want 201, got %d (%s)", code, body)
	}
}

// TestMonetizedDispatchCharges: the full chain — publish a MONETIZED public listing,
// install it, then dispatch the tool. The registry consults the marketplace Pricer,
// settles through the x402 Charger seam with the seller's price + recipient wallet,
// and only then runs the tool.
func TestMonetizedDispatchCharges(t *testing.T) {
	app := setup(t, "conn_premium")

	// Publish a monetized public listing.
	code, body := do(t, app, http.MethodPost, "/v1/marketplace/listings", "acme", publishReq{
		Tool: "conn_premium", Title: "Premium", PriceCents: 750, Currency: "USD",
		Recipient: "0xSELLERWALLET", Public: true,
	})
	if code != 201 {
		t.Fatalf("publish monetized want 201, got %d (%s)", code, body)
	}

	// A BUYER org installs it.
	if code, _ := do(t, app, http.MethodPost, "/v1/marketplace/install", "buyer", map[string]any{"tool": "conn_premium"}); code != 200 {
		t.Fatalf("install want 200, got %d", code)
	}

	// With NO charger wired, a priced dispatch fails closed.
	_, err := tools.Default().Dispatch(context.Background(), tools.Principal{Org: "buyer", Owner: "buyer"}, "conn_premium", nil)
	if err == nil {
		t.Fatal("priced dispatch with no charger must fail closed")
	}

	// Wire the x402 charger; dispatch now settles to the seller, then runs.
	charger := &fakeCharger{}
	tools.Default().SetCharger(charger)
	out, err := tools.Default().Dispatch(context.Background(), tools.Principal{Org: "buyer", Owner: "buyer"}, "conn_premium", nil)
	if err != nil {
		t.Fatalf("settled dispatch must run, got %v", err)
	}
	if m, _ := out.(map[string]any); m["ran"] != true {
		t.Fatalf("tool must run after settlement, got %v", out)
	}
	if len(charger.charged) != 1 {
		t.Fatalf("expected exactly one x402 settlement, got %d", len(charger.charged))
	}
	c := charger.charged[0]
	if c.Cents != 750 || c.Recipient != "0xSELLERWALLET" || c.Currency != "USD" || c.Payer != "buyer" {
		t.Fatalf("x402 charge mismatch: %+v", c)
	}
}

// TestDiscovery: discovery returns the catalog with the listing overlay + installed flag.
func TestDiscovery(t *testing.T) {
	app := setup(t, "conn_delta")
	do(t, app, http.MethodPost, "/v1/marketplace/listings", "acme", publishReq{
		Tool: "conn_delta", Title: "Delta Tool", Category: "search", Public: true,
	})
	do(t, app, http.MethodPost, "/v1/marketplace/install", "acme", map[string]any{"tool": "conn_delta"})

	code, body := do(t, app, http.MethodGet, "/v1/marketplace", "acme", nil)
	if code != 200 {
		t.Fatalf("discover want 200, got %d", code)
	}
	var out struct {
		Items []struct {
			Name      string `json:"name"`
			Title     string `json:"title"`
			Category  string `json:"category"`
			Installed bool   `json:"installed"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
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
		t.Fatalf("discovery must include conn_delta, got %+v", out.Items)
	}
}
