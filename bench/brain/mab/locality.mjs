/**
 * Serial locality: on a multi-hop gold path, is the gold version of a
 * versioned key nearer in the list to the path's other facts than the
 * competing version is? The haystack is one numbered list with no block
 * markers, so the only structure a store can read is position. If the
 * generator appends each question's updates near its own chain, position
 * separates the update the gold follows from the one it ignores, and it does
 * so without any world knowledge.
 *
 *   node mab/locality.mjs [--sizes=32k,64k,262k]
 */
import { readFileSync } from 'node:fs'
import { load, parse, norm, DATA } from './parse.mjs'
const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const plans = JSON.parse(readFileSync(DATA + 'plans.json', 'utf8'))
const clean = (s) => norm(String(s)).replace(/[^\p{L}\p{N}\s]/gu, ' ').replace(/\b(a|an|the)\b/g, ' ').replace(/\s+/g, ' ').trim()
const hit = (s, golds) => golds.some((g) => clean(s).includes(clean(g)))
for (const size of arg('sizes', '32k,64k,262k').split(',')) {
  const mh = load().find((r) => r.id === `factconsolidation_mh_${size}`); const { facts } = parse(mh.context)
  const out = new Map(), byKey = new Map()
  for (const f of facts) { const s = norm(f.subject); (out.get(s) ?? out.set(s, []).get(s)).push(f); const kk = `${s}|${f.relation}`; (byKey.get(kk) ?? byKey.set(kk, []).get(kk)).push(f) }
  for (const v of byKey.values()) v.sort((a, b) => a.serial - b.serial)
  const has = new Set(out.keys()), names = [...has]
  const find = (name) => { const c = norm(name).replace(/^the\s+/, ''); if (has.has(norm(name))) return norm(name); if (has.has(c)) return c; let best = null; for (const n of names) if ((n.includes(c) || c.includes(n)) && n.length > 3 && (!best || Math.abs(n.length - c.length) < Math.abs(best.length - c.length))) best = n; return best }
  const T = { older: { n: 0, goldNearer: 0, otherNearer: 0, prevNearer: 0, prevOther: 0, gaps: [] }, latest: { n: 0, goldNearer: 0, otherNearer: 0, prevNearer: 0, prevOther: 0, gaps: [] } }
  let qLocal = 0, qLatest = 0, qBoth = 0, qN = 0
  mh.qa_ids.forEach((qid, i) => {
    const q = mh.questions[i], golds = mh.answers[i], plan = plans[q]; if (!plan) return; const start = find(plan.entity); if (!start) return
    const maxDepth = plan.chain.length + 1, found = []
    const dfs = (e, path, depth, seen) => { if (depth > maxDepth) return; for (const f of out.get(e) ?? []) { if (hit(f.object, golds)) { found.push([...path, f]); continue } const o = norm(f.object); if (!seen.has(o) && has.has(o) && depth < maxDepth) dfs(o, [...path, f], depth + 1, new Set([...seen, o])) } }
    dfs(start, [], 1, new Set([start])); if (!found.length) return
    found.sort((a, b) => a.length - b.length); const path = found[0]; qN++
    let localOk = true, latestOk = true
    path.forEach((f, j) => { const vs = byKey.get(`${norm(f.subject)}|${f.relation}`); if (vs.length < 2) return
      const isLatest = vs[vs.length - 1].serial === f.serial, t = isLatest ? T.latest : T.older; t.n++
      const mates = path.filter((g) => g !== f).map((g) => g.serial)
      const near = (s) => mates.length ? Math.min(...mates.map((m) => Math.abs(m - s))) : 0
      const others = vs.filter((v) => v.serial !== f.serial)
      const dg = near(f.serial), doth = Math.min(...others.map((v) => near(v.serial)))
      if (dg < doth) t.goldNearer++; else if (doth < dg) t.otherNearer++
      t.gaps.push(dg)
      // the signal a forward resolver actually has: distance to the previous hop's fact
      if (j > 0) { const p = path[j - 1].serial; const dgp = Math.abs(f.serial - p), dop = Math.min(...others.map((v) => Math.abs(v.serial - p))); if (dgp < dop) t.prevNearer++; else if (dop < dgp) t.prevOther++ }
      // path-level: would "nearest to chain-mates" and "latest" pick the gold?
      if (!(dg < doth)) localOk = false
      if (!isLatest) latestOk = false })
    if (localOk) qLocal++; if (latestOk) qLatest++; if (localOk || latestOk) qBoth++
  })
  const med = (a) => a.length ? a.sort((x, y) => x - y)[Math.floor(a.length / 2)] : null
  for (const [k, t] of Object.entries(T)) console.log(`${size} · gold on ${k.toUpperCase()}: hops ${t.n} · gold nearer to chain-mates ${t.goldNearer} · other nearer ${t.otherNearer} · median gap ${med(t.gaps)} · vs previous hop only: gold nearer ${t.prevNearer} other nearer ${t.prevOther}`)
  console.log(`${size} · questions with a gold path ${qN}: nearest-to-chain picks gold on every versioned hop ${qLocal} · latest-wins ${qLatest} · either ${qBoth}`)
}
