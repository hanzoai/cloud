# What naïve publishes

Every figure this suite compares against, with where it came from and when it
was read. A comparison whose other column has no source is not a comparison.

Read 2026-09-11 from the pages in the sitemap at `usenaive.ai/sitemap.xml`.

**Re-read 2026-09-15.** Every price below is unchanged. `usenaive.ai/benchmarks`
still answers 404, and the computer page still publishes no start-up, resume or
per-agent memory figure — so four days on there is still nothing of theirs to
compare a latency or an accuracy against, and any such claim of ours stands on our
own measurement alone. Four details the first read did not capture:

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
| isolated-vm 2.79 ms cold start | Sandbox, Agent as goroutine | **unsourced** — and measured here at 0.59 ms |
| isolated-vm 1.2 MB per agent | Agent as goroutine | **unsourced** — and measured here at 1.00 MiB, which is V8's floor |
| ~1 MB per dormant agent | Fleet residency — already marked *"their assumption"* | modelled, not theirs |
| "$0.05 a credit" | Pricing | **withdrawn** — see below |

The sandbox lane prints three more, about other vendors, and none of those is
sourced either. `docs.e2b.dev` carries no start-up time and no default memory on
the page checked 2026-09-11; Modal's and Cloudflare's were not traced to a page
at all.

| cited as | status |
|---|---|
| E2B <200 ms, 512 MB min | unsourced; not on the docs landing page |
| Modal ~1 s, 128 MB min | unsourced |
| Cloudflare 1–3 s, 256 MB min | unsourced |

A figure with a vendor's name on it and no page behind it is worth less than no
figure. Either source them or measure them, as the isolate row now is.

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
