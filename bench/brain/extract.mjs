/**
 * The write side of memory: one LLM call per LoCoMo session turns the raw turns
 * into atomic facts, canonical entities, events and cue anchors, checkpointed
 * per session under data/extract/ so a run resumes where it stopped. The raw
 * turn stays the value the reader gets; everything here is an index onto it.
 *
 *   node extract.mjs                 # extract every session not yet done
 *   node extract.mjs --embed         # canonicalise, embed, write facts-ours-vectors.json
 *
 * Env: MODEL (default: a local Ollama chat model if one is pulled, else
 * deepseek-v4-flash on api.hanzo.ai), WORKERS (6), CONV (only this conversation).
 */
import { readFileSync, writeFileSync, existsSync, mkdirSync, readdirSync } from 'node:fs'
import { assemble } from './entities.mjs'

const corpus = JSON.parse(readFileSync('locomo10.json', 'utf8'))
const SYSTEM = readFileSync('prompts/extract.txt', 'utf8')
const OLLAMA = process.env.OLLAMA ?? 'http://127.0.0.1:11434'
const API = process.env.HANZO_API ?? 'https://api.hanzo.ai/v1'
const home = process.env.HOME
const read = (f) => { try { return JSON.parse(readFileSync(`${home}/.hanzo/${f}`, 'utf8')) } catch { return {} } }
const key = process.env.HANZO_API_KEY ?? read('credentials.json').access_token ?? read('config.json').apiKey
const WORKERS = Number(process.env.WORKERS ?? 6)
const LOCAL = ['gemma4:31b', 'gemma4', 'qwen3.6:35b-a3b', 'qwen3:30b-a3b']
const REMOTE = ['deepseek-v4-flash', 'glm-5.3-flash', 'gpt-oss-120b']
mkdirSync('data/extract', { recursive: true })

const MONTHS = { january:1,february:2,march:3,april:4,may:5,june:6,july:7,august:8,september:9,october:10,november:11,december:12 }
/** "1:56 pm on 8 May, 2023" -> "2023-05-08" */
export const isoDate = (s) => { const m = s?.match(/(\d{1,2}) (\w+),? (\d{4})/); if (!m || !MONTHS[m[2].toLowerCase()]) return null
  return `${m[3]}-${String(MONTHS[m[2].toLowerCase()]).padStart(2, '0')}-${String(m[1]).padStart(2, '0')}` }

export function sessions(conv) {
  const out = []
  for (const k of Object.keys(conv)) if (/^session_\d+$/.test(k)) out.push({ key: k, date: conv[`${k}_date_time`], iso: isoDate(conv[`${k}_date_time`]), turns: conv[k].filter((t) => t?.dia_id && t?.text) })
  return out
}
const render = (t) => `${t.dia_id} ${t.speaker}: ${t.text}${t.blip_caption ? ` [shares a photo: ${t.blip_caption}]` : ''}`

async function localModel() {
  try { const r = await fetch(`${OLLAMA}/api/tags`); const { models } = await r.json(); const have = new Set(models.map((m) => m.name.replace(/:latest$/, '')))
    return LOCAL.find((m) => have.has(m)) ?? null } catch { return null }
}
async function chat(model, user, maxTokens) {
  const local = !model.includes('/') && !REMOTE.includes(model)
  const ctl = new AbortController(); const tm = setTimeout(() => ctl.abort(), 900000)
  try {
    if (local) {
      // streamed, because a reply that takes more than five minutes to begin trips
      // fetch's header timeout, and a 40-turn session on a busy GPU does
      const r = await fetch(`${OLLAMA}/api/chat`, { method: 'POST', headers: { 'content-type': 'application/json' }, signal: ctl.signal,
        body: JSON.stringify({ model, stream: true, format: 'json', options: { temperature: 0, num_predict: maxTokens, num_ctx: 32768 }, messages: [{ role: 'system', content: SYSTEM }, { role: 'user', content: user }] }) })
      if (!r.ok) throw new Error(`${r.status} ${(await r.text()).slice(0, 120)}`)
      let text = '', usage = {}, buf = ''
      for await (const chunk of r.body) { buf += Buffer.from(chunk).toString('utf8'); let nl
        while ((nl = buf.indexOf('\n')) >= 0) { const line = buf.slice(0, nl); buf = buf.slice(nl + 1); if (!line.trim()) continue
          const j = JSON.parse(line); text += j.message?.content ?? ''; if (j.done) usage = { prompt_tokens: j.prompt_eval_count, completion_tokens: j.eval_count } } }
      return { text, usage }
    }
    const r = await fetch(`${API}/chat/completions`, { method: 'POST', headers: { authorization: `Bearer ${key}`, 'content-type': 'application/json' }, signal: ctl.signal,
      body: JSON.stringify({ model, temperature: 0, max_tokens: maxTokens, response_format: { type: 'json_object' }, messages: [{ role: 'system', content: SYSTEM }, { role: 'user', content: user }] }) })
    const text = await r.text(); let j; try { j = JSON.parse(text) } catch { throw new Error(`${r.status} non-json ${text.slice(0, 80)}`) }
    if (!r.ok) throw new Error(`${r.status} ${JSON.stringify(j.error ?? j).slice(0, 120)}`)
    const content = j.choices?.[0]?.message?.content; if (typeof content !== 'string' || !content.trim()) throw new Error('no content')
    return { text: content, usage: j.usage ?? {} }
  } finally { clearTimeout(tm) }
}
/** The model's JSON, tolerating fences and prose around it. */
function parseJSON(text) {
  const s = text.indexOf('{'), e = text.lastIndexOf('}')
  if (s < 0 || e < 0) throw new Error('no object')
  const body = text.slice(s, e + 1)
  try { return JSON.parse(body) } catch (err) {
    // trailing commas, and a reply cut off mid-way: close what is open and keep what parsed
    const noTrail = body.replace(/,\s*([}\]])/g, '$1')
    try { return JSON.parse(noTrail) } catch {}
    let cut = noTrail; for (let i = 0; i < 40; i++) { const j = Math.max(cut.lastIndexOf('},'), cut.lastIndexOf('],'), cut.lastIndexOf('"'))
      if (j <= 0) break; cut = cut.slice(0, j)
      const opens = []; let inStr = false
      for (let k = 0; k < cut.length; k++) { const ch = cut[k]; if (inStr) { if (ch === '\\') k++; else if (ch === '"') inStr = false; continue } if (ch === '"') inStr = true; else if (ch === '{' || ch === '[') opens.push(ch); else if (ch === '}' || ch === ']') opens.pop() }
      if (inStr) continue
      const closed = cut.replace(/,\s*$/, '') + opens.reverse().map((o) => (o === '{' ? '}' : ']')).join('')
      try { const v = JSON.parse(closed.replace(/,\s*([}\]])/g, '$1')); v._repaired = true; return v } catch {} }
    throw err }
}
function check(out, ids) {
  const idSet = new Set(ids); const ents = new Set((out.entities ?? []).map((e) => e.id))
  const facts = (out.facts ?? []).filter((f) => f && typeof f.text === 'string').map((f) => ({ ...f, turns: (Array.isArray(f.turns) ? f.turns : [f.turns]).filter((t) => idSet.has(t)) })).filter((f) => f.turns.length)
  const bad = facts.filter((f) => f.subject && !ents.has(f.subject)).length
  const anchors = {}; for (const [k, v] of Object.entries(out.anchors ?? {})) if (idSet.has(k) && Array.isArray(v)) anchors[k] = v.filter((x) => typeof x === 'string').slice(0, 3)
  const events = (out.events ?? []).map((ev) => ({ ...ev, turns: (ev.turns ?? []).filter((t) => idSet.has(t)) }))
  if (!facts.length) throw new Error('no facts')
  return { entities: out.entities ?? [], facts, events, anchors, dropped: (out.facts ?? []).length - facts.length, badSubject: bad }
}

/** One call for a slice of a session's turns; the whole session when it is short. */
async function extractSlice(ci, s, turns, model) {
  const conv = corpus[ci].conversation
  const user = `Speakers: ${conv.speaker_a} and ${conv.speaker_b}\nSession ${s.key.split('_')[1]} of conversation ${ci + 1}, dated ${s.iso ?? s.date} (${s.date})\n\nTurns:\n${turns.map(render).join('\n')}`
  const ids = turns.map((t) => t.dia_id)
  const models = [model, ...REMOTE.filter((m) => m !== model)]
  let last
  for (const m of models) for (let attempt = 0; attempt < 2; attempt++) {
    const t0 = Date.now()
    try {
      const { text, usage } = await chat(m, user, 16000)
      let parsed; try { parsed = parseJSON(text) } catch (e) { e.raw = text; throw e }
      const out = check(parsed, ids); if (parsed._repaired) out.repaired = true
      return { model: m, ms: Date.now() - t0, usage, ...out }
    } catch (e) { last = e; process.stderr.write(`\n  ${ci}/${s.key} ${m} try ${attempt + 1} ${((Date.now() - t0) / 1000).toFixed(0)}s: ${e.message.slice(0, 100)}\n`); if (e.raw) writeFileSync(`data/extract/raw-${ci}-${s.key}-${m.replace(/[^a-z0-9]/gi, '_')}.txt`, e.raw); await new Promise((z) => setTimeout(z, 3000)) }
  }
  throw last
}
/**
 * A session, in slices of CHUNK turns (16): a 47-turn session answered whole
 * runs past the edge's 100 s and comes back as a 524, and a slice is what a
 * worker can hold anyway. Slices are merged into one checkpoint; entity ids are
 * prefixed per slice and reconciled later by entities.mjs.
 */
const CHUNK = Number(process.env.CHUNK ?? 16)
async function extractOne(ci, s, model) {
  const slices = []; for (let i = 0; i < s.turns.length; i += CHUNK) slices.push(s.turns.slice(i, i + CHUNK))
  if (slices.length > 1 && slices.at(-1).length < 6) { const tail = slices.pop(); slices.at(-1).push(...tail) }
  const parts = await Promise.all(slices.map((turns) => extractSlice(ci, s, turns, model)))
  const out = { conv: ci, session: s.key, date: s.date, observed_at: s.iso, model: parts.map((p) => p.model).join('+'), ms: Math.max(...parts.map((p) => p.ms)), usage: { prompt_tokens: parts.reduce((a, p) => a + (p.usage?.prompt_tokens ?? 0), 0), completion_tokens: parts.reduce((a, p) => a + (p.usage?.completion_tokens ?? 0), 0) }, turns: s.turns.length, slices: parts.length, entities: [], facts: [], events: [], anchors: {}, dropped: 0, badSubject: 0, repaired: parts.some((p) => p.repaired) }
  parts.forEach((p, k) => {
    const re = (id) => (id ? `${k}.${id}` : null)
    const base = out.facts.length
    out.entities.push(...p.entities.map((e) => ({ ...e, id: re(e.id) })))
    out.facts.push(...p.facts.map((f) => ({ ...f, subject: re(f.subject), object: re(f.object) })))
    out.events.push(...p.events.map((ev) => ({ ...ev, facts: (ev.facts ?? []).map((fi) => fi + base) })))
    Object.assign(out.anchors, p.anchors); out.dropped += p.dropped; out.badSubject += p.badSubject
  })
  return out
}

async function pool(items, fn) {
  const out = new Array(items.length); let i = 0
  await Promise.all(Array.from({ length: Math.min(WORKERS, items.length) }, async () => { while (i < items.length) { const j = i++; out[j] = await fn(items[j], j) } }))
  return out
}

const MAIN = process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())
if (MAIN && !process.argv.includes('--embed')) {
  const model = process.env.MODEL ?? (await localModel()) ?? REMOTE[0]
  const jobs = []
  corpus.forEach((c, ci) => { if (process.env.CONV && Number(process.env.CONV) !== ci) return
    for (const s of sessions(c.conversation)) { const f = `data/extract/${ci}-${s.key}.json`; if (!existsSync(f)) jobs.push({ ci, s, f }) } })
  if (process.env.LIMIT) jobs.splice(Number(process.env.LIMIT))
  console.log(`${jobs.length} sessions to extract with ${model} (${WORKERS} workers)`)
  let done = 0, failed = 0; const t0 = Date.now(); let tokens = 0
  await pool(jobs, async ({ ci, s, f }) => {
    try { const out = await extractOne(ci, s, model); writeFileSync(f, JSON.stringify(out)); tokens += (out.usage?.prompt_tokens ?? 0) + (out.usage?.completion_tokens ?? 0); done++
      process.stdout.write(`\r  ${done}/${jobs.length} sessions · ${failed} failed · ${((Date.now() - t0) / 60000).toFixed(1)} min · ${tokens} tokens   `) }
    catch (e) { failed++; process.stderr.write(`\n  FAILED ${ci}/${s.key}: ${e.message.slice(0, 120)}\n`) }
  })
  console.log(`\ndone: ${done} extracted, ${failed} failed, ${((Date.now() - t0) / 60000).toFixed(1)} min, ${tokens} tokens`)
}

if (MAIN && process.argv.includes('--embed')) {
  const MODEL = process.env.EMBED_MODEL ?? 'zenlm/zen-embedding-0.6b'
  async function embed(texts, attempt = 0) {
    try { const r = await fetch(`${OLLAMA}/api/embed`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ model: MODEL, input: texts }) })
      if (!r.ok) throw new Error(`embed ${r.status}`); return (await r.json()).embeddings }
    catch (e) { if (attempt >= 4) throw e; await new Promise((z) => setTimeout(z, 10000 * (attempt + 1))); return embed(texts, attempt + 1) } }
  async function embedAll(texts) { const out = []; for (let i = 0; i < texts.length; i += 16) out.push(...(await embed(texts.slice(i, i + 16)))); return out }
  const graph = assemble(corpus)
  const out = []
  for (const [ci, g] of graph.entries()) {
    const rows = [...g.facts.map((f) => ({ kind: 'fact', text: f.text, ids: f.turns, sess: f.sess, subject: f.subject, relation: f.relation, object: f.object, literal: f.literal, event_time: f.event_time, valid_from: f.valid_from, valid_to: f.valid_to, observed_at: f.observed_at, confidence: f.confidence, event: f.event, supersedes: f.supersedes })),
      ...g.anchors.map((a) => ({ kind: 'anchor', text: a.text, ids: [a.turn], sess: a.sess }))]
    const vs = await embedAll(rows.map((r) => r.text))
    rows.forEach((r, i) => (r.v = vs[i]))
    out.push(rows); process.stdout.write(`\r  embedded conversation ${ci + 1}/10: ${g.facts.length} facts, ${g.anchors.length} anchors, ${g.entities.length} entities   `)
  }
  writeFileSync('facts-ours-vectors.json', JSON.stringify(out))
  writeFileSync('data/extract/graph.json', JSON.stringify(graph.map((g) => ({ entities: g.entities, events: g.events, facts: g.facts.length, anchors: g.anchors.length }))))
  console.log(`\nfacts-ours-vectors.json: ${out.reduce((a, r) => a + r.length, 0)} rows`)
}
