/**
 * Which version the multi-hop gold uses at a key that has several.
 *
 * For every multi-hop question, a search over the parsed graph from the plan's
 * base entity finds every path of the plan's length (±1) whose last object is
 * the gold, taking ANY version at each hop. For each such path, every hop
 * whose (subject, relation) key holds more than one version is recorded as
 * using the latest serial or an older one. If the gold overwhelmingly sits on
 * older versions, the benchmark computes multi-hop answers on the original
 * facts and the injected updates are conflicts to be resolved AWAY for chains
 * — a property of the benchmark, measured, not assumed.
 *
 *   node mab/goldversion.mjs [--sizes=mh_6k,mh_32k,mh_64k,mh_262k]
 */
import { readFileSync } from 'node:fs'
import { load, parse, norm, DATA } from './parse.mjs'
const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const SIZES = arg('sizes', 'mh_6k,mh_32k,mh_64k,mh_262k').split(',')
const plans = JSON.parse(readFileSync(DATA + 'plans.json', 'utf8'))
const clean = (s) => norm(String(s)).replace(/[^\p{L}\p{N}\s]/gu, ' ').replace(/\b(a|an|the)\b/g, ' ').replace(/\s+/g, ' ').trim()
const hit = (s, golds) => golds.some((g) => clean(s).includes(clean(g)))
for (const size of SIZES) {
  const row = load().find((r) => r.id === `factconsolidation_${size}`); if (!row) continue
  const { facts } = parse(row.context)
  const out = new Map(), byKey = new Map()
  for (const f of facts) { const k = norm(f.subject); (out.get(k) ?? out.set(k, []).get(k)).push(f); const kk = `${k}|${f.relation}`; (byKey.get(kk) ?? byKey.set(kk, []).get(kk)).push(f) }
  for (const v of byKey.values()) v.sort((a, b) => a.serial - b.serial)
  const has = new Set(out.keys()), names = [...has]
  const find = (name) => { const c = norm(name).replace(/^the\s+/, ''); if (has.has(norm(name))) return norm(name); if (has.has(c)) return c; let best = null; for (const n of names) if ((n.includes(c) || c.includes(n)) && n.length > 3 && (!best || Math.abs(n.length - c.length) < Math.abs(best.length - c.length))) best = n; return best }
  let latest = 0, older = 0, questionsWithVersionedHop = 0, questionsGoldOnOlder = 0, questionsGoldOnLatest = 0, found = 0
  row.qa_ids.forEach((qid, i) => {
    const q = row.questions[i], golds = row.answers[i], plan = plans[q]; if (!plan) return
    const start = find(plan.entity); if (!start) return
    const maxDepth = plan.chain.length + 1, paths = []
    const dfs = (e, path, depth, seen) => { if (depth > maxDepth) return; for (const f of out.get(e) ?? []) { const vs = byKey.get(`${e}|${f.relation}`), isLatest = vs[vs.length - 1].serial === f.serial, multi = vs.length > 1
        const step = { multi, isLatest }; if (hit(f.object, golds)) { paths.push([...path, step]); continue } const o = norm(f.object); if (!seen.has(o) && has.has(o) && depth < maxDepth) dfs(o, [...path, step], depth + 1, new Set([...seen, o])) } }
    dfs(start, [], 1, new Set([start])); if (!paths.length) return; found++
    // the shortest path decides; among equals, the one using the latest versions most
    paths.sort((a, b) => a.length - b.length || b.filter((s) => s.isLatest).length - a.filter((s) => s.isLatest).length)
    const p = paths[0]; const versioned = p.filter((s) => s.multi); if (!versioned.length) return
    questionsWithVersionedHop++; const l = versioned.filter((s) => s.isLatest).length, o = versioned.length - l; latest += l; older += o
    if (o === 0) questionsGoldOnLatest++; else questionsGoldOnOlder++
  })
  console.log(`${size}: gold path found ${found}/100 · with a versioned hop ${questionsWithVersionedHop} → gold on latest at every versioned hop ${questionsGoldOnLatest}, gold needs an OLDER version somewhere ${questionsGoldOnOlder} · hops: latest ${latest}, older ${older}`)
}
