# market

Every other lane here grades against an answer key. This one grades against
what happened.

A bench in `brain/` asks whether a system returns the turn an annotator marked.
A bench here asks whether a company template, run for real by an agent on a
fixed daily budget, produced anything the world accepted — a change a maintainer
merged, a thread that stayed closed, a page that ranked. The answer key is
somebody else's decision, taken after the fact, which is the only kind of grade
an agent cannot study for.

**Nothing here has been run. There are no results in this directory and there
will be none until a live run finishes.** What is here is the harness, four
definitions, and a dry mode that exercises the whole thing without money.

```
node bench.mjs                                   # every bench, validated
node bench.mjs code-desk                         # one of them
node run.mjs --bench=code-desk --arm=all --dry --now=2026-09-10
node --test 'test/*.test.mjs'                   # or make test
```

## What is defined

| bench | status | outcome | duration | budget |
|---|---|---|---|---|
| `code-desk` | ready | changes merged | 28 days | $10/arm/day |
| `support-desk` | soon | threads resolved | 28 days | $10/arm/day |
| `search-desk` | soon | top-ten rankings | 12 weeks | $70/arm/week |
| `social-desk` | soon | views | 84 days | $10/arm/day |

**Ready** means the harness can run it and grade it today. **Soon** means it
cannot — the outcome has no reader, so a number would have to be typed in by
hand, and a number typed in by hand is not a measurement. Every bench also
carries `decisions`: what a human has to settle before a live run. Those are
listed in the file, in plain words, because they are the real reason three of
these four are not running.

The one that carries the most weight: **automated posting is against the terms
of several of the platforms `social-desk` would be interesting on**, and the
rest allow it only through a paid API tier that caps the cadence. Which platform
under which terms decides whether that bench can exist at all. It is a decision
for somebody who can accept terms on the org's behalf, not a configuration.

## How a run works

A run is one arm of one bench, and it is not a process that stays up for four
weeks. Each invocation does what the calendar has made due — dispatch the
current period's tasks, close every period whose read window has passed — writes
it down, and exits. Run it daily from cron. Run it twice in one day and the
second does nothing.

The budget belongs to the platform. Each arm is an agent carrying
`cap_micro_usd` for the period and `max_task_micro_usd` for the task, and every
model call it makes is checked against both before it is made
(`apps/agents/budget.go`). The runner does not enforce a ceiling of its own,
because a second opinion about money is how two ledgers disagree. A dispatch the
platform refuses comes back refused, is written down as refused, and lowers the
arm's completion — which is the whole point: an arm that spends its day on the
first task skips the rest and pays for it in a column, not a footnote.

The record goes to `/v1/research` — the benchmark definition, the study it
belongs to, and the run with one measure per period and one per total. There is
no second place results are published from. `completion` there is derived from
`questions`, `answered` and `ended`, and this runner writes `ended` only when
the last period's read window has closed, so a run in progress cannot be read as
a finished one.

## Why the leaderboards are worth reading

A board varies one thing. A model track holds the harness and the stack, a
stack track holds the harness and the model, and an open track varies anything
and says so. That is not a promise in the prose: a track declares what it varies
and what it holds, and a definition whose track covers two arms differing in more
than one axis is **refused at load** — so a board that cannot attribute its own
ordering never gets published. `bench.mjs` does the refusing and
`test/market.test.mjs` pins it.

`code-desk` today has a model track and a stack track and **no harness track**,
because running another vendor's harness needs that vendor's credential under
that vendor's terms. An absent board is the honest form of an unrun comparison.

Every bench declares its control — a named arm, and the reason it is the right
zero. The control's run is recorded with `baseline: true`, which is the flag
`/v1/research` already has for exactly this, and a headline stated as a multiple
is a multiple of that arm.

## The dry run

`--dry` swaps the platform for `local.mjs`, which answers the same seven
operations from arithmetic: no network, no model, no money, no credential. It
ticks the clock through every period of the bench in one process, so
twenty-eight days of dispatches and thirty-five days of read windows happen in a
few milliseconds and every branch is taken.

It proves the harness — that the periods advance, that a period stays open until
its window closes, that a refusal lowers completion, that a day nobody ran the
runner is written down as missed rather than forgotten, that a run is never
recorded as finished early. It proves nothing about the platform: the budget
arithmetic in `local.mjs` mirrors `apps/agents/budget.go` and a mirror is not
the thing. Where the two disagree the platform is right.

Dry runs write to `runs-dry/`, which is gitignored, and are never sent to
`/v1/research`.

## Layout

| path | what it is |
|---|---|
| `benches/*.json` | the definitions — a bench is data, and this is all of it |
| `bench.mjs` | load, validate, refuse; and what a definition implies but does not state |
| `run.mjs` | the runner: periods, dispatch, the read window, the record |
| `grade.mjs` | the readers — how an outcome is read and what makes a task complete |
| `cloud.mjs` | the live platform, seven operations over `api.hanzo.ai` |
| `local.mjs` | the same seven, from arithmetic, for a dry run |
| `test/` | the fixture bench and what the lane claims, checked |
| `METHOD.md` | the protocol: the format field by field, and what a number here means |
