package cloud

// scope.go is the answer to the class of outage where ONE subsystem reaches the
// whole binary. An embedded subsystem called app.Use() on the shared *zip.App and
// silently gated every subsystem that mounted after it; the blast radius was a
// slice position in apps.Wire(), not anything anyone declared. The point fix was to
// stop that one subsystem. This is the structural one: a subsystem cannot install
// middleware it has not declared it may install.
//
// There are exactly TWO paths from a bare app to app-wide middleware:
//
//	app.Use(mw)                 // matches every path
//	app.Group("/x", mw)         // matches every path under /x
//
// Both go through Router, and Router is the only thing UseAll hands a subsystem
// that is not global. Everything else — leaf routes, bare Groups, the *fiber.App
// escape — passes through untouched, so absolute paths and fiber's most-specific-
// wins precedence are exactly what they were.
//
// The third idiom, `g := app.Group("/v1/x"); g.Use(mw)`, was documented here as
// needing no policing — "the group already bounds it". THAT WAS WRONG, and it is
// what crash-looped ten plugins on v1.801.425/.426. It bounds the middleware and
// says nothing about where the ROUTES went: typed ops register through ZipApp, on
// the root, so the group node is empty and zip refuses to compose it. Group now
// returns a child scope, so all three idioms are ONE install — at the root, gated
// by path — and none of them can leave an empty node behind.

import (
	"fmt"
	"strings"

	"github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// Router is the surface a subsystem mounts on: zip's routing surface EMBEDDED,
// plus two named, read-only holes onto the host — the *fiber.App the in-process
// dispatchers need (fiber.Test, GetRoutes, adaptor.FiberApp) and Plugins(), what
// this process is actually running. *zip.App satisfies it as-is, so Serve can
// hand the bare app to a Global subsystem, and tests can pass a raw app.
//
// TWO METHODS ARE DECLARED HERE, AND THAT IS THE WHOLE OF WHAT CLOUD ADDS. The
// end state worth reaching is fewer: `type Router = zip.Router` with Fiber and
// Plugins as package FUNCTIONS taking a Router — the shape ZipApp below already
// has, and the shape zip itself chose when it dropped Fiber() from its own Router
// (a decorator wraps something and has no *fiber.App of its own to return, so
// requiring one makes decoration impossible). That is what commerce's mintRouter
// runs into. It is not done here only because the two holes have 131 call sites
// in apps/, which is a mechanical sweep and a different commit.
//
// Fiber() is a deliberate, named hole: it is the concrete engine, and middleware
// installed through it is app-wide. It is promoted onto the scoped Router rather
// than granting those subsystems App — the alternative was four more of them for
// four read-only uses. Its callers are greppable and none of them registers
// middleware.
//
// Plugins() is promoted for the same reason and is strictly weaker: it registers
// nothing and mutates nothing. It exists because the ONE thing config cannot tell
// you is what a live process actually loaded — deployment manifests answer what was
// INTENDED, and during a rolling upgrade the two disagree by design. The admin
// fleet board (/v1/admin/plugins) is the reader; making it Global to ask a
// read-only question would have granted app-wide middleware to buy a status field.
type Router interface {
	// The routing surface is zip.Router — BY REFERENCE, not restated. Ten method
	// lines used to be copied here, and copying an interface makes cloud a second
	// place zip's routing surface is defined: the two agree only for as long as
	// someone keeps them agreeing, and when they disagreed the cost was not a
	// compile error here but a fleet stall. zip v1.23 widened ONE signature (Use
	// took Component instead of Handler) and every implementor that had spelled
	// the methods out — this interface, and the decorators downstream of it — had
	// to be edited in lockstep before v1.19+ could be adopted anywhere.
	//
	// Embedded, a zip routing change costs this file ZERO edits, and the two
	// things below are visibly what cloud ADDS rather than being buried among ten
	// lines cloud merely echoes.
	//
	// It carries zip.OpTarget with it, which is a gain and not a widening: every
	// implementor already had OpScope (scope below, *zip.App, and commerce's
	// mintRouter), and a Router that IS an OpTarget is one a typed registrar —
	// zip.Get[In, Out] — accepts directly.
	zip.Router

	Fiber() *fiber.App

	// Plugins reports every plugin this HOST has loaded — name, prefixes, source,
	// artifact digest, pid, running, uptime, reloads, restarts and kernel-measured
	// usage. It is this replica's answer, never the fleet's: a reader that presents
	// it as fleet-wide is lying about the other pods.
	Plugins() []zip.Status
}

// ZipApp recovers the concrete *zip.App behind a Router. It is what zip's TYPED
// registrars (zip.Get[In, Out] and friends) take, because a typed op is a route
// PLUS a registry entry — the one value the OpenAPI document, the MCP tool list
// and the CLI are all projected from — and that registry lives on the App.
//
// This is the same deliberate hole as Fiber(), one level up, and it is safe for
// the same reason: registering an op is route registration, which scope has
// never bounded (it bounds middleware). nil means the Router is neither an App
// nor a scope, which no caller should paper over — a subsystem that cannot reach
// the registry must fail its mount rather than serve routes no projection knows.
func ZipApp(r Router) *zip.App {
	switch v := r.(type) {
	case *zip.App:
		return v
	case *scope:
		return v.app
	}
	return nil
}

// scope is the Router a scoped subsystem mounts on. It holds the subsystem's
// declared prefixes and refuses to install middleware outside them.
type scope struct {
	app      *zip.App
	name     string
	prefixes []string

	// escaped records every middleware install the subsystem attempted outside its
	// prefixes. Nothing is installed for those; UseAll turns the record into a
	// boot failure, so the binary never runs half-gated.
	escaped *[]string

	// outside is set on a child Group whose prefix the subsystem does not own. The
	// group itself is legal — a prefix is just a path — so it is the Use that is
	// the escape, and recording it there is what keeps a bare group free while
	// still refusing middleware. Empty on every scope that is within its bounds.
	outside string

	// at is the group's path prefix, "" at the subsystem root. A group PREFIXES the
	// routes registered through it — that is what a group IS — so a child must
	// prepend it or `v1 := app.Group("/v1"); v1.Group("/guide").Get("/x")` silently
	// registers /x instead of /v1/guide/x. Composition would still succeed, which is
	// exactly why this cannot be left to the compose check to catch.
	at string
}

// path resolves a route path registered THROUGH this scope. At the subsystem root
// (at == "") it is the identity, so the absolute paths every subsystem already
// writes are untouched.
func (s *scope) path(p string) string {
	if s.at == "" {
		return p
	}
	if p == "" || p == "/" {
		return s.at
	}
	return strings.TrimSuffix(s.at, "/") + "/" + strings.TrimPrefix(p, "/")
}

// newScope binds a subsystem to the subtrees its middleware may gate. An empty
// Prefixes means the convention every subsystem already follows — /v1/<name>, the
// same subtree Serve's generic liveness route assumes — so only a subsystem that
// owns something else has to say so.
func newScope(app *zip.App, name string, prefixes []string) *scope {
	return &scope{app: app, name: name, prefixes: UsePrefixes(name, prefixes), escaped: new([]string)}
}

// owns reports whether path is the subsystem's own subtree or lives inside it.
// "/v1/kms" owns "/v1/kms/auth" and does not own "/v1/kmsx".
func (s *scope) owns(path string) bool {
	for _, p := range s.prefixes {
		if under(path, p) {
			return true
		}
	}
	return false
}

// Use installs the subsystem's middleware so it runs for the subsystem's own
// subtrees and nothing else.
//
// IT IS INSTALLED ONCE, AT THE ROOT, AND GATED BY PATH — not once per prefix.
// The per-prefix form (s.app.Group(p).Use(...)) is what took nine plugins down:
// Group(p) creates a NODE at p, while the subsystem's routes are registered
// through whatever router IT used — scope.Get delegates straight to s.app, so
// they land on the ROOT node. Same paths, different nodes. zip >= 1.23 checks
// the subtree OF THE NODE THE MIDDLEWARE IS ON, finds it empty, and refuses to
// compose a program whose middleware could never run:
//
//	panic: the group "/v1/avatar" declares middleware at scope.go
//	       and no routes anywhere beneath it
//
// Declaring prefixes did not fix it — it only moved the panic from /v1/account
// to /v1/avatar — because the mismatch is the NODE, not the prefix list.
//
// The root always has routes, so nothing is empty; `owns` then does what the
// per-prefix node was there to do, and does it on the request rather than on the
// tree. Confinement is unchanged and still tested: a neighbour's path fails
// `owns` and the handler is skipped.
func (s *scope) Use(cs ...zip.Component) zip.Router {
	// A Group at a prefix the subsystem does not own: the group was legal, this is
	// not. Record and install nothing — UseAll fails the boot on the record.
	if s.outside != "" {
		*s.escaped = append(*s.escaped, s.outside)
		return s.app
	}
	out := make([]zip.Component, 0, len(cs))
	for _, c := range cs {
		h, ok := c.(zip.Handler)
		if !ok {
			// A composed *App, not a wrapping handler: there is nothing to gate on
			// a per-request basis, so it keeps the subtree form it always had.
			for _, p := range s.prefixes {
				s.app.Group(p).Use(c)
			}
			continue
		}
		inner := h
		out = append(out, zip.H(func(ctx *zip.Ctx) error {
			if !s.owns(ctx.Path()) {
				return ctx.Continue()
			}
			return inner(ctx)
		}))
	}
	if len(out) == 0 {
		return s.app
	}
	return s.app.Use(out...)
}

// Group returns a Router bounded to prefix — a CHILD SCOPE, never a raw zip group.
//
// THAT IS THE WHOLE POINT, and it is what makes the three idioms ONE mechanism:
//
//	app.Use(mw)                        // gated to the subsystem's prefixes
//	app.Group("/v1/x", mw)             // gated to /v1/x
//	g := app.Group("/v1/x"); g.Use(mw) // gated to /v1/x — the same install
//
// The third form is the one that took ten plugins down on v1.801.425/.426. Handing
// back `s.app.Group(prefix)` handed back the RAW router, so `g.Use(mw)` went to zip
// directly, past every gate this file installs, and hung middleware on a node whose
// subtree is necessarily empty: a typed op registers through ZipApp (the concrete
// *zip.App, which is what the op registry lives on), so a subsystem's routes land on
// the ROOT node and never beneath the group. Same paths, different nodes. zip >= 1.23
// refuses to compose middleware that could never run, and it was right to.
//
// So a group cannot be a place; it is a BOUND. The child installs through scope.Use
// like everything else — once, at the root, gated by path — and there is no node left
// that can be empty. Confinement is unchanged: the child's bound is the prefix, which
// is narrower than the parent's.
//
// A prefix the subsystem does not own is still just a path (routes are not policed),
// so the group is returned; but middleware on it would reach where the subsystem may
// not, so the child records the attempt instead of installing, and UseAll fails the
// boot rather than running half-gated.
func (s *scope) Group(prefix string, handlers ...zip.Handler) zip.Router {
	full := s.path(prefix) // nested groups concatenate, as zip's own do
	g := &scope{app: s.app, name: s.name, prefixes: []string{full}, escaped: s.escaped, at: full}
	if !s.owns(full) {
		g.prefixes, g.outside = s.prefixes, full
	}
	for _, h := range handlers {
		g.Use(h)
	}
	return g
}

// under reports whether path is p or lives inside it. Shared by the two gates so
// "inside my subtree" has ONE meaning.
//
// Both sides fold through RoutePath, because the ROUTER matches case-insensitively
// and this comparison decides whether a subsystem's middleware runs. Fiber delivers
// /V1/EXEC to the handler registered at /v1/exec, so a raw prefix test answers "not
// my subtree" for a request the subsystem is about to serve — the route runs and the
// gate in front of it does not. Where that middleware is a credential check rather
// than a decorator, one capital letter is the whole of the bypass, which is how
// POST /V1/EXEC once ran code with no credential at all.
//
// Compare what the ROUTER matched, never what the client typed. RoutePath is the one
// normalisation; the abuse and rate-limit gates already read it, and a second spelling
// of "same path" is how two gates come to disagree about one request.
func under(path, p string) bool {
	path, p = RoutePath(path), RoutePath(p)
	return path == p || strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/")
}

func (s *scope) Get(p string, h ...zip.Handler) zip.Router     { return s.app.Get(s.path(p), h...) }
func (s *scope) Post(p string, h ...zip.Handler) zip.Router    { return s.app.Post(s.path(p), h...) }
func (s *scope) Put(p string, h ...zip.Handler) zip.Router     { return s.app.Put(s.path(p), h...) }
func (s *scope) Patch(p string, h ...zip.Handler) zip.Router   { return s.app.Patch(s.path(p), h...) }
func (s *scope) Delete(p string, h ...zip.Handler) zip.Router  { return s.app.Delete(s.path(p), h...) }
func (s *scope) Head(p string, h ...zip.Handler) zip.Router    { return s.app.Head(s.path(p), h...) }
func (s *scope) Options(p string, h ...zip.Handler) zip.Router { return s.app.Options(s.path(p), h...) }
func (s *scope) All(p string, h ...zip.Handler) zip.Router     { return s.app.All(s.path(p), h...) }
func (s *scope) Fiber() *fiber.App                             { return s.app.Fiber() }

// OpScope is where a TYPED op registers, and it is the root App's — the same answer
// ZipApp gives, because the op registry (the one value the OpenAPI document, the MCP
// tool list and the CLI are projected from) lives on the App and there is only one.
// Scope bounds middleware, never route registration, so a typed op declared through a
// child Group lands exactly where it always did.
// It carries the group's Prefix for the same reason the route methods do:
// `zip.Get(app.Group("/v1"), "/bots", o.list)` is a REAL idiom here (bots,
// entitlements), and an OpScope without the prefix would register /bots — a route
// silently MOVED, which still composes, so no compose check could have caught it.
func (s *scope) OpScope() zip.OpScope {
	o := s.app.OpScope()
	o.Prefix += s.at
	return o
}
func (s *scope) Plugins() []zip.Status { return s.app.Plugins() }

// err reports the middleware the subsystem tried to install outside its prefixes.
func (s *scope) err() error {
	if len(*s.escaped) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%s installed middleware at %s, outside the prefixes it owns (%s) — declare those prefixes in its App, or set App instead if it really gates the whole binary",
		s.name, strings.Join(*s.escaped, ", "), strings.Join(s.prefixes, ", "))
}
