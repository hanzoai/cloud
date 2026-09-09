/**
 * The numbers every table here is made of, defined once.
 *
 * Retrieval is scored against a set of gold turn ids: ALL (every gold turn in
 * the top k), ANY (at least one), MRR of the first gold turn, NDCG@k with binary
 * gains. Answers are scored with LoCoMo's own token F1 and exact match, and
 * MemoryAgentBench's substring exact match. Every headline carries a bootstrap
 * 95% interval over questions, 1000 resamples, seeded so a rerun agrees.
 */

export const norm = (s) => String(s ?? '').toLowerCase().replace(/\b(a|an|the)\b/g, ' ').replace(/[^\w\s]/g, ' ').split(/\s+/).filter(Boolean)

export function f1(pred, gold) {
  const p = norm(pred), g = norm(gold)
  if (!p.length || !g.length) return Number(p.join(' ') === g.join(' '))
  const gm = new Map(); for (const w of g) gm.set(w, (gm.get(w) ?? 0) + 1)
  let same = 0; for (const w of p) if (gm.get(w) > 0) { same++; gm.set(w, gm.get(w) - 1) }
  if (!same) return 0
  const pr = same / p.length, rc = same / g.length; return (2 * pr * rc) / (pr + rc)
}
export const em = (pred, gold) => norm(pred).join(' ') === norm(gold).join(' ')
/** MemoryAgentBench: the normalised gold appears inside the normalised prediction. */
export const substringEm = (pred, golds) => (Array.isArray(golds) ? golds : [golds]).some((g) => norm(pred).join(' ').includes(norm(g).join(' ')))

/** Recall-style scores for one ranked list against a gold set. */
export function rank(ids, gold, ks = [5, 10, 20]) {
  const G = new Set(gold), out = {}
  for (const k of ks) { const top = new Set(ids.slice(0, k)); const hits = gold.filter((g) => top.has(g)).length; out[`all@${k}`] = Number(hits === gold.length); out[`any@${k}`] = Number(hits > 0); out[`recall@${k}`] = gold.length ? hits / gold.length : 0 }
  const first = ids.findIndex((id) => G.has(id)); out.mrr = first < 0 ? 0 : 1 / (first + 1)
  for (const k of ks) { let dcg = 0; ids.slice(0, k).forEach((id, i) => { if (G.has(id)) dcg += 1 / Math.log2(i + 2) })
    let ideal = 0; for (let i = 0; i < Math.min(gold.length, k); i++) ideal += 1 / Math.log2(i + 2)
    out[`ndcg@${k}`] = ideal ? dcg / ideal : 0 }
  return out
}

/** Mean of a column, with a seeded bootstrap 95% interval. */
export function ci(values, resamples = 1000, seed = 7) {
  const n = values.length; if (!n) return { mean: 0, lo: 0, hi: 0, n: 0 }
  const mean = values.reduce((a, b) => a + b, 0) / n
  let s = seed >>> 0; const rnd = () => { s ^= s << 13; s >>>= 0; s ^= s >>> 17; s ^= s << 5; s >>>= 0; return s / 4294967296 }
  const means = []
  for (let r = 0; r < resamples; r++) { let t = 0; for (let i = 0; i < n; i++) t += values[(rnd() * n) | 0]; means.push(t / n) }
  means.sort((a, b) => a - b)
  return { mean, lo: means[Math.floor(0.025 * resamples)], hi: means[Math.ceil(0.975 * resamples) - 1], n }
}

export const pct = (x, d = 1) => (x * 100).toFixed(d)
export const quantile = (xs, q) => { const s = [...xs].sort((a, b) => a - b); return s.length ? s[Math.min(s.length - 1, Math.floor(q * s.length))] : 0 }
