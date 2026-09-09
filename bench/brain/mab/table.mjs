/** The FactConsolidation table from runs/mab-*: one line per (split, row, reader). */
import { readdirSync, readFileSync, existsSync } from 'node:fs'
const ROOT = new URL('../runs/', import.meta.url).pathname
const SIZES = ['sh_6k', 'mh_6k', 'sh_32k', 'mh_32k', 'sh_64k', 'mh_64k', 'sh_262k', 'mh_262k']
const rows = readdirSync(ROOT).filter((d) => d.startsWith('mab-') && existsSync(ROOT + d + '/metrics.json')).map((d) => JSON.parse(readFileSync(ROOT + d + '/metrics.json', 'utf8')))
const ORDER = ['semantic', 'lexical', 'rrf', 'entities', 'timeline', 'hops', 'resolver', 'full', 'noreader']
rows.sort((a, b) => a.split.localeCompare(b.split) || a.reader.localeCompare(b.reader) || ORDER.indexOf(a.row) - ORDER.indexOf(b.row))
const cell = (m, s) => m.by_size[s] ? `${(m.by_size[s].substring_em * 100).toFixed(1)} [${(m.by_size[s].ci95[0] * 100).toFixed(0)}–${(m.by_size[s].ci95[1] * 100).toFixed(0)}]` : '—'
// CAR pools over the 6k–262k haystacks, so the pooled mean over every answered question of a hop type sits beside the per-size cells
const pooled = (m, hop) => { const xs = Object.entries(m.by_size).filter(([s]) => s.startsWith(hop)); const n = xs.reduce((a, [, v]) => a + v.n, 0); return n ? `${(xs.reduce((a, [, v]) => a + v.substring_em * v.n, 0) / n * 100).toFixed(1)} (n=${n})` : '—' }
console.log('| split | reader | row | ' + SIZES.join(' | ') + ' | pooled SH | pooled MH | facts/q | tokens/q | p50 ms |'); console.log('|' + '---|'.repeat(SIZES.length + 8))
for (const m of rows) console.log(`| ${m.split} | ${m.reader} | ${m.row} | ${SIZES.map((s) => cell(m, s)).join(' | ')} | ${pooled(m, 'sh')} | ${pooled(m, 'mh')} | ${m.facts_per_q} | ${m.context_tokens_per_q} | ${m.retrieval_ms_p50} |`)
