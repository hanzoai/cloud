// What an agent costs when it is a goroutine, measured.
//
// Naïve publish 2.79 ms cold start and 1.2 MB per agent on isolated-vm, and
// that is the number to beat. But an isolate is a JavaScript execution context
// inside a running process — so the honest comparison is not our container, it
// is our equivalent: a goroutine holding the loop, and wazero holding the code.
//
// A goroutine starts with a 2 KB stack that grows on demand, so a fleet is
// bounded by what the agents actually hold rather than by a per-agent floor.
// wazero is a pure-Go WebAssembly runtime — no CGO, no container, in-process
// like an isolate — and WASM is not one language: CPython, TypeScript via
// QuickJS, Rust and Go all compile to it.
//
//	go run .            # spawn, memory, and WASM instantiation
//	FLEET=1000000 go run .
package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func mb(b uint64) float64 { return float64(b) / 1024 / 1024 }

func heap() uint64 {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// A minimal WASM module: one exported function that adds two numbers. Small on
// purpose — it measures the runtime's instantiation cost rather than the
// module's own work, which is what "cold start" means.
var addWasm = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // magic, version
	0x01, 0x07, 0x01, 0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7f, // type: (i32,i32)->i32
	0x03, 0x02, 0x01, 0x00, // func 0 has type 0
	0x07, 0x07, 0x01, 0x03, 0x61, 0x64, 0x64, 0x00, 0x00, // export "add"
	0x0a, 0x09, 0x01, 0x07, 0x00, 0x20, 0x00, 0x20, 0x01, 0x6a, 0x0b, // body: local.get 0, local.get 1, i32.add
}

func main() {
	fleet := 1_000_000
	if v := os.Getenv("FLEET"); v != "" {
		fleet, _ = strconv.Atoi(v)
	}

	fmt.Printf("\n── Agent as goroutine · %s ──\n\n", runtime.Version())
	fmt.Printf("  host: %s/%s, %d cores\n\n", runtime.GOOS, runtime.GOARCH, runtime.NumCPU())

	// ── 1. Spawn latency. One agent, started, doing nothing yet.
	const spawns = 200_000
	var wg sync.WaitGroup
	wg.Add(spawns)
	start := time.Now()
	for i := 0; i < spawns; i++ {
		go func() { wg.Done() }()
	}
	wg.Wait()
	per := time.Since(start).Nanoseconds() / int64(spawns)
	fmt.Printf("  spawn                 %d ns per agent  (%s for %s)\n",
		per, time.Since(start).Round(time.Millisecond), fmtN(spawns))

	// ── 2. A whole fleet, resident and parked. Every goroutine is alive and
	// blocked on a channel — the shape of an agent waiting for its next turn.
	before := heap()
	gate := make(chan struct{})
	var live atomic.Int64
	var fleetWg sync.WaitGroup
	fleetWg.Add(fleet)

	t0 := time.Now()
	for i := 0; i < fleet; i++ {
		go func() {
			live.Add(1)
			<-gate // parked: waiting on a person, a schedule, an upstream job
			fleetWg.Done()
		}()
	}
	for live.Load() < int64(fleet) {
		time.Sleep(time.Millisecond)
	}
	spawnAll := time.Since(t0)
	after := heap()

	fmt.Printf("  fleet resident        %s goroutines in %s (%s/s)\n",
		fmtN(fleet), spawnAll.Round(time.Millisecond),
		fmtN(int(float64(fleet)/spawnAll.Seconds())))
	fmt.Printf("  memory                %.1f MB total, %.0f bytes per agent\n",
		mb(after-before), float64(after-before)/float64(fleet))
	fmt.Printf("  runtime.NumGoroutine  %s\n\n", fmtN(runtime.NumGoroutine()))

	// ── 3. Waking the fleet. Closing the gate releases every parked agent.
	t1 := time.Now()
	close(gate)
	fleetWg.Wait()
	wake := time.Since(t1)
	fmt.Printf("  wake all              %s for %s (%.0f ns per agent)\n\n",
		wake.Round(time.Millisecond), fmtN(fleet),
		float64(wake.Nanoseconds())/float64(fleet))

	// ── 4. The sandbox: a WASM module instantiated per call, in-process.
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	defer rt.Close(ctx)

	compiled, err := rt.CompileModule(ctx, addWasm)
	if err != nil {
		fmt.Printf("  wasm compile failed: %v\n", err)
		return
	}

	const insts = 2000
	cfg := wazero.NewModuleConfig().WithName("")
	t2 := time.Now()
	var fn api.Function
	for i := 0; i < insts; i++ {
		mod, err := rt.InstantiateModule(ctx, compiled, cfg)
		if err != nil {
			fmt.Printf("  wasm instantiate failed: %v\n", err)
			return
		}
		fn = mod.ExportedFunction("add")
		_ = mod.Close(ctx)
	}
	perInst := float64(time.Since(t2).Microseconds()) / float64(insts)
	fmt.Printf("── Sandbox: wazero, in-process ──\n\n")
	fmt.Printf("  compile once          %s\n", "cached")
	fmt.Printf("  instantiate           %.1f µs per sandbox  (%.3f ms)\n", perInst, perInst/1000)

	// A call into the sandbox, to show the boundary is cheap once crossed.
	if fn != nil {
		mod, _ := rt.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName(""))
		f := mod.ExportedFunction("add")
		const calls = 100_000
		t3 := time.Now()
		for i := 0; i < calls; i++ {
			_, _ = f.Call(ctx, 2, 3)
		}
		fmt.Printf("  call into sandbox     %.0f ns\n", float64(time.Since(t3).Nanoseconds())/float64(calls))
		_ = mod.Close(ctx)
	}

	languages()

	fmt.Printf(`
── Against the published numbers ──

  Naïve, isolated-vm     2.79 ms cold start   1.2 MB per agent
  Hanzo, goroutine       %.4f ms spawn        %.0f bytes per agent
  Hanzo, wazero sandbox  %.4f ms instantiate  in-process, no container

  WASM is not one language: CPython, QuickJS for TypeScript, Rust and Go all
  target it. A V8 isolate is JavaScript only.
`, float64(per)/1e6, float64(after-before)/float64(fleet), perInst/1000)
}

func fmtN(n int) string {
	s := strconv.Itoa(n)
	out := ""
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out += ","
		}
		out += string(c)
	}
	return out
}
