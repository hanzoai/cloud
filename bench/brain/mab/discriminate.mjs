/**
 * Can a store tell the gold version from the injected one without knowing the
 * world? For every multi-version key on a multi-hop gold path, compare the
 * version the gold uses with the others on signals a system can read: how
 * connected each version's object is in the store (facts it appears in as
 * subject or object), whether it has outgoing facts at all, and its serial
 * distance from the chain's other facts.
 *
 *   node mab/discriminate.mjs [--sizes=32k,262k]
 */
import { readFileSync } from 'node:fs'
import { load, parse, norm, DATA } from './parse.mjs'
const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const plans = JSON.parse(readFileSync(DATA + 'plans.json', 'utf8'))
const clean = (s) => norm(String(s)).replace(/[^\p{L}\p{N}\s]/gu, ' ').replace(/\b(a|an|the)\b/g, ' ').replace(/\s+/g, ' ').trim()
const hit = (s, golds) => golds.some((g) => clean(s).includes(clean(g)))
for (const size of arg('sizes', '32k,64k,262k').split(',')) {
  const mh = load().find((r) => r.id === `factconsolidation_mh_${size}`); const { facts } = parse(mh.context)
  const out = new Map(), byKey = new Map(), degree = new Map()
  for (const f of facts) { const k = norm(f.subject); (out.get(k) ?? out.set(k, []).get(k)).push(f); const kk = `${k}|${f.relation}`; (byKey.get(kk) ?? byKey.set(kk, []).get(kk)).push(f); for (const n of [k, norm(f.object)]) degree.set(n, (degree.get(n) ?? 0) + 1) }
  for (const v of byKey.values()) v.sort((a, b) => a.serial - b.serial)
  const has = new Set(out.keys()), names = [...has]
  const find = (name) => { const c = norm(name).replace(/^the\s+/, ''); if (has.has(norm(name))) return norm(name); if (has.has(c)) return c; let best = null; for (const n of names) if ((n.includes(c) || c.includes(n)) && n.length > 3 && (!best || Math.abs(n.length - c.length) < Math.abs(best.length - c.length))) best = n; return best }
  const tally = { gold_latest: 0, gold_older: 0, deg_gold_higher: 0, deg_gold_lower: 0, deg_equal: 0, gold_has_out: 0, other_has_out: 0, both_out: 0, neither_out: 0, examples: [] }
  const tl = { ...tally }, to = { ...tally }
  mh.qa_ids.forEach((qid, i) => {
    const q = mh.questions[i], golds = mh.answers[i], plan = plans[q]; if (!plan) return; const start = find(plan.entity); if (!start) return
    const maxDepth = plan.chain.length + 1, paths = []
    const dfs = (e, path, depth, seen) => { if (depth > maxDepth) return; for (const f of out.get(e) ?? []) { const vs = byKey.get(`${e}|${f.relation}`); const step = { f, vs, isLatest: vs[vs.length - 1].serial === f.serial }
        if (hit(f.object, golds)) { paths.push([...path, step]); continue } const o = norm(f.object); if (!seen.has(o) && has.has(o) && depth < maxDepth) dfs(o, [...path, step], depth + 1, new Set([...seen, o])) } }
    dfs(start, [], 1, new Set([start])); if (!paths.length) return
    paths.sort((a, b) => a.length - b.length || b.filter((s) => s.isLatest).length - a.filter((s) => s.isLatest).length)
    for (const s of paths[0]) { if (s.vs.length < 2) continue; const t = s.isLatest ? tl : to; if (s.isLatest) t.gold_latest++; else t.gold_older++
      const others = s.vs.filter((v) => v.serial !== s.f.serial); const dg = degree.get(norm(s.f.object)) ?? 0, dmax = Math.max(...others.map((v) => degree.get(norm(v.object)) ?? 0))
      if (dg > dmax) t.deg_gold_higher++; else if (dg < dmax) t.deg_gold_lower++; else t.deg_equal++
      const gOut = has.has(norm(s.f.object)), oOut = others.some((v) => has.has(norm(v.object)))
      if (gOut && oOut) t.both_out++; else if (gOut) t.gold_has_out++; else if (oOut) t.other_has_out++; else t.neither_out++
      if (!s.isLatest && t.examples.length < 4) t.examples.push(`${norm(s.f.subject)} -${s.f.relation}-> gold "${s.f.object}" (serial ${s.f.serial}, degree ${dg}) vs ${others.map((v) => `"${v.object}" (serial ${v.serial}, degree ${degree.get(norm(v.object)) ?? 0})`).join(', ')}`) }
  })
  for (const [name, t] of [['gold on LATEST', tl], ['gold on OLDER', to]]) console.log(`${size} · ${name}: hops ${t.gold_latest + t.gold_older} · degree: gold higher ${t.deg_gold_higher}, lower ${t.deg_gold_lower}, equal ${t.deg_equal} · outgoing facts: both ${t.both_out}, gold only ${t.gold_has_out}, other only ${t.other_has_out}, neither ${t.neither_out}`)
  for (const e of to.examples) console.log('     older: ' + e)
}
