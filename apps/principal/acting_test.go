// Copyright © 2026 Hanzo AI. MIT License.

package principal

import (
	"context"
	"testing"

	"github.com/zap-proto/zip"
)

// AN ORG REACHES AN OP TWO WAYS AND Acting IS THE ONE THING THAT TELLS THEM APART.
//
// zip.CallerOf returns a plain string either way: the request's X-Org-Id when a
// request is bound, the stated caller when none is. The identity boundary
// deliberately restores an unvalidated caller's own X-Org-Id for the data path,
// so on the way IN that string is the client's own until something stands behind
// it. Coming from INSIDE — cloud.For, or a peer on the plane's own socket —
// nothing outside could have written it.
//
// WithEdge is what separates them, and cloud.Bridge parks it on every request.
// These are the three shapes, and the middle one is the whole point.
func TestActingSeparatesTheTwoWaysAnOrgArrives(t *testing.T) {
	stated := func(org string) context.Context {
		return zip.WithCaller(context.Background(), zip.Caller{Org: org})
	}
	for _, tc := range []struct {
		what     string
		ctx      context.Context
		resolved bool
	}{
		// From inside: a background job or a peer states the tenant it acts for.
		// There is no user, and there does not need to be — nothing outside this
		// process can put a value in that slot.
		{"a caller stated off the edge", stated("acme"), true},
		// The SAME value, arriving through the edge. Now it is a header, and a
		// header with no validated principal behind it is nobody's claim but the
		// client's own.
		{"the same org, stated through the edge", WithEdge(stated("acme")), false},
		// Nothing stated at all, either side.
		{"nothing stated, off the edge", context.Background(), false},
		{"nothing stated, through the edge", WithEdge(context.Background()), false},
		// A validated principal wins on both sides — it is the first thing asked
		// and the edge does not weaken it.
		{"a validated org, through the edge", WithEdge(context.WithValue(context.Background(), orgKey{}, "acme")), true},
	} {
		org, err := Acting(tc.ctx)
		if (err == nil) != tc.resolved {
			t.Errorf("%s: Acting = %q, %v — want resolved=%v", tc.what, org, err, tc.resolved)
		}
		if err == nil && org != "acme" {
			t.Errorf("%s: Acting resolved %q, want acme", tc.what, org)
		}
	}
}

// A whitespace-bearing org grants NO scope rather than being trimmed onto a
// neighbour's, and that has to hold on the stated path too — it is the same
// injective boundary OrgOf keeps, and the stated path is the one that skips it.
func TestActingWillNotFoldOneOrgOntoAnother(t *testing.T) {
	for _, org := range []string{" acme", "acme ", "", "   "} {
		got, err := Acting(zip.WithCaller(context.Background(), zip.Caller{Org: org}))
		if err == nil && got != "acme" {
			t.Errorf("a stated org %q resolved to %q", org, got)
		}
		if err == nil && got == "acme" && org != "acme" {
			t.Errorf("a stated org %q was folded onto %q", org, got)
		}
	}
}
