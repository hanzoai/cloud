/**
 * Query understanding, once per question and independent of any haystack: the
 * question becomes a base entity and a chain of typed relations. The plan is
 * what every later hop is retrieved WITH — hop two searches for
 * (resolved entity, next relation), never the original sentence.
 *
 *   node compile.mjs [--model=deepseek-v4-flash] [--workers=3]
 * Caches to data/mab/plans.json keyed by question text; rerun to fill gaps.
 */
import { readFileSync, writeFileSync, existsSync, renameSync } from 'node:fs'
import { load, DATA, TEMPLATES, parse, norm } from './parse.mjs'
import { byLexicon } from './plan.mjs'
const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const MODEL = arg('model', 'deepseek-v4-flash'), WORKERS = Number(arg('workers', 3)), BATCH = Number(arg('batch', 1))
const LOCAL = MODEL.startsWith('gemma') || MODEL.startsWith('qwen')
const API = LOCAL ? 'http://127.0.0.1:11434/v1' : (process.env.HANZO_API ?? 'https://api.hanzo.ai/v1')
const home = process.env.HOME, read = (f) => { try { return JSON.parse(readFileSync(`${home}/.hanzo/${f}`, 'utf8')) } catch { return {} } }
const key = LOCAL ? 'ollama' : (process.env.HANZO_API_KEY ?? read('credentials.json').access_token)
const PROMPT = readFileSync(new URL('../prompts/compile-mab.txt', import.meta.url), 'utf8')
const RELS = new Set(TEMPLATES.map((t) => t[0]))

// The single-hop questions the benchmark writes in one of a few fixed shapes need no model:
// an anchored template names the relation and captures the entity. Everything else is compiled.
const E = '(.+?)'
const RULES = [
  ['capital', `^What is the capital of ${E}\\?$`], ['head_of_gov', `^What is the name of the current head of the ${E} government\\?$`],
  ['continent', `^Which continent is ${E} located in\\?$`], ['citizenship', `^What is the country of citizenship of ${E}\\?$`],
  ['language', `^What language does ${E} speak\\?$`], ['official_lang', `^What is the official language of ${E}\\?$`],
  ['head_of_state', `^What is the name of the current head of state in ${E}\\?$`], ['author', `^Who is the author of ${E}\\?$`],
  ['country_origin', `^Which country was ${E} created in\\?$`], ['performer', `^Who performed ${E}\\?$`], ['spouse', `^Who is ${E} married to\\?$`],
  ['sport', `^Which sport is ${E} associated with\\?$`], ['position', `^What position does ${E} play\\?$`], ['religion', `^Which religion is ${E} affiliated with\\?$`],
  ['genre', `^What type of music does ${E} play\\?$`], ['child', `^Who is ${E}'s child\\?$`], ['notable_work', `^What is ${E} famous for\\?$`],
  ['educated_at', `^Which university was ${E} educated at\\?$`], ['founder', `^Who founded ${E}\\?$`], ['founder', `^Who was the founder of ${E}\\?$`],
  ['chairperson', `^Who is the chairperson of ${E}\\?$`], ['director', `^Who is the director of ${E}\\?$`], ['ceo', `^Who is the chief executive officer of ${E}\\?$`], ['ceo', `^Who is the CEO of ${E}\\?$`],
  ['headquarters', `^(?:Where|In which city) is the headquarters of ${E}(?: located)?\\?$`], ['death_place', `^(?:Where|In which city) did ${E} die\\?$`], ['birth_place', `^(?:Where|In which city) was ${E} born\\?$`],
  ['employer', `^Who is ${E} employed by\\?$`], ['employer', `^Who employs ${E}\\?$`], ['producer', `^Which company produced ${E}\\?$`], ['broadcaster', `^Who was the original broadcaster of ${E}\\?$`],
  ['original_lang', `^(?:In which language was ${E} written|What is the original language of ${E})\\?$`], ['founding_place', `^In which city was ${E} founded\\?$`],
  ['occupation', `^What field does ${E} work in\\?$`], ['head_coach', `^Who is the head coach of ${E}\\?$`], ['creator', `^Who created ${E}\\?$`], ['developer', `^Who developed ${E}\\?$`],
  ['work_location', `^(?:Where|In which city) did ${E} work\\?$`],
  ['office', `^Who is the ${E}\\?$`],
].map(([rel, re]) => [rel, new RegExp(re)])
export function byRule(q) { for (const [relation, re] of RULES) { const m = q.match(re); if (m) { const entity = m.slice(1).find(Boolean).trim(); if (!known(entity)) return null; return { entity, chain: [relation], model: 'rules' } } } return null }
let KNOWN = null
function known(name) { if (!KNOWN) { KNOWN = new Set(); for (const r of load()) for (const f of parse(r.context).facts) { if (f.subject) KNOWN.add(norm(f.subject)); if (f.object) KNOWN.add(norm(f.object)) } } return KNOWN.has(norm(name.replace(/['’]s\b/g, ''))) || KNOWN.has(norm(name)) }

const OUT = DATA + 'plans.json'
let plans = existsSync(OUT) ? JSON.parse(readFileSync(OUT, 'utf8')) : {}
const fromDisk = () => { try { return existsSync(OUT) ? JSON.parse(readFileSync(OUT, 'utf8')) : {} } catch { return {} } }
const save = () => { plans = { ...fromDisk(), ...plans }; const tmp = OUT + '.' + process.pid; writeFileSync(tmp, JSON.stringify(plans, null, 1)); renameSync(tmp, OUT) }

/** Several questions in one call: a JSON array of plans, in order. Each plan is validated on its own. */
async function compileMany(qs, attempt = 0) {
  const ctl = new AbortController(); const timer = setTimeout(() => ctl.abort(), 240000)
  const user = qs.map((q, i) => `Q${i + 1}: ${q}`).join('\n') + `\n\nCompile every question above. Reply with a JSON array of ${qs.length} plan objects, in the same order, and nothing else.`
  const messages = [{ role: 'system', content: PROMPT }, { role: 'user', content: user }]
  const r = LOCAL
    ? await fetch('http://127.0.0.1:11434/api/chat', { method: 'POST', signal: ctl.signal, headers: { 'content-type': 'application/json' }, body: JSON.stringify({ model: MODEL, stream: false, think: false, options: { temperature: 0, num_predict: 160 * qs.length }, messages }) })
    : await fetch(`${API}/chat/completions`, { method: 'POST', signal: ctl.signal, headers: { authorization: `Bearer ${key}`, 'content-type': 'application/json' }, body: JSON.stringify({ model: MODEL, temperature: 0, max_tokens: 160 * qs.length, messages }) })
  const text = await r.text().finally(() => clearTimeout(timer)); let j = null; try { j = JSON.parse(text) } catch {}
  const content = LOCAL ? j?.message?.content : j?.choices?.[0]?.message?.content
  if (!r.ok || typeof content !== 'string') { if (attempt < 3) { await new Promise((z) => setTimeout(z, 3000 * (attempt + 1))); return compileMany(qs, attempt + 1) } throw new Error(`${r.status} ${text.slice(0, 100)}`) }
  const m = content.match(/\[[\s\S]*\]/); if (!m) throw new Error(`no json array: ${content.slice(0, 80)}`)
  let arr; try { arr = JSON.parse(m[0]) } catch { throw new Error(`unparseable array: ${m[0].slice(0, 80)}`) }
  if (!Array.isArray(arr) || arr.length !== qs.length) throw new Error(`array of ${arr?.length} for ${qs.length} questions`)
  return arr.map((plan) => (typeof plan?.entity === 'string' && Array.isArray(plan.chain) && plan.chain.length && plan.chain.every((c) => RELS.has(c))) ? { entity: plan.entity.trim(), chain: plan.chain, model: MODEL } : null)
}
async function compile(q, attempt = 0) {
  const ctl = new AbortController(); const timer = setTimeout(() => ctl.abort(), 150000)
  const messages = [{ role: 'system', content: PROMPT }, { role: 'user', content: `Q: ${q}` }]
  // Locally, Ollama's own endpoint: thinking off (the OpenAI-compatible one spends the budget on hidden
  // thinking and returns nothing) and JSON mode, so the reply is the object and nothing else.
  const r = LOCAL
    ? await fetch('http://127.0.0.1:11434/api/chat', { method: 'POST', signal: ctl.signal, headers: { 'content-type': 'application/json' }, body: JSON.stringify({ model: MODEL, stream: false, think: false, format: 'json', options: { temperature: 0, num_predict: 120 }, messages }) })
    : await fetch(`${API}/chat/completions`, { method: 'POST', signal: ctl.signal, headers: { authorization: `Bearer ${key}`, 'content-type': 'application/json' }, body: JSON.stringify({ model: MODEL, temperature: 0, max_tokens: 120, messages }) })
  const text = await r.text().finally(() => clearTimeout(timer)); let j = null; try { j = JSON.parse(text) } catch {}
  const content = LOCAL ? j?.message?.content : j?.choices?.[0]?.message?.content
  if (!r.ok || typeof content !== 'string') { if (attempt < 4) { await new Promise((z) => setTimeout(z, 3000 * (attempt + 1))); return compile(q, attempt + 1) } throw new Error(`${r.status} ${text.slice(0, 100)}`) }
  const m = content.match(/\{[\s\S]*\}/); if (!m) throw new Error(`no json: ${content.slice(0, 80)}`)
  const plan = JSON.parse(m[0])
  if (typeof plan.entity !== 'string' || !Array.isArray(plan.chain) || !plan.chain.length || !plan.chain.every((c) => RELS.has(c))) throw new Error(`bad plan ${m[0].slice(0, 100)}`)
  return { entity: plan.entity.trim(), chain: plan.chain, model: MODEL }
}
const rows = load(); const every = [...new Set(rows.flatMap((r) => r.questions))]
// Rules first: the benchmark's single-hop templates are exact, so a rule that matches is right.
// Everything else is compiled by the model; the lexicon only fills in where the model failed.
// (The lexicon used to override the model. It fitted the 6k phrasings and not the test ones:
// chains in surface order, spurious hops from repeated words — 126 of 209 multi-hop misses at 32k+.)
let ruled = 0, lexed = 0, kept = 0
for (const q of every) { const p = byRule(q); if (p) { plans[q] = p; ruled++ } else if (plans[q] && plans[q].model !== 'rules' && plans[q].model !== 'lexicon') kept++; else delete plans[q] }
{ const tmp = OUT + '.' + process.pid; writeFileSync(tmp, JSON.stringify(plans, null, 1)); renameSync(tmp, OUT) }
console.log(`${ruled} questions compiled by rule, ${lexed} by lexicon, ${kept} by a model where neither reads them`)
if (process.argv.includes('--rules-only')) process.exit(0)
let qs = every.filter((q) => !plans[q]); if (process.argv.includes('--reverse')) qs = qs.reverse()
console.log(`${qs.length} questions to compile with ${MODEL}, ${WORKERS} workers (${Object.keys(plans).length} cached)`)
let i = 0, done = 0, failed = 0
await Promise.all(Array.from({ length: WORKERS }, async () => { while (i < qs.length) {
  plans = { ...fromDisk(), ...plans }
  const batch = []; while (batch.length < BATCH && i < qs.length) { const q = qs[i++]; if (!plans[q]) batch.push(q) }
  if (!batch.length) continue
  try {
    if (batch.length === 1) { plans[batch[0]] = await compile(batch[0]); done++ }
    else { const out = await compileMany(batch); out.forEach((plan, k) => { if (plan) { plans[batch[k]] = plan; done++ } else failed++ }) }
  } catch (e) { failed += batch.length; process.stderr.write(`\n${batch[0].slice(0, 60)} (+${batch.length - 1}): ${e.message}\n`) }
  save(); process.stdout.write(`\r  ${done} compiled, ${failed} failed   `) } }))
for (const q of every) if (!plans[q]) { const p = byLexicon(q); if (p) { plans[q] = p; lexed++ } }
save(); console.log(`\ndone: ${done} compiled by ${MODEL}, ${failed} failed, ${lexed} filled by the lexicon, ${ruled} by rule, ${Object.keys(plans).length} total`)
