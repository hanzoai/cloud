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
 * Give it more than one run and it prints them as a table: the policies in a
 * column each, which is how they get compared without spending a reader token.
 *
 *   node depth.mjs runs/<name> [runs/<other> ...]
 */
import { readFileSync } from 'node:fs'

const store = JSON.parse(readFileSync(new URL('./brain-vectors.json', import.meta.url), 'utf8'))
const dirs = process.argv.slice(2)
if (!dirs.length) { console.error('usage: node depth.mjs runs/<name> [runs/<other> ...]'); process.exit(1) }

const pct = (a, b) => `${((100 * a) / b).toFixed(1)}%`
const KS = [1, 2, 3, 5, 8, 10, 15, 20]

const read = (dir) => {
  const rows = readFileSync(`${dir}/predictions.jsonl`, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l))
  const depth = []
  let asked = 0
  for (const r of rows) {
    const ev = store[r.ci]?.qa?.[r.qi]?.evidence ?? []
    const ctx = r.ctx ?? []
    if (!ev.length) continue
    asked++
    const at = ev.map((e) => ctx.indexOf(e))
    if (at.some((i) => i < 0)) continue
    depth.push(Math.max(...at) + 1)
  }
  depth.sort((a, b) => a - b)
  return { name: dir.split('/').pop(), asked, depth, within: (k) => depth.filter((d) => d <= k).length }
}

const runs = dirs.map(read)
const w = Math.max(...runs.map((r) => r.name.length))

console.log(`${'run'.padEnd(w)}  ${'evidence'.padStart(9)}  ${KS.map((k) => `k${k}`.padStart(7)).join('')}`)
console.log(`${''.padEnd(w)}  ${'retrieved'.padStart(9)}  ${KS.map(() => 'of all'.padStart(7)).join('')}`)
for (const r of runs) {
  console.log(
    `${r.name.padEnd(w)}  ${pct(r.depth.length, r.asked).padStart(9)}  ` +
      KS.map((k) => pct(r.within(k), r.asked).padStart(7)).join('')
  )
}
