package cloud_test

import (
	"context"
	"github.com/hanzoai/cloud/internal/planetest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// A peer that is THIS process is reached without a wire.
//
// This test used to assert the opposite shape: it observed the bytes crossing
// the socket and proved they were ZAP rather than JSON — the field NAMES absent,
// the values present. That assertion was right and it is still made, in the one
// place it belongs: internal/zapenc's TestNothingIsJSON and TestBytesAreAZAPMessage
// pin the encoding at the encoder. Restating it here bought a second copy of a
// fact zip already owns.
//
// What is true HERE, and was not true before zip v1.27.0, is that a co-resident
// call has no bytes to inspect at all. [zip.Serving] answers with the App bound
// to a name in this process, and Ask hands the call to that op's own invoke client
// instead of dialling its socket. Nothing about the call needed a wire; only the
// addressing did.
//
// So the middleware below is the instrument, and an EMPTY observation is the
// result: if a body ever appears, cloud has started serialising a value, handing
// it to the kernel, reading it back and parsing a fresh copy, to reach a function
// pointer that was in memory the whole time.
func TestCoresidentCallTakesNoWire(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	cloud.ResetPlane()

	var seen []byte
	app := zip.New(zip.Config{AppName: "probe"})
	app.Use(zip.H(func(c *zip.Ctx) error {
		seen = append([]byte(nil), c.Body()...)
		return c.Next()
	}))
	zip.Post[client.BalanceIn, client.Balance](app, "/probe/balance",
		func(context.Context, *client.BalanceIn) (*client.Balance, error) {
			return &client.Balance{Amount: client.Money{Decimal: "50.00", Currency: "USD"}}, nil
		}, zip.WithOperationID("probe_balance"))
	go func() { _ = app.Listen(zip.SocketPath("probe")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, "probe")

	out, err := cloud.Ask[client.BalanceIn, client.Balance](cloud.For(context.Background(), "acme"),
		"probe", "probe_balance", &client.BalanceIn{Subject: "acme", Currency: "usd"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}

	// The answer is the op's answer, so the op ran — the client is the same
	// validate → authorize → run core every other endpoint lands on.
	if out.Amount.Decimal != "50.00" {
		t.Fatalf("reply lost its value: %+v", out)
	}
	// And it ran without a byte crossing the socket that is bound and listening
	// three lines up.
	if len(seen) != 0 {
		t.Fatalf("a co-resident call went over the wire: %d bytes, %q", len(seen), seen)
	}
}
