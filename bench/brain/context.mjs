/**
 * The Context engine, over a LoCoMo-shaped store.
 *
 * READ is a small query compiler: several bounded candidate generators run in
 * parallel — dense turns, BM25, atomic facts (cited turns), canonical entities,
 * the timeline, adjacency, one hop of typed expansion, query facets — and a
 * second retrieval hop whose query is built from what the first hop resolved,
 * not from the original sentence. Every candidate then gets ONE score: its
 * dense similarity plus a weighted contribution from each generator that
 * nominated it, and every contribution is written to the trace, so the rank a
 * turn ends at is explainable term by term.
 *
 * Nothing floods the pool. cer.mjs measured what happens when whole ranked
 * lists are fused (−4 points): a generator here proposes at most its budget of
 * candidates, and a candidate proposed by nobody but a weak generator carries
 * only that generator's small weight. The failed shapes are kept as rows —
 * pseudo-relevance feedback, multi-query with reciprocal-rank fusion, chain
 * search, surface-entity expansion, global RRF — because a table that shows
 * only what worked does not say why.
 *
 *   node context.mjs --split=dev|test|all --rows=all|<name,...> --facts=locomo|ours|union --k=20 [--write]
 *   node context.mjs --facets --split=dev     # fill the facet cache for a split
 *
 * Retrieval is measured only here; answers are measured by run.mjs, which
 * takes this file's ranked ids through `rank()`.
 */
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { execSync } from 'node:child_process'
import { complete, embed } from './llm.mjs'
import { rank as rankScore, ci, pct, quantile } from './metrics.mjs'

const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.slice(k.length + 3) : d }
const has = (k) => process.argv.includes(`--${k}`)
export const K = Number(arg('k', 20))
export const DEV = new Set([0, 1, 2])
export const SPLITS = { dev: (ci) => DEV.has(ci), test: (ci) => !DEV.has(ci), all: () => true }

// ── data
// --embed=<name> loads brain-vectors-<name>.json (see embed-store.mjs): the same store in another embedding space
export const EMBED = arg('embed', process.env.EMBED ?? '')
const suffix = EMBED ? `-${EMBED}` : ''
const store = JSON.parse(readFileSync(new URL(`./brain-vectors${suffix}.json`, import.meta.url), 'utf8'))
const corpus = JSON.parse(readFileSync(new URL('./locomo10.json', import.meta.url), 'utf8'))
const FACTS = arg('facts', process.env.FACTS ?? 'locomo')
const loadFacts = (f) => { const p = new URL(f, import.meta.url); return existsSync(p) ? JSON.parse(readFileSync(p, 'utf8')) : null }
const factSets = { locomo: loadFacts(`./facts-vectors${suffix}.json`), ours: loadFacts(`./facts-ours-vectors${suffix}.json`) }
if (EMBED && !process.env.EMBED_MODEL) process.env.EMBED_MODEL = { minilm: 'all-minilm' }[EMBED] ?? EMBED
if (FACTS !== 'locomo' && !factSets.ours) { console.error('no facts-ours-vectors.json yet — run extract.mjs, or use --facts=locomo'); process.exit(1) }
const factsFor = (ci) => FACTS === 'union' ? [...(factSets.locomo?.[ci] ?? []), ...(factSets.ours?.[ci] ?? [])] : (factSets[FACTS]?.[ci] ?? [])

const dot = (a, b) => { let s = 0; for (let i = 0; i < a.length; i++) s += a[i] * b[i]; return s }
const unit = (v) => { const n = Math.sqrt(dot(v, v)) || 1; return v.map((x) => x / n) }
const STOP = new Set('the a an and or of to in on at for with is are was were be been it its this that i you he she they we my your his her their our me him them us do did does have has had not so but if then than as about from by up out into over just like really also very what when where who how why which did'.split(' '))
export const toks = (s) => String(s).toLowerCase().replace(/[^a-z0-9' ]+/g, ' ').split(/\s+/).filter((w) => w.length > 1 && !STOP.has(w))
const MONTHS = { january: 1, february: 2, march: 3, april: 4, may: 5, june: 6, july: 7, august: 8, september: 9, october: 10, november: 11, december: 12 }
const parseDate = (s) => { const m = s?.match(/(\d{1,2}) (\w+),? (\d{4})/); return m && MONTHS[m[2].toLowerCase()] ? { y: +m[3], m: MONTHS[m[2].toLowerCase()], d: +m[1] } : null }
export const cue = (q) => { const l = q.toLowerCase(); const y = l.match(/\b(20\d\d)\b/); const mo = Object.keys(MONTHS).find((m) => l.includes(m)); return { y: y ? +y[1] : null, m: mo ? MONTHS[mo] : null } }
const ENT_STOP = new Set(`I I'm I've I'll I'd The A An And But So Yes No Oh Hey Wow Thanks That This It What When Where Who How Why Also Just Really Maybe Sure Okay OK Well Great Good Nice Yeah Haha Lol My Your Our`.split(/\s+/))
const surfaceNames = (s) => { const o = new Set(); for (const m of String(s).matchAll(/\b([A-Z][a-zA-Z']{2,})\b/g)) if (!ENT_STOP.has(m[1])) o.add(m[1].toLowerCase()); return o }

/** BM25 over one conversation's turns. */
class BM25 {
  constructor(docs, k1 = 1.2, b = 0.75) {
    this.docs = docs.map(toks); this.k1 = k1; this.b = b; this.n = docs.length
    this.avg = this.docs.reduce((a, d) => a + d.length, 0) / Math.max(1, this.n)
    this.df = new Map(); for (const d of this.docs) for (const w of new Set(d)) this.df.set(w, (this.df.get(w) ?? 0) + 1)
    this.tf = this.docs.map((d) => { const m = new Map(); for (const w of d) m.set(w, (m.get(w) ?? 0) + 1); return m })
  }
  scores(query) {
    const q = toks(query), out = new Float64Array(this.n)
    for (const w of new Set(q)) { const df = this.df.get(w); if (!df) continue; const idf = Math.log(1 + (this.n - df + 0.5) / (df + 0.5))
      for (let i = 0; i < this.n; i++) { const tf = this.tf[i].get(w); if (!tf) continue; const dl = this.docs[i].length; out[i] += idf * (tf * (this.k1 + 1)) / (tf + this.k1 * (1 - this.b + this.b * dl / this.avg)) } }
    return out
  }
}

/** The index for one conversation: turns, facts, entities, dates, lexical. */
function build(ci) {
  const cached = store[ci], raw = corpus[ci], conv = raw.conversation
  const speakers = new Set([conv.speaker_a, conv.speaker_b].filter(Boolean).map((s) => s.toLowerCase()))
  const date = {}; for (const k of Object.keys(conv)) if (k.endsWith('_date_time')) date[k.replace('_date_time', '')] = parseDate(conv[k])
  const turns = cached.turns.map((t, i) => { const sess = 'session_' + t.id.match(/^D(\d+):/)[1]
    return { id: t.id, i, sess, when: date[sess], v: unit(t.v), text: t.text, body: t.text.replace(/^[^:]+:\s*/, ''), speaker: t.text.split(':')[0].toLowerCase() } })
  const byId = new Map(turns.map((t) => [t.id, t]))
  const facts = factsFor(ci).map((f, fi) => ({ fi, text: f.text, v: unit(f.v), idx: (f.ids ?? []).map((x) => byId.get(x)?.i).filter((x) => x != null),
    entities: new Set((f.entities ?? []).map((e) => String(e).toLowerCase())), subject: f.subject?.toLowerCase?.() ?? null, relation: f.relation ?? null, time: f.event_time ?? f.time ?? null,
    names: new Set([...surfaceNames(f.text)].filter((e) => !speakers.has(e))) }))
  const citing = new Map(); facts.forEach((f) => { for (const i of f.idx) (citing.get(i) ?? citing.set(i, []).get(i)).push(f.fi) })
  const byEntity = new Map(); facts.forEach((f) => { for (const e of f.entities) (byEntity.get(e) ?? byEntity.set(e, new Set()).get(e)); for (const e of f.entities) for (const i of f.idx) byEntity.get(e).add(i) })
  const aliases = new Map(); for (const [e] of byEntity) for (const a of e.split(/\s*\|\s*/)) aliases.set(a, e)
  const answers = new Map(raw.qa.map((q) => [q.question, q.answer]))
  return { ci, turns, byId, facts, citing, byEntity, aliases, speakers, bm25: new BM25(turns.map((t) => t.body)), answers }
}
export const ixs = store.map((_, ci) => build(ci))

// ── caches: query-time embeddings and facets, on disk so a rerun is free
const CACHE_DIR = new URL('./data/', import.meta.url); mkdirSync(CACHE_DIR, { recursive: true })
const embedFile = new URL(`./data/embed-cache${suffix}.json`, import.meta.url)
const embedCache = new Map(existsSync(embedFile) ? Object.entries(JSON.parse(readFileSync(embedFile, 'utf8'))) : [])
let embedDirty = 0
async function qvec(text) {
  if (embedCache.has(text)) return embedCache.get(text)
  const [v] = await embed([text]); const u = unit(v); embedCache.set(text, u); if (++embedDirty % 50 === 0) flushEmbed(); return u
}
// merge with what another process wrote meanwhile, so two runs never lose each other's entries
const flushEmbed = () => { if (existsSync(embedFile)) { try { for (const [k, v] of Object.entries(JSON.parse(readFileSync(embedFile, 'utf8')))) if (!embedCache.has(k)) embedCache.set(k, v) } catch {} } writeFileSync(embedFile, JSON.stringify(Object.fromEntries(embedCache))) }
/** Many texts at once: one queued request per 64 instead of one per question. */
export async function qvecMany(texts) {
  const todo = [...new Set(texts.filter((t) => !embedCache.has(t)))]
  for (let i = 0; i < todo.length; i += 64) { const chunk = todo.slice(i, i + 64); const vs = await embed(chunk); chunk.forEach((t, j) => embedCache.set(t, unit(vs[j]))); process.stderr.write(`\r  embedded ${Math.min(i + 64, todo.length)}/${todo.length}`) }
  if (todo.length) { flushEmbed(); process.stderr.write('\n') }
}
const facetFile = new URL('./data/facets.json', import.meta.url)
const facetCache = existsSync(facetFile) ? JSON.parse(readFileSync(facetFile, 'utf8')) : {}
const FACET_PROMPT = 'Rewrite the question below as 3 to 5 short search queries for a store of two people\'s chat turns. Cover the different ways the same memory could have been phrased, and the sub-questions the question contains (each entity, each time). Reply with JSON: {"queries":[...]}.'
export async function facets(question) {
  if (facetCache[question]) return facetCache[question]
  const r = await complete([{ role: 'system', content: FACET_PROMPT }, { role: 'user', content: question }], { json: true, max_tokens: 200 })
  const qs = (r.json?.queries ?? []).map(String).filter((s) => s.length > 3).slice(0, 5)
  facetCache[question] = qs; writeFileSync(facetFile, JSON.stringify(facetCache, null, 1)); return qs
}

// ── the planner: what kind of lookup this is, from the question alone
export function plan(question) {
  const l = question.toLowerCase(), c = cue(question)
  return {
    temporal: /\b(when|what (date|day|month|year|time)|how long|how many (days|weeks|months|years)|since|until|before|after|first time|last time)\b/.test(l) || !!(c.y || c.m),
    current: /\b(now|currently|these days|still|latest|most recent|now living|now working)\b/.test(l),
    multi: /\b(after|before|since|both|and then|following|prior to|the same|together|because of|led to)\b/.test(l) || (l.match(/\bof\b/g) ?? []).length >= 2,
    cue: c,
  }
}

// ── generators and the scorer
export const DEFAULT_W = { fact: 0.2, lex: 0.15, ent: 0.1, time: 0.05, adj: 0.06, exp: 0.08, hop: 0.3, facet: 0.2 }
export const BUDGET = { dense: 40, lex: 20, fact: 20, ent: 12, time: 8, adj: 3, exp: 8, hop: 10, facetSeed: 8 }

/**
 * retrieve(ix, q, cfg) -> { ids, examined, contrib: Map<i, {…}>, ms }
 * cfg: { dense, lex, fact, ent, time, adj, exp, hop, facet, prf, rrf, chain, surface, w, budget }
 */
export async function retrieve(ix, q, cfg) {
  const t0 = process.hrtime.bigint()
  const W = { ...DEFAULT_W, ...(cfg.w ?? {}) }, B = { ...BUDGET, ...(cfg.budget ?? {}) }
  let qv = unit(q.v); const p = plan(q.question)
  const dense = ix.turns.map((t) => dot(qv, t.v))
  const order = [...dense.keys()].sort((a, b) => dense[b] - dense[a])
  if (cfg.prf) { // pseudo-relevance feedback: the query drifts toward its own top three
    const m = new Float64Array(qv.length); for (const i of order.slice(0, 3)) for (let d = 0; d < m.length; d++) m[d] += ix.turns[i].v[d] / 3
    qv = unit(qv.map((x, d) => 0.7 * x + 0.3 * m[d])); const d2 = ix.turns.map((t) => dot(qv, t.v)); const o2 = [...d2.keys()].sort((a, b) => d2[b] - d2[a])
    return finish(o2.slice(0, K).map((i) => ix.turns[i].id), o2.length, new Map(), t0)
  }
  const cand = new Map(); const add = (i, gen, val) => { const c = cand.get(i) ?? cand.set(i, { dense: dense[i] }).get(i); c[gen] = Math.max(c[gen] ?? 0, val) }
  const lists = { dense: order.slice(0, B.dense) }
  for (const i of lists.dense) add(i, 'seed', 1)
  if (cfg.lex) { const s = ix.bm25.scores(q.question); const mx = Math.max(...s) || 1; const o = [...s.keys()].filter((i) => s[i] > 0).sort((a, b) => s[b] - s[a]).slice(0, B.lex); lists.lex = o; for (const i of o) add(i, 'lex', s[i] / mx) }
  let fScore = null, fOrder = null
  if (cfg.fact && ix.facts.length) { fScore = ix.facts.map((f) => dot(qv, f.v)); fOrder = [...fScore.keys()].sort((a, b) => fScore[b] - fScore[a])
    lists.fact = []; for (const fi of fOrder.slice(0, B.fact)) for (const i of ix.facts[fi].idx) { add(i, 'fact', fScore[fi]); lists.fact.push(i) } }
  if (cfg.ent && ix.byEntity.size) { // canonical entities the question names, through the typed facts
    const asked = new Set(); const l = q.question.toLowerCase(); for (const [alias, e] of ix.aliases) if (alias.length > 2 && l.includes(alias)) asked.add(e)
    lists.ent = []; for (const e of asked) { const turns = [...(ix.byEntity.get(e) ?? [])].sort((a, b) => dense[b] - dense[a]).slice(0, B.ent); for (const i of turns) { add(i, 'ent', 1); lists.ent.push(i) } } }
  if (cfg.time && (p.cue.y || p.cue.m)) { const inWindow = ix.turns.filter((t) => t.when && (!p.cue.y || t.when.y === p.cue.y) && (!p.cue.m || t.when.m === p.cue.m)).map((t) => t.i).sort((a, b) => dense[b] - dense[a]).slice(0, B.time); lists.time = inWindow; for (const i of inWindow) add(i, 'time', 1) }
  if (cfg.adj) { const top = order.slice(0, B.adj); lists.adj = []; for (const i of top) for (const j of [i - 1, i + 1]) if (ix.turns[j] && ix.turns[j].sess === ix.turns[i].sess) { add(j, 'adj', 1); lists.adj.push(j) } }
  if (cfg.exp && fOrder) { // one bounded hop through the typed fact graph: facts sharing a canonical entity with a top fact
    const seen = new Set(); let n = 0; lists.exp = []
    for (const fi of fOrder.slice(0, 5)) for (const e of ix.facts[fi].entities) for (const [gi, g] of ix.facts.entries()) if (gi !== fi && !seen.has(gi) && g.entities.has(e)) { seen.add(gi); if (n++ >= B.exp) break; for (const i of g.idx) { add(i, 'exp', fScore[fi] * fScore[gi]); lists.exp.push(i) } } }
  if (cfg.surface && fOrder) { // the failed variant: surface names, not canonical entities
    const asked = surfaceNames(q.question); lists.surface = []
    for (const fi of fOrder.slice(0, 8)) for (const nm of ix.facts[fi].names) if (!asked.has(nm)) for (const [gi, g] of ix.facts.entries()) if (g.names.has(nm) && gi !== fi) for (const i of g.idx) { add(i, 'exp', fScore[fi]); lists.surface.push(i) } }
  if (cfg.facet) { // 3–5 rewrites, each a small seed pool; no fusion of whole lists
    const qs = await facets(q.question); lists.facet = []
    for (const f of qs) { const fv = await qvec(f); const s = ix.turns.map((t) => dot(fv, t.v)); const o = [...s.keys()].sort((a, b) => s[b] - s[a]).slice(0, B.facetSeed); for (const i of o) { add(i, 'facet', s[i]); lists.facet.push(i) }
      if (fOrder) { const fs = ix.facts.map((x) => dot(fv, x.v)); const fo = [...fs.keys()].sort((a, b) => fs[b] - fs[a]).slice(0, 5); for (const fi of fo) for (const i of ix.facts[fi].idx) { add(i, 'facet', fs[fi]); lists.facet.push(i) } } } }
  if (cfg.hop || cfg.chain) { // the second hop is asked with what the first resolved, not with the original sentence
    const text = hopText(ix, q, cfg, order, fOrder)
    const hv = await qvec(text); const s = ix.turns.map((t) => dot(hv, t.v)); const o = [...s.keys()].sort((a, b) => s[b] - s[a]).slice(0, B.hop); lists.hop = o; for (const i of o) add(i, 'hop', s[i])
    if (fOrder) { // typed: keep to facts about the entities the first hop resolved, when the facts carry entities
      const resolved = new Set(); for (const fi of fOrder.slice(0, 3)) for (const e of ix.facts[fi].entities) resolved.add(e)
      const fs = ix.facts.map((x) => dot(hv, x.v)); let fo = [...fs.keys()].sort((a, b) => fs[b] - fs[a])
      if (resolved.size) fo = fo.filter((fi) => [...ix.facts[fi].entities].some((e) => resolved.has(e)))
      for (const fi of fo.slice(0, 8)) for (const i of ix.facts[fi].idx) { add(i, 'hop', fs[fi]); lists.hop.push(i) } } }
  let ids
  if (cfg.rrf) { // the failed shape: fuse every generator's whole list by reciprocal rank
    const score = new Map(); for (const [gen, list] of Object.entries(lists)) list.forEach((i, r) => score.set(i, (score.get(i) ?? 0) + 1 / (60 + r)))
    ids = [...score.entries()].sort((a, b) => b[1] - a[1]).slice(0, K).map(([i]) => ix.turns[i].id)
  } else {
    const scored = [...cand.entries()].map(([i, c]) => { let s = c.dense; c.score = s; for (const g of ['fact', 'lex', 'ent', 'time', 'adj', 'exp', 'hop', 'facet']) if (c[g]) { const part = W[g] * c[g]; c[`w_${g}`] = part; s += part } c.score = s; return [i, s] }).sort((a, b) => b[1] - a[1])
    ids = scored.slice(0, K).map(([i]) => ix.turns[i].id)
  }
  return finish(ids, cand.size, cand, t0)
}
const finish = (ids, examined, contrib, t0) => ({ ids, examined, contrib, ms: Number(process.hrtime.bigint() - t0) / 1e6 })

/** A query that is not in the store — a LoCoMo-Conv request, a live user — embedded at query time, then retrieved like any other. */
export async function retrieveText(ix, question, cfg) { const v = await qvec(question); return retrieve(ix, { question, v }, cfg) }
/** The frozen configuration for a fact set, if the dev sweep has produced one. */
export function frozenConfig(row = '+iterative hops') { const f = new URL(`./ablations/frozen-${FACTS}.json`, import.meta.url); if (!existsSync(f)) return ROWS[row]; const z = JSON.parse(readFileSync(f, 'utf8')); return { ...ROWS[z.row ?? row], w: z.w, budget: z.budget } }

/** The second hop's query: the question plus what the first hop resolved (or, for chain search, the top turn alone). */
export function hopText(ix, q, cfg, order, fOrder) {
  const known = cfg.chain ? [ix.turns[order[0]].body] : (fOrder ? fOrder.slice(0, 3).map((fi) => ix.facts[fi].text) : order.slice(0, 2).map((i) => ix.turns[i].body))
  return cfg.chain ? known[0] : `${q.question}\nKnown: ${known.join(' ')}`
}
/** Every query-time text a configuration will embed on a split, embedded in batches before evaluation. */
export async function prepare(cfg, split = 'dev') {
  const inSplit = SPLITS[split], texts = []
  for (const [ci, c] of store.entries()) { if (!inSplit(ci)) continue; const ix = ixs[ci]
    for (const q of c.qa) { if (!q.evidence.length) continue; const qv = unit(q.v)
      if (cfg.hop || cfg.chain) { const dense = ix.turns.map((t) => dot(qv, t.v)); const order = [...dense.keys()].sort((a, b) => dense[b] - dense[a])
        let fOrder = null; if (cfg.fact && ix.facts.length) { const fs = ix.facts.map((f) => dot(qv, f.v)); fOrder = [...fs.keys()].sort((a, b) => fs[b] - fs[a]) }
        texts.push(hopText(ix, q, cfg, order, fOrder)) }
      if (cfg.facet) texts.push(...(await facets(q.question))) } }
  await qvecMany(texts)
}

/** Named rows: what each table carries, in the order it carries them. */
export const ROWS = {
  'semantic only': {},
  '+lexical': { lex: true },
  '+facts': { lex: true, fact: true },
  '+entities': { lex: true, fact: true, ent: true },
  '+timeline': { lex: true, fact: true, ent: true, time: true },
  '+adjacency': { lex: true, fact: true, ent: true, time: true, adj: true },
  '+typed graph': { lex: true, fact: true, ent: true, time: true, adj: true, exp: true },
  '+iterative hops': { lex: true, fact: true, ent: true, time: true, adj: true, exp: true, hop: true },
  'full (+facets)': { lex: true, fact: true, ent: true, time: true, adj: true, exp: true, hop: true, facet: true },
  'failed: PRF': { prf: true },
  'failed: multi-query RRF': { facet: true, fact: true, lex: true, rrf: true, budget: { facetSeed: 40, fact: 40, lex: 40 } },
  'failed: chain search': { lex: true, fact: true, chain: true },
  'failed: surface entities': { lex: true, fact: true, surface: true },
  'failed: global RRF': { lex: true, fact: true, time: true, adj: true, rrf: true, budget: { dense: 60, lex: 60, fact: 60 } },
}

/** Evaluate one configuration on a split: per-category retrieval metrics, grades, latency. */
export async function evaluate(cfg, split = 'all', o = {}) {
  const inSplit = SPLITS[split]; const rows = []; const traces = []
  for (const [ci, c] of store.entries()) { if (!inSplit(ci)) continue; const ix = ixs[ci]
    for (const [qi, q] of c.qa.entries()) { if (!q.evidence.length) continue
      const r = await retrieve(ix, q, cfg); const m = rankScore(r.ids, q.evidence)
      const gold = new Set(q.evidence), goldFacts = new Set(); for (const g of q.evidence) for (const fi of ix.citing.get(ix.byId.get(g)?.i) ?? []) goldFacts.add(fi)
      const top = r.ids.slice(0, K), ans = String(ix.answers.get(q.question) ?? '').toLowerCase()
      const supported = top.some((id) => { const t = ix.byId.get(id); if (gold.has(id)) return true; if (ans.length > 2 && t.body.toLowerCase().includes(ans)) return true; return (ix.citing.get(t.i) ?? []).some((fi) => goldFacts.has(fi)) })
      const tokens = Math.round(top.reduce((a, id) => a + ix.byId.get(id).text.length, 0) / 4)
      rows.push({ ci, qi, cat: q.category, ...m, exact: m['any@' + K] ?? m['any@20'], supported: Number(supported), examined: r.examined, ms: r.ms, tokens })
      if (o.trace) traces.push({ ci, qi, question: q.question, gold: q.evidence, ids: r.ids, contrib: top.map((id) => ({ id, ...Object.fromEntries(Object.entries(r.contrib.get(ix.byId.get(id).i) ?? {}).map(([k, v]) => [k, +(+v).toFixed(4)])) })) })
    } }
  const by = {}; for (const r of rows) (by[r.cat] ??= []).push(r); by.all = rows
  const summary = {}
  for (const [cat, rs] of Object.entries(by)) { const s = { n: rs.length }; for (const k of Object.keys(rs[0]).filter((k) => /^(all|any|recall)@|^mrr|^ndcg|^supported$|^exact$/.test(k))) s[k] = ci(rs.map((r) => r[k]))
    s.examined = rs.reduce((a, r) => a + r.examined, 0) / rs.length; s.tokens = rs.reduce((a, r) => a + r.tokens, 0) / rs.length; s.p50 = quantile(rs.map((r) => r.ms), 0.5); s.p95 = quantile(rs.map((r) => r.ms), 0.95); summary[cat] = s }
  flushEmbed(); return { summary, rows, traces }
}

const commit = () => { try { return execSync('git rev-parse --short=12 HEAD', { cwd: new URL('.', import.meta.url).pathname }).toString().trim() } catch { return 'unknown' } }
const sha = (s) => createHash('sha256').update(s).digest('hex').slice(0, 16)

const MAIN = process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())
if (MAIN) {
  const split = arg('split', 'dev'), want = arg('rows', 'all'), inSplit = SPLITS[split]
  if (has('facets')) { let n = 0; for (const [ci, c] of store.entries()) { if (!inSplit(ci)) continue; for (const q of c.qa) { if (!q.evidence.length) continue; await facets(q.question); if (++n % 25 === 0) process.stderr.write(`\r  facets ${n}`) } } console.log(`\nfacets cached for ${n} questions (${split})`); process.exit(0) }
  const names = want === 'all' ? Object.keys(ROWS) : want.split(',').map((s) => s.trim())
  // --frozen: every row takes the weights and budgets the dev sweep froze, so a test table is one configuration, ablated
  const frozenFile = new URL(`./ablations/frozen-${FACTS}.json`, import.meta.url)
  const frozen = has('frozen') ? JSON.parse(readFileSync(frozenFile, 'utf8')) : null
  if (has('frozen')) console.log(`frozen configuration from ${frozenFile.pathname.split('/').pop()} · commit ${frozen.commit}`)
  const withFrozen = (cfg) => frozen ? { ...cfg, w: { ...frozen.w, ...(cfg.w ?? {}) }, budget: { ...frozen.budget, ...(cfg.budget ?? {}) } } : cfg
  const cats = [...new Set(store.flatMap((c) => c.qa.map((q) => q.category)))].sort()
  const label = { 1: 'multi-hop', 2: 'temporal', 3: 'open-domain', 4: 'single-hop' }
  console.log(`\n── LoCoMo retrieval · split ${split} · facts ${FACTS} · k=${K} · commit ${commit()} ──`)
  console.log(`${'row'.padEnd(26)} ${cats.map((c) => (label[c] ?? c).padStart(9) + ' ALL/ANY').join('  ')}   MRR   nDCG  SUPP   pool   tok    p50ms`)
  for (const name of names) { const cfg = withFrozen(ROWS[name] ?? JSON.parse(name)); await prepare(cfg, split); const t = await evaluate(cfg, split, { trace: has('write') }); const s = t.summary
    const cells = cats.map((c) => s[c] ? `${pct(s[c][`all@${K}`].mean).padStart(5)}/${pct(s[c][`any@${K}`].mean).padStart(5)}` : '     -/-    ')
    console.log(`${name.padEnd(26)} ${cells.join('        ')}   ${pct(s.all.mrr.mean).padStart(5)} ${pct(s.all[`ndcg@${K}`].mean).padStart(5)} ${pct(s.all.supported.mean).padStart(5)}  ${s.all.examined.toFixed(0).padStart(4)}  ${s.all.tokens.toFixed(0).padStart(5)}  ${s.all.p50.toFixed(2).padStart(6)}`)
    if (has('write')) { const dir = new URL(`./runs/locomo-retrieval-${split}-${name.replace(/[^a-z0-9]+/gi, '-').replace(/^-|-$/g, '').toLowerCase()}-${FACTS}${suffix}/`, import.meta.url); mkdirSync(dir, { recursive: true })
      writeFileSync(new URL('metrics.json', dir), JSON.stringify({ row: name, cfg, split, facts: FACTS, k: K, frozen: frozen ? { commit: frozen.commit, objective: frozen.objective } : null, summary: s }, null, 1))
      writeFileSync(new URL('traces.jsonl', dir), t.traces.map((x) => JSON.stringify(x)).join('\n'))
      writeFileSync(new URL('meta.json', dir), JSON.stringify({ commit: commit(), embedding: process.env.EMBED_MODEL ?? 'zenlm/zen-embedding-0.6b', facts: FACTS, k: K, split, store: sha(readFileSync(new URL('./brain-vectors.json', import.meta.url))), when: new Date().toISOString() }, null, 1)) }
  }
}
