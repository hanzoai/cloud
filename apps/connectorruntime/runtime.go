// Package connectorruntime executes an automation connector's action NATIVELY
// in-process — no Node. A connector authored against the ActivePieces
// framework (@activepieces/pieces-framework + pieces-common) is compiled once
// to a single CommonJS program (see Bundle) and run inside goja, with the
// framework supplied by an in-process shim (shim.js) whose only impure
// primitive is a Go HTTP doer. Because that doer resolves synchronously, an
// action's `async run(ctx)` settles inside goja's own microtask drain, so a
// connector executes as ordinary in-process work — a goroutine, not a service.
//
// This is the substrate that retires the standalone ActivePieces Node engine
// (the `auto` pod). It sits ALONGSIDE clients/automations' Tier-A native Go
// connectors: those are hand-written Go; this runs the long tail of JS
// connectors unchanged. Both are org-scoped by the caller — the runtime never
// resolves a credential itself; it receives the already-resolved `auth`.
package connectorruntime

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/dop251/goja"
)

// HTTPRequest is the request an in-VM httpClient.sendRequest hands to the Go
// doer. It mirrors the ActivePieces HttpRequest fields connectors actually set.
type HTTPRequest struct {
	Method      string
	URL         string
	Headers     map[string]string
	QueryParams map[string]string
	Body        any
	TimeoutMS   int
}

// HTTPResponse is what the doer returns; Body is the parsed value (JSON
// decoded when the response is JSON, else the raw string) the connector sees
// as response.body.
type HTTPResponse struct {
	Status  int
	Headers map[string]string
	Body    any
}

// HTTPDoer performs one connector HTTP call. ctx bounds it (the caller's
// request/flow context); the default doer is net/http (see http.go). Injecting
// a doer lets the host enforce SSRF policy or record egress.
type HTTPDoer func(ctx context.Context, req HTTPRequest) (HTTPResponse, error)

// Runtime holds the compiled shim shared across all connectors and the HTTP
// doer. It is immutable after construction and safe for concurrent use — each
// Run builds its own goja VM, so no connector state crosses invocations
// (the tenant-isolation property: org A's run shares no heap with org B's).
type Runtime struct {
	shim *goja.Program
	http HTTPDoer
}

// Connector is one compiled connector program (an ActivePieces piece bundle
// wrapped as a CommonJS module). Compile once, Run many times.
type Connector struct {
	Name string
	prog *goja.Program
}

// RunInput is one action invocation.
type RunInput struct {
	Action string         // action name, e.g. "web_search" / "custom_api_call"
	Auth   any            // resolved credential value handed to ctx.auth
	Props  map[string]any // ctx.propsValue
}

// NewRuntime compiles the shim and returns a runtime. A nil doer selects the
// default net/http doer.
func NewRuntime(doer HTTPDoer) (*Runtime, error) {
	prog, err := goja.Compile("connectorruntime/shim.js", shimJS, true)
	if err != nil {
		return nil, fmt.Errorf("connectorruntime: compile shim: %w", err)
	}
	if doer == nil {
		doer = defaultHTTPDoer
	}
	return &Runtime{shim: prog, http: doer}, nil
}

// Compile wraps a connector's bundled CommonJS source as a callable module and
// compiles it. bundledJS is the output of Bundle (framework packages external).
func (rt *Runtime) Compile(name string, bundledJS []byte) (*Connector, error) {
	if name == "" {
		return nil, errors.New("connectorruntime: empty connector name")
	}
	// CommonJS harness: the bundle assigns to module.exports / calls require();
	// wrapping it as a function lets us supply (module, exports, require).
	wrapped := "(function(module, exports, require){\n" + string(bundledJS) + "\n})"
	prog, err := goja.Compile(name+".connector.js", wrapped, false)
	if err != nil {
		return nil, fmt.Errorf("connectorruntime: compile connector %s: %w", name, err)
	}
	return &Connector{Name: name, prog: prog}, nil
}

// Run executes c's action with the given auth+props and returns the action's
// result (JSON-shaped Go values). It is the single-connector execution the KB
// long-tail sync and the /v1/automations/connectors/:id/run surface call.
func (rt *Runtime) Run(ctx context.Context, c *Connector, in RunInput) (any, error) {
	if c == nil {
		return nil, errors.New("connectorruntime: nil connector")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	vm := goja.New()
	installConsole(vm)

	// The sole impure primitive: perform the HTTP call synchronously on this
	// goroutine. A doer error is thrown into JS (caught by the shim's Promise
	// executor -> reject) so the action's try/catch and failsafe paths work.
	if err := vm.Set("__hanzoHttpSend", func(call goja.FunctionCall) goja.Value {
		resp, err := rt.http(ctx, parseHTTPRequest(vm, call.Argument(0)))
		if err != nil {
			panic(vm.ToValue(vm.NewGoError(err)))
		}
		return vm.ToValue(map[string]any{
			"status":  resp.Status,
			"headers": resp.Headers,
			"body":    resp.Body,
		})
	}); err != nil {
		return nil, fmt.Errorf("connectorruntime: bind http: %w", err)
	}

	// Install the framework shim (defines require + module table), then the
	// connector program (which require()s the shim and calls createPiece).
	if _, err := vm.RunProgram(rt.shim); err != nil {
		return nil, fmt.Errorf("connectorruntime: init shim: %w", err)
	}
	if err := runConnectorModule(vm, c.prog); err != nil {
		return nil, fmt.Errorf("connectorruntime: load connector %s: %w", c.Name, err)
	}

	action, err := resolveAction(vm, c.Name, in.Action)
	if err != nil {
		return nil, err
	}

	ctxObj, err := makeContext(vm, in)
	if err != nil {
		return nil, err
	}

	runFn, ok := goja.AssertFunction(action.Get("run"))
	if !ok {
		return nil, fmt.Errorf("connectorruntime: action %q has no run()", in.Action)
	}
	res, err := runFn(action, ctxObj)
	if err != nil {
		return nil, jsError(err)
	}
	return settle(vm, res)
}

// runConnectorModule invokes the wrapped bundle with a fresh module/exports and
// the shim-provided require, executing the connector body (which calls
// createPiece).
func runConnectorModule(vm *goja.Runtime, prog *goja.Program) error {
	fnVal, err := vm.RunProgram(prog)
	if err != nil {
		return err
	}
	fn, ok := goja.AssertFunction(fnVal)
	if !ok {
		return errors.New("connector program is not a function")
	}
	module := vm.NewObject()
	exports := vm.NewObject()
	if err := module.Set("exports", exports); err != nil {
		return err
	}
	requireFn := vm.Get("require")
	_, err = fn(goja.Undefined(), module, exports, requireFn)
	return err
}

// resolveAction returns the named action object off the connector's piece. A
// connector defines exactly one piece (createPiece); reading the last-created
// piece is unambiguous per VM.
func resolveAction(vm *goja.Runtime, connector, action string) (*goja.Object, error) {
	piecesVal := vm.Get("__ap_pieces")
	if piecesVal == nil || goja.IsUndefined(piecesVal) || goja.IsNull(piecesVal) {
		return nil, fmt.Errorf("connector %q defined no piece", connector)
	}
	arr := piecesVal.ToObject(vm)
	n := int(arr.Get("length").ToInteger())
	if n == 0 {
		return nil, fmt.Errorf("connector %q did not call createPiece", connector)
	}
	piece := arr.Get(strconv.Itoa(n - 1)).ToObject(vm)
	getAction, ok := goja.AssertFunction(piece.Get("getAction"))
	if !ok {
		return nil, fmt.Errorf("connector %q piece missing getAction", connector)
	}
	actVal, err := getAction(piece, vm.ToValue(action))
	if err != nil {
		return nil, jsError(err)
	}
	if actVal == nil || goja.IsUndefined(actVal) || goja.IsNull(actVal) {
		return nil, fmt.Errorf("unknown action %q on connector %q", action, connector)
	}
	return actVal.ToObject(vm), nil
}

// makeContext calls the shim's __makeContext(auth, propsValue) to build the
// ActionContext.
func makeContext(vm *goja.Runtime, in RunInput) (goja.Value, error) {
	mk, ok := goja.AssertFunction(vm.Get("__makeContext"))
	if !ok {
		return nil, errors.New("connectorruntime: shim __makeContext missing")
	}
	props := in.Props
	if props == nil {
		props = map[string]any{}
	}
	ctxVal, err := mk(goja.Undefined(), vm.ToValue(in.Auth), vm.ToValue(props))
	if err != nil {
		return nil, jsError(err)
	}
	return ctxVal, nil
}

// settle resolves the action's return value. An async run returns a Promise
// that — because every await resolved synchronously through the Go doer — is
// already Fulfilled by the time goja hands control back (the microtask queue
// drained). A Pending promise means the connector used a timer or off-thread
// async the in-process runtime does not drive; that is an honest error, not a
// hang.
func settle(vm *goja.Runtime, v goja.Value) (any, error) {
	p, ok := v.Export().(*goja.Promise)
	if !ok {
		if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
			return nil, nil
		}
		return v.Export(), nil
	}
	switch p.State() {
	case goja.PromiseStateFulfilled:
		r := p.Result()
		if r == nil || goja.IsUndefined(r) || goja.IsNull(r) {
			return nil, nil
		}
		return r.Export(), nil
	case goja.PromiseStateRejected:
		return nil, promiseRejection(vm, p.Result())
	default:
		return nil, errors.New("connectorruntime: action did not settle synchronously (unsupported timer/async)")
	}
}

// parseHTTPRequest extracts the Go HTTPRequest from the in-VM request object.
func parseHTTPRequest(vm *goja.Runtime, v goja.Value) HTTPRequest {
	req := HTTPRequest{}
	m, _ := v.Export().(map[string]any)
	if m == nil {
		return req
	}
	req.Method, _ = m["method"].(string)
	req.URL, _ = m["url"].(string)
	req.Headers = toStringMap(m["headers"])
	req.QueryParams = toStringMap(m["queryParams"])
	req.Body = m["body"]
	req.TimeoutMS = toInt(m["timeout"])
	return req
}
