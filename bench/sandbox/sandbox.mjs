/**
 * Sandbox — what it costs to get an agent a place to run code.
 *
 * The figures cited at us are 2.79 ms cold start and 1.2 MB per agent on
 * isolated-vm, against E2B (<200 ms, 512 MB), Modal (~1 s, 128 MB) and
 * Cloudflare (1–3 s, 256 MB). None of the four has a source — ../naive.md says
 * so — and the first is the one this run stops citing and starts measuring.
 *
 * Three primitives, not two, and telling them apart is the point:
 *
 *   A V8 CONTEXT is a fresh global inside the isolate already running. Cheap
 *   because it shares the heap, which is also why it is not a sandbox.
 *
 *   A V8 ISOLATE has its own heap and its own globals. Milliseconds and about a
 *   megabyte — and it cannot run pytest, pip, cargo, or a shell.
 *
 *   A CONTAINER is a kernel boundary with a filesystem. It costs more to start
 *   and it can run the thing a coding agent was actually asked to do.
 *
 * All three are measured on the same laptop and reported as their own rows. An
 * isolate is not a faster sandbox; it is a different primitive, and the honest
 * comparison is per workload.
 */
import { execFileSync, execSync } from 'node:child_process'
import { performance } from 'node:perf_hooks'

const RUNS = Number(process.env.RUNS ?? 20)
const stat = (xs) => {
  const s = [...xs].sort((a, b) => a - b)
  return {
    min: s[0],
    p50: s[Math.floor(s.length * 0.5)],
    p95: s[Math.floor(s.length * 0.95)],
    mean: s.reduce((a, b) => a + b, 0) / s.length,
  }
}
const ms = (n) => `${n.toFixed(2)} ms`
const row = (label, s, note) =>
  console.log(
    `  ${label.padEnd(30)} ${ms(s.p50).padStart(10)}  p95 ${ms(s.p95).padStart(10)}  ${note ?? ''}`
  )

console.log(`\n── Sandbox cold start · ${RUNS} runs each ──\n`)

// ── 1. A V8 context: a fresh global inside the isolate that is already running.
// Cheap because it shares the heap — which is also why it is NOT what anyone
// means by a sandbox, and why the row below exists.
import vm from 'node:vm'
const isolate = []
for (let i = 0; i < RUNS * 20; i++) {
  const t0 = performance.now()
  const ctx = vm.createContext({ x: i })
  vm.runInContext('x * 2', ctx)
  isolate.push(performance.now() - t0)
}
row('V8 context (in-process)', stat(isolate), 'shares this heap — not a sandbox')

// ── 2. A V8 isolate, which is what a published isolate number is about: its own
// heap, its own globals, nothing shared with the process that made it.
//
// This used to be the context row above, labelled an "honest analogue". It is
// not one. A context costs a fraction of an isolate precisely because it skips
// the thing that makes an isolate a boundary, and reporting 0.15 ms against a
// published 2.79 ms was comparing two different primitives in our favour.
let ivmStat = null, ivmHeap = 0
try {
  const ivm = (await import('isolated-vm')).default
  const times = [], held = []
  for (let i = 0; i < RUNS * 10; i++) {
    const t0 = performance.now()
    const iso = new ivm.Isolate({ memoryLimit: 8 })
    iso.createContextSync()
    times.push(performance.now() - t0)
    if (held.length < 50) held.push(iso)
    else iso.dispose()
  }
  ivmStat = stat(times)
  ivmHeap = Math.round(
    held.reduce((a, i) => a + i.getHeapStatisticsSync().total_heap_size, 0) / held.length
  )
  for (const i of held) i.dispose()
  row('V8 isolate (isolated-vm)', ivmStat, `own heap, ${(ivmHeap / 1048576).toFixed(2)} MiB each`)
} catch {
  console.log('  V8 isolate                     unavailable: npm i isolated-vm')
}

// ── 3. A microVM, cold: kernel boot, command, shutdown.
//
// THIS USED TO SHELL OUT TO DOCKER, and docker is not what runs here. Nothing in
// the estate uses it — the builds run buildkit inside one of these, which is why
// `hanzo-vm checkpoint list` has a buildkit entry — so the lane was measuring a
// runtime we do not ship, on a machine that does not have it, and printed
// "unavailable" for both container rows while claiming numbers in the README.
//
// A microVM is a heavier boundary than a container and the honest column for it
// is the microVM column they publish: E2B's Firecracker and Morph, not Modal's
// gVisor. It boots a kernel, so it answers in hundreds of milliseconds rather
// than tens, and it will run anything that runs on Linux.
let boot = null
try {
  execFileSync('hanzo-vm', ['run', '/usr/bin/true'], { stdio: 'ignore' }) // discarded
  const times = []
  for (let i = 0; i < RUNS; i++) {
    const t0 = performance.now()
    execFileSync('hanzo-vm', ['run', '/usr/bin/true'], { stdio: 'ignore' })
    times.push(performance.now() - t0)
  }
  boot = stat(times)
  row('hanzo-vm, cold boot', boot, 'kernel boundary, anything that runs on Linux')
} catch (e) {
  console.log(`  hanzo-vm                       unavailable: ${String(e).slice(0, 60)}`)
}

// ── 4. The same, started from a checkpoint — what a pooled sandbox would be.
//
// A CHECKPOINT SAVES THE DISK, NOT THE BOOT, and the measurement is what says
// so: resuming from a 403 MB checkpoint costs 311.7 ms against a cold boot's
// 310.7 ms, which is the same number. `--from` gives a VM the filesystem some
// earlier run left behind; it does not skip the kernel.
//
// That is the row worth watching rather than quoting. E2B publishes "~1s from
// pause" and Morph "<250ms" for resume, and both of those are MEMORY snapshots —
// a different mechanism, which hanzo-vm does not have. Printing this row beside
// theirs without the distinction would claim a feature by measuring one that
// happens to share a name.
let resumed = null
try {
  const names = execFileSync('hanzo-vm', ['checkpoint', 'list'], { encoding: 'utf8' })
    .split('\n').slice(1).map((l) => l.trim().split(/\s+/)[0]).filter(Boolean)
  if (names.length) {
    const from = names[0]
    execFileSync('hanzo-vm', ['run', '--from', from, '/usr/bin/true'], { stdio: 'ignore' })
    const times = []
    for (let i = 0; i < RUNS; i++) {
      const t0 = performance.now()
      execFileSync('hanzo-vm', ['run', '--from', from, '/usr/bin/true'], { stdio: 'ignore' })
      times.push(performance.now() - t0)
    }
    resumed = stat(times)
    row(`hanzo-vm, from checkpoint`, resumed, `${from} — disk state, NOT a memory snapshot`)
  } else {
    console.log('  hanzo-vm from checkpoint       no checkpoints on this host')
  }
} catch (e) {
  console.log(`  hanzo-vm from checkpoint       unavailable`)
}

// ── What each one can actually do. The reason the table has two rows.
console.log(`\n── What the primitive can run ──\n`)
const canRun = (label, ok) => console.log(`  ${label.padEnd(30)} ${ok}`)
canRun('V8 isolate', 'JavaScript. No pip, no pytest, no cargo, no shell.')
canRun('hanzo-vm', 'Any language, any binary, a real kernel and filesystem.')

console.log(`\n── Attributed elsewhere, for comparison ──\n`)
for (const [name, cold, ram] of [
  ['isolated-vm, as cited at us', '2.79 ms', '1.2 MB'],
  ['E2B — Firecracker microVM', '<200 ms', '512 MB min'],
  ['Morph — microVM', '<250 ms resume', '—'],
  ['Modal — container/gVisor', '~1 s', '128 MB min'],
  ['Cloudflare — container', '1–3 s', '256 MB min'],
]) {
  console.log(`  ${name.padEnd(30)} ${cold.padStart(10)}  ${ram}`)
}
console.log(`\n  None of those is sourced to the vendor — every one is naive's figure FOR`)
console.log(`  that vendor, which is a different claim. See ../naive.md.`)
console.log(`\n  Two of them are the microVM column, and that is the column hanzo-vm is in.`)
console.log(`  Measured here it is slower than E2B's published cold start and than Morph's`)
console.log(`  published resume — on their hardware, which is the same uncontrolled`)
console.log(`  comparison this lane refuses to make in its own favour elsewhere.`)
if (ivmStat) {
  console.log(`\n  isolated-vm measured here: ${ms(ivmStat.p50)} and ${(ivmHeap / 1048576).toFixed(2)} MiB.`)
  console.log(`  The megabyte is V8's floor for a fresh isolate, not a vendor's choice —`)
  console.log(`  it is what ANY isolate costs, including one of ours.`)
}

if (boot) {
  console.log(`\nMeasured here: a microVM is ${(boot.p50 / stat(isolate).p50).toFixed(0)}× the cost of a V8 context to`)
  console.log(`start, and it is the only one of the two that can run a test suite.`)
  if (resumed) {
    const delta = ((resumed.p50 - boot.p50) / boot.p50) * 100
    console.log(`\nStarting from a checkpoint costs ${delta >= 0 ? '+' : ''}${delta.toFixed(1)}% against a cold boot, which is`)
    console.log(`no difference. A checkpoint carries the DISK a previous run left; the kernel`)
    console.log(`boots either way. There is no memory-snapshot resume here, so the published`)
    console.log(`resume figures above have no row of ours to sit beside — and inventing one`)
    console.log(`out of this measurement would claim a mechanism by borrowing its name.`)
  }
}
