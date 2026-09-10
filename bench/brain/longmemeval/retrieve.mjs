/**
 * LongMemEval-S, retrieval only: does the session holding the answer come back?
 *
 * Each of the 500 questions carries its own haystack of ~50 chat sessions, one
 * or more of which (`answer_session_ids`) contain the evidence. Sessions repeat
 * across questions, so every unique session is embedded once, turn by turn.
 * Two rankers, both the plain dense baseline the paper starts from:
 *   turn-max      a session scores the best cosine of any of its turns
 *   session-mean  a session is the mean of its turn vectors
 * Reported: R@5 / R@10 ALL and ANY over the answer sessions, MRR of the first,
 * per question type — the six LongMemEval types, and the whole.
 *
 *   EMBED=all-minilm node retrieve.mjs            (Ollama, 127.0.0.1:11434)
 *   EMBED=zenlm/zen-embedding-0.6b node retrieve.mjs
 */
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
const EMBED = process.env.EMBED ?? 'all-minilm', OLLAMA = process.env.OLLAMA ?? 'http://127.0.0.1:11434'
const DATA = new URL('../data/longmemeval/', import.meta.url).pathname
const items = JSON.parse(readFileSync(DATA + 'longmemeval_s', 'utf8'))
// vectors cached as float32, turns then questions: a JSON string of 200k vectors is longer than V8 allows
const CACHE = DATA + `vec-${EMBED.replace(/[^a-z0-9.-]/gi, '_')}.f32`

// every unique session, once
const sessions = new Map()
for (const it of items) it.haystack_session_ids.forEach((sid, i) => { if (!sessions.has(sid)) sessions.set(sid, it.haystack_sessions[i]) })
const texts = [], owner = []
for (const [sid, turns] of sessions) turns.forEach((t, j) => { texts.push(`${t.role}: ${t.content}`); owner.push(sid) })
console.log(`${items.length} questions · ${sessions.size} unique sessions · ${texts.length} turns · embed ${EMBED}`)

async function embed(batch) {
  for (let attempt = 0; ; attempt++) {
    const r = await fetch(`${OLLAMA}/api/embed`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ model: EMBED, input: batch }) })
    if (r.ok) return (await r.json()).embeddings
    if (attempt > 3) throw new Error(`embed ${r.status}: ${(await r.text()).slice(0, 120)}`)
    await new Promise((z) => setTimeout(z, 1000 * (attempt + 1)))
  }
}
const unit = (v) => { let n = 0; for (const x of v) n += x * x; n = Math.sqrt(n) || 1; return v.map((x) => x / n) }
const dot = (a, b) => { let s = 0; for (let i = 0; i < a.length; i++) s += a[i] * b[i]; return s }
let vec
const views = (buf, n, d) => Array.from({ length: n }, (_, i) => buf.subarray(i * d, (i + 1) * d))
if (existsSync(CACHE)) { const h = JSON.parse(readFileSync(CACHE + '.json', 'utf8')); const b = readFileSync(CACHE); const f = new Float32Array(b.buffer, b.byteOffset, b.byteLength / 4)
  vec = { turns: views(f.subarray(0, h.turns * h.dim), h.turns, h.dim), questions: views(f.subarray(h.turns * h.dim), h.questions, h.dim) }; console.log('cached vectors') }
else {
  vec = { turns: [], questions: [] }; const B = 64, t0 = Date.now()
  for (let i = 0; i < texts.length; i += B) { vec.turns.push(...(await embed(texts.slice(i, i + B))).map(unit)); if ((i / B) % 50 === 0) process.stdout.write(`\r  turns ${i}/${texts.length}  ${((Date.now() - t0) / 1000).toFixed(0)}s   `) }
  for (let i = 0; i < items.length; i += B) vec.questions.push(...(await embed(items.slice(i, i + B).map((q) => q.question))).map(unit))
  const dim = vec.turns[0].length, f = new Float32Array((vec.turns.length + vec.questions.length) * dim); [...vec.turns, ...vec.questions].forEach((v, i) => f.set(v, i * dim))
  writeFileSync(CACHE, Buffer.from(f.buffer)); writeFileSync(CACHE + '.json', JSON.stringify({ turns: vec.turns.length, questions: vec.questions.length, dim })); console.log(`\n  embedded in ${((Date.now() - t0) / 1000).toFixed(0)}s`)
}
// per-session views
const idx = new Map(); owner.forEach((sid, i) => (idx.get(sid) ?? idx.set(sid, []).get(sid)).push(i))
const mean = new Map(); for (const [sid, ids] of idx) { const m = new Array(vec.turns[0].length).fill(0); for (const i of ids) for (let d = 0; d < m.length; d++) m[d] += vec.turns[i][d]; mean.set(sid, unit(m)) }

const KS = [5, 10]
const tally = {}; const cell = (type) => (tally[type] ??= { n: 0, mrr: { tm: 0, sm: 0 }, all: {}, any: {} })
for (const type of ['ALL', ...new Set(items.map((q) => q.question_type))]) { const c = cell(type); for (const r of ['tm', 'sm']) for (const k of KS) { c.all[`${r}@${k}`] = 0; c.any[`${r}@${k}`] = 0 } }
const t0 = process.hrtime.bigint(); let latency = []
items.forEach((q, qi) => {
  const qv = vec.questions[qi], gold = new Set(q.answer_session_ids)
  const t1 = process.hrtime.bigint()
  const tm = q.haystack_session_ids.map((sid) => [sid, Math.max(...idx.get(sid).map((i) => dot(qv, vec.turns[i])))]).sort((a, b) => b[1] - a[1]).map(([s]) => s)
  const sm = q.haystack_session_ids.map((sid) => [sid, dot(qv, mean.get(sid))]).sort((a, b) => b[1] - a[1]).map(([s]) => s)
  latency.push(Number(process.hrtime.bigint() - t1) / 1e6)
  for (const type of ['ALL', q.question_type]) { const c = cell(type); c.n++
    for (const [r, ranked] of [['tm', tm], ['sm', sm]]) {
      const first = ranked.findIndex((s) => gold.has(s)); if (first >= 0) c.mrr[r] += 1 / (first + 1)
      for (const k of KS) { const got = new Set(ranked.slice(0, k)); const hits = [...gold].filter((g) => got.has(g)).length; if (hits === gold.size) c.all[`${r}@${k}`]++; if (hits > 0) c.any[`${r}@${k}`]++ } } }
})
latency.sort((a, b) => a - b)
const pct = (a, b) => ((a / b) * 100).toFixed(1).padStart(5)
console.log(`\n── LongMemEval-S · session recall · ${EMBED} ──`)
console.log(`type                        n    turn-max  R@5 all/any  R@10 all/any  MRR   session-mean R@5 all/any  R@10 all/any  MRR`)
for (const [type, c] of Object.entries(tally)) console.log(`${type.padEnd(26)} ${String(c.n).padStart(4)}    ${pct(c.all['tm@5'], c.n)}/${pct(c.any['tm@5'], c.n)}   ${pct(c.all['tm@10'], c.n)}/${pct(c.any['tm@10'], c.n)}  ${(c.mrr.tm / c.n).toFixed(3)}        ${pct(c.all['sm@5'], c.n)}/${pct(c.any['sm@5'], c.n)}   ${pct(c.all['sm@10'], c.n)}/${pct(c.any['sm@10'], c.n)}  ${(c.mrr.sm / c.n).toFixed(3)}`)
console.log(`retrieval latency p50 ${latency[Math.floor(latency.length * 0.5)].toFixed(2)} ms · p95 ${latency[Math.floor(latency.length * 0.95)].toFixed(2)} ms · sessions per haystack ${(items.reduce((a, q) => a + q.haystack_session_ids.length, 0) / items.length).toFixed(0)}`)
const out = { embed: EMBED, k: KS, tally, latency_ms: { p50: latency[Math.floor(latency.length * 0.5)], p95: latency[Math.floor(latency.length * 0.95)] } }
writeFileSync(new URL(`./baseline-${EMBED.replace(/[^a-z0-9.-]/gi, '_')}.json`, import.meta.url).pathname, JSON.stringify(out, null, 1))
