/**
 * A run reports itself to /v1/research while it is running.
 *
 * The defect this exists to prevent: a lane that checkpoints as it goes writes
 * whatever has been answered so far, and a table built from that file cannot
 * tell question 57 of 282 from question 282 of 282. One such row reached
 * hanzo.ai/benchmarks reading 9.4 F1 points above where the run finally landed.
 * So a run says how many questions it set out to answer, how many it has
 * answered, and when it ended — from the first pass, not at the end — and the
 * record derives completion from those three. `?completion=partial` is then a
 * query anyone can run, rather than a number nobody caught.
 *
 *   node record.mjs                      # post every run under runs/ and ../code/runs
 *   node record.mjs runs/<id> ...        # post the ones named
 *
 * Recording is never on a measurement's critical path. Without a credential the
 * recorder says so once and the run proceeds; a failed post is logged and the
 * run proceeds. A benchmark that dies because a record was unreachable is a
 * worse instrument than one that keeps no record at all.
 *
 * What is posted is what the run wrote about itself: meta.json for the
 * configuration and the counts, metrics.json for the numbers, normalized by
 * section.mjs — the same normalization results.mjs publishes, so the record and
 * the tables cannot disagree.
 */
import { existsSync, readdirSync } from 'node:fs'
import { credential, ROUTER } from './answer.mjs'
import { json, sectionOf } from './section.mjs'

const here = new URL('.', import.meta.url)
const API = process.env.HANZO_API ?? 'https://api.hanzo.ai'

/** The two inquiries the bench tree is, each stated by its own README. */
export const STUDY = { brain: 'memory-retrieval', code: 'code-retrieval' }

// Resolved once per process. '' means "not recording", said once on stderr
// rather than silently: a run nobody is recording should look different from a
// run nobody can read back.
let key
function token() {
  if (key === undefined) {
    try { key = credential(ROUTER) } catch { key = ''; console.error('record: no credential — runs are not being recorded (hanzo auth login, or HANZO_API_KEY)') }
  }
  return key
}

/** A run directory, given as a URL or as a path relative to this file. */
const at = (dir) => (dir instanceof URL ? dir : new URL(String(dir).replace(/\/?$/, '/'), here))

/** Byte order, so the record's rows sort the way the plane's do. */
const by = (a, b) => (a < b ? -1 : a > b ? 1 : 0)

/**
 * The run's numbers at the grain a comparison needs: one row per (category,
 * metric), carrying the interval where the lane reported one and carrying none
 * where it did not. `of` is the category's size, not a metric of it, so it
 * becomes every row's denominator instead of a row of its own.
 */
const measuresOf = (s) => s.categories
  .flatMap((c) => {
    const of = typeof c.metrics.of === 'number' ? c.metrics.of : null
    return Object.entries(c.metrics).filter(([m]) => m !== 'of').map(([metric, x]) => ({
      category: c.name, metric,
      value: typeof x === 'number' ? x : x.mean,
      lo: typeof x === 'number' ? null : x.lo,
      hi: typeof x === 'number' ? null : x.hi,
      n: c.n ?? null, of,
    }))
  })
  .sort((a, b) => by(a.category, b.category) || by(a.metric, b.metric))

/**
 * One run directory as a record: its identity, the execution record section.mjs
 * already reads for the published tables, and its numbers at the grain a
 * comparison joins on.
 *
 * Nothing is read twice. `record` is the same object benchmarks.json carries, so
 * a run cannot say one thing on the page and another in the plane; what is added
 * here is only what identifies the run — which task, which split, which system —
 * and the measures.
 *
 * A field the lane does not state stays UNSTATED: null for a count, empty for a
 * name. Absence is never rounded down; a temperature of 0 is a deliberate and
 * common setting, so defaulting it would be the most expensive guess available.
 */
export function runOf(dir, id, study) {
  const s = sectionOf(at(dir), id)
  const r = s.record
  return {
    id,
    benchmark: s.bench,
    split: s.split ?? '',
    // Three lanes, three words for the system that was measured: an answer run
    // calls it the policy, a retrieval run the row, the conv lane the label.
    system: s.row ?? metaRow(dir) ?? '',
    embedder: r.embedding ?? '',
    reader: r.reader ?? s.reader ?? '',
    k: s.k ?? null,
    temperature: r.temperature,
    max_tokens: r.max_tokens,
    prompt: r.prompt ?? '',
    prompt_digest: r.prompt_digest ?? '',
    dataset_digest: r.dataset_digest ?? '',
    store_digest: r.store_digest ?? '',
    facts_digest: r.facts_digest ?? '',
    commit: r.commit ?? '',
    questions: r.questions,
    answered: r.answered,
    when: r.started ?? '',
    ended: r.ended ?? '',
    study,
    notes: notesOf(dir),
    measures: measuresOf(s),
  }
}

/**
 * The system under test where metrics.json does not name it: an answer run
 * records its `policy`, the conv lane its `label`. Where nothing names it the
 * system stays unrecorded rather than guessed out of the directory name.
 */
const metaRow = (dir) => { const m = meta(dir); return m.policy ?? m.row ?? null }

/** What a lane says about the task itself, where the fields above carry none. */
const notesOf = (dir) => { const m = meta(dir); return [m.bench, m.protocol].filter(Boolean).join(' ') }

const meta = (dir) => json(new URL('meta.json', at(dir))) ?? {}

/**
 * Post one run. Returns whether the record took it. Called after the directory's
 * meta.json and metrics.json are on disk — at the start of a run, at every pass,
 * and at the end — so the record tracks the files rather than a second copy of
 * them held in memory.
 */
export async function record(dir, id = at(dir).pathname.replace(/\/$/, '').split('/').pop(), study = STUDY.brain) {
  const t = token()
  if (!t) return false
  try {
    const res = await fetch(`${API}/v1/research/runs`, {
      method: 'POST',
      headers: { 'content-type': 'application/json', authorization: `Bearer ${t}` },
      body: JSON.stringify({ runs: [runOf(dir, id, study)] }),
    })
    if (res.ok) return true
    console.error(`record ${id}: ${res.status} ${(await res.text()).slice(0, 200)}`)
  } catch (e) {
    console.error(`record ${id}: ${e.message}`)
  }
  return false
}

/** The run directories under one lane: those that wrote something about themselves. */
const lane = (rel) => {
  const root = new URL(`${rel}/`, here)
  if (!existsSync(root)) return []
  return readdirSync(root).sort()
    .filter((d) => existsSync(new URL(`${d}/meta.json`, root)) || existsSync(new URL(`${d}/metrics.json`, root)))
    .map((d) => ({ dir: new URL(`${d}/`, root), id: d }))
}

const MAIN = process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())
if (MAIN) {
  const named = process.argv.slice(2).filter((a) => !a.startsWith('--'))
  const runs = named.length
    ? named.map((p) => ({ dir: at(p), id: p.replace(/\/$/, '').split('/').pop(), study: p.includes('code/') ? STUDY.code : STUDY.brain }))
    : [...lane('runs').map((r) => ({ ...r, study: STUDY.brain })), ...lane('../code/runs').map((r) => ({ ...r, study: STUDY.code }))]
  let ok = 0
  for (const r of runs) if (await record(r.dir, r.id, r.study)) ok++
  console.log(`${ok}/${runs.length} runs recorded at ${API}/v1/research/runs`)
}
