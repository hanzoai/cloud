package pricing

// Where the published price of OUR OWN models comes from.
//
// The served catalog is assembled from two sources, and this file is the client.
// github.com/hanzoai/pricing ships the document's shape and the resold
// third-party section; commerce owns the RETAIL NUMBER on the models we make.
// One owner per fact, which is the whole point: the number a customer is quoted
// for Enso or Zen now comes from the same place an admin edits it, instead of
// being typed a second time into a snapshot that then goes stale.
//
// It went stale exactly that way. The embedded snapshot this overlays is dated
// 2026-03-14 and contains NO Enso rows at all, while enso has billed 4/20 since
// the 2026-07-22/23 reprice — so the public price list has been silent about a
// family we charge for. Other copies of the same number drifted the other way
// and advertised 20/60, and marketing advertised 3/12, BELOW the charge.
//
// WHAT THIS DOES NOT DO. It does not touch billing. enso meters from its own
// catalog (hanzoai/zen catalog-enso.yaml, mounted via universe) and must keep
// doing so: a charge that fails when commerce is unreachable is strictly worse
// than one reading a local copy CI proves equal. This overlay moves only what is
// DISPLAYED. Commerce is the owner; the billing catalog is a replica; the guard
// in commerce (models/catalogentry/seed_test.go) is what keeps them equal.
//
// SCOPE. First-party families only (enso, zen) — the models whose price we SET,
// which is precisely where the divergence was. The resold third-party section
// stays on its existing sync path: its price is upstream cost x markup, it was
// never the bug, and 403 rows whose presence in the production store cannot be
// verified from here are not worth risking on a public endpoint. Completing the
// repoint so commerce owns every row is the follow-up that retires the snapshot.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hanzoai/commerce/models/catalogentry"
	luxlog "github.com/luxfi/log"
)

// overlay applies commerce's rates to the served document, reports what it did,
// and hands the document back either way.
//
// It never fails the caller. This surface is a public, heavily-read price list;
// refusing to boot because a catalog query failed would trade a stale price for
// no price at all. But it does not degrade QUIETLY either — the whole reason
// this file exists is that a wrong price sat unnoticed for weeks. Both failure
// modes are logged at a level that gets read: an unreachable store is an error,
// and a store that prices nothing is a warning naming exactly what that means.
//
// commerce reaches its database through a process-global set at commerce.Mount,
// so this resolves in the fused binary. Run as a lone plugin with no commerce in
// the process, the query fails and the snapshot is served — which is why the
// error says which prices are then suspect.
func overlay(ctx context.Context, data any, log luxlog.Logger) any {
	doc, ok := data.(map[string]any)
	if !ok {
		return data
	}
	priced, added, legacy, err := applyCommerceRates(ctx, doc)
	switch {
	case err != nil:
		log.Error("pricing: commerce rates unavailable — serving the embedded snapshot, whose first-party prices may be stale", "err", err)
	case priced == 0 && added == 0:
		log.Warn("pricing: commerce priced no first-party model — our own models are being published at the embedded snapshot's price, which is what went stale before")
	default:
		log.Info("pricing: first-party prices sourced from commerce",
			"repriced", priced, "added", added, "snapshotOnly", legacy)
	}
	return doc
}

// firstPartyCategories are the catalog categories holding models we make. They
// are the two families the pricing document's `hanzoModels` section describes.
var firstPartyCategories = []string{catalogentry.CategoryEnso, catalogentry.CategoryZen}

// applyCommerceRates rewrites the document's first-party section so every model
// commerce prices is published at commerce's price.
//
// A slug commerce and the snapshot BOTH carry keeps the snapshot's editorial
// copy and takes commerce's price — the copy is not commerce's to own. A slug
// only commerce carries is APPENDED, which is how Enso reaches the price list at
// all. A slug only the snapshot carries is left alone and counted as `legacy`:
// it is a row whose price still has no owner in commerce, and the count is
// returned so that remainder stays visible instead of quietly persisting.
//
// It returns counts rather than logging them so the caller decides how loud to
// be; a store with no first-party rows is NOT an error here (a fresh database
// has none) but it is a condition the caller reports.
func applyCommerceRates(ctx context.Context, doc map[string]any) (priced, added, legacy int, err error) {
	db := catalogentry.SystemDB(ctx)

	models := map[string]*catalogentry.CatalogEntry{}
	for _, cat := range firstPartyCategories {
		var entries []*catalogentry.CatalogEntry
		if _, qerr := catalogentry.Query(db).Filter("Category=", cat).GetAll(&entries); qerr != nil {
			return 0, 0, 0, fmt.Errorf("list %s models: %w", cat, qerr)
		}
		for _, e := range entries {
			// Unpublished rows are deliberately withheld from the public
			// projection — the Enso vision engines are internal and reached only
			// through the family's vision fallback. Publishing them here would
			// put them on the price list without the decision to publish them.
			if e == nil || !e.Published {
				continue
			}
			models[e.Slug] = e
		}
	}
	if len(models) == 0 {
		return 0, 0, 0, nil
	}

	section, _ := doc["hanzoModels"].([]any)
	out := make([]any, 0, len(section)+len(models))
	for _, raw := range section {
		m, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		name, _ := m["name"].(string)
		e, owned := models[name]
		if !owned {
			out = append(out, raw)
			legacy++
			continue
		}
		if p := pricingOf(e); p != nil {
			m["pricing"] = p
			priced++
		}
		out = append(out, m)
		delete(models, name)
	}

	// Whatever commerce prices that the snapshot never carried. Sorted by the
	// entry's own display rank so the family lists in the order admin set.
	for _, e := range byOrder(models) {
		m := map[string]any{
			"name":        e.Slug,
			"fullName":    e.Name,
			"description": e.Description,
		}
		if e.Spec != nil && e.Spec.ContextWindow > 0 {
			m["context"] = e.Spec.ContextWindow
		}
		p := pricingOf(e)
		if p == nil {
			// A listed model with no price is a billing hole. Commerce's own seed
			// guard forbids one; if it somehow appears, leave it off the list
			// rather than publish a model nobody can be charged for.
			continue
		}
		m["pricing"] = p
		out = append(out, m)
		added++
	}

	doc["hanzoModels"] = out
	return priced, added, legacy, nil
}

// pricingOf projects an entry's rate vector into the document's pricing block.
//
// The document has always carried JSON numbers, and its consumers parse numbers,
// so the exact decimal strings commerce stores are converted here — at the
// publish boundary, once. Billing never reads this block; it parses the decimal
// as money, which is why the precision loss is confined to display.
//
// Only the all-context per-MTok rates are read. A rate limited to a context rung
// has no selector on this surface, and guessing which rung to quote would post a
// price the caller may not be charged.
func pricingOf(e *catalogentry.CatalogEntry) map[string]any {
	if e == nil {
		return nil
	}
	out := map[string]any{}
	for _, r := range catalogentry.RatesOf(e) {
		if r.Unit != catalogentry.UnitMTok || r.MaxContext != 0 {
			continue
		}
		key := ""
		switch r.Key {
		case catalogentry.RateIn:
			key = "input"
		case catalogentry.RateOut:
			key = "output"
		case catalogentry.RateCacheRead:
			key = "cacheRead"
		case catalogentry.RateCacheWrite:
			key = "cacheWrite"
		default:
			continue
		}
		retail := r.RetailPrice(e.Markup)
		if retail == "" {
			continue
		}
		v, cerr := strconv.ParseFloat(retail, 64)
		if cerr != nil {
			continue
		}
		out[key] = v
	}
	if _, ok := out["input"]; !ok {
		return nil
	}
	if _, ok := out["output"]; !ok {
		return nil
	}
	// Absent cache components are absent, not zero: the snapshot distinguishes a
	// model that does not price caching from one that prices it at nothing.
	return out
}

// byOrder returns the entries sorted by display rank, then slug, so an append
// is deterministic across boots rather than following map iteration.
func byOrder(m map[string]*catalogentry.CatalogEntry) []*catalogentry.CatalogEntry {
	out := make([]*catalogentry.CatalogEntry, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, b := out[j-1], out[j]
			if a.Order < b.Order || (a.Order == b.Order && a.Slug <= b.Slug) {
				break
			}
			out[j-1], out[j] = b, a
		}
	}
	return out
}
