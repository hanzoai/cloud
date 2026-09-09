/** The FactConsolidation table from runs/mab-*: one line per (split, row, reader). */
import { readdirSync, readFileSync, existsSync } from 'node:fs'
const ROOT = new URL('../runs/', import.meta.url).pathname
const SIZES = ['sh_6k', 'mh_6k', 'sh_32k', 'mh_32k', 'sh_64k', 'mh_64k', 'sh_262k', 'mh_262k']
const rows = readdirSync(ROOT).filter((d) => d.startsWith('mab-') && existsSync(ROOT + d + '/metrics.json')).map((d) => JSON.parse(readFileSync(ROOT + d + '/metrics.json', 'utf8')))
const ORDER = ['semantic', 'lexical', 'rrf', 'entities', 'timeline', 'hops', 'resolver', 'full', 'noreader']
rows.sort((a, b) => a.split.localeCompare(b.split) || a.reader.localeCompare(b.reader) || ORDER.indexOf(a.row) - ORDER.indexOf(b.row))
const cell = (m, s) => m.by_size[s] ? `${(m.by_size[s].substring_em * 100).toFixed(1)} [${(m.by_size[s].ci95[0] * 100).toFixed(0)}–${(m.by_size[s].ci95[1] * 100).toFixed(0)}]` : '—'
console.log('| split | reader | row | ' + SIZES.join(' | ') + ' | facts/q | tokens/q | p50 ms |'); console.log('|' + '---|'.repeat(SIZES.length + 6))
for (const m of rows) console.log(`| ${m.split} | ${m.reader} | ${m.row} | ${SIZES.map((s) => cell(m, s)).join(' | ')} | ${m.facts_per_q} | ${m.context_tokens_per_q} | ${m.retrieval_ms_p50} |`)
