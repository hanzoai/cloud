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
// that is not Global. Everything else — leaf routes, bare Groups, the *fiber.App
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

// Router is the surface a subsystem mounts on: zip's routing methods, plus the
// *fiber.App escape the in-process dispatchers need (fiber.Test, GetRoutes,
// adaptor.FiberApp). *zip.App satisfies it as-is, so Serve can hand the bare app
// to a Global subsystem and tests can pass a raw app.
//
// Fiber() is a deliberate, named hole: it is the concrete engine, and middleware
// installed through it is app-wide. It is promoted onto the scoped Router rather
// than granting those subsystems Global — the alternative was four more Globals for
// four read-only uses. Its callers are greppable and none of them registers
// middleware.
type Router interface {
	Use(handlers ...zip.Handler) zip.Router

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
}

// scope is the Router a non-Global subsystem mounts on. It holds the subsystem's
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
		if path == p || strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}

// Use installs the middleware once per declared prefix. On a bare app this is the
// app-wide door; here it is the subsystem's own subtrees and nothing else.
func (s *scope) Use(handlers ...zip.Handler) zip.Router {
	var last zip.Router
	for _, p := range s.prefixes {
		last = s.app.Group(p).Use(handlers...)
	}
	return last
}

// Group passes straight through when it carries no middleware — a bare group is
// just a path prefix. With middleware it is the second app-wide door, so the
// prefix must be one the subsystem owns; if it is not, the group is created
// WITHOUT the middleware and the escape is recorded for MountAll to fail on.
func (s *scope) Group(prefix string, handlers ...zip.Handler) zip.Router {
	if len(handlers) == 0 || s.owns(prefix) {
		return s.app.Group(prefix, handlers...)
	}
	*s.escaped = append(*s.escaped, prefix)
	return s.app.Group(prefix)
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

// err reports the middleware the subsystem tried to install outside its prefixes.
func (s *scope) err() error {
	if len(*s.escaped) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%s installed middleware at %s, outside the prefixes it owns (%s) — declare those prefixes in its MountSpec, or Global: true if it really gates the whole binary",
		s.name, strings.Join(*s.escaped, ", "), strings.Join(s.prefixes, ", "))
}

// Global adapts a subsystem whose Mount still takes the concrete *zip.App into a
// MountFunc. It exists for the linked modules cloud does not own — they cannot take
// cloud.Router until their own module takes it — and it only works on a spec that
// also declares Global: true, because the bare app is the only Router that IS a
// *zip.App. Anything else fails the mount at boot rather than silently.
func Global(fn func(*zip.App, Deps) error) MountFunc {
	return func(r Router, deps Deps) error {
		app, ok := r.(*zip.App)
		if !ok {
			return fmt.Errorf("this subsystem takes the bare *zip.App; its MountSpec needs Global: true")
		}
		return fn(app, deps)
	}
}
