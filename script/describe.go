package script

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dop251/goja"
)

//go:embed describe.js
var describeJS string

// describeBudget bounds how long a piece's module body may run while it is
// described. A body declares things; one that loops is refused, not waited on.
const describeBudget = 5 * time.Second

// Describe loads a piece program (from Bundle or BundleOpen) and returns what
// it declares — name, description, logo, categories, auth, actions, triggers —
// as JSON. Nothing the piece declares is executed: no action runs, no auth is
// validated, and a module it imports that is not the framework is a stand-in.
// HTTP is refused outright, so a body that tries to call out fails here rather
// than reaching the network.
//
// It is the ingest step's reader (cmd/piece), not a request path.
func Describe(bundledJS []byte) (json.RawMessage, error) {
	vm := goja.New()
	installConsole(vm)
	if err := vm.Set("__hanzoHttpSend", func(goja.FunctionCall) goja.Value {
		panic(vm.NewGoError(errors.New("script: no network while describing")))
	}); err != nil {
		return nil, err
	}
	timer := time.AfterFunc(describeBudget, func() { vm.Interrupt("describe budget exceeded") })
	defer timer.Stop()

	shim, err := goja.Compile("script/shim.js", shimJS, true)
	if err != nil {
		return nil, fmt.Errorf("script: compile shim: %w", err)
	}
	if _, err := vm.RunProgram(shim); err != nil {
		return nil, fmt.Errorf("script: init shim: %w", err)
	}
	if _, err := vm.RunString(describeJS); err != nil {
		return nil, fmt.Errorf("script: init describe: %w", err)
	}
	prog, err := goja.Compile("piece.js", "(function(module, exports, require){\n"+string(bundledJS)+"\n})", false)
	if err != nil {
		return nil, fmt.Errorf("script: compile piece: %w", err)
	}
	if err := runConnectorModule(vm, prog); err != nil {
		return nil, fmt.Errorf("script: load piece: %w", jsError(err))
	}
	fn, ok := goja.AssertFunction(vm.Get("__describe"))
	if !ok {
		return nil, errors.New("script: describe missing")
	}
	out, err := fn(goja.Undefined())
	if err != nil {
		return nil, jsError(err)
	}
	return json.RawMessage(out.String()), nil
}

// Loads reports whether a program runs its module body under the runtime
// shim ALONE — no stand-ins — which is what makes a piece runnable here. The
// body only declares; the network is refused while it runs.
func Loads(name string, bundledJS []byte) error {
	rt, err := NewRuntime(func(context.Context, HTTPRequest) (HTTPResponse, error) {
		return HTTPResponse{}, errors.New("script: no network while loading")
	})
	if err != nil {
		return err
	}
	c, err := rt.Compile(name, bundledJS)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), describeBudget)
	defer cancel()
	vm, stop, err := rt.load(ctx, c)
	if err != nil {
		return err
	}
	defer stop()
	_, err = lastPiece(vm, name)
	return err
}
