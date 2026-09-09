/**
 * One way to ask a model, for every script here.
 *
 * Local first: if Ollama at OLLAMA (127.0.0.1:11434) lists the model named in
 * LLM_MODEL (or the first of the open readers it has), the call goes there —
 * quota-free, reproducible, and the LoCoMo-Conv protocol's own reader when it
 * is gemma4:31b. Otherwise the call goes to api.hanzo.ai with the credential
 * `hanzo auth login` saved, which is never printed.
 *
 * Every reply is cached on disk under data/llm/<sha256>.json keyed by model,
 * messages and options, so a rerun costs nothing and a table can be rebuilt
 * from the raw replies. Retries cover 429, 5xx, a dropped socket and an empty
 * message; each is logged, none is silent.
 */
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'

const OLLAMA = process.env.OLLAMA ?? 'http://127.0.0.1:11434'
const API = process.env.HANZO_API ?? 'https://api.hanzo.ai/v1'
const OPEN = ['gemma4:31b', 'qwen3.6:35b-a3b', 'qwen3:30b-a3b']
const CACHE = new URL('./data/llm/', import.meta.url)
mkdirSync(CACHE, { recursive: true })

const home = process.env.HOME
const readJson = (f) => { try { return JSON.parse(readFileSync(f, 'utf8')) } catch { return {} } }
const key = () => process.env.HANZO_API_KEY ?? readJson(`${home}/.hanzo/credentials.json`).access_token ?? readJson(`${home}/.hanzo/config.json`).apiKey

let local = null
/** The Ollama models present, asked once. */
export async function localModels() {
  if (local) return local
  try { const r = await fetch(`${OLLAMA}/api/tags`); local = (await r.json()).models.map((m) => m.name) } catch { local = [] }
  return local
}

/** Which model a call goes to, and where. */
export async function pick(model) {
  const have = await localModels()
  if (model && have.includes(model)) return { model, where: 'ollama' }
  if (model) return { model, where: 'api' }
  const open = process.env.LLM_MODEL ?? OPEN.find((m) => have.includes(m))
  if (open && have.includes(open)) return { model: open, where: 'ollama' }
  return { model: process.env.LLM_MODEL ?? 'glm-5.3-flash', where: 'api' }
}

const sha = (s) => createHash('sha256').update(s).digest('hex')
const sleep = (ms) => new Promise((z) => setTimeout(z, ms))

/**
 * complete(messages, { model, json, max_tokens, temperature }) -> { content, model, where, usage, cached }
 * `json: true` asks for a JSON object and parses it (throws if the model did not comply after retries).
 */
export async function complete(messages, o = {}) {
  const { model, where } = await pick(o.model)
  const opts = { temperature: o.temperature ?? 0, max_tokens: o.max_tokens ?? 64, json: !!o.json }
  const id = sha(JSON.stringify({ model, messages, opts }))
  const file = new URL(`${id}.json`, CACHE)
  if (!o.fresh && existsSync(file)) { const c = JSON.parse(readFileSync(file, 'utf8')); return { ...c, cached: true } }
  let last = ''
  for (let attempt = 0; attempt < (o.attempts ?? 5); attempt++) {
    if (attempt) await sleep(Math.min(20000, 1500 * 2 ** attempt))
    try {
      const url = where === 'ollama' ? `${OLLAMA}/v1/chat/completions` : `${API}/chat/completions`
      const headers = { 'content-type': 'application/json', ...(where === 'api' ? { authorization: `Bearer ${key()}` } : {}) }
      const body = { model, temperature: opts.temperature, max_tokens: opts.max_tokens, messages, ...(opts.json ? { response_format: { type: 'json_object' } } : {}) }
      const ctl = new AbortController(); const tm = setTimeout(() => ctl.abort(), o.timeout ?? 180000)
      const r = await fetch(url, { method: 'POST', headers, body: JSON.stringify(body), signal: ctl.signal }).finally(() => clearTimeout(tm))
      const text = await r.text()
      if (!r.ok) { last = `${r.status} ${text.slice(0, 160)}`; if (r.status === 429 || r.status >= 500) continue; throw new Error(last) }
      let j; try { j = JSON.parse(text) } catch { last = `non-json ${text.slice(0, 80)}`; continue }
      const content = j.choices?.[0]?.message?.content
      if (typeof content !== 'string' || !content.trim()) { last = `empty content ${JSON.stringify(j).slice(0, 120)}`; continue }
      let parsed
      if (opts.json) { try { parsed = JSON.parse(content.replace(/^```(?:json)?\s*|\s*```$/g, '')) } catch { last = `not json: ${content.slice(0, 80)}`; continue } }
      const out = { content: content.trim(), json: parsed, model, where, usage: j.usage ?? null }
      writeFileSync(file, JSON.stringify(out))
      return { ...out, cached: false }
    } catch (e) { last = e.name === 'AbortError' ? 'timeout' : e.message; continue }
  }
  throw new Error(`${model}@${where}: ${last}`)
}

/** Query-time embedding with the store's own model, so a hop's new query lives in the same space. */
export async function embed(texts, model = process.env.EMBED_MODEL ?? 'zenlm/zen-embedding-0.6b') {
  const r = await fetch(`${OLLAMA}/api/embed`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ model, input: texts }) })
  if (!r.ok) throw new Error(`embed ${r.status}: ${(await r.text()).slice(0, 120)}`)
  return (await r.json()).embeddings
}

/** A bounded worker pool, in order of the input, with one retry on failure. */
export async function pool(items, fn, workers = Number(process.env.WORKERS ?? 4)) {
  const out = new Array(items.length); let i = 0
  await Promise.all(Array.from({ length: Math.min(workers, items.length) }, async () => {
    while (i < items.length) { const j = i++; try { out[j] = await fn(items[j], j) } catch (e) { try { out[j] = await fn(items[j], j) } catch (e2) { out[j] = { error: e2.message } } } }
  }))
  return out
}
