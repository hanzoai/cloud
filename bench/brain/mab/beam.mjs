/**
 * The plan is a sketch; the search is the resolver.
 *
 * The oracle decomposition (oracle.mjs) says a path from the plan's base
 * entity to the gold exists for almost every miss, and that the compiler's
 * chain names a near-synonym of the relation the store used, or stops a hop
 * early, or adds one. So the chain is followed as a sketch: at each hop the
 * planned relation is preferred, its class-mates allowed at a cost, and the
 * latest version taken unless it leads nowhere — an older version is a
 * backtrack, not a choice. The search may stop one hop early or go one hop
 * further when the answer type the question names says so. The best complete
 * path wins by a fixed score; ties go to the more literal plan and the later
 * version. Nothing reads the gold. Nothing calls a model.
 */
import { norm, TEMPLATES } from './parse.mjs'

/** Relations that answer the same kind of question, so a sketch that says one may mean another. */
export const CLASS = {
  language: ['official_lang', 'original_lang', 'language'],
  origin: ['country_origin', 'citizenship', 'founding_place', 'headquarters'],
  maker: ['founder', 'creator', 'developer', 'producer', 'author', 'performer'],
  leader: ['head_of_state', 'head_of_gov', 'chairperson', 'ceo', 'director', 'head_coach', 'office'],
  place: ['birth_place', 'death_place', 'work_location', 'headquarters', 'founding_place', 'capital'],
  work: ['notable_work', 'position', 'occupation', 'sport', 'genre'],
}
const classOf = {}; for (const [c, rels] of Object.entries(CLASS)) for (const r of rels) (classOf[r] ??= []).push(c)
/** The kind of thing each relation's object is. */
export const TYPE = {
  capital: 'city', headquarters: 'city', founding_place: 'city', death_place: 'city', birth_place: 'city', work_location: 'city',
  citizenship: 'country', country_origin: 'country', continent: 'continent',
  official_lang: 'language', original_lang: 'language', language: 'language',
  head_of_gov: 'person', head_of_state: 'person', author: 'person', chairperson: 'person', director: 'person', ceo: 'person', founder: 'person', creator: 'person', performer: 'person', spouse: 'person', child: 'person', head_coach: 'person', office: 'person',
  employer: 'org', producer: 'org', developer: 'org', educated_at: 'org', broadcaster: 'org',
  sport: 'sport', religion: 'religion', genre: 'genre', occupation: 'occupation', position: 'position', notable_work: 'work',
}
/** What the question asks for, from its own words; null when it does not say. */
export function answerType(q) {
  // the last question in a compound question is the one the gold answers
  const parts = q.split(/,\s*and\s+(?=who|what|which|where|in which|on which)/i); const l = parts[parts.length - 1].toLowerCase().trim()
  const lead = l.match(/^(?:in |on |from |at |to )?(?:which|what)\s+(?:is the |was the )?(city|town|country|nation|continent|language|tongue|sport|religion|genre|profession|occupation|field|position|person|individual|political leader|university|school)\b/)
  if (lead) return { city: 'city', town: 'city', country: 'country', nation: 'country', continent: 'continent', language: 'language', tongue: 'language', sport: 'sport', religion: 'religion', genre: 'genre', profession: 'occupation', occupation: 'occupation', field: 'occupation', position: 'position', person: 'person', individual: 'person', 'political leader': 'person', university: 'org', school: 'org' }[lead[1]]
  // "what is the name of the <role>": the role decides
  const named = l.match(/^what (?:is|was) the name of (?:the |an? )?(?:current |present )?(head of state|head of (?:the )?government|chief of state|leader|person|individual|chairperson|chief executive|ceo|director|founder|author|creator|performer|spouse|head coach|city|capital|country|continent|language|sport|religion|genre|university|company|organi[sz]ation|institution)\b/)
  if (named) { const r = named[1]; if (/city|capital/.test(r)) return 'city'; if (/country/.test(r)) return 'country'; if (r === 'continent') return 'continent'; if (r === 'language') return 'language'; if (r === 'sport') return 'sport'; if (r === 'religion') return 'religion'; if (r === 'genre') return 'genre'; if (/university|company|organi|institution/.test(r)) return 'org'; return 'person' }
  if (/^(who|which (person|individual|political leader))/.test(l) || /\bwho (is|was|holds|currently)\b/.test(l)) return 'person'
  if (/\bcontinent\b/.test(l)) return 'continent'
  if (/\b(capital|birthplace|place of (birth|death|work|employment)|head office|headquarter)/.test(l) || /\b(which|what)\s+(city|town)\b/.test(l)) return 'city'
  if (/\bcountry\b|\bnationality\b/.test(l)) return 'country'
  if (/\blanguage|\btongue\b/.test(l)) return 'language'
  if (/\bsport\b/.test(l)) return 'sport'
  if (/\breligio/.test(l)) return 'religion'
  if (/\bgenre\b|\btype of music\b/.test(l)) return 'genre'
  if (/\b(occupation|profession|field)\b/.test(l)) return 'occupation'
  if (/\bposition\b/.test(l)) return 'position'
  if (/\bfamous for\b|\bnotable work|\bcontribution\b/.test(l)) return 'work'
  if (/^(where|in which (place|location))\b/.test(l)) return 'place'
  return null
}
// 'who' may be answered by an organisation (a developer, an employer, a producer), and 'where' by any place
const typeOk = (rel, want) => !want || TYPE[rel] === want || (want === 'place' && ['city', 'country', 'continent'].includes(TYPE[rel])) || (want === 'person' && TYPE[rel] === 'org')

/** Entity lookup: exact after normalisation, else the closest containing name. */
export function finder(ix) {
  const names = [...ix.byEntity.keys()]
  return (name) => { if (!name) return null; const cands = [norm(name), norm(name.replace(/^the\s+/i, '')), norm(name.replace(/^"|"$/g, ''))]
    for (const c of cands) if (ix.byEntity.has(c)) return { key: c, how: 'exact' }
    const q = cands[0]; let best = null
    for (const n of names) if ((n.includes(q) || q.includes(n)) && n.length > 3 && (!best || Math.abs(n.length - q.length) < Math.abs(best.length - q.length))) best = n
    return best ? { key: best, how: 'contains' } : null }
}

const versionsOf = (ix, entity, rel) => {
  if (rel === 'origin') { for (const r of ['citizenship', 'country_origin']) { const v = ix.versions(entity, r); if (v.length) return v.map((f) => ({ ...f, relation: r })) } return [] }
  const v = ix.versions(entity, rel).slice()
  if (!v.length && rel === 'spouse') for (const f of ix.byEntity.get(entity) ?? []) if (f.relation === 'spouse' && norm(f.object) === entity) v.push({ ...f, object: f.subject, subject: f.object })
  return v.sort((a, b) => a.serial - b.serial)
}
/** The words a relation's own template uses, so a question that uses them supports that relation at some hop. */
const STOP = new Set('the of in is was a an s o to by for and name current where that with'.split(' '))
// the sentence each relation is written in, from the compiler prompt's own list (`rel: "S ... O."`)
import { readFileSync } from 'node:fs'
const PROMPT_TEMPLATES = readFileSync(new URL('../prompts/compile-mab.txt', import.meta.url), 'utf8').split('\n').map((l) => l.match(/^(\w+):\s+"([^"]+)"/)).filter(Boolean)
const WORDS = Object.fromEntries(PROMPT_TEMPLATES.map(([, rel, tpl]) => [rel, new Set(tpl.toLowerCase().replace(/\b[so]\b/g, ' ').replace(/[^a-z ]+/g, ' ').split(/\s+/).filter((w) => w.length > 2 && !STOP.has(w)))]))
export function lexical(rel, question) { const ws = WORDS[rel]; if (!ws || !ws.size) return 0; const q = new Set(question.toLowerCase().replace(/[^a-z ]+/g, ' ').split(/\s+/)); let n = 0; for (const w of ws) if (q.has(w) || q.has(w + 's') || q.has(w.replace(/s$/, ''))) n++; return n / ws.size }
const alternatives = (rel) => { if (CLASS[rel] && rel !== 'origin') return [...CLASS[rel]] // a family name: every member, equally
  const s = new Set([rel]); for (const c of classOf[rel] ?? []) for (const r of CLASS[c]) s.add(r); if (rel === 'origin') for (const r of CLASS.origin) s.add(r); return [...s] }
const FAMILY_TYPE = { language: 'language', origin: 'country', maker: 'person', leader: 'person', place: 'city', work: 'work' }

/**
 * beamResolve(ix, find, plan, question) -> { answer, evidence, trace, score, complete }
 * Score per hop: planned relation +3, a class-mate +1; latest version 0, each step back −0.5;
 * a chain that ends on the answer type +2, one that had to add a hop −1, one that stopped early −1.
 */
export function beamResolve(ix, find, plan, question, o = {}) {
  const WIDTH = o.width ?? 24, want = answerType(question)
  const base = find(plan.entity); if (!base) return { answer: null, evidence: [], trace: [{ step: 'entity', entity: plan.entity, found: null }], score: -Infinity, complete: false }
  let beam = [{ entity: base.key, hop: 0, path: [], score: 0, evidence: [] }]; const finals = []
  const consider = (s, extra) => { const last = s.evidence[s.evidence.length - 1]; if (!last) return; const ok = typeOk(last.relation, want); finals.push({ ...s, score: s.score + (ok ? 2 : want ? -2 : 0) + extra, complete: true }) }
  for (let h = 0; h <= plan.chain.length; h++) {
    const next = []
    for (const s of beam) {
      let rels = h < plan.chain.length ? [...new Set([...alternatives(plan.chain[h]), ...Object.keys(plan.votes?.[h] ?? {}).flatMap((r) => alternatives(r))])] : (want ? Object.keys(TYPE).filter((r) => typeOk(r, want)) : [])
      if (o.exhaustive && h < plan.chain.length + 1) { const has = new Set((ix.byEntity.get(s.entity) ?? []).filter((f) => norm(f.subject) === s.entity).map((f) => f.relation)); rels = [...new Set([...rels, ...has])] }
      if (h === plan.chain.length - 1 && want && !(CLASS[plan.chain[h]] ? FAMILY_TYPE[plan.chain[h]] === want || (want === 'place' && ['city', 'country'].includes(FAMILY_TYPE[plan.chain[h]])) || (want === 'person' && FAMILY_TYPE[plan.chain[h]] === 'person') : typeOk(plan.chain[h], want))) rels = [...new Set([...rels, ...Object.keys(TYPE).filter((r) => typeOk(r, want))])]
      if (h === plan.chain.length) consider(s, 0) // the plan's own length
      else if (h > 0 && want && typeOk(s.evidence[s.evidence.length - 1]?.relation, want) && !(CLASS[plan.chain[plan.chain.length - 1]] ? FAMILY_TYPE[plan.chain[plan.chain.length - 1]] === want : typeOk(plan.chain[plan.chain.length - 1], want))) consider(s, -1) // stop early: the type is already right and the plan's tail would break it
      const expand = (rel, relScore, via) => {
        const vs = versionsOf(ix, via ? norm(via.object) : s.entity, rel); if (!vs.length) return false
        vs.slice().reverse().forEach((f, back) => { const nk = find(f.object)
          const st = { entity: nk?.key ?? null, hop: h + 1, path: [...s.path, rel], score: s.score + relScore - 0.5 * back + (o.lexical ?? 0) * lexical(rel, question), evidence: [...s.evidence, ...(via ? [via] : []), f] }
          if (h + 1 >= plan.chain.length) { if (h + 1 === plan.chain.length) consider(st, 0); else consider(st, -1) }
          if (st.entity && h + 1 < plan.chain.length + 1) next.push(st) })
        return true
      }
      let direct = false
      const planned = (rel, h) => h < plan.chain.length && (rel === plan.chain[h] || (CLASS[plan.chain[h]] ?? []).includes(rel) || !!(plan.votes?.[h]?.[rel]) || Object.keys(plan.votes?.[h] ?? {}).some((r) => (CLASS[r] ?? []).includes(rel)))
      const weight = (rel, h) => plan.votes ? (Math.max(0, ...Object.entries(plan.votes[h] ?? {}).filter(([r]) => r === rel || (CLASS[r] ?? []).includes(rel)).map(([, n]) => n)) / plan.plans) : 1
      for (const rel of rels) { const relScore = h < plan.chain.length ? (planned(rel, h) ? 2 + weight(rel, h) : (alternatives(plan.chain[h]).includes(rel) ? 1 : 0)) : -1; if (expand(rel, relScore, null)) direct = true }
      let bridged = false
      if (!direct && h < plan.chain.length) { // the bridge: the entity's own current facts, one step, then the relation
        const own = (ix.byEntity.get(s.entity) ?? []).filter((f) => f.current && norm(f.subject) === s.entity).sort((a, b) => a.serial - b.serial)
        for (const via of own) for (const rel of rels) { const relScore = planned(rel, h) ? 2 : 0.5; if (expand(rel, relScore, via)) bridged = true } }
      if (!direct && !bridged && h < plan.chain.length - 1 && s.evidence.length) { // the skip: a planned relation nothing satisfies is dropped and the chain goes on from here
        const skipped = { ...s, hop: h + 1, path: [...s.path, `(skip ${plan.chain[h]})`], score: s.score - 2 }; next.push(skipped) }
    }
    beam = next.sort((a, b) => b.score - a.score).slice(0, WIDTH); if (!beam.length) break
  }
  if (!finals.length) return { answer: null, evidence: [], trace: [{ step: 'entity', entity: plan.entity, found: base.key, how: base.how }, { step: 'dead', chain: plan.chain }], score: -Infinity, complete: false }
  for (const f of finals) f.score -= plan.penalty ?? 0
  finals.sort((a, b) => b.score - a.score || b.evidence[b.evidence.length - 1].serial - a.evidence[a.evidence.length - 1].serial)
  const best = finals[0]
  return { answer: best.evidence[best.evidence.length - 1].object, evidence: best.evidence, score: best.score, complete: true,
    trace: [{ step: 'entity', entity: plan.entity, found: base.key, how: base.how }, ...best.evidence.map((f, i) => ({ step: 'hop', entity: norm(f.subject), relation: f.relation, planned: plan.chain[i] ?? null, serial: f.serial, object: f.object })), { step: 'type', want, finals: finals.length, runner_up: finals[1] ? { answer: finals[1].evidence[finals[1].evidence.length - 1].object, score: finals[1].score } : null }] }
}

/**
 * The lattice: several plans for one question merged into one sketch. At each
 * hop position the relations the plans proposed are kept with their agreement
 * (how many plans said so); the chain is as long as the longest plan and the
 * search may stop early on the answer type. A relation that more compilers
 * proposed scores higher; a path may take hop 1 from one plan and hop 2 from
 * another, which no single plan allowed. Deterministic; no gold; no model.
 */
export function lattice(plans) {
  const entity = plans.map((p) => p.entity).sort((a, b) => plans.filter((x) => x.entity === b).length - plans.filter((x) => x.entity === a).length)[0]
  const len = Math.max(...plans.map((p) => p.chain.length)); const hops = []
  for (let h = 0; h < len; h++) { const votes = {}; for (const p of plans) if (p.chain[h]) votes[p.chain[h]] = (votes[p.chain[h]] ?? 0) + 1; hops.push(votes) }
  return { entity, chain: hops.map((v) => Object.entries(v).sort((a, b) => b[1] - a[1])[0][0]), votes: hops, plans: plans.length, set: 'lattice' }
}
