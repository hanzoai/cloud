/**
 * Whose updates are they? Each haystack holds both single-hop and multi-hop
 * questions over the same facts. This asks, per multi-hop question whose gold
 * needs an older version of some key, whether that key is one a single-hop
 * question of the same haystack asks about — i.e. whether the injected update
 * belongs to another question. And the converse: on multi-hop gold paths that
 * use the latest version of an updated key, is that key a single-hop target?
 *
 *   node mab/updates.mjs [--sizes=32k,262k]
 */
import { readFileSync } from 'node:fs'
import { load, parse, norm, DATA } from './parse.mjs'
const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const plans = JSON.parse(readFileSync(DATA + 'plans.json', 'utf8'))
const clean = (s) => norm(String(s)).replace(/[^\p{L}\p{N}\s]/gu, ' ').replace(/\b(a|an|the)\b/g, ' ').replace(/\s+/g, ' ').trim()
const hit = (s, golds) => golds.some((g) => clean(s).includes(clean(g)))
for (const size of arg('sizes', '6k,32k,64k,262k').split(',')) {
  const rows = load(); const mh = rows.find((r) => r.id === `factconsolidation_mh_${size}`), sh = rows.find((r) => r.id === `factconsolidation_sh_${size}`)
  const { facts } = parse(mh.context)
  const out = new Map(), byKey = new Map()
  for (const f of facts) { const k = norm(f.subject); (out.get(k) ?? out.set(k, []).get(k)).push(f); const kk = `${k}|${f.relation}`; (byKey.get(kk) ?? byKey.set(kk, []).get(kk)).push(f) }
  for (const v of byKey.values()) v.sort((a, b) => a.serial - b.serial)
  const has = new Set(out.keys()), names = [...has]
  const find = (name) => { const c = norm(name).replace(/^the\s+/, ''); if (has.has(norm(name))) return norm(name); if (has.has(c)) return c; let best = null; for (const n of names) if ((n.includes(c) || c.includes(n)) && n.length > 3 && (!best || Math.abs(n.length - c.length) < Math.abs(best.length - c.length))) best = n; return best }
  // the single-hop targets: (entity, relation) of every single-hop question, and the keys whose latest object is the single-hop gold
  const shKeys = new Set()
  sh.questions.forEach((q, i) => { const p = plans[q]; if (!p) return; const e = find(p.entity); if (!e) return; const rel = p.chain[0] === 'origin' ? (byKey.has(`${e}|citizenship`) ? 'citizenship' : 'country_origin') : p.chain[0]; shKeys.add(`${e}|${rel}`) })
  const multiKeys = [...byKey.entries()].filter(([, v]) => v.length > 1).map(([k]) => k)
  const shMulti = multiKeys.filter((k) => shKeys.has(k)).length
  let olderHops = 0, olderInSh = 0, latestHops = 0, latestInSh = 0
  mh.qa_ids.forEach((qid, i) => {
    const q = mh.questions[i], golds = mh.answers[i], plan = plans[q]; if (!plan) return; const start = find(plan.entity); if (!start) return
    const maxDepth = plan.chain.length + 1, paths = []
    const dfs = (e, path, depth, seen) => { if (depth > maxDepth) return; for (const f of out.get(e) ?? []) { const vs = byKey.get(`${e}|${f.relation}`); const step = { key: `${e}|${f.relation}`, multi: vs.length > 1, isLatest: vs[vs.length - 1].serial === f.serial }
        if (hit(f.object, golds)) { paths.push([...path, step]); continue } const o = norm(f.object); if (!seen.has(o) && has.has(o) && depth < maxDepth) dfs(o, [...path, step], depth + 1, new Set([...seen, o])) } }
    dfs(start, [], 1, new Set([start])); if (!paths.length) return
    paths.sort((a, b) => a.length - b.length || b.filter((s) => s.isLatest).length - a.filter((s) => s.isLatest).length)
    for (const s of paths[0]) { if (!s.multi) continue; if (s.isLatest) { latestHops++; if (shKeys.has(s.key)) latestInSh++ } else { olderHops++; if (shKeys.has(s.key)) olderInSh++ } }
  })
  console.log(`${size}: updated keys ${multiKeys.length}, of which single-hop targets ${shMulti} · on multi-hop gold paths: hops on an OLDER version ${olderHops} (key is a single-hop target: ${olderInSh}) · hops on the LATEST version ${latestHops} (single-hop target: ${latestInSh})`)
}
