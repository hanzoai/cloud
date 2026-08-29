// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// reqOrgs is req with a membership set, which is what the listing reads. The
// harness mounts a bare app with no SanitizeIdentity, so these headers stand in
// for what the boundary mints — the same shortcut every other test here takes
// for X-Org-Id.
func reqOrgs(t *testing.T, app *zip.App, path, user, orgs string) (int, []byte) {
	t.Helper()
	rq := httptest.NewRequest(http.MethodGet, path, nil)
	if user != "" {
		rq.Header.Set("X-User-Id", user)
	}
	if orgs != "" {
		rq.Header.Set("X-User-Orgs", orgs)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Test GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		b = append(b, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, b
}

// The defect this whole file exists for: the listing has to WIN the address
// against /v1/base/*, or it reaches one org's engine, which has no route for it
// and answers not-found — which the space showed as an account with no Bases.
func TestListingBeatsTheWildcard(t *testing.T) {
	app, _ := mountApp(t)
	code, body := reqOrgs(t, app, "/v1/base/bases", "u_hanzo", "hanzo,lux")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/base/bases = %d %s, want 200 — the wildcard swallowed it", code, body)
	}
	var got baseList
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(got) != 2 || got[0].Org != "hanzo" || got[1].Org != "lux" {
		t.Fatalf("got %+v, want one entry per org on the token, in the token's order", got)
	}
}

// The wire is a BARE ARRAY. An envelope is the shape a typed op reaches for by
// reflex and would break the space, which decodes Base[].
func TestListingIsABareArray(t *testing.T) {
	app, _ := mountApp(t)
	_, body := reqOrgs(t, app, "/v1/base/bases", "u_hanzo", "hanzo")
	if len(body) == 0 || body[0] != '[' {
		t.Fatalf("body starts %q, want a bare JSON array", string(body[:min(20, len(body))]))
	}
}

// Membership is the whole of what a caller may see, and it comes from the token
// rather than from the request. An org the token does not carry is NOT FOUND —
// not forbidden, so the address cannot be used to learn which orgs exist.
func TestReadRefusesAnOrgTheTokenDoesNotCarry(t *testing.T) {
	app, _ := mountApp(t)
	if code, _ := reqOrgs(t, app, "/v1/base/bases/hanzo", "u_hanzo", "hanzo"); code != http.StatusOK {
		t.Fatalf("own org = %d, want 200", code)
	}
	code, body := reqOrgs(t, app, "/v1/base/bases/victim", "u_hanzo", "hanzo")
	if code != http.StatusNotFound {
		t.Fatalf("foreign org = %d %s, want 404", code, body)
	}
}

// It fails closed on the same fact every other read here does: no validated
// user means the headers that rode along are untrusted, so there is no
// membership set and nothing to list.
func TestListingFailsClosedWithoutAValidatedPrincipal(t *testing.T) {
	app, _ := mountApp(t)
	code, body := reqOrgs(t, app, "/v1/base/bases", "", "hanzo,lux")
	if code != http.StatusOK {
		t.Fatalf("= %d %s, want 200 with an empty list", code, body)
	}
	var got baseList
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want none — an unvalidated caller has no membership set", got)
	}
}

// `exists` is the one fact a Base adds to an org name, so it has to track the
// store rather than the org: false before anything is stored, true after.
func TestExistsTracksTheStore(t *testing.T) {
	app, _ := mountApp(t)
	_, body := reqOrgs(t, app, "/v1/base/bases/fresh", "u_x", "fresh")
	var before baseView
	if err := json.Unmarshal(body, &before); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if before.Exists {
		t.Fatalf("a Base nobody has touched reports exists=true (%+v)", before)
	}
	if _, err := mounted.pool.appFor("fresh"); err != nil {
		t.Fatalf("appFor: %v", err)
	}
	_, body = reqOrgs(t, app, "/v1/base/bases/fresh", "u_x", "fresh")
	var after baseView
	if err := json.Unmarshal(body, &after); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if !after.Exists || after.Bytes <= 0 {
		t.Fatalf("after provisioning: %+v, want exists=true with a size", after)
	}
}

// A deployment with no Base engine hosts no Bases, so it must say none rather
// than reading a directory off a subsystem that was never mounted.
func TestDescribeWithoutAPool(t *testing.T) {
	got := baseOps{}.describe("hanzo")
	if got.Org != "hanzo" || got.Exists || got.Bytes != 0 {
		t.Fatalf("describe with no pool = %+v; want the org named and nothing claimed", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
