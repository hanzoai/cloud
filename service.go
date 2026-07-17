package cloud

// service.go — the ONE subsystem abstraction.
//
// Every /v1 subsystem used to declare its own `type svc struct { … }` holding a
// re-plumbed copy of the shared deps (log, kms, billing, brand) plus its own
// state, hang handler methods off it, and hand-write a Mount body. That is the
// same shape copied ~40 times. This collapses it to one generic value.
//
// A subsystem is Base (the shared deps, derived once) + its own typed State.
// Handlers are FREE FUNCTIONS `func(*Service[S], *zip.Ctx) error` — so a package
// declares no service/receiver type at all, only its State (plain data) and its
// handlers. `Mount` builds the value and wires routes; `Handle` binds a handler
// to a route. Generics carry the state type; functions carry the behaviour.

import (
	"errors"
	"fmt"
	"net/http"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Base is the shared dependency set every subsystem needs, derived ONCE from
// Deps at mount. It is embedded in Service, so a handler reaches Log/KMS/Bill/
// Brand directly (s.Log, s.Bill, …) with no package re-plumbing them.
type Base struct {
	Log     luxlog.Logger
	KMS     KMSClient
	Bill    *ResourceMeter
	Brand   string
	Env     string
	Domain  string
	DataDir string
}

// NewBase derives the shared deps for a named subsystem: a scoped child logger,
// the embedded KMS client, and the per-org resource meter (provider = name).
//
// Most packages never call this — cloud.Mount does. It is exported for the few
// subsystems whose Mount is more than build+routes (a background reconciler, a
// package-global for cross-package hooks, a shutdown cancel): they construct the
// value directly — `s := &cloud.Service[state]{Base: cloud.NewBase(deps, "x"),
// State: st}` — then wire routes with Handle, still on the ONE generic type.
func NewBase(deps Deps, name string) Base {
	return Base{
		Log:     deps.Logger.New("subsystem", name),
		KMS:     deps.KMS,
		Bill:    NewResourceMeter(deps, name),
		Brand:   deps.Brand,
		Env:     deps.Env,
		Domain:  deps.Domain,
		DataDir: deps.DataDir,
	}
}

// Service is THE subsystem value: the shared Base plus the subsystem's own typed
// State. One generic type across the whole binary; S is plain data (the
// package's fields). A handler is a free function `func(*Service[S], *zip.Ctx)
// error` bound with Handle — a package declares no service type, only its State.
type Service[S any] struct {
	Base
	State S
}

// Mount is the one generic subsystem entrypoint. `build` constructs the typed
// State from Base (open stores, dial clients — returns an error to fail the
// mount closed); `routes` registers the handlers. A package's exported Mount is
// then one line: `return cloud.Mount(app, deps, "name", build, routes)`.
func Mount[S any](app *zip.App, deps Deps, name string, build func(Base) (S, error), routes func(*zip.App, *Service[S])) error {
	if app == nil {
		return fmt.Errorf("%s.Mount: nil zip.App", name)
	}
	if deps.Logger == nil {
		return fmt.Errorf("%s.Mount: nil deps.Logger", name)
	}
	b := NewBase(deps, name)
	state, err := build(b)
	if err != nil {
		return fmt.Errorf("%s.Mount: %w", name, err)
	}
	routes(app, &Service[S]{Base: b, State: state})
	b.Log.Info(name + " mounted")
	return nil
}

// Handle binds a Service-scoped handler to a route: it adapts a
// `func(*Service[S], *zip.Ctx) error` to the plain `func(*zip.Ctx) error` the
// router takes, capturing s. One adapter, so packages write free-function
// handlers and register them with `app.Get("/path", cloud.Handle(s, myHandler))`.
func Handle[S any](s *Service[S], h func(*Service[S], *zip.Ctx) error) func(*zip.Ctx) error {
	return func(c *zip.Ctx) error { return h(s, c) }
}

// Terminal wraps a handler so a returned *zip.HTTPError is written in-band (its
// status + the {status,code,error} JSON zip's default errorHandler would emit)
// and nil is returned, instead of propagating the error up the middleware chain.
//
// It exists for routes mounted UNDER an outer error-flattening filter. The
// commerce embed installs one: mountCommerce (apps) registers ErrorHandlerJSON on
// an app.Group("/v1") whose middleware rewrites ANY error a downstream /v1 handler
// PROPAGATES into a hardcoded HTTP 500 — so a reject that returns zip.ErrUnauthorized
// (401) or zip.ErrBadRequest (400) up the chain surfaces to the client as 500. A
// subsystem mounted after commerce (git, sync, integrations, …) whose reject path
// must keep its real 4xx wraps its handler here: the status is written before the
// filter runs, so the filter's c.Next() sees nil and has nothing to flatten. A
// non-HTTPError (a genuine unexpected failure) passes through unchanged — those are
// 500s regardless. Compose with Handle: cloud.Terminal(cloud.Handle(s, fn)).
func Terminal(h func(*zip.Ctx) error) func(*zip.Ctx) error {
	return func(c *zip.Ctx) error {
		err := h(c)
		var he *zip.HTTPError
		if errors.As(err, &he) {
			if he.Status == 0 {
				he.Status = http.StatusInternalServerError
			}
			return c.JSON(he.Status, he)
		}
		return err
	}
}
