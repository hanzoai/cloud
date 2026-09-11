/**
 * A bench is a file. This reads one, refuses the ones that cannot mean the same
 * thing twice, and reports what the definition implies but does not state.
 *
 *   node bench.mjs           # every bench, validated, with what each implies
 *   node bench.mjs code-desk # one of them
 *
 * The refusals are the point. A benchmark whose arms differ in two things at
 * once produces a leaderboard that cannot attribute its own ordering, and a
 * benchmark whose control is not one of its arms has no zero. Both are caught
 * here rather than discovered in the results.
 */
import { readFileSync, readdirSync, existsSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { readers } from './grade.mjs'

const here = new URL('.', import.meta.url)
const root = new URL('../../', here)
const dir = new URL('./benches/', here)

/** The windows a budget resets on. The platform's set (apps/agents/budget.go), not a second one. */
export const periods = ['day', 'week', 'month']
/** The axes an arm varies along, and the only things a track may vary. */
export const axes = ['harness', 'model', 'stack']

// ── what a period is ─────────────────────────────────────────────────────────
//
// The platform resets an agent's cap on the calendar, in UTC, so a bench's
// periods are the same boundaries: the day the bench counts and the day the
// budget resets on have to be one day, or an arm gets two caps on one Tuesday.
// apps/agents/budget.go periodStart is what this mirrors.

/** The UTC instant the window holding `ms` began. */
export function periodStart(ms, period) {
  const t = new Date(ms)
  const [y, m, d] = [t.getUTCFullYear(), t.getUTCMonth(), t.getUTCDate()]
  if (period === 'month') return Date.UTC(y, m, 1)
  if (period === 'week') return Date.UTC(y, m, d - ((t.getUTCDay() + 6) % 7))
  return Date.UTC(y, m, d)
}

/** `n` periods after a period start. */
export function advance(ms, n, period) {
  if (period === 'month') { const t = new Date(ms); return Date.UTC(t.getUTCFullYear(), t.getUTCMonth() + n, 1) }
  return ms + n * (period === 'week' ? 7 : 1) * 86400000
}

/** How many whole periods have passed since `from`. */
export function elapsed(from, now, period) { let n = 0; while (advance(from, n + 1, period) <= now) n++; return n }

/** Every bench id on disk, in reading order. */
export const ids = () => readdirSync(dir).filter((f) => f.endsWith('.json')).map((f) => f.slice(0, -5)).sort()

class Refused extends Error {}
const refuse = (bench, why) => { throw new Refused(`${bench}: ${why}`) }

/**
 * load reads one definition, validates it, and returns it with the fields a
 * reader would otherwise have to work out: the digest two runs must share to be
 * comparable, the commit the file last moved in, how many tasks a period's cap
 * can actually pay for, and how long the run takes to finish including the last
 * period's read window.
 */
export const load = (id) => loadFrom(`bench/market/benches/${id}.json`)

/** The same, from any path in the repository — which is how a test holds a fixture. */
export function loadFrom(rel) {
  const path = new URL(rel, root)
  if (!existsSync(path)) refuse(rel, 'no such bench')
  const bytes = readFileSync(path)
  const def = JSON.parse(bytes.toString('utf8'))
  const id = rel.split('/').pop().replace(/\.json$/, '')
  validate(id, def)
  const t = def.cadence.tasks_per_period
  return {
    ...def,
    path: rel,
    digest: createHash('sha256').update(bytes).digest('hex').slice(0, 16),
    commit: commitOf(rel),
    dirty: dirty(rel),
    tasks: def.duration.periods * t,
    // A period buys whole tasks at the task ceiling. Below the cadence, the
    // remaining tasks of every period are refused by the platform and the run's
    // completion says so — which is a legal design and a terrible accident.
    affordable: Math.floor(def.budget.cap_micro_usd / def.budget.max_task_micro_usd),
    // The last period's outcome is not readable until its window closes, so a
    // run is this long whatever the duration says.
    span: def.duration.periods + def.outcome.window_periods,
  }
}

/** Every bench, loaded. */
export const all = () => ids().map(load)

/** validate is the whole contract. It throws on the first thing that cannot mean one thing. */
export function validate(id, d) {
  if (d.id !== id) refuse(id, `the file is named ${id} and the definition calls itself ${d.id}`)
  if (!['ready', 'soon'].includes(d.status)) refuse(id, `status is ready or soon, not ${d.status}`)
  if (!d.question?.trim()) refuse(id, 'a bench states the question running it answers')
  // A firm is what the arm is asked to be, what one task of it produces, where
  // the work lands, and how the period's work is chosen. Leave out the last and
  // two people running this bench draw different tasks from the same surface.
  for (const k of ['brief', 'deliverable', 'surface', 'queue']) if (!d.firm?.[k]?.trim()) refuse(id, `firm.${k} is required`)
  if (!(d.assist_minutes >= 0)) refuse(id, 'assist_minutes states the human help an arm gets, and zero is a value')

  if (!periods.includes(d.duration?.period)) refuse(id, `duration.period is one of ${periods.join(', ')}`)
  if (!(d.duration.periods >= 1)) refuse(id, 'duration.periods is at least 1')

  // The same three rules the platform enforces on an agent's budget. Stated here
  // so a definition is refused before an arm is created rather than at the first
  // dispatch, and stated the same way so there is one answer to what a legal
  // budget is.
  const b = d.budget ?? {}
  if (!(b.cap_micro_usd > 0)) refuse(id, 'budget.cap_micro_usd is a positive integer of micro-USD')
  if (!(b.max_task_micro_usd > 0)) refuse(id, 'budget.max_task_micro_usd is a positive integer of micro-USD')
  if (b.max_task_micro_usd > b.cap_micro_usd) refuse(id, 'budget.max_task_micro_usd cannot exceed the period cap')

  if (!(d.cadence?.tasks_per_period >= 1)) refuse(id, 'cadence.tasks_per_period is at least 1')
  if (!Array.isArray(d.tools) || !d.tools.length) refuse(id, 'tools lists what the arm may call; an empty list is not "everything"')

  const o = d.outcome ?? {}
  if (!o.metric?.trim() || !o.unit?.trim()) refuse(id, 'outcome names one metric and its unit')
  if (!o.reader?.trim()) refuse(id, 'outcome.reader names how the metric is read')
  // Ready means the harness can run it and grade it. A bench whose reader does
  // not exist is soon, whatever it calls itself.
  if (d.status === 'ready' && !readers[o.reader]) refuse(id, `status is ready and there is no reader called ${o.reader}`)
  if (readers[o.reader] && readers[o.reader].metric !== o.metric) refuse(id, `reader ${o.reader} reads ${readers[o.reader].metric}, not ${o.metric}`)
  if (!(o.window_periods >= 0)) refuse(id, 'outcome.window_periods is the read window, in periods, and is not negative')
  if (!d.completion?.trim()) refuse(id, 'completion says what makes a task complete')

  if (!Array.isArray(d.arms) || !d.arms.length) refuse(id, 'a bench has at least one arm')
  const seen = new Set()
  for (const a of d.arms) {
    if (!a.id?.trim()) refuse(id, 'every arm has an id')
    if (seen.has(a.id)) refuse(id, `two arms are called ${a.id}`)
    seen.add(a.id)
    for (const ax of axes) if (!a[ax]?.trim()) refuse(id, `arm ${a.id} does not say its ${ax}`)
  }

  if (!d.control?.arm) refuse(id, 'a bench declares the control every other arm is read against')
  if (!seen.has(d.control.arm)) refuse(id, `the control names ${d.control.arm}, which is not an arm`)
  if (!d.control.why?.trim()) refuse(id, 'the control says why it is the right zero')

  for (const t of d.tracks ?? []) track(id, d, t)

  if (!Array.isArray(d.decisions)) refuse(id, 'decisions lists what a human must settle before a live run, and may be empty')
  for (const x of d.decisions) if (!x.id || !x.what || !x.who) refuse(id, 'a decision names itself, what it is, and whose it is')
}

/**
 * track holds the one idea a leaderboard rests on: a board that varies one thing
 * can attribute its ordering to that thing, and a board that varies two cannot.
 * The arms a track covers are the ones matching its held axes, and they must
 * differ in exactly the axis it varies.
 */
function track(id, d, t) {
  if (!t.id?.trim()) refuse(id, 'every track has an id')
  if (t.vary !== 'any' && !axes.includes(t.vary)) refuse(id, `track ${t.id} varies one of ${axes.join(', ')}, or any`)
  if (t.vary === 'any') return
  const held = axes.filter((a) => a !== t.vary)
  for (const a of held) if (!t.hold?.[a]) refuse(id, `track ${t.id} varies ${t.vary} and must hold ${a}`)
  const arms = d.arms.filter((a) => held.every((x) => a[x] === t.hold[x]))
  if (arms.length < 2) refuse(id, `track ${t.id} covers ${arms.length} arm(s); a ranking of one is not a ranking`)
  const values = new Set(arms.map((a) => a[t.vary]))
  if (values.size !== arms.length) refuse(id, `track ${t.id} has two arms with the same ${t.vary}`)
}

/** The arms a track covers, in definition order. */
export const armsOf = (d, t) =>
  t.vary === 'any' ? d.arms : d.arms.filter((a) => axes.filter((x) => x !== t.vary).every((x) => a[x] === t.hold[x]))

/** One arm by id, or a refusal naming the ones there are. */
export function armOf(d, id) {
  const a = d.arms.find((x) => x.id === id)
  if (!a) refuse(d.id, `no arm ${id}; the arms are ${d.arms.map((x) => x.id).join(', ')}`)
  return a
}

// git is asked once per question per process. Nothing here changes while a run
// is running, and a definition loaded fifty times should not spawn a hundred
// processes to be told the same thing.
const asked = new Map()
const git = (...args) => {
  const k = args.join(' ')
  if (!asked.has(k)) {
    try { asked.set(k, execFileSync('git', args, { cwd: root.pathname, encoding: 'utf8' }).trim()) } catch { asked.set(k, '') }
  }
  return asked.get(k)
}
/** The commit a path last moved in, or empty where git cannot say. */
export const commitOf = (path) => git('log', '-1', '--format=%H', '--', path)
/** Whether a path differs from what is committed. A live run refuses on true. */
export const dirty = (path) => git('status', '--porcelain', '--', path) !== ''
/** The repository's head, recorded on every run. */
export const head = () => git('rev-parse', 'HEAD')

const usd = (m) => `$${(m / 1e6).toFixed(2)}`

if (import.meta.url === `file://${process.argv[1]}`) {
  const want = process.argv[2]
  for (const d of want ? [load(want)] : all()) {
    console.log(`${d.id} — ${d.title} [${d.status}]`)
    console.log(`  ${d.question}`)
    console.log(`  ${d.duration.periods} ${d.duration.period}s, ${d.cadence.tasks_per_period} tasks each = ${d.tasks} tasks; outcome read ${d.outcome.window_periods} ${d.duration.period}s later, so the run spans ${d.span}`)
    console.log(`  ${usd(d.budget.cap_micro_usd)} per ${d.duration.period} per arm, ${usd(d.budget.max_task_micro_usd)} per task — ${d.affordable} task(s) a ${d.duration.period} at the ceiling, cadence ${d.cadence.tasks_per_period}`)
    console.log(`  ${d.arms.length} arms, control ${d.control.arm}; tracks ${(d.tracks ?? []).map((t) => `${t.id}(${t.vary}, ${armsOf(d, t).length})`).join(' ') || 'none'}`)
    console.log(`  outcome ${d.outcome.metric} in ${d.outcome.unit}, read by ${d.outcome.reader}`)
    console.log(`  digest ${d.digest}, last moved in ${d.commit.slice(0, 12) || 'no commit'}${d.dirty ? ' (DIRTY — a live run refuses)' : ''}`)
    for (const x of d.decisions) console.log(`  decision ${x.id}: ${x.what} — ${x.who}`)
    console.log()
  }
}
