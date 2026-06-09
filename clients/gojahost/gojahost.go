// Package gojahost runs a Hanzo Node service's goja bundle (a self-contained,
// ESM-free JS file exposing globalThis.handle(req)) inside the unified cloud
// binary, per HIP-0106.
//
// It is the SHARED glue used by clients/plansvc and clients/pricingsvc to host
// @hanzo/plans and @hanzo/pricing in-process via dop251/goja — the same engine
// base/plugins/gojavm uses. We do not import base's gojavm Runtime directly
// because that loader is manifest-driven (extension.json + a single exported
// `fn` over JSON-over-the-wire payloads); our services instead inject a catalog
// of JSON globals at VM init and call a richer handle({route,params,...}) entry.
// The VM-pool + compile-once + per-runtime ensureLoaded discipline here mirrors
// gojavm/runtime.go exactly so behavior and the pool semantics are identical.
//
// Module boundary: the JS bundle + catalog data live in the service repos
// (hanzoai/plans, hanzoai/pricing) and are passed in by the caller. This
// package carries zero service logic — only the engine plumbing.
package gojahost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/dop251/goja"
)

// defaultPoolSize mirrors gojavm's default. Override via CLOUD_GOJAHOST_POOL_SIZE.
const defaultPoolSize = 8

// Request is the dispatch envelope handed to globalThis.handle in JS.
type Request struct {
	Route  string            `json:"route"`
	Params map[string]string `json:"params,omitempty"`
	Query  map[string]string `json:"query,omitempty"`
	Tenant string            `json:"tenant,omitempty"`
}

// Response is what globalThis.handle returns: an HTTP status + an opaque body
// that the host serializes straight to JSON.
type Response struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// Host is a compiled service bundle plus a pool of goja runtimes that have had
// the bundle + the injected globals evaluated. Safe for concurrent use.
type Host struct {
	name    string
	program *goja.Program
	globals map[string]any
	pool    []*slot
	factory func() *goja.Runtime

	mu     sync.Mutex
	closed bool
}

type slot struct {
	mu      sync.Mutex
	busy    bool
	vm      *goja.Runtime
	loaded  bool
	hasFunc bool
}

// Config configures a Host.
type Config struct {
	// Name identifies the service for error messages ("plans", "pricing").
	Name string
	// Bundle is the goja bundle source (goja/bundle.js from the service repo).
	Bundle []byte
	// Globals are injected onto each runtime before the bundle runs, e.g.
	// {"__PLANS_DATA__": <catalog>}. Values are converted via goja.ToValue.
	// Pointers to the same Go value are shared read-only across runtimes; the
	// bundles never mutate injected globals.
	Globals map[string]any
}

// New compiles the bundle and pre-warms the runtime pool. The bundle is
// compiled once (goja.Program is safe to share across runtimes); each pool
// runtime evaluates it lazily on first use.
func New(cfg Config) (*Host, error) {
	if cfg.Name == "" {
		return nil, errors.New("gojahost: Config.Name required")
	}
	if len(cfg.Bundle) == 0 {
		return nil, fmt.Errorf("gojahost[%s]: empty bundle", cfg.Name)
	}
	prog, err := goja.Compile(cfg.Name+"/bundle.js", string(cfg.Bundle), true)
	if err != nil {
		return nil, fmt.Errorf("gojahost[%s]: compile: %w", cfg.Name, err)
	}

	size := defaultPoolSize
	if v := os.Getenv("CLOUD_GOJAHOST_POOL_SIZE"); v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			size = n
		}
	}
	if size < 1 {
		size = 1
	}

	h := &Host{
		name:    cfg.Name,
		program: prog,
		globals: cfg.Globals,
		factory: func() *goja.Runtime { return goja.New() },
		pool:    make([]*slot, size),
	}
	for i := range h.pool {
		h.pool[i] = &slot{vm: h.factory()}
	}

	// Eagerly load + validate one runtime so misconfiguration (bad bundle,
	// missing handle export) fails at Mount, not at first request.
	if err := h.withSlot(func(s *slot) error {
		if err := h.ensure(s); err != nil {
			return err
		}
		if !s.hasFunc {
			return fmt.Errorf("gojahost[%s]: bundle does not define globalThis.handle", cfg.Name)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return h, nil
}

// ensure evaluates the bundle on a runtime exactly once (installing the
// injected globals first), then records whether globalThis.handle exists.
func (h *Host) ensure(s *slot) error {
	if s.loaded {
		return nil
	}
	for k, v := range h.globals {
		if err := s.vm.Set(k, v); err != nil {
			return fmt.Errorf("gojahost[%s]: set global %s: %w", h.name, k, err)
		}
	}
	// Minimal node-ish shims the bundles may touch. The bundles are written
	// to avoid console, but defensively wire a no-op console so a stray
	// console.* never throws ReferenceError.
	installConsole(s.vm)

	if _, err := s.vm.RunProgram(h.program); err != nil {
		return fmt.Errorf("gojahost[%s]: run bundle: %w", h.name, err)
	}
	_, s.hasFunc = goja.AssertFunction(s.vm.Get("handle"))
	s.loaded = true
	return nil
}

// Dispatch calls globalThis.handle(req) on a pooled runtime and returns the
// JS-side {status, body}. ctx cancellation interrupts the call.
func (h *Host) Dispatch(ctx context.Context, req Request) (*Response, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, fmt.Errorf("gojahost[%s]: closed", h.name)
	}
	h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var resp *Response
	err := h.withSlot(func(s *slot) error {
		if err := h.ensure(s); err != nil {
			return err
		}
		fn, ok := goja.AssertFunction(s.vm.Get("handle"))
		if !ok {
			return fmt.Errorf("gojahost[%s]: globalThis.handle missing", h.name)
		}

		// Watchdog: interrupt the VM if ctx cancels mid-call.
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				s.vm.Interrupt(ctx.Err())
			case <-done:
			}
		}()

		arg := s.vm.ToValue(map[string]any{
			"route":  req.Route,
			"params": toAnyMap(req.Params),
			"query":  toAnyMap(req.Query),
			"tenant": req.Tenant,
		})
		out, callErr := fn(goja.Undefined(), arg)
		if callErr != nil {
			var iex *goja.InterruptedError
			if errors.As(callErr, &iex) {
				if ce := ctx.Err(); ce != nil {
					return ce
				}
			}
			return fmt.Errorf("gojahost[%s]: handle(%s): %w", h.name, req.Route, callErr)
		}

		// The JS returns {status:number, body:any}. Pull them out and
		// re-marshal body to canonical JSON bytes via the export view.
		exported := out.Export()
		m, ok := exported.(map[string]any)
		if !ok {
			return fmt.Errorf("gojahost[%s]: handle(%s) returned %T, want object", h.name, req.Route, exported)
		}
		status := 200
		if sv, ok := m["status"]; ok {
			if f, ok := sv.(int64); ok {
				status = int(f)
			} else if f, ok := sv.(float64); ok {
				status = int(f)
			}
		}
		bodyBytes, mErr := json.Marshal(m["body"])
		if mErr != nil {
			return fmt.Errorf("gojahost[%s]: marshal body: %w", h.name, mErr)
		}
		resp = &Response{Status: status, Body: bodyBytes}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// Eval runs an arbitrary JS expression against a pooled runtime (bundle
// already loaded) and returns the exported Go value. Used by callers that
// want to invoke a non-route helper the bundle exposes (e.g. applyMarkup).
func (h *Host) Eval(ctx context.Context, fnName string, jsonArg []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []byte
	err := h.withSlot(func(s *slot) error {
		if err := h.ensure(s); err != nil {
			return err
		}
		fn, ok := goja.AssertFunction(s.vm.Get(fnName))
		if !ok {
			return fmt.Errorf("gojahost[%s]: globalThis.%s is not a function", h.name, fnName)
		}
		var arg any
		if len(jsonArg) > 0 {
			if err := json.Unmarshal(jsonArg, &arg); err != nil {
				return fmt.Errorf("gojahost[%s]: %s arg not JSON: %w", h.name, fnName, err)
			}
		}
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				s.vm.Interrupt(ctx.Err())
			case <-done:
			}
		}()
		res, callErr := fn(goja.Undefined(), s.vm.ToValue(arg))
		if callErr != nil {
			var iex *goja.InterruptedError
			if errors.As(callErr, &iex) {
				if ce := ctx.Err(); ce != nil {
					return ce
				}
			}
			return fmt.Errorf("gojahost[%s]: %s: %w", h.name, fnName, callErr)
		}
		b, mErr := json.Marshal(res.Export())
		if mErr != nil {
			return fmt.Errorf("gojahost[%s]: marshal %s result: %w", h.name, fnName, mErr)
		}
		out = b
		return nil
	})
	return out, err
}

// SetGlobal updates an injected global and forces every pooled runtime to
// re-evaluate the bundle on next use (so the new value takes effect). Used by
// the pricing sync path to swap in freshly-synced data.
func (h *Host) SetGlobal(key string, value any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.globals == nil {
		h.globals = map[string]any{}
	}
	h.globals[key] = value
	for _, s := range h.pool {
		s.mu.Lock()
		s.loaded = false // re-run bundle with new globals on next ensure
		s.mu.Unlock()
	}
}

// Close drops the pool.
func (h *Host) Close() error {
	h.mu.Lock()
	h.closed = true
	h.pool = nil
	h.mu.Unlock()
	return nil
}

// withSlot borrows a free pool slot; if all are busy it spins up a one-off
// runtime (matching gojavm's saturation fallback).
func (h *Host) withSlot(call func(*slot) error) error {
	h.mu.Lock()
	pool := h.pool
	h.mu.Unlock()

	for _, s := range pool {
		s.mu.Lock()
		if s.busy {
			s.mu.Unlock()
			continue
		}
		s.busy = true
		s.mu.Unlock()

		err := call(s)

		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
		return err
	}
	// Saturated: ephemeral runtime, fully loaded fresh.
	tmp := &slot{vm: h.factory()}
	return call(tmp)
}

func toAnyMap(m map[string]string) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// installConsole wires a no-op console.{log,info,warn,error,debug} so guest
// code that logs does not throw. goja has no console by default.
func installConsole(vm *goja.Runtime) {
	if !goja.IsUndefined(vm.Get("console")) {
		return
	}
	noop := func(goja.FunctionCall) goja.Value { return goja.Undefined() }
	console := vm.NewObject()
	for _, m := range []string{"log", "info", "warn", "error", "debug", "trace"} {
		_ = console.Set(m, noop)
	}
	_ = vm.Set("console", console)
}
