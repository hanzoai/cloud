/**
 * Contested keys and the ceiling of any consistent version policy.
 *
 * For every multi-hop question, the shortest gold path through the parsed
 * store, and for each (subject, relation) on that path with more than one
 * version, which version the question requires. A key is contested when two
 * questions require different versions of it. A policy that chooses one
 * version per key — latest, oldest, or anything derived from the store — can
 * satisfy at most one side of each contest, so the number of questions that
 * only the losing side can answer is a ceiling no such policy passes.
 *
 *   node mab/contested.mjs [--sizes=6k,32k,64k,262k] [--run=mab-test-beamx-none]
 */
import { readFileSync } from 'node:fs'
import { load, parse, norm, DATA } from './parse.mjs'
const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const RUN = arg('run', '')
const plans = JSON.parse(readFileSync(DATA + 'plans.json', 'utf8'))
const clean = (s) => norm(String(s)).replace(/[^\p{L}\p{N}\s]/gu, ' ').replace(/\b(a|an|the)\b/g, ' ').replace(/\s+/g, ' ').trim()
const hit = (s, golds) => golds.some((g) => clean(s).includes(clean(g)))
for (const size of arg('sizes', '6k,32k,64k,262k').split(',')) {
  const mh = load().find((r) => r.id === `factconsolidation_mh_${size}`); const { facts } = parse(mh.context)
  const out = new Map(), byKey = new Map()
  for (const f of facts) { const s = norm(f.subject); (out.get(s) ?? out.set(s, []).get(s)).push(f); const kk = `${s}|${f.relation}`; (byKey.get(kk) ?? byKey.set(kk, []).get(kk)).push(f) }
  for (const v of byKey.values()) v.sort((a, b) => a.serial - b.serial)
  const has = new Set(out.keys()), names = [...has]
  const find = (name) => { const c = norm(name).replace(/^the\s+/, ''); if (has.has(norm(name))) return norm(name); if (has.has(c)) return c; let best = null; for (const n of names) if ((n.includes(c) || c.includes(n)) && n.length > 3 && (!best || Math.abs(n.length - c.length) < Math.abs(best.length - c.length))) best = n; return best }
  const need = new Map(), pathOf = new Map() // key -> Map(serial -> [qi])
  let paths = 0, withVersioned = 0
  mh.qa_ids.forEach((qid, i) => {
    const q = mh.questions[i], golds = mh.answers[i], plan = plans[q]; if (!plan) return; const start = find(plan.entity); if (!start) return
    const maxDepth = plan.chain.length + 1, found = []
    const dfs = (e, path, depth, seen) => { if (depth > maxDepth) return; for (const f of out.get(e) ?? []) { if (hit(f.object, golds)) { found.push([...path, f]); continue } const o = norm(f.object); if (!seen.has(o) && has.has(o) && depth < maxDepth) dfs(o, [...path, f], depth + 1, new Set([...seen, o])) } }
    dfs(start, [], 1, new Set([start])); if (!found.length) return
    paths++
    found.sort((a, b) => a.length - b.length)
    // among the shortest paths, the one that agrees most with "latest wins" (gives the policy its best chance)
    const shortest = found.filter((p) => p.length === found[0].length)
    const latestness = (p) => p.filter((f) => { const vs = byKey.get(`${norm(f.subject)}|${f.relation}`); return vs[vs.length - 1].serial === f.serial }).length
    shortest.sort((a, b) => latestness(b) - latestness(a)); pathOf.set(i, shortest[0])
    let any = false
    for (const f of shortest[0]) { const k = `${norm(f.subject)}|${f.relation}`; if (byKey.get(k).length < 2) continue; any = true; const m = need.get(k) ?? need.set(k, new Map()).get(k); (m.get(f.serial) ?? m.set(f.serial, []).get(f.serial)).push(i) }
    if (any) withVersioned++
  })
  // a contest: one key, two or more serials required by different questions
  let contested = 0, lost = 0; const losers = new Set(), examples = []
  for (const [k, m] of need) { if (m.size < 2) continue; contested++
    const sides = [...m.entries()].sort((a, b) => b[1].length - a[1].length)
    for (const [, qs] of sides.slice(1)) for (const qi of qs) losers.add(qi)
    if (examples.length < 4) examples.push(`${k}: ` + sides.map(([s, qs]) => `serial ${s} needed by ${qs.length} q`).join(' vs ')) }
  // latest-wins loses exactly the questions that need a non-latest version somewhere
  const latestLosers = new Set(); for (const [k, m] of need) { const vs = byKey.get(k); const last = vs[vs.length - 1].serial; for (const [s, qs] of m) if (s !== last) qs.forEach((qi) => latestLosers.add(qi)) }
  if (RUN) { const preds = new Map(readFileSync(new URL(`../runs/${RUN}/predictions.jsonl`, import.meta.url).pathname, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l)).map((p) => [p.qid, p]))
    const miss = new Set(mh.qa_ids.map((qid, i) => [qid, i]).filter(([qid]) => !preds.get(qid)?.em).map(([, i]) => i))
    const inLoser = [...miss].filter((i) => latestLosers.has(i)).length, noPath = [...miss].filter((i) => !pathOf.has(i)).length
    console.log(`${size} · ${RUN}: misses ${miss.size} · of which need an older version ${inLoser} · no gold path ${noPath} · other ${miss.size - inLoser - noPath}`)
    for (const i of miss) { if (latestLosers.has(i) || !pathOf.has(i)) continue; const p = preds.get(mh.qa_ids[i])
      console.log(`   OTHER q${i}: ${mh.questions[i]}\n      gold ${JSON.stringify(mh.answers[i])} · pred ${JSON.stringify(p?.pred ?? null)} · plan ${JSON.stringify(plans[mh.questions[i]]?.chain)} from ${JSON.stringify(plans[mh.questions[i]]?.entity)}\n      path ${pathOf.get(i).map((f) => { const vs = byKey.get(`${norm(f.subject)}|${f.relation}`); return `${f.subject} --${f.relation}--> ${f.object} [#${f.serial}${vs.length > 1 ? ` ${vs.length} versions, ${vs[vs.length - 1].serial === f.serial ? 'latest' : 'older'}` : ''}]` }).join(' ; ')}`) } }
  console.log(`${size}: gold paths ${paths}/100 · through a versioned key ${withVersioned} · versioned keys on gold paths ${need.size} · contested keys ${contested} · questions on a losing side ${losers.size} → ceiling of any one-version-per-key policy ${100 - losers.size} · latest-wins loses ${latestLosers.size} → ${100 - latestLosers.size}`)
  for (const e of examples) console.log('   ' + e.slice(0, 200))
}
