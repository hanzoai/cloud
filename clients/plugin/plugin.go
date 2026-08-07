// Package plugin is the runtime plugin loader for the unified cloud binary.
//
// cloud is a thin host: its native Go subsystems are compiled in, but
// everything else mounts at RUNTIME from a manifest — no cloud rebuild to add
// or update a service. Two plugin kinds, both reduced to "produce an
// http.Handler, then app.All(prefix+"/*", zip.AdaptNetHTTP(h))":
//
//   - wasm  — a polyglot service (Rust/WASM, or Python/TS via goa) loaded
//     in-process through github.com/hanzoai/goa (wazero/gpython/goja,
//     pure Go, CGO_ENABLED=0). Drop a .wasm + manifest entry → mounted.
//   - proxy — a standalone server (e.g. the beego apps ai, vm) reached over a
//     pluggable transport. The "zap" transport is registered by the ZAP
//     client when available; until then proxying uses plain HTTP. Either
//     way cloud never recompiles to point at a service.
//
// The manifest path comes from CLOUD_PLUGINS (a JSON file); if unset, plugin
// mounts nothing. Adding a service = edit the manifest + drop a .wasm or
// redeploy the standalone — cloud is unchanged unless its own core changes.
//
// Mounting happens in two parts, and only one of them is paid at boot. Every
// entry is CHECKED and its route REGISTERED while the process starts, so a
// manifest typo still kills the process immediately and the routing table is
// complete the moment cloud listens. The expensive half — compiling a .wasm
// into a pool of interpreters — waits for the first request to that prefix.
// Boot therefore costs what the manifest DECLARES, not what it weighs, and one
// unbuildable plugin can no longer take the whole binary down with it.
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/goa"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Plugin is one manifest entry. Kind selects which fields apply.
type Plugin struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`   // "wasm" | "proxy" | "native"
	Prefix string `json:"prefix"` // mount point, e.g. /v1/pricing

	// kind=wasm (goa): a polyglot module + its route table.
	Lang   string            `json:"lang,omitempty"`   // "rust"/"wasm", "python", "javascript"…
	Source string            `json:"source,omitempty"` // path to .wasm/.py/.ts, relative to the manifest
	Pool   int               `json:"pool,omitempty"`   // pooled interpreters (default 8)
	Routes []goa.Route       `json:"routes,omitempty"`
	Env    map[string]string `json:"env,omitempty"`

	// kind=proxy: a standalone server reached over a transport.
	Target      string `json:"target,omitempty"`      // e.g. http://ai.internal:8080
	Via         string `json:"via,omitempty"`         // transport: "http" (default) | "zap"
	StripPrefix bool   `json:"stripPrefix,omitempty"` // strip Prefix before forwarding
}

// Manifest is the runtime plugin set.
type Manifest struct {
	Plugins []Plugin `json:"plugins"`
}

// --- transport seam ------------------------------------------------------
//
// The proxy kind dials its target through an http.RoundTripper chosen by
// Plugin.Via. "http" is built in; "zap" (and any future transport) is
// registered here by its client package, so plugin has no hard dependency
// on the ZAP wire code and works today over HTTP.

var (
	transportsMu sync.RWMutex
	transports   = map[string]http.RoundTripper{}
)

// RegisterTransport makes rt selectable as Plugin.Via == name. Called from the
// transport client's init() (e.g. the ZAP client registers "zap").
func RegisterTransport(name string, rt http.RoundTripper) {
	transportsMu.Lock()
	defer transportsMu.Unlock()
	transports[name] = rt
}

func transportFor(via string) (http.RoundTripper, error) {
	if via == "" || via == "http" {
		return http.DefaultTransport, nil
	}
	transportsMu.RLock()
	defer transportsMu.RUnlock()
	if rt, ok := transports[via]; ok {
		return rt, nil
	}
	return nil, fmt.Errorf("transport %q not registered (its client package must call plugin.RegisterTransport)", via)
}

// --- lazy loading --------------------------------------------------------

// loader is one manifest entry plus whatever the entry turned into. Mount makes
// one per plugin and registers its route; the first request through that route
// builds that plugin, and only that plugin.
type loader struct {
	p       Plugin
	baseDir string
	log     luxlog.Logger

	// mu guards h and svc. It is per plugin and IS held across that plugin's
	// build, which is what makes concurrent first requests produce exactly one
	// build that every one of them is then served by. Per plugin, so a slow
	// build stalls its own prefix and no other. Serving happens after the
	// unlock: holding it there would serialize the plugin's whole traffic
	// behind one request.
	mu  sync.Mutex
	h   zip.Handler  // non-nil once built
	svc *goa.Service // the interpreter pool to release, when the built plugin has one
}

// handler returns the plugin's handler, building it on the first call.
//
// A failed build is NOT remembered: the next request tries again. The manifest
// was already checked at boot, so what can still fail here is operational — a
// .wasm not copied in yet, a transport client that registers late — and those
// heal without restarting cloud, which is the whole reason services load from a
// manifest. Caching the failure would only make "come back later" faster while
// turning it into a lie. The cost is that a permanently broken plugin pays one
// build attempt per request; it answers 503 either way.
func (l *loader) handler() (zip.Handler, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.h != nil {
		return l.h, nil
	}
	// The build outlives the request that triggered it — every later request
	// shares the pool — so it runs on a background context. One client hanging
	// up must not abort what it started for everyone else.
	h, svc, err := build(context.Background(), l.p, l.baseDir)
	if err != nil {
		return nil, err
	}
	l.h, l.svc = zip.AdaptNetHTTP(h), svc
	l.log.Info("plugin loaded", "name", l.p.Name, "kind", l.p.Kind, "prefix", l.p.Prefix)
	return l.h, nil
}

// serve is the route registered at mount. A plugin that will not build answers
// 503 with the plugin's name and the build's own error, because that is the
// truth: the route exists, the thing behind it is not there yet.
func (l *loader) serve(c *zip.Ctx) error {
	h, err := l.handler()
	if err != nil {
		l.log.Warn("plugin build failed", "name", l.p.Name, "prefix", l.p.Prefix, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "plugin %q failed to load: %v", l.p.Name, err)
	}
	return h(c)
}

// loaded reports whether the plugin has been built yet. It is what /v1/plugins
// publishes: the one observable that says whether a prefix has paid its cost.
func (l *loader) loaded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.h != nil
}

// release closes what this plugin actually built. A plugin no request ever
// reached built nothing and so has nothing to close.
func (l *loader) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.svc != nil && l.svc.Pool != nil {
		_ = l.svc.Pool.Close()
	}
	l.h, l.svc = nil, nil
}

// --- mounting ------------------------------------------------------------

// mounted is every plugin Mount registered, in manifest order: what Shutdown
// walks to release the ones that ended up being built.
var (
	mu      sync.Mutex
	mounted []*loader
)

// Mount reads the plugin manifest, checks every entry and registers a route for
// each one. Missing or unset manifest is a no-op (cloud runs fine with zero
// plugins). Nothing is BUILT here — see loader.handler.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("plugin.Mount: nil zip.App")
	}
	log := deps.Logger.New("subsystem", "plugins")

	path := os.Getenv("CLOUD_PLUGINS")
	if path == "" {
		log.Debug("no plugin manifest (CLOUD_PLUGINS unset); mounting none")
		return nil
	}
	man, err := load(path)
	if err != nil {
		return fmt.Errorf("plugin.Mount: %w", err)
	}
	baseDir := filepath.Dir(path)

	// Check the whole manifest before registering any of it: a typo in the last
	// entry must not leave the earlier ones half-mounted.
	ls := make([]*loader, 0, len(man.Plugins))
	for _, p := range man.Plugins {
		if err := check(p); err != nil {
			return fmt.Errorf("plugin.Mount: plugin %q: %w", p.Name, err)
		}
		ls = append(ls, &loader{p: p, baseDir: baseDir, log: log})
	}
	for _, l := range ls {
		app.All(l.p.Prefix+"/*", l.serve)
		log.Info("plugin mounted", "name", l.p.Name, "kind", l.p.Kind, "prefix", l.p.Prefix)
	}
	mu.Lock()
	mounted = append(mounted, ls...)
	mu.Unlock()

	// Introspection: what is mounted, and which of it has actually loaded.
	app.Get("/v1/plugins", func(c *zip.Ctx) error {
		out := make([]view, 0, len(ls))
		for _, l := range ls {
			out = append(out, view{Name: l.p.Name, Kind: l.p.Kind, Prefix: l.p.Prefix, Loaded: l.loaded()})
		}
		return c.JSON(http.StatusOK, map[string]any{"plugins": out})
	})

	log.Info("plugins mounted", "count", len(ls), "brand", deps.Brand)
	return nil
}

// view is one plugin as /v1/plugins reports it. Loaded is the observable for
// lazy loading: false means the prefix is routed but nothing has been built
// behind it, which is the ordinary state of a plugin nobody has called.
type view struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Prefix string `json:"prefix"`
	Loaded bool   `json:"loaded"`
}

func load(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	return &m, nil
}

// check reads one manifest entry without building it. Everything here is a fact
// that is wrong whenever it is read, so it kills the process at boot instead of
// ambushing the first request to arrive. What laziness defers is the work,
// never the checking.
//
// Whether Via names a registered transport is deliberately NOT checked: a
// transport client can register after Mount, so its absence is a condition of
// the moment rather than a typo, and build says so at 503.
func check(p Plugin) error {
	if !strings.HasPrefix(p.Prefix, "/") {
		return fmt.Errorf("prefix must be an absolute path, got %q", p.Prefix)
	}
	switch p.Kind {
	case "wasm", "goa", "":
		if p.Source == "" {
			return fmt.Errorf("wasm plugin needs a source")
		}
	case "proxy":
		if p.Target == "" {
			return fmt.Errorf("proxy plugin needs a target")
		}
		if _, err := url.Parse(p.Target); err != nil {
			return fmt.Errorf("bad target %q: %w", p.Target, err)
		}
	default:
		return badKind(p.Kind)
	}
	return nil
}

func badKind(kind string) error {
	return fmt.Errorf("unknown kind %q (want wasm|proxy)", kind)
}

// build turns a plugin into a mountable http.Handler, and returns the goa
// service behind it when the kind has one, so its pool can be released later.
func build(ctx context.Context, p Plugin, baseDir string) (http.Handler, *goa.Service, error) {
	switch p.Kind {
	case "wasm", "goa", "": // polyglot via goa (default)
		man := goa.Manifest{
			Name: p.Name, Lang: p.Lang, Source: p.Source,
			Pool: p.Pool, Prefix: p.Prefix, Routes: p.Routes, Env: p.Env,
		}
		svc, err := man.Build(ctx, os.DirFS(baseDir))
		if err != nil {
			return nil, nil, err
		}
		return svc.Handler(), svc, nil
	case "proxy":
		h, err := buildProxy(p)
		return h, nil, err
	default:
		return nil, nil, badKind(p.Kind)
	}
}

func buildProxy(p Plugin) (http.Handler, error) {
	if p.Target == "" {
		return nil, fmt.Errorf("proxy plugin needs a target")
	}
	u, err := url.Parse(p.Target)
	if err != nil {
		return nil, fmt.Errorf("bad target %q: %w", p.Target, err)
	}
	rt, err := transportFor(p.Via)
	if err != nil {
		return nil, err
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.Transport = rt
	var h http.Handler = rp
	if p.StripPrefix {
		h = http.StripPrefix(p.Prefix, rp)
	}
	return h, nil
}

// Shutdown releases every plugin that was actually built. Idempotent.
func Shutdown(context.Context) error {
	mu.Lock()
	defer mu.Unlock()
	for _, l := range mounted {
		l.release()
	}
	mounted = nil
	return nil
}
