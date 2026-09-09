/**
 * MemoryAgentBench FactConsolidation: the haystack is a numbered list of
 * templated facts, and a later fact about the same (subject, relation) silently
 * replaces an earlier one. Nothing marks the update. So the write side is an
 * extractor that turns every line into a typed record — subject, relation,
 * object, serial — and a supersession chain keyed by (subject, relation), where
 * the highest serial is current. The templates are a closed set; each is one
 * pattern below, and a line that matches none is reported, never guessed.
 *
 *   node parse.mjs            # parse all eight haystacks, report coverage
 *   import { parse, load }    # used by index.mjs / run.mjs
 */
import { readFileSync, existsSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'

export const DATA = new URL('../data/mab/', import.meta.url).pathname
const ROWS = DATA + 'rows.json'

/** The eight haystacks as JSON, extracted once from the parquet with pandas. */
export function load() {
  if (!existsSync(ROWS)) {
    execFileSync('uv', ['run', '--quiet', '--with', 'pandas', '--with', 'pyarrow', 'python3', '-c', `
import pandas as pd, json
df = pd.read_parquet('${DATA}Conflict_Resolution-00000-of-00001.parquet')
rows = []
for _, r in df.iterrows():
    ids = list(r['metadata']['qa_pair_ids'])
    rows.append({'id': ids[0].rsplit('_no', 1)[0], 'qa_ids': ids, 'context': r['context'],
                 'questions': list(r['questions']), 'answers': [list(a) for a in r['answers']]})
json.dump(rows, open('${ROWS}', 'w'))
`], { stdio: 'inherit' })
  }
  return JSON.parse(readFileSync(ROWS, 'utf8'))
}

// relation → [pattern]. Subject and object are the two captures, in that order.
// Order matters where one template is a prefix of another.
const E = '(.+?)'
export const TEMPLATES = [
  ['capital',        `^The capital of ${E} is ${E}\\.$`],
  ['head_of_gov',    `^The name of the current head of the ${E} government is ${E}\\.$`],
  ['head_of_state',  `^The name of the current head of state in ${E} is ${E}\\.$`],
  ['official_lang',  `^The official language of ${E} is ${E}\\.$`],
  ['author',         `^The author of ${E} is ${E}\\.$`],
  ['chairperson',    `^The chairperson of ${E} is ${E}\\.$`],
  ['director',       `^The director of ${E} is ${E}\\.$`],
  ['ceo',            `^The chief executive officer of ${E} is ${E}\\.$`],
  ['headquarters',   `^The headquarters of ${E} is located in the city of ${E}\\.$`],
  ['educated_at',    `^The univ\\S*ty where ${E} was educated is ${E}\\.$`],
  ['employer',       `^${E} is employed by ${E}\\.$`],
  ['producer',       `^The company that produced ${E} is ${E}\\.$`],
  ['broadcaster',    `^The origi?a?n?a?l broadcaster of ${E} is ${E}\\.$`],
  ['original_lang',  `^${E} was written in the language of ${E}\\.$`],
  ['original_lang',  `^The original language of ${E} is ${E}\\.$`],
  ['founding_place', `^${E} was founded in the city of ${E}\\.$`],
  ['genre',          `^The type of music that ${E} plays is ${E}\\.$`],
  ['child',          `^${E}'s child is ${E}\\.$`],
  ['occupation',     `^${E} works in the field of ${E}\\.$`],
  ['head_coach',     `^The head coach of ${E} is ${E}\\.$`],
  ['citizenship',    `^${E} is a citizen of ${E}\\.$`],
  ['continent',      `^${E} is located in the continent of ${E}\\.$`],
  ['spouse',         `^${E} is married to ${E}\\.$`],
  ['performer',      `^${E} was performed by ${E}\\.$`],
  ['country_origin', `^${E} was created in the country of ${E}\\.$`],
  ['death_place',    `^${E} died in the city of ${E}\\.$`],
  ['birth_place',    `^${E} was born in the city of ${E}\\.$`],
  ['language',       `^${E} speaks the language of ${E}\\.$`],
  ['position',       `^${E} plays the position of ${E}\\.$`],
  ['work_location',  `^${E} worked in the city of ${E}\\.$`],
  ['founder',        `^${E} was founded by ${E}\\.$`],
  ['sport',          `^${E} is associated with the sport of ${E}\\.$`],
  ['religion',       `^${E} is affiliated with the religion of ${E}\\.$`],
  ['notable_work',   `^${E} is famous for ${E}\\.$`],
  ['creator',        `^${E} was created by ${E}\\.$`],
  ['developer',      `^${E} was developed by ${E}\\.$`],
  ['member_of',      `^${E} is a member of ${E}\\.$`],
  ['part_of',        `^${E} is part of ${E}\\.$`],
  ['owner',          `^${E} is owned by ${E}\\.$`],
  ['producer',       `^${E} was produced by ${E}\\.$`],
  ['screenwriter',   `^${E} was written by ${E}\\.$`],
  ['composer',       `^${E} was composed by ${E}\\.$`],
  ['award',          `^${E} received the award ${E}\\.$`],
  // an office and its holder: "The Prime Minister of Sweden is X.", "The Illinois Attorney General is Y."
  // The subject is the whole office name, polity included. Last, so every "The X of Y is Z"
  // template above claims its line first.
  ['office',         `^The ${E} is ${E}\\.$`],
].map(([rel, re]) => [rel, new RegExp(re)])

/** The typed record for one line, or null with the line kept for the report. */
export function parseLine(line) {
  const m = line.match(/^(\d+)\.\s*(.+)$/); if (!m) return null
  const serial = Number(m[1]), text = m[2].trim()
  for (const [relation, re] of TEMPLATES) {
    const h = text.match(re); if (h) return { serial, text, subject: h[1].trim(), relation, object: h[2].trim() }
  }
  return { serial, text, subject: null, relation: null, object: null }
}

/** Every fact of a haystack, plus the chain: (subject|relation) → serials ascending. */
export function parse(context) {
  const facts = []
  for (const line of context.split('\n')) { const f = parseLine(line); if (f) facts.push(f) }
  const chain = new Map()
  for (const f of facts) if (f.relation) { const k = key(f.subject, f.relation); (chain.get(k) ?? chain.set(k, []).get(k)).push(f.serial) }
  return { facts, chain }
}
export const norm = (s) => s.toLowerCase().replace(/["“”‘’']/g, '').replace(/\s+/g, ' ').trim()
export const key = (subject, relation) => `${norm(subject)}|${relation}`

if (process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())) {
  const rows = load()
  const seen = new Map()
  for (const r of rows) {
    const { facts, chain } = parse(r.context)
    const bad = facts.filter((f) => !f.relation)
    const rels = {}; for (const f of facts) if (f.relation) rels[f.relation] = (rels[f.relation] ?? 0) + 1
    const multi = [...chain.values()].filter((s) => s.length > 1)
    const objects = new Set(facts.map((f) => f.object && norm(f.object)))
    const goldSeen = r.answers.filter((a) => a.some((g) => objects.has(norm(g)))).length
    console.log(`${r.id.padEnd(26)} facts ${String(facts.length).padStart(6)}  unparsed ${String(bad.length).padStart(4)}  (subject,relation) keys ${chain.size}  with updates ${multi.length} (max chain ${Math.max(0, ...multi.map((s) => s.length))})  gold answer is some object: ${goldSeen}/100`)
    for (const b of bad.slice(0, 6)) if (!seen.has(b.text)) { seen.set(b.text, 1); console.log('   unparsed:', b.text.slice(0, 110)) }
    if (r.id.endsWith('6k')) console.log('   relations:', Object.entries(rels).sort((a, b) => b[1] - a[1]).map(([k, v]) => `${k}:${v}`).join(' '))
  }
}
