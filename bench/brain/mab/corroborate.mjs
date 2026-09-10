/**
 * Corroboration: is a version's object connected to its subject anywhere
 * else in the store? The true author of a work is also the person the work is
 * listed under; the true head of state of a country is one of its citizens;
 * an injected replacement is a random entity with no such tie. For every
 * multi-version key on a multi-hop gold path, this counts, for the gold
 * version and for the others, whether object and subject share any other
 * fact — object as subject with the subject as object, subject as subject
 * with the object as object under another relation, or one hop through a
 * shared third entity.
 *
 *   node mab/corroborate.mjs [--sizes=32k,262k]
 */
import { readFileSync } from 'node:fs'
import { load, parse, norm, DATA } from './parse.mjs'
const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const plans = JSON.parse(readFileSync(DATA + 'plans.json', 'utf8'))
const clean = (s) => norm(String(s)).replace(/[^\p{L}\p{N}\s]/gu, ' ').replace(/\b(a|an|the)\b/g, ' ').replace(/\s+/g, ' ').trim()
const hit = (s, golds) => golds.some((g) => clean(s).includes(clean(g)))
for (const size of arg('sizes', '32k,262k').split(',')) {
  const mh = load().find((r) => r.id === `factconsolidation_mh_${size}`); const { facts } = parse(mh.context)
  const out = new Map(), inc = new Map(), byKey = new Map()
  for (const f of facts) { const s = norm(f.subject), o = norm(f.object); (out.get(s) ?? out.set(s, []).get(s)).push(f); (inc.get(o) ?? inc.set(o, []).get(o)).push(f); const kk = `${s}|${f.relation}`; (byKey.get(kk) ?? byKey.set(kk, []).get(kk)).push(f) }
  for (const v of byKey.values()) v.sort((a, b) => a.serial - b.serial)
  const has = new Set(out.keys()), names = [...has]
  const find = (name) => { const c = norm(name).replace(/^the\s+/, ''); if (has.has(norm(name))) return norm(name); if (has.has(c)) return c; let best = null; for (const n of names) if ((n.includes(c) || c.includes(n)) && n.length > 3 && (!best || Math.abs(n.length - c.length) < Math.abs(best.length - c.length))) best = n; return best }
  const neighbours = (e) => new Set([...(out.get(e) ?? []).map((f) => norm(f.object)), ...(inc.get(e) ?? []).map((f) => norm(f.subject))])
  /** ties between subject s and object o other than the fact itself: direct (any relation, either direction) or through one shared entity */
  const ties = (s, o, self) => { let direct = 0, shared = 0
    for (const f of out.get(s) ?? []) if (f.serial !== self && norm(f.object) === o) direct++
    for (const f of out.get(o) ?? []) if (norm(f.object) === s) direct++
    const ns = neighbours(s), no = neighbours(o); ns.delete(o); no.delete(s); for (const n of ns) if (no.has(n)) shared++
    return { direct, shared } }
  const T = { latest: { n: 0, goldTied: 0, otherTied: 0, goldMore: 0, otherMore: 0 }, older: { n: 0, goldTied: 0, otherTied: 0, goldMore: 0, otherMore: 0 } }
  mh.qa_ids.forEach((qid, i) => {
    const q = mh.questions[i], golds = mh.answers[i], plan = plans[q]; if (!plan) return; const start = find(plan.entity); if (!start) return
    const maxDepth = plan.chain.length + 1, paths = []
    const dfs = (e, path, depth, seen) => { if (depth > maxDepth) return; for (const f of out.get(e) ?? []) { const vs = byKey.get(`${e}|${f.relation}`); const step = { f, vs, isLatest: vs[vs.length - 1].serial === f.serial }
        if (hit(f.object, golds)) { paths.push([...path, step]); continue } const o = norm(f.object); if (!seen.has(o) && has.has(o) && depth < maxDepth) dfs(o, [...path, step], depth + 1, new Set([...seen, o])) } }
    dfs(start, [], 1, new Set([start])); if (!paths.length) return
    paths.sort((a, b) => a.length - b.length || b.filter((s) => s.isLatest).length - a.filter((s) => s.isLatest).length)
    for (const s of paths[0]) { if (s.vs.length < 2) continue; const t = s.isLatest ? T.latest : T.older; t.n++
      const subj = norm(s.f.subject); const g = ties(subj, norm(s.f.object), s.f.serial); const gs = g.direct * 2 + g.shared
      const os = Math.max(...s.vs.filter((v) => v.serial !== s.f.serial).map((v) => { const x = ties(subj, norm(v.object), v.serial); return x.direct * 2 + x.shared }))
      if (gs > 0) t.goldTied++; if (os > 0) t.otherTied++; if (gs > os) t.goldMore++; else if (os > gs) t.otherMore++ }
  })
  for (const [k, t] of Object.entries(T)) console.log(`${size} · gold on ${k.toUpperCase()}: hops ${t.n} · gold tied to subject elsewhere ${t.goldTied} · other version tied ${t.otherTied} · gold more tied ${t.goldMore} · other more tied ${t.otherMore}`)
}
