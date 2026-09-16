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

Medians of eight runs, September 2026. Memory was identical on every run.

| | cold start | vs an isolate on THIS machine | vs their published figure |
|---|---|---|---|
| goroutine | **125 ns** | 2,800× | 22,300× |
| goja — JavaScript | **2.4 µs** | 146× | 1,163× |
| wazero — WASM | **7.8 µs** | 45× | 358× |
| gpython — Python | **25.0 µs** | 14× | 112× |

| a fleet of 1,000,000 | |
|---|---|
| resident memory | **573.0 MB — 601 bytes per agent** |
| against an isolate measured here (1.00 MiB) | **1,745× smaller** |
| against their published 1.2 MB | 1,997× smaller |
| spawn all | 446 ms (2.24M/s) |
| wake all | 107 ms (107 ns each) |

Warm paths, for the case where an agent is already up: **740 ns** per goja eval,
**20 ns** to call into a WASM sandbox.

**TWO COLUMNS, BECAUSE ONLY ONE OF THEM IS CONTROLLED.** Their 2.79 ms and 1.2 MB
were measured on an m7i.8xlarge; everything here is a laptop. Running the same
library they attribute those to — isolated-vm 7.0.1 — on this machine gives 0.35
ms and 1.00 MiB (`../sandbox`), and that is the only comparison where the
hardware is held still. The right-hand column is printed because it is the figure
being cited at us, not because it is evidence. It is also flattering, which is
the reason to keep it in the weaker column rather than the headline.

That middle column read 4,720× for a few hours. It was computed against a 0.59 ms
isolate, and five consecutive runs give 0.35 ms p50 (p95 0.41) — so every ratio in
it came down by 1.7×. The correction is in the unflattering direction, which is
the only reason anyone would believe the ones that are not.

And a goroutine is not an isolate: one is a scheduled stack in a Go process, the
other a JavaScript heap. Read these as what each primitive costs, not as one
beating the other.

### The first run of a fresh binary is not the number

This table used to read 145 ns, and `../README.md` read 187 ns for the same
quantity — two published values of one measurement, and the multipliers were
computed from the older one.

Neither was noise. Measured as the first thing the process does, spawn reads
**202, 210 and 201 ns** on three freshly built binaries, and **123, 125 and 123
ns** when the same binary runs again: a 1.65× step, three times out of three,
tight on both sides. The spawn did not get cheaper — the window is 41 ms rather
than 25 ms for the same 200,000 goroutines, so it is one fixed cost inside the
measurement, paid once per binary: first-touch paging of the text segment and
the kernel's signature check on first exec.

So the old figure was the cost of *starting this program*, divided by 200,000
and printed as the cost of starting a goroutine. `main.go` now runs the round
twice and reports the second, naming the discarded one; a fresh binary's first
run reads 124–133 ns, the same as its tenth.

This is the neighbour of something the suite already knew — *benchmark a built
binary, under `go run` the compile is counted* — which stopped one step short. A
built binary's first run is inflated too. The correction happens to favour us,
which is exactly when the method has to be published beside the number.

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
