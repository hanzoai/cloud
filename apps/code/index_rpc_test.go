// Copyright © 2026 Hanzo AI. MIT License.

package code

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/plane"
)

// THE OP MUST EXIST, because the thing it replaced did not.
//
// apps/git carried an `Indexer` function seam for a composition root to fill with
// this package's client. The fleet runs one binary per app — plugin/git links git,
// plugin/code links code, nothing links both — so no process could fill it and
// push-indexing was inert everywhere it shipped, for as long as it shipped, with
// no test able to notice because a nil seam is a legal state.
//
// An op cannot fail that way: it is either published or it is not, and this asks.
func TestTheIndexOpIsPublished(t *testing.T) {
	if plane.CodeIndex == "" {
		t.Fatal("plane.CodeIndex has no operation id")
	}
}

// Identity is required. Without an org there is no index to write and no tenant
// to bill, and indexing EMBEDS — silently accepting a blank org would run paid
// inference against nobody.
func TestIndexRefusesAnUnidentifiedTree(t *testing.T) {
	for _, in := range []plane.IndexIn{
		{Repo: "r"},
		{Org: "acme"},
		{},
	} {
		if _, err := planeIndex(context.Background(), &in); err == nil {
			t.Errorf("planeIndex(%+v) accepted a tree with no identity", in)
		}
	}
}

// A DEPLOYMENT THAT HOSTS NO CODE INDEX IS NOT A FAULT IN THE PUSH THAT LANDED.
// The caller is a push reactor doing background enrichment, so an unmounted
// service answers an empty reconcile rather than an error the reactor would log
// on every push forever.
func TestIndexIsQuietWhenUnmounted(t *testing.T) {
	saved := mounted
	t.Cleanup(func() { mounted = saved })
	mounted = nil

	out, err := planeIndex(context.Background(), &plane.IndexIn{
		Org: "acme", BillingOrg: "acme", Repo: "r",
		Files: []plane.IndexFile{{Path: "a.go", Content: "package a"}},
	})
	if err != nil {
		t.Fatalf("an unmounted code plane returned an error: %v — a push must not fail "+
			"because this deployment hosts no index", err)
	}
	if out == nil || out.Files != 0 {
		t.Errorf("unmounted reconcile reported %+v, want an empty one", out)
	}
}
