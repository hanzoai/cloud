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
	"sync"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/goa"
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

// --- mounting ------------------------------------------------------------

// mounted tracks goa services so Shutdown can release their pools.
var (
	mu      sync.Mutex
	mounted []*goa.Service
)

// Mount reads the plugin manifest and mounts every plugin onto app. Missing or
// unset manifest is a no-op (cloud runs fine with zero plugins).
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("plugin.Mount: nil app")
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

	for _, p := range man.Plugins {
		h, err := build(context.Background(), p, baseDir)
		if err != nil {
			return fmt.Errorf("plugin.Mount: plugin %q: %w", p.Name, err)
		}
		app.All(p.Prefix+"/*", zip.AdaptNetHTTP(h))
		log.Info("plugin mounted", "name", p.Name, "kind", p.Kind, "prefix", p.Prefix)
	}

	// Introspection: list what is mounted.
	plugins := man.Plugins
	app.Get("/v1/plugins", func(c *zip.Ctx) error {
		out := make([]map[string]string, 0, len(plugins))
		for _, p := range plugins {
			out = append(out, map[string]string{"name": p.Name, "kind": p.Kind, "prefix": p.Prefix})
		}
		return c.JSON(http.StatusOK, map[string]any{"plugins": out})
	})

	log.Info("plugins loaded", "count", len(man.Plugins), "brand", deps.Brand)
	return nil
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

// build turns a plugin into a mountable http.Handler.
func build(ctx context.Context, p Plugin, baseDir string) (http.Handler, error) {
	switch p.Kind {
	case "wasm", "goa", "": // polyglot via goa (default)
		man := goa.Manifest{
			Name: p.Name, Lang: p.Lang, Source: p.Source,
			Pool: p.Pool, Prefix: p.Prefix, Routes: p.Routes, Env: p.Env,
		}
		svc, err := man.Build(ctx, os.DirFS(baseDir))
		if err != nil {
			return nil, err
		}
		mu.Lock()
		mounted = append(mounted, svc)
		mu.Unlock()
		return svc.Handler(), nil
	case "proxy":
		return buildProxy(p)
	default:
		return nil, fmt.Errorf("unknown kind %q (want wasm|proxy)", p.Kind)
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

// Shutdown releases every mounted goa service pool. Idempotent.
func Shutdown(context.Context) error {
	mu.Lock()
	defer mu.Unlock()
	for _, s := range mounted {
		if s != nil && s.Pool != nil {
			_ = s.Pool.Close()
		}
	}
	mounted = nil
	return nil
}
