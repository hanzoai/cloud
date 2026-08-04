package cloud

// scope.go is the answer to the class of outage where ONE subsystem reaches the
// whole binary. An embedded subsystem called app.Use() on the shared *zip.App and
// silently gated every subsystem that mounted after it; the blast radius was a
// slice position in apps.Wire(), not anything anyone declared. The point fix was to
// stop that one subsystem. This is the structural one: a subsystem cannot install
// middleware it has not declared it may install.
//
// There are exactly TWO doors from a bare app to app-wide middleware:
//
//	app.Use(mw)                 // matches every path
//	app.Group("/x", mw)         // matches every path under /x
//
// Both go through Router, and Router is the only thing MountAll hands a subsystem
// that is not global. Everything else — leaf routes, bare Groups, the *fiber.App
// escape — passes through untouched, so absolute paths and fiber's most-specific-
// wins precedence are exactly what they were.
//
// The third idiom, `g := app.Group("/v1/x"); g.Use(mw)`, needs no policing: the
// group already bounds it. That is the idiom this file makes the only one.

import (
	"fmt"
	"strings"

	"github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// Router is the surface a subsystem mounts on: zip's routing methods, plus two
// named, read-only holes onto the host — the *fiber.App the in-process
// dispatchers need (fiber.Test, GetRoutes, adaptor.FiberApp) and Plugins(), what
// this process is actually running. *zip.App satisfies it as-is, so Serve can
// hand the bare app to a subsystem that supplies App, and tests can pass a raw app.
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
	// Use is zip's ONE composition verb, so it takes a [zip.Component] —
	// middleware, or another *zip.App included by reference. It is the only
	// signature that widened in zip v1.23; every route method below still takes
	// ...Handler. Mirroring zip.Router exactly is what lets a *zip.App satisfy
	// this interface, which ZipApp's type switch depends on.
	Use(cs ...zip.Component) zip.Router

	Get(path string, handlers ...zip.Handler) zip.Router
	Post(path string, handlers ...zip.Handler) zip.Router
	Put(path string, handlers ...zip.Handler) zip.Router
	Patch(path string, handlers ...zip.Handler) zip.Router
	Delete(path string, handlers ...zip.Handler) zip.Router
	Head(path string, handlers ...zip.Handler) zip.Router
	Options(path string, handlers ...zip.Handler) zip.Router
	All(path string, handlers ...zip.Handler) zip.Router

	Group(prefix string, handlers ...zip.Handler) zip.Router

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
	// prefixes. Nothing is installed for those; MountAll turns the record into a
	// boot failure, so the binary never runs half-gated.
	escaped *[]string
}

// newScope binds a subsystem to the subtrees its middleware may gate. An empty
// Prefixes means the convention every subsystem already follows — /v1/<name>, the
// same subtree Serve's generic liveness route assumes — so only a subsystem that
// owns something else has to say so.
func newScope(app *zip.App, name string, prefixes []string) *scope {
	return &scope{app: app, name: name, prefixes: MountPrefixes(name, prefixes), escaped: new([]string)}
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

// Group passes straight through when it carries no middleware — a bare group is
// just a path prefix. With middleware it is the second app-wide door, so the
// prefix must be one the subsystem owns; if it is not, the group is created
// WITHOUT the middleware and the escape is recorded for MountAll to fail on.
func (s *scope) Group(prefix string, handlers ...zip.Handler) zip.Router {
	if len(handlers) == 0 {
		return s.app.Group(prefix) // a bare group is just a path prefix
	}
	if !s.owns(prefix) {
		*s.escaped = append(*s.escaped, prefix)
		return s.app.Group(prefix)
	}
	// Gated at the root by path, for the same reason Use is: a group node
	// carrying middleware is empty whenever the routes beneath it were
	// registered through a different router — which is exactly what scope.Get
	// does — and zip refuses to compose that. The gate is the prefix itself, so
	// the middleware still runs for this subtree and nothing else.
	wrapped := make([]zip.Component, 0, len(handlers))
	for _, h := range handlers {
		inner := h
		wrapped = append(wrapped, zip.H(func(ctx *zip.Ctx) error {
			if !under(ctx.Path(), prefix) {
				return ctx.Continue()
			}
			return inner(ctx)
		}))
	}
	s.app.Use(wrapped...)
	return s.app.Group(prefix)
}

// under reports whether path is p or lives inside it. Shared by the two gates so
// "inside my subtree" has ONE meaning.
func under(path, p string) bool {
	return path == p || strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/")
}

func (s *scope) Get(p string, h ...zip.Handler) zip.Router     { return s.app.Get(p, h...) }
func (s *scope) Post(p string, h ...zip.Handler) zip.Router    { return s.app.Post(p, h...) }
func (s *scope) Put(p string, h ...zip.Handler) zip.Router     { return s.app.Put(p, h...) }
func (s *scope) Patch(p string, h ...zip.Handler) zip.Router   { return s.app.Patch(p, h...) }
func (s *scope) Delete(p string, h ...zip.Handler) zip.Router  { return s.app.Delete(p, h...) }
func (s *scope) Head(p string, h ...zip.Handler) zip.Router    { return s.app.Head(p, h...) }
func (s *scope) Options(p string, h ...zip.Handler) zip.Router { return s.app.Options(p, h...) }
func (s *scope) All(p string, h ...zip.Handler) zip.Router     { return s.app.All(p, h...) }
func (s *scope) Fiber() *fiber.App                             { return s.app.Fiber() }
func (s *scope) Plugins() []zip.Status                   { return s.app.Plugins() }

// err reports the middleware the subsystem tried to install outside its prefixes.
func (s *scope) err() error {
	if len(*s.escaped) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%s installed middleware at %s, outside the prefixes it owns (%s) — declare those prefixes in its App, or set App instead if it really gates the whole binary",
		s.name, strings.Join(*s.escaped, ", "), strings.Join(s.prefixes, ", "))
}
