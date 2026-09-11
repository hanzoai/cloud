/**
 * LoCoMo-Conv harness: the paper's protocol, with the ranker pluggable.
 *
 * Protocol (arXiv 2609.03467): memories are the raw LoCoMo turns, embedded with
 * all-MiniLM-L6-v2; top-10 are returned; recall is |retrieved ∩ gold| / |gold|
 * with verbatim containment. The paper's four query styles — dialog, implicit,
 * counterfactual, composed — are loaded from data/conv/repo when the authors
 * release them (https://github.com/MiuLab/LoCoMo-Conv/, not public as of this
 * commit). Until then the one runnable style is `original`: the LoCoMo QA pool
 * the rewrites were made from, which is the sanity check that this harness
 * matches theirs — the paper's dialog row is a near-paraphrase of it.
 *
 *   node retrieve.mjs                       # all-MiniLM (exact weights), k=10
 *   VEC=all-minilm node retrieve.mjs        # Ollama all-minilm GGUF
 *   VEC=zenlm/zen-embedding-0.6b node retrieve.mjs
 *   K=20 node retrieve.mjs
 *
 * To plug another ranker in: import { run } and pass rank(ci, query) -> [turn ids],
 * where query is { text, v } and ci indexes conversations.
 */
import { readFileSync, writeFileSync, existsSync, mkdirSync } from 'node:fs'
import { record } from '../record.mjs'
import { recallOf, summary, fmt } from './score.mjs'
const ROOT = new URL('../', import.meta.url).pathname
const VEC = process.env.VEC ?? 'st-minilm', K = Number(process.env.K ?? 10), OLLAMA = process.env.OLLAMA ?? 'http://127.0.0.1:11434'
const unit = (v) => { let n = 0; for (const x of v) n += x * x; n = Math.sqrt(n) || 1; return v.map((x) => x / n) }
const dot = (a, b) => { let s = 0; for (let i = 0; i < a.length; i++) s += a[i] * b[i]; return s }

async function embed(model, texts) {
  const out = []
  for (let i = 0; i < texts.length; i += 64) {
    const r = await fetch(`${OLLAMA}/api/embed`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ model, input: texts.slice(i, i + 64) }) })
    if (!r.ok) throw new Error(`embed ${r.status}`)
    out.push(...(await r.json()).embeddings.map(unit))
  }
  return out
}
/** The memory bank: every conversation's turns with vectors, and the QA pool with vectors. */
export async function load() {
  const st = ROOT + 'data/conv/vec-st-minilm.json'
  if (VEC === 'st-minilm') { if (!existsSync(st)) throw new Error('run: uv run --with sentence-transformers python3 conv/embed-st.py'); return JSON.parse(readFileSync(st, 'utf8')) }
  const cache = ROOT + `data/conv/vec-${VEC.replace(/[^a-z0-9.-]/gi, '_')}.json`
  if (existsSync(cache)) return JSON.parse(readFileSync(cache, 'utf8'))
  const base = JSON.parse(readFileSync(st, 'utf8')) // same turns and qa, re-embedded
  for (const c of base) {
    const tv = await embed(VEC, c.turns.map((t) => `${t.speaker}: ${t.text}`)); c.turns.forEach((t, i) => (t.v = tv[i]))
    const qv = await embed(VEC, c.qa.map((q) => q.question)); c.qa.forEach((q, i) => (q.v = qv[i]))
  }
  writeFileSync(cache, JSON.stringify(base)); return base
}
/** The paper's queries, when released: {style, ci, text, v?, gold:[dia ids], supportive:[dia ids], answer}. */
export function styles(bank) {
  const out = { original: [] }
  bank.forEach((c, ci) => c.qa.forEach((q) => { if (q.evidence.length) out.original.push({ style: 'original', ci, text: q.question, v: q.v, gold: q.evidence, category: q.category, answer: q.answer }) }))
  const repo = ROOT + 'data/conv/repo'
  if (existsSync(repo)) console.log(`data/conv/repo present — add its loader here (styles dialog/implicit/counterfactual/composed)`)
  return out
}
/** The dense baseline the paper calls Naive RAG: cosine over raw turns. */
export const dense = (bank) => (ci, query) => {
  const turns = bank[ci].turns
  return turns.map((t, i) => [i, dot(query.v, t.v)]).sort((a, b) => b[1] - a[1]).slice(0, K).map(([i]) => turns[i].id)
}
export async function run(rank, label, bank) {
  bank ??= await load(); const S = styles(bank); const rows = []; const lat = []
  for (const [style, items] of Object.entries(S)) for (const it of items) {
    const byId = new Map(bank[it.ci].turns.map((t) => [t.id, t]))
    const t0 = process.hrtime.bigint(); const ids = rank(it.ci, it); lat.push(Number(process.hrtime.bigint() - t0) / 1e6)
    const returned = ids.map((id) => byId.get(id)).filter(Boolean), gold = it.gold.map((id) => byId.get(id)).filter(Boolean)
    const r = recallOf(returned, gold); if (!r) continue
    rows.push({ style, category: it.category, ci: it.ci, ...r, tokens: Math.round(returned.reduce((a, t) => a + t.text.length, 0) / 4) })
  }
  lat.sort((a, b) => a - b)
  const groups = {}
  for (const r of rows) { for (const key of [r.style, `${r.style}·cat${r.category}`, r.category === 5 ? null : `${r.style}·no-adversarial`].filter(Boolean)) (groups[key] ??= []).push(r) }
  console.log(`\n── LoCoMo-Conv harness · ${label} · k=${K} · ${VEC} ──`)
  console.log(`group                       n     recall@k (paper metric)      all      any     tokens/q`)
  const table = {}
  for (const [g, rs] of Object.entries(groups)) { const s = summary(rs), a = summary(rs, 'all'), y = summary(rs, 'any'); table[g] = { n: s.n, recall: s.mean, ci: [s.lo, s.hi], all: a.mean, any: y.mean, tokens: rs.reduce((x, r) => x + r.tokens, 0) / rs.length }
    console.log(`${g.padEnd(26)} ${String(s.n).padStart(5)}   ${fmt(s).padEnd(26)}  ${(a.mean * 100).toFixed(1).padStart(5)}    ${(y.mean * 100).toFixed(1).padStart(5)}   ${table[g].tokens.toFixed(0).padStart(6)}`) }
  console.log(`latency p50 ${lat[Math.floor(lat.length / 2)].toFixed(3)} ms · p95 ${lat[Math.floor(lat.length * 0.95)].toFixed(3)} ms`)
  const dir = ROOT + `runs/conv-proxy-${label}-${VEC.replace(/[^a-z0-9.-]/gi, '_')}-k${K}/`; mkdirSync(dir, { recursive: true })
  writeFileSync(dir + 'metrics.json', JSON.stringify({ label, vec: VEC, k: K, table, latency_ms: { p50: lat[Math.floor(lat.length / 2)], p95: lat[Math.floor(lat.length * 0.95)] } }, null, 1))
  writeFileSync(dir + 'predictions.jsonl', rows.map((r) => JSON.stringify(r)).join('\n') + '\n')
  // questions counts every item the styles enumerate; answered counts the rows
  // that scored, and an item with no recoverable gold is the difference
  writeFileSync(dir + 'meta.json', JSON.stringify({ bench: 'locomo-conv (proxy: original LoCoMo QA pool; paper styles unreleased)', protocol: 'arXiv 2609.03467: raw turns, top-k, recall=|ret∩gold|/|gold| with verbatim containment', embed: VEC, k: K, memories: 'speaker: text', when: new Date().toISOString(),
    questions: Object.values(S).reduce((a, xs) => a + xs.length, 0), answered: rows.length, finished: new Date().toISOString() }, null, 1))
  await record(dir)
  return table
}
if (process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())) { const bank = await load(); await run(dense(bank), 'naive-rag', bank) }
