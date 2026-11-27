// Package plan holds the pure-Go plan tier and licence predicates shared by OSS
// subsystems, plus an optional EntitlementsResolver hook that commercial plan
// catalogs (such as hanzo-inc/cloud's @hanzo/plans plugin) can register into.
//
// When no commercial catalog is registered, Entitlements fails closed with
// ErrUnmounted so callers degrade to their Free-tier floor — never above.
package plan

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
)

// ErrUnmounted is returned by Entitlements when no plan catalog provider has
// been registered in this process.
var ErrUnmounted = errors.New("plan: catalog provider is not mounted")

var accountCategories = map[string]bool{
	"personal":   true,
	"team":       true,
	"enterprise": true,
}

// Tier is the money facts of one plan row: its category and whether it costs money.
type Tier struct {
	Category     string
	Price        int64
	ContactSales bool
}

// Paid reports whether t is a paid cloud account tier.
func Paid(t Tier) bool {
	if !accountCategories[strings.TrimSpace(t.Category)] {
		return false
	}
	return t.ContactSales || t.Price > 0
}

// Licence is the licensing facts of one plan row.
type Licence struct {
	Products []string
	Apps     []string
	Features []string
}

// Tokens returns l as the sorted, deduplicated flat license-feature list.
func Tokens(l Licence) []string {
	out := make([]string, 0, len(l.Features)+len(l.Apps)+len(l.Products))
	out = append(out, l.Features...)
	for _, a := range l.Apps {
		out = append(out, "licensing.app:"+a)
	}
	for _, p := range l.Products {
		out = append(out, "licensing.product:"+p)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// EntitlementsFunc resolves the entitlement map for a plan slug.
type EntitlementsFunc func(ctx context.Context, planID string) (map[string]any, error)

var entitlementsHook atomic.Value // holds EntitlementsFunc

// RegisterEntitlements installs the process-wide plan entitlements resolver.
func RegisterEntitlements(fn EntitlementsFunc) {
	if fn == nil {
		entitlementsHook.Store(EntitlementsFunc(nil))
		return
	}
	entitlementsHook.Store(fn)
}

// Entitlements resolves the entitlement map for planID through the registered
// resolver, or fails closed with ErrUnmounted when none is mounted.
func Entitlements(ctx context.Context, planID string) (map[string]any, error) {
	v := entitlementsHook.Load()
	if v == nil {
		return nil, ErrUnmounted
	}
	fn, _ := v.(EntitlementsFunc)
	if fn == nil {
		return nil, ErrUnmounted
	}
	return fn(ctx, planID)
}
