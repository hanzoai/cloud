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
bench/self/modules.sh               # and whether anyone else can fetch what it is built from
bench/egress/run.sh                 # and whether having it means anyone hears about it
bench/cipher/run.sh                 # write a value through the API, look for it on the disk
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
| modules the binary needs, fetchable with no credential | **127 of 127** |
| build from source | 13 s |
| binary | 80 MB |
| boot to a healthy answer | **1.2 s** |
| operations served by default | 13 over 8 paths |

The second row is the load-bearing one. Counting private paths in `go.mod` is a
property of a file; asking the public Go proxy for every module the binary
actually needs, with no credential, is a property of whether anyone else can
build it. All 127 answer, and the run ends by asking for a module that does not
exist — 404, or the sweep above would not be evidence.

Everything else here measures what the software does; this measures whether you
can have it.

### Privacy — nothing in the default path phones anyone, including us

`egress/run.sh` starts the binary with no configuration, asks every door for an
operation sixty times, and watches what it connects to throughout.

| | measured |
|---|---|
| attempts to leave | **0** |
| peers off this machine | **0** |

Two observers, because one of them can miss. A listener standing in for the
internet takes `HTTP_PROXY`, so every attempt is recorded with what it asked for
and timing cannot defeat it. `lsof` sampling watches the sockets directly and
**can** miss — the run reports how much it missed of traffic it generated
itself, which on this machine is all of it, so that row is corroboration and
never the proof. Both end pointed at something that does reach out, and the lane
fails if either reports nothing.

The claim is narrow and checkable: nothing in the default path phones home. A
deployment that enables a subsystem with an upstream connects to it, on purpose.

### At rest — the value is not in the file

`cipher/run.sh` writes a value nothing else could have written through two
operations, stops the server, and searches every file under the data directory.
The same binary runs twice.

| build, given a master key | result |
|---|---|
| `CGO_ENABLED=0` | 7 distinct headers · canary in **0** files |
| `CGO_ENABLED=1`, as built | **refuses**, and names the recipe |
| `CGO_ENABLED=1 -tags libsqlite3` | 7 distinct headers · canary in **0** files |
| control: `CLOUD_DEV_UNENCRYPTED=1` | 1 header, `SQLite format 3` · canary in **1** file |

The refusal is a result. cgo is on by default on macOS and that build links
ordinary SQLite, so given a key it stops rather than writing a plaintext store
and reporting success — which is what it did before the check existed.

Each store gets its own data encryption key, so seven files begin with seven
different ciphertexts; a plaintext run writes the same magic seven times.

The right-hand column is not a competitor. It is the proof the left-hand one
measures anything: same binary, same writes, same search, on a store that is
deliberately not encrypted. Without it, a zero could mean the write never
reached the disk or that the search does not read binary files — and the second
of those happened while the lane was being written, which is why the lane fails
if the control ever reads zero.

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

### Sandbox — three primitives, and one of them we now measure

| | measured here | attributed elsewhere |
|---|---|---|
| V8 context | 0.23 ms | — |
| V8 isolate, isolated-vm 7.0.1 | **0.59 ms**, 1.00 MiB each | 2.79 ms, 1.2 MB |
| container, cold | **150.8 ms** | E2B <200 ms · Modal ~1 s · Cloudflare 1–3 s |
| container, pooled | **37.5 ms** | — |

**The isolate row used to be the context row.** A `vm.createContext` makes a
fresh global inside the isolate already running; it is cheap because it shares
the heap, which is exactly what an isolate does not do. Reporting 0.15 ms
against a published 2.79 ms and calling it 17× was comparing two different
primitives in our favour. Running the library the number is attributed to, on
this laptop, an isolate costs **0.59 ms** — still under the figure cited at us,
by 4.7× rather than 17×, and now a measurement rather than an argument.

**The megabyte is V8's, not a vendor's.** A fresh isolate's heap measures 1.00
MiB here, which is close to the 1.2 MB cited — because it is what any V8 isolate
costs, including one of ours. It is not a competitor's weakness.

The cold container beats E2B, Modal and Cloudflare, and a pooled one answers in
37.5 ms. But an isolate runs JavaScript: no pytest, no pip, no cargo, no shell —
which is what a coding agent was asked to do. The rows are different primitives
and the comparison is per workload, not per millisecond.

### Agent as goroutine — the execution half of the fleet claim

A dormant agent is a row. An agent that is *running* still has to hold its
place, and that is a goroutine rather than a container.

| | measured here | attributed elsewhere |
|---|---|---|
| per live agent | **601 bytes** of heap | isolated-vm: 1.2 MB — and 1.00 MiB measured |
| spawn | **187 ns** | 2.79 ms — and 0.59 ms measured |
| wake 1M | 114 ms (114 ns each) | — |
| wazero (WASM) | 8.9 µs instantiate, 23 ns call | — |
| goja (JavaScript) | 2.7 µs per VM, 818 ns warm eval | — |
| gpython (Python) | 29.9 µs per context | — |

601 bytes against a 128 MB container floor is ~223,000×. Against a V8 isolate it
is ~1,700× — and that one is a goroutine against a JavaScript VM, so read it as
what each primitive costs rather than as one beating the other. Memory was
identical on all five runs; the timings are medians. Benchmark a **built
binary** — under `go run` the compile is counted and spawn reads 253 ns instead
of 187 ns.

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
than a like-for-like — and it is **unsourced**: it is not on any page reachable
from their sitemap as of 2026-09-11. [naive.md](naive.md) lists every figure in
this file that is in that position.

### Answering, and what running it yourself costs

Recall says whether the right turn was retrieved. This says whether the question
got answered. LoCoMo token-F1 against the gold string, k=20, from
`brain/benchmarks.json`.

Every row below is the same 282 multi-hop questions, so the column compares. The
hosted readers were run on that category alone; the local ones answered all four,
which is why only their overall column exists.

| reader | where it runs | multi-hop F1 | all four, F1 |
|---|---|---|---|
| gpt-oss-120b | hosted | **45.3** | — |
| enso-flash | hosted | 45.1 | — |
| **qwen3.6:35b-a3b** | **your machine** | **43.0** | **53.3** |
| gemma4:31b | your machine | 40.5 | — |

**Running it yourself costs 2.3 F1 points.** That is the whole of the ownership
premium on this benchmark: 43.0 against 45.3, local open weights against a
hosted 120B, on identical retrieval and identical questions. Nobody has to take
that on faith — `self/` says the software is yours, `egress/` says it tells no
one, and this says what it scores when nothing leaves the machine.

**And the ceiling says where the rest of the gap is.** Handing the same reader
perfect retrieval — the oracle policy, the right turns every time — scores 55.6
on those questions and 61.8 over all four. Our retrieval reaches **77% of that
on multi-hop and 86% overall**, so most of what is left is the reader rather
than the store. A better embedding buys less than a better answer does.

| policy, local reader | multi-hop F1 | all four, F1 |
|---|---|---|
| oracle — perfect retrieval | 55.6 | 61.8 |
| the full stack | 43.0 | 53.3 |
| one hop only | 36.7 | 48.8 |

**What each answer cost, from the runs' own rows.** Every prediction records its
latency and the reader's token usage, so this is arithmetic over
`predictions.jsonl` rather than a second measurement.

| | per question | prompt tokens | seconds |
|---|---|---|---|
| the full stack, local reader | 1,525 answered | 1,400 | **4.8** |
| the full stack, hosted 120B | 282 answered | 1,296 | 6.9 |
| oracle, local reader | 1,525 answered | **294** | 3.1 |

Two things fall out, and the second is the more useful.

**The local reader answered faster.** 4.8 s against 6.9 s on the same policy and
nearly the same prompt. Read the hosted figure as what a caller waits for — it
carries the network and the router with it — rather than as the model being
slower.

**Retrieval sends 4.8× the context the answer needs.** The oracle policy hands
the reader 294 prompt tokens and scores 61.8; the full stack hands it 1,400 and
scores 53.3. Those 1,100 extra tokens per question are what precision is
currently costing, and they are the same tokens any hosted bill is metered on.
Better precision is the lever on both speed and price here — not a faster model,
and not a cheaper rate.

**And the rows say how much of k=20 is doing work.** `brain/depth.mjs` reads a
finished run and asks, of the questions whose evidence was retrieved at all, how
far down the list its last piece sat.

| all evidence within the first… | of those questions | of every question |
|---|---|---|
| 1 | 51.0% | 38.3% |
| 5 | 80.6% | 60.6% |
| 10 | 90.7% | 68.1% |
| 20 | 100% | 75.1% |

Half the time the single top result is the whole answer. Going from k=10 to k=20
buys **7 points of coverage for twice the prompt**, and from k=5 to k=20, 14.5
points for four times it. So k=20 is not waste, and most of it is: the last ten
positions are the expensive half of the context and the smaller half of the
work.

Coverage is not accuracy — having the evidence in front of the reader is
necessary for a right answer and does not produce one — so read those as a
ceiling on what trimming k could cost rather than as an F1 prediction.

**What each k costs, beside what it covers.** The same script reconstructs the
retrieved half of the prompt from the turns it named, and prints it against the
tokens the reader reported for the run's own k:

| k | retrieved tokens | covers | total prompt |
|---|---|---|---|
| 1 | 48 | 38.3% | ~560 |
| 5 | 228 | 60.6% | ~740 |
| 10 | 460 | 68.1% | ~975 |
| 20 | **885** | 75.1% | **1,400** — what the reader counted |

The reconstruction lands on the reader's own number at k=20, which is what makes
the shorter rows worth reading.

**Halving k does not halve the bill.** Going from 20 to 10 removes 48% of the
retrieved tokens and 30% of the prompt, because the rest of it — the
instruction, the question — does not shrink. The oracle row is the floor: 1.52
turns, 76 tokens of context, 294 in total.

**And what those seven points of coverage are worth: 2.2 F1.** The same policy,
the same reader, the same 1,536 questions, answered at k=10:

| | prompt tokens | F1 | multi-hop | single-hop |
|---|---|---|---|---|
| k=20 | 1,400 | **53.3** | 43.0 | 61.9 |
| k=10 | **812** | 51.1 | 40.8 | 58.7 |
| oracle, 1.52 turns | 294 | 61.8 | 55.6 | 70.8 |

So half the context costs 2.2 points overall and 2.2 on multi-hop, for 42% of
the prompt. Whether that is a good trade is a pricing question rather than a
retrieval one, and it is now a question with both numbers in it.

**Latency across runs is not comparable, and this is how we know.** The k=20 run
recorded 4.79 s a question and the oracle run 3.08 s — at 294 prompt tokens
against 1,400, which prompt size cannot explain. They were taken on different
days under unknown load. Measured back to back on an idle machine, the same
category at both settings:

| k | context tokens | p50 |
|---|---|---|
| 10 | 479 | **1.4 s** |
| 20 | 917 | 2.2 s |

**1.6× faster for half the context** — not the 3× the older rows suggested. The
doors lane learned this and said so; this lane had not, and its per-question
seconds were being read as a property of the configuration when they were a
property of the afternoon.

**Across three policies it ordered them the way the reader did.** The same
measurement, run on each, against the F1 those runs scored:

| policy | evidence at k=1 | at k=5 | at k=20 | F1 |
|---|---|---|---|---|
| the full stack | **38.3%** | **60.6%** | **75.1%** | **53.3** |
| cer | 32.6% | 56.5% | 71.3% | 50.3 |
| one hop only | 25.7% | 49.4% | 65.3% | 48.8 |

Three points is not a law, but it is three for three, and it suggests something
useful: a retrieval change can be ranked **before** anyone spends a reader token
on it, because the evidence was either retrieved or it was not and that question
costs nothing to ask.

The other thing the k=1 column says is where the extra hops earn their keep. The
full stack leads the single-hop row by 12.6 points at k=1 and by 9.8 at k=20 —
so following the first result mostly moves the right turn **up the list**,
rather than finding turns that were missing. That is the same finding as the
token row from the other side: what is being bought is position, and position is
what a smaller k is allowed to trade on.

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

### Pricing — compute is not where the difference is

Our costs from `railway.com/pricing`; their prices from `usenaive.ai/pricing`,
read 2026-09-11 and recorded verbatim in [naive.md](naive.md); volumes measured
above.

- one call ≈ **51 ms** of compute → **$0.00000063** marginal, measured
- the same call at their published **$0.0504/vCPU-hr** → **$0.00000071**
- so their compute is about **1.1×** what the work costs — a thin markup

An earlier version of this row said they charge $0.05 a credit and called it
79,614× marginal. No per-credit price is published; $0.0504 is an hour of a
vCPU, and reading it as a call overstates their price by the ratio of an hour to
a call. The correction goes against us and the row is here as it reads.

Where their published prices do carry a markup is the metered tools —
`$0.002158` for one web search — and the model tokens, which every stack bills
and which dominate both columns.

- at **$0.005** a call, 1B calls/month returns **$3.7M** after giving 25%
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

`sandbox.mjs` needs a container runtime for two of its four rows — `colima
start` is enough on macOS — and `npm i isolated-vm` in `bench/sandbox/` for the
isolate row. Each row says so and is skipped rather than guessed when its
dependency is absent.

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
