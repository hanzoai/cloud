# LLM.md — apps/agents

Guidance for apps/agents. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Built-in agents are Hanzo's team, nothing else (`apps/agents/persona.go`)

The built-in crew is `house.Team()` from the `hanzoai/personas` pin in go.mod
(v1.3.2: 24 seats, team/*.md), and no built-in is defined in cloud. `SeedPersonas`
creates each missing handle and marks it in `agent_personas`; every store open
(`migratePersonas`) rewrites a marked row's title and brief to the source, and an
edit through PATCH drops the mark, so the org keeps its edit.

A seat carries its member's role skills (`skills:` in team/<id>.md, names from
`apps/skills` builtin.go) in its tools as `skill_<name>`, which is how `withSkills`
reads them into a run: a new seat is created with them, and `Store.equip` on every
open adds a skill the source added and removes one it dropped, against
`agent_personas.skills`, the set last carried, so a skill the org took out stays
out and the org's own tools are never touched. `apps/skills` `TestTheTeamsSkillsAreServed`
fails a member naming a skill the catalogue does not serve.

A marked one-shot row whose member is not in the source is `status: retired`
(`Store.retire`): left out of `GET /v1/agent` and the `agent_*` tool plane, carried
by the roster as retired so Team draws an inactive member that keeps its past
messages, and still readable by handle. Its row, runs and spend stay. It is ready
again when its member returns or when the org PATCHes its brief. A long-running
seat is never retired: it acts on its own, and hiding it would hide spend.
`earlier` holds pre-personas titles and sums for current members only, because
`seated` reads it for a crew member. Moving the pin moves `roster` in
persona_test.go.

## A run says how far along it is, and says that it is a GUESS (`apps/agents/progress.go`)

A session's `status` is what the SURFACE reported — running, paused, done, error
— and "running" is equally true of a run three turns in and of one that has been
waiting forty minutes for an approval nobody will give. Both render the same on a
board, so a human scanning fifty runs cannot tell which one needs them. The
difference between those two is not in the row; it is in the transcript, which is
prose, which is what a model reads.

So `sessionView.progress` is ESTIMATED on `cloud.DefaultModel` from the run's own
goal and the tail of its log, and it rides every session read — list, detail,
tree, register, patch — plus one typed op of its own, `GET
/v1/agents/sessions/{id}/progress`, which is the same value and the one address
that WAITS for a fresh reading. The wire is `{pct?, phase, activity?, at?,
estimated}`, one `sessionProgress` component `$ref`d from both.

- **`estimated` is the whole design, not a caveat on it.** A fabricated 90% that
  reads as a measurement tells a human to leave a stuck run alone. It is true
  only where a model produced the number; a TERMINAL session reports the row's
  own word and is marked false — a finished run is 100% because it finished.
- **`pct` IS ABSENT when progress is indeterminate**, and the omission is the
  mechanism rather than a convention: a Go zero and a missing key decode
  identically, so `sessionProgress.Pct` is a `*int` and the tests assert on the
  BYTES. `CreateSession` writes the -1 sentinel itself, so the law holds by
  construction instead of by every register path remembering.
- **`blocked` is what earns the field.** It is the one state the surface running
  an agent will never report, because a run that is stuck does not know it is.
  `done` from a model on a still-open run is the other half: a run somebody
  forgot to close. `error` only ever comes from the row.
- **Two terms decide a re-estimate and they bound different things**: the
  interval (30s) bounds COST, the transcript (`UpdatedAt > ProgressAt`) bounds
  WASTE — re-reading an unchanged log returns the same answer from the same
  input, which is what makes a fleet of mostly-idle runs nearly free. `progress_at`
  is the clock AND the stamp, so `SetProgress` is its only writer.
  **The test that covers this passed with the interval term DELETED** until
  `setClocks` existed: the stamps are second-granular, so inside one test both
  terms are true together and either alone satisfies it. Move one clock at a time
  or the invariant is declared and unasserted.
- **An estimate is only ever replaced by another estimate.** An outage or an
  unreadable reply advances the clock — not doing so would retry on every poll,
  exactly when the gateway is already unwell — and leaves the last good answer
  standing, whose growing age is published as `at`.
- The reply parser is FORGIVING about prose around the object and about a `pct`
  it cannot use, and STRICT about the phase: a word outside the closed four is
  refused whole, because treating an unknown one as "running" publishes a state
  nobody chose. A bad `pct` costs its own field only, so a `blocked` is never
  lost to a number.
- Cost is bounded three ways with no sweep of its own: the clock, a single-flight
  claim per run, and a process-wide ceiling of 8 in flight — the in-flight map
  cannot outgrow the ceiling and every claim is released, so nothing reclaims it.
  The completion is charged to the wallet the session already named
  (`Payer`, else the org); an empty billing scope would make it EXEMPT from the
  meter, and a board makes a lot of these.
- **There is no todo parser and there must not be one.** A run that keeps a
  checklist writes it as a tool call whose payload shape belongs to whoever wrote
  it, and this surface has never interpreted a payload. The turns are handed over
  as they are; reading a checklist out of prose is the one thing the model is
  unambiguously better at than a regexp. Payloads are already safe to forward —
  `guardEvent` refuses a credential at the append.
- `Store.TailEvents` exists because `ListEvents` pages FORWARD from a cursor, so
  asking it for twenty turns with no cursor answers with a run's FIRST twenty —
  the opposite end of the log from the question. A short run cannot see the
  difference, which is why the test that catches it is long.

### The run's own word outranks the guess (`KindProgress`)

An estimate is what you fall back to when the run will not say. A run that KNOWS
how far along it is should say so, and most agent harnesses do — they keep a
checklist. So a run REPORTS by appending a `progress` turn to its own transcript:

    POST /v1/agents/sessions/{id}/events
    {"kind":"progress","payload":{"pct":60,"phase":"running","activity":"…"}}

**It is an event KIND rather than a route of its own, and that is the whole
design.** Progress is something that HAPPENED in the run, and the transcript is
where what happened is recorded — so the append route already carries every
property a second route would have had to restate: typed and `x-tool`, org-scoped
by the same `tenantStore`, credential-scanned by the same `guardEvent`,
size-bounded, sequenced, and FANNED OUT to every live subscriber. It also makes
the reports DURABLE HISTORY: how a run's own sense of its progress moved is
replayable, which an estimate overwritten in place never was.

- **`estimated` is the discriminator and there is no second field.** A report
  writes the same four columns marked NOT estimated; the wire is unchanged in
  shape and gains only a value it could always have carried. The column says who
  produced the VALUE.
- **Live, without polling.** The append publishes a SESSION frame beside the
  event frame, and `sessionView` already carries `progress` — so a subscriber's
  existing handler moves the bar with nothing new to parse. A second event-shaped
  payload would have made every client parse the same fact twice.
- **The RACE is what needed code**: an estimate is formed over a transcript read
  seconds earlier, so it can land after a report that supersedes it.
  `Store.SetEstimate` is therefore a compare-and-set on `progress_at` — the shape
  `AdvanceSession` already uses — and `SetProgress` stays unconditional because
  the run's own word wins at the instant it is written. Losing the CAS costs that
  estimate and nothing else.
- **`SetEstimate` writes p's OWN provenance, never a literal 1**, and this was a
  live bug in the first draft: the estimator also calls it to KEEP a value it
  could not replace, so stamping "estimated" there re-labelled the run's own
  report as a guess the first time the gateway hiccuped — the honesty law running
  backwards. Found by reading a live run's third measurement, not by a test;
  `TestAnOutageDoesNotRelabelAReportAsAGuess` gates it now.
- **The payload is the ONE this surface reads, and it is refused rather than
  salvaged.** Every other kind's body belongs to whoever wrote it. A `progress`
  body is OURS — published in the append op's own prose — so parsing it is
  reading our own contract, and a malformed one is a 400 that stores NO turn: a
  run must not believe it reported. That is the opposite of the model parser,
  which salvages what it can, and the asymmetry is deliberate — a model is a text
  generator whose output we rescue, a client is a caller holding a contract.
- `pct` is optional on a report, so a run that knows it is BLOCKED and does not
  know how far along it is says the half it knows — and the wire still carries no
  `pct`, which is the unknown-never-zero law holding on the ground-truth path too.

## A turn is served by one model (`apps/agents/pin.go`, #802)

A run's tool loop is one turn of many completions, and each round goes through
`completeWithFailover` (the agent's model, then `aimodel.FallbackModel` when that
model is busy, refuses, or runs out of time). Every round used to start from the
agent's model, so a round the fallback answered was followed by one on the agent's
model again: one tool sequence, two models. `turnModel` keeps one:

- The model that answers the first round holds the turn; every later round asks it
  first and the other model only when it cannot answer. A first round the fallback
  answered is the fallback's turn, not a switch.
- A later round another model answers is a switch. That model is shown a fresh
  reasoning context (`freshContext`: what the turn said and did, without reasoning a
  model wrote inline at the head of its content) and keeps the turn from then on.
- The `agent.step` span carries `hanzo.agent.turn.model` and
  `hanzo.agent.turn.switches`, and one `model switch` event per switch with
  `hanzo.agent.turn.from`, `.to`, `.round` and `.fresh_context`.

Hidden reasoning (`reasoning_content`, signed thinking blocks) never enters these
transcripts, because `sdk/infer` reads only content and tool calls. `shown` is the
one place that decides what a model other than the turn's reads; a capability record
saying two models share reasoning (#801) belongs there. The same rule for Zen and
Enso arms inside one request is hanzo-inc/zen's (`walk`, `sticks`), not this package's.

## A web run works only in a folder the machine's owner granted (`apps/agents/folders.go`, hanzo-inc/app#310)

- `agent_target_folders` holds grants `{org, target_id, folder, granted_by, granted_at, revoked_at}`.
  A folder is absolute, lexically clean, and refused with a `..` segment (`cleanFolder`).
  `Granted` is containment: `/a/b` is under `/a`, `/ab` is not.
- `POST|GET|DELETE /v1/agent/targets/:id/folders` (`?folder=` on DELETE). Grant and revoke
  are the machine's owner's alone (`owner`, not `targetOwns`: an org admin is 403 too);
  anyone who reaches the machine lists.
- The target view carries `folders` (paths), so a daemon's heartbeat answer is how it learns a
  revoke. A claimed run carries `dir`.
- A run with `dir` is checked at Start (`TargetDispatchable`, 403 `folder_not_granted`), at
  dispatch, at the queue (`Admit`, which audits `machine.folder.run`), and at claim (a run in a
  folder revoked while queued is reported failed, never handed out).
- Audit actions: `machine.folder.grant`, `.revoke`, `.run`, `.deny`; resource `machine/<id>`,
  `after` = `{machine, folder, run}`.

## A machine's terminal is on the machine (`apps/agents/targetterminal.go`, 2026-11-27)

`hanzo link` published a laptop's shell on a SESSION (agent `tmux`, `host`, no target). The
reaper rightly ends a session with no activity for an hour (`reap.go`), and a link never
appends events, so about an hour in the machine's terminal vanished from Hanzo Tabs while the
shell was still live (production, 2026-11-26). The terminal is a fact about the machine, and
the machine already beats, so it now rides the beat.

- The run-target row has `terminal` and `terminal_at`. `POST /v1/agent/targets` and
  `PATCH /v1/agent/targets/:id` take an optional `terminal`: absent leaves both columns
  alone (a `hanzo dev` beat never wipes a link's), `""` clears, a URL is checked by
  `sessionTerminal` (https only, 512 bytes; the one validator) and stamped from the server
  clock.
- `Target.LiveTerminal(now)` is the reading: the stored URL only while the machine is online
  and `terminal_at` is inside `LiveWindow`. One window, no new knob. A link therefore
  restates its terminal on every beat.
- `targetView.terminal` carries it only for a caller who `targetOwns` the machine. A
  colleague's machine never shows it.
- Session `terminal`, the reaper, the meter and `Store.End` are untouched: older CLIs still
  publish on a session, and a reader keeps that as its fallback.
- `/v1/agents/targets` (plural) is the same route: the router rewrites the other number of a
  capability's name (`manifest.Normalize`).
- Pinned by `targetterminal_test.go`, including `TestAReapedLinkSessionLeavesTheMachineItsTerminal`.
