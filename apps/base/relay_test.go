// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// A Base routes with its own router and answers through its own middleware, so
// this package adopts that handler rather than restating it. The three tests here
// hold the shape of that adoption: it happens in ONE place, both lanes serve the
// ONE value it produced, and what reaches the client is the engine's own bytes.

// TestOnePlaceAdoptsABase reads this package's own source and requires that
// zip.AdaptNetHTTP appear exactly once, inside handler.
//
// The count is the property. A Base's routes are compiled by
// github.com/hanzoai/base/tools/router into an *http.ServeMux whose handlers are
// unexported, so no zip primitive can take them: Static wants an fs.FS, Proxy
// wants an address to dial, Use wants a *zip.App. Adopting the handler is what is
// left, and adopting it where a Base is COMPILED — rather than where one is
// served — is what makes both lanes serve one value and no request build an
// adapter. A second occurrence means some caller went back to building its own.
func TestOnePlaceAdoptsABase(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("parsed no package, so this test is watching nothing")
	}

	var at []string
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "AdaptNetHTTP" {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "zip" {
					return true
				}
				at = append(at, name+":"+enclosing(file, sel.Pos()))
				return true
			})
		}
	}
	if len(at) != 1 || !strings.HasSuffix(at[0], ":handler") {
		t.Fatalf("zip.AdaptNetHTTP at %v, want exactly one in handler — "+
			"a Base is adopted where it is compiled, so every lane serves that one value",
			at)
	}
}

// enclosing names the function a position falls inside.
func enclosing(file *ast.File, pos token.Pos) string {
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if ok && fn.Pos() <= pos && pos <= fn.End() {
			return fn.Name.Name
		}
	}
	return "(file scope)"
}

// TestBothLanesServeOneBase drives the same request at the same org through both
// lanes — the authenticated /v1/base/* wildcard, where the org comes from the
// validated principal, and pool.serve, the value the published-site lane is given,
// where it comes from the resolved Site — and requires the same bytes back off ONE
// pooled engine.
//
// The org is the only thing the two lanes disagree about. If they ever answer
// differently, or open a Base each, they are two engines over one org's rows and
// a record written through one is invisible through the other.
func TestBothLanesServeOneBase(t *testing.T) {
	authed, _ := mountApp(t)
	seedPublicNotes(t, "acme")

	// The site lane's shape: an org from somewhere other than the caller, handed
	// to the one function base.Mount gives sites.SetBaseHostHandler.
	p := mounted.pool
	site := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	site.All("/v1/base/*", func(c *zip.Ctx) error { return p.serve("acme", c) })

	const path = "/v1/base/collections/notes/records"
	if code, body := req(t, authed, http.MethodPost, path, "acme",
		map[string]any{"title": "one-base"}); code >= 300 {
		t.Fatalf("create want <300, got %d (%s)", code, body)
	}

	viaAuth, authBody := drive(t, authed, path, "acme")
	viaSite, siteBody := drive(t, site, path, "")
	if viaAuth != viaSite || !bytes.Equal(authBody, siteBody) {
		t.Fatalf("lanes disagree: authenticated %d %s, site %d %s",
			viaAuth, authBody, viaSite, siteBody)
	}
	if !bytes.Contains(authBody, []byte("one-base")) {
		t.Fatalf("neither lane read the record back: %s", authBody)
	}

	p.mu.Lock()
	open := len(p.m)
	p.mu.Unlock()
	if open != 1 {
		t.Fatalf("%d Bases open for one org, want 1 — the lanes are not sharing an engine", open)
	}
}

// TestServeRefusesAnOrgItCannotOpen pins the refusal pool.serve owns: an org with
// no Base to open is 500, said once, by the one function both lanes call.
func TestServeRefusesAnOrgItCannotOpen(t *testing.T) {
	mountApp(t)
	p := mounted.pool
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.All("/v1/base/*", func(c *zip.Ctx) error { return p.serve("", c) })

	code, body := drive(t, app, "/v1/base/collections/notes/records", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("unopenable org want 500, got %d (%s)", code, body)
	}
	if !bytes.Contains(body, []byte("base unavailable")) {
		t.Fatalf("refusal should say what failed, got %s", body)
	}
}

// engineAddresses is every address the two lanes hand to an embedded Base: the
// waitlist plugin's ten routes and a sample of the per-org engine's own.
var engineAddresses = []struct{ method, path, org string }{
	{http.MethodPost, "/v1/waitlist/join", ""},
	{http.MethodGet, "/v1/waitlist/status", ""},
	{http.MethodGet, "/v1/waitlist/neighborhood", ""},
	{http.MethodGet, "/v1/waitlist/list", ""},
	{http.MethodGet, "/v1/waitlist/activity", ""},
	{http.MethodPost, "/v1/waitlist/track-share", ""},
	{http.MethodPost, "/v1/waitlist/invite", ""},
	{http.MethodPost, "/v1/waitlist/boost", ""},
	{http.MethodPost, "/v1/waitlist/award", ""},
	{http.MethodGet, "/v1/waitlist/export", ""},
	{http.MethodGet, "/v1/waitlist/nope", ""},
	{http.MethodGet, "/v1/base/collections", "acme"},
	{http.MethodGet, "/v1/base/settings", "acme"},
	{http.MethodGet, "/v1/base/rest/notes", "acme"},
	{http.MethodGet, "/v1/base/nope", "acme"},
}

// TestTheEngineAnswers requires every relayed address to be answered by the
// embedded Base rather than by this router.
//
// The tell is X-Content-Type-Options, which base's own securityHeaders middleware
// sets and this process does not. It reaches the client through the adopted
// handler on EVERY address, including the ones the engine refuses — the waitlist
// routes that want a slug, the collections API that wants a record token, and the
// paths no route matches, which base answers with its own JSON rather than
// letting the address fall through.
//
// That is the whole reason these addresses are relayed and not rewritten: the
// refusals, the shapes and the middleware chain belong to that program. Ten of
// the fifteen are the waitlist plugin's, whose handlers it does not export at all.
func TestTheEngineAnswers(t *testing.T) {
	app, _ := mountApp(t)
	seedPublicNotes(t, "acme")

	for _, a := range engineAddresses {
		rq := httptest.NewRequest(a.method, a.path, nil)
		if a.org != "" {
			rq.Header.Set("X-Org-Id", a.org)
			rq.Header.Set("X-User-Id", "u_"+a.org)
		}
		resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("%s %s: %v", a.method, a.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s %s answered %d without the engine's security header (%q) — "+
				"this address is no longer served by the Base: %s",
				a.method, a.path, resp.StatusCode, got, body)
		}
	}
}

// drive sends one GET and returns the status and body.
func drive(t *testing.T, app *zip.App, path, org string) (int, []byte) {
	t.Helper()
	rq := httptest.NewRequest(http.MethodGet, path, nil)
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, body
}
