/**
 * Agent Brain — recall on LoCoMo, measured.
 *
 * Naïve publishes 91% single-hop and 57% multi-hop against a field of named
 * systems. Those columns are LoCoMo: ten long conversations, 1,986 questions,
 * each carrying the `dia_id` of the turns that answer it. So the claim is
 * checkable, and this checks ours the same way.
 *
 * Categories are the paper's own (Maharana et al.): 4 is single-hop retrieval,
 * 1 is multi-hop reasoning. Those are the two columns.
 *
 * WHAT IS MEASURED: retrieval. Given a question, does the store hand back the
 * turns that contain the answer? That is what a memory system is for, and it is
 * what separates the systems in their table — an LLM reading the right turns
 * answers; one reading the wrong turns cannot.
 *
 *   recall@k (all)  every gold turn is in the top k   — the honest bar for
 *                                                       multi-hop, where you
 *                                                       need several facts
 *   recall@k (any)  at least one gold turn is in top k
 *
 * Both are reported because a table with one number and no definition is not
 * comparable to anything.
 */
import { readFileSync, writeFileSync, existsSync } from 'node:fs'

const DATA = process.argv[2] ?? 'locomo10.json'
const CACHE = 'brain-vectors.json'
const MODEL = process.env.EMBED_MODEL ?? 'zenlm/zen-embedding-0.6b'
const OLLAMA = process.env.OLLAMA ?? 'http://localhost:11434'
const KS = [5, 10, 20]

const corpus = JSON.parse(readFileSync(DATA, 'utf8'))

/** Every turn in a conversation, flattened, keyed by the id the QA cites. */
function turns(conv) {
  const out = []
  for (const key of Object.keys(conv)) {
    if (!/^session_\d+$/.test(key)) continue
    for (const t of conv[key]) {
      if (!t?.dia_id || !t?.text) continue
      out.push({ id: t.dia_id, text: `${t.speaker}: ${t.text}` })
    }
  }
  return out
}

async function embed(texts) {
  const res = await fetch(`${OLLAMA}/api/embed`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ model: MODEL, input: texts }),
  })
  if (!res.ok) throw new Error(`embed ${res.status}: ${(await res.text()).slice(0, 120)}`)
  const { embeddings } = await res.json()
  return embeddings
}

/** Batched, because one call per turn is 8x slower for the same work. */
async function embedAll(texts, label) {
  const BATCH = 32
  const out = []
  const started = Date.now()
  for (let i = 0; i < texts.length; i += BATCH) {
    out.push(...(await embed(texts.slice(i, i + BATCH))))
    if (i % (BATCH * 8) === 0 || i + BATCH >= texts.length) {
      const done = Math.min(i + BATCH, texts.length)
      const rate = done / ((Date.now() - started) / 1000)
      process.stdout.write(`\r  ${label}: ${done}/${texts.length} (${rate.toFixed(0)}/s)   `)
    }
  }
  process.stdout.write('\n')
  return out
}

const dot = (a, b) => {
  let s = 0
  for (let i = 0; i < a.length; i++) s += a[i] * b[i]
  return s
}
const norm = (v) => Math.sqrt(dot(v, v))
const cosine = (a, b) => dot(a, b) / (norm(a) * norm(b))

// ── Embed the corpus and the questions, once.
let store
if (existsSync(CACHE)) {
  console.log('using cached vectors')
  store = JSON.parse(readFileSync(CACHE, 'utf8'))
} else {
  store = []
  for (const [n, conv] of corpus.entries()) {
    const rows = turns(conv.conversation)
    const asked = conv.qa.filter((q) => q.category === 1 || q.category === 4)
    console.log(`conversation ${n + 1}/${corpus.length}: ${rows.length} turns, ${asked.length} questions`)
    const tv = await embedAll(rows.map((r) => r.text), 'turns')
    const qv = await embedAll(asked.map((q) => q.question), 'questions')
    store.push({
      turns: rows.map((r, i) => ({ ...r, v: tv[i] })),
      qa: asked.map((q, i) => ({
        question: q.question,
        category: q.category,
        evidence: Array.isArray(q.evidence) ? q.evidence : [q.evidence].filter(Boolean),
        v: qv[i],
      })),
    })
  }
  writeFileSync(CACHE, JSON.stringify(store))
}

// ── Score.
const tally = {
  1: { n: 0, all: {}, any: {} },
  4: { n: 0, all: {}, any: {} },
}
for (const k of KS) for (const c of [1, 4]) (tally[c].all[k] = 0), (tally[c].any[k] = 0)

const latencies = []
for (const conv of store) {
  for (const q of conv.qa) {
    if (!q.evidence.length) continue
    const t0 = process.hrtime.bigint()
    const ranked = conv.turns
      .map((t) => ({ id: t.id, s: cosine(q.v, t.v) }))
      .sort((a, b) => b.s - a.s)
    latencies.push(Number(process.hrtime.bigint() - t0) / 1e6)

    const cell = tally[q.category]
    cell.n++
    for (const k of KS) {
      const got = new Set(ranked.slice(0, k).map((r) => r.id))
      const hits = q.evidence.filter((e) => got.has(e)).length
      if (hits === q.evidence.length) cell.all[k]++
      if (hits > 0) cell.any[k]++
    }
  }
}

const pct = (a, b) => `${((a / b) * 100).toFixed(1)}%`
console.log(`\n── LoCoMo recall · ${MODEL} ──\n`)
console.log(`                    single-hop (cat 4)   multi-hop (cat 1)`)
console.log(`questions           ${String(tally[4].n).padEnd(20)} ${tally[1].n}`)
for (const k of KS) {
  console.log(`recall@${String(k).padEnd(2)} all        ${pct(tally[4].all[k], tally[4].n).padEnd(20)} ${pct(tally[1].all[k], tally[1].n)}`)
}
for (const k of KS) {
  console.log(`recall@${String(k).padEnd(2)} any        ${pct(tally[4].any[k], tally[4].n).padEnd(20)} ${pct(tally[1].any[k], tally[1].n)}`)
}
const avg = latencies.reduce((a, b) => a + b, 0) / latencies.length
console.log(`\nsearch over a whole conversation: ${avg.toFixed(2)} ms (brute-force cosine, no index)`)
console.log(`\nNaïve publish 91% single-hop, 57% multi-hop. Their k and their`)
console.log(`all-vs-any are not stated, so compare like for like before quoting.`)
