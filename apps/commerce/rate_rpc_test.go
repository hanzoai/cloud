// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"testing"
	"time"

	commercemod "github.com/hanzoai/commerce"
	commercerate "github.com/hanzoai/commerce/models/rate"

	"github.com/hanzoai/cloud/client"
)

// A PRICE THAT CANNOT BE READ MUST NOT READ AS FREE. Every case here is one way
// the authority fails to answer, and the property that has to hold across all of
// them is that the caller can TELL — because the caller's fallback is the number
// it charged yesterday, and the only thing worse than a stale price is a zero one.

// boot stands the embedded commerce up for one test. It is named for what it
// does rather than for the first test that needed it — nothing here is about
// rates, and a helper named after one caller is a helper the next caller copies.
func boot(t *testing.T) {
	t.Helper()
	emb, err := commercemod.Embed(context.Background(), commercemod.EmbedConfig{DataDir: t.TempDir(), Dev: true})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	PublishEmbedded(emb)
	t.Cleanup(func() {
		PublishEmbedded(nil)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = emb.Stop(ctx)
	})
}

// write a row straight into the authority — the same namespace and model the
// seed and the admin CRUD write.
func putRate(t *testing.T, ctx context.Context, product, meter, unit string, nano int64, status string) {
	t.Helper()
	row := commercerate.New(commercerate.AuthorityDB(ctx))
	row.Product, row.Meter, row.Unit, row.Rate = product, meter, unit, nano
	row.Currency, row.Status = "USD", status
	row.Bind()
	if err := row.Create(); err != nil {
		t.Fatalf("seed rate %s/%s: %v", product, meter, err)
	}
}

func TestRate_AnswersThePublishedPrice(t *testing.T) {
	boot(t)
	ctx := context.Background()
	putRate(t, ctx, "storage", "cold-gb-month", "GB-month", 80_000_000, "")

	out, err := planeRate(ctx, &client.RateIn{Product: "storage", Meter: "cold-gb-month"})
	if err != nil {
		t.Fatalf("planeRate: %v", err)
	}
	if !out.Found {
		t.Fatal("a published rate answered Found=false, so every caller charges its floor " +
			"and the operator's price does nothing")
	}
	if out.Nano != 80_000_000 {
		t.Errorf("nano = %d, want 80000000", out.Nano)
	}
	if out.Unit != "GB-month" {
		t.Errorf("unit = %q — without it a reader cannot tell a GB-month from a thousand "+
			"characters", out.Unit)
	}
}

// ZERO IS A PRICE. Something the platform meters and gives away is not the same
// as something nobody has priced, and a number alone cannot carry the difference.
func TestRate_ZeroIsPublishedNotAbsent(t *testing.T) {
	boot(t)
	ctx := context.Background()
	putRate(t, ctx, "translate", "given-away", "1k characters", 0, "")

	out, err := planeRate(ctx, &client.RateIn{Product: "translate", Meter: "given-away"})
	if err != nil {
		t.Fatalf("planeRate: %v", err)
	}
	if !out.Found {
		t.Fatal("a rate published at zero read as ABSENT, so the caller falls back to its " +
			"floor and charges for work an operator said to give away")
	}
	if out.Nano != 0 {
		t.Errorf("nano = %d, want 0", out.Nano)
	}
}

// An absent rate is not an error: a meter nobody has priced is the ordinary state
// of a new one, and failing would stop the work over a missing row.
func TestRate_AbsentIsNotAnError(t *testing.T) {
	boot(t)
	out, err := planeRate(context.Background(), &client.RateIn{Product: "storage", Meter: "nothing-here"})
	if err != nil {
		t.Fatalf("an unpublished rate returned an error: %v — a meter nobody has priced "+
			"must not stop the work it meters", err)
	}
	if out.Found {
		t.Error("an unpublished rate answered Found=true")
	}
	if out.Nano != 0 {
		t.Errorf("nano = %d for an absent rate, want 0", out.Nano)
	}
}

// A row that is stored but NOT SOLD is not what the platform charges today, so it
// reads as absent — the same answer the public plan catalog gives for one.
func TestRate_AnUnlistedRowDoesNotPrice(t *testing.T) {
	boot(t)
	ctx := context.Background()
	for _, status := range []string{"archived", "draft"} {
		putRate(t, ctx, "risk", "screen-"+status, "screen", 999_000, status)
		out, err := planeRate(ctx, &client.RateIn{Product: "risk", Meter: "screen-" + status})
		if err != nil {
			t.Fatalf("planeRate(%s): %v", status, err)
		}
		if out.Found {
			t.Errorf("a %s rate priced the meter at %d — a row that is not sold must not "+
				"charge anyone", status, out.Nano)
		}
	}
}

// Identity is BOTH parts, and a request that names neither is a bad request
// rather than a lookup of "/".
func TestRate_RefusesAHalfNamedMeter(t *testing.T) {
	boot(t)
	for _, in := range []client.RateIn{
		{Product: "storage"},
		{Meter: "block-gb-month"},
		{},
		{Product: "  ", Meter: "  "},
	} {
		if _, err := planeRate(context.Background(), &in); err == nil {
			t.Errorf("planeRate(%+v) answered without an identity", in)
		}
	}
}

// With no commerce in the process there is no authority to read, and that is an
// ERROR rather than an absent rate: the caller must be able to tell "nobody has
// priced this" from "the price is unreadable here".
func TestRate_RefusesWhenCommerceIsNotHere(t *testing.T) {
	PublishEmbedded(nil)
	if _, err := planeRate(context.Background(), &client.RateIn{Product: "storage", Meter: "block-gb-month"}); err == nil {
		t.Fatal("planeRate answered with no commerce beside it; an unreadable authority " +
			"must not be reported as an unpriced meter")
	}
}
