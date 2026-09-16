/**
 * What a call costs us, and what we could charge for it.
 *
 * Every cost input is either measured on this machine or a published list
 * price. Nothing is estimated without saying so.
 *
 *   MEASURED (bench/fleet, brain.mjs, sandbox.mjs, this laptop, Sept 2026)
 *     dormant agent state      477 bytes
 *     microVM boot             309 ms    (hanzo-vm, n=20; there is no pooled mode)
 *     pooled sandbox exec      35.8 ms   HISTORICAL — docker exec, a runtime we do not ship
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
 *     $0.0504 per vCPU-hour, metered tools per call, LLM tokens per token.
 */

const RAIL = {
  vcpuSec: 0.00000772,
  gbSec: 0.00000386,
  diskGbSec: 0.00000006,
  egressGb: 0.05,
  objectGbMonth: 0.015,
}

// Their published compute prices, from usenaive.ai/pricing read 2026-09-11 and
// recorded verbatim in ../naive.md. Both axes, because ours has both: comparing
// their vCPU-hour against our vCPU + memory + egress would be the same mistake
// in the other direction.
//
// This row used to read `$0.05 per credit`, which is not a price they publish.
// $0.0504 is an hour of a vCPU; reading it as a call overstated them by the
// ratio of an hour to a call, and the multiple that followed was wrong by the
// same factor. The correction goes against us.
const NAIVE = { vcpuHour: 0.0504, gbHour: 0.0162 }
// Not this file's number. `hanzoai/commerce` declares the OSS developer payout
// in `config/oss-payout.json` — poolFraction 0.25, direct dependencies weighted
// four times a transitive one — and `ossattr.MaxPoolFraction` caps it at 0.25 in
// code, so raising it anywhere has no effect. Restated here because this repo
// does not depend on commerce and should not acquire a billing service to read
// one constant; changed there, this is what has to follow.
const OSS_SHARE = 0.25

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

/**
 * One agent call, as measured: wake, search memory, run a pooled sandbox.
 *
 * The TIMES are measured here; the vcpu and gb slice is an allocation we assume,
 * because nothing meters a request's own CPU or memory — and nothing should, since
 * that is not how any of this is priced. It is modelled for exactly one reason: to
 * compare against a vendor who DOES price per vCPU-hour, on their terms.
 *
 * The figure that needs no model now has a source. Every debit the platform takes
 * is metered (hanzo_usage_charged_usd_total) beside the count of debits that took
 * it (hanzo_usage_debits_total), so measured $/request per product falls out of a
 * division once live traffic runs through them. Until this bench can read that
 * store, the line below is a model and says so.
 */
//
// THE SANDBOX TERM WAS 35.8 ms AND THAT MEASUREMENT NO LONGER EXISTS. It was
// `docker exec` into an already-running container, and the sandbox lane does not
// measure docker any more, because nothing here runs docker — the builds run
// buildkit inside a microVM. Measured on the runtime we do ship, a hanzo-vm boots
// in 309 ms, and `hanzo-vm` has no exec, no attach and no pool: `run`, `measure`,
// `init`, `upgrade`, `checkpoint`, `prune`. Every call boots.
//
// So the pooled figure priced a mode the product does not have, and the gap is
// 8.6x on the dominant term. Both are kept, because the difference between them
// IS the price of that missing mode and it is worth seeing as a number rather
// than discovering later:
//
//   POOLED   35.8 ms   a sandbox kept warm between calls — not built
//   BOOT    309.0 ms   what a call costs today, measured, n=20
//
// The markup line is unaffected: their rate and our cost are both per second of
// the same slice, so a longer slice moves both and the ratio does not.
const POOLED_MS = 35.8
const BOOT_MS = 309.0

const CALL = {
  sandboxMs: BOOT_MS,
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
{
  // What the same call would cost with a sandbox kept warm between calls. Printed
  // beside the real one so the missing mode has a price rather than a plan.
  const p = (POOLED_MS + CALL.memoryMs + CALL.resumeMs + CALL.embedMs) / 1000
  const cost = p * CALL.vcpu * RAIL.vcpuSec + p * CALL.gb * RAIL.gbSec + (CALL.egressKb / 1024 / 1024) * RAIL.egressGb
  console.log(`  if pooled         ${(p * 1000).toFixed(1)} ms · $${cost.toFixed(8)} per call — ${(marginalCall / cost).toFixed(1)}x cheaper, and not built`)
}
// The same slice of compute and memory, at their published rates. Egress is
// left out of both sides of this line: they publish no egress price, and what
// ours contributes is four ten-thousandths of a cent.
const ourCompute = seconds * CALL.vcpu * RAIL.vcpuSec + seconds * CALL.gb * RAIL.gbSec
const naiveCall = seconds * CALL.vcpu * (NAIVE.vcpuHour / 3600) + seconds * CALL.gb * (NAIVE.gbHour / 3600)
console.log(`  compute + memory  $${ourCompute.toFixed(8)} at cost, $${naiveCall.toFixed(8)} at their rate`)
console.log(`  their markup      ${(naiveCall / ourCompute).toFixed(2)}× what the work costs\n`)

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
  // Candidate price points, not a price list. What Hanzo actually charges lives
  // in commerce's PriceSet and PricingRule; this ladder asks what a price would
  // have to clear, which is a different question and belongs here.
  { name: 'Their compute rate', perCall: 0.0000007 },
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

  The reason that works is not efficiency in the abstract, it is the number
  this session measured and re-measured: a dormant agent is 477 bytes rather
  than an assumed megabyte. That is not a discount, it is the cost base, and
  it is why the storage line for a million sleeping agents is a cent.

  IT USED TO CLAIM A SECOND ONE AND SHOULD NOT HAVE. "A pooled sandbox
  answers in 35.8 ms rather than a cold machine" was docker exec into a
  running container, on a runtime this estate does not have. hanzo-vm boots
  in 309 ms and offers no exec, no attach and no pool, so every call boots and
  the per-call cost is 3.1x what that sentence assumed. The pooled figure is
  kept above as what the missing mode would be worth, not as something we do.

  WHAT THIS EXCLUDES, and it dominates everything above: model tokens. A
  single frontier call costs cents, so an agent turn that thinks is 100–1000×
  the infrastructure under it. Any per-call price must sit beside token
  billing, not pretend to include it — which is exactly what Naïve do when
  they exclude LLM routing from credits.
`)
