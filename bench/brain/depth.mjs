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

// What the reader was handed, per question, for the first k of its list. The
// prompt is one line per turn — id, a bracketed date, the text — so this is the
// line lengths over four, the same estimate `run.mjs` records. The date is not
// in the store, so a line carries a fixed allowance for it; the k=20 column is
// printed beside the tokens the reader actually reported, which is how you know
// whether to trust the shorter ones.
const DATE = 14
const textOf = (ci) => (textOf.seen ??= new Map()).get(ci)
  ?? (textOf.seen.set(ci, new Map((store[ci]?.turns ?? []).map((t) => [t.id, (t.text ?? '').length]))), textOf.seen.get(ci))

const read = (dir) => {
  const rows = readFileSync(`${dir}/predictions.jsonl`, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l))
  const depth = []
  const chars = new Map()
  let asked = 0, reported = 0, reportedOf = 0
  for (const r of rows) {
    const ev = store[r.ci]?.qa?.[r.qi]?.evidence ?? []
    const ctx = r.ctx ?? []
    if (!ev.length) continue
    asked++
    const len = textOf(r.ci)
    for (const k of KS) {
      let n = 0
      for (const id of ctx.slice(0, k)) n += (len.get(id) ?? 0) + id.length + DATE
      chars.set(k, (chars.get(k) ?? 0) + n)
    }
    if (r.usage?.prompt_tokens) { reported += r.usage.prompt_tokens; reportedOf++ }
    const at = ev.map((e) => ctx.indexOf(e))
    if (at.some((i) => i < 0)) continue
    depth.push(Math.max(...at) + 1)
  }
  depth.sort((a, b) => a - b)
  return {
    name: dir.split('/').pop(), asked, depth,
    within: (k) => depth.filter((d) => d <= k).length,
    tokens: (k) => Math.round(chars.get(k) / 4 / asked),
    reported: reportedOf ? Math.round(reported / reportedOf) : null,
  }
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

console.log(`\n${'prompt tokens'.padEnd(w)}  ${'reader said'.padStart(9)}  ${KS.map((k) => `k${k}`.padStart(7)).join('')}`)
for (const r of runs) {
  console.log(
    `${r.name.padEnd(w)}  ${String(r.reported ?? '-').padStart(9)}  ` +
      KS.map((k) => String(r.tokens(k)).padStart(7)).join('')
  )
}
