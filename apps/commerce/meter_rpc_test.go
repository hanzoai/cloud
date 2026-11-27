// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/internal/planetest"
	credit "github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/zap-proto/zip"
)

// A usage ref already spent on another amount crosses the plane as a 409, so the
// caller in another process can tell a conflict it must resolve from a fault it
// may retry. A plain error would arrive as the latter, and that retry never clears.
func TestRecordAnswersAReusedRefAsAConflict(t *testing.T) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })

	m, err := meter.New(meter.Config{BaseURL: "http://in-process"})
	if err != nil {
		t.Fatalf("meter: %v", err)
	}
	o := meterOps{m: m}
	ctx := cloud.For(context.Background(), "acme")
	in := func(cents int64) *client.RecordIn {
		return &client.RecordIn{
			Subject: "acme",
			Amount:  client.Amount(credit.FromCents(cents).Unwrap()),
			Usage:   client.Usage{Service: "compute", Ref: "usage:admin/hanzo-visor:m1-h14"},
		}
	}

	if _, err := o.record(ctx, in(70)); err != nil {
		t.Fatalf("first record: %v", err)
	}
	if _, err := o.record(ctx, in(70)); err != nil {
		t.Fatalf("a replay of the same act must be a no-op, got %v", err)
	}
	_, err = o.record(ctx, in(500))
	he, ok := errors.AsType[*zip.HTTPError](err)
	if !ok || he.Status != http.StatusConflict {
		t.Fatalf("the same ref for another amount: %v, want a 409", err)
	}
}

// The gate every process in the fleet lands on, for a FUNDED org: no cap set is
// ok, a spent cap is cap-spent, and a cap nobody could read is cap-unread — never
// ok. The cap is asked by name, so it reaches the op this process serves rather
// than an HTTP address another process owns (which answered 404 and was read as
// "no cap").
func TestAuthorizeAnswersTheCapByName(t *testing.T) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	ctx := cloud.For(context.Background(), "acme")
	if _, err := fin.Deposit(ctx, credit.DepositInput{Org: "acme", Subject: "acme", Amount: credit.FromCents(10000), Ref: "sq_cap", Cash: true}); err != nil {
		t.Fatal(err)
	}
	m, err := meter.New(meter.Config{BaseURL: "http://in-process"})
	if err != nil {
		t.Fatalf("meter: %v", err)
	}
	o := meterOps{m: m}
	ask := &client.AuthorizeIn{Subject: "acme", Amount: client.Amount(credit.FromCents(1).Unwrap()), Service: "ai"}
	peer := planetest.Serve(t)

	if v, err := o.authorize(ctx, ask); err != nil || !v.OK {
		t.Fatalf("no cap set: %+v, %v; want ok", v, err)
	}
	peer.Cap(client.CapVerdict{Allow: false, Reason: "spend_cap", CapCents: 100, SpentCents: 100})
	if v, err := o.authorize(ctx, ask); err != nil || !v.CapSpent || v.OK {
		t.Fatalf("spent cap: %+v, %v; want cap-spent", v, err)
	}
	peer.CapFails(zip.Errorf(http.StatusBadGateway, "cap authorize: spend-cap: enforced cap's spend unreadable"))
	if v, err := o.authorize(ctx, ask); err != nil || !v.CapUnread || v.OK {
		t.Fatalf("unread cap: %+v, %v; want cap-unread", v, err)
	}
	if asks := peer.CapAsks(); len(asks) != 3 || asks[0] != "acme" {
		t.Fatalf("cap checks = %v, want three for acme", asks)
	}
}
