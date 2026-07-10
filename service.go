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
	"fmt"

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

// newBase derives the shared deps for a named subsystem: a scoped child logger,
// the embedded KMS client, and the per-org resource meter (provider = name).
func newBase(deps Deps, name string) Base {
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
	b := newBase(deps, name)
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
