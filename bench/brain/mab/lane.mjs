/**
 * The FactConsolidation lane, end to end: plan → retrieve → resolve → read.
 *
 *   node lane.mjs --split=dev|test --rows=semantic,lexical,... --reader=gemma4:31b|enso-flash [--workers=2] [--k=10]
 *
 * Every row hands the SAME reader the SAME prompt; rows differ only in which
 * facts reach it and in what order. The metric is the benchmark's own
 * substring_exact_match. Each run writes runs/mab-<split>-<row>-<reader>/.
 */
import { readFileSync, writeFileSync, mkdirSync, existsSync, appendFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { execSync } from 'node:child_process'
import { load, norm, DATA } from './parse.mjs'
import { build, embed, dot } from './index.mjs'
import { beamResolve } from './beam.mjs'

const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const RESCORE = process.argv.includes('--rescore')
const SPLIT = arg('split', 'dev'), ROWS = arg('rows', 'noreader').split(','), READER = arg('reader', 'gemma4:31b'), K = Number(arg('k', 10)), WORKERS = Number(arg('workers', READER.startsWith('gemma') ? 2 : 3))
const ROOT = new URL('../', import.meta.url).pathname
const PROMPT = readFileSync(ROOT + 'prompts/reader-mab.txt', 'utf8').trim()
const plans = JSON.parse(readFileSync(DATA + 'plans.json', 'utf8'))
const sha = (s) => createHash('sha256').update(s).digest('hex')

// ── readers: one call shape, two hosts. The key is never printed.
const home = process.env.HOME, cfg = (f) => { try { return JSON.parse(readFileSync(`${home}/.hanzo/${f}`, 'utf8')) } catch { return {} } }
const HOST = READER.startsWith('gemma') || READER.startsWith('qwen') ? { url: 'http://127.0.0.1:11434/v1', key: 'ollama' } : { url: process.env.HANZO_API ?? 'https://api.hanzo.ai/v1', key: process.env.HANZO_API_KEY ?? cfg('credentials.json').access_token }
const CACHE = DATA + `reader-cache-${READER.replace(/[^\w.-]/g, '_')}.json`
const rcache = existsSync(CACHE) ? JSON.parse(readFileSync(CACHE, 'utf8')) : {}
async function ask(user, attempt = 0) {
  const h = sha(PROMPT + '\n' + user); if (rcache[h] != null) return rcache[h]
  const messages = [{ role: 'system', content: PROMPT }, { role: 'user', content: user }]
  // Ollama's OpenAI endpoint spends the token budget on hidden thinking and returns nothing; its own
  // endpoint can switch thinking off. Same model, same temperature, same budget.
  const r = HOST.key === 'ollama'
    ? await fetch(`http://127.0.0.1:11434/api/chat`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ model: READER, stream: false, think: false, options: { temperature: 0, num_predict: 64 }, messages }) })
    : await fetch(`${HOST.url}/chat/completions`, { method: 'POST', headers: { authorization: `Bearer ${HOST.key}`, 'content-type': 'application/json' }, body: JSON.stringify({ model: READER, temperature: 0, max_tokens: 64, messages }) })
  const text = await r.text(); let j = null; try { j = JSON.parse(text) } catch {}
  const content = HOST.key === 'ollama' ? j?.message?.content : j?.choices?.[0]?.message?.content
  if (!r.ok || typeof content !== 'string' || !content.trim()) { if (attempt < 4) { await new Promise((z) => setTimeout(z, 3000 * (attempt + 1))); return ask(user, attempt + 1) } throw new Error(`${r.status} ${text.slice(0, 100)}`) }
  rcache[h] = content.trim(); return rcache[h]
}
const saveCache = () => writeFileSync(CACHE, JSON.stringify(rcache))

// ── the metric, as MemoryAgentBench scores it: normalised gold inside normalised prediction
const normalize = (s) => String(s).toLowerCase().replace(/[^\p{L}\p{N}\s]/gu, ' ').replace(/\b(a|an|the)\b/g, ' ').replace(/\s+/g, ' ').trim()
const subEM = (pred, golds) => golds.some((g) => normalize(pred).includes(normalize(g))) ? 1 : 0
function bootstrap(xs, n = 1000) { const N = xs.length; if (!N) return [0, 0]; const means = []; let seed = 7
  const rnd = () => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed / 0x7fffffff }
  for (let b = 0; b < n; b++) { let s = 0; for (let i = 0; i < N; i++) s += xs[Math.floor(rnd() * N)]; means.push(s / N) }
  means.sort((a, b) => a - b); return [means[Math.floor(0.025 * n)], means[Math.floor(0.975 * n)]] }

// ── entity resolution: exact, then unquoted, then lexical over entity names
function finder(ix) {
  const names = [...ix.byEntity.keys()]
  const { bm25 } = { bm25: null }
  let lex = null
  return (name) => {
    if (!name) return null
    const cands = [norm(name), norm(name.replace(/^the\s+/i, '')), norm(name.replace(/^"|"$/g, ''))]
    for (const c of cands) if (ix.byEntity.has(c)) return { key: c, how: 'exact' }
    // containment either way, longest first
    const q = cands[0]; let best = null
    for (const n of names) if ((n.includes(q) || q.includes(n)) && n.length > 3) if (!best || Math.abs(n.length - q.length) < Math.abs(best.length - q.length)) best = n
    if (best) return { key: best, how: 'contains' }
    return null
  }
}
const templateOf = (rel) => ({ capital: 'capital of', head_of_gov: 'head of the government', head_of_state: 'head of state', official_lang: 'official language', author: 'author of', chairperson: 'chairperson of', director: 'director of', ceo: 'chief executive officer of', headquarters: 'headquarters of', educated_at: 'educated', employer: 'employed by', producer: 'company that produced', broadcaster: 'original broadcaster', original_lang: 'original language', founding_place: 'founded in the city', genre: 'type of music', child: 'child', occupation: 'works in the field', head_coach: 'head coach', office: 'is', citizenship: 'citizen of', continent: 'continent', spouse: 'married to', performer: 'performed by', country_origin: 'created in the country', death_place: 'died in the city', birth_place: 'born in the city', language: 'speaks the language', position: 'plays the position', work_location: 'worked in the city', founder: 'founded by', sport: 'associated with the sport', religion: 'religion', notable_work: 'famous for', creator: 'created by', developer: 'developed by' }[rel] ?? rel)

/** One hop: the facts that answer (entity, relation), with every version, current first. */
function hop(ix, find, entityKey, rel, o) {
  // `origin` is the plan's virtual relation: a person comes from a country of citizenship, a sport or
  // a club from the country it was created in. The facts the entity has decide, deterministically.
  if (rel === 'origin') { for (const r of ['citizenship', 'country_origin']) { const h = hop(ix, find, entityKey, r, { ...o, bridge: false }); if (h.versions.length) return h } rel = 'country_origin' }
  const versions = ix.versions(entityKey, rel).slice()
  if (!versions.length && rel === 'spouse') for (const f of ix.byEntity.get(entityKey) ?? []) if (f.relation === 'spouse' && norm(f.object) === entityKey) versions.push({ ...f, object: f.subject, subject: f.object, mirrored: true })
  if (!versions.length && o.fallback && o.bridge !== false) {
    // one typed bridge: a current fact of the entity whose object carries the missing relation
    // ("the country of origin of a club" → its sport → where the sport was created). Bounded to one step.
    for (const f of (ix.byEntity.get(entityKey) ?? []).filter((f) => f.current && norm(f.subject) === entityKey).sort((a, b) => a.serial - b.serial)) {
      const via = hop(ix, find, norm(f.object), rel, { ...o, bridge: false }); if (via.versions.length) { via.versions.forEach((v) => (v.bridge = f.serial)); return via } }
  }
  versions.sort((a, b) => a.serial - b.serial)
  const current = o.timeline ? versions[versions.length - 1] : null
  return { versions, current }
}

/** Follow the plan hop by hop. `o.timeline` picks the latest version deterministically; without it every version is kept for the reader. */
function resolve(ix, find, plan, o) {
  const trace = []; let cur = find(plan.entity); if (!cur) return { evidence: [], trace: [{ step: 'entity', entity: plan.entity, found: null }], answer: null, complete: false }
  trace.push({ step: 'entity', entity: plan.entity, found: cur.key, how: cur.how })
  const evidence = []; let answer = null, complete = true
  for (const rel of plan.chain) {
    const h = hop(ix, find, cur.key, rel, o)
    trace.push({ step: 'hop', entity: cur.key, relation: rel, versions: h.versions.map((f) => f.serial), current: h.current?.serial ?? null })
    if (!h.versions.length) { complete = false; break }
    evidence.push(...(o.timeline ? [h.current] : h.versions))
    const next = o.timeline ? h.current : h.versions[h.versions.length - 1]
    answer = next.object
    const nk = find(next.object); if (!nk) { if (rel !== plan.chain[plan.chain.length - 1]) complete = false; break }
    cur = nk
  }
  return { evidence, trace, answer, complete }
}

// ── rows: what reaches the reader
const uniq = (fs) => { const s = new Set(); return fs.filter((f) => f && !s.has(f.serial) && s.add(f.serial)) }
const bySerial = (fs) => uniq(fs).sort((a, b) => a.serial - b.serial)
function rrf(lists, k = 60) { const s = new Map(); for (const l of lists) l.forEach((f, r) => s.set(f.serial, (s.get(f.serial) ?? 0) + 1 / (k + r + 1))); return [...s.entries()].sort((a, b) => b[1] - a[1]) }
const ROW = {
  semantic:  async (ix, q, qv) => ({ facts: ix.dense(qv, K).map(([i]) => ix.facts[i]) }),
  lexical:   async (ix, q, qv) => ({ facts: bySerial([...ix.dense(qv, K).map(([i]) => ix.facts[i]), ...ix.lexical(q, K).map(([i]) => ix.facts[i])]) }),
  rrf:       async (ix, q, qv, plan, find) => { const ent = find(plan?.entity); const lists = [ix.dense(qv, 20).map(([i]) => ix.facts[i]), ix.lexical(q, 20).map(([i]) => ix.facts[i]), ent ? ix.byEntity.get(ent.key) ?? [] : []]
    const fused = rrf(lists).slice(0, K).map(([serial]) => ix.bySerial.get(serial)); return { facts: fused, note: 'global RRF of three lists' } },
  entities:  async (ix, q, qv, plan, find) => { const ent = find(plan?.entity); const mine = ent ? ix.byEntity.get(ent.key) ?? [] : []; return { facts: bySerial([...mine, ...ix.dense(qv, 5).map(([i]) => ix.facts[i])]) } },
  timeline:  async (ix, q, qv, plan, find) => { const ent = find(plan?.entity); const mine = (ent ? ix.byEntity.get(ent.key) ?? [] : []).filter((f) => f.current); return { facts: bySerial([...mine, ...ix.dense(qv, 5).map(([i]) => ix.facts[i]).filter((f) => f.current)]) } },
  hops:      async (ix, q, qv, plan, find) => { if (!plan) return { facts: ix.dense(qv, K).map(([i]) => ix.facts[i]) }; const r = resolve(ix, find, plan, { timeline: false, fallback: true }); return { facts: bySerial(r.evidence.length ? r.evidence : ix.dense(qv, K).map(([i]) => ix.facts[i])), trace: r.trace } },
  resolver:  async (ix, q, qv, plan, find) => { if (!plan) return { facts: [] }; const r = resolve(ix, find, plan, { timeline: true, fallback: false }); return { facts: bySerial(r.evidence), trace: r.trace, resolved: r.complete ? r.answer : null } },
  full:      async (ix, q, qv, plan, find) => { if (!plan) return { facts: ix.dense(qv, K).map(([i]) => ix.facts[i]) }; const r = resolve(ix, find, plan, { timeline: true, fallback: true })
    const facts = r.complete ? r.evidence : [...r.evidence, ...ix.dense(qv, K).map(([i]) => ix.facts[i]).filter((f) => f.current)]; return { facts: bySerial(facts), trace: r.trace, resolved: r.complete ? r.answer : null } },
  beam:      async (ix, q, qv, plan, find) => { if (!plan) return { facts: [], direct: '' }; const r = beamResolve(ix, find, plan, q); return { facts: bySerial(r.evidence), trace: r.trace, direct: r.answer ?? '' } },
  noreader:  async (ix, q, qv, plan, find) => { if (!plan) return { facts: [], direct: '' }; const r = resolve(ix, find, plan, { timeline: true, fallback: true }); return { facts: bySerial(r.evidence), trace: r.trace, direct: r.complete ? r.answer : '' } },
}

// ── run
const rows = load().filter((r) => (SPLIT === 'dev' ? r.id.endsWith('_6k') : !r.id.endsWith('_6k')))
const commit = execSync('git rev-parse --short HEAD', { cwd: ROOT }).toString().trim()
const ixCache = new Map()
for (const rowName of ROWS) {
  const dir = ROOT + `runs/mab-${SPLIT}-${rowName}-${['noreader', 'beam'].includes(rowName) ? 'none' : READER.replace(/[^\w.-]/g, '_')}/`; mkdirSync(dir, { recursive: true })
  const predFile = dir + 'predictions.jsonl', traceFile = dir + 'traces.jsonl'
  const have = new Set(existsSync(predFile) ? readFileSync(predFile, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l).qid) : [])
  const per = {}, all = [], t0 = Date.now(); let calls = 0, tokens = 0
  for (const r of rows) { if (RESCORE) break
    const ix = ixCache.get(r.context) ?? (ixCache.set(r.context, await build(r.context)), ixCache.get(r.context)); const find = finder(ix)
    const qv = await embed(r.questions); const items = r.questions.map((q, i) => ({ qid: r.qa_ids[i], q, qv: qv[i], gold: r.answers[i], size: r.id.replace('factconsolidation_', '') }))
    let n = 0
    await Promise.all(Array.from({ length: WORKERS }, async () => { while (n < items.length) { const it = items[n++]; if (have.has(it.qid)) continue
      if (!plans[it.q] && !['semantic', 'lexical'].includes(rowName)) continue // no plan yet: leave the question for a later pass
      const t1 = process.hrtime.bigint(); const sel = await ROW[rowName](ix, it.q, it.qv, plans[it.q], find); const ms = Number(process.hrtime.bigint() - t1) / 1e6
      const shown = sel.facts.map((f) => `${f.serial}. ${f.text}`).join('\n'); let pred
      if (sel.direct != null) pred = sel.direct
      else { const user = `Facts:\n${shown || '(none)'}\n\nQuestion: ${it.q}\nAnswer:`; tokens += Math.round(user.length / 4); try { pred = await ask(user); calls++ } catch (e) { process.stderr.write(`\n${it.qid}: ${e.message}\n`); continue } }
      const rec = { qid: it.qid, size: it.size, q: it.q, gold: it.gold, pred, em: subEM(pred, it.gold), facts: sel.facts.length, ms: +ms.toFixed(3) }
      appendFileSync(predFile, JSON.stringify(rec) + '\n'); appendFileSync(traceFile, JSON.stringify({ qid: it.qid, plan: plans[it.q] ?? null, trace: sel.trace ?? null, shown: sel.facts.map((f) => f.serial), resolved: sel.resolved ?? null }) + '\n')
      if (calls % 10 === 0) saveCache() } }))
  }
  saveCache()
  const preds = readFileSync(predFile, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l))
  // The denominator is the benchmark's: every question of every haystack in the split. A question this
  // row could not answer (no plan, a failed call) scores 0 here; it is not dropped from n.
  const answered = new Map(preds.map((p) => [p.qid, p.em]))
  for (const r of rows) { const size = r.id.replace('factconsolidation_', ''); for (const qid of r.qa_ids) (per[size] ??= []).push(answered.get(qid) ?? 0) }
  const metrics = { row: rowName, split: SPLIT, reader: ['noreader', 'beam'].includes(rowName) ? 'none' : READER, k: K, prompt_sha: sha(PROMPT), commit, n: preds.length, by_size: Object.fromEntries(Object.entries(per).map(([s, xs]) => [s, { n: xs.length, substring_em: +(xs.reduce((a, b) => a + b, 0) / xs.length).toFixed(4), ci95: bootstrap(xs).map((x) => +x.toFixed(4)) }])),
    facts_per_q: +(preds.reduce((a, p) => a + p.facts, 0) / preds.length).toFixed(2), retrieval_ms_p50: +[...preds.map((p) => p.ms)].sort((a, b) => a - b)[Math.floor(preds.length / 2)].toFixed(3), retrieval_ms_p95: +[...preds.map((p) => p.ms)].sort((a, b) => a - b)[Math.floor(preds.length * 0.95)].toFixed(3), reader_calls: calls, context_tokens_per_q: calls ? Math.round(tokens / calls) : 0, wall_s: Math.round((Date.now() - t0) / 1000) }
  writeFileSync(dir + 'metrics.json', JSON.stringify(metrics, null, 1))
  writeFileSync(dir + 'meta.json', JSON.stringify({ bench: 'MemoryAgentBench Conflict_Resolution (FactConsolidation)', dataset_sha256: sha(readFileSync(DATA + 'Conflict_Resolution-00000-of-00001.parquet')), split: SPLIT, row: rowName, reader: metrics.reader, planner: Object.entries(Object.values(plans).reduce((a, p) => (a[p.model] = (a[p.model] ?? 0) + 1, a), {})).map(([m, n]) => `${m}:${n}`).join(' '), embedding: 'zenlm/zen-embedding-0.6b', k: K, temperature: 0, max_tokens: 64, prompt: 'prompts/reader-mab.txt', prompt_sha256: sha(PROMPT), commit }, null, 1))
  console.log(`${rowName.padEnd(9)} ${Object.entries(metrics.by_size).map(([s, m]) => `${s} ${(m.substring_em * 100).toFixed(1)} [${(m.ci95[0] * 100).toFixed(0)}–${(m.ci95[1] * 100).toFixed(0)}] n=${m.n}`).join('  ')}   facts/q ${metrics.facts_per_q}  p50 ${metrics.retrieval_ms_p50}ms  calls ${calls}`)
}
