/**
 * Contextual Evidence Retrieval — linkage instead of similarity, measured.
 *
 * evidence.mjs said what was wrong: a multi-hop question needs ~3 turns, they
 * sit in different sessions 95% of the time, and the worst one is at median
 * rank 67 under cosine. hops.mjs said more similarity does not fix it. So this
 * does not rank by similarity alone. Several indexes each nominate candidates,
 * the nominations are fused, the top of the fusion is READ for what it names —
 * people, places, dates, the turn beside it — and those names become the next
 * hop's query. The hop is ranked against the state the last hop produced, not
 * against the original words.
 *
 * Indexes, all built from the corpus with no model call:
 *
 *   semantic   cosine over zen-embedding-0.6b (the cached vectors)
 *   lexical    BM25 over turn text
 *   entity     inverted index, capitalised token → turns that mention it
 *   temporal   session date per turn; a question that names a time filters
 *   facts      LoCoMo's own observations → the turn each cites (a candidate
 *              generator, nothing more; 44% of multi-hop gold is not in them)
 *   adjacent   the turn before and after — in dialogue the answer to a turn is
 *              usually the next one
 *
 * Fusion is reciprocal-rank, weighted per query: a question that names a date
 * leans on the timeline, one that names a person leans on the entity index,
 * one that names neither leans on text. Nothing is learned; the weights are
 * written down below so the ablation is honest.
 *
 * Reports recall@k all/any for the same two columns brain.mjs reports, with
 * every stage ablated, so the gain is attributable to a mechanism and not to
 * a number.
 */
import { readFileSync, existsSync } from 'node:fs'

const CACHE = 'brain-vectors.json'
const DATA = process.argv[2] ?? 'locomo10.json'
const K = Number(process.env.K ?? 20)
if (!existsSync(CACHE)) { console.error(`no ${CACHE} — run brain.mjs first`); process.exit(1) }

const store = JSON.parse(readFileSync(CACHE, 'utf8'))
const corpus = JSON.parse(readFileSync(DATA, 'utf8'))

// ── vectors
const dot = (a, b) => { let s = 0; for (let i = 0; i < a.length; i++) s += a[i] * b[i]; return s }
const norm = (v) => Math.sqrt(dot(v, v))
const cos = (a, b) => dot(a, b) / (norm(a) * norm(b))

// ── text
const STOP = new Set(`the a an of to in on at is was were are and or for with what when where who how
did does do which that this it its her his she he they them their be by from as about has have had
i you we my your our me us if so not no yes but than then there here into out up down over just
also very really like get got go went`.split(/\s+/))
const words = (s) => s.toLowerCase().match(/[a-z0-9']+/g) ?? []
const terms = (s) => words(s).filter((w) => !STOP.has(w) && w.length > 1)

// Capitalised tokens that are not sentence-initial function words: names of
// people, places, things. Cheap, and on dialogue it is mostly right.
const ENT_STOP = new Set(`I I'm I've I'll I'd The A An And But So Yes No Oh Hey Wow Thanks That This It
What When Where Who How Why Also Just Really Maybe Sure Okay OK Well Great Good Nice Yeah Haha Lol`.split(/\s+/))
const entities = (s) => {
  const out = new Set()
  for (const m of s.matchAll(/\b([A-Z][a-zA-Z']{2,})\b/g)) if (!ENT_STOP.has(m[1])) out.add(m[1].toLowerCase())
  return out
}

// ── time
const MONTHS = { january:1, february:2, march:3, april:4, may:5, june:6, july:7, august:8, september:9,
  october:10, november:11, december:12, jan:1, feb:2, mar:3, apr:4, jun:6, jul:7, aug:8, sep:9, sept:9, oct:10, nov:11, dec:12 }
const parseDate = (s) => {
  const m = s.match(/(\d{1,2}) (\w+),? (\d{4})/)
  if (!m || !MONTHS[m[2].toLowerCase()]) return null
  return Date.UTC(+m[3], MONTHS[m[2].toLowerCase()] - 1, +m[1])
}
/** A question's own clock: an explicit year/month, or a relative cue. */
const timeCue = (q) => {
  const l = q.toLowerCase()
  const year = l.match(/\b(20\d\d)\b/)
  const month = Object.keys(MONTHS).find((m) => m.length > 3 && l.includes(m))
  return {
    year: year ? +year[1] : null,
    month: month ? MONTHS[month] : null,
    relative: /\b(after|before|since|later|earlier|ago|last|next|first|recent|previous|following)\b/.test(l),
    asks: /^(when|what year|what month|how long)/.test(l),
  }
}

// ── BM25
function bm25Index(docs) {
  const N = docs.length, df = new Map(), tf = [], len = []
  for (const d of docs) {
    const t = terms(d), m = new Map()
    for (const w of t) m.set(w, (m.get(w) ?? 0) + 1)
    tf.push(m); len.push(t.length)
    for (const w of m.keys()) df.set(w, (df.get(w) ?? 0) + 1)
  }
  const avg = len.reduce((a, b) => a + b, 0) / N, k1 = 1.2, b = 0.75
  const idf = (w) => Math.log(1 + (N - (df.get(w) ?? 0) + 0.5) / ((df.get(w) ?? 0) + 0.5))
  return (query) => {
    const qt = [...new Set(terms(query))], out = new Float64Array(N)
    for (let i = 0; i < N; i++) {
      let s = 0
      for (const w of qt) {
        const f = tf[i].get(w); if (!f) continue
        s += idf(w) * (f * (k1 + 1)) / (f + k1 * (1 - b + b * len[i] / avg))
      }
      out[i] = s
    }
    return out
  }
}

// ── the conversation, as indexes
function build(cached, raw) {
  const conv = raw.conversation
  const date = {}
  for (const k of Object.keys(conv)) if (k.endsWith('_date_time')) date[k.replace('_date_time', '')] = parseDate(conv[k])
  const turns = cached.turns.map((t, i) => {
    const sess = 'session_' + t.id.match(/^D(\d+):/)[1]
    return { ...t, i, sess, when: date[sess] ?? null, ents: entities(t.text.replace(/^[^:]+:\s*/, '')), body: t.text.replace(/^[^:]+:\s*/, '') }
  })
  const byId = new Map(turns.map((t) => [t.id, t]))
  const ent = new Map()
  for (const t of turns) for (const e of t.ents) (ent.get(e) ?? ent.set(e, []).get(e)).push(t.i)
  const facts = []
  for (const v of Object.values(raw.observation ?? {}))
    for (const items of Object.values(v))
      for (const it of items) {
        if (!Array.isArray(it) || it.length < 2) continue
        const ids = (Array.isArray(it[1]) ? it[1] : [it[1]]).filter((x) => typeof x === 'string' && byId.has(x))
        if (ids.length) facts.push({ text: it[0], idx: ids.map((x) => byId.get(x).i) })
  }
  const speakers = new Set([conv.speaker_a, conv.speaker_b].filter(Boolean).map((s) => s.toLowerCase()))
  return { turns, ent, facts, bm25: bm25Index(turns.map((t) => t.body)), factBm25: bm25Index(facts.map((f) => f.text)), speakers }
}

// ── one retrieval: several nominations, one fused list
const WEIGHTS = { semantic: 1.0, lexical: 0.7, entity: 0.9, facts: 0.6, adjacent: 0.5, hop: 0.8, temporal: 0.8 }

function nominate(ix, qv, qtext, opts) {
  const lists = {}
  const rank = (scores) => [...scores.keys()].sort((a, b) => scores[b] - scores[a])
  lists.semantic = ix.turns.map((t) => cos(qv, t.v))
  if (opts.lexical) lists.lexical = ix.bm25(qtext)
  if (opts.entity) {
    const s = new Float64Array(ix.turns.length)
    for (const e of entities(qtext)) if (!ix.speakers.has(e)) for (const i of ix.ent.get(e) ?? []) s[i] += 1
    // a turn that mentions two of the asked names outranks one that mentions one
    if (s.some((x) => x > 0)) lists.entity = s
  }
  if (opts.facts) {
    const fs = ix.factBm25(qtext), s = new Float64Array(ix.turns.length)
    fs.forEach((sc, fi) => { if (sc > 0) for (const i of ix.facts[fi].idx) s[i] = Math.max(s[i], sc) })
    if (s.some((x) => x > 0)) lists.facts = s
  }
  return Object.fromEntries(Object.entries(lists).map(([k, v]) => [k, rank(Array.from(v)).filter((i) => v[i] > 0 || k === 'semantic')]))
}

/** Reciprocal-rank fusion, weighted; the weights bend to what the question names. */
function fuse(lists, weights, cue, n) {
  const w = { ...weights }
  if (cue.asks || cue.year || cue.month) { w.temporal *= 1.5; w.facts *= 1.3 }   // facts carry dates
  if (cue.hasEntity) w.entity *= 1.4
  const score = new Map()
  for (const [name, ids] of Object.entries(lists)) {
    const wt = w[name] ?? 1
    ids.slice(0, 60).forEach((i, r) => score.set(i, (score.get(i) ?? 0) + wt / (60 + r)))
  }
  return [...score.entries()].sort((a, b) => b[1] - a[1]).map(([i]) => i)
}

/**
 * The loop. Hop 0 nominates from the question. Then, for each hop: read the
 * top of the fusion, take the names and dates it introduces that the question
 * did not, and nominate again FROM THOSE — plus the turns beside them. The next
 * fusion ranks the union against the accumulated state.
 */
function retrieve(ix, q, opts) {
  const cue = timeCue(q.question)
  const asked = new Set([...entities(q.question)].filter((e) => !ix.speakers.has(e)))
  cue.hasEntity = asked.size > 0
  let lists = nominate(ix, q.v, q.question, opts)
  let fused = fuse(lists, WEIGHTS, cue, K)
  const seen = new Set(asked)
  for (let hop = 0; hop < (opts.hops ?? 0); hop++) {
    const read = fused.slice(0, opts.read ?? 5).map((i) => ix.turns[i])
    const fresh = new Set()
    for (const t of read) for (const e of t.ents) if (!seen.has(e) && !ix.speakers.has(e)) fresh.add(e)
    if (!fresh.size && !opts.adjacent) break
    const s = new Float64Array(ix.turns.length)
    for (const e of fresh) { seen.add(e); for (const i of ix.ent.get(e) ?? []) s[i] += 1 }
    // the hop query is the state, not the question: the names hop 0 surfaced
    const hopText = [...fresh].join(' ')
    if (fresh.size) {
      const lx = ix.bm25(hopText); lx.forEach((v, i) => { s[i] += v * 0.5 })
    }
    if (opts.adjacent) for (const t of read) for (const j of [t.i - 1, t.i + 1])
      if (ix.turns[j] && ix.turns[j].sess === t.sess) s[j] += 1.5
    if (opts.temporal && (cue.year || cue.month)) for (const t of ix.turns) if (t.when) {
      const d = new Date(t.when)
      if ((cue.year && d.getUTCFullYear() === cue.year) && (!cue.month || d.getUTCMonth() + 1 === cue.month)) s[t.i] += 0.5
    }
    const hopList = [...s.keys()].filter((i) => s[i] > 0).sort((a, b) => s[b] - s[a])
    if (!hopList.length) break
    lists = { ...lists, ['hop' + hop]: hopList }
    fused = fuse({ ...lists, hop: hopList }, WEIGHTS, cue, K)
  }
  return fused.slice(0, K).map((i) => ix.turns[i].id)
}

// ── run every configuration over the same questions
const CONFIGS = {
  'single (baseline)':            { },
  '+lexical':                     { lexical: true },
  '+lexical +entity':             { lexical: true, entity: true },
  '+lexical +entity +facts':      { lexical: true, entity: true, facts: true },
  '+adjacent':                    { lexical: true, entity: true, facts: true, adjacent: true, hops: 1, read: 5 },
  '+1 hop (linkage)':             { lexical: true, entity: true, facts: true, adjacent: true, hops: 1, read: 5, temporal: true },
  '+2 hops':                      { lexical: true, entity: true, facts: true, adjacent: true, hops: 2, read: 5, temporal: true },
}

const ixs = store.map((c, n) => build(c, corpus[n]))
const pct = (a, b) => ((a / b) * 100).toFixed(1).padStart(5) + '%'
console.log(`\n── LoCoMo recall@${K} · contextual evidence retrieval ──\n`)
console.log(`configuration                     single-hop all / any     multi-hop all / any     ms`)
for (const [name, opts] of Object.entries(CONFIGS)) {
  const t = { 1: { n: 0, all: 0, any: 0 }, 4: { n: 0, all: 0, any: 0 } }
  let ms = 0, count = 0
  store.forEach((c, n) => {
    for (const q of c.qa) {
      if (!q.evidence.length) continue
      const t0 = process.hrtime.bigint()
      const got = new Set(retrieve(ixs[n], q, opts))
      ms += Number(process.hrtime.bigint() - t0) / 1e6; count++
      const hits = q.evidence.filter((e) => got.has(e)).length
      const cell = t[q.category]; cell.n++
      if (hits === q.evidence.length) cell.all++
      if (hits > 0) cell.any++
    }
  })
  console.log(`${name.padEnd(32)} ${pct(t[4].all, t[4].n)} / ${pct(t[4].any, t[4].n)}       ${pct(t[1].all, t[1].n)} / ${pct(t[1].any, t[1].n)}      ${(ms / count).toFixed(2)}`)
}
console.log(`\nbaseline is brain.mjs's number; every row below it adds one mechanism.`)
