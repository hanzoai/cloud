/**
 * One question, one reader, one answer — the worker run.mjs calls.
 *
 * Scoring is the LoCoMo paper's own: token-level F1 against the gold answer,
 * plus exact match after normalisation. No LLM judge — a judge is a second
 * model with its own opinions, and the point of this file is to be checkable.
 *
 * The reader is any OpenAI-compatible chat endpoint: the Hanzo router for the
 * house and matched lanes, local Ollama for the open lane. The credential is
 * HANZO_API_KEY, else the token `hanzo auth login` saved; Ollama needs none.
 *
 *   node answer.mjs --reader=enso-flash "Excerpt lines..." "Question?"
 */
import { readFileSync } from 'node:fs'

// ── scoring, as the paper does it
export const norm = (s) => String(s ?? '').toLowerCase().replace(/\b(a|an|the)\b/g, ' ').replace(/[^\w\s]/g, ' ').split(/\s+/).filter(Boolean)
export function f1(pred, gold) {
  const p = norm(pred), g = norm(gold)
  if (!p.length || !g.length) return Number(p.join(' ') === g.join(' '))
  const gm = new Map(); for (const w of g) gm.set(w, (gm.get(w) ?? 0) + 1)
  let same = 0; for (const w of p) if (gm.get(w) > 0) { same++; gm.set(w, gm.get(w) - 1) }
  if (!same) return 0
  const pr = same / p.length, rc = same / g.length
  return (2 * pr * rc) / (pr + rc)
}
export const em = (pred, gold) => norm(pred).join(' ') === norm(gold).join(' ')

// ── where a reader lives
export const ROUTER = 'https://api.hanzo.ai/v1'
export const OLLAMA = 'http://127.0.0.1:11434/v1'
/** A model name with a tag (`gemma4:31b`) is a local Ollama model; the rest go to the router. */
export const apiFor = (reader) => (reader.includes(':') && !reader.includes('/') ? OLLAMA : ROUTER)
export function credential(api) {
  if (api.startsWith(OLLAMA)) return 'ollama'
  const home = process.env.HOME
  const read = (f) => { try { return JSON.parse(readFileSync(`${home}/.hanzo/${f}`, 'utf8')) } catch { return {} } }
  const key = process.env.HANZO_API_KEY ?? read('credentials.json').access_token ?? read('config.json').apiKey
  if (!key) throw new Error('no credential: run `hanzo auth login` or set HANZO_API_KEY')
  return key
}

/** The excerpt block the reader sees: one line per retrieved turn, in the order given. */
export function contextOf(turnsById, dates, ids) {
  return ids.map((id) => { const t = turnsById.get(id); const sess = 'session_' + id.match(/^D(\d+):/)[1]; return `${id} [${dates[sess] ?? ''}] ${t.text}` }).join('\n')
}

/**
 * One call. Throws with `.status` (HTTP code, or 0 for a dropped connection) and `.kind`.
 *
 * A local model goes through Ollama's own chat route rather than its OpenAI
 * facade: Gemma 4 thinks by default, and through the facade a 64-token budget
 * is spent on thought and the content comes back empty. The native route takes
 * `think: false`, so the open lane answers the way the router lanes do.
 */
export async function ask({ api, key, reader, system, context, question, maxTokens = 64, timeoutMs = 300000 }) {
  const user = `Excerpts (each is "turn id [date] speaker: text"):\n\n${context}\n\nQuestion: ${question}\nAnswer:`
  const messages = [{ role: 'system', content: system }, { role: 'user', content: user }]
  const local = api.startsWith(OLLAMA)
  const url = local ? `${OLLAMA.replace(/\/v1$/, '')}/api/chat` : `${api}/chat/completions`
  const body = local
    // num_ctx: a prompt here is under 2k tokens; left unset, Ollama reserves the
    // model's whole 262k window and prompt processing crawls under the cache
    ? { model: reader, messages, stream: false, think: false, options: { temperature: 0, num_predict: maxTokens, num_ctx: 8192 } }
    : { model: reader, temperature: 0, max_tokens: maxTokens, messages }
  const ctl = new AbortController(); const timer = setTimeout(() => ctl.abort(), timeoutMs)
  const t0 = Date.now()
  let r
  try {
    r = await fetch(url, { method: 'POST', signal: ctl.signal, headers: { authorization: `Bearer ${key}`, 'content-type': 'application/json' }, body: JSON.stringify(body) })
  } catch (e) { clearTimeout(timer); throw Object.assign(new Error(`network: ${e.name}`), { status: 0, kind: 'network' }) }
  clearTimeout(timer)
  const text = await r.text()
  if (!r.ok) throw Object.assign(new Error(`${r.status} ${text.replace(/\s+/g, ' ').slice(0, 160)}`), { status: r.status, kind: r.status === 429 ? 'quota' : r.status >= 500 ? 'server' : 'client' })
  let j; try { j = JSON.parse(text) } catch { throw Object.assign(new Error(`non-json: ${text.slice(0, 80)}`), { status: r.status, kind: 'server' }) }
  const content = local ? j.message?.content : j.choices?.[0]?.message?.content
  if (typeof content !== 'string' || !content.trim()) throw Object.assign(new Error(`no content: ${JSON.stringify(j).slice(0, 120)}`), { status: r.status, kind: 'empty' })
  const usage = local ? { prompt_tokens: j.prompt_eval_count ?? null, completion_tokens: j.eval_count ?? null, total_tokens: (j.prompt_eval_count ?? 0) + (j.eval_count ?? 0) } : (j.usage ?? null)
  return { pred: content.trim(), usage, ms: Date.now() - t0 }
}

const MAIN = process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())
if (MAIN) {
  const arg = (key, d) => { const m = process.argv.find((a) => a.startsWith(`--${key}=`)); return m ? m.split('=')[1] : d }
  const reader = arg('reader', 'enso-flash'), api = arg('api', apiFor(reader))
  const [context, question] = process.argv.filter((a) => !a.startsWith('--')).slice(2)
  const system = readFileSync(new URL('./prompts/reader-locomo.txt', import.meta.url), 'utf8').trim()
  const out = await ask({ api, key: credential(api), reader, system, context, question })
  console.log(JSON.stringify(out))
}
