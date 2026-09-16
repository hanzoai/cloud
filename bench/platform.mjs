/**
 * The platform lanes, as data.
 *
 * `brain/results.mjs` writes the retrieval and answer tables that hanzo.ai reads.
 * The lanes that measure the software itself — whether you can have it, whether
 * it tells anyone, what is on the disk, what a call costs — printed to a
 * terminal and reached no further. This runs them and writes what they said.
 *
 * Nothing is typed in here: each lane emits its own rows through `say` (or, for
 * the transports harness, `-json`), so a rerun that changes a number changes this
 * file, and a lane that fails leaves its section out rather than a stale one in.
 *
 *   node bench/platform.mjs            # runs every lane, writes benchmarks-platform.json
 *   node bench/platform.mjs self transports # just those
 */
import { execFileSync } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const here = new URL('.', import.meta.url).pathname

const LANES = [
  { lane: 'self', script: 'self/run.sh', title: 'Ownership — building and serving it yourself' },
  { lane: 'modules', script: 'self/modules.sh', title: 'Ownership — can anyone fetch what it is built from' },
  { lane: 'egress', script: 'egress/run.sh', title: 'Privacy — what leaves the machine' },
  { lane: 'cipher', script: 'cipher/run.sh', title: 'At rest — what is on the disk' },
  { lane: 'transports', script: 'transport/run.sh', title: 'The per-call tax, per transport' },
]

const want = process.argv.slice(2)
const lanes = want.length ? LANES.filter((l) => want.includes(l.lane)) : LANES

const dir = mkdtempSync(join(tmpdir(), 'bench-platform-'))
const sections = []
let host = ''

for (const l of lanes) {
  const out = join(dir, `${l.lane}.json`)
  process.stderr.write(`— ${l.lane}\n`)
  let text = ''
  try {
    text = execFileSync('bash', [join(here, l.script)], {
      encoding: 'utf8',
      env: { ...process.env, BENCH_JSON: out },
      stdio: ['ignore', 'pipe', 'inherit'],
      maxBuffer: 1 << 24,
    })
  } catch (e) {
    // A lane that fails says so and contributes nothing. A section carried over
    // from a previous run would be the one thing worse than an absent one.
    process.stderr.write(`  ${l.lane} failed; leaving it out\n`)
    continue
  }
  const line = text.split('\n').find((x) => x.startsWith('host: '))
  if (line && !host) host = line.slice('host: '.length).trim()
  if (!existsSync(out)) continue
  const rows = JSON.parse(readFileSync(out, 'utf8'))
  sections.push({ lane: l.lane, title: l.title, rows })
}

rmSync(dir, { recursive: true, force: true })
const path = join(here, 'benchmarks-platform.json')
writeFileSync(path, JSON.stringify({ generated: new Date().toISOString(), host, sections }, null, 1) + '\n')
process.stderr.write(`\n${path}: ${sections.length} lanes\n`)
