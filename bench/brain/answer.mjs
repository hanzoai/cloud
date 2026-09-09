/**
 * Answer accuracy on LoCoMo — the number the published figures actually are.
 *
 * brain.mjs and cer2.mjs measure retrieval: did the gold turns land in the top
 * k. CAR's 78 / 30.2 and Naive's 91 / 57 are a different quantity — a model
 * reads a retrieved context and is scored on what it says. This is that
 * measurement, over the same questions, with the retrieval policy switchable so
 * the gain from linkage shows up where it is supposed to: in answers.
 *
 * Scoring is the LoCoMo paper's own: token-level F1 against the gold answer,
 * plus exact match after normalisation. No LLM judge — a judge is a second
 * model with its own opinions, and the point of this file is to be checkable.
 *
 * Every row reports what it cost to get the answer: turns delivered, tokens
 * delivered (approximate, chars/4), and the reader model, because "92%" with
 * the whole conversation in the prompt is a different result from "92%" with
 * twenty turns.
 *
 *   READER=openai/gpt-4o-mini node answer.mjs [--policy=cer|single] [--n=282] [--cat=1]
 *
 * The credential is HANZO_API_KEY, else the token `hanzo auth login` saved.
 */
import { readFileSync, existsSync, writeFileSync } from 'node:fs'
import { execSync } from 'node:child_process'

const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const POLICY = arg('policy', 'cer'), CAT = Number(arg('cat', 1)), N = Number(arg('n', 0)), K = Number(arg('k', 20))
const READER = process.env.READER ?? 'openai/gpt-4o-mini'
const API = process.env.HANZO_API ?? 'https://api.hanzo.ai/v1'
// A credential, in the order a person would reach for one: the environment,
// then the token `hanzo auth login` left behind, then a minted key.
const home = process.env.HOME
const read = (f) => { try { return JSON.parse(readFileSync(`${home}/.hanzo/${f}`, 'utf8')) } catch { return {} } }
const key = process.env.HANZO_API_KEY ?? read('credentials.json').access_token ?? read('config.json').apiKey
if (!key) { console.error('no credential: run `hanzo auth login` or set HANZO_API_KEY'); process.exit(1) }

// Retrieval comes from cer2.mjs; it is run as a module by asking it for one
// configuration's ranked ids per question. Kept as a subprocess on purpose —
// the retriever stays a script anyone can run, and this file adds a reader.
const corpus = JSON.parse(readFileSync('locomo10.json', 'utf8'))
const store = JSON.parse(readFileSync('brain-vectors.json', 'utf8'))
const ranked = JSON.parse(execSync(`node rank.mjs --policy=${POLICY} --k=${K}`, { maxBuffer: 1 << 28 }).toString())

// ── scoring, as the paper does it
const norm = (s) => String(s).toLowerCase().replace(/\b(a|an|the)\b/g, ' ').replace(/[^\w\s]/g, ' ').split(/\s+/).filter(Boolean)
function f1(pred, gold) {
  const p = norm(pred), g = norm(gold); if (!p.length || !g.length) return Number(p.join(' ') === g.join(' '))
  const gm = new Map(); for (const w of g) gm.set(w, (gm.get(w) ?? 0) + 1)
  let same = 0; for (const w of p) if (gm.get(w) > 0) { same++; gm.set(w, gm.get(w) - 1) }
  if (!same) return 0
  const pr = same / p.length, rc = same / g.length; return (2 * pr * rc) / (pr + rc)
}
const em = (pred, gold) => norm(pred).join(' ') === norm(gold).join(' ')

const WORKERS = Number(process.env.WORKERS ?? 6)
async function pool(items, fn) {
  const out = new Array(items.length); let i = 0
  await Promise.all(Array.from({ length: Math.min(WORKERS, items.length) }, async () => {
    while (i < items.length) { const j = i++; out[j] = await fn(items[j], j) }
  }))
  return out
}
async function ask(context, question, attempt = 0) {
  const sys = 'You answer questions about a conversation between two people using only the excerpts given. Reply with the shortest possible answer: a name, a date, a place, a number, or a noun phrase of a few words. No sentence, no explanation, nothing the question did not ask for. Infer from the excerpts when the answer is implied rather than stated; answer "unknown" only if nothing in them bears on the question.'
  const user = `Excerpts (each is "turn id [date] speaker: text"):\n\n${context}\n\nQuestion: ${question}\nAnswer:`
  const r = await fetch(`${API}/chat/completions`, { method: 'POST', headers: { authorization: `Bearer ${key}`, 'content-type': 'application/json' },
    body: JSON.stringify({ model: READER, temperature: 0, max_tokens: 40, messages: [{ role: 'system', content: sys }, { role: 'user', content: user }] }) })
  if (!r.ok) {
    if (attempt < 2 && (r.status === 429 || r.status >= 500)) { await new Promise((z) => setTimeout(z, 1500 * (attempt + 1))); return ask(context, question, attempt + 1) }
    throw new Error(`${r.status} ${(await r.text()).slice(0, 160)}`)
  }
  const j = await r.json()
  const content = j.choices?.[0]?.message?.content
  if (typeof content !== 'string') throw new Error(`no content: ${JSON.stringify(j).slice(0, 120)}`)
  return content.trim()
}

const dates = corpus.map((c) => Object.fromEntries(Object.entries(c.conversation).filter(([k]) => k.endsWith('_date_time')).map(([k, v]) => [k.replace('_date_time', ''), v])))
const qs = []
store.forEach((c, ci) => c.qa.forEach((q, qi) => { if (q.category === CAT && q.evidence.length) qs.push({ ci, qi, q }) }))
const OUT = `answers-${POLICY}-cat${CAT}.json`
const kept = process.argv.includes('--only-missing') && existsSync(OUT) ? JSON.parse(readFileSync(OUT, 'utf8')) : []
const have = new Set(kept.map((r) => r.q))
const todo = (N ? qs.slice(0, N) : qs).filter(({ q }) => !have.has(q.question))
if (kept.length) console.log(`keeping ${kept.length} answered; asking ${todo.length} again`)
console.log(`${todo.length} questions · category ${CAT} · policy ${POLICY} · k=${K} · reader ${READER} · ${WORKERS} workers\n`)
let done = 0, failed = 0
const rows = kept.concat((await pool(todo, async ({ ci, qi, q }) => {
  const gold = corpus[ci].qa.find((x) => x.question === q.question)?.answer ?? ''
  const byId = new Map(store[ci].turns.map((t) => [t.id, t]))
  const context = ranked[ci][qi].map((id) => { const t = byId.get(id); const sess = 'session_' + id.match(/^D(\d+):/)[1]; return `${id} [${dates[ci][sess] ?? ''}] ${t.text}` }).join('\n')
  const tokens = Math.round(context.length / 4)
  let pred
  try { pred = await ask(context, q.question) } catch (e) { failed++; process.stderr.write(`\n${q.question.slice(0, 60)}: ${e.message}\n`); return null }
  done++; if (done % 10 === 0) process.stdout.write(`\r  ${done}/${todo.length}   `)
  return { q: q.question, gold, pred, f1: f1(pred, gold), em: em(pred, gold), tokens }
})).filter(Boolean))
const sumF1 = rows.reduce((a, r) => a + r.f1, 0), sumEM = rows.reduce((a, r) => a + r.em, 0), sumTok = rows.reduce((a, r) => a + r.tokens, 0)
console.log(`\n\n── LoCoMo answers · cat ${CAT} · ${POLICY}@${K} · ${READER} ──`)
console.log(`questions ${rows.length} (${failed} failed)   F1 ${(sumF1 / rows.length * 100).toFixed(1)}%   exact ${(sumEM / rows.length * 100).toFixed(1)}%   tokens delivered/q ${(sumTok / rows.length).toFixed(0)}`)
writeFileSync(OUT, JSON.stringify(rows, null, 1))
