package script

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/dop251/goja"
)

// HTTPRequest is the request an in-VM httpClient.sendRequest hands to the Go
// doer. It mirrors the ActivePieces HttpRequest fields providers actually set.
type HTTPRequest struct {
	Method      string
	URL         string
	Headers     map[string]string
	QueryParams map[string]string
	Body        any
	TimeoutMS   int
}

// HTTPResponse is what the doer returns; Body is the parsed value (JSON
// decoded when the response is JSON, else the raw string) the provider sees
// as response.body.
type HTTPResponse struct {
	Status  int
	Headers map[string]string
	Body    any
}

// HTTPDoer performs one provider HTTP call. ctx bounds it (the caller's
// request/flow context); the default doer is net/http (see http.go). Injecting
// a doer lets the host enforce SSRF policy or record egress.
type HTTPDoer func(ctx context.Context, req HTTPRequest) (HTTPResponse, error)

// Runtime holds the compiled shim shared across all providers and the HTTP
// doer. It is immutable after construction and safe for concurrent use — each
// Run builds its own goja VM, so no provider state crosses invocations
// (the tenant-isolation property: org A's run shares no heap with org B's).
type Runtime struct {
	shim *goja.Program
	http HTTPDoer
}

// Program is one compiled provider program (an ActivePieces provider bundle
// wrapped as a CommonJS module). Compile once, Run many times.
type Program struct {
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
	prog, err := goja.Compile("script/shim.js", shimJS, true)
	if err != nil {
		return nil, fmt.Errorf("script: compile shim: %w", err)
	}
	if doer == nil {
		doer = defaultHTTPDoer
	}
	return &Runtime{shim: prog, http: doer}, nil
}

// Compile wraps a provider's bundled CommonJS source as a callable module and
// compiles it. bundledJS is the output of Bundle (framework packages external).
func (rt *Runtime) Compile(name string, bundledJS []byte) (*Program, error) {
	if name == "" {
		return nil, errors.New("script: empty provider name")
	}
	// CommonJS harness: the bundle assigns to module.exports / calls require();
	// wrapping it as a function lets us supply (module, exports, require).
	wrapped := "(function(module, exports, require){\n" + string(bundledJS) + "\n})"
	prog, err := goja.Compile(name+".provider.js", wrapped, false)
	if err != nil {
		return nil, fmt.Errorf("script: compile provider %s: %w", name, err)
	}
	return &Program{Name: name, prog: prog}, nil
}

// Run executes c's action with the given auth+props and returns the action's
// result (JSON-shaped Go values). It is the single-provider execution the KB
// long-tail sync and the /v1/auto/provider/:id/run surface call.
func (rt *Runtime) Run(ctx context.Context, c *Program, in RunInput) (any, error) {
	if c == nil {
		return nil, errors.New("script: nil provider")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	vm, stop, err := rt.load(ctx, c)
	if err != nil {
		return nil, err
	}
	defer stop()

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
		return nil, fmt.Errorf("script: action %q has no run()", in.Action)
	}
	res, err := runFn(action, ctxObj)
	if err != nil {
		return nil, jsError(err)
	}
	return settle(vm, res)
}

// load builds a fresh VM holding c's module: the shim, the HTTP primitive
// bound to ctx, and the program body evaluated. Every invocation gets its own
// VM, so no state crosses tenants. The VM is interrupted when ctx ends, so a
// program that loops is stopped with the request rather than outliving it;
// the returned stop releases that watch.
func (rt *Runtime) load(ctx context.Context, c *Program) (*goja.Runtime, func() bool, error) {
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
		return nil, nil, fmt.Errorf("script: bind http: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { vm.Interrupt(ctx.Err()) })

	// Install the framework shim (defines require + module table), then the
	// provider program (which require()s the shim and calls createPiece).
	if _, err := vm.RunProgram(rt.shim); err != nil {
		stop()
		return nil, nil, fmt.Errorf("script: init shim: %w", err)
	}
	if err := runConnectorModule(vm, c.prog); err != nil {
		stop()
		return nil, nil, fmt.Errorf("script: load provider %s: %w", c.Name, err)
	}
	return vm, stop, nil
}

// Validate runs the credential check a piece's auth declares — its own
// validate(), the code the piece's authors wrote to ask their API whether a
// key works — against auth, the value the connection would store. It answers
// nil when the piece says valid, an error carrying the piece's reason when it
// says not, and ErrNoCheck when that auth declares no check at all.
//
// kind is the auth's PropertyType (SECRET_TEXT, BASIC_AUTH, CUSTOM_AUTH), and
// auth is the value in the shape validate() takes: the secret itself for
// SECRET_TEXT, {username,password} for BASIC_AUTH, the props object for
// CUSTOM_AUTH.
func (rt *Runtime) Validate(ctx context.Context, c *Program, kind string, auth any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	vm, stop, err := rt.load(ctx, c)
	if err != nil {
		return err
	}
	defer stop()
	mod, err := lastPiece(vm, c.Name)
	if err != nil {
		return err
	}
	var decl *goja.Object
	switch a := mod.Get("auth"); {
	case a == nil || goja.IsUndefined(a) || goja.IsNull(a):
		return ErrNoCheck
	default:
		obj := a.ToObject(vm)
		if arr, ok := a.Export().([]any); ok {
			for i := range arr {
				o := obj.Get(strconv.Itoa(i)).ToObject(vm)
				if o.Get("type").String() == kind {
					decl = o
					break
				}
			}
		} else if obj.Get("type").String() == kind {
			decl = obj
		}
	}
	if decl == nil {
		return ErrNoCheck
	}
	fn, ok := goja.AssertFunction(decl.Get("validate"))
	if !ok {
		return ErrNoCheck
	}
	arg := vm.NewObject()
	_ = arg.Set("auth", vm.ToValue(auth))
	server := vm.NewObject()
	_ = server.Set("apiUrl", "")
	_ = server.Set("publicUrl", "")
	_ = arg.Set("server", server)
	res, err := fn(decl, arg)
	if err != nil {
		return jsError(err)
	}
	out, err := settle(vm, res)
	if err != nil {
		return err
	}
	verdict, _ := out.(map[string]any)
	if valid, _ := verdict["valid"].(bool); valid {
		return nil
	}
	if reason, _ := verdict["error"].(string); reason != "" {
		return fmt.Errorf("%s", reason)
	}
	return errors.New("the credential was refused")
}

// ErrNoCheck is Validate's answer for an auth that declares no validate().
var ErrNoCheck = errors.New("script: the piece declares no credential check")

// runConnectorModule invokes the wrapped bundle with a fresh module/exports and
// the shim-provided require, executing the provider body (which calls
// createPiece).
func runConnectorModule(vm *goja.Runtime, prog *goja.Program) error {
	fnVal, err := vm.RunProgram(prog)
	if err != nil {
		return err
	}
	fn, ok := goja.AssertFunction(fnVal)
	if !ok {
		return errors.New("provider program is not a function")
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

// resolveAction returns the named action object off the provider's provider. A
// provider defines exactly one provider (createPiece); reading the last-created
// provider is unambiguous per VM.
func resolveAction(vm *goja.Runtime, provider, action string) (*goja.Object, error) {
	mod, err := lastPiece(vm, provider)
	if err != nil {
		return nil, err
	}
	getAction, ok := goja.AssertFunction(mod.Get("getAction"))
	if !ok {
		return nil, fmt.Errorf("provider %q exports no getAction", provider)
	}
	actVal, err := getAction(mod, vm.ToValue(action))
	if err != nil {
		return nil, jsError(err)
	}
	if actVal == nil || goja.IsUndefined(actVal) || goja.IsNull(actVal) {
		return nil, fmt.Errorf("unknown action %q on provider %q", action, provider)
	}
	return actVal.ToObject(vm), nil
}

// lastPiece is the piece the module created. A module calls createPiece once;
// the last one is unambiguous per VM.
func lastPiece(vm *goja.Runtime, provider string) (*goja.Object, error) {
	providersVal := vm.Get("__ap_pieces")
	if providersVal == nil || goja.IsUndefined(providersVal) || goja.IsNull(providersVal) {
		return nil, fmt.Errorf("provider %q exported nothing", provider)
	}
	arr := providersVal.ToObject(vm)
	n := int(arr.Get("length").ToInteger())
	if n == 0 {
		return nil, fmt.Errorf("provider %q did not call createPiece", provider)
	}
	return arr.Get(strconv.Itoa(n - 1)).ToObject(vm), nil
}

// makeContext calls the shim's __makeContext(auth, propsValue) to build the
// ActionContext.
func makeContext(vm *goja.Runtime, in RunInput) (goja.Value, error) {
	mk, ok := goja.AssertFunction(vm.Get("__makeContext"))
	if !ok {
		return nil, errors.New("script: shim __makeContext missing")
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
// drained). A Pending promise means the provider used a timer or off-thread
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
		return nil, errors.New("script: action did not settle synchronously (unsupported timer/async)")
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
