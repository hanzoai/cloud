// Package stripe — catalog (Products and Prices) helpers used by the plan seeder.
//
// These methods are deliberately kept on Provider (not the generic processor
// interface) because catalog management is Stripe-specific. The seeder calls
// them at startup to ensure every @hanzo/plans entry has a corresponding
// Stripe Product and monthly/annual Price — idempotent via lookup keys.
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Product represents a Stripe Product (catalog item).
type Product struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Active      bool              `json:"active"`
	Metadata    map[string]string `json:"metadata"`
}

// Price represents a Stripe Price attached to a Product.
type Price struct {
	ID          string            `json:"id"`
	Product     string            `json:"product"`
	Active      bool              `json:"active"`
	Currency    string            `json:"currency"`
	UnitAmount  int64             `json:"unit_amount"`
	LookupKey   string            `json:"lookup_key"`
	Recurring   *PriceRecurring   `json:"recurring"`
	Metadata    map[string]string `json:"metadata"`
}

// PriceRecurring describes the billing cadence.
type PriceRecurring struct {
	Interval      string `json:"interval"`       // "month" | "year"
	IntervalCount int    `json:"interval_count"` // normally 1
}

// GetProduct fetches a product by its ID. Returns nil if not found.
func (p *Provider) GetProduct(ctx context.Context, id string) (*Product, error) {
	if id == "" {
		return nil, fmt.Errorf("stripe: product id required")
	}
	var out Product
	if err := p.get(ctx, "/products/"+id, nil, &out); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// CreateOrUpdateProduct creates a Stripe Product if one does not exist with the
// given ID, otherwise updates name/description/metadata. The ID is specified
// by the caller (we use the plan slug — e.g. "world-pro") so the operation is
// idempotent across deploys.
func (p *Provider) CreateOrUpdateProduct(ctx context.Context, prod Product) (*Product, error) {
	if prod.ID == "" {
		return nil, fmt.Errorf("stripe: product ID required for idempotent upsert")
	}

	existing, err := p.GetProduct(ctx, prod.ID)
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("name", prod.Name)
	if prod.Description != "" {
		params.Set("description", prod.Description)
	}
	params.Set("active", strconv.FormatBool(prod.Active || existing == nil))
	for k, v := range prod.Metadata {
		params.Set("metadata["+k+"]", v)
	}

	var out Product
	if existing == nil {
		params.Set("id", prod.ID)
		if err := p.post(ctx, "/products", params, &out); err != nil {
			return nil, err
		}
	} else {
		if err := p.post(ctx, "/products/"+prod.ID, params, &out); err != nil {
			return nil, err
		}
	}
	return &out, nil
}

// FindPriceByLookupKey returns the first active price matching the key, or nil.
func (p *Provider) FindPriceByLookupKey(ctx context.Context, lookupKey string) (*Price, error) {
	if lookupKey == "" {
		return nil, fmt.Errorf("stripe: lookup_key required")
	}
	params := url.Values{}
	params.Set("lookup_keys[]", lookupKey)
	params.Set("active", "true")
	params.Set("limit", "1")

	var list struct {
		Data []Price `json:"data"`
	}
	if err := p.get(ctx, "/prices/search", nil, &list); err != nil {
		// /prices/search requires API permissions; fall back to list with expand.
		return p.findPriceByLookupKeyFallback(ctx, lookupKey)
	}
	if len(list.Data) == 0 {
		return nil, nil
	}
	return &list.Data[0], nil
}

// findPriceByLookupKeyFallback uses GET /v1/prices (not search) to find a price.
func (p *Provider) findPriceByLookupKeyFallback(ctx context.Context, lookupKey string) (*Price, error) {
	params := url.Values{}
	params.Set("lookup_keys[]", lookupKey)
	params.Set("active", "true")
	params.Set("limit", "1")

	var list struct {
		Data []Price `json:"data"`
	}
	if err := p.get(ctx, "/prices", params, &list); err != nil {
		return nil, err
	}
	if len(list.Data) == 0 {
		return nil, nil
	}
	return &list.Data[0], nil
}

// EnsurePrice finds-or-creates a recurring Price attached to a Product. Idempotent
// via lookup_key: the caller supplies a stable key like "world-pro-month". If a
// matching active Price exists, it is returned unchanged; Stripe prices are
// immutable so we never update them.
func (p *Provider) EnsurePrice(ctx context.Context, productID, lookupKey string, unitAmountCents int64, currency, interval string) (*Price, error) {
	if productID == "" || lookupKey == "" {
		return nil, fmt.Errorf("stripe: productID and lookupKey required")
	}
	if interval != "month" && interval != "year" {
		return nil, fmt.Errorf("stripe: interval must be month or year, got %q", interval)
	}

	found, err := p.FindPriceByLookupKey(ctx, lookupKey)
	if err != nil {
		return nil, err
	}
	if found != nil {
		// Re-use existing price. Price amount is immutable — if it drifts, the
		// operator must manually deactivate the old price and re-run seed.
		return found, nil
	}

	params := url.Values{}
	params.Set("product", productID)
	params.Set("unit_amount", strconv.FormatInt(unitAmountCents, 10))
	params.Set("currency", strings.ToLower(currency))
	params.Set("lookup_key", lookupKey)
	params.Set("recurring[interval]", interval)
	params.Set("recurring[interval_count]", "1")
	params.Set("active", "true")

	var out Price
	if err := p.post(ctx, "/prices", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// isNotFound returns true if the Stripe error represents a 404.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	// PaymentError from stripe.doRequest has .Code
	type coder interface{ Code() string }
	if c, ok := err.(coder); ok && c.Code() == "resource_missing" {
		return true
	}
	return strings.Contains(err.Error(), "resource_missing") ||
		strings.Contains(err.Error(), "No such product") ||
		strings.Contains(err.Error(), "No such price")
}

// DecodePriceJSON is a test helper for parsing price JSON payloads.
func DecodePriceJSON(raw []byte) (*Price, error) {
	var pr Price
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}
