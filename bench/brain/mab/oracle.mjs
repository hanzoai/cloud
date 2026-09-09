/**
 * Oracle decomposition of the multi-hop misses: how far a perfect planner,
 * a perfect entity lookup and a perfect version choice would each get.
 *
 * For each multi-hop question a bounded search over the parsed fact graph
 * (subject → object edges labelled by relation, every version) looks for any
 * path from the plan's base entity to the gold within chain-length + 1 hops.
 * If one exists, the question is reachable by planning alone; the relation
 * sequence of the shortest such path is compared with the plan's chain. If
 * none exists from that entity, the search is repeated from every entity whose
 * name appears in the question. What remains is unreachable in the store.
 *
 *   node mab/oracle.mjs [--sizes=mh_6k,mh_32k,mh_64k,mh_262k] [--examples=3]
 */
import { readFileSync } from 'node:fs'
import { load, parse, norm, DATA } from './parse.mjs'
const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const SIZES = arg('sizes', 'mh_6k,mh_32k,mh_64k,mh_262k').split(','), EX = Number(arg('examples', 3)), RUN = arg('run', 'noreader')
const plans = JSON.parse(readFileSync(DATA + 'plans.json', 'utf8'))
const clean = (s) => norm(String(s)).replace(/[^\p{L}\p{N}\s]/gu, ' ').replace(/\b(a|an|the)\b/g, ' ').replace(/\s+/g, ' ').trim()
const hit = (s, golds) => golds.some((g) => clean(s).includes(clean(g)))

for (const size of SIZES) {
  const row = load().find((r) => r.id === `factconsolidation_${size}`); if (!row) continue
  const { facts } = parse(row.context)
  const out = new Map(); for (const f of facts) { const k = norm(f.subject); (out.get(k) ?? out.set(k, []).get(k)).push(f) }
  const has = new Set(out.keys()), names = [...has]
  const find = (name) => { const c = norm(name).replace(/^the\s+/, ''); if (has.has(norm(name))) return norm(name); if (has.has(c)) return c; let best = null; for (const n of names) if ((n.includes(c) || c.includes(n)) && n.length > 3 && (!best || Math.abs(n.length - c.length) < Math.abs(best.length - c.length))) best = n; return best }
  // shortest relation path from an entity to the gold, any version at each step
  const search = (start, golds, maxDepth) => { const seen = new Set([start]); let frontier = [[start, []]]
    for (let d = 0; d < maxDepth; d++) { const next = []
      for (const [e, path] of frontier) for (const f of out.get(e) ?? []) { if (hit(f.object, golds)) return [...path, f.relation]; const o = norm(f.object); if (!seen.has(o) && has.has(o)) { seen.add(o); next.push([o, [...path, f.relation]]) } }
      frontier = next; if (!frontier.length) break }
    return null }
  const c = { n: 0, noPlan: 0, exact: 0, planPath: 0, planPathSameRels: 0, planPathOtherRels: 0, fromOtherEntity: 0, unreachable: 0 }
  const ex = { other: [], unreachable: [], entity: [] }
  const preds = new Map(readFileSync(new URL(`../runs/mab-${size.endsWith('6k') ? 'dev' : 'test'}-${RUN}-none/predictions.jsonl`, import.meta.url).pathname, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l)).map((p) => [p.qid, p]))
  row.qa_ids.forEach((qid, i) => {
    const q = row.questions[i], golds = row.answers[i], plan = plans[q]; c.n++
    if (preds.get(qid)?.em) { c.exact++; return }
    if (!plan) { c.noPlan++; return }
    const base = find(plan.entity)
    const path = base ? search(base, golds, plan.chain.length + 1) : null
    if (path) { c.planPath++; if (path.join(',') === plan.chain.join(',')) c.planPathSameRels++; else { c.planPathOtherRels++; ex.other.length < EX && ex.other.push({ q: q.slice(0, 90), plan: plan.chain, found: path, base }) } return }
    // from any entity the question names
    const ql = norm(q); const cands = names.filter((n) => n.length > 3 && ql.includes(n)).sort((a, b) => b.length - a.length).slice(0, 6)
    for (const e of cands) { const p2 = search(e, golds, plan.chain.length + 2); if (p2) { c.fromOtherEntity++; ex.entity.length < EX && ex.entity.push({ q: q.slice(0, 90), plan_entity: plan.entity, base, from: e, path: p2 }); return } }
    c.unreachable++; ex.unreachable.length < EX && ex.unreachable.push({ q: q.slice(0, 90), gold: golds[0], plan })
  })
  console.log(`\n== ${size}: exact ${c.exact}  | of the ${c.n - c.exact} misses: no plan ${c.noPlan} · a path from the base entity exists ${c.planPath} (same relations as the plan ${c.planPathSameRels}, other relations ${c.planPathOtherRels}) · only from another entity in the question ${c.fromOtherEntity} · unreachable in the store ${c.unreachable}`)
  console.log(`   ceiling: perfect version choice on the plan's own chain ≤ ${c.exact + c.planPathSameRels}; perfect plan ≤ ${c.exact + c.planPath}; perfect plan + entity ≤ ${c.exact + c.planPath + c.fromOtherEntity}`)
  for (const [k, v] of Object.entries(ex)) for (const e of v) console.log(`   ${k}: ${JSON.stringify(e).slice(0, 260)}`)
}
