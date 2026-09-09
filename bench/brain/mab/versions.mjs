/**
 * Which version the benchmark means.
 *
 * A (subject, relation) key can hold several facts, later serials after
 * earlier ones. The resolver took "latest serial wins" as the meaning of
 * "current". This measures that against the gold on the DEV haystacks first,
 * then on test: for single-hop questions, whether the gold is the first, the
 * last, or a middle version of its key; for multi-hop questions, which choice
 * of version at each hop (first or last) reaches the gold, over every
 * combination. Nothing here changes a run; it decides what the resolver
 * should mean by "current", on dev, before test is touched.
 *
 *   node mab/versions.mjs [--sizes=sh_6k,mh_6k,...]
 */
import { readFileSync } from 'node:fs'
import { load, parse, norm, DATA } from './parse.mjs'
const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const SIZES = arg('sizes', 'sh_6k,mh_6k,sh_32k,mh_32k,sh_64k,mh_64k,sh_262k,mh_262k').split(',')
const plans = JSON.parse(readFileSync(DATA + 'plans.json', 'utf8'))
const clean = (s) => norm(String(s)).replace(/[^\p{L}\p{N}\s]/gu, ' ').replace(/\b(a|an|the)\b/g, ' ').replace(/\s+/g, ' ').trim()
const hit = (s, golds) => golds.some((g) => clean(s).includes(clean(g)))

for (const size of SIZES) {
  const row = load().find((r) => r.id === `factconsolidation_${size}`); if (!row) continue
  const { facts } = parse(row.context)
  const byKey = new Map(); for (const f of facts) { const k = `${norm(f.subject)}|${f.relation}`; (byKey.get(k) ?? byKey.set(k, []).get(k)).push(f) }
  for (const v of byKey.values()) v.sort((a, b) => a.serial - b.serial)
  const names = [...new Set(facts.flatMap((f) => [norm(f.subject), norm(f.object)]))]
  const has = new Set(facts.map((f) => norm(f.subject)))
  const find = (name) => { const c = norm(name).replace(/^the\s+/, ''); if (has.has(norm(name))) return norm(name); if (has.has(c)) return c; let best = null; for (const n of names) if (has.has(n) && (n.includes(c) || c.includes(n)) && n.length > 3 && (!best || Math.abs(n.length - c.length) < Math.abs(best.length - c.length))) best = n; return best }
  const versions = (e, rel) => { if (rel === 'origin') { for (const r of ['citizenship', 'country_origin']) { const v = byKey.get(`${e}|${r}`); if (v?.length) return v } return [] } const v = byKey.get(`${e}|${rel}`) ?? []; if (!v.length && rel === 'spouse') return facts.filter((f) => f.relation === 'spouse' && norm(f.object) === e).map((f) => ({ ...f, object: f.subject })); return v }
  const tally = { n: 0, plan: 0, first: 0, last: 0, both: 0, middle: 0, none: 0, single: 0 }
  const mh = { n: 0, allLast: 0, allFirst: 0, mixedOnly: 0, none: 0, noPlan: 0, lastWhereMulti: 0, firstWhereMulti: 0, multiHops: 0 }
  row.qa_ids.forEach((qid, i) => {
    const q = row.questions[i], golds = row.answers[i], plan = plans[q]
    if (size.startsWith('sh')) {
      tally.n++; if (!plan) return; tally.plan++
      const e = find(plan.entity); const vs = e ? versions(e, plan.chain[0]) : []
      if (!vs.length) { tally.none++; return }
      if (vs.length === 1) { tally.single++; return }
      const f = hit(vs[0].object, golds), l = hit(vs[vs.length - 1].object, golds), m = vs.slice(1, -1).some((v) => hit(v.object, golds))
      if (f && l) tally.both++; else if (l) tally.last++; else if (f) tally.first++; else if (m) tally.middle++; else tally.none++
    } else {
      mh.n++; if (!plan) { mh.noPlan++; return }
      const n = plan.chain.length; const hits = []
      for (let mask = 0; mask < (1 << n); mask++) {
        let cur = find(plan.entity), ans = null, ok = true, multi = 0
        for (let h = 0; h < n && ok; h++) { const vs = cur ? versions(cur, plan.chain[h]) : []; if (!vs.length) { ok = false; break } if (vs.length > 1) multi++
          const pick = (mask >> h) & 1 ? vs[vs.length - 1] : vs[0]; ans = pick.object; cur = h < n - 1 ? find(ans) : cur; if (h < n - 1 && !cur) ok = false }
        if (ok && ans != null && hit(ans, golds)) hits.push({ mask, multi })
      }
      if (!hits.length) { mh.none++; return }
      const allLast = hits.some((h) => h.mask === (1 << n) - 1), allFirst = hits.some((h) => h.mask === 0)
      if (allLast && !allFirst) mh.allLast++; else if (allFirst && !allLast) mh.allFirst++; else if (allLast && allFirst) mh.allLast++, mh.allFirst++; else mh.mixedOnly++
    }
  })
  if (size.startsWith('sh')) console.log(`${size.padEnd(8)} single-hop  n=${tally.n} plans=${tally.plan}  one version=${tally.single}  gold is LAST=${tally.last}  FIRST=${tally.first}  both=${tally.both}  middle=${tally.middle}  none=${tally.none}`)
  else console.log(`${size.padEnd(8)} multi-hop   n=${mh.n} noPlan=${mh.noPlan}  reachable with all-LAST=${mh.allLast}  all-FIRST=${mh.allFirst}  only a mix=${mh.mixedOnly}  unreachable by any version choice=${mh.none}`)
}
