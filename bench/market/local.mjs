/**
 * The dry platform: the same seven operations, answered from arithmetic.
 *
 * It exists so the harness can be run end to end — every period, every task,
 * every refusal, every record — without money, without a model, and without
 * touching anything outside this process. That is what `make test` runs and what
 * anyone reading this lane can run before believing any of it.
 *
 * WHAT A DRY RUN PROVES, AND WHAT IT DOES NOT. It proves the harness: that the
 * periods advance, that the read window holds a period open until it closes,
 * that a refused task lowers completion, that a run is never recorded as
 * finished before its last window. It proves nothing about the platform. The
 * budget arithmetic below MIRRORS apps/agents/budget.go — cap per period, ceiling
 * per task, reset on the calendar — and a mirror is not the thing: a live run is
 * refused by the platform's ledger, and a dry one by this file's copy of its
 * rules. Where the two disagree the platform is right.
 *
 * Everything is a function of (seed, bench, arm, period, index), so two dry runs
 * of the same bench produce the same bytes and a diff means a change in the
 * harness rather than in the weather.
 */
import { createHash } from 'node:crypto'
import { periodStart } from './bench.mjs'

/** A number in [0,1) from a string. The whole source of variation here. */
const frac = (...parts) => {
  const h = createHash('sha256').update(parts.join(' ')).digest()
  return h.readUInt32BE(0) / 2 ** 32
}

/**
 * open returns a dry platform. `clock` is the runner's own, so a run of
 * twenty-eight days finishes in a millisecond and the budget still resets on the
 * days it would have.
 */
export function open({ seed = 'dry', clock = () => Date.now() } = {}) {
  const agents = new Map()
  const changes = new Map()
  const records = []

  return {
    live: false,
    api: 'local',
    /** Everything the dry run recorded instead of posting, for a test to read. */
    records,

    async agent(bench, arm) {
      const ref = `market-${bench.id}-${arm.id}`
      const a = {
        ref, arm: arm.id, bench: bench.id,
        cap_micro_usd: bench.budget.cap_micro_usd,
        max_task_micro_usd: bench.budget.max_task_micro_usd,
        period: bench.duration.period,
        consumed: 0, started: periodStart(clock(), bench.duration.period),
      }
      agents.set(ref, agents.get(ref) ? { ...agents.get(ref), ...a, consumed: agents.get(ref).consumed, started: agents.get(ref).started } : a)
      const cur = agents.get(ref)
      return { ref, cap_micro_usd: cur.cap_micro_usd, max_task_micro_usd: cur.max_task_micro_usd, period: cur.period }
    },

    async work(bench, period, n, taken = []) {
      const seen = new Set(taken)
      return Array.from({ length: bench.cadence.tasks_per_period }, (_, i) => ({
        id: `${bench.id}-${period}-${i}`,
        title: `queued work ${period}.${i}`,
        body: `A task drawn from ${bench.firm.surface} for period ${period}.`,
      })).filter((t) => !seen.has(t.id)).slice(0, n)
    },

    /**
     * One task, priced and gated the way the platform would.
     *
     * The cost is between a third and the whole of the task ceiling — a spread
     * wide enough that a cadence which does not fit its cap runs out partway
     * through a period, which is the case worth testing. The gate is the quote
     * against what remains, in the platform's order: the period's cap first,
     * then the task's own ceiling.
     */
    async run(ref, brief) {
      const a = agents.get(ref)
      if (!a) throw new Error(`no agent ${ref}`)
      const start = periodStart(clock(), a.period)
      if (start > a.started) { a.started = start; a.consumed = 0 }

      const tag = brief.match(/^task: (\S+)/m)?.[1] ?? brief.slice(0, 32)
      const cost = Math.round(a.max_task_micro_usd * (0.34 + 0.66 * frac(seed, a.ref, 'cost', tag)))
      const remaining = a.cap_micro_usd - a.consumed
      if (cost > remaining) return { refused: 'budget_exceeded', micro_usd: 0, ms: 1, remaining_micro_usd: remaining }

      a.consumed += cost
      // Not every dispatch produces something. A run that spends and lands
      // nothing is the ordinary failure an outcome bench has to be able to show.
      const landed = frac(seed, a.ref, 'landed', tag) > 0.15
      const id = `chg-${a.bench}-${a.arm}-${tag}`
      if (landed) {
        changes.set(id, {
          id,
          checks: frac(seed, a.ref, 'checks', tag) > 0.2 ? 'pass' : 'fail',
          merged: frac(seed, a.ref, 'merged', tag) > 0.45,
        })
      }
      return {
        run_id: `run-${id}`, micro_usd: cost, ms: 1 + Math.round(1000 * frac(seed, a.ref, 'ms', tag)),
        output: landed ? `Worked the issue and opened a change.\n${id}` : 'Could not reproduce; no change opened.',
      }
    },

    async spend(ref) {
      const a = agents.get(ref)
      return {
        ref, period: a.period, cap_micro_usd: a.cap_micro_usd, max_task_micro_usd: a.max_task_micro_usd,
        consumed_micro_usd: a.consumed, remaining_micro_usd: a.cap_micro_usd - a.consumed,
        by_component: a.consumed ? { model: a.consumed } : undefined,
      }
    },

    async audit() { return { seq: records.length } },

    async changes(ids) { return ids.map((id) => changes.get(id)).filter(Boolean) },

    async record(payload) { records.push(payload); return { ok: true } },
  }
}
