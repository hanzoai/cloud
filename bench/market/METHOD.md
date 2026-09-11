# Method

What a bench in this lane is, what a number from it means, and what stops two
numbers from being comparable.

## A bench is a file

One JSON file in `benches/`, named by its id. `bench.mjs` refuses anything the
list below does not describe, so a definition that loads is a definition two
people can run and get comparable numbers from.

| field | what it fixes |
|---|---|
| `id`, `title` | the name, and it matches the filename |
| `status` | `ready` if the harness can run AND grade it; `soon` otherwise |
| `question` | what running it answers, in a sentence. It becomes the Study in `/v1/research` |
| `firm.brief` | the whole instruction the arm is given. It is the agent's system prompt, verbatim |
| `firm.deliverable` | what one task produces |
| `firm.surface` | where the work lands — the repository, the inbox, the site, the account |
| `firm.queue` | the selector the surface understands: a label on a repository, a state on an inbox. The rule is the lane's, stated below, and is the same for every bench |
| `duration` | how many periods, and whether a period is a day, a week or a month |
| `budget` | `cap_micro_usd` per period per arm, `max_task_micro_usd` per task. Integer micro-USD, the platform's unit |
| `cadence` | tasks per period |
| `tools` | what the arm may call. An empty list is not "everything"; it is refused |
| `assist_minutes` | the human help an arm gets. Zero is a value, not an absence |
| `outcome.metric`, `.unit` | the headline, named once |
| `outcome.reader` | the reader in `grade.mjs` that reads it. `ready` requires it to exist |
| `outcome.window_periods` | the read window |
| `completion` | what makes a task complete, in the reader's own terms |
| `arms` | every arm, each with its harness, model and stack |
| `tracks` | each board: the one axis it varies, and the axes it holds |
| `control` | the arm every other arm is read against, and why it is the right zero |
| `decisions` | what a human must settle before a live run: accounts, payment, terms |

Nothing in the file names a commit. The **run** records three things instead:
the repository's head, the definition's content digest, and the commit the
definition file last moved in. A live run refuses to start while the definition
differs from what is committed, because then no commit describes what ran.

## The queue

One rule, the same for every bench: a period's tasks are drawn from the surface
by the bench's selector, and **each item is taken once for the whole run**. The
runner tells the surface which ids it has already used rather than counting an
offset into it, because a real queue gains and loses items while a run is
running and an offset points somewhere different every day.

Slots the surface could not fill are written down as `queue_empty`, and they are
not coverage: the bench planned work that did not exist.

## The read window

The outcome of work done in period `p` is what the world did with it by the time
`window_periods` further periods have passed. Period `p` therefore stays open
until `p + window + 1` periods after the run began, and only then is it read and
written down. One number, two consequences: it sets when a period is graded, and
it sets when the run can end.

A run's last period is read `window` periods after the duration, so a
twenty-eight-day bench with a seven-day window takes thirty-five days.

## Coverage and completion are two different things

`/v1/research` derives `completion` from `questions`, `answered` and `ended`.
Those fields exist because a harness that checkpoints reports whatever it has,
and a checkpoint published as a result is indistinguishable from a finished run
unless the record carries the denominator. Here:

- **`questions`** is every task the bench planned: periods × cadence. It never
  moves.
- **`answered`** is **coverage** — how many of those tasks the run got to, in
  periods that have been read. A task the budget refused counts: the budget is
  the experiment, and an arm measured against its cap has been measured. A task
  nobody dispatched does **not** count, and the row says which of the two
  reasons it was: `missed`, because the runner was not invoked that day, or
  `queue_empty`, because the surface had no work in it.
- **`ended`** is written only when the last period's window has closed.

So `completion` answers "did the harness cover the bench", and an operator who
forgot to run it for a week cannot hide that.

The arm's own completion rate — how many tasks produced the deliverable — is a
**measure**, `tasks_complete`, and it belongs there because it is a result: an
arm that burns its cap on the first task of each day shows it in that column.
Folding it into coverage would make every honest finished run read as
unfinished.

Beside it are `tasks_refused`, `tasks_missed` and `tasks_queue_empty`, each
named for the reason in the task row it counts, so a short denominator always
says whose it was — the budget's, the operator's, or the queue's.

## The budget

Each arm is one agent carrying the bench's `cap_micro_usd` for the period and
`max_task_micro_usd` for the task. Every model call passes `afford` in
`apps/agents/budget.go` before it is made: the org's balance, the agent's period
cap, the task ceiling, the session's budget — in that order — and a breach
refuses the call with a 402. The period is calendar-aligned in UTC, and this
lane's periods align to the same boundaries so the bench's day and the budget's
day are one day.

The runner adds no ceiling. It records what the platform decided.

Two figures for money, and they are not the same figure. `spend_micro_usd` is
the run's total, summed from what each dispatch reported. `budget` in
`metrics.json` is the arm's cap and what the **current** period has drawn
against it, read from the ledger that enforced it.

## The control, and what a multiple means

Every bench names one arm as the control and says why it is the right zero. The
control runs the same firm under the same budget with the same cadence; what it
lacks is the thing under test. Its run is recorded with `baseline: true`, which
is the mark `/v1/research` already carries for this, and a headline stated as a
multiple is a multiple of that arm and of nothing else.

An outcome without a control is unreadable. Changes merged per day, threads
resolved, views — every one of them is a function of the month, the queue and
the subject as much as of the system. The ratio to a frozen arm run over the
same days is the only part that is about the system.

## One variable per board

A track declares the axis it varies and holds the other two. The arms it covers
are the arms matching what it holds, and `bench.mjs` refuses the definition
unless they differ in exactly the varied axis and nothing else. A track covering
fewer than two arms is refused too: a ranking of one is not a ranking.

This is checkable rather than promised, which is the difference between a
leaderboard and a table. A board that varies two things can order its rows but
cannot say what the ordering is about.

## Comparability

Two runs of this lane mean the same thing when they share the benchmark id, the
definition digest, the split, the duration, the budget and the cadence. The
definition digest is the strong one: it changes when any field changes, so two
runs at different digests ran different benches whatever they are called.

Two runs differing in more than one of harness, model and stack are a
comparison of a bundle, not of a variable, and no track will carry them.

A comparison against anybody else's published number is directional unless the
firm, the budget, the cadence, the outcome metric and the read window all match.
They rarely will — an outcome bench is a company, not a dataset — so the honest
form is to say what was run here and let a reader judge the distance.

## What is recorded, and where

One place: `/v1/research`, as three records.

- **ResearchBenchmark** — the task. Id, title, the definition digest as its
  version, the surface as its dataset, the metric, the split, and in its notes
  the completion rule, the read window, the budget and the control.
- **Study** — the question the bench exists to answer. Its `finding` stays empty
  until the runs are finished; a finding recorded before then is the failure
  that record exists to make visible.
- **Run** — one arm's execution, with one measure per `(category, metric)`:
  `all` for the whole run, `period-<n>` for each period, which is how a growth
  curve is read back without inventing a second shape for it.

The run directory keeps the same thing on disk — `meta.json`, `tasks.jsonl`,
`periods.jsonl`, `metrics.json` — so a run can be resumed, audited and re-posted
without the server.

Every dispatch also records the span of the org's audit trail it moved through,
so an action a run took is findable in `/v1/audit` by sequence rather than by
recollection.
