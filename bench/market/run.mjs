/**
 * One arm of one bench, advanced as far as the calendar allows, and recorded.
 *
 *   node run.mjs --bench=code-desk --arm=hanzo-enso-flash-context
 *   node run.mjs --bench=code-desk --arm=all --dry --now=2026-10-08
 *
 * A run is NOT a process that stays up for four weeks. Each invocation does the
 * work the calendar has made due — dispatch the current period's tasks, close
 * every period whose read window has passed — appends it, and exits. Run it
 * daily from cron; run it twice in one day and the second does nothing; miss a
 * week and the run says so rather than quietly shrinking.
 *
 * WHAT THE RUNNER WILL NOT DO.
 *
 * It will not dispatch into a period that has ended. A period that went by with
 * tasks undispatched has missed them, and they are written as missed so the
 * denominator is the bench's, not the operator's.
 *
 * It will not grade a period before its window closes. The outcome of work done
 * on a Monday is what the market did with it by the following Monday, so Monday
 * stays open until then and the run stays partial.
 *
 * It will not write `ended` until the last window has closed. This is the whole
 * reason /v1/research derives completion from questions, answered and ended: a
 * harness that checkpoints reports whatever it has, and a checkpoint published
 * as a result is the defect this field exists to catch. Here it cannot happen,
 * because nothing sets `ended` except the last period closing.
 *
 * It will not enforce a budget. The platform does that — the arm carries
 * cap_micro_usd for the period and max_task_micro_usd for the task, and a
 * dispatch it refuses comes back refused and lowers completion. A second ceiling
 * here would be a second opinion about money.
 */
import { readFileSync, writeFileSync, appendFileSync, mkdirSync, existsSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { load, armOf, head, periodStart, advance, elapsed } from './bench.mjs'
import { readerOf } from './grade.mjs'
import * as cloud from './cloud.mjs'
import * as local from './local.mjs'

const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.slice(k.length + 3) : d }
const flag = (k) => process.argv.includes(`--${k}`)

const jsonl = (p) => existsSync(p) ? readFileSync(p, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l)) : []

/**
 * The identifier of what a task produced: the last line of the agent's reply,
 * which the brief asks for on its own line. One rule for every bench — a reader
 * that had to parse prose would be reading a different thing per bench.
 */
function artifactOf(output) {
  const last = String(output ?? '').split('\n').map((s) => s.trim()).filter(Boolean).pop() ?? ''
  return /^\S{1,128}$/.test(last) ? last : ''
}

/** What the agent is asked for, once per task. */
const briefOf = (bench, task) =>
  `${bench.firm.brief}\n\nDeliverable: ${bench.firm.deliverable}\n\ntask: ${task.id}\n${task.title}\n\n${task.body}\n\n` +
  `End your reply with the identifier of what you produced, on its own line, and nothing after it.`

export async function advanceRun({ bench, arm, platform, now, dir, log = () => {} }) {
  mkdirSync(dir, { recursive: true })
  const period = bench.duration.period
  const cadence = bench.cadence.tasks_per_period
  const metaPath = `${dir}/meta.json`
  const tasksPath = `${dir}/tasks.jsonl`
  const periodsPath = `${dir}/periods.jsonl`

  const agent = await platform.agent(bench, arm)
  let meta = existsSync(metaPath) ? JSON.parse(readFileSync(metaPath, 'utf8')) : {}
  // Period zero begins at the boundary the first invocation fell inside, so the
  // bench's periods and the budget's reset together.
  const zero = meta.zero ?? periodStart(now, period)
  meta = {
    ...meta,
    bench: bench.id, arm: arm.id, harness: arm.harness, model: arm.model, stack: arm.stack,
    control: arm.id === bench.control.arm,
    agent: agent.ref, budget: { ...agent },
    periods: bench.duration.periods, period, cadence, window: bench.outcome.window_periods,
    tasks: bench.tasks, metric: bench.outcome.metric, unit: bench.outcome.unit, reader: bench.outcome.reader,
    bench_digest: bench.digest, bench_commit: bench.commit, commit: head(),
    live: platform.live, api: platform.api,
    zero, started: meta.started ?? new Date(zero).toISOString(),
  }

  const tasks = jsonl(tasksPath)
  const closed = new Map(jsonl(periodsPath).map((r) => [r.period, r]))
  const at = elapsed(zero, now, period)
  const has = (p) => tasks.filter((t) => t.period === p).length

  // Periods that ended with tasks undispatched. Written once, as missed: the run
  // planned them, nothing ran them, and the completion has to see that.
  for (let p = 0; p < Math.min(at, bench.duration.periods); p++) {
    for (let i = has(p); i < cadence; i++) {
      const row = { period: p, index: i, id: '', refused: 'missed', micro_usd: 0, ms: 0, artifact: '', when: new Date(advance(zero, p, period)).toISOString() }
      appendFileSync(tasksPath, JSON.stringify(row) + '\n'); tasks.push(row)
      log(`period ${p} task ${i}: missed`)
    }
  }

  // The current period's work, if the run is still inside its duration. The
  // queue is asked for what this period still needs and told what the run has
  // already taken, so no task is worked twice however the surface reorders.
  if (at < bench.duration.periods) {
    const want = cadence - has(at)
    if (want > 0) {
      const queue = await platform.work(bench, at, want, tasks.map((t) => t.id).filter(Boolean))
      // A surface with nothing in it is the bench asking for work that does not
      // exist. The slots are written down as such: not the arm's failure, and
      // not coverage either.
      for (let j = queue.length; j < want; j++) {
        const row = { period: at, index: has(at), id: '', refused: 'queue_empty', micro_usd: 0, ms: 0, artifact: '', when: new Date(now).toISOString() }
        appendFileSync(tasksPath, JSON.stringify(row) + '\n'); tasks.push(row)
        log(`period ${at} task ${row.index}: the queue had nothing`)
      }
      for (let i = 0; i < queue.length; i++) {
        const task = queue[i]
        const from = (await platform.audit()).seq
        const r = await platform.run(agent.ref, briefOf(bench, task))
        const to = (await platform.audit()).seq
        const row = {
          period: at, index: has(at), id: task.id, run_id: r.run_id ?? '', refused: r.refused ?? '',
          micro_usd: r.micro_usd ?? 0, ms: r.ms ?? 0, artifact: r.refused ? '' : artifactOf(r.output),
          error: r.error ?? '', audit: [from, to], when: new Date(now).toISOString(),
        }
        appendFileSync(tasksPath, JSON.stringify(row) + '\n'); tasks.push(row)
        log(`period ${at} task ${row.index}: ${row.refused ? `refused (${row.refused})` : row.artifact || 'nothing produced'} · ${row.micro_usd} µUSD`)
        if (row.refused === 'budget_exceeded') {
          // The cap has spoken for this period. Asking again costs a round trip
          // to be told the same thing, and the slots it would have refused are
          // recorded refused for the reason they would have been.
          for (let j = i + 1; j < queue.length; j++) {
            const skip = { period: at, index: has(at), id: queue[j].id, run_id: '', refused: 'budget_exceeded', micro_usd: 0, ms: 0, artifact: '', error: '', audit: [to, to], when: new Date(now).toISOString() }
            appendFileSync(tasksPath, JSON.stringify(skip) + '\n'); tasks.push(skip)
          }
          log(`period ${at}: the cap refused the rest of the period`)
          break
        }
      }
    }
  }

  // Every period whose window has closed and that has not been read.
  const reader = readerOf(bench.outcome.reader)
  for (let p = 0; p < bench.duration.periods; p++) {
    if (closed.has(p)) continue
    if (now < advance(zero, p + bench.outcome.window_periods + 1, period)) continue
    const mine = tasks.filter((t) => t.period === p)
    const { value, done, detail } = await reader.read({ platform, bench, arm, period: p, tasks: mine })
    const row = {
      period: p, value, done, of: cadence, unit: bench.outcome.unit,
      micro_usd: mine.reduce((a, t) => a + t.micro_usd, 0),
      refused: mine.filter((t) => t.refused).length,
      opened: new Date(advance(zero, p, period)).toISOString(),
      read: new Date(advance(zero, p + bench.outcome.window_periods + 1, period)).toISOString(),
      detail,
    }
    appendFileSync(periodsPath, JSON.stringify(row) + '\n'); closed.set(p, row)
    log(`period ${p} closed: ${value} ${bench.outcome.unit}, ${done}/${cadence} complete, ${row.micro_usd} µUSD`)
  }

  const done = [...closed.values()]
  const budget = await platform.spend(agent.ref)
  // The last window has passed and every period has been read: this run is over,
  // and only now can it say so.
  if (done.length === bench.duration.periods && !meta.ended) meta.ended = new Date(now).toISOString()
  writeFileSync(metaPath, JSON.stringify(meta, null, 1))

  const metrics = summarize({ bench, arm, meta, tasks, periods: done, budget })
  writeFileSync(`${dir}/metrics.json`, JSON.stringify(metrics, null, 1))
  return { meta, metrics, tasks, periods: done }
}

/**
 * The run's numbers, at the grain /v1/research compares on: one row per
 * (category, metric). `all` is the whole run; `period-<n>` is one period, which
 * is how a growth curve is read back without a second shape for it.
 *
 * COVERAGE AND COMPLETION ARE TWO THINGS AND THIS IS WHERE THEY PART.
 *
 * `answered` is coverage: how many of the planned tasks the run actually got to,
 * in periods that have been read. A task the budget refused counts — the budget
 * is the experiment, and an arm that spends its day on the first task has been
 * measured, not skipped. A task nobody dispatched, because the runner was not
 * invoked that day or because the surface had no work in it, does NOT count, and
 * that is what makes /v1/research call such a run partial: what went wrong was
 * the operator's or the queue's, not the arm's.
 *
 * `tasks_complete` is the grade: how many tasks produced the deliverable, as the
 * bench's reader judges it. It is a MEASURE, because it is a result — the column
 * where an arm that burned its cap early pays for it visibly. Folding it into
 * coverage would make every honest finished run read as unfinished.
 */
function summarize({ bench, arm, meta, tasks, periods, budget }) {
  const M = (category, metric, value, n, of) => ({ category, metric, value, lo: null, hi: null, n: n ?? null, of: of ?? null })
  const read = tasks.filter((t) => periods.some((p) => p.period === t.period))
  const why = (reason) => read.filter((t) => t.refused === reason).length
  // Covered is what the run got to. The two reasons it might not have are named
  // measures of their own, so a short denominator always says whose fault it was.
  const covered = read.filter((t) => t.refused !== 'missed' && t.refused !== 'queue_empty')
  const ran = read.filter((t) => !t.refused)
  const cost = read.reduce((a, t) => a + t.micro_usd, 0)
  const outcome = periods.reduce((a, p) => a + p.value, 0)
  const complete = periods.reduce((a, p) => a + p.done, 0)
  const n = covered.length, of = bench.tasks

  const measures = [
    M('all', bench.outcome.metric, outcome, n, of),
    M('all', 'tasks_complete', complete, n, of),
    M('all', 'tasks_refused', why('budget_exceeded'), n, of),
    M('all', 'tasks_missed', why('missed'), read.length, of),
    M('all', 'tasks_queue_empty', why('queue_empty'), read.length, of),
    M('all', 'spend_micro_usd', cost, n, of),
  ]
  if (ran.length) measures.push(M('all', 'cost_per_task_micro_usd', Math.round(cost / ran.length), ran.length, of))
  if (outcome > 0) measures.push(M('all', 'cost_per_outcome_micro_usd', Math.round(cost / outcome), n, of))
  for (const p of periods) {
    measures.push(M(`period-${p.period}`, bench.outcome.metric, p.value, p.of, p.of))
    measures.push(M(`period-${p.period}`, 'spend_micro_usd', p.micro_usd, p.of, p.of))
  }

  return {
    bench: bench.id, arm: arm.id, control: meta.control, metric: bench.outcome.metric, unit: bench.outcome.unit,
    questions: bench.tasks, answered: covered.length, complete,
    periods: { planned: bench.duration.periods, closed: periods.length },
    ended: meta.ended ?? null,
    // The arm's ceiling and what the CURRENT period has drawn against it, from
    // the ledger that enforced it. The run's total is the spend_micro_usd measure.
    budget, measures,
  }
}

/** The three records this run is. A benchmark, the question it answers, and the execution. */
export function records({ bench, arm, meta, metrics }) {
  const brief = createHash('sha256').update(bench.firm.brief).digest('hex').slice(0, 16)
  return {
    benchmark: {
      id: bench.id, title: bench.title, version: bench.digest,
      dataset: bench.firm.surface, license: 'our own work queue; no third-party dataset',
      origin: `bench/market/benches/${bench.id}.json`,
      splits: [{ name: 'all', items: bench.tasks, definition: `${bench.duration.periods} ${bench.duration.period}s of ${bench.cadence.tasks_per_period} tasks` }],
      metric: bench.outcome.metric,
      metrics: [bench.outcome.metric, 'tasks_complete', 'tasks_refused', 'spend_micro_usd', 'cost_per_task_micro_usd'],
      citation: `bench/market/benches/${bench.id}.json @ ${bench.commit || 'uncommitted'}`,
      notes: `${bench.completion} Outcome read ${bench.outcome.window_periods} ${bench.duration.period}(s) after the period it measures. ` +
        `Budget ${bench.budget.cap_micro_usd} micro-USD per ${bench.duration.period} per arm, ${bench.budget.max_task_micro_usd} per task, enforced by the platform. ` +
        `Control: ${bench.control.arm} — ${bench.control.why}`,
    },
    study: { id: bench.id, title: bench.title, question: bench.question, finding: '' },
    run: {
      id: `${bench.id}-${arm.id}`, benchmark: bench.id, split: 'all',
      system: arm.id, version: arm.model, baseline: arm.id === bench.control.arm, study: bench.id,
      reader: arm.model, embedder: '', k: null, temperature: null, max_tokens: null,
      prompt: `bench/market/benches/${bench.id}.json`, prompt_digest: brief,
      dataset_digest: '', store_digest: '', facts_digest: '', commit: meta.commit,
      questions: metrics.questions, answered: metrics.answered,
      when: meta.started, ended: meta.ended ?? '', by: meta.agent,
      measures: metrics.measures,
      notes: `${metrics.periods.closed} of ${metrics.periods.planned} periods read; ${metrics.complete} of ${metrics.questions} tasks complete. ` +
        `harness ${arm.harness}, stack ${arm.stack}. definition ${bench.digest} (bench/market/benches/${bench.id}.json @ ${bench.commit || 'uncommitted'}).`,
    },
  }
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const dry = flag('dry')
  const bench = load(arg('bench') ?? (() => { throw new Error('--bench= is required') })())
  if (bench.status !== 'ready') throw new Error(`${bench.id} is ${bench.status}: ${bench.decisions.map((d) => d.id).join(', ') || 'no reader'}`)
  const when = arg('now')
  if (when && !dry) throw new Error('--now is a dry-run instrument; a live run happens when it happens')
  if (!dry && bench.dirty) throw new Error(`${bench.path} differs from what is committed; no commit would describe this run`)
  const now = when ? Date.parse(when.length <= 10 ? `${when}T12:00:00Z` : when) : Date.now()

  const arms = arg('arm') === 'all' ? bench.arms : [armOf(bench, arg('arm') ?? (() => { throw new Error('--arm= is required') })())]
  // A dry run holds the clock, so it can do in one process what a live run does
  // over weeks: it ticks through every period of the bench, and the state machine
  // runs once per tick exactly as cron would call it.
  let tick = now
  const platform = dry ? local.open({ seed: arg('seed', 'dry'), clock: () => tick }) : cloud.open({ project: arg('project', 'market') })
  const base = dry ? 'runs-dry' : 'runs'
  const period = bench.duration.period

  for (const arm of arms) {
    const dir = new URL(`./${base}/${bench.id}-${arm.id}/`, import.meta.url).pathname.replace(/\/$/, '')
    console.log(`\n${bench.id} · ${arm.id}${dry ? ' (dry)' : ''}`)
    const log = (s) => { if (!dry || /closed|refused|missed/.test(s)) console.log(`  ${s}`) }
    let out
    if (dry) {
      const zero = periodStart(now, period)
      for (let n = 0; n <= bench.span; n++) { tick = advance(zero, n, period); out = await advanceRun({ bench, arm, platform, now: tick, dir, log }) }
    } else {
      out = await advanceRun({ bench, arm, platform, now, dir, log })
    }
    const rec = records({ bench, arm, ...out })
    if (dry) writeFileSync(`${dir}/records.json`, JSON.stringify(rec, null, 1))
    else await platform.record(rec)
    const m = out.metrics
    console.log(`  ${m.answered}/${m.questions} tasks covered, ${m.complete} complete · ${m.periods.closed}/${m.periods.planned} periods read · ${m.ended ? 'ended ' + m.ended : 'still running'}`)
  }
}
