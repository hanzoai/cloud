/**
 * Sandbox — what it costs to get an agent a place to run code.
 *
 * Naïve publish 2.79 ms cold start and 1.2 MB per agent, on isolated-vm, against
 * E2B (<200 ms, 512 MB), Modal (~1 s, 128 MB) and Cloudflare (1–3 s, 256 MB).
 *
 * Two things are being conflated in that table, and separating them is the whole
 * point of this measurement:
 *
 *   A V8 ISOLATE runs JavaScript inside a process that is already running. It is
 *   a `new Isolate()`, so milliseconds and a megabyte are the right order of
 *   magnitude — and it cannot run pytest, pip, cargo, or a shell.
 *
 *   A CONTAINER is a kernel boundary with a filesystem. It costs more to start
 *   and it can run the thing a coding agent was actually asked to do.
 *
 * So this measures both, on the same laptop, and reports them as two rows
 * rather than one. An isolate is not a faster sandbox; it is a different
 * primitive, and the honest comparison is per workload.
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

// ── 1. A V8 isolate, which is what they measure. Node ships the same engine, so
// a fresh execution context is the closest honest local analogue.
import vm from 'node:vm'
const isolate = []
for (let i = 0; i < RUNS * 20; i++) {
  const t0 = performance.now()
  const ctx = vm.createContext({ x: i })
  vm.runInContext('x * 2', ctx)
  isolate.push(performance.now() - t0)
}
row('V8 context (in-process)', stat(isolate), 'JS only, no filesystem')

// ── 2. A container, cold: image already pulled, container created and run.
let container = null
try {
  execSync('docker image inspect alpine:3.20 > /dev/null 2>&1 || docker pull -q alpine:3.20', {
    stdio: 'ignore',
  })
  const times = []
  for (let i = 0; i < RUNS; i++) {
    const t0 = performance.now()
    execFileSync('docker', ['run', '--rm', 'alpine:3.20', 'true'], { stdio: 'ignore' })
    times.push(performance.now() - t0)
  }
  container = stat(times)
  row('OCI container (docker run)', container, 'anything that runs on Linux')
} catch (e) {
  console.log(`  OCI container                  unavailable: ${String(e).slice(0, 60)}`)
}

// ── 3. A container that is already warm — the shape a pooled sandbox has.
let warm = null
try {
  const id = execFileSync('docker', ['run', '-d', 'alpine:3.20', 'sleep', '300'], {
    encoding: 'utf8',
  }).trim()
  const times = []
  for (let i = 0; i < RUNS; i++) {
    const t0 = performance.now()
    execFileSync('docker', ['exec', id, 'true'], { stdio: 'ignore' })
    times.push(performance.now() - t0)
  }
  warm = stat(times)
  row('Warm container (docker exec)', warm, 'pooled: pay once, reuse')
  execFileSync('docker', ['rm', '-f', id], { stdio: 'ignore' })
} catch (e) {
  console.log(`  Warm container                 unavailable`)
}

// ── What each one can actually do. The reason the table has two rows.
console.log(`\n── What the primitive can run ──\n`)
const canRun = (label, ok) => console.log(`  ${label.padEnd(30)} ${ok}`)
canRun('V8 isolate', 'JavaScript. No pip, no pytest, no cargo, no shell.')
canRun('Container', 'Any language, any binary, a real filesystem.')

console.log(`\n── Published, for comparison ──\n`)
for (const [name, cold, ram] of [
  ['Naïve (isolated-vm)', '2.79 ms', '1.2 MB'],
  ['E2B', '<200 ms', '512 MB min'],
  ['Modal', '~1 s', '128 MB min'],
  ['Cloudflare', '1–3 s', '256 MB min'],
]) {
  console.log(`  ${name.padEnd(30)} ${cold.padStart(10)}  ${ram}`)
}

if (container) {
  console.log(`\nMeasured here: a container is ${(container.p50 / stat(isolate).p50).toFixed(0)}× the cost of an isolate`)
  console.log(`to start, and it is the only one of the two that can run a test suite.`)
  if (warm) {
    console.log(`A pooled container answers in ${ms(warm.p50)}, which is the number that`)
    console.log(`matters when a fleet reuses sandboxes instead of creating one per call.`)
  }
}
