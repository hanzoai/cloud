// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
)

// serveCaps publishes commerce's own cap handlers on a real socket. The calls
// below dial it rather than going through plane.Call, which finds an app
// served in this process and runs the handler with no codec between: an
// in-process call hands the pointer over as it is and could never see the wire
// drop one.
func serveCaps(t *testing.T) string {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	const name = "capwire"
	app := zip.New(zip.Config{AppName: name})
	app.Post("/billing/alerts", planeAlerts, zip.WithOperationID(client.BillingAlerts))
	app.Post("/billing/alert/raise", planeAlertRaise, zip.WithOperationID(client.BillingAlertRaise))
	app.Post("/billing/alert/amend", planeAlertAmend, zip.WithOperationID(client.BillingAlertAmend))
	app.Post("/billing/alert/drop", planeAlertDrop, zip.WithOperationID(client.BillingAlertDrop))
	go func() { _ = app.Listen(zip.SocketPath(name)) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for range 200 {
		if c, err := net.Dial("unix", zip.SocketPath(name)); err == nil {
			_ = c.Close()
			return name
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never began listening", zip.SocketPath(name))
	return ""
}

// wire is one call through the socket and the plane codec, both ways.
func wire[In, Out any](ctx context.Context, app, op string, in *In) (*Out, error) {
	c, err := plane.Peer(app)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return zip.Call[In, Out](ctx, c, op, in)
}

// TestACapKeepsItsZeroesOnTheWire is the defect a cap showed in production: a
// pointer to false or to zero arrived as absent. enforce:false created a hard
// cap, PATCH enforce:false changed nothing, and a scope with no spend reported
// its spend as unknown instead of 0.
func TestACapKeepsItsZeroesOnTheWire(t *testing.T) {
	bootRecord(t)
	app := serveCaps(t)
	const org = "cap-wire"
	ctx := cloud.For(context.Background(), org)
	off, on := false, true

	raised, err := wire[client.AlertSpec, client.Alert](ctx, app, client.BillingAlertRaise, &client.AlertSpec{
		Subject: org, Title: "soft", Threshold: 500, Service: "nothing-spent-here", Enforce: &off,
	})
	if err != nil {
		t.Fatalf("raise: %v", err)
	}
	if raised.Enforce {
		t.Fatal("a cap raised with enforce:false came back enforcing")
	}
	if raised.PeriodSpentCents == nil || *raised.PeriodSpentCents != 0 {
		t.Fatalf("a scope with no spend reports periodSpentCents=%v, want 0", raised.PeriodSpentCents)
	}

	rows, err := wire[client.SubjectIn, client.Alerts](ctx, app, client.BillingAlerts, &client.SubjectIn{Subject: org})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows.Rows) != 1 || rows.Rows[0].Enforce || rows.Rows[0].PeriodSpentCents == nil || *rows.Rows[0].PeriodSpentCents != 0 {
		t.Fatalf("read back %+v, want one soft cap with periodSpentCents 0", rows.Rows)
	}

	for _, want := range []bool{true, false} {
		flip := &off
		if want {
			flip = &on
		}
		got, err := wire[client.AlertPatch, client.Alert](ctx, app, client.BillingAlertAmend, &client.AlertPatch{
			Subject: org, ID: raised.ID, Enforce: flip,
		})
		if err != nil {
			t.Fatalf("amend enforce:%v: %v", want, err)
		}
		if got.Enforce != want {
			t.Fatalf("PATCH enforce:%v left enforce=%v", want, got.Enforce)
		}
		if got.Threshold != 500 {
			t.Fatalf("PATCH enforce:%v moved the threshold to %d", want, got.Threshold)
		}
	}

	if _, err := wire[client.AlertRef, client.Dropped](ctx, app, client.BillingAlertDrop, &client.AlertRef{Subject: org, ID: raised.ID}); err != nil {
		t.Fatalf("drop: %v", err)
	}
}
