/**
 * Score a run: token-F1 and exact match with bootstrap intervals, and the
 * three evidence grades a question can earn.
 *
 *   EXACT      an annotated gold turn was in the context
 *   SUPPORTED  a context turn contains the gold answer string
 *   ANSWERED   the reader's answer is right (F1 ≥ 0.5, or exact)
 *
 * Recall of annotated turns is the unit test; ANSWERED is the score. A system
 * can be SUPPORTED and ANSWERED without EXACT — it found the fact somewhere
 * the annotator did not mark — and that is a hit, not a miss.
 *
 *   node score.mjs runs/<name> [runs/<other> ...]    one table per run
 *   node score.mjs --all                             every run under runs/
 */
import { readFileSync, readdirSync, existsSync, statSync } from 'node:fs'
import { f1, em, norm } from './answer.mjs'

export const CATS = { 1: 'multi-hop', 2: 'temporal', 3: 'open-domain', 4: 'single-hop' }
export const SPLITS = { all: () => true, dev: (ci) => ci < 3, test: (ci) => ci >= 3 }

/** Percentile bootstrap of a mean: 1000 resamples over questions, a seeded LCG so it is repeatable. */
export function bootstrap(values, n = 1000, seed = 7) {
  if (!values.length) return { mean: 0, lo: 0, hi: 0 }
  let s = seed >>> 0; const rnd = () => { s = (Math.imul(1664525, s) + 1013904223) >>> 0; return s / 4294967296 }
  const means = []
  for (let r = 0; r < n; r++) { let sum = 0; for (let i = 0; i < values.length; i++) sum += values[Math.floor(rnd() * values.length)]; means.push(sum / values.length) }
  means.sort((a, b) => a - b)
  const mean = values.reduce((a, b) => a + b, 0) / values.length
  return { mean, lo: means[Math.floor(0.025 * n)], hi: means[Math.ceil(0.975 * n) - 1] }
}

export function readRows(dir) {
  const p = `${dir}/predictions.jsonl`
  if (!existsSync(p)) return []
  return readFileSync(p, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l))
}

/** Grade one prediction against the store. */
export function grade(row, store) {
  const q = store[row.ci].qa[row.qi], ctx = new Set(row.ctx ?? [])
  const byId = new Map(store[row.ci].turns.map((t) => [t.id, t]))
  const exact = q.evidence.some((e) => ctx.has(e))
  const gold = String(row.gold ?? '').trim().toLowerCase()
  const supported = gold.length >= 3 && [...ctx].some((id) => (byId.get(id)?.text ?? '').toLowerCase().includes(gold))
  const answered = row.em || row.f1 >= 0.5
  return { exact, supported, answered }
}

/** Metrics for a run: per category and overall, per split, with intervals and grades. */
export function scoreRows(rows, store, expected) {
  const out = {}
  for (const [split, keep] of Object.entries(SPLITS)) {
    out[split] = {}
    const groups = { overall: rows.filter((r) => keep(r.ci)) }
    for (const c of Object.keys(CATS)) groups[CATS[c]] = rows.filter((r) => keep(r.ci) && r.cat === Number(c))
    for (const [name, rs] of Object.entries(groups)) {
      const g = rs.map((r) => grade(r, store))
      const n = rs.length, want = expected ? expected(split, name) : n
      out[split][name] = {
        n, of: want,
        f1: bootstrap(rs.map((r) => r.f1)), em: bootstrap(rs.map((r) => Number(r.em))),
        exact: n ? g.filter((x) => x.exact).length / n : 0,
        supported: n ? g.filter((x) => x.supported).length / n : 0,
        answered: n ? g.filter((x) => x.answered).length / n : 0,
        tokens: n ? rs.reduce((a, r) => a + (r.tokens ?? 0), 0) / n : 0,
      }
    }
  }
  return out
}

/** How many questions a run should hold per split and group, so a partial run says so. */
export function expectedCounts(store, cats) {
  return (split, name) => {
    let n = 0
    store.forEach((c, ci) => { if (!SPLITS[split](ci)) return
      for (const q of c.qa) if (q.evidence.length && cats.includes(q.category) && (name === 'overall' || CATS[q.category] === name)) n++ })
    return n
  }
}

const pct = (x) => (x * 100).toFixed(1).padStart(5)
export function table(name, m, split = 'all') {
  const lines = [`${name} · ${split}`, `  ${'category'.padEnd(12)} ${'n'.padStart(9)}   F1 [95% CI]          EM     EXACT  SUPP   ANSW   tok/q`]
  for (const [g, r] of Object.entries(m[split])) {
    lines.push(`  ${g.padEnd(12)} ${String(r.n).padStart(4)}/${String(r.of).padEnd(4)}  ${pct(r.f1.mean)} [${pct(r.f1.lo)},${pct(r.f1.hi)}]  ${pct(r.em.mean)}  ${pct(r.exact)}  ${pct(r.supported)}  ${pct(r.answered)}  ${r.tokens.toFixed(0).padStart(5)}`)
  }
  return lines.join('\n')
}

const MAIN = process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())
if (MAIN) {
  const store = JSON.parse(readFileSync('brain-vectors.json', 'utf8'))
  const dirs = process.argv.includes('--all')
    ? readdirSync('runs').map((d) => `runs/${d}`).filter((d) => statSync(d).isDirectory() && d.includes('locomo')).sort()
    : process.argv.slice(2).filter((a) => !a.startsWith('--'))
  const split = (process.argv.find((a) => a.startsWith('--split=')) ?? '--split=all').split('=')[1]
  for (const d of dirs) {
    const rows = readRows(d); if (!rows.length) { console.log(`${d}: no predictions`); continue }
    const meta = existsSync(`${d}/meta.json`) ? JSON.parse(readFileSync(`${d}/meta.json`, 'utf8')) : {}
    const m = scoreRows(rows, store, expectedCounts(store, meta.cats ?? [1, 2, 3, 4]))
    console.log(table(d.replace(/^runs\//, ''), m, split) + '\n')
  }
}
