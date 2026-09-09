/**
 * Canonical entities across a conversation's sessions, and the assembled graph.
 *
 * Each session's extraction names its entities locally (e1, e2 …). Across
 * sessions the same person or place comes back under the same name or one of
 * its aliases, so canonicalisation is a merge on (type, normalised name) with
 * the alias table as a second key. The two speakers are fixed ids. Nothing is
 * inferred from capitalisation: an entity exists because the extractor said so.
 */
import { readFileSync, existsSync } from 'node:fs'
import { sessions } from './extract.mjs'

const norm = (s) => String(s ?? '').toLowerCase().replace(/[^a-z0-9 ]+/g, ' ').replace(/\s+/g, ' ').trim()
const slug = (s) => norm(s).replace(/ /g, '-')

/** Merge one session's local entities into the conversation's table; returns local id -> canonical id. */
function merge(table, byKey, local, speakers) {
  const map = {}
  for (const e of local) {
    if (!e?.id || !e?.name) continue
    const type = String(e.type ?? 'other').toLowerCase(), n = norm(e.name)
    let id = null
    if (speakers.has(n)) id = `p:${slug(e.name)}`
    else { const keys = [`${type}:${n}`, ...(e.aliases ?? []).map((a) => `${type}:${norm(a)}`)]; id = keys.map((k) => byKey.get(k)).find(Boolean) ?? null
      if (!id && type !== 'person') { const k2 = byKey.get(`*:${n}`); if (k2) id = k2 } }
    if (!id) { id = `e:${type}:${slug(e.name)}`; if (table.has(id)) id = `${id}~${table.size}`; table.set(id, { id, name: e.name, type, aliases: new Set(), mentions: 0 }) }
    const row = table.get(id); row.mentions++
    for (const a of [e.name, ...(e.aliases ?? [])]) { if (norm(a) !== norm(row.name)) row.aliases.add(a); byKey.set(`${type}:${norm(a)}`, id); byKey.set(`*:${norm(a)}`, id) }
    map[e.id] = id
  }
  return map
}

/** The graph of one conversation: canonical entities, facts, events, anchors, from the checkpoints. */
export function assembleOne(ci, conv) {
  const speakers = new Set([conv.speaker_a, conv.speaker_b].map(norm))
  const table = new Map(); const byKey = new Map()
  for (const sp of [conv.speaker_a, conv.speaker_b]) table.set(`p:${slug(sp)}`, { id: `p:${slug(sp)}`, name: sp, type: 'person', aliases: new Set(), mentions: 0 })
  const facts = [], events = [], anchors = []; let sessionsDone = 0
  for (const s of sessions(conv)) {
    const f = `data/extract/${ci}-${s.key}.json`; if (!existsSync(f)) continue
    const x = JSON.parse(readFileSync(f, 'utf8')); sessionsDone++
    const map = merge(table, byKey, x.entities, speakers)
    const base = facts.length
    for (const fa of x.facts) facts.push({ text: fa.text, subject: map[fa.subject] ?? null, relation: fa.relation ?? null, object: map[fa.object] ?? null, literal: fa.literal ?? null,
      event_time: fa.event_time ?? null, valid_from: fa.valid_from ?? null, valid_to: fa.valid_to ?? null, observed_at: x.observed_at, confidence: fa.confidence ?? null, supersedes: fa.supersedes ?? null, turns: fa.turns, sess: s.key, event: null, model: x.model })
    for (const ev of x.events ?? []) { const id = `ev:${ci}:${events.length}`; events.push({ id, name: ev.name, time: ev.time ?? null, turns: ev.turns ?? [], sess: s.key }); for (const fi of ev.facts ?? []) if (facts[base + fi]) facts[base + fi].event = id }
    for (const [turn, cues] of Object.entries(x.anchors ?? {})) for (const text of cues) anchors.push({ text, turn, sess: s.key })
  }
  const entities = [...table.values()].map((e) => ({ ...e, aliases: [...e.aliases] }))
  return { entities, facts, events, anchors, sessionsDone, sessions: sessions(conv).length }
}
export const assemble = (corpus) => corpus.map((c, ci) => assembleOne(ci, c.conversation))

const MAIN = process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())
if (MAIN) {
  const corpus = JSON.parse(readFileSync('locomo10.json', 'utf8'))
  for (const [ci, g] of assemble(corpus).entries()) console.log(`conversation ${ci + 1}: ${g.sessionsDone}/${g.sessions} sessions · ${g.entities.length} entities · ${g.facts.length} facts · ${g.events.length} events · ${g.anchors.length} anchors`)
}
