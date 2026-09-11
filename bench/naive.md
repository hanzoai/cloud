# What naïve publishes

Every figure this suite compares against, with where it came from and when it
was read. A comparison whose other column has no source is not a comparison.

Read 2026-09-11 from the pages in the sitemap at `usenaive.ai/sitemap.xml`.

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
| 91% single-hop, 57% multi-hop, LoCoMo | Agent Brain | **unsourced** |
| isolated-vm 2.79 ms cold start | Sandbox, Agent as goroutine | **unsourced** |
| isolated-vm 1.2 MB per agent | Agent as goroutine | **unsourced** |
| ~1 MB per dormant agent | Fleet residency — already marked *"their assumption"* | modelled, not theirs |
| "$0.05 a credit" | Pricing | **withdrawn** — see below |

## The pricing row was wrong, and the correction goes the other way

The Pricing section read "Naïve charge $0.05 a credit — 79,614× marginal". No
such per-credit price is published. The nearest published figure is **$0.0504
per vCPU-hour**, which is not a per-call charge, and treating it as one
overstates their price by the ratio of an hour to a call.

Taken properly: a call measured here at 51 ms of one vCPU is
`0.0504 × 51/3600/1000` = **$0.00000071** at their published rate, against
**$0.00000063** measured as our marginal cost. Their compute is priced at about
1.1× what the work costs — a thin markup, not a multiple.

So compute is not where the difference is, and claiming it was is the kind of
error that ends a conversation with anyone who checks. Where their published
prices do carry a markup is the metered tools — `$0.002158` for one web search
against a request that costs a fraction of that — and the model tokens, which
every stack bills and which dominate both columns.
