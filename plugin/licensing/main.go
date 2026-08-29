package main

import (
	"context"
	"fmt"
	licsvc "github.com/hanzoai/licensing/pkg/licensing"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/licensing"
)

// Standalone entry for the licensing app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `licensing openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "licensing",
		Price: cloud.Free,
		// licensing BUILDS ITS OWN app now, so this composes rather than mounts.
		// It used to be handed cloud's router and cloud's Deps, which meant the
		// leaf imported its host — and a leaf that imports its host can be composed
		// by exactly one host, because both editions of cloud declare the same
		// module path and therefore two different cloud.Deps types.
		//
		// Entitlement is the one fact it needs and cannot know: whether this org
		// has paid for this product. It declares the question (Check) and the host
		// answers it from commerce. The two shapes are field-identical, so the
		// adapter is a copy and not a translation — but it is a copy across a
		// BOUNDARY, which is what lets licensing be built, tested and released
		// without cloud in its graph at all.
		Use: func(app cloud.Router, deps cloud.Deps) error {
			sub, err := licensing.App(deps.Brand, deps.DataDir, entitlements{deps.Commerce})
			if err != nil {
				return err
			}
			zapp := cloud.ZipApp(app)
			if zapp == nil {
				return fmt.Errorf("licensing: the router is not a zip app, so the app cannot be composed")
			}
			zapp.Use(sub)
			return nil
		},
		Global: true,
	}}, []string{"licensing"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// entitlements answers licensing's one question from commerce, the subsystem that
// holds the ledger. A nil commerce is NOT an allow-all: it answers
// ErrNoEntitlement, so a deployment with no money plane refuses to mint rather
// than minting for everyone.
type entitlements struct{ c cloud.CommerceClient }

func (e entitlements) Check(ctx context.Context, tenant, product string) (*licsvc.Entitlement, error) {
	if e.c == nil {
		return nil, licsvc.ErrNoEntitlement
	}
	got, err := e.c.CheckEntitlement(ctx, tenant, product)
	if err != nil {
		return nil, err
	}
	if got == nil {
		return nil, licsvc.ErrNoEntitlement
	}
	return &licsvc.Entitlement{
		Active:      got.Active,
		Plan:        got.Plan,
		Features:    got.Features,
		ExpiresUnix: got.ExpiresUnix,
	}, nil
}
