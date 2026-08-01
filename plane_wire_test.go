package cloud_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// The plane's bytes are ZAP. A recognisable value goes across and the field
// NAMES must not appear anywhere on the wire — under JSON they would, which is
// what makes this a positive identification rather than an inference from the
// call having worked.
func TestPlaneWireCarriesNoFieldNames(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", runDir(t))
	cloud.ResetPlane()

	var seen []byte
	app := zip.New(zip.Config{AppName: "probe"})
	app.Use(func(c *zip.Ctx) error {
		seen = append([]byte(nil), c.Body()...)
		return c.Next()
	})
	zip.Post[plane.BalanceIn, plane.Balance](app, "/probe/balance",
		func(context.Context, *plane.BalanceIn) (*plane.Balance, error) {
			return &plane.Balance{Amount: plane.Money{Decimal: "50.00", Currency: "USD"}}, nil
		}, zip.WithOperationID("probe_balance"))
	go func() { _ = app.Listen(zip.SocketPath("probe")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, "probe")

	out, err := cloud.Ask[plane.BalanceIn, plane.Balance](cloud.For(context.Background(), "acme"),
		"probe", "probe_balance", &plane.BalanceIn{Subject: "acme", Currency: "usd"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.Amount.Decimal != "50.00" {
		t.Fatalf("reply lost its value: %+v", out)
	}
	if len(seen) == 0 {
		t.Fatal("no body was observed")
	}
	body := string(seen)
	for _, name := range []string{`"subject"`, `"currency"`, `{`, `}`} {
		if strings.Contains(body, name) {
			t.Fatalf("the request body contains %s — that is JSON, not ZAP: %q", name, body)
		}
	}
	// The VALUES are there verbatim, because ZAP stores text as text.
	if !strings.Contains(body, "acme") || !strings.Contains(body, "usd") {
		t.Fatalf("the request body did not carry its values: %q", body)
	}
}
