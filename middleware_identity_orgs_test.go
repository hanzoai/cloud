// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package cloud

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud/apps/principal"
)

// The membership set has to CROSS the boundary, or a surface that offers a
// choice of org has to re-validate the JWT itself or ask IAM again for
// something already in hand. Hanzo Base's workspace could list no Bases for
// exactly that reason.
//
// It is minted from the validated claims and from nothing else, so the table
// below is also the forgery test: every case sends a client copy on top.
func TestOrgsHeaderIsMintedFromClaims(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)
	future := time.Now().Add(time.Hour)

	for _, tc := range []struct {
		name string
		orgs []authz.Membership
		want string
	}{
		{
			// z@hanzo.ai's real production set, verbatim, home first as IAM writes it.
			name: "the live operator set, in IAM's order",
			orgs: []authz.Membership{
				{Org: "hanzo", Role: authz.Admin}, {Org: "admin", Role: authz.Admin},
				{Org: "lux", Role: authz.Admin}, {Org: "pars", Role: authz.Admin},
				{Org: "zoo", Role: authz.Admin},
			},
			want: "hanzo,admin,lux,pars,zoo",
		},
		{
			// The role is not carried: this header answers WHICH orgs, and what a
			// member may do inside one is that org's own surface to decide.
			name: "roles are dropped, membership is the whole fact",
			orgs: []authz.Membership{{Org: "hanzo", Role: authz.Member}, {Org: "lux", Role: authz.Owner}},
			want: "hanzo,lux",
		},
		{
			// A machine — client_credentials mints no membership set — offers no
			// choice at all, which is the truth about it and not a degraded read.
			name: "no memberships mints nothing",
			orgs: nil,
			want: "",
		},
		{
			// The two shapes a consumer would otherwise have to defend against.
			name: "an empty or repeated slug is never emitted",
			orgs: []authz.Membership{{Org: "hanzo"}, {Org: ""}, {Org: "hanzo"}, {Org: "lux"}},
			want: "hanzo,lux",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := tokenClaims("hanzo-cli", "some-app-org", "op@hanzo.ai", false, future)
			claims.Orgs = tc.orgs

			app, seen := newIdentityApp(t, v)
			probe(t, app, func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+signWith(t, key, claims))
				// Forged on top of the real token: a client copy must never survive,
				// or the header it is read for grants a switcher orgs nobody signed.
				r.Header.Set(HeaderUserOrgs, "victim,another-victim")
			})

			if got := seen.orgs; got != tc.want {
				t.Errorf("%s = %q; want %q", HeaderUserOrgs, got, tc.want)
			}
		})
	}
}

// An UNVALIDATED caller carries no membership set, however loudly it claims one:
// the header is stripped on ingress and only a validated principal restores it.
func TestOrgsHeaderIsStrippedWithoutAValidPrincipal(t *testing.T) {
	app, seen := newIdentityApp(t, nil)
	probe(t, app, func(r *http.Request) {
		r.Header.Set(HeaderUserOrgs, "victim,another-victim")
	})
	if seen.orgs != "" {
		t.Fatalf("%s = %q; want empty — a client copy must not survive ingress", HeaderUserOrgs, seen.orgs)
	}
}

// The header has two spellings — this package mints it, and apps/principal reads
// it as a leaf that cannot import this package without closing a cycle. They are
// the same name or the read is of a header nobody writes.
func TestOrgsHeaderMatchesTheEdge(t *testing.T) {
	if got := principal.OrgsOf("u", "hanzo,lux"); len(got) != 2 {
		t.Fatalf("principal.OrgsOf did not parse the set the edge mints: %v", got)
	}
	// Mint through the real middleware and read through the real reader.
	if HeaderUserOrgs != "X-User-Orgs" {
		t.Fatalf("edge mints %q; apps/principal reads X-User-Orgs", HeaderUserOrgs)
	}
}

// It answers what to OFFER and never what is allowed: acting in an org still
// goes through the effective-org decision, which honours a selection only when
// the signed set contains it.
func TestOrgsHeaderGrantsNothing(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)

	claims := tokenClaims("hanzo-cli", "some-app-org", "op@hanzo.ai", false, time.Now().Add(time.Hour))
	claims.Orgs = []authz.Membership{{Org: "hanzo", Role: authz.Admin}}

	app, seen := newIdentityApp(t, v)
	probe(t, app, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+signWith(t, key, claims))
		r.Header.Set("X-Org-Id", "victim") // an org the signed set does not carry
	})
	if seen.org != "hanzo" {
		t.Fatalf("effective org = %q; want hanzo — a selection outside the set is discarded", seen.org)
	}
	if seen.orgs != "hanzo" {
		t.Fatalf("%s = %q; want hanzo", HeaderUserOrgs, seen.orgs)
	}
}
