/**
 * Ranked turn ids per question for one retrieval policy — the retrieval half
 * of every answer row. run.mjs imports it; the CLI prints the same as JSON.
 *
 * `oracle` hands back the annotated evidence itself, in conversation order. It
 * is the ceiling: what a reader scores when retrieval is perfect, so the gap
 * between a policy and this row is retrieval's share of the error and the rest
 * belongs to the reader or the benchmark.
 *
 * k above 20 needs K=<k> in the environment, which is what cer2.mjs cuts at.
 */
import { ixs, retrieve, store, SHIPPED } from './cer2.mjs'

export const POLICIES = {
  single: (k) => (ix, q) => retrieve(ix, q, { pool: k }).slice(0, k),
  cer: (k) => (ix, q) => retrieve(ix, q, SHIPPED).slice(0, k),
  oracle: () => (ix, q) => {
    const at = new Map(ix.turns.map((t, i) => [t.id, i]))
    return [...new Set(q.evidence)].filter((e) => at.has(e)).sort((a, b) => at.get(a) - at.get(b))
  },
}

/** ids per [conversation][question]; a question without evidence gets none. */
export function rankAll(policy, k) {
  const f = POLICIES[policy]?.(k)
  if (!f) throw new Error(`no policy ${policy}; one of ${Object.keys(POLICIES).join(', ')}`)
  return store.map((c, ci) => c.qa.map((q) => (q.evidence.length ? f(ixs[ci], q) : [])))
}
export { store }

const MAIN = process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())
if (MAIN) {
  const arg = (key, d) => { const m = process.argv.find((a) => a.startsWith(`--${key}=`)); return m ? m.split('=')[1] : d }
  process.stdout.write(JSON.stringify(rankAll(arg('policy', 'cer'), Number(arg('k', 20)))))
}
