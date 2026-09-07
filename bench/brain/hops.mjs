/**
 * Multi-hop retrieval, and whether it closes the gap the baseline exposed.
 *
 * The baseline said exactly what was wrong. At recall@20 on LoCoMo, "any" was
 * 80.1% and "all" was 22.7%: a single vector search finds ONE of the turns a
 * multi-hop question needs, nearly every time, and all of them almost never.
 * That is not an embedding problem — the right turns are in reach, they are
 * just not all near the question in one hop.
 *
 * The published fix is to search more than once and let the first hop inform
 * the second (pseudo-relevance feedback; IRCoT). Four strategies here, each a
 * few lines over the same index, so the comparison is the retrieval policy and
 * nothing else:
 *
 *   single    one search on the question               — the baseline
 *   prf       question vector nudged toward its own top hits, searched again
 *   multi     search from each of the top hits, union the results
 *   chain     multi, then one more hop from what that found
 *
 * Nothing here calls a model. A hop is a vector search, so the cost is
 * microseconds and the whole thing stays inside the store.
 */
import { readFileSync, existsSync } from 'node:fs'

const CACHE = 'brain-vectors.json'
if (!existsSync(CACHE)) {
  console.error(`no ${CACHE} — run brain.mjs first to embed the corpus`)
  process.exit(1)
}
const store = JSON.parse(readFileSync(CACHE, 'utf8'))

const dot = (a, b) => {
  let s = 0
  for (let i = 0; i < a.length; i++) s += a[i] * b[i]
  return s
}
const norm = (v) => Math.sqrt(dot(v, v))
const cos = (a, b) => dot(a, b) / (norm(a) * norm(b))

/** Rank every turn in one conversation against a vector. */
const rank = (turns, v) =>
  turns.map((t) => ({ id: t.id, v: t.v, s: cos(v, t.v) })).sort((a, b) => b.s - a.s)

const blend = (a, b, w) => a.map((x, i) => x * (1 - w) + b[i] * w)

/**
 * Each strategy returns an ordered list of ids, longest-first by confidence.
 * `k` is the budget: how many turns the caller is willing to read.
 */
const STRATEGIES = {
  single: (turns, q, k) => rank(turns, q.v).slice(0, k).map((r) => r.id),

  // Rocchio, in vector space: pull the query toward what it already found.
  prf: (turns, q, k, { feedback = 3, weight = 0.4 } = {}) => {
    const first = rank(turns, q.v)
    const top = first.slice(0, feedback)
    const centroid = top[0].v.map(
      (_, i) => top.reduce((s, t) => s + t.v[i], 0) / top.length
    )
    const second = rank(turns, blend(q.v, centroid, weight))
    // Keep the first hop's best, then fill from the second.
    const out = []
    for (const r of [...first.slice(0, Math.ceil(k / 2)), ...second]) {
      if (!out.includes(r.id)) out.push(r.id)
      if (out.length === k) break
    }
    return out
  },

  // Each of the top hits asks its own question. Union, ordered by best score.
  multi: (turns, q, k, { hops = 3 } = {}) => {
    const first = rank(turns, q.v)
    const best = new Map()
    for (const r of first) best.set(r.id, r.s)
    for (const seed of first.slice(0, hops)) {
      for (const r of rank(turns, seed.v)) {
        // A neighbour is worth the seed's confidence times its own.
        const score = seed.s * r.s
        if (score > (best.get(r.id) ?? -1)) best.set(r.id, score)
      }
    }
    return [...best.entries()]
      .sort((a, b) => b[1] - a[1])
      .slice(0, k)
      .map(([id]) => id)
  },

  // One more hop from what multi found: the shape a three-fact question needs.
  chain: (turns, q, k, { hops = 3, depth = 2 } = {}) => {
    const byId = new Map(turns.map((t) => [t.id, t]))
    let frontier = rank(turns, q.v).slice(0, hops)
    const best = new Map()
    for (const r of rank(turns, q.v)) best.set(r.id, r.s)

    for (let d = 0; d < depth; d++) {
      const next = []
      for (const seed of frontier) {
        const found = rank(turns, byId.get(seed.id).v).slice(0, hops)
        for (const r of found) {
          const score = seed.s * r.s
          if (score > (best.get(r.id) ?? -1)) {
            best.set(r.id, score)
            next.push({ id: r.id, s: score })
          }
        }
      }
      frontier = next.sort((a, b) => b.s - a.s).slice(0, hops)
      if (!frontier.length) break
    }
    return [...best.entries()]
      .sort((a, b) => b[1] - a[1])
      .slice(0, k)
      .map(([id]) => id)
  },
}

const KS = [5, 10, 20]
const results = {}

for (const [name, fn] of Object.entries(STRATEGIES)) {
  const tally = { 1: { n: 0, all: {}, any: {} }, 4: { n: 0, all: {}, any: {} } }
  for (const k of KS) for (const c of [1, 4]) (tally[c].all[k] = 0), (tally[c].any[k] = 0)
  const times = []

  for (const conv of store) {
    for (const q of conv.qa) {
      if (!q.evidence?.length) continue
      const cell = tally[q.category]
      cell.n++
      for (const k of KS) {
        const t0 = process.hrtime.bigint()
        const got = new Set(fn(conv.turns, q, k))
        if (k === 20) times.push(Number(process.hrtime.bigint() - t0) / 1e6)
        const hits = q.evidence.filter((e) => got.has(e)).length
        if (hits === q.evidence.length) cell.all[k]++
        if (hits > 0) cell.any[k]++
      }
    }
  }
  results[name] = { tally, ms: times.reduce((a, b) => a + b, 0) / times.length }
}

const pct = (a, b) => ((a / b) * 100).toFixed(1)
const base = results.single.tally

console.log(`\n── Multi-hop retrieval on LoCoMo ──\n`)
console.log(`                 single-hop (cat 4)          multi-hop (cat 1)`)
console.log(`strategy      @5    @10   @20    Δ      @5    @10   @20    Δ      ms`)
for (const [name, r] of Object.entries(results)) {
  const s = KS.map((k) => pct(r.tally[4].all[k], r.tally[4].n).padStart(5)).join(' ')
  const m = KS.map((k) => pct(r.tally[1].all[k], r.tally[1].n).padStart(5)).join(' ')
  const ds = (pct(r.tally[4].all[20], r.tally[4].n) - pct(base[4].all[20], base[4].n)).toFixed(1)
  const dm = (pct(r.tally[1].all[20], r.tally[1].n) - pct(base[1].all[20], base[1].n)).toFixed(1)
  console.log(
    `${name.padEnd(10)} ${s} ${String(ds >= 0 ? '+' + ds : ds).padStart(6)} ${m} ${String(dm >= 0 ? '+' + dm : dm).padStart(6)}   ${r.ms.toFixed(2)}`
  )
}
console.log('\n(all = every gold turn retrieved. Delta is against single-hop at k=20.)')

const bestMulti = Object.entries(results).sort(
  (a, b) => b[1].tally[1].all[20] - a[1].tally[1].all[20]
)[0]
console.log(
  `\nbest multi-hop: ${bestMulti[0]} at ${pct(bestMulti[1].tally[1].all[20], bestMulti[1].tally[1].n)}% ` +
    `(baseline ${pct(base[1].all[20], base[1].n)}%)`
)
