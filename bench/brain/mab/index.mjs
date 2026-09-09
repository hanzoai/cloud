/**
 * The read-side indexes over one haystack: dense (zen-embedding via Ollama,
 * cached by text), BM25 over fact text, exact entity → facts, and the
 * supersession chain from parse.mjs. Built once per haystack; the 6k and 262k
 * SH/MH pairs share a haystack, so the cache is keyed by fact text.
 */
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { parse, norm, key, DATA } from './parse.mjs'

const OLLAMA = process.env.OLLAMA ?? 'http://127.0.0.1:11434'
const MODEL = process.env.EMBED_MODEL ?? 'zenlm/zen-embedding-0.6b'
const CACHE = DATA + 'embeddings.json'
let cache = existsSync(CACHE) ? JSON.parse(readFileSync(CACHE, 'utf8')) : {}

export async function embed(texts) {
  const todo = [...new Set(texts.filter((t) => !cache[t]))]
  for (let i = 0; i < todo.length; i += 64) {
    const batch = todo.slice(i, i + 64)
    const r = await fetch(`${OLLAMA}/api/embed`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ model: MODEL, input: batch }) })
    if (!r.ok) throw new Error(`embed ${r.status}: ${(await r.text()).slice(0, 120)}`)
    const { embeddings } = await r.json(); batch.forEach((t, j) => (cache[t] = unit(embeddings[j])))
    if ((i / 64) % 20 === 0 && todo.length > 200) process.stderr.write(`\r  embedded ${Math.min(i + 64, todo.length)}/${todo.length}   `)
  }
  if (todo.length) { // merge with whatever another process wrote meanwhile, so no embedding is lost
    const disk = existsSync(CACHE) ? JSON.parse(readFileSync(CACHE, 'utf8')) : {}; cache = { ...disk, ...cache }; writeFileSync(CACHE, JSON.stringify(cache)) }
  return texts.map((t) => cache[t])
}
const dot = (a, b) => { let s = 0; for (let i = 0; i < a.length; i++) s += a[i] * b[i]; return s }
const unit = (v) => { const n = Math.sqrt(dot(v, v)); return v.map((x) => x / n) }
export { dot }

const tok = (s) => norm(s).replace(/[^\p{L}\p{N}\s]/gu, ' ').split(/\s+/).filter(Boolean)
/** A small BM25: enough for a haystack of templated sentences. */
export function bm25(docs) {
  const N = docs.length, df = new Map(), tf = docs.map((d) => { const m = new Map(); for (const t of tok(d)) m.set(t, (m.get(t) ?? 0) + 1); for (const t of m.keys()) df.set(t, (df.get(t) ?? 0) + 1); return m })
  const len = tf.map((m) => [...m.values()].reduce((a, b) => a + b, 0)), avg = len.reduce((a, b) => a + b, 0) / N, k1 = 1.2, b = 0.75
  return (q, k) => {
    const qt = [...new Set(tok(q))], s = new Float64Array(N)
    for (const t of qt) { const n = df.get(t); if (!n) continue; const idf = Math.log(1 + (N - n + 0.5) / (n + 0.5))
      for (let i = 0; i < N; i++) { const f = tf[i].get(t); if (f) s[i] += idf * (f * (k1 + 1)) / (f + k1 * (1 - b + b * len[i] / avg)) } }
    return [...s.keys()].filter((i) => s[i] > 0).sort((i, j) => s[j] - s[i]).slice(0, k).map((i) => [i, s[i]])
  }
}

/** All indexes for one haystack. `facts[i]` carries `.v` (unit vector) and `.current` (true if last in its chain). */
export async function build(context) {
  const { facts, chain } = parse(context)
  const V = await embed(facts.map((f) => f.text)); facts.forEach((f, i) => (f.v = V[i], f.i = i))
  const bySerial = new Map(facts.map((f) => [f.serial, f]))
  for (const serials of chain.values()) serials.forEach((s, j) => (bySerial.get(s).current = j === serials.length - 1))
  for (const f of facts) if (f.current == null) f.current = true
  const byEntity = new Map() // norm(name) → facts where it is subject or object
  for (const f of facts) for (const nm of [f.subject, f.object]) if (nm) { const k = norm(nm); (byEntity.get(k) ?? byEntity.set(k, []).get(k)).push(f) }
  const lexical = bm25(facts.map((f) => f.text))
  const dense = (qv, k) => [...facts.keys()].map((i) => [i, dot(qv, facts[i].v)]).sort((a, b) => b[1] - a[1]).slice(0, k)
  const current = (subject, relation) => { const serials = chain.get(key(subject, relation)); return serials ? bySerial.get(serials[serials.length - 1]) : null }
  const versions = (subject, relation) => (chain.get(key(subject, relation)) ?? []).map((s) => bySerial.get(s))
  return { facts, chain, byEntity, lexical, dense, current, versions, bySerial }
}

if (process.argv[1] && import.meta.url.endsWith('index.mjs') && process.argv[1].endsWith('index.mjs')) {
  const { load } = await import('./parse.mjs'); const rows = load(); const seen = new Set()
  for (const r of rows) { if (seen.has(r.context.length)) continue; seen.add(r.context.length)
    const t0 = Date.now(); const ix = await build(r.context); console.log(`${r.id}: ${ix.facts.length} facts indexed in ${((Date.now() - t0) / 1000).toFixed(1)}s`) }
}
