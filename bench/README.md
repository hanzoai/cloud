# bench

One lane per claim a competitor makes about us, and two for the claims they
cannot make: that you can have the software, and what it costs to ask it for
something.

Every number below was taken on an M-series laptop between 2026-09-07 and
2026-09-11, and every script here re-runs to produce it. Where we lose, the row
says so — a benchmark suite that only contains wins is a brochure.

Timings are the median of the pass; memory is deterministic and has no range.
This is the same pass the two papers cite, and they carry the ranges:
`hanzoai/papers` `hanzo-dormant-agents` and `hanzo-multi-hop-retrieval`. One set
of numbers, one run behind them — if you re-run and get different figures,
update both.

```
bench/self/run.sh                   # build it, boot it, ask each door: can you have this
bench/doors/run.sh                  # what an agent pays per call, per envelope
node fleet/fleet.mjs /tmp/fleet     # 1M dormant agents: bytes, write rate, resume
node fleet/cost.mjs                 # the same, as a monthly bill
cd goroutine && go build -o /tmp/g . && /tmp/g   # agent as goroutine; goja, gpython, wasm
node sandbox/sandbox.mjs            # cold start: isolate vs container vs pooled
node pricing/pricing.mjs            # what a call costs, and what it could sell for
```

`brain/` and `code/` are their own lanes with their own protocol and generated
tables — `brain/METHOD.md` is the protocol, `brain/RESULTS.md` is written from
the runs, and `node brain/results.mjs` reproduces both from a fresh clone. The
summaries below are read from those tables, not typed in beside them.

## What was measured

### Ownership — the row a hosted competitor has no way to fill

`self/run.sh` builds the binary from this repository, starts it, asks it what it
serves, and reaches one operation through every door it opens.

| | measured |
|---|---|
| private modules required | **0** |
| build from source | 13 s |
| binary | 80 MB |
| boot to a healthy answer | **1.2 s** |
| operations served by default | 13 over 8 paths |

Zero private modules is the load-bearing number, and it is the one you can check
without trusting this table: it is a property of `go.mod`. Everything else here
measures what the software does; this measures whether you can have it.

### The per-call tax — same handler, three envelopes

An agent's loop is call, read, decide, call again, so what the envelope costs is
multiplied by every step of every task. `doors/run.sh` asks the same operation
over each door, interleaved, n=200.

`min` across four runs, because it is the only column whose ordering held:

| door | min, best | min, worst |
|---|---|---|
| REST | 0.22 ms | 0.29 ms |
| op-call plane | 0.23 ms | 0.30 ms |
| MCP | 0.25 ms | 0.31 ms |

**The envelope costs 20 to 30 microseconds.** REST wins because the operation is
already the address; MCP pays for a name lookup and a JSON-RPC frame. The p50
read three different orderings across those runs, so it is not in this table —
the difference is smaller than the median's own movement. A hundred tool calls is
three milliseconds of difference against a model turn measured in seconds, which
is the useful finding: pick the door that fits the caller, not the one that
benchmarks fastest.

### Fleet residency — we win, decisively

| | Hanzo, measured | Naïve, published |
|---|---|---|
| state per dormant agent | **477 bytes** | ~1 MB *(their assumption)* |
| 1M agents on disk | **455 MB** | ~977 GB |
| storage at list | **$0.01/mo** | $22/mo |
| write | 309,789 agents/s | — |
| resume | **0.034 ms** in-process | — |

Their row is marked *"MODELLED, NEVER BILLED."* Ours is a file you can `ls`.
2,198× is the distance between an assumption and a measurement.

### Sandbox — the table conflates two primitives

| | measured here | published |
|---|---|---|
| V8 context | **0.15 ms** | Naïve, isolated-vm: 2.79 ms |
| container, cold | **150.8 ms** | E2B <200 ms · Modal ~1 s · Cloudflare 1–3 s |
| container, pooled | **37.5 ms** | — |

Two honest readings. A plain V8 context is **17× faster than their isolate
number**, so an isolate tier would beat the row they lead with. And our cold
container already beats E2B, Modal and Cloudflare, while a pooled one answers in
37.5 ms.

But an isolate runs JavaScript. It cannot run pytest, pip, cargo, or a shell —
which is what a coding agent was asked to do. The two rows are different
primitives and the comparison is per workload, not per millisecond.

### Agent as goroutine — the execution half of the fleet claim

A dormant agent is a row. An agent that is *running* still has to hold its
place, and that is a goroutine rather than a container.

| | measured here | published |
|---|---|---|
| per live agent | **601 bytes** of heap | Naïve, isolated-vm: 1.2 MB |
| spawn | **187 ns** | 2.79 ms cold start |
| wake 1M | 114 ms (114 ns each) | — |
| wazero (WASM) | 8.9 µs instantiate, 23 ns call | — |
| goja (JavaScript) | 2.7 µs per VM, 818 ns warm eval | — |
| gpython (Python) | 29.9 µs per context | — |

601 bytes against a 128 MB container floor is ~223,000×. Memory was identical
on all five runs; the timings are medians. Benchmark a **built binary** — under
`go run` the compile is counted and spawn reads 253 ns instead of 187 ns.

WASM is not one language: CPython, QuickJS for TypeScript, Rust and Go all
target it. A V8 isolate is JavaScript only.

### Agent Brain — most of the gap is closed, and what remains is legible

LoCoMo, the same benchmark their column comes from (Maharana et al.), scored at
k=20 with `zen-embedding-0.6b`. The rows are the generators switched on one at a
time, read from `brain/benchmarks-retrieval.json`:

| | single-hop all | multi-hop all | multi-hop any | recall |
|---|---|---|---|---|
| semantic only | 78.2% | 22.7% | 79.8% | 71.9% |
| + lexical | 82.5% | 22.7% | 78.0% | 74.6% |
| + facts | 82.3% | 36.9% | 88.3% | 78.2% |
| + adjacency | 84.2% | 39.0% | 88.7% | 79.7% |
| + iterative hops | **86.8%** | **39.0%** | 87.9% | **80.9%** |
| Naïve claim | 91% | 57% | — | — |

Flat cosine already beat several systems in their own table — they list
HippoRAG-v2 at 54%, MemGPT/Cognee 28%, Mem0 18%, Zep 7% on single-hop — and the
rest of the ladder is what a store does that similarity does not.

**The multi-hop diagnosis held.** At semantic only, "any" was 79.8% and "all"
22.7%: the store found *a* relevant turn almost every time and all of them
almost never, which is what a multi-hop failure is. A better embedding does not
fix it. Retrieving a second time with what the first pass resolved does: facts
and adjacency together carry multi-hop from 22.7% to 39.0%, and a second hop
carries single-hop to 86.8%.

Two costs. The full stack is p50 1.65 ms against 0.55 ms for cosine alone, and
a global RRF over the same generators scored **worse** on multi-hop (33.7%) than
the ladder it was meant to improve — it is in the table as a failed row because
it was run, not because it was guessed.

Their k and their all-vs-any are unstated, so treat 91/57 as a target rather
than a like-for-like.

### Code retrieval — the typed link beats the model

RepoBench-R, cross-file-first, test split, n=500, `all-MiniLM-L6-v2`, from
`brain/benchmarks-code.json`:

| | recall@1 | recall@5 | MRR |
|---|---|---|---|
| dense only | 18.8% | 72.8% | 0.408 |
| BM25 only | 19.4% | 67.6% | 0.404 |
| typed links only, no model | 31.4% | 75.4% | 0.505 |
| full: dense + BM25 + typed links | **33.2%** | **79.4%** | **0.523** |

The row worth reading twice is the third: **parsing the repository and following
its declarations beats either retrieval model on its own**, at recall@1 by 12
points, with no embedding call at all. The model earns its place in the last
row, not the first.

### Pricing — 90% under them still clears

Costs from `railway.com/pricing`; volumes measured above.

- one call ≈ **51 ms** of compute → **$0.00000063** marginal
- Naïve charge **$0.05** a credit — 79,614× marginal
- at **$0.005** (90% under), 1B calls/month returns **$3.7M** after giving 25%
  to open source, three availability zones, twelve months of retained history,
  and twice the infrastructure bill in people

The line that grows is not agents, it is **history**: a dormant agent stays 477
bytes, its transcript does not. At 2 KB retained per call, a billion calls a
month is 22 TB after a year, and that eventually exceeds compute.

Excluded, and it dominates all of the above: **model tokens**. A turn that
thinks costs 100–1000× the infrastructure under it. Any per-call price sits
beside token billing rather than pretending to include it — which is what Naïve
do when they keep LLM routing out of credits.

## Method notes

`fleet.mjs` pipes SQL on stdin rather than argv: a 50,000-row insert is 20 MB
and argv tops out well before that.

`brain.mjs` caches vectors in `brain-vectors.json` so scoring can be re-run
without re-embedding. Fetch the dataset first:

```
curl -sL https://raw.githubusercontent.com/snap-research/locomo/main/data/locomo10.json -o locomo10.json
```

`sandbox.mjs` needs a container runtime. `colima start` is enough on macOS.

`self/run.sh` and `doors/run.sh` build and start the binary themselves and need
nothing fetched. `doors/run.sh` measures round-robin rather than door by door:
measured in blocks the three rows disagreed about which door was fastest on
every run, because drift between phases on a busy machine is larger than the
difference being measured.

## A lane with no numbers yet

`market/` grades against what happened rather than against an answer key: a
company template, run by an agent on a hard daily budget, scored on what the
world accepted — a change merged, a thread resolved, a page ranked. Four benches
are defined there and **none has been run**, so it contributes no row above.

What it does contribute is the harness and the refusals: a board whose arms
differ in more than one of harness, model and stack will not load, a period is
not graded until its read window closes, and a run is recorded as finished only
when the last window has. `bench/market/README.md` says what each bench still
needs a human to decide — accounts, payment, and the platform terms that decide
whether one of them can exist at all.
