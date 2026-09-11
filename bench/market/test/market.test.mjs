/**
 * What the lane claims, checked: `make test` here, or
 * `node --test 'bench/market/test/*.test.mjs'` from the root.
 *
 * Nothing here reaches the network, spends anything or needs a credential: the
 * dry platform answers the same seven operations from arithmetic, so the whole
 * state machine — periods, the read window, refusals, missed days, the record —
 * runs in a few milliseconds.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { all, load, loadFrom, validate, armsOf, axes, periodStart } from '../bench.mjs'
import { readers } from '../grade.mjs'
import * as local from '../local.mjs'
import { advanceRun, records } from '../run.mjs'

const fixture = () => loadFrom('bench/market/test/tight-desk.json')
const clone = (d) => JSON.parse(JSON.stringify(d))
const dir = () => mkdtempSync(join(tmpdir(), 'market-'))
const lines = (p) => existsSync(p) ? readFileSync(p, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l)) : []
const DAY = 86400000

/** Drive a run through `ticks` periods from `zero`, skipping the ones in `skip`. */
async function drive(bench, arm, { ticks, skip = [], seed = 't', to } = {}) {
  const zero = Date.UTC(2026, 0, 1)
  let now = zero
  const platform = local.open({ seed, clock: () => now })
  const out = join(to ?? dir(), 'run')
  let last
  for (let n = 0; n <= ticks; n++) {
    if (skip.includes(n)) continue
    now = zero + n * DAY
    last = await advanceRun({ bench, arm, platform, now, dir: out })
  }
  return { ...last, dir: out, platform }
}

test('every definition on disk validates, and the ready ones can be graded', () => {
  const benches = all()
  assert.ok(benches.length >= 4, 'there are benches')
  for (const b of benches) {
    assert.ok(b.arms.some((a) => a.id === b.control.arm), `${b.id}: the control is an arm`)
    if (b.status === 'ready') assert.ok(readers[b.outcome.reader], `${b.id}: ready, so it has a reader`)
    for (const t of b.tracks ?? []) {
      const arms = armsOf(b, t)
      if (t.vary === 'any') continue
      for (const held of axes.filter((a) => a !== t.vary)) {
        assert.equal(new Set(arms.map((a) => a[held])).size, 1, `${b.id}/${t.id}: ${held} is held`)
      }
      assert.equal(new Set(arms.map((a) => a[t.vary])).size, arms.length, `${b.id}/${t.id}: every arm differs in ${t.vary}`)
    }
  }
})

test('a track that varies two things at once is refused', () => {
  const d = clone(fixture())
  // b-context differs from a-context in the model; call the stack track's hold
  // the one that covers both and the track now varies model AND stack.
  d.arms.push({ id: 'b-plain', harness: 'h', model: 'm2', stack: 'plain' })
  d.tracks = [{ id: 'stack', vary: 'stack', hold: { harness: 'h', model: 'm1' } }, { id: 'bad', vary: 'stack', hold: { harness: 'h' } }]
  assert.throws(() => validate('tight-desk', d), /must hold model/)
})

test('a track that ranks one arm is refused', () => {
  const d = clone(fixture())
  d.tracks = [{ id: 'lonely', vary: 'model', hold: { harness: 'h', stack: 'plain' } }]
  assert.throws(() => validate('tight-desk', d), /covers 1 arm/)
})

test('a control that is not an arm is refused, and so is one with no reason', () => {
  const d = clone(fixture())
  d.control = { arm: 'nobody', why: 'x' }
  assert.throws(() => validate('tight-desk', d), /not an arm/)
  d.control = { arm: 'a-plain', why: '' }
  assert.throws(() => validate('tight-desk', d), /why it is the right zero/)
})

test('ready without a reader is refused; soon without one is not', () => {
  const d = clone(fixture())
  d.outcome.reader = 'nothing-reads-this'
  assert.throws(() => validate('tight-desk', d), /no reader called/)
  d.status = 'soon'
  validate('tight-desk', d)
})

test('a budget bigger than the period cap is refused, as the platform refuses it', () => {
  const d = clone(fixture())
  d.budget.max_task_micro_usd = d.budget.cap_micro_usd + 1
  assert.throws(() => validate('tight-desk', d), /cannot exceed the period cap/)
})

test('the period cap refuses the tasks it cannot pay for, and they lower the grade', async () => {
  const bench = fixture()
  const { metrics, tasks } = await drive(bench, bench.arms[0], { ticks: bench.span })
  const refused = tasks.filter((t) => t.refused === 'budget_exceeded')
  assert.ok(refused.length > 0, 'eight tasks at a two-task cap must refuse some')
  assert.equal(refused.every((t) => t.micro_usd === 0), true, 'a refused task costs nothing')

  // A refused task was covered by the run — the budget is the experiment — but it
  // is not complete, which is where the arm pays for it.
  assert.equal(metrics.answered, bench.tasks)
  assert.ok(metrics.complete < bench.tasks)
  const spent = metrics.measures.find((m) => m.category === 'all' && m.metric === 'spend_micro_usd').value
  assert.ok(spent <= bench.budget.cap_micro_usd * bench.duration.periods, 'no period spent past its cap')
})

test('a period stays open until its window closes, and the run until the last one does', async () => {
  const bench = fixture()
  const zero = Date.UTC(2026, 0, 1)
  const platform = local.open({ seed: 'w', clock: () => now })
  const out = join(dir(), 'run')
  let now = zero
  for (let n = 0; n <= bench.span; n++) {
    now = zero + n * DAY
    const { metrics } = await advanceRun({ bench, arm: bench.arms[0], platform, now, dir: out })
    // Period p is read one window after it ends: at tick n, periods 0..n-window-1 are closed.
    assert.equal(metrics.periods.closed, Math.max(0, Math.min(bench.duration.periods, n - bench.outcome.window_periods)))
    if (n < bench.span) assert.equal(metrics.ended, null, `tick ${n}: nothing has ended`)
  }
  const { metrics } = await advanceRun({ bench, arm: bench.arms[0], platform, now, dir: out })
  assert.equal(metrics.periods.closed, bench.duration.periods)
  assert.ok(metrics.ended, 'the last window closed, so the run ended')
})

test('an unfinished run is never recorded as finished', async () => {
  const bench = fixture()
  const mid = await drive(bench, bench.arms[0], { ticks: 2 })
  const r = records({ bench, arm: bench.arms[0], ...mid })
  assert.equal(r.run.ended, '', 'no end date while periods are unread')
  assert.ok(r.run.answered < r.run.questions, 'coverage is short, which /v1/research reads as partial')
  assert.equal(r.study.finding, '', 'a finding before the runs finish is the failure the record exists to catch')

  const end = await drive(bench, bench.arms[0], { ticks: bench.span, to: mid.dir.replace(/\/run$/, '') })
  const done = records({ bench, arm: bench.arms[0], ...end })
  assert.ok(done.run.ended, 'the finished run says when')
  assert.equal(done.run.answered, done.run.questions, 'and covered everything it planned')
})

test('a day nobody ran the runner is missed, not forgotten', async () => {
  const bench = fixture()
  const { metrics, tasks } = await drive(bench, bench.arms[0], { ticks: bench.span, skip: [1] })
  const missed = tasks.filter((t) => t.refused === 'missed')
  assert.equal(missed.length, bench.cadence.tasks_per_period, 'the skipped period is written missed, in full')
  assert.equal(metrics.questions, bench.tasks, 'the denominator is the bench itself, not what was attempted')
  assert.ok(metrics.answered < metrics.questions, 'coverage is short, so /v1/research calls the run partial')
  assert.equal(metrics.measures.find((m) => m.metric === 'tasks_missed').value, bench.cadence.tasks_per_period)
})

test('a surface with no work in it is the queue\'s gap, not the arm\'s', async () => {
  const bench = fixture()
  const zero = Date.UTC(2026, 0, 1)
  let now = zero
  const inner = local.open({ seed: 'q', clock: () => now })
  // Two tasks a period on offer against a cadence of eight.
  const platform = { ...inner, work: async (b, p, n, taken) => (await inner.work(b, p, n, taken)).slice(0, 2) }
  const out = join(dir(), 'run')
  let metrics
  for (let n = 0; n <= bench.span; n++) { now = zero + n * DAY; ({ metrics } = await advanceRun({ bench, arm: bench.arms[0], platform, now, dir: out })) }

  const empty = metrics.measures.find((m) => m.metric === 'tasks_queue_empty').value
  assert.equal(empty, (bench.cadence.tasks_per_period - 2) * bench.duration.periods)
  assert.equal(metrics.measures.find((m) => m.metric === 'tasks_missed').value, 0, 'the runner ran every day')
  assert.equal(metrics.answered, 2 * bench.duration.periods, 'coverage is what the queue supplied')
  assert.ok(metrics.answered < metrics.questions, 'so /v1/research reads the run partial, and the measure says why')
})

test('the control is the only arm marked baseline', async () => {
  const bench = fixture()
  for (const arm of bench.arms) {
    const out = await drive(bench, arm, { ticks: bench.span })
    const r = records({ bench, arm, ...out })
    assert.equal(r.run.baseline, arm.id === bench.control.arm, `${arm.id}`)
    assert.equal(r.run.id, `${bench.id}-${arm.id}`)
    assert.equal(r.run.benchmark, bench.id)
    assert.equal(r.benchmark.version, bench.digest, 'the definition version is its digest')
  }
})

test('every period is recorded, not just the total', async () => {
  const bench = fixture()
  const { metrics } = await drive(bench, bench.arms[0], { ticks: bench.span })
  for (let p = 0; p < bench.duration.periods; p++) {
    assert.ok(metrics.measures.some((m) => m.category === `period-${p}` && m.metric === bench.outcome.metric), `period ${p} has its outcome`)
    assert.ok(metrics.measures.some((m) => m.category === `period-${p}` && m.metric === 'spend_micro_usd'), `period ${p} has its spend`)
  }
  const total = metrics.measures.find((m) => m.category === 'all' && m.metric === bench.outcome.metric).value
  const sum = metrics.measures.filter((m) => m.category.startsWith('period-') && m.metric === bench.outcome.metric).reduce((a, m) => a + m.value, 0)
  assert.equal(total, sum, 'the total is the periods')
})

test('two dry runs of one seed are the same run', async () => {
  const bench = fixture()
  const a = await drive(bench, bench.arms[1], { ticks: bench.span, seed: 'same' })
  const b = await drive(bench, bench.arms[1], { ticks: bench.span, seed: 'same' })
  const strip = (rows) => rows.map(({ when, ...r }) => r)
  assert.deepEqual(strip(lines(`${a.dir}/tasks.jsonl`)), strip(lines(`${b.dir}/tasks.jsonl`)))
  assert.deepEqual(lines(`${a.dir}/periods.jsonl`), lines(`${b.dir}/periods.jsonl`))
})

test('running twice in one period does nothing the second time', async () => {
  const bench = fixture()
  const once = await drive(bench, bench.arms[0], { ticks: 0 })
  const before = lines(`${once.dir}/tasks.jsonl`).length
  const platform = local.open({ seed: 't', clock: () => Date.UTC(2026, 0, 1) })
  await advanceRun({ bench, arm: bench.arms[0], platform, now: Date.UTC(2026, 0, 1), dir: once.dir })
  assert.equal(lines(`${once.dir}/tasks.jsonl`).length, before)
})

test('the budget period is the platform\'s calendar, not a rolling window', () => {
  const wed = Date.UTC(2026, 0, 7, 13, 30)
  assert.equal(periodStart(wed, 'day'), Date.UTC(2026, 0, 7))
  assert.equal(periodStart(wed, 'week'), Date.UTC(2026, 0, 5), 'weeks start Monday')
  assert.equal(periodStart(wed, 'month'), Date.UTC(2026, 0, 1))
})

test('the definition the shipped bench names is the one the reader reads', () => {
  const code = load('code-desk')
  assert.equal(code.status, 'ready')
  assert.equal(readers[code.outcome.reader].metric, code.outcome.metric)
  assert.equal(code.budget.cap_micro_usd, 10_000_000, 'ten dollars a day, as the field says')
  assert.ok(code.affordable >= code.cadence.tasks_per_period, 'the cap pays for the cadence it asks for')
  assert.ok(code.decisions.length > 0, 'and it says what a human still has to settle')
})
