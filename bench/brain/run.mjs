/**
 * An answer run that finishes. Every answer is appended to
 * runs/<name>/predictions.jsonl the moment it lands; a question that fails
 * (quota, 5xx, a dropped connection, an empty reply) is asked again on the next
 * pass, and passes continue until nothing is missing. Killing it loses nothing:
 * the same command resumes.
 *
 *   node run.mjs --policy=cer --k=20 --reader=enso-flash [--cats=1,2,3,4] [--workers=3] [--name=...]
 *
 * The reader's endpoint follows its name (answer.mjs): a tagged model such as
 * `gemma4:31b` is local Ollama, anything else is the router. Questions are asked
 * in category order (multi-hop, temporal, open-domain, single-hop) so a partial
 * run is complete for whole categories first.
 */
import { readFileSync, writeFileSync, appendFileSync, mkdirSync, existsSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { execSync } from 'node:child_process'
import { rankAll, store } from './rank.mjs'
import { ask, apiFor, credential, contextOf, f1, em } from './answer.mjs'
import { scoreRows, expectedCounts, readRows, counts, qid, table } from './score.mjs'
import { record } from './record.mjs'

const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const POLICY = arg('policy', 'cer'), K = Number(arg('k', 20)), READER = arg('reader', 'enso-flash')
const CATS = arg('cats', '1,2,3,4').split(',').map(Number)
const WORKERS = Number(arg('workers', 3)), MAX_PASSES = Number(arg('passes', 200))
const API = arg('api', apiFor(READER))
const NAME = arg('name', `locomo-all-${POLICY}-k${K}-${READER.replace(/[/:]/g, '-')}`)
const DIR = `runs/${NAME}`
mkdirSync(DIR, { recursive: true })

const sha = (p) => createHash('sha256').update(readFileSync(p)).digest('hex').slice(0, 16)
const PROMPT_PATH = new URL('./prompts/reader-locomo.txt', import.meta.url)
const system = readFileSync(PROMPT_PATH, 'utf8').trim()
const corpus = JSON.parse(readFileSync('locomo10.json', 'utf8'))
const dates = corpus.map((c) => Object.fromEntries(Object.entries(c.conversation).filter(([k]) => k.endsWith('_date_time')).map(([k, v]) => [k.replace('_date_time', ''), v])))
const turnsById = store.map((c) => new Map(c.turns.map((t) => [t.id, t])))
const ranked = await rankAll(POLICY, K)
const key = credential(API)

const qs = []
store.forEach((c, ci) => c.qa.forEach((q, qi) => { if (q.evidence.length && CATS.includes(q.category)) qs.push({ ci, qi, cat: q.category, question: q.question }) }))
qs.sort((a, b) => a.cat - b.cat || a.ci - b.ci || a.qi - b.qi)
const goldOf = ({ ci, question }) => corpus[ci].qa.find((x) => x.question === question)?.answer ?? ''

const log = (s) => { const line = `${new Date().toISOString()} ${s}`; console.log(line); appendFileSync(`${DIR}/log.txt`, line + '\n') }
const started = Date.now()
let meta = existsSync(`${DIR}/meta.json`) ? JSON.parse(readFileSync(`${DIR}/meta.json`, 'utf8')) : {}
// wall time accumulates across invocations: a row filled one category at a time is still one row
const priorWall = meta.wall_seconds ?? 0
const wall = () => priorWall + Math.round((Date.now() - started) / 1000)
// Categories accumulate: a directory asked for multi-hop yesterday and for the
// rest today has been asked for both, and its denominator is both.
const asked = [...new Set([...(meta.cats ?? []), ...CATS])].sort((a, b) => a - b)
meta = { ...meta, name: NAME, benchmark: 'locomo', policy: POLICY, k: K, reader: READER, api: new URL(API).host, cats: asked, workers: WORKERS,
  prompt: PROMPT_PATH.pathname.split('/').slice(-2).join('/'), prompt_sha256: sha(PROMPT_PATH), dataset_sha256: sha('locomo10.json'), store_sha256: sha('brain-vectors.json'),
  facts_sha256: existsSync('facts-vectors.json') ? sha('facts-vectors.json') : null, commit: execSync('git rev-parse HEAD').toString().trim(),
  temperature: 0, max_tokens: 64, started: meta.started ?? new Date(started).toISOString() }
// questions and answered are read off the rows on disk every time, so the two
// counts always describe the same artifact and a resumed run cannot claim a
// denominator it no longer has.
const writeMeta = (extra = {}) => writeFileSync(`${DIR}/meta.json`, JSON.stringify({ ...meta, ...counts(readRows(DIR), store), ...extra }, null, 1))
const writeMetrics = () => { const rows = readRows(DIR); const m = scoreRows(rows, store, expectedCounts(store, [1, 2, 3, 4])); writeFileSync(`${DIR}/metrics.json`, JSON.stringify(m, null, 1)); return m }
writeMeta()
// The record tracks the files, not a second copy of them held here: a run exists
// in /v1/research from the moment it starts, and says where it is at every pass.
await record(DIR)

async function pool(items, fn) {
  let i = 0
  await Promise.all(Array.from({ length: Math.min(WORKERS, items.length) }, async () => { while (i < items.length) { const j = i++; await fn(items[j]) } }))
}

const WAIT = { quota: 8000, server: 4000, network: 4000, empty: 1500, client: 0 }
async function one(q) {
  const ids = ranked[q.ci][q.qi]
  const context = contextOf(turnsById[q.ci], dates[q.ci], ids)
  const gold = goldOf(q)
  let last
  for (let attempt = 1; attempt <= 3; attempt++) {
    try {
      const { pred, usage, ms } = await ask({ api: API, key, reader: READER, system, context, question: q.question })
      const row = { ci: q.ci, qi: q.qi, cat: q.cat, q: q.question, gold, pred, f1: f1(pred, gold), em: em(pred, gold), tokens: Math.round(context.length / 4), ctx: ids, ms, attempt, usage }
      appendFileSync(`${DIR}/predictions.jsonl`, JSON.stringify(row) + '\n')
      return { ok: true }
    } catch (e) {
      last = e
      if (e.kind === 'client') break
      await new Promise((z) => setTimeout(z, (WAIT[e.kind] ?? 3000) * attempt))
    }
  }
  return { ok: false, kind: last?.kind ?? 'unknown', message: last?.message ?? '' }
}

let pass = 0, quiet = 0
while (pass++ < MAX_PASSES) {
  const have = new Set(readRows(DIR).map(qid))
  const todo = qs.filter((q) => !have.has(qid(q)))
  if (!todo.length) break
  const fails = {}; let okCount = 0, sample = ''
  const t0 = Date.now()
  // a line every 25 answers, and the first failure of each kind as it happens, so a
  // pass over 1,536 questions is not silent for an hour
  await pool(todo, async (q) => {
    const r = await one(q)
    if (r.ok) { okCount++; if (okCount % 25 === 0) log(`  ${okCount + Object.values(fails).reduce((a, b) => a + b, 0)}/${todo.length} asked, ${okCount} answered, ${((Date.now() - t0) / 1000 / okCount).toFixed(1)}s each`) }
    else { fails[r.kind] = (fails[r.kind] ?? 0) + 1; if (fails[r.kind] === 1) log(`  first ${r.kind} failure: ${r.message.slice(0, 140)}`); if (!sample) sample = r.message.slice(0, 120) }
  })
  const failed = Object.values(fails).reduce((a, b) => a + b, 0)
  log(`pass ${pass}: asked ${todo.length}, answered ${okCount}, failed ${failed}${failed ? ' ' + JSON.stringify(fails) + ' e.g. ' + sample : ''} in ${((Date.now() - t0) / 1000).toFixed(0)}s`)
  writeMetrics(); writeMeta({ passes: pass, wall_seconds: wall() }); await record(DIR)
  if (!failed) continue
  quiet = okCount ? 0 : quiet + 1
  // a pass that answered nothing is the router saying not now: wait longer each time, up to half an hour
  const wait = okCount ? 15000 : Math.min(1800000, 60000 * 2 ** (quiet - 1))
  log(`waiting ${Math.round(wait / 1000)}s before the next pass`)
  await new Promise((z) => setTimeout(z, wait))
}
const m = writeMetrics()
const rows = readRows(DIR)
writeMeta({ finished: new Date().toISOString(), wall_seconds: wall(),
  tokens_per_question: rows.length ? Math.round(rows.reduce((a, r) => a + r.tokens, 0) / rows.length) : 0,
  reader_tokens: rows.reduce((a, r) => a + (r.usage?.total_tokens ?? 0), 0) })
await record(DIR)
const done = counts(rows, store)
log(`done: ${done.answered}/${done.questions} answered`)
console.log(table(NAME, m))
