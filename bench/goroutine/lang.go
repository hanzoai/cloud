package main

import (
	"fmt"
	"time"

	"github.com/dop251/goja"
	"github.com/go-python/gpython/py"
	_ "github.com/go-python/gpython/stdlib"
)

// Two more in-process runtimes, both pure Go. This is the part a V8 isolate
// cannot answer: the same process runs JavaScript AND Python with no container,
// no CGO, and no second language runtime installed on the host.
func languages() {
	fmt.Printf("\n── Language runtimes, in-process ──\n\n")

	// goja: a JavaScript interpreter written in Go. One vm per agent is the
	// direct analogue of one isolate per agent.
	const n = 2000
	t0 := time.Now()
	var last goja.Value
	for i := 0; i < n; i++ {
		vm := goja.New()
		v, err := vm.RunString(`(function(a,b){return a+b})(2,3)`)
		if err != nil {
			fmt.Printf("  goja failed: %v\n", err)
			return
		}
		last = v
	}
	per := float64(time.Since(t0).Microseconds()) / float64(n)
	fmt.Printf("  goja (JavaScript)     %.1f µs per vm  (%.4f ms) → %v\n", per, per/1000, last)

	// Reusing one vm, which is what a warm agent does between turns.
	vm := goja.New()
	const calls = 100_000
	t1 := time.Now()
	for i := 0; i < calls; i++ {
		_, _ = vm.RunString(`2+3`)
	}
	fmt.Printf("  goja, warm vm         %.0f ns per eval\n",
		float64(time.Since(t1).Nanoseconds())/float64(calls))

	python()
}

// gpython: a Python interpreter written in Go. Same shape as goja — an
// interpreter in the same process, no CPython on the host, no container.
func python() {
	const n = 200
	t0 := time.Now()
	var out py.Object
	for i := 0; i < n; i++ {
		ctx := py.NewContext(py.DefaultContextOpts())
		mod, err := py.RunFile(ctx, "add.py", py.CompileOpts{}, nil)
		if err != nil {
			fmt.Printf("  gpython failed: %v\n", err)
			return
		}
		out, _ = mod.Globals["result"], error(nil)
		ctx.Close()
	}
	per := float64(time.Since(t0).Microseconds()) / float64(n)
	fmt.Printf("  gpython (Python)      %.1f µs per context (%.4f ms) → %v\n", per, per/1000, out)
}
