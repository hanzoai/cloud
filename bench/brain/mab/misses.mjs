/**
 * Every miss of a resolver run, put in the bucket where its chain broke.
 *
 * The pooled score says the architecture works; the 262k column says
 * something discrete fails at scale. This reads a run's traces beside the
 * parsed haystack and asks, per missed question: was there a plan; was the
 * base entity found, and how; which hop first returned no versions; did the
 * chain stop because an object was not an entity; and when the chain
 * completed with the wrong answer, is the gold among the versions of the
 * final (entity, relation) — a version choice — or the object of another
 * relation of that entity — a relation choice — or somewhere else entirely —
 * a wrong path earlier. It also prints accuracy hop by hop, so the 33% has a
 * shape rather than a number.
 *
 *   node mab/misses.mjs --run=mab-test-noreader-none [--sizes=mh_32k,mh_64k,mh_262k] [--examples=3]
 */
import { readFileSync, existsSync } from 'node:fs'
import { load, parse, norm, DATA } from './parse.mjs'

const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const RUN = arg('run', 'mab-test-noreader-none'), SIZES = arg('sizes', 'mh_32k,mh_64k,mh_262k').split(','), EX = Number(arg('examples', 3))
const dir = new URL(`../runs/${RUN}/`, import.meta.url).pathname
const preds = new Map(readFileSync(dir + 'predictions.jsonl', 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l)).map((p) => [p.qid, p]))
const traces = new Map(readFileSync(dir + 'traces.jsonl', 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l)).map((t) => [t.qid, t]))
const plans = JSON.parse(readFileSync(DATA + 'plans.json', 'utf8'))
const clean = (s) => norm(String(s)).replace(/[^\p{L}\p{N}\s]/gu, ' ').replace(/\b(a|an|the)\b/g, ' ').replace(/\s+/g, ' ').trim()
const hit = (s, golds) => golds.some((g) => clean(s).includes(clean(g)))

for (const size of SIZES) {
  const row = load().find((r) => r.id === `factconsolidation_${size}`); if (!row) continue
  const { facts } = parse(row.context)
  const bySerial = new Map(facts.map((f) => [f.serial, f]))
  const byKey = new Map(); for (const f of facts) { const k = `${norm(f.subject)}|${f.relation}`; (byKey.get(k) ?? byKey.set(k, []).get(k)).push(f) }
  const byObject = new Map(); for (const f of facts) { const k = clean(f.object); (byObject.get(k) ?? byObject.set(k, []).get(k)).push(f) }
  const buckets = {}, ex = {}, hops = { n: 0, plan: 0, entity: 0, h1: 0, h2: 0, h3: 0, hmax: 0, finalRel: 0, exact: 0 }
  const put = (b, e) => { buckets[b] = (buckets[b] ?? 0) + 1; (ex[b] ??= []).length < EX && ex[b].push(e) }
  row.qa_ids.forEach((qid, i) => {
    const q = row.questions[i], golds = row.answers[i], p = preds.get(qid), t = traces.get(qid), plan = plans[q]
    hops.n++
    const em = p ? p.em : 0; if (em) hops.exact++
    const tr = t?.trace ?? [], hs = tr.filter((s) => s.step === 'hop')
    if (!plan || !p) { if (!em) put('plan missing', { q }); return }
    hops.plan++
    const ent = tr[0]; if (!ent || ent.found == null) { if (!em) put('entity not found', { q, entity: plan.entity }); return }
    hops.entity++
    hs.forEach((h, k) => { if (h.versions.length) { if (k === 0) hops.h1++; if (k === 1) hops.h2++; if (k === 2) hops.h3++ } })
    if (hs.length && hs[hs.length - 1].versions.length) hops.hmax++
    // the final (entity, relation) the plan names, whether the chain got there or not
    const last = hs[hs.length - 1]
    const finalKey = last ? `${last.entity}|${last.relation}` : null
    const finalVersions = last ? (byKey.get(finalKey) ?? []) : []
    const goldInFinal = finalVersions.some((f) => hit(f.object, golds))
    if (goldInFinal) hops.finalRel++
    if (em) return
    const broke = hs.findIndex((h) => !h.versions.length)
    if (broke === 0) { put(`hop 1 miss: no ${hs[0].relation} for the base entity`, { q, entity: ent.found, how: ent.how, chain: plan.chain }); return }
    if (broke > 0) { put(`hop ${broke + 1} miss: no ${hs[broke].relation} for the resolved entity`, { q, at: hs[broke].entity, chain: plan.chain, prev: hs[broke - 1] && { rel: hs[broke - 1].relation, versions: hs[broke - 1].versions.length } }); return }
    if (hs.length < plan.chain.length) { put('chain stopped: an object is not an entity', { q, chain: plan.chain, stopped_after: hs.length, object: bySerial.get(last.current)?.object }); return }
    // complete, wrong answer
    if (goldInFinal) { const cur = bySerial.get(last.current); put('version choice at the final hop (gold is an older or newer version)', { q, key: finalKey, versions: finalVersions.map((f) => `${f.serial}:${f.object}`), chose: cur && `${cur.serial}:${cur.object}`, gold: golds[0] }); return }
    const other = (last ? facts.filter((f) => norm(f.subject) === last.entity && hit(f.object, golds)) : [])
    if (other.length) { put('relation choice at the final hop (gold is another relation of the same entity)', { q, entity: last.entity, planned: last.relation, gold_relations: [...new Set(other.map((f) => f.relation))], chain: plan.chain }); return }
    const goldFacts = byObject.get(clean(golds[0])) ?? []
    const earlier = hs.slice(0, -1).some((h) => h.versions.length > 1)
    if (goldFacts.length) { put(earlier ? 'wrong path: an earlier hop with several versions led elsewhere' : 'wrong path: base entity or plan (gold is the object of a different subject)', { q, entity: ent.found, how: ent.how, chain: plan.chain, gold_subjects: [...new Set(goldFacts.map((f) => `${f.subject}|${f.relation}`))].slice(0, 3), answered: p.pred }); return }
    put('gold is not the object of any fact', { q, gold: golds[0], answered: p.pred })
  })
  const pct = (x) => (100 * x / hops.n).toFixed(0).padStart(3)
  console.log(`\n== ${size} · ${RUN} · ${hops.n} questions · exact ${pct(hops.exact)}%`)
  console.log(`   plan valid ${pct(hops.plan)}%   base entity found ${pct(hops.entity)}%   hop 1 resolved ${pct(hops.h1)}%   hop 2 ${pct(hops.h2)}%   hop 3 ${pct(hops.h3)}%   last hop resolved ${pct(hops.hmax)}%   gold among final (entity, relation) versions ${pct(hops.finalRel)}%   exact ${pct(hops.exact)}%`)
  for (const [b, n] of Object.entries(buckets).sort((a, b) => b[1] - a[1])) { console.log(`   ${String(n).padStart(3)}  ${b}`); for (const e of ex[b]) console.log('        ' + JSON.stringify(e).slice(0, 300)) }
}
