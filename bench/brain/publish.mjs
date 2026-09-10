/**
 * The runs, written into the Hanzo evals plane — the native home for graded
 * examples and the scores a run gave them.
 *
 *   dataset  one per benchmark: locomo, memoryagentbench-factconsolidation, repobench-r-python
 *   item     one per question, id = the benchmark's own id, input = the question and its
 *            haystack/category, expectedOutput = the gold
 *   score    one event per (run, item, metric): substring_em / f1 / em / all@20 / any@20 /
 *            recall@1 / mrr, runName = the run directory, comment = the prediction
 *
 * Idempotent: data/published.json remembers every item and score already filed, so a
 * rerun files only what is new. The credential is `hanzo auth login`'s token; nothing is
 * printed. `/v1/research/experiments` is the plane these runs also belong in; it is not
 * mounted on the deployed cloud yet, and gains a target here the day it answers.
 *
 *   node publish.mjs [--only=mab|locomo|code] [--dry]
 */
import { readFileSync, writeFileSync, existsSync, readdirSync } from 'node:fs'
import { pool } from './llm.mjs'
// ids and names must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$
const safe = (x) => String(x).replace(/[^A-Za-z0-9._-]+/g, '-').replace(/^[^A-Za-z0-9]+/, '').slice(0, 64)
const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.slice(k.length + 3) : d }
const ONLY = arg('only', ''), DRY = process.argv.includes('--dry')
const API = process.env.HANZO_API ?? 'https://api.hanzo.ai/v1'
const home = process.env.HOME
const key = process.env.HANZO_API_KEY ?? JSON.parse(readFileSync(`${home}/.hanzo/credentials.json`, 'utf8')).access_token
const here = new URL('.', import.meta.url)
const ledgerFile = new URL('./data/published.json', here)
const ledger = existsSync(ledgerFile) ? JSON.parse(readFileSync(ledgerFile, 'utf8')) : { datasets: {}, items: {}, scores: {} }
const save = () => writeFileSync(ledgerFile, JSON.stringify(ledger))
let filed = { datasets: 0, items: 0, scores: 0, failed: 0 }
async function post(path, body) {
  if (DRY) return { ok: true }
  for (let attempt = 0; attempt < 4; attempt++) {
    const r = await fetch(`${API}${path}`, { method: 'POST', headers: { authorization: `Bearer ${key}`, 'content-type': 'application/json' }, body: JSON.stringify(body) })
    if (r.ok) return r.json().catch(() => ({}))
    if (r.status === 409) return { exists: true }
    if (r.status === 429 || r.status >= 500) { await new Promise((z) => setTimeout(z, 1500 * (attempt + 1))); continue }
    throw new Error(`${r.status} ${(await r.text()).slice(0, 160)}`)
  }
  throw new Error(`gave up on ${path}`)
}
async function dataset(name, description, metadata) { if (ledger.datasets[name]) return; await post('/evals/datasets', { name, description, metadata }); ledger.datasets[name] = true; filed.datasets++; save() }
async function items(name, rows) {
  const todo = rows.filter((r) => !ledger.items[`${name}/${r.id}`])
  await pool(todo, async (r) => { try { await post(`/evals/datasets/${name}/items`, { id: safe(r.id), input: r.input, expectedOutput: r.expectedOutput, metadata: r.metadata ?? {}, status: 'active' }); ledger.items[`${name}/${r.id}`] = true; filed.items++ } catch (e) { filed.failed++; process.stderr.write(`\n${name}/${r.id}: ${e.message}\n`) } }, 6)
  save()
}
async function scores(name, runName, rows) {
  const todo = rows.filter((r) => !ledger.scores[`${runName}/${r.id}/${r.metric}`])
  await pool(todo, async (r) => { try { await post('/evals/scores', { name: safe(r.metric.replace('@', '_at_')), value: r.value, datasetName: name, datasetItemId: safe(r.id), runName: safe(runName), comment: r.comment?.slice(0, 500), dataType: 'NUMERIC' }); ledger.scores[`${runName}/${r.id}/${r.metric}`] = true; filed.scores++ } catch (e) { filed.failed++; process.stderr.write(`\n${runName}/${r.id}: ${e.message}\n`) } }, 6)
  save()
}
const runsDir = new URL('./runs/', here)
const jsonl = (p) => readFileSync(p, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l))

// ── MemoryAgentBench FactConsolidation
if (!ONLY || ONLY === 'mab') {
  const name = 'memoryagentbench-factconsolidation'
  await dataset(name, 'MemoryAgentBench Conflict Resolution, FactConsolidation: 8 haystacks (SH/MH at 6k/32k/64k/262k), 100 questions each; gold answers as strings; metric substring exact match.', { source: 'hf ai-hyz/MemoryAgentBench', split: 'Conflict_Resolution', dev: ['sh_6k', 'mh_6k'], test: ['sh_32k', 'mh_32k', 'sh_64k', 'mh_64k', 'sh_262k', 'mh_262k'] })
  const { load } = await import('./mab/parse.mjs')
  const rows = load().flatMap((r) => r.qa_ids.map((qid, i) => ({ id: qid, input: { question: r.questions[i], haystack: r.id.replace('factconsolidation_', ''), tokens_in_haystack: Math.round(r.context.length / 4) }, expectedOutput: { answers: Array.from(r.answers[i]) }, metadata: { split: r.id.endsWith('_6k') ? 'dev' : 'test' } })))
  await items(name, rows)
  for (const d of readdirSync(runsDir).filter((d) => /^mab-(dev|test)-(beam|noreader)-none(-v\d+)?$/.test(d))) {
    const preds = jsonl(new URL(`./runs/${d}/predictions.jsonl`, here))
    await scores(name, d, preds.map((p) => ({ id: p.qid, metric: 'substring_em', value: p.em ? 1 : 0, comment: String(p.pred ?? '') })))
  }
}
// ── LoCoMo
if (!ONLY || ONLY === 'locomo') {
  const name = 'locomo'
  await dataset(name, 'LoCoMo (Maharana et al. 2024): ten long conversations, 1,540 questions with annotated evidence turns; categories 1 multi-hop, 2 temporal, 3 open-domain, 4 single-hop (5 adversarial excluded). Metrics: token F1 and exact match; retrieval ALL/ANY at k.', { source: 'snap-research/locomo locomo10.json', dev: 'conversations 0-2', test: 'conversations 3-9' })
  const store = JSON.parse(readFileSync(new URL('./brain-vectors.json', here), 'utf8')), corpus = JSON.parse(readFileSync(new URL('./locomo10.json', here), 'utf8'))
  const LABEL = { 1: 'multi-hop', 2: 'temporal', 3: 'open-domain', 4: 'single-hop' }
  const ids = new Map(); const rows = []
  store.forEach((c, ci) => c.qa.forEach((q, qi) => { if (!q.evidence.length) return; const id = `c${ci}q${qi}`; ids.set(q.question, id); rows.push({ id, input: { question: q.question, conversation: ci, category: LABEL[q.category] }, expectedOutput: { answer: corpus[ci].qa.find((x) => x.question === q.question)?.answer ?? '', evidence: q.evidence }, metadata: { split: ci < 3 ? 'dev' : 'test', category: q.category } }) }))
  await items(name, rows)
  for (const d of readdirSync(runsDir).filter((d) => /^locomo-all-(single|cer|context|oracle)-k\d+-/.test(d) && existsSync(new URL(`./runs/${d}/predictions.jsonl`, here)))) {
    const preds = jsonl(new URL(`./runs/${d}/predictions.jsonl`, here))
    const rows2 = preds.flatMap((p) => { const id = ids.get(p.q ?? p.question); if (!id) return []; return [{ id, metric: 'f1', value: +p.f1, comment: String(p.pred ?? '') }, { id, metric: 'em', value: p.em ? 1 : 0 }] })
    await scores(name, d, rows2)
  }
  for (const d of readdirSync(runsDir).filter((d) => /^locomo-retrieval-(test|all)-/.test(d) && existsSync(new URL(`./runs/${d}/traces.jsonl`, here)))) {
    const traces = jsonl(new URL(`./runs/${d}/traces.jsonl`, here))
    const rows3 = traces.flatMap((t) => { const id = `c${t.ci}q${t.qi}`; const top = new Set(t.ids.slice(0, 20)); const hits = t.gold.filter((g) => top.has(g)).length; return [{ id, metric: 'all@20', value: hits === t.gold.length ? 1 : 0 }, { id, metric: 'any@20', value: hits > 0 ? 1 : 0 }] })
    await scores(name, d, rows3)
  }
}
// ── RepoBench-R
if (!ONLY || ONLY === 'code') {
  const name = 'repobench-r-python'
  await dataset(name, 'RepoBench-R (Liu et al. 2023), Python, cross-file-first and cross-file-random: a completion point with candidate snippets from other files, one of which the next line needs. Retrieval only: R@k, MRR, nDCG. dev = train_easy[0:100] + train_hard[0:100]; test = test_easy[0:250] + test_hard[0:250] per setting.', { source: 'tianyang/repobench-r' })
  const codeRuns = new URL('../code/runs/', here)
  if (existsSync(codeRuns)) for (const d of readdirSync(codeRuns).filter((d) => existsSync(new URL(`../code/runs/${d}/traces.jsonl`, here)))) {
    const traces = jsonl(new URL(`../code/runs/${d}/traces.jsonl`, here))
    await items(name, traces.map((t) => ({ id: String(t.id), input: { setting: d.split('-')[2], split: d.split('-')[3] }, expectedOutput: { gold: t.gold } })))
    await scores(name, d, traces.map((t) => ({ id: String(t.id), metric: 'recall@1', value: String(t.order[0]) === String(t.gold) ? 1 : 0 })))
  }
}
console.log(`filed: ${filed.datasets} datasets, ${filed.items} items, ${filed.scores} scores, ${filed.failed} failed${DRY ? ' (dry)' : ''}`)
