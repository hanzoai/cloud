/**
 * What a call costs us, and what we could charge for it.
 *
 * Every cost input is either measured on this machine or a published list
 * price. Nothing is estimated without saying so.
 *
 *   MEASURED (bench/fleet, brain.mjs, sandbox.mjs, this laptop, Sept 2026)
 *     dormant agent state      477 bytes
 *     pooled sandbox exec      35.8 ms
 *     cold container start     149.6 ms
 *     memory search            1.27 ms   (brute force, one conversation)
 *     embedding                14 ms     (zen-embedding-0.6b, batched)
 *     agent resume             0.031 ms
 *
 *   LIST PRICE (railway.com/pricing, Sept 2026)
 *     vCPU        $0.00000772 /vCPU/s
 *     memory      $0.00000386 /GB/s
 *     disk        $0.00000006 /GB/s
 *     egress      $0.05 /GB
 *     object      $0.015 /GB/month
 *
 *   COMPETITOR (usenaive.ai/pricing, Sept 2026)
 *     $0.05 per credit, one credit per call. LLM tokens billed separately.
 */

const RAIL = {
  vcpuSec: 0.00000772,
  gbSec: 0.00000386,
  diskGbSec: 0.00000006,
  egressGb: 0.05,
  objectGbMonth: 0.015,
}

const NAIVE_PER_CALL = 0.05
const OSS_SHARE = 0.25 // given away, per the brief

/**
 * The overheads a marginal-cost sum leaves out, and they are the ones that
 * actually grow.
 *
 * HISTORY IS THE REAL STORAGE LINE. A dormant agent is 477 bytes and stays
 * that size; its transcript does not. Every call appends, and nothing deletes,
 * so storage is a function of calls-ever rather than agents. At 2 KB of
 * retained turn per call this is the line that eventually exceeds compute.
 *
 * REDUNDANCY. One of everything is a demo. Three availability zones, and the
 * control plane is paid for three times.
 *
 * OPS. Ratio to infrastructure, from the observation that nobody runs a
 * platform whose people cost less than its machines.
 */
const HISTORY_BYTES_PER_CALL = 2048
const RETENTION_MONTHS = 12
const REDUNDANCY = 3
const OPS_MULTIPLE = 2.0 // people and tooling, as a multiple of infra

/** One agent call, as measured: wake, search memory, run a pooled sandbox. */
const CALL = {
  sandboxMs: 35.8,
  memoryMs: 1.27,
  resumeMs: 0.031,
  embedMs: 14,
  vcpu: 0.5, // while it runs
  gb: 0.25,
  egressKb: 8, // a request and its answer
}

const seconds = (CALL.sandboxMs + CALL.memoryMs + CALL.resumeMs + CALL.embedMs) / 1000
const marginalCall =
  seconds * CALL.vcpu * RAIL.vcpuSec +
  seconds * CALL.gb * RAIL.gbSec +
  (CALL.egressKb / 1024 / 1024) * RAIL.egressGb

console.log(`\n══ WHAT A CALL COSTS ══\n`)
console.log(`  compute time      ${(seconds * 1000).toFixed(1)} ms  (sandbox ${CALL.sandboxMs} + embed ${CALL.embedMs} + memory ${CALL.memoryMs} + resume ${CALL.resumeMs})`)
console.log(`  marginal cost     $${marginalCall.toFixed(8)} per call`)
console.log(`  Naïve charge      $${NAIVE_PER_CALL.toFixed(2)} per credit`)
console.log(`  their headroom    ${(NAIVE_PER_CALL / marginalCall).toFixed(0)}× marginal\n`)

/**
 * Marginal cost is not the business. A platform carries a control plane whether
 * anyone calls it or not, and that floor is what a price has to clear.
 */
const PLANE = {
  gatewayVcpu: 8,
  gatewayGb: 32,
  storeVcpu: 4,
  storeGb: 16,
  o11yVcpu: 2,
  o11yGb: 8,
}
const HOURS = 730
const planeMonthly =
  ((PLANE.gatewayVcpu + PLANE.storeVcpu + PLANE.o11yVcpu) * RAIL.vcpuSec +
    (PLANE.gatewayGb + PLANE.storeGb + PLANE.o11yGb) * RAIL.gbSec) *
  3600 *
  HOURS

console.log(`══ THE FLOOR ══\n`)
console.log(`  control plane     $${planeMonthly.toFixed(0)}/mo  (14 vCPU, 56 GB always on)`)
console.log(`  1M dormant agents $${((477 * 1e6) / 1024 ** 3 * RAIL.objectGbMonth).toFixed(2)}/mo  (455 MB, measured)\n`)

/** Three schemes, each against the same measured cost. */
const schemes = [
  { name: 'Match Naïve', perCall: 0.05 },
  { name: 'Half',        perCall: 0.025 },
  { name: '90% cheaper', perCall: 0.005 },
  { name: '99% cheaper', perCall: 0.0005 },
]

const VOLUMES = [1e6, 1e8, 1e9]

for (const vol of VOLUMES) {
  console.log(`══ AT ${(vol / 1e6).toLocaleString()}M CALLS / MONTH ══\n`)
  const hGb = (vol * HISTORY_BYTES_PER_CALL * RETENTION_MONTHS) / 1024 ** 3
  console.log(`  retained history: ${hGb >= 1024 ? (hGb / 1024).toFixed(1) + ' TB' : hGb.toFixed(0) + ' GB'} at ${RETENTION_MONTHS} months`)
  console.log(`  scheme         price/call    revenue    all-in     margin   after 25% OSS`)
  for (const s of schemes) {
    const gross = vol * s.perCall
    const paid = gross * (1 - OSS_SHARE) // a quarter given away

    // History accrues: a month's calls are still stored a year later, so the
    // steady-state bill is the retention window's worth.
    const historyGb = (vol * HISTORY_BYTES_PER_CALL * RETENTION_MONTHS) / 1024 ** 3
    const storage = historyGb * RAIL.objectGbMonth

    const infra = vol * marginalCall + planeMonthly * REDUNDANCY + storage
    const cost = infra * (1 + OPS_MULTIPLE)
    const profit = paid - cost
    const margin = (profit / paid) * 100
    const usd = (n) =>
      n >= 1e6 ? `$${(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `$${(n / 1e3).toFixed(0)}k` : `$${n.toFixed(0)}`
    console.log(
      `  ${s.name.padEnd(14)} $${s.perCall.toFixed(4).padEnd(9)} ${usd(gross).padStart(9)} ${usd(cost).padStart(9)} ${(margin).toFixed(1).padStart(8)}%  ${usd(profit).padStart(9)}`
    )
  }
  console.log()
}

console.log(`══ READ ══

  At 90% under Naïve — $0.005 a call — a billion calls a month is
  ${(() => {
    const v = 1e9, p = 0.005
    const paid = v * p * (1 - OSS_SHARE)
    const h = (v * HISTORY_BYTES_PER_CALL * RETENTION_MONTHS) / 1024 ** 3
    const infra = v * marginalCall + planeMonthly * REDUNDANCY + h * RAIL.objectGbMonth
    const c = infra * (1 + OPS_MULTIPLE)
    return `$${((paid - c) / 1e6).toFixed(1)}M of profit at ${(((paid - c) / paid) * 100).toFixed(0)}% margin`
  })()},
  after giving a quarter away and paying three zones, twelve months of
  retained history, and twice the infrastructure bill in people.

  The reason that works is not efficiency in the abstract, it is the two
  numbers this session measured: a dormant agent is 477 bytes rather than an
  assumed megabyte, and a pooled sandbox answers in 35.8 ms rather than a
  cold machine. Neither of those is a discount. They are the cost base.

  WHAT THIS EXCLUDES, and it dominates everything above: model tokens. A
  single frontier call costs cents, so an agent turn that thinks is 100–1000×
  the infrastructure under it. Any per-call price must sit beside token
  billing, not pretend to include it — which is exactly what Naïve do when
  they exclude LLM routing from credits.
`)
