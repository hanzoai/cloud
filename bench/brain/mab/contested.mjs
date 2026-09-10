/**
 * Contested keys and the ceiling of any consistent version policy.
 *
 * For every multi-hop question, the shortest gold path through the parsed
 * store, and for each (subject, relation) on that path with more than one
 * version, which version the question requires. A key is contested when two
 * questions require different versions of it. A policy that chooses one
 * version per key — latest, oldest, or anything derived from the store — can
 * satisfy at most one side of each contest, so the number of questions that
 * only the losing side can answer is a ceiling no such policy passes. With
 * --run, a run's misses are bucketed against those paths: needs an older
 * version, no path at all, or something else.
 *
 *   node mab/contested.mjs [--sizes=6k,32k,64k,262k] [--run=mab-test-beamx-none]
 */
import { readFileSync } from 'node:fs'
import { load, parse, norm, DATA } from './parse.mjs'
const plans = JSON.parse(readFileSync(DATA + 'plans.json', 'utf8'))
const clean = (s) => norm(String(s)).replace(/[^\p{L}\p{N}\s]/gu, ' ').replace(/\b(a|an|the)\b/g, ' ').replace(/\s+/g, ' ').trim()
const hit = (s, golds) => golds.some((g) => clean(s).includes(clean(g)))

/** One haystack: gold paths, contested keys, the two ceilings, and with a run its misses in buckets. */
export function ceiling(size, run) {
  const mh = load().find((r) => r.id === `factconsolidation_mh_${size}`); const { facts } = parse(mh.context)
  const out = new Map(), byKey = new Map()
  for (const f of facts) { const s = norm(f.subject); (out.get(s) ?? out.set(s, []).get(s)).push(f); const kk = `${s}|${f.relation}`; (byKey.get(kk) ?? byKey.set(kk, []).get(kk)).push(f) }
  for (const v of byKey.values()) v.sort((a, b) => a.serial - b.serial)
  const has = new Set(out.keys()), names = [...has]
  const find = (name) => { const c = norm(name).replace(/^the\s+/, ''); if (has.has(norm(name))) return norm(name); if (has.has(c)) return c; let best = null; for (const n of names) if ((n.includes(c) || c.includes(n)) && n.length > 3 && (!best || Math.abs(n.length - c.length) < Math.abs(best.length - c.length))) best = n; return best }
  const need = new Map(), pathOf = new Map() // key -> Map(serial -> [qi]); qi -> gold path
  mh.qa_ids.forEach((qid, i) => {
    const q = mh.questions[i], golds = mh.answers[i], plan = plans[q]; if (!plan) return; const start = find(plan.entity); if (!start) return
    const maxDepth = plan.chain.length + 1, found = []
    const dfs = (e, path, depth, seen) => { if (depth > maxDepth) return; for (const f of out.get(e) ?? []) { if (hit(f.object, golds)) { found.push([...path, f]); continue } const o = norm(f.object); if (!seen.has(o) && has.has(o) && depth < maxDepth) dfs(o, [...path, f], depth + 1, new Set([...seen, o])) } }
    dfs(start, [], 1, new Set([start])); if (!found.length) return
    found.sort((a, b) => a.length - b.length)
    // among the shortest paths, the one that agrees most with "latest wins" (gives the policy its best chance)
    const shortest = found.filter((p) => p.length === found[0].length)
    const latestness = (p) => p.filter((f) => { const vs = byKey.get(`${norm(f.subject)}|${f.relation}`); return vs[vs.length - 1].serial === f.serial }).length
    shortest.sort((a, b) => latestness(b) - latestness(a)); pathOf.set(i, shortest[0])
    for (const f of shortest[0]) { const k = `${norm(f.subject)}|${f.relation}`; if (byKey.get(k).length < 2) continue; const m = need.get(k) ?? need.set(k, new Map()).get(k); (m.get(f.serial) ?? m.set(f.serial, []).get(f.serial)).push(i) }
  })
  let contested = 0; const losers = new Set(), examples = []
  for (const [k, m] of need) { if (m.size < 2) continue; contested++
    const sides = [...m.entries()].sort((a, b) => b[1].length - a[1].length)
    for (const [, qs] of sides.slice(1)) for (const qi of qs) losers.add(qi)
    examples.push(`${k}: ` + sides.map(([s, qs]) => `serial ${s} needed by ${qs.length} q`).join(' vs ')) }
  const latestLosers = new Set(); for (const [k, m] of need) { const vs = byKey.get(k); const last = vs[vs.length - 1].serial; for (const [s, qs] of m) if (s !== last) qs.forEach((qi) => latestLosers.add(qi)) }
  const r = { size, n: mh.qa_ids.length, paths: pathOf.size, versionedKeys: need.size, contested, losers: losers.size, oracleCeiling: mh.qa_ids.length - losers.size, needOlder: latestLosers.size, latestCeiling: mh.qa_ids.length - latestLosers.size, examples }
  if (run) {
    const preds = new Map(readFileSync(new URL(`../runs/${run}/predictions.jsonl`, import.meta.url).pathname, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l)).map((p) => [p.qid, p]))
    const miss = [...mh.qa_ids.keys()].filter((i) => !preds.get(mh.qa_ids[i])?.em)
    r.run = run; r.exact = mh.qa_ids.length - miss.length; r.misses = miss.length; r.missOlder = miss.filter((i) => latestLosers.has(i)).length; r.missNoPath = miss.filter((i) => !pathOf.has(i)).length
    r.missOther = miss.filter((i) => !latestLosers.has(i) && pathOf.has(i)).map((i) => { const p = preds.get(mh.qa_ids[i]); return { i, q: mh.questions[i], gold: mh.answers[i], pred: p?.pred ?? null, plan: plans[mh.questions[i]],
      path: pathOf.get(i).map((f) => { const vs = byKey.get(`${norm(f.subject)}|${f.relation}`); return `${f.subject} --${f.relation}--> ${f.object} [#${f.serial}${vs.length > 1 ? ` ${vs.length} versions, ${vs[vs.length - 1].serial === f.serial ? 'latest' : 'older'}` : ''}]` }).join(' ; ') } })
  }
  return r
}

if (process.argv[1] && /contested\.mjs$/.test(process.argv[1])) {
  const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
  for (const size of arg('sizes', '6k,32k,64k,262k').split(',')) { const r = ceiling(size, arg('run', ''))
    if (r.run) { console.log(`${size} · ${r.run}: misses ${r.misses} · of which need an older version ${r.missOlder} · no gold path ${r.missNoPath} · other ${r.missOther.length}`)
      for (const m of r.missOther) console.log(`   OTHER q${m.i}: ${m.q}\n      gold ${JSON.stringify(m.gold)} · pred ${JSON.stringify(m.pred)} · plan ${JSON.stringify(m.plan?.chain)} from ${JSON.stringify(m.plan?.entity)}\n      path ${m.path}`) }
    console.log(`${size}: gold paths ${r.paths}/${r.n} · versioned keys on gold paths ${r.versionedKeys} · contested keys ${r.contested} · questions on a losing side ${r.losers} → ceiling of any one-version-per-key policy ${r.oracleCeiling} · latest-wins loses ${r.needOlder} → ${r.latestCeiling}`)
    for (const e of r.examples.slice(0, 4)) console.log('   ' + e.slice(0, 200)) }
}
