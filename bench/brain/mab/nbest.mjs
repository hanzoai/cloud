/**
 * Best-of-N against selected-of-N, from a beam run's traces.
 *
 * Plan Recall@N: some plan among the first N sets, executed, reaches the gold.
 * Execution Success@N: the plan the score selected among those N is right.
 * Selection Accuracy@N: given that some plan among N is right, the selected one is.
 * Sets are taken in the lane's order: plans.json (the model at T=0) first, then
 * plans-<tag>.json alphabetically; N grows one set at a time. If recall@N climbs
 * and success@N does not, selection is the problem; if recall@N is flat, the
 * compiler lacks diversity.
 *
 *   node mab/nbest.mjs --run=mab-test-beam-none [--sizes=mh_32k,mh_64k,mh_262k]
 */
import { readFileSync } from 'node:fs'
import { load, norm } from './parse.mjs'
const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const RUN = arg('run', 'mab-test-beam-none'), SIZES = arg('sizes', 'mh_6k,mh_32k,mh_64k,mh_262k').split(',')
const dir = new URL(`../runs/${RUN}/`, import.meta.url).pathname
const traces = new Map(readFileSync(dir + 'traces.jsonl', 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l)).map((t) => [t.qid, t]))
const clean = (s) => norm(String(s)).replace(/[^\p{L}\p{N}\s]/gu, ' ').replace(/\b(a|an|the)\b/g, ' ').replace(/\s+/g, ' ').trim()
const hit = (s, golds) => s != null && golds.some((g) => clean(s).includes(clean(g)))
for (const size of SIZES) {
  const row = load().find((r) => r.id === `factconsolidation_${size}`); if (!row) continue
  const sets = []; for (const t of traces.values()) for (const c of (t.trace?.[0]?.candidates ?? [])) if (!sets.includes(c.set)) sets.push(c.set)
  const order = ['default', ...sets.filter((s) => s !== 'default').sort()].filter((s) => sets.includes(s))
  const n = row.qa_ids.length; const recall = order.map(() => 0), success = order.map(() => 0); let plansPerQ = 0
  row.qa_ids.forEach((qid, i) => { const golds = row.answers[i], t = traces.get(qid); const cands = t?.trace?.[0]?.candidates ?? []; plansPerQ += cands.length
    for (let k = 1; k <= order.length; k++) { const upto = new Set(order.slice(0, k)); const among = cands.filter((c) => upto.has(c.set))
      if (among.some((c) => hit(c.answer, golds))) recall[k - 1]++
      const sel = among.filter((c) => c.score != null).sort((a, b) => b.score - a.score)[0]; if (sel && hit(sel.answer, golds)) success[k - 1]++ } })
  // unique solves: questions only one set's plan reaches
  const solvedBy = {}; for (const s of order) solvedBy[s] = 0; const solvedAny = new Set()
  row.qa_ids.forEach((qid, i) => { const golds = row.answers[i], cands = traces.get(qid)?.trace?.[0]?.candidates ?? []; const winners = order.filter((s) => cands.some((c) => c.set === s && hit(c.answer, golds))); if (winners.length === 1) solvedBy[winners[0]]++; if (winners.length) solvedAny.add(qid) })
  console.log(`\n== ${size} · ${RUN} · ${n} questions · sets in order: ${order.join(' → ')} · plans/question ${(plansPerQ / n).toFixed(2)} · solved by any set ${solvedAny.size} · unique solves: ${order.map((s) => `${s} ${solvedBy[s]}`).join(', ')}`)
  console.log('   N   sets                          plan recall@N   execution success@N   selection accuracy@N')
  order.forEach((s, k) => console.log(`   ${String(k + 1).padStart(1)}   ${order.slice(0, k + 1).join('+').padEnd(28)} ${String(recall[k]).padStart(8)}%        ${String(success[k]).padStart(10)}%          ${(recall[k] ? 100 * success[k] / recall[k] : 0).toFixed(1).padStart(12)}%`))
}
