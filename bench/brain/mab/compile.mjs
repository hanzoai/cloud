/**
 * Query understanding, once per question and independent of any haystack: the
 * question becomes a base entity and a chain of typed relations. The plan is
 * what every later hop is retrieved WITH — hop two searches for
 * (resolved entity, next relation), never the original sentence.
 *
 *   node compile.mjs [--model=deepseek-v4-flash] [--workers=3]
 * Caches to data/mab/plans.json keyed by question text; rerun to fill gaps.
 */
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { load, DATA, TEMPLATES } from './parse.mjs'
const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const MODEL = arg('model', 'deepseek-v4-flash'), WORKERS = Number(arg('workers', 3))
const LOCAL = MODEL.startsWith('gemma') || MODEL.startsWith('qwen')
const API = LOCAL ? 'http://127.0.0.1:11434/v1' : (process.env.HANZO_API ?? 'https://api.hanzo.ai/v1')
const home = process.env.HOME, read = (f) => { try { return JSON.parse(readFileSync(`${home}/.hanzo/${f}`, 'utf8')) } catch { return {} } }
const key = LOCAL ? 'ollama' : (process.env.HANZO_API_KEY ?? read('credentials.json').access_token)
const PROMPT = readFileSync(new URL('../prompts/compile-mab.txt', import.meta.url), 'utf8')
const RELS = new Set(TEMPLATES.map((t) => t[0]))
const OUT = DATA + 'plans.json'
let plans = existsSync(OUT) ? JSON.parse(readFileSync(OUT, 'utf8')) : {}
const save = () => { const disk = existsSync(OUT) ? JSON.parse(readFileSync(OUT, 'utf8')) : {}; plans = { ...disk, ...plans }; writeFileSync(OUT, JSON.stringify(plans, null, 1)) }

async function compile(q, attempt = 0) {
  const ctl = new AbortController(); const timer = setTimeout(() => ctl.abort(), 90000)
  const r = await fetch(`${API}/chat/completions`, { method: 'POST', signal: ctl.signal, headers: { authorization: `Bearer ${key}`, 'content-type': 'application/json' },
    body: JSON.stringify({ model: MODEL, temperature: 0, max_tokens: 120, messages: [{ role: 'system', content: PROMPT }, { role: 'user', content: `Q: ${q}` }] }) })
  const text = await r.text().finally(() => clearTimeout(timer)); let j = null; try { j = JSON.parse(text) } catch {}
  const content = j?.choices?.[0]?.message?.content
  if (!r.ok || typeof content !== 'string') { if (attempt < 4) { await new Promise((z) => setTimeout(z, 3000 * (attempt + 1))); return compile(q, attempt + 1) } throw new Error(`${r.status} ${text.slice(0, 100)}`) }
  const m = content.match(/\{[\s\S]*\}/); if (!m) throw new Error(`no json: ${content.slice(0, 80)}`)
  const plan = JSON.parse(m[0])
  if (typeof plan.entity !== 'string' || !Array.isArray(plan.chain) || !plan.chain.length || !plan.chain.every((c) => RELS.has(c))) throw new Error(`bad plan ${m[0].slice(0, 100)}`)
  return { entity: plan.entity.trim(), chain: plan.chain, model: MODEL }
}
const rows = load(); let qs = [...new Set(rows.flatMap((r) => r.questions))].filter((q) => !plans[q]); if (process.argv.includes('--reverse')) qs = qs.reverse()
console.log(`${qs.length} questions to compile with ${MODEL}, ${WORKERS} workers (${Object.keys(plans).length} cached)`)
let i = 0, done = 0, failed = 0
await Promise.all(Array.from({ length: WORKERS }, async () => { while (i < qs.length) { const q = qs[i++]; if (plans[q]) continue; if (i % 5 === 0) { const disk = existsSync(OUT) ? JSON.parse(readFileSync(OUT, 'utf8')) : {}; plans = { ...disk, ...plans }; if (plans[q]) continue }
  try { plans[q] = await compile(q); done++ } catch (e) { failed++; process.stderr.write(`\n${q.slice(0, 60)}: ${e.message}\n`) }
  save(); process.stdout.write(`\r  ${done} compiled, ${failed} failed   `) } }))
save(); console.log(`\ndone: ${done} compiled, ${failed} failed, ${Object.keys(plans).length} total`)
