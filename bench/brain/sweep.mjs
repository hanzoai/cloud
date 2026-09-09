/**
 * The parameter search, on the dev split only.
 *
 * Coordinate descent over the scorer's weights and the generators' budgets:
 * each parameter is moved through its grid while the others hold, the best
 * value is kept, and the pass repeats until nothing moves. The objective is the
 * mean over categories of (R@K ALL + nDCG@K) / 2 — ALL so that a multi-hop
 * question needs all of its turns, nDCG so that a gold turn at rank 2 counts
 * for more than one at rank 19. Every evaluation is written to
 * ablations/sweep-<facts>.json, so the search itself is a table, not a story.
 *
 * The chosen configuration is printed and written to ablations/frozen-<facts>.json
 * with the commit hash. context.mjs and run.mjs read that file when asked to
 * run the frozen configuration on test. Nothing here reads the test split.
 *
 *   node sweep.mjs [--facts=locomo|ours|union] [--rows=+adjacency,+iterative hops] [--fast]
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { execSync } from 'node:child_process'
import { evaluate, ROWS, DEFAULT_W, BUDGET, K } from './context.mjs'

const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.slice(k.length + 3) : d }
const FACTS = arg('facts', process.env.FACTS ?? 'locomo')
const rowName = arg('rows', '+iterative hops').split(',')[0].trim()
const base = ROWS[rowName]; if (!base) { console.error(`no row ${rowName}`); process.exit(1) }
mkdirSync(new URL('./ablations/', import.meta.url), { recursive: true })

const GRID = {
  'w.fact': [0.1, 0.15, 0.2, 0.25, 0.3, 0.4],
  'w.lex': [0, 0.05, 0.1, 0.15, 0.2, 0.3],
  'w.ent': [0, 0.05, 0.1, 0.2],
  'w.time': [0, 0.03, 0.05, 0.08, 0.12],
  'w.adj': [0, 0.03, 0.06, 0.1],
  'w.exp': [0, 0.04, 0.08, 0.15],
  'w.hop': [0, 0.1, 0.2, 0.3, 0.45],
  'w.facet': [0, 0.1, 0.2, 0.3],
  'budget.dense': [20, 30, 40, 60],
  'budget.fact': [10, 20, 30],
  'budget.lex': [10, 20, 30],
  'budget.hop': [5, 10, 15],
}
const active = (name) => { const [kind, key] = name.split('.'); if (kind === 'w') return !!base[key] || key === 'fact' && base.fact; return true }
const params = Object.keys(GRID).filter(active).filter((p) => !process.argv.includes('--fast') || !p.startsWith('budget'))

const objective = (summary) => { const cats = Object.keys(summary).filter((c) => c !== 'all'); return cats.reduce((a, c) => a + (summary[c][`all@${K}`].mean + summary[c][`ndcg@${K}`].mean) / 2, 0) / cats.length }
const table = []
async function score(w, budget) {
  const cfg = { ...base, w, budget }
  const { summary } = await evaluate(cfg, 'dev')
  const obj = objective(summary); table.push({ w: { ...w }, budget: { ...budget }, objective: obj, summary: Object.fromEntries(Object.entries(summary).map(([c, s]) => [c, { all: s[`all@${K}`].mean, any: s[`any@${K}`].mean, ndcg: s[`ndcg@${K}`].mean, mrr: s.mrr.mean }])) })
  return obj
}

let w = { ...DEFAULT_W }, budget = { ...BUDGET }
let best = await score(w, budget)
console.log(`\n── sweep on dev · row "${rowName}" · facts ${FACTS} · start ${(best * 100).toFixed(2)} ──`)
for (let pass = 1; pass <= 4; pass++) {
  let moved = false
  for (const p of params) {
    const [kind, key] = p.split('.'); const cur = kind === 'w' ? w[key] : budget[key]
    for (const v of GRID[p]) { if (v === cur) continue
      const w2 = { ...w }, b2 = { ...budget }; if (kind === 'w') w2[key] = v; else b2[key] = v
      const s = await score(w2, b2)
      if (s > best + 1e-6) { best = s; w = w2; budget = b2; moved = true; console.log(`  pass ${pass}: ${p} → ${v}  objective ${(best * 100).toFixed(2)}`) } }
  }
  writeFileSync(new URL(`./ablations/sweep-${FACTS}.json`, import.meta.url), JSON.stringify({ row: rowName, facts: FACTS, k: K, objective: 'mean over categories of (R@K ALL + nDCG@K)/2 on dev', evaluations: table }, null, 1))
  if (!moved) break
}
const commit = execSync('git rev-parse --short=12 HEAD', { cwd: new URL('.', import.meta.url).pathname }).toString().trim()
const frozen = { row: rowName, facts: FACTS, k: K, w, budget, objective: best, commit, evaluations: table.length }
writeFileSync(new URL(`./ablations/frozen-${FACTS}.json`, import.meta.url), JSON.stringify(frozen, null, 1))
console.log(`\nfrozen (${table.length} evaluations): w=${JSON.stringify(w)} budget=${JSON.stringify(budget)} objective ${(best * 100).toFixed(2)} · commit ${commit}`)
