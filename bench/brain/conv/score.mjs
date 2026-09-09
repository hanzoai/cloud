/**
 * LoCoMo-Conv scoring, as the paper defines it.
 *
 * Retrieval recall for one item is |retrieved ∩ gold| / |gold|, and a gold turn
 * counts as retrieved when its verbatim text appears, case-insensitively, inside
 * any returned memory. With raw turns as memories that is an id match; with
 * summaries or chunks it is a containment test, so both are done here.
 * ALL and ANY are kept beside it — they are what the rest of this bench reports.
 */
export function recallOf(returned, goldTurns) {
  if (!goldTurns.length) return null
  const bodies = returned.map((m) => (m.text ?? '').toLowerCase())
  const hit = goldTurns.filter((g) => returned.some((m) => m.id === g.id) || bodies.some((b) => g.text && b.includes(g.text.toLowerCase())))
  return { recall: hit.length / goldTurns.length, all: hit.length === goldTurns.length, any: hit.length > 0, hit: hit.length, gold: goldTurns.length }
}
/** Mean of a field, with a bootstrap 95% interval over items. */
export function summary(rows, field = 'recall', resamples = 1000) {
  const xs = rows.map((r) => Number(r[field])).filter((x) => !Number.isNaN(x)); const n = xs.length
  if (!n) return { n: 0 }
  const mean = xs.reduce((a, b) => a + b, 0) / n
  let seed = 7; const rnd = () => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed / 0x7fffffff }
  const boots = []
  for (let b = 0; b < resamples; b++) { let s = 0; for (let i = 0; i < n; i++) s += xs[Math.floor(rnd() * n)]; boots.push(s / n) }
  boots.sort((a, b) => a - b)
  return { n, mean, lo: boots[Math.floor(resamples * 0.025)], hi: boots[Math.floor(resamples * 0.975)] }
}
export const fmt = (s) => s.n ? `${(s.mean * 100).toFixed(1)} [${(s.lo * 100).toFixed(1)}, ${(s.hi * 100).toFixed(1)}]` : '-'
