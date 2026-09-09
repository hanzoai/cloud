/**
 * Contextual Evidence Retrieval, second cut: a pool and a re-rank, not a fusion.
 *
 * cer.mjs fused whole ranked lists and got worse: reciprocal-rank fusion lets
 * every weak nominator push sixty candidates in, and the linkage that was meant
 * to lift one turn from rank 67 to rank 12 was drowned by fifty-nine others it
 * lifted for no reason. oracle.mjs then measured each link on its own — of the
 * evidence cosine misses, a LoCoMo fact reaches 24%, adjacency 17%, a shared
 * name 20%, and all of them together 52%. Weak individually, real together, and
 * every one of them only makes sense as a BONUS on a candidate that already has
 * a reason to be there.
 *
 * So: the backbone stays cosine over turns. A tight pool is drawn — the top of
 * that ranking, plus the turns cited by the facts nearest the question, plus
 * the neighbours of the very top. Each candidate is then scored once:
 *
 *   turn cosine
 *   + the cosine of the best fact that cites it        (the fact index)
 *   + being adjacent to a top turn                     (dialogue structure)
 *   + being cited by a fact that shares a NAME with a  (one hop of linkage,
 *     fact near the question                            through the fact graph)
 *   + sitting in the session the question's date names (timeline)
 *
 * Nothing learned, nothing called. The weights are constants below and every
 * term is ablated in the table, so a gain is attributable to a mechanism.
 */
import { readFileSync, existsSync } from 'node:fs'

const K = Number(process.env.K ?? 20)
for (const f of ['brain-vectors.json', 'facts-vectors.json']) if (!existsSync(f)) { console.error(`no ${f}`); process.exit(1) }
const store = JSON.parse(readFileSync('brain-vectors.json', 'utf8'))
const factsAll = JSON.parse(readFileSync('facts-vectors.json', 'utf8'))
const corpus = JSON.parse(readFileSync('locomo10.json', 'utf8'))

const dot = (a, b) => { let s = 0; for (let i = 0; i < a.length; i++) s += a[i] * b[i]; return s }
const unit = (v) => { const n = Math.sqrt(dot(v, v)); return v.map((x) => x / n) }

const ENT_STOP = new Set(`I I'm I've I'll I'd The A An And But So Yes No Oh Hey Wow Thanks That This It What When Where Who How Why Also Just Really Maybe Sure Okay OK Well Great Good Nice Yeah Haha Lol My Your Our`.split(/\s+/))
const names = (s) => { const o = new Set(); for (const m of s.matchAll(/\b([A-Z][a-zA-Z']{2,})\b/g)) if (!ENT_STOP.has(m[1])) o.add(m[1].toLowerCase()); return o }
const MONTHS = { january:1,february:2,march:3,april:4,may:5,june:6,july:7,august:8,september:9,october:10,november:11,december:12 }
const parseDate = (s) => { const m = s?.match(/(\d{1,2}) (\w+),? (\d{4})/); return m && MONTHS[m[2].toLowerCase()] ? { y: +m[3], m: MONTHS[m[2].toLowerCase()] } : null }
const cue = (q) => { const l = q.toLowerCase(); const y = l.match(/\b(20\d\d)\b/); const mo = Object.keys(MONTHS).find((m) => l.includes(m)); return { y: y ? +y[1] : null, m: mo ? MONTHS[mo] : null } }

function build(cached, raw, facts) {
  const conv = raw.conversation, speakers = new Set([conv.speaker_a, conv.speaker_b].filter(Boolean).map((s) => s.toLowerCase()))
  const date = {}; for (const k of Object.keys(conv)) if (k.endsWith('_date_time')) date[k.replace('_date_time', '')] = parseDate(conv[k])
  const turns = cached.turns.map((t, i) => { const sess = 'session_' + t.id.match(/^D(\d+):/)[1]
    return { id: t.id, i, sess, when: date[sess], v: unit(t.v), body: t.text.replace(/^[^:]+:\s*/, '') } })
  const byId = new Map(turns.map((t) => [t.id, t]))
  const F = facts.map((f) => ({ ...f, v: unit(f.v), idx: f.ids.map((x) => byId.get(x)?.i).filter((x) => x != null), names: new Set([...names(f.text)].filter((e) => !speakers.has(e))) }))
  const citing = new Map(); F.forEach((f, fi) => { for (const i of f.idx) (citing.get(i) ?? citing.set(i, []).get(i)).push(fi) })
  return { turns, F, citing, speakers }
}

const W = { fact: +(process.env.WFACT ?? 0.2), adjacent: +(process.env.WADJ ?? 0.06), link: +(process.env.WLINK ?? 0.12), time: +(process.env.WTIME ?? 0.05) }
const ENV_POOL = +(process.env.POOL ?? 40), ENV_FPOOL = +(process.env.FPOOL ?? 20)

function retrieve(ix, q, o) {
  const qv = unit(q.v), c = cue(q.question)
  const tScore = ix.turns.map((t) => dot(qv, t.v))
  const order = [...tScore.keys()].sort((a, b) => tScore[b] - tScore[a])
  const pool = new Set(order.slice(0, o.pool ?? ENV_POOL))
  let fScore = null, fOrder = null
  if (o.fact) {
    fScore = ix.F.map((f) => dot(qv, f.v)); fOrder = [...fScore.keys()].sort((a, b) => fScore[b] - fScore[a])
    for (const fi of fOrder.slice(0, o.fpool ?? ENV_FPOOL)) for (const i of ix.F[fi].idx) pool.add(i)
  }
  if (o.adjacent) for (const i of order.slice(0, 3)) for (const j of [i - 1, i + 1]) if (ix.turns[j] && ix.turns[j].sess === ix.turns[i].sess) pool.add(j)
  // one hop through the fact graph: names the nearest facts mention, that the question did not
  let linked = null
  if (o.link && fOrder) {
    const asked = names(q.question); linked = new Map()
    for (const fi of fOrder.slice(0, 8)) for (const nm of ix.F[fi].names) if (!asked.has(nm)) {
      for (const [gi, g] of ix.F.entries()) if (g.names.has(nm) && gi !== fi) for (const i of g.idx) { pool.add(i); linked.set(i, Math.max(linked.get(i) ?? 0, fScore[fi])) }
    }
  }
  const top3 = new Set(order.slice(0, 3))
  const scored = [...pool].map((i) => {
    let s = tScore[i]
    if (o.fact) { let best = 0; for (const fi of ix.citing.get(i) ?? []) best = Math.max(best, fScore[fi]); s += W.fact * best }
    if (o.adjacent) for (const j of [i - 1, i + 1]) if (top3.has(j) && ix.turns[j].sess === ix.turns[i].sess) s += W.adjacent
    if (o.link && linked?.has(i)) s += W.link * linked.get(i)
    if (o.time && (c.y || c.m) && ix.turns[i].when) { const d = ix.turns[i].when; if ((!c.y || d.y === c.y) && (!c.m || d.m === c.m)) s += W.time }
    return [i, s]
  }).sort((a, b) => b[1] - a[1])
  return scored.slice(0, K).map(([i]) => ix.turns[i].id)
}

const CONFIGS = {
  'single (baseline)':     { pool: K },
  '+fact index':           { fact: true },
  '+fact +adjacent':       { fact: true, adjacent: true },
  '+name link (hurts)':    { fact: true, adjacent: true, link: true },
  '+time (shipped)':       { fact: true, adjacent: true, time: true },
}
export const SHIPPED = { fact: true, adjacent: true, time: true }
const KS = [5, 10, 20]
export const ixs = store.map((c, n) => build(c, corpus[n], factsAll[n]))
export { retrieve, store }
const MAIN = process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())
if (MAIN) {
const pct = (a, b) => ((a / b) * 100).toFixed(1).padStart(5) + '%'
console.log(`\n── LoCoMo recall@${K} · pool and re-rank ──\n`)
console.log(`configuration               single-hop all / any     multi-hop all / any     ms`)
for (const [name, o] of Object.entries(CONFIGS)) {
  if (process.env.ONLY && !name.includes(process.env.ONLY)) continue
  const t = { 1: { n: 0, all: 0, any: 0 }, 4: { n: 0, all: 0, any: 0 } }; let ms = 0, n = 0
  store.forEach((c, ci) => { for (const q of c.qa) { if (!q.evidence.length) continue
    const t0 = process.hrtime.bigint(); const got = new Set(retrieve(ixs[ci], q, o)); ms += Number(process.hrtime.bigint() - t0) / 1e6; n++
    const hits = q.evidence.filter((e) => got.has(e)).length, cell = t[q.category]; if (!cell) continue; cell.n++
    if (hits === q.evidence.length) cell.all++; if (hits > 0) cell.any++ } })
  console.log(`${name.padEnd(26)} ${pct(t[4].all, t[4].n)} / ${pct(t[4].any, t[4].n)}       ${pct(t[1].all, t[1].n)} / ${pct(t[1].any, t[1].n)}      ${(ms / n).toFixed(2)}`)
}
// the shipped configuration at every k, so a number quoted from here carries its k
console.log(`\nshipped (+fact +adjacent +time), by k:      single-hop all / any     multi-hop all / any`)
for (const k of KS) {
  const t = { 1: { n: 0, all: 0, any: 0 }, 4: { n: 0, all: 0, any: 0 } }
  store.forEach((c, ci) => { for (const q of c.qa) { if (!q.evidence.length) continue
    const got = new Set(retrieve(ixs[ci], q, SHIPPED).slice(0, k))
    const hits = q.evidence.filter((e) => got.has(e)).length, cell = t[q.category]; if (!cell) continue; cell.n++
    if (hits === q.evidence.length) cell.all++; if (hits > 0) cell.any++ } })
  console.log(`recall@${String(k).padEnd(2)}                             ${pct(t[4].all, t[4].n)} / ${pct(t[4].any, t[4].n)}       ${pct(t[1].all, t[1].n)} / ${pct(t[1].any, t[1].n)}`)
}
}
