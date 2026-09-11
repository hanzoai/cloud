/**
 * One run directory, read once: what it was, what it set out to answer, what it
 * answered, and the numbers it produced.
 *
 * Each lane writes metrics.json in its own shape — `summary` keyed by LoCoMo's
 * numeric categories, `by_size` for the haystacks, a `table` of styles, the flat
 * split blocks rescore.mjs writes — and two readers want the same thing out of
 * it: `results.mjs`, which publishes the sections hanzo.ai/benchmarks renders,
 * and `record.mjs`, which posts them to /v1/research. Reading a run twice is how
 * a page and a record come to disagree about it, so it is read here.
 *
 * Nothing is recomputed. The numbers are the ones the lane wrote, and a field it
 * did not write stays null — a count that is absent is never a count of zero.
 */
import { existsSync, readFileSync } from 'node:fs'

/** LoCoMo's numeric categories, in the words the tables use. */
export const LABEL = { 1: 'multi-hop', 2: 'temporal', 3: 'open-domain', 4: 'single-hop', all: 'all' }

/** A leaf is a number or a bootstrapped interval. Anything else is structure. */
export const leaf = (v) => typeof v === 'number' || (v && typeof v === 'object' && 'mean' in v)

/** The fields a lane may put its grid under, most specific first. */
export const GRIDS = ['summary', 'by_size', 'by_style', 'by_category', 'rows', 'table']

/** A grid as [name, row] pairs, whether the lane wrote an object or an array. */
export const pairs = (v) => (Array.isArray(v) ? v.map((r, i) => [r.name ?? r.row ?? r.style ?? r.size ?? String(i), r]) : Object.entries(v))

/** A file that may not be there. Absent reads as absent, never as an error. */
export const json = (url) => (existsSync(url) ? JSON.parse(readFileSync(url, 'utf8')) : null)

/**
 * The split blocks run.mjs writes, in the flat `split` / `split:category` shape
 * rescore.mjs writes — for the runs scored before that shape existed. A run that
 * carries a summary keeps it: the summary is the deduped one, and the nested
 * blocks beside it are whatever the run was scored at last.
 */
export const flatten = (m) => {
  const out = {}
  for (const split of ['all', 'dev', 'test']) { const b = m[split]; if (!b || typeof b !== 'object') continue
    if (b.overall?.n) out[split] = b.overall
    for (const [name, x] of Object.entries(b)) if (name !== 'overall' && x?.n) out[`${split}:${name}`] = x }
  return Object.keys(out).length ? out : null
}

/** Every category a run reported: its name, how many items it covers, its metrics. */
export function categoriesOf(m) {
  const key = GRIDS.find((k) => m[k] && typeof m[k] === 'object')
  const v = key ? m[key] : flatten(m)
  return (v ? pairs(v) : []).filter(([, r]) => r && typeof r === 'object' && Object.values(r).some(leaf))
    .map(([c, s]) => ({ name: LABEL[c] ?? c, n: s.n ?? null,
      metrics: Object.fromEntries(Object.entries(s).filter(([k, x]) => k !== 'n' && leaf(x)).map(([k, x]) => [k, x && typeof x === 'object' ? { mean: x.mean, lo: x.lo, hi: x.hi } : x])) }))
}

/**
 * Completion, DERIVED from what the record says and never written by a run.
 *
 * A harness that checkpoints produces the record a finished run produces minus
 * the denominator, so a count that is absent is never assumed and counts that
 * agree are not enough on their own: something has to say the run ended. Same
 * four words, same rule, as /v1/research derives on read.
 */
export const completionOf = (questions, answered, ended) =>
  questions == null || answered == null ? 'unknown'
    : answered > questions ? 'inconsistent'
      : answered < questions ? 'partial'
        : ended ? 'complete' : 'unknown'

/**
 * One run's execution record: what it was frozen at, what it read, what it set
 * out to answer, what it answered, and what it cost. Absent stays absent — a
 * field the harness did not write is null here and `unknown` downstream, never
 * zero. hanzo.ai/benchmarks admits a row to a leaderboard on `completion`.
 */
export const record = (meta) => ({
  commit: meta.commit ?? null,
  prompt: meta.prompt ?? null,
  prompt_digest: meta.prompt_sha256 ?? null,
  dataset_digest: meta.dataset_sha256 ?? null,
  store_digest: meta.store_sha256 ?? meta.store ?? null,
  facts_digest: meta.facts_sha256 ?? null,
  reader: meta.reader ?? null,
  embedding: meta.embedding ?? meta.embed ?? null,
  temperature: meta.temperature ?? null,
  max_tokens: meta.max_tokens ?? null,
  questions: meta.questions ?? null,
  answered: meta.answered ?? null,
  started: meta.started ?? meta.when ?? null,
  ended: meta.finished ?? null,
  completion: completionOf(meta.questions ?? null, meta.answered ?? null, meta.finished ?? null),
  wall_seconds: meta.wall_seconds ?? null,
  tokens_per_question: meta.tokens_per_question ?? null,
  reader_tokens: meta.reader_tokens ?? null,
})

/**
 * One run directory as a section: what it was, its execution record, and its
 * numbers. `dir` is the directory URL, `id` its name — which is also the run's
 * identity and, before the first dash, the benchmark it measured.
 */
export function sectionOf(dir, id) {
  const m = json(new URL('metrics.json', dir)) ?? {}
  const meta = json(new URL('meta.json', dir)) ?? {}
  return { bench: id.split('-')[0], run: id, row: m.row ?? null, split: m.split ?? meta.split ?? null,
    k: m.k ?? meta.k ?? null, reader: m.reader ?? meta.reader ?? null, facts: m.facts ?? meta.facts ?? null,
    commit: meta.commit ?? null, record: record(meta), categories: categoriesOf(m),
    table: typeof m.table === 'string' ? m.table : null }
}
