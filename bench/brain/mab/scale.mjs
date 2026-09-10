/**
 * What actually scales: per multi-hop question, the structural variables a
 * haystack imposes — chain depth, the base entity's degree, versions along the
 * planned path, entities whose name contains or is contained in another's —
 * against whether the search got it right, per size. If accuracy tracks one of
 * these and not the token count, the failure is that variable, not length.
 *
 *   node mab/scale.mjs --run=beam [--sizes=mh_6k,mh_32k,mh_64k,mh_262k]
 */
import { readFileSync } from 'node:fs'
import { load, parse, norm, DATA } from './parse.mjs'
const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const RUN = arg('run', 'beam'), SIZES = arg('sizes', 'mh_6k,mh_32k,mh_64k,mh_262k').split(',')
const plans = JSON.parse(readFileSync(DATA + 'plans.json', 'utf8'))
const bucket = (x, edges) => { for (const e of edges) if (x <= e) return `≤${e}`; return `>${edges[edges.length - 1]}` }
const table = {}
for (const size of SIZES) {
  const row = load().find((r) => r.id === `factconsolidation_${size}`); if (!row) continue
  const preds = new Map(readFileSync(`${new URL('../runs/', import.meta.url).pathname}mab-${size.endsWith('6k') ? 'dev' : 'test'}-${RUN}-none/predictions.jsonl`, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l)).map((p) => [p.qid, p]))
  const { facts } = parse(row.context)
  const bySubject = new Map(); for (const f of facts) { const k = norm(f.subject); (bySubject.get(k) ?? bySubject.set(k, []).get(k)).push(f) }
  const byKey = new Map(); for (const f of facts) { const k = `${norm(f.subject)}|${f.relation}`; byKey.set(k, (byKey.get(k) ?? 0) + 1) }
  const names = [...bySubject.keys()]
  const collisions = (e) => names.filter((n) => n !== e && n.length > 3 && (n.includes(e) || e.includes(n))).length
  const rows = []
  row.qa_ids.forEach((qid, i) => { const q = row.questions[i], plan = plans[q]; if (!plan) return; const base = norm(plan.entity)
    // versions along the planned path, following the latest version like the resolver
    let cur = base, versions = 0, multi = 0
    for (const rel of plan.chain) { const key = `${cur}|${rel === 'origin' ? 'country_origin' : rel}`; const n = byKey.get(key) ?? byKey.get(`${cur}|citizenship`) ?? 0; versions += n; if (n > 1) multi++; const fs = (bySubject.get(cur) ?? []).filter((f) => f.relation === (rel === 'origin' ? 'country_origin' : rel) || (rel === 'origin' && f.relation === 'citizenship')).sort((a, b) => b.serial - a.serial); if (!fs.length) break; cur = norm(fs[0].object) }
    rows.push({ ok: preds.get(qid)?.em ?? 0, depth: plan.chain.length, degree: (bySubject.get(base) ?? []).length, versions, multi, collisions: collisions(base) }) })
  const by = (key, edges) => { const g = {}; for (const r of rows) { const b = bucket(r[key], edges); (g[b] ??= []).push(r.ok) } return Object.fromEntries(Object.entries(g).map(([b, xs]) => [b, `${(100 * xs.reduce((a, x) => a + x, 0) / xs.length).toFixed(0)}% (${xs.length})`])) }
  table[size] = { accuracy: `${rows.reduce((a, r) => a + r.ok, 0)}%`, facts: facts.length, entities: names.length, 'mean degree of base entity': (rows.reduce((a, r) => a + r.degree, 0) / rows.length).toFixed(1), 'mean versions on path': (rows.reduce((a, r) => a + r.versions, 0) / rows.length).toFixed(2), 'hops with >1 version': (rows.reduce((a, r) => a + r.multi, 0) / rows.length).toFixed(2), 'mean name collisions': (rows.reduce((a, r) => a + r.collisions, 0) / rows.length).toFixed(1),
    'by depth': by('depth', [1, 2, 3]), 'by versions on path': by('versions', [1, 2, 3, 4]), 'by base degree': by('degree', [2, 4, 8]), 'by name collisions': by('collisions', [0, 2, 5]) }
}
for (const [size, t] of Object.entries(table)) { console.log(`\n== ${size}: accuracy ${t.accuracy} · facts ${t.facts} · entities ${t.entities} · base degree ${t['mean degree of base entity']} · versions on path ${t['mean versions on path']} · hops with >1 version ${t['hops with >1 version']} · name collisions ${t['mean name collisions']}`); for (const k of ['by depth', 'by versions on path', 'by base degree', 'by name collisions']) console.log(`   ${k.padEnd(22)} ${Object.entries(t[k]).map(([b, v]) => `${b}: ${v}`).join('   ')}`) }
