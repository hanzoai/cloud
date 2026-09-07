# bench

Four measurements, one per claim a competitor makes about us.

Every number below was taken on an M-series laptop in September 2026, and every
script here re-runs to produce it. Where we lose, the row says so — a benchmark
suite that only contains wins is a brochure.

```
node fleet/fleet.mjs /tmp/fleet     # 1M dormant agents: bytes, write rate, resume
node fleet/cost.mjs                 # the same, as a monthly bill
node brain/brain.mjs                # LoCoMo recall, single-hop and multi-hop
node sandbox/sandbox.mjs            # cold start: isolate vs container vs pooled
node pricing/pricing.mjs            # what a call costs, and what it could sell for
```

## What was measured

### Fleet residency — we win, decisively

| | Hanzo, measured | Naïve, published |
|---|---|---|
| state per dormant agent | **477 bytes** | ~1 MB *(their assumption)* |
| 1M agents on disk | **455 MB** | ~977 GB |
| storage at list | **$0.01/mo** | $22/mo |
| write | 310,270 agents/s | — |
| resume | **0.031 ms** in-process | — |

Their row is marked *"MODELLED, NEVER BILLED."* Ours is a file you can `ls`.
2,198× is the distance between an assumption and a measurement.

### Sandbox — the table conflates two primitives

| | measured here | published |
|---|---|---|
| V8 context | **0.16 ms** | Naïve, isolated-vm: 2.79 ms |
| container, cold | **149.6 ms** | E2B <200 ms · Modal ~1 s · Cloudflare 1–3 s |
| container, pooled | **35.8 ms** | — |

Two honest readings. A plain V8 context is **17× faster than their isolate
number**, so an isolate tier would beat the row they lead with. And our cold
container already beats E2B, Modal and Cloudflare, while a pooled one answers in
35.8 ms.

But an isolate runs JavaScript. It cannot run pytest, pip, cargo, or a shell —
which is what a coding agent was asked to do. The two rows are different
primitives and the comparison is per workload, not per millisecond.

### Agent Brain — we lose today, and the gap is legible

LoCoMo, the same benchmark their column comes from (Maharana et al.), category 4
single-hop and category 1 multi-hop. Retrieval with `zen-embedding-0.6b`, plain
cosine, no reranking and no graph:

| | single-hop (841 Q) | multi-hop (282 Q) |
|---|---|---|
| recall@5 all | 59.1% | 10.3% |
| recall@10 all | 69.3% | 14.9% |
| recall@20 all | **78.2%** | 22.7% |
| recall@20 any | 80.7% | **80.1%** |
| Naïve claim | 91% | 57% |

A flat vector search beats several systems in their own table — they list
HippoRAG-v2 at 54%, MemGPT/Cognee 28%, Mem0 18%, Zep 7% on single-hop — but it
does not reach their number.

The multi-hop rows say why, and say what to build. **Recall@20 "any" is 80.1%
while "all" is 22.7%**: the store finds *a* relevant turn almost every time and
all of them almost never. That is the definition of a multi-hop failure, and it
is not fixed by a better embedding. It is fixed by retrieving more than once —
follow the entities in the first hit and search again.

Their k and their all-vs-any are unstated, so treat 91/57 as a target rather
than a like-for-like.

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
