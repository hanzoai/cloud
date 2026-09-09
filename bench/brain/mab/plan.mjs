/**
 * Query understanding without a model: the question names one base entity and
 * a chain of relations in phrases drawn from a small lexicon. The chain is the
 * relation phrases in reverse order of appearance — the phrase nearest the
 * entity is applied first — and the base entity is the longest name from the
 * fact store that the question contains. A question the lexicon cannot read is
 * left for compile.mjs (a model); nothing is guessed.
 */
import { load, parse, norm } from './parse.mjs'

// relation ← phrases, longest and most specific first. Every alternative is anchored on word boundaries.
export const LEXICON = [
  ['official_lang', /\bofficial (?:language|documents)\b/i],
  ['original_lang', /\b(?:original language|language (?:of|in which) the (?:work|book|film|series|show)|written in)\b/i],
  ['language',      /\b(?:languages?|speak|spoken|sign(?:ed)?)\b/i],
  ['head_of_state', /\b(?:head of state|chief of state)\b/i],
  ['head_of_gov',   /\b(?:head of (?:the )?(?:\S+ )?government|chief executive of the country|prime minister of the country|leads the government)\b/i],
  ['country_origin',/\b(?:(?:was|were) created in|created in the country|invented in)\b/i],
  ['origin',        /\b(?:of origin|origin|originat\w*|come[s]? from|came from|hail(?:ed|s)? from|birthplace of the (?=sport)|came into existence|first developed|developed(?! by)|first practi[cs]ed)\b/i],
  ['religion',      /\b(?:religion|faith)\b[^.?]{0,30}?\b(?:belongs?|follows?|adheres?|practi[cs]es?)\b/i],
  ['citizenship',   /\b(?:citizenship|citizen|nationality|belongs?|national of)\b/i],
  ['continent',     /\bcontinent\b/i],
  ['capital',       /\bcapital\b/i],
  ['death_place',   /\b(?:pass(?:ed)? away|died|death|breathe(?:d)? (?:their|his|her) last|last breath|die|passing)\b/i],
  ['birth_place',   /\b(?:birthplace|born|place of birth|birth)\b/i],
  ['educated_at',   /\b(?:educat|educational institution|alma mater|universit|school|studied|received education)\w*/i],
  ['headquarters',  /\b(?:headquarter|based)\w*/i],
  ['founding_place',/\b(?:founded in|established in|city where .{0,30}?was founded)\b/i],
  ['founder',       /\b(?:found(?:ed|er|ing)|establish(?:ed|er|ing|ment)|set up)\b/i],
  ['ceo',           /\b(?:chief executive officer|ceo|chief executive)\b/i],
  ['director',      /\b(?:director|manager)\b/i],
  ['chairperson',   /\b(?:chairperson|chairman|chairwoman|chair)\b/i],
  ['head_coach',    /\b(?:head coach|coach)\b/i],
  ['performer',     /\b(?:perform\w*|artist|singer|band behind|musician)\b/i],
  ['author',        /\b(?:author|wrote|writer|written by)\b/i],
  ['creator',       /\b(?:creat\w*|inventor|invented)\b/i],
  ['developer',     /\b(?:developer|develop(?:ed|s)? by)\b/i],
  ['producer',      /\b(?:produc\w*|manufactur\w*|maker|made by|makes)\b/i],
  ['broadcaster',   /\b(?:broadcast\w*|aired|network that)\b/i],
  ['employer',      /\b(?:employ\w*|works? for|worked for|organization (?:where|that) .{0,30}?(?:employed|part of|member)|company (?:where|that) .{0,30}?(?:work|employ)|part of|member of)\b/i],
  ['occupation',    /\b(?:occupation|profession|job title|job|field|works in|line of work|career)\b/i],
  ['genre',         /\b(?:genre|type of music|kind of music|style of music|music .{0,10}?play)\b/i],
  ['child',         /\b(?:child|son|daughter|offspring|kid)\b/i],
  ['spouse',        /\b(?:spouse|partner|married|wife|husband|marital)\b/i],
  ['religion',      /\b(?:religio\w*|faith|belie\w*)\b/i],
  ['sport',         /\bsport\w*\b/i],
  ['position',      /\bposition\b(?! of (?:the )?(?:chairperson|chairman|chairwoman|chair|director|manager|ceo|chief|head|president|coach|leader))/i],
  ['work_location', /\b(?:work location|location of work|place of work|where .{0,30}?work|workplace|spend .{0,20}?work hours|worked in)\b/i],
  ['notable_work',  /\b(?:famous for|known for|notable|significant creation|creation associated|best known|renowned)\b/i],
  ['office',        /\b(?:officeholder|holder of|office)\b/i],
]
// Words that name a relation but describe the answer's TYPE, not a hop: "Which city is the capital ..." — the city is the capital.
const TYPE_WORDS = /^(?:in |to |from |at |on |by )?(?:what|which|who|where|when)(?:'s| is| was| are| were| did| does| do| kind of| type of)?\s+(?:city|town|country|place|location|person|individual|name of the|name|continent|language|sport|university|organization|company|faith|religion)?\b/i

let ENTITY_NAMES = null
function entityNames() {
  if (ENTITY_NAMES) return ENTITY_NAMES
  const set = new Set()
  for (const r of load()) for (const f of parse(r.context).facts) { if (f.subject) set.add(norm(f.subject)); if (f.object) set.add(norm(f.object)) }
  ENTITY_NAMES = [...set].filter((n) => n.length > 1).sort((a, b) => b.length - a.length)
  return ENTITY_NAMES
}
const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

/** The longest known entity name inside the question, and where it sits. */
export function baseEntity(q) {
  const nq = norm(q.replace(/['’]s\b/g, ''))
  for (const n of entityNames()) { const re = new RegExp(`(?:^|[^\\p{L}\\p{N}])${esc(n)}(?=$|[^\\p{L}\\p{N}])`, 'u'); const m = nq.match(re); if (m) return { name: n, at: m.index + (m[0].length - n.length) } }
  return null
}

export function byLexicon(q) {
  const ent = baseEntity(q); if (!ent) return null
  const nq = norm(q.replace(/['’]s\b/g, '')); const rest = nq.slice(0, ent.at) + '_' + nq.slice(ent.at + ent.name.length); const entAt = ent.at
  const hits = []
  for (const [rel, re] of LEXICON) { const g = new RegExp(re.source, re.flags.includes('g') ? re.flags : re.flags + 'g'); let m; while ((m = g.exec(rest))) hits.push({ rel, at: m.index, len: m[0].length }) }
  // one relation per span: the earliest-listed lexicon entry wins an overlap
  hits.sort((a, b) => a.at - b.at || b.len - a.len)
  const chosen = []; for (const h of hits) if (!chosen.some((c) => h.at < c.at + c.len && c.at < h.at + h.len) && !/^\s*(?:who|that|which) (?:is|was) the\b/.test(rest.slice(h.at + h.len, h.at + h.len + 14))) chosen.push(h)
  // Phrases before the entity run outer → inner, so they reverse. A relative marker ("the country
  // where …", "the person who …") splits them: what precedes the marker wraps the clause, what follows
  // it — and the noun the marker hangs on ("the sport that E …") — belongs to the entity's own clause.
  // A verb after the entity applies to that clause. When an auxiliary sits right before the entity
  // ("did E originate") the entity is the subject of the whole question: the verb applies first and
  // every phrase before wraps it.
  const pre = rest.slice(0, entAt); const markers = [...pre.matchAll(/\b(?:where|that|which|who|whose|whom)\b/g)]
  const cut = markers.length ? markers[markers.length - 1].index : -1
  const auxBefore = /\b(?:did|does|do|is|was|are|were|has|have|had)(?: the| a| an)?\s*$/.test(pre.slice(-14))
  const beforeHits = chosen.filter((h) => h.at < entAt), after = chosen.filter((h) => h.at > entAt).map((h) => h.rel)
  let order
  if (auxBefore && cut < 0) order = [...after, ...beforeHits.map((h) => h.rel).reverse()]
  else {
    const hangs = (h) => cut >= 0 && h.at + h.len <= cut && cut - (h.at + h.len) <= 3
    const inner = beforeHits.filter((h) => h.at >= cut || hangs(h)).map((h) => h.rel), outer = beforeHits.filter((h) => h.at < cut && !hangs(h)).map((h) => h.rel)
    order = [...inner.reverse(), ...after, ...outer.reverse()]
  }
  // a leading type word that another phrase of the same family restates ("Which language … the official language of")
  if (order.length > 1 && chosen[0] && chosen[0].at < 12) { const fam = { language: 1, official_lang: 1, original_lang: 1 }; const first = chosen[0].rel
    if (fam[first] && chosen.some((h, i) => i > 0 && fam[h.rel])) order = order.filter((r, i) => !(r === first && i === order.lastIndexOf(first))) }
  const chain = []; for (const r of order) if (!chain.includes(r)) chain.push(r)
  if (!chain.length) return null
  return { entity: ent.name, chain, model: 'lexicon' }
}

if (process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())) {
  const { readFileSync } = await import('node:fs'); const { DATA } = await import('./parse.mjs')
  const plans = JSON.parse(readFileSync(DATA + 'plans.json', 'utf8'))
  let agree = 0, differ = 0, none = 0; const shown = { differ: 0, none: 0 }
  for (const [q, p] of Object.entries(plans)) { if (p.model === 'rules') continue; const l = byLexicon(q)
    if (!l) { none++; if (shown.none++ < 5) console.log('  none  ', q.slice(0, 80), '| ref', p.entity, p.chain); continue }
    if (norm(l.entity) === norm(p.entity) && l.chain.join() === p.chain.join()) agree++
    else { differ++; if (shown.differ++ < 14) console.log('  differ', q.slice(0, 80), '| lex', l.entity, l.chain, '| ref', p.entity, p.chain, `(${p.model})`) } }
  console.log(`\nagainst the model-made plans: agree ${agree}, differ ${differ}, unreadable ${none}`)
  const rows = load(); const every = [...new Set(rows.flatMap((r) => r.questions))].filter((q) => !plans[q]); let ok = 0
  for (const q of every) if (byLexicon(q)) ok++; console.log(`of the ${every.length} questions without a plan, the lexicon reads ${ok}`)
}
