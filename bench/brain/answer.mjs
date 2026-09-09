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
 *   HANZO_API_KEY=… READER=openai/gpt-4o-mini node answer.mjs [--policy=cer|single] [--n=282] [--cat=1]
 *
 * The key is read from the environment, or from ~/.hanzo/config.json's apiKey.
 */
import { readFileSync, existsSync, writeFileSync } from 'node:fs'
import { execSync } from 'node:child_process'

const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const POLICY = arg('policy', 'cer'), CAT = Number(arg('cat', 1)), N = Number(arg('n', 0)), K = Number(arg('k', 20))
const READER = process.env.READER ?? 'openai/gpt-4o-mini'
const API = process.env.HANZO_API ?? 'https://api.hanzo.ai/v1'
const key = process.env.HANZO_API_KEY ?? JSON.parse(readFileSync(process.env.HOME + '/.hanzo/config.json', 'utf8')).apiKey
if (!key) { console.error('no key: set HANZO_API_KEY'); process.exit(1) }

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

async function ask(context, question) {
  const sys = 'You answer questions about a conversation between two people using only the excerpts given. Answer in as few words as possible: a name, a date, a place, a short phrase. If the excerpts do not say, answer "unknown".'
  const user = `Excerpts (each is "turn id [date] speaker: text"):\n\n${context}\n\nQuestion: ${question}\nAnswer:`
  const r = await fetch(`${API}/chat/completions`, { method: 'POST', headers: { authorization: `Bearer ${key}`, 'content-type': 'application/json' },
    body: JSON.stringify({ model: READER, temperature: 0, max_tokens: 40, messages: [{ role: 'system', content: sys }, { role: 'user', content: user }] }) })
  if (!r.ok) throw new Error(`${r.status} ${(await r.text()).slice(0, 160)}`)
  return (await r.json()).choices[0].message.content.trim()
}

const dates = corpus.map((c) => Object.fromEntries(Object.entries(c.conversation).filter(([k]) => k.endsWith('_date_time')).map(([k, v]) => [k.replace('_date_time', ''), v])))
const rows = []; let done = 0, sumF1 = 0, sumEM = 0, sumTok = 0
const qs = []
store.forEach((c, ci) => c.qa.forEach((q, qi) => { if (q.category === CAT && q.evidence.length) qs.push({ ci, qi, q }) }))
const todo = N ? qs.slice(0, N) : qs
console.log(`${todo.length} questions · category ${CAT} · policy ${POLICY} · k=${K} · reader ${READER}\n`)
for (const { ci, qi, q } of todo) {
  const gold = corpus[ci].qa.find((x) => x.question === q.question)?.answer ?? ''
  const ids = ranked[ci][qi]
  const byId = new Map(store[ci].turns.map((t) => [t.id, t]))
  const context = ids.map((id) => { const t = byId.get(id); const sess = 'session_' + id.match(/^D(\d+):/)[1]; return `${id} [${dates[ci][sess] ?? ''}] ${t.text}` }).join('\n')
  const tok = Math.round(context.length / 4)
  let pred = ''
  try { pred = await ask(context, q.question) } catch (e) { console.error(`\n${q.question}: ${e.message}`); continue }
  const s = f1(pred, gold), x = em(pred, gold)
  rows.push({ q: q.question, gold, pred, f1: s, em: x, tokens: tok }); done++; sumF1 += s; sumEM += x; sumTok += tok
  if (done % 10 === 0) process.stdout.write(`\r  ${done}/${todo.length}  F1 ${(sumF1 / done * 100).toFixed(1)}  EM ${(sumEM / done * 100).toFixed(1)}  tokens/q ${(sumTok / done).toFixed(0)}   `)
}
console.log(`\n\n── LoCoMo answers · cat ${CAT} · ${POLICY}@${K} · ${READER} ──`)
console.log(`questions ${done}   F1 ${(sumF1 / done * 100).toFixed(1)}%   exact ${(sumEM / done * 100).toFixed(1)}%   tokens delivered/q ${(sumTok / done).toFixed(0)}`)
writeFileSync(`answers-${POLICY}-cat${CAT}.json`, JSON.stringify(rows, null, 1))
