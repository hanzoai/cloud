/**
 * One repair call, on deterministic failure only.
 *
 * When the search's best path for a question carries a low-confidence
 * signature — a relation no plan proposed, a permuted chain, a bridge or a
 * skip, a low score, or no path at all — the compiler is asked once more,
 * with what the executor knows: the base entity it found, the relations that
 * entity actually has in the store, the relations the store has for the
 * entity the path ended on, the answer type the question names, and the
 * path that was taken. It returns a plan, nothing else; the executor runs it
 * like any other candidate and keeps it only if it scores higher. No model
 * reads the facts or writes the answer.
 */
import { readFileSync } from 'node:fs'
import { norm } from './parse.mjs'
const API = process.env.REPAIR_API ?? 'http://127.0.0.1:8080/v1', MODEL = process.env.REPAIR_MODEL ?? 'mlx-community/Qwen3.8-27B-4bit'
const PROMPT = readFileSync(new URL('../prompts/compile-mab-family.txt', import.meta.url), 'utf8')
const RELS = new Set(PROMPT.split('\n').map((l) => l.match(/^(\w+):\s+"/)).filter(Boolean).map((m) => m[1]).concat(['origin', 'language', 'maker', 'leader', 'place', 'work']))
const cache = new Map()
export async function repair(ix, question, best, find) {
  const key = question + '|' + JSON.stringify(best?.plan?.chain ?? null); if (cache.has(key)) return cache.get(key)
  const base = best?.plan?.entity ? find(best.plan.entity) : null
  const relsOf = (e) => [...new Set((ix.byEntity.get(e) ?? []).filter((f) => norm(f.subject) === e).map((f) => f.relation))]
  const hops = (best?.trace ?? []).filter((s) => s.step === 'hop')
  const failure = [
    `The plan tried: entity "${best?.plan?.entity ?? '?'}", chain ${JSON.stringify(best?.plan?.chain ?? [])}.`,
    base ? `The store knows that entity as "${base.key}" with these relations: ${relsOf(base.key).join(', ') || 'none'}.` : 'The store does not know that entity.',
    hops.length ? `The path taken: ${hops.map((h) => `${h.entity} -${h.relation}-> ${h.object}`).join(' ; ')}.` : 'No path completed.',
    hops.length ? `Relations the store has for the last entity reached (${norm(hops[hops.length - 1].object)}): ${relsOf(norm(hops[hops.length - 1].object)).join(', ') || 'none'}.` : '',
    `Give a corrected plan for the question, using only the relation names and families of the list. Reply with the JSON object only.`,
  ].filter(Boolean).join('\n')
  const messages = [{ role: 'system', content: PROMPT }, { role: 'user', content: `Q: ${question}\n\n${failure}` }]
  const ctl = new AbortController(); const tm = setTimeout(() => ctl.abort(), 120000)
  try {
    const r = await fetch(`${API}/chat/completions`, { method: 'POST', signal: ctl.signal, headers: { 'content-type': 'application/json' }, body: JSON.stringify({ model: MODEL, temperature: 0, max_tokens: 160, messages, chat_template_kwargs: { enable_thinking: false } }) })
    const j = await r.json(); const content = j?.choices?.[0]?.message?.content ?? ''; const m = content.match(/\{[\s\S]*\}/)
    const plan = m ? JSON.parse(m[0]) : null
    const ok = plan && typeof plan.entity === 'string' && Array.isArray(plan.chain) && plan.chain.length && plan.chain.every((c) => RELS.has(c))
    const out = ok ? { entity: plan.entity.trim(), chain: plan.chain, model: MODEL, set: 'repair' } : null
    cache.set(key, out); return out
  } catch { cache.set(key, null); return null } finally { clearTimeout(tm) }
}
/** The signature that triggers a repair: what the executor had to do to complete, or that it could not. */
export function suspicious(best) {
  if (!best) return true
  const chain = best.plan?.chain ?? [], hops = (best.trace ?? []).filter((s) => s.step === 'hop')
  if (String(best.plan?.set ?? '').endsWith('~order')) return true
  if (hops.length !== chain.length) return true
  for (const h of hops) if (h.planned == null || (h.relation !== h.planned && !(FAM[h.planned] ?? []).includes(h.relation))) return true
  return (best.score ?? 99) < 6
}
const FAM = { language: ['official_lang', 'original_lang', 'language'], origin: ['country_origin', 'citizenship', 'founding_place', 'headquarters'], maker: ['founder', 'creator', 'developer', 'producer', 'author', 'performer'], leader: ['head_of_state', 'head_of_gov', 'chairperson', 'ceo', 'director', 'head_coach', 'office'], place: ['birth_place', 'death_place', 'work_location', 'headquarters', 'founding_place', 'capital'], work: ['notable_work', 'position', 'occupation', 'sport', 'genre'] }
