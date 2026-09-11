/**
 * How deep the evidence actually sat.
 *
 * A run hands the reader k retrieved turns and the reader answers from them.
 * This asks, of the questions whose evidence was retrieved at all, how far down
 * the list the last piece of it was — which is what a smaller k would cost and
 * what better ranking would save. It reads a finished run's own rows: the ids
 * it gave the reader are in `ctx`, and the evidence is in the store.
 *
 * Coverage is not accuracy. Having the evidence in front of the reader is
 * necessary for a correct answer and does not produce one, so read a row here as
 * a ceiling on what trimming k could cost, not as an F1 prediction.
 *
 *   node depth.mjs runs/<name>
 */
import { readFileSync } from 'node:fs'

const store = JSON.parse(readFileSync(new URL('./brain-vectors.json', import.meta.url), 'utf8'))
const dir = process.argv[2]
if (!dir) { console.error('usage: node depth.mjs runs/<name>'); process.exit(1) }

const rows = readFileSync(`${dir}/predictions.jsonl`, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l))
const depth = []
let asked = 0, whole = 0
for (const r of rows) {
  const ev = store[r.ci]?.qa?.[r.qi]?.evidence ?? []
  const ctx = r.ctx ?? []
  if (!ev.length) continue
  asked++
  const at = ev.map((e) => ctx.indexOf(e))
  if (at.some((i) => i < 0)) continue
  whole++
  depth.push(Math.max(...at) + 1)
}
depth.sort((a, b) => a - b)
const within = (k) => depth.filter((d) => d <= k).length
const pct = (a, b) => `${((100 * a) / b).toFixed(1)}%`

console.log(`${dir.split('/').pop()}`)
console.log(`  questions with evidence          ${asked}`)
console.log(`  all of it retrieved              ${whole}  ${pct(whole, asked)}`)
console.log(`\n  k    all evidence within it   of every question`)
for (const k of [1, 2, 3, 5, 8, 10, 15, 20]) {
  if (k > Math.max(...depth)) break
  console.log(`  ${String(k).padEnd(4)} ${String(within(k)).padStart(6)}  ${pct(within(k), whole).padStart(7)}   ${pct(within(k), asked).padStart(7)}`)
}
