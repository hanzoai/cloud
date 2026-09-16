# What naïve publishes

Every figure this suite compares against, with where it came from and when it
was read. A comparison whose other column has no source is not a comparison.

Read 2026-09-11 from the pages in the sitemap at `usenaive.ai/sitemap.xml`.

**Re-read 2026-09-15.** Every price below is unchanged. Four details the first
read did not capture:

- the same compute rates per second — "$0.000014 per second, while running" and
  "$0.0000045 per GiB-second", which divide out of the hourly figures exactly
- "Usage metered per tier, booked in integer micro-USD" — their ledger's unit is
  the integer micro-USD, where ours is an exact 18-decimal amount
- creation fees "$0" ("there is no creation fee")
- the completion ladder in full: "immediate · now 1.00×", "priority · ≤ 1 h 0.60×",
  "loose · ≤ 24 h 0.35×" — the first read recorded only the last of the three

One discrepancy, left as a discrepancy rather than quietly resolved: the example
box reads `2 vcpu · 4 gb · chromium 129` today where the line below records 4 vcpu.
The page renders the region and the spec without a separator
(`iad/workspace2 vcpu · 4 gb`), so this is either a change on their side or a
mis-split of that string on the first read. It is not load-bearing for any figure
in this suite — no table prices their example box — and it is recorded because a
source that moved without anyone noticing is the failure this file exists to catch.

## They publish more than this file said, and it was never hidden

`usenaive.ai/benchmarks` answers 404, and both the first read and the 2026-09-15
re-read concluded from that there was nothing of theirs to compare against. That
was wrong, and wrong in the way that is hardest to notice: each read checked the
pages this file already listed. The sitemap it cites carries ninety-odd URLs, and
among them are `/lab/memory`, `/lab/sandbox`, `/lab/inference` and four
`/benchmark/<name>` pages — singular, which is why guessing the plural 404'd.

Read 2026-09-15 from `usenaive.ai/lab/sandbox`, measured by them on an
**AWS m7i.8xlarge, 2026-07-22**:

| | published |
|---|---|
| cold start | "2.79ms" |
| warm resume | "~1ms warm resume" |
| fork | "1.55ms copy-on-write" |
| RAM per agent | "1.2MB" |
| idle agent | "KBs of state storage only" |
| concurrency | "2,000 concurrent agents under an 8-second heartbeat at a p99 execution latency of 37.5 ms" |
| at scale | "at 4,000 agents p99 rises to 73.8 ms, over our 50 ms SLO" |
| worker_threads fallback | "roughly 15x slower on create and 15-17x heavier per agent" |
| E2B (Firecracker microVM) | cold "<200ms", resume "~1s from pause", RAM min "512MB" |
| Modal (container/gVisor) | cold "~1s", resume "~1s mem snapshot", RAM min "128MB" |
| Morph (microVM) | resume "<250ms", fork "<250ms" |
| Cloudflare (container) | cold "1-3s", RAM min "256MB" |

Read the same day from `usenaive.ai/lab/memory`, dated July 2026:

| | single-hop | multi-hop |
|---|---|---|
| Naive Brain | "91%" | "57%" |
| CAR (prior SOTA) | "78%" | "30.2%" |
| HippoRAG-v2 | "54%" | "<=7%" |
| MemGPT / Cognee | "28%" | "<=7%" |
| Mem0 / Contriever | "18%" | "<=7%" |
| Zep / Graphiti | "7%" | "<=7%" |

on "FictionalCharacters QA on a matched gpt-4o-mini backbone", metric
"% of held-out questions answered correctly", and "Naive measured, others
published". The preprint, "Both Engines, One Harness", is "Coming soon".

### Two comparisons in this suite are now known to be wrong

**The 91/57 row is not a LoCoMo row.** README's LoCoMo retrieval ladder ends with
"Naive claim 91% 57%" beside our 86.8% / 39.0%. Ours are RETRIEVAL recall on
LoCoMo; theirs are ANSWER accuracy on FictionalCharacters QA with a gpt-4o-mini
reader. Different dataset, different quantity, different backbone — three ways
apart, and the row reads as one table. It has to come out or be relabelled.

**The isolate comparison is across different machines.** Their 2.79 ms is an
m7i.8xlarge; our 0.59 ms is a laptop. The direction happens to favour us and that
is not the point — an uncontrolled comparison is not evidence, whichever way it
falls.

### What their benchmark is, established by its own baselines

`/lab/memory` names no dataset, no question count, no protocol and no citation, so
the first reading here was that the identification could not be made. It can, from
the page's own comparison column. Every baseline it prints is the CAR paper's
MemoryAgentBench number, verified in `brain/mab/README.md` against arXiv 2606.01435:

| | CAR paper, MemoryAgentBench | `/lab/memory` |
|---|---|---|
| CAR, gpt-4o-mini, pooled 6K-262K | 78.0 / 30.2 | "78%" / "30.2%" |
| HippoRAG-v2 | 54 / 5 | "54%" / "<=7%" |
| MemGPT, Cognee | 28 / 3 | "28%" / "<=7%" |
| Mem0 | 18 / 2 | "18%" / "<=7%" |
| Zep | 7 / 3 | "7%" / "<=7%" |

Five baselines, matching to the decimal, with their multi-hop scores collapsed
into the "<=7%" the page's own headline uses. A table is identified by its
baselines the way a photograph is identified by its background. "FictionalCharacters
QA" is MemoryAgentBench Conflict Resolution — FactConsolidation, whose haystacks
are templated facts about invented entities, which is where the name comes from —
and `brain/mab` has been running it all along.

So the comparison IS like-for-like on dataset and metric, and it is ours:

| | single-hop | multi-hop |
|---|---|---|
| **this suite**, beamx, **no reader** | **98.2** | **84.5** |
| Naive Brain, "matched gpt-4o-mini backbone" | 91 | 57 |
| CAR, gpt-4o | 94.8 | 51.5 |
| CAR, gpt-4o-mini | 78.0 | 30.2 |

+7.2 single-hop and **+27.5 multi-hop** over their published figure, and the
readers differ in the direction that favours this suite: theirs is a matched
gpt-4o-mini, ours is none at all — the answer is resolved by typed search, and
`substring_exact_match` scores a median ten-character string, not a retrieved
blob. `table.mjs` names the reader on every row, because a row with a different
reader is a different table.

Two things still stand against over-quoting it. Their row is "Naive measured,
others published" with no protocol given, so what varied under their 91/57 is
unknown. And the pooled mean mixes 6k, which is this suite's dev split, with the
three test sizes — CAR pools that way, so the columns match, and it should be said
wherever the pooled number is.

## Pricing — `usenaive.ai/pricing`

| | published |
|---|---|
| Pro | "$20/month", "$20 of credit included each month" |
| seats | "Seats $0 — no per-seat fee" |
| computer, vCPU | "$0.0504/hr" |
| computer, memory | "$0.0162/GiB-hr" |
| computer, paused | "Computer · paused $0" |
| web search | "$0.002158/call" |
| web fetch | "$0.001079/call" |
| model inference | per token, five tiers: input, cached reads, cache writes, output, reasoning |
| not metered | "MCP, the SDK and the CLI are not metered" |
| media generation | billed at actual job cost after completion |

## Computer — `usenaive.ai/primitives/computer`

> "A disposable Linux box with a shell, a filesystem, and a real browser."

The example box is "4 vcpu · 4 gb · chromium 129". Idle is "$0.00 / hr asleep".
Completion windows are tiered, "loose · ≤ 24 h" at 0.35× base.

**No start-up time, resume time, or per-agent memory is published.**

## Docs — `usenaive.ai/docs`

One comparative claim, with no figure behind it on the page:

> "the same model on different stacks varies several-fold in cost per completed task"

It points at `usenaive.ai/benchmarks`, which **answers 404**.

## Landing — `usenaive.ai`

Run counts per template (483,271 · 917,438 · 152,904 · 178,326) and team sizes
("5 agents · 1 app"). No performance, latency, memory or accuracy figures.

## Figures this suite cites that are NOT on those pages

These appear in `README.md` and are not published anywhere reachable from the
sitemap as of the date above. Each needs a source or should come out of the
tables — a number in a comparison column with no citation is the weakest thing
in this directory.

| cited as | where in this suite | status |
|---|---|---|
| 91% single-hop, 57% multi-hop | Agent Brain | **sourced** `/lab/memory` — but it is FictionalCharacters QA answer accuracy, NOT LoCoMo retrieval; the comparison is invalid, see above |
| isolated-vm 2.79 ms cold start | Sandbox, Agent as goroutine | **sourced** `/lab/sandbox` — on an m7i.8xlarge; measured here at 0.59 ms on a laptop, so not controlled |
| isolated-vm 1.2 MB per agent | Agent as goroutine | **sourced** `/lab/sandbox` — and measured here at 1.00 MiB, which is V8's floor either way |
| ~1 MB per dormant agent | Fleet residency — already marked *"their assumption"* | modelled, not theirs |
| "$0.05 a credit" | Pricing | **withdrawn** — see below |

The sandbox lane prints three more, about other vendors. All three are naive's
numbers for those vendors, published on `/lab/sandbox` — E2B "<200ms" / "512MB",
Modal "~1s" / "128MB", Cloudflare "1-3s" / "256MB" — and they are sourced to
naive, not to E2B, Modal or Cloudflare. `docs.e2b.dev` carried no start-up time
and no default memory on the page checked 2026-09-11.

That distinction is the whole of it: quoting a competitor's figure for a third
party is citing what naive says about E2B, which is a different claim from what
E2B says about itself, and a table that blurs the two is repeating a rival's
marketing as though it were the vendor's own documentation.

## The pricing row was wrong, and the correction goes the other way

The Pricing section read "Naïve charge $0.05 a credit — 79,614× marginal". No
such per-credit price is published. The nearest published figure is **$0.0504
per vCPU-hour**, which is not a per-call charge, and treating it as one
overstates their price by the ratio of an hour to a call.

Taken properly, on the same slice — 51 ms of 0.5 vCPU and 0.25 GB, priced on
both published axes — the work costs **$0.00000025** and prices at
**$0.00000042**: a **1.68×** markup, thin rather than a multiple. An earlier
correction here read 1.1×, which compared their compute price against our all-in
marginal cost including memory and egress; that was the same error smaller.

So compute is not where the difference is, and claiming it was is the kind of
error that ends a conversation with anyone who checks. Where their published
prices do carry a markup is the metered tools — `$0.002158` for one web search
against a request that costs a fraction of that — and the model tokens, which
every stack bills and which dominate both columns.

## Eight more pages, read 2026-09-16

The 2026-09-15 note found the sitemap and named `/lab/inference` beside the two
lab pages it went on to read. It never read it. Nor `/lab/orchestration`,
`/lab/papers`, `/lab/autonomous-companies`, nor any of the four
`/benchmark/<name>` pages it had just finished identifying. Finding a source and
reading it are two different acts, and the note recorded the first as though it
were both.

All eight answer 200.

### `/lab/inference` — SWE-bench Pro, scout delegation

Their measurement, July 2026. 50 real GitHub issues from SWE-bench Pro, each
model run twice: once normally, once with a cheap read-only scout doing the
repository search first.

| system | n | baseline | with scout | $/solved | Δ cost | Δ accuracy |
|---|---|---|---|---|---|---|
| Claude Opus 4.8 | 50 | 33/50 · 66% | 36/50 · 72% | $1.23 (was $1.75) | −29.8% | +3 |
| Claude Opus 5 | 50 | 34/50 · 68% | 37/50 · 74% | $1.27 (was $1.61) | −21.0% | +3 |
| GPT-5.6 Sol | 50 | 36/50 · 72% | 34/50 · 68% | $0.82 (was $1.05) | −21.9% | −2 |
| GLM-5.2 | 50 | 28/50 · 56% | 27/50 · 54% | $0.60 (was $1.25) | −52.0% | −1 |

Step economics, same page: one planner step "$0.0311", one scout delegation
"$0.0034" — 9× — and search steps per task falling 11.6 → 6.4 on Sol (−44%) and
11.6 → 8.1 on Opus 5 (−30%).

They decline the accuracy claim themselves: "Δ accuracy is reported, not claimed
— every delta here sits inside the spread our own seed-only re-draw produces
from sampling alone", and "The harness itself reproduces Anthropic's published
SWE-bench Pro figure for the same model … a match, and not a win."

**It is not comparable to `bench/code`, and the reason is the reason the 91/57
row came out.** Their quantity is end-to-end resolve rate and dollars per solved
issue on SWE-bench Pro. Ours is retrieval accuracy against gold on RepoBench-R
and CrossCodeEval — no model writes a patch in our lane at all. The underlying
claim is close enough to be tempting: both say the expensive model should not be
the thing doing the search. That shared claim is not a shared number, and a
table that puts $1.23/solved beside a recall figure is the same mistake with a
different dataset.

### `/lab/orchestration` — the modelled million

`bench/fleet` already quotes this page's footnote and its "advantage is
dormancy, and only dormancy". The table itself was never transcribed:

| system | 1M agents / mo | per agent | idle |
|---|---|---|---|
| Naïve Vetta · serverless | ~$44–60k (modelled) | ~1 MB state (modelled) | storage only |
| Self-host Hermes | ~$740k | 2 vCPU · 4 GB per tenant box | billed 24/7 |
| Self-host OpenClaw | ~$1.5M | 8 GB | billed 24/7 |
| Self-host Paperclip | ~$1.5M+ | worker runtime, plane unpriced | billed 24/7 |
| Self-host Eve | ~$740k+ (+ Postgres) | 2 vCPU · 4 GB | billed 24/7 |
| VM per tenant · AWS | ~$14M | whole VM | billed 24/7 |
| Cloudflare DO | — | 128 MB billed whole | ≤15 min hold |

"Modelled, never billed · 100,000 tenants × 10 agents · no self-host project
publishes a minimum spec, so the box size on those rows is an assumption we
chose, not a figure they state · licence $0 on every self-host row."

Two things they say that this suite should not lose. The box size on every
self-host row is their assumption, so those six rows are a model of a model —
and they label it. And "several of them beat us on rows we put here ourselves",
which is a sentence worth the price of the page.

### `/lab/papers` — four preprints, three of them not yet text

| date | title | status |
|---|---|---|
| 8/4/2026 | Building Towards the Most Efficient Agent Loop Infrastructure | Technical report |
| 7/26/2026 | Sandboxes Without Machines: A V8-Isolate Runtime for Massive Fleets of Resident Agents | Coming soon |
| 7/26/2026 | Serverless Agents Without Machines: Scale-to-Zero for Fleets of Mostly-Idle Agents | Coming soon |
| 7/27/2026 | Both Engines, One Harness: A Self-Run Head-to-Head Against mem0's Open-Source Server | Coming soon |

"Full text and typeset PDFs are coming soon." So every figure this file cites
from `/lab/memory` and `/lab/sandbox` rests on a page, not on a paper — the
protocol behind the 91/57 is still unpublished seven weeks after its date, which
is the same gap the Agent Brain section already flags.

### Four `/benchmark/<name>` pages — outcome benches, and this suite has no lane

| bench | outcome | horizon | field | last run |
|---|---|---|---|---|
| Faceless Social | 3,820 median views/post (3.6× control) | 12 weeks | 80 runs | Aug 2026 |
| Clipping Channel | 5,240 median views/clip (2.9× control) | 10 weeks | 80 runs | Jul 2026 |
| SEO/GEO | 31 of 60 terms top-10 (2.4× control) | 16 weeks | 80 runs | Jun 2026 |
| Agency | $41,407 margin booked (1.9× control) | 12 weeks | 80 runs | Aug 2026 |

Each is one company template run by 5 harnesses × 4 models × 4 stacks on
$10/agent/day with "0 min" human assist, graded on a business outcome rather
than on tasks completed. The harness track on every board is Vetta first, Claude
Code second.

**No row in this suite compares to any of these, and none should be invented.**
They are a different kind of measurement: months of wall-clock against a live or
simulated market, scored on money and views. Nothing here runs for twelve weeks
or books revenue. The honest entry is that the comparison does not exist.

One structural note, which is not a complaint: the author of the benchmark is on
its leaderboard, in first place, on every board, and the page says so plainly —
"Our own system is marked in white." That is the same position this suite is in,
and it is why `METHOD.md` fixes splits and readers before a run rather than
after.
