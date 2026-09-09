/**
 * Can the structured index reach the gold turns at all? A re-ranker can only lift
 * a turn some fact cites, so this is the ceiling on the fact layer, measured on
 * the gold turns of LoCoMo categories 1–4 (multi-hop, temporal, open-domain,
 * single-hop):
 *
 *   coverage   % of gold turns cited by at least one fact (ours / LoCoMo's / union)
 *   entity     % of gold turns cited by a fact whose subject or object is an
 *              entity the question names (over questions that name one)
 *   temporal   % of category-2 gold turns cited by a fact carrying a date
 *   anchors    % of gold turns with a cue anchor
 *
 *   node coverage.mjs             # the table, over the sessions extracted so far
 *   node coverage.mjs --judge     # + precision: 40 sampled facts judged against
 *                                 #   their source turns by a different model
 */
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { assemble } from './entities.mjs'

const corpus = JSON.parse(readFileSync('locomo10.json', 'utf8'))
const theirs = JSON.parse(readFileSync('facts-vectors.json', 'utf8'))
const graph = assemble(corpus)
const CATS = { 1: 'multi-hop', 2: 'temporal', 3: 'open-domain', 4: 'single-hop' }
const pct = (a, b) => (b ? ((a / b) * 100).toFixed(1).padStart(5) + '%' : '    –')
const norm = (s) => String(s ?? '').toLowerCase().replace(/[^a-z0-9 ]+/g, ' ').replace(/\s+/g, ' ').trim()

const rows = {}; for (const c of Object.keys(CATS)) rows[c] = { turns: 0, ours: 0, theirs: 0, union: 0, anchored: 0, entQ: 0, entOk: 0, timeQ: 0, timeOk: 0, qAll: 0, qAllOurs: 0, qAllUnion: 0 }
let sessionsDone = 0, sessionsAll = 0
for (const [ci, conv] of corpus.entries()) {
  const g = graph[ci]; sessionsDone += g.sessionsDone; sessionsAll += g.sessions
  const done = new Set(); for (const f of g.facts) done.add(f.sess); for (const a of g.anchors) done.add(a.sess)
  const ours = new Map(), anchored = new Set(), dated = new Set(), byTurnEnts = new Map()
  for (const f of g.facts) for (const t of f.turns) { ours.set(t, (ours.get(t) ?? 0) + 1); if (f.event_time || f.valid_from || f.valid_to) dated.add(t)
    const s = byTurnEnts.get(t) ?? byTurnEnts.set(t, new Set()).get(t); if (f.subject) s.add(f.subject); if (f.object) s.add(f.object) }
  for (const a of g.anchors) anchored.add(a.turn)
  const th = new Set(); for (const f of theirs[ci]) for (const t of f.ids) th.add(t)
  const sessOf = (id) => { const m = String(id).match(/^D(\d+):\d+$/); return m ? 'session_' + m[1] : null }
  const entityNames = g.entities.flatMap((e) => [e.name, ...e.aliases].map((n) => [norm(n), e.id])).filter(([n]) => n.length >= 3)
  for (const q of conv.qa) {
    const r = rows[q.category]; if (!r) continue
    const ev = (Array.isArray(q.evidence) ? q.evidence : [q.evidence]).filter((t) => sessOf(t) && done.has(sessOf(t)))
    if (!ev.length) continue
    const qn = ' ' + norm(q.question) + ' '
    const asked = new Set(entityNames.filter(([n]) => qn.includes(' ' + n + ' ')).map(([, id]) => id))
    let allOurs = true, allUnion = true
    for (const t of ev) {
      r.turns++; const o = ours.has(t), y = th.has(t); if (o) r.ours++; if (y) r.theirs++; if (o || y) r.union++; if (anchored.has(t)) r.anchored++
      if (!o) allOurs = false; if (!o && !y) allUnion = false
      if (asked.size) { r.entQ++; const ents = byTurnEnts.get(t); if (ents && [...asked].some((e) => ents.has(e))) r.entOk++ }
      if (q.category === 2) { r.timeQ++; if (dated.has(t)) r.timeOk++ }
    }
    r.qAll++; if (allOurs) r.qAllOurs++; if (allUnion) r.qAllUnion++
  }
}
console.log(`\n── fact coverage of gold turns · ${sessionsDone}/${sessionsAll} sessions extracted (gold turns in unextracted sessions excluded) ──\n`)
console.log(`category      gold turns   ours    LoCoMo   union   | entity-correct   dated (cat 2)   anchored | questions all-gold: ours  union`)
for (const [c, name] of Object.entries(CATS)) { const r = rows[c]
  console.log(`${name.padEnd(12)}  ${String(r.turns).padStart(8)}   ${pct(r.ours, r.turns)}  ${pct(r.theirs, r.turns)}  ${pct(r.union, r.turns)}  | ${pct(r.entOk, r.entQ)} of ${String(r.entQ).padStart(4)}   ${c === '2' ? pct(r.timeOk, r.timeQ) : '     –'}         ${pct(r.anchored, r.turns)} |               ${pct(r.qAllOurs, r.qAll)} ${pct(r.qAllUnion, r.qAll)}`) }
const tot = Object.values(rows).reduce((a, r) => { for (const k of Object.keys(r)) a[k] = (a[k] ?? 0) + r[k]; return a }, {})
console.log(`${'all'.padEnd(12)}  ${String(tot.turns).padStart(8)}   ${pct(tot.ours, tot.turns)}  ${pct(tot.theirs, tot.turns)}  ${pct(tot.union, tot.turns)}  | ${pct(tot.entOk, tot.entQ)} of ${String(tot.entQ).padStart(4)}                          ${pct(tot.anchored, tot.turns)} |               ${pct(tot.qAllOurs, tot.qAll)} ${pct(tot.qAllUnion, tot.qAll)}`)
const facts = graph.reduce((a, g) => a + g.facts.length, 0), anchors = graph.reduce((a, g) => a + g.anchors.length, 0), ents = graph.reduce((a, g) => a + g.entities.length, 0)
console.log(`\n${facts} facts · ${anchors} anchors · ${ents} canonical entities · ${(facts / Math.max(1, sessionsDone)).toFixed(1)} facts/session`)

if (process.argv.includes('--judge')) {
  // 40 facts, fixed seed, judged against their own turns by a model that did not write them
  const JUDGE = process.env.JUDGE ?? 'gemma4:31b'
  let seed = 7; const rand = () => (seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648
  const all = graph.flatMap((g, ci) => g.facts.map((f) => ({ ...f, ci })))
  const sample = []; while (sample.length < Math.min(40, all.length)) { const f = all[Math.floor(rand() * all.length)]; if (!sample.includes(f)) sample.push(f) }
  const turnText = (ci, id) => { for (const k of Object.keys(corpus[ci].conversation)) if (/^session_\d+$/.test(k)) { const t = corpus[ci].conversation[k].find((x) => x.dia_id === id); if (t) return `${id} ${t.speaker}: ${t.text}${t.blip_caption ? ` [shares a photo: ${t.blip_caption}]` : ''}` } return id }
  const out = []
  for (const [i, f] of sample.entries()) {
    const turns = f.turns.map((t) => turnText(f.ci, t)).join('\n')
    const prompt = `Turns from a conversation:\n${turns}\n\nA fact extracted from them: "${f.text}"\n(subject: ${f.subject}, relation: ${f.relation}, object: ${f.object ?? f.literal}, event_time: ${f.event_time}, valid_from: ${f.valid_from})\n\nIs the fact stated or clearly implied by these turns, with the right people and the right date? Reply as JSON: {"verdict": "supported" | "partly" | "unsupported", "reason": "one sentence"}`
    const r = await fetch(`${process.env.OLLAMA ?? 'http://127.0.0.1:11434'}/api/chat`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ model: JUDGE, stream: false, format: 'json', options: { temperature: 0, num_predict: 200 }, messages: [{ role: 'user', content: prompt }] }) })
    let v = { verdict: 'error', reason: `${r.status}` }; try { v = JSON.parse((await r.json()).message.content) } catch {}
    out.push({ fact: f.text, structured: { subject: f.subject, relation: f.relation, object: f.object, literal: f.literal, event_time: f.event_time, valid_from: f.valid_from, valid_to: f.valid_to }, turns, extractor: f.model, judge: JUDGE, verdict: v.verdict, reason: v.reason })
    process.stdout.write(`\r  judged ${i + 1}/${sample.length}   `)
  }
  writeFileSync('data/extract/precision.json', JSON.stringify(out, null, 1))
  const n = (k) => out.filter((o) => o.verdict === k).length
  console.log(`\nprecision on ${out.length} sampled facts (judge ${JUDGE}): supported ${n('supported')} · partly ${n('partly')} · unsupported ${n('unsupported')} · error ${n('error')} → ${((n('supported') / out.length) * 100).toFixed(0)}% strict, ${(((n('supported') + n('partly')) / out.length) * 100).toFixed(0)}% lenient · data/extract/precision.json`)
}
