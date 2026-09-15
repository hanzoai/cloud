/**
 * One row of a lane's table, and — when `BENCH_JSON` names a file — one entry in
 * it. The shell lanes have this in `host.sh`; these are the same two lines for
 * the ones written in node, so a row reaches the terminal and the file by the
 * same call and the two cannot disagree.
 *
 * The value is a string. A lane says "455 MB (477 bytes per agent)" as readily
 * as it says a number, and a reader that wants a number knows which row it
 * asked for.
 */
import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs'

const FILE = process.env.BENCH_JSON || ''

export function say(label, value) {
  console.log(`${String(label).padEnd(26)} ${value}`)
  if (!FILE) return
  let rows = []
  if (existsSync(FILE)) {
    try {
      rows = JSON.parse(readFileSync(FILE, 'utf8'))
    } catch {
      rows = []
    }
  }
  rows.push({ label: String(label), value: String(value) })
  writeFileSync(FILE, JSON.stringify(rows, null, 1))
}
