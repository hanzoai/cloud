/**
 * Rescore a LoCoMo answer run from its predictions alone — no model, no
 * retrieval: F1 and EM per question with LoCoMo's normalisation, bootstrap
 * intervals, the evidence grades where the row carries its context ids, and
 * one summary block per split in the shape run.mjs writes, so tables.mjs and
 * results.mjs read it unchanged. Duplicate rows (a resumed runner asking a
 * question twice) collapse to the last answer.
 *
 *   node rescore.mjs runs/<name> [runs/<other> ...]
 */
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { f1, em, ci, quantile } from './metrics.mjs'
const store = JSON.parse(readFileSync(new URL('./brain-vectors.json', import.meta.url), 'utf8'))
const cat = new Map(); store.forEach((c, ci_) => c.qa.forEach((q) => cat.set(q.question, { category: q.category, evidence: q.evidence, ci: ci_ })))
const DEV = new Set([0, 1, 2]); const LABEL = { 1: 'multi-hop', 2: 'temporal', 3: 'open-domain', 4: 'single-hop' }
for (const dir of process.argv.slice(2)) {
  const rows = readFileSync(`${dir}/predictions.jsonl`, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l))
  const last = new Map(); for (const r of rows) last.set(r.q ?? r.question, r)
  const preds = [...last.values()].map((r) => { const q = r.q ?? r.question, meta = cat.get(q) ?? {}; const gold = r.gold ?? r.answer; const ids = r.ctx ?? r.ids ?? r.context_ids ?? r.turns ?? null
    const exact = ids && meta.evidence ? Number(meta.evidence.some((e) => ids.includes(e))) : (r.exact ?? null)
    const F = f1(r.pred, gold), E = em(r.pred, gold)
    return { q, category: meta.category, split: DEV.has(meta.ci) ? 'dev' : 'test', f1: F, em: Number(E), exact, supported: r.supported ?? null, answered: Number(F >= 0.5 || E), tokens: r.tokens ?? 0, ms: r.ms ?? r.latency_ms ?? 0 } })
  writeFileSync(`${dir}/predictions.jsonl`, [...last.values()].map((r) => JSON.stringify(r)).join('\n') + '\n')
  const block = (xs) => ({ n: xs.length, of: xs.length, f1: ci(xs.map((r) => r.f1)), em: ci(xs.map((r) => r.em)), exact: xs.some((r) => r.exact != null) ? xs.reduce((a, r) => a + (r.exact ?? 0), 0) / xs.length : null, supported: xs.some((r) => r.supported != null) ? xs.reduce((a, r) => a + (r.supported ?? 0), 0) / xs.length : null, answered: xs.reduce((a, r) => a + r.answered, 0) / xs.length, tokens: xs.reduce((a, r) => a + r.tokens, 0) / xs.length, p50_ms: quantile(xs.map((r) => r.ms), 0.5), p95_ms: quantile(xs.map((r) => r.ms), 0.95) })
  const summary = {}
  for (const split of ['all', 'dev', 'test']) { const xs = preds.filter((r) => split === 'all' || r.split === split); if (!xs.length) continue; summary[split] = block(xs); for (const c of [1, 2, 3, 4]) { const ys = xs.filter((r) => r.category === c); if (ys.length) summary[`${split}:${LABEL[c]}`] = block(ys) } }
  const m = existsSync(`${dir}/metrics.json`) ? JSON.parse(readFileSync(`${dir}/metrics.json`, 'utf8')) : {}
  writeFileSync(`${dir}/metrics.json`, JSON.stringify({ ...m, summary, rescored: { rows: rows.length, unique: preds.length } }, null, 1))
  const s = summary.all; console.log(`${dir.split('/').pop().padEnd(44)} n ${s.n}  F1 ${(s.f1.mean * 100).toFixed(1)} [${(s.f1.lo * 100).toFixed(1)}, ${(s.f1.hi * 100).toFixed(1)}]  EM ${(s.em.mean * 100).toFixed(1)}  EXACT ${s.exact == null ? '-' : (s.exact * 100).toFixed(1)}  ANSWERED ${(s.answered * 100).toFixed(1)}  tok/q ${s.tokens.toFixed(0)}` + [1, 2, 3, 4].map((c) => { const b = summary[`all:${LABEL[c]}`]; return b ? `  ${LABEL[c]} ${(b.f1.mean * 100).toFixed(1)}` : '' }).join(''))
}
