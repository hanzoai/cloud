// Package seed wires @hanzo/plans entries to payment-processor catalogs.
//
// SyncStripe is called at commerce bootstrap when STRIPE_SECRET_KEY is
// configured. It walks the static plan catalog loaded by api/billing (via
// the json:billing/plans/subscription.json embed) and ensures that every
// priced plan has a matching Stripe Product and Price.
//
// The sync is idempotent: Products are keyed by plan slug, Prices are keyed
// by {slug}-month / {slug}-year lookup_keys.
//
// Free plans (priceMonthly == 0) are skipped for Price creation because
// Stripe rejects zero-amount recurring prices. A Product is still created so
// the plan appears in the catalog for reporting.
package seed

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/payment/providers/stripe"
)

// Plan is the minimal catalog shape the seeder operates on. It mirrors
// billing.SeedPlan so the api/billing package can feed us without either
// side importing the other.
type Plan struct {
	Slug        string
	Name        string
	Description string
	Category    string
	PriceMonth  int64 // cents / month (0 = free)
	PriceYear   int64 // cents / month when billed annually (0 = free)
	Currency    string
}

// SyncResult summarises what the seeder did.
type SyncResult struct {
	Products []string
	Prices   []string
	Skipped  []string
}

// SyncStripe ensures every plan in `plans` has a Stripe Product (and per-
// interval Price where pricing is non-zero).
//
// categoryFilter, when non-empty, restricts the sync to plans whose Category
// matches. Use "world" for Hanzo World products, or "" to sync everything.
func SyncStripe(ctx context.Context, provider *stripe.Provider, plans []Plan, categoryFilter string) (*SyncResult, error) {
	if provider == nil {
		return nil, fmt.Errorf("seed: stripe provider is nil")
	}

	res := &SyncResult{}

	for _, pl := range plans {
		if categoryFilter != "" && !strings.EqualFold(pl.Category, categoryFilter) {
			continue
		}
		if pl.Slug == "" || pl.Name == "" {
			continue
		}

		// --- Product ---
		prod, err := provider.CreateOrUpdateProduct(ctx, stripe.Product{
			ID:          pl.Slug,
			Name:        pl.Name,
			Description: pl.Description,
			Active:      true,
			Metadata: map[string]string{
				"hanzo_plan_slug":     pl.Slug,
				"hanzo_plan_category": pl.Category,
			},
		})
		if err != nil {
			return res, fmt.Errorf("seed %s: product sync failed: %w", pl.Slug, err)
		}
		res.Products = append(res.Products, prod.ID)

		// --- Prices (monthly / yearly) ---
		currency := pl.Currency
		if currency == "" {
			currency = "usd"
		}

		if pl.PriceMonth > 0 {
			price, err := provider.EnsurePrice(ctx, prod.ID, pl.Slug+"-month", pl.PriceMonth, currency, "month")
			if err != nil {
				return res, fmt.Errorf("seed %s: monthly price sync failed: %w", pl.Slug, err)
			}
			res.Prices = append(res.Prices, price.ID)
		} else {
			res.Skipped = append(res.Skipped, pl.Slug+"-month (free)")
		}

		if pl.PriceYear > 0 {
			// Stripe yearly price = annual total in cents.
			// PriceYear is stored as "cents per month when billed annually",
			// matching the canonical plans JSON. Multiply by 12.
			yearlyCents := pl.PriceYear * 12
			price, err := provider.EnsurePrice(ctx, prod.ID, pl.Slug+"-year", yearlyCents, currency, "year")
			if err != nil {
				return res, fmt.Errorf("seed %s: yearly price sync failed: %w", pl.Slug, err)
			}
			res.Prices = append(res.Prices, price.ID)
		} else {
			res.Skipped = append(res.Skipped, pl.Slug+"-year (free)")
		}
	}

	return res, nil
}

// LogResult prints a concise seeder summary to w.
func LogResult(w io.Writer, res *SyncResult, err error, started time.Time) {
	if w == nil {
		w = os.Stdout
	}
	dur := time.Since(started).Round(time.Millisecond)
	if err != nil {
		fmt.Fprintf(w, "seed.stripe: FAILED after %s: %v\n", dur, err)
		return
	}
	fmt.Fprintf(w, "seed.stripe: products=%d prices=%d skipped=%d (%s)\n",
		len(res.Products), len(res.Prices), len(res.Skipped), dur)
	if len(res.Skipped) > 0 {
		fmt.Fprintf(w, "seed.stripe: skipped %v\n", res.Skipped)
	}
}
