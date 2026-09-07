# Agent as goroutine

The honest comparison to an isolate is not our container — it is our equivalent.

Naïve publish **2.79 ms cold start, 1.2 MB per agent** on isolated-vm. An
isolate is a JavaScript execution context inside a process that is already
running. Ours is a goroutine holding the loop, with the code in wazero, goja or
gpython — all pure Go, all in the same process, no CGO and no container.

```
go run .                # spawn, 1M-agent fleet, wazero, goja, gpython
FLEET=5000000 go run .  # bigger fleet
```

## Measured — M-series laptop, 16 cores, Go 1.26.5

| | cold start | vs isolated-vm |
|---|---|---|
| goroutine | **145 ns** | 19,241× faster |
| goja — JavaScript | **3.2 µs** | 872× faster |
| wazero — WASM | **8.9 µs** | 313× faster |
| gpython — Python | **42.5 µs** | 66× faster |

| a fleet of 1,000,000 | |
|---|---|
| resident memory | **573 MB — 601 bytes per agent** |
| against their 1.2 MB | **2,092× smaller** |
| spawn all | 518 ms (1.9M/s) |
| wake all | 109 ms (109 ns each) |

Warm paths, for the case where an agent is already up: **897 ns** per goja eval,
**27 ns** to call into a WASM sandbox.

## Why this is the comparison that matters

A goroutine starts on a 2 KB stack that grows on demand, so a fleet costs what
its agents actually hold rather than a per-agent floor. Parking a million of
them on a channel is the shape of a real fleet: awake when there is work,
resident and nearly free when there is not.

And the language row is the part an isolate cannot answer. **goja runs
JavaScript and gpython runs Python, in the same process, with neither runtime
installed on the host.** WASM adds Rust, Go, C and CPython through wazero. A V8
isolate is JavaScript, and only JavaScript.

## What this does not claim

These are in-process interpreters, not kernel boundaries. Untrusted code that
needs a filesystem, a package manager, or a real syscall surface still wants a
container — see `../sandbox`, where a pooled one answers in 35.8 ms. The right
read is a ladder: goroutine for the loop, goja/gpython/wasm for code you are
willing to run in-process, container for code you are not.
