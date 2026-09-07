/**
 * What a fleet costs to keep, from the measurement rather than from a model.
 *
 * The only input here that is ours is the one we measured: 477 bytes of state
 * per dormant agent, from writing a million of them (fleet.mjs). Everything
 * else is a published list price, named where it is used.
 */

const MEASURED_BYTES_PER_AGENT = 477 // fleet.mjs, 1,000,000 agents, 454.6 MB
const AGENTS = 1_000_000

// Published list prices, September 2026.
const S3_GB_MONTH = 0.023 // AWS S3 Standard, first 50 TB
const EBS_GB_MONTH = 0.08 // AWS gp3
const VCPU_HOUR = 0.04048 // AWS t4g.medium on-demand, us-east-1, ÷2 vCPU
const GB_RAM_HOUR = 0.004445 // same instance, ÷4 GB
const HOURS = 730

const gb = (bytes) => bytes / 1024 ** 3
const usd = (n) =>
  n >= 1000 ? `$${Math.round(n).toLocaleString()}` : n >= 1 ? `$${n.toFixed(2)}` : `$${n.toFixed(4)}`

console.log(`\n1,000,000 agents · what each shape costs to keep them\n`)

// ── Ours, dormant. The state is the whole residency cost.
const ourBytes = MEASURED_BYTES_PER_AGENT * AGENTS
const ourStorage = gb(ourBytes) * S3_GB_MONTH
console.log(`Hanzo Base · dormant          ${usd(ourStorage).padStart(12)}   ${gb(ourBytes).toFixed(2)} GB, MEASURED`)
console.log(`  477 bytes/agent × 1M = ${(ourBytes / 1024 ** 2).toFixed(0)} MB. Object storage at list.`)
console.log(`  Nothing is reserved: a dormant agent is a row, not a machine.\n`)

// ── Naïve's own published figure and the assumption under it.
const naiveBytes = 1024 ** 2 * AGENTS // "~1 MB state", their number
console.log(`Naïve Vetta · serverless      ${'$44k–60k'.padStart(12)}   ${gb(naiveBytes).toFixed(0)} GB, MODELLED`)
console.log(`  Their published row, marked "MODELLED, NEVER BILLED".`)
console.log(`  ~1 MB of state per agent — ${(1024 ** 2 / MEASURED_BYTES_PER_AGENT).toFixed(0)}× what we measured for ours.\n`)

// ── A machine per tenant, which is what the self-host shapes are.
const perTenant = { vcpu: 2, ram: 4 }
const tenants = AGENTS / 10 // their basis: 100,000 tenants × 10 agents
const boxes = tenants * (perTenant.vcpu * VCPU_HOUR + perTenant.ram * GB_RAM_HOUR) * HOURS
console.log(`A box per tenant · 2 vCPU/4 GB ${usd(boxes).padStart(11)}   100,000 boxes billed 24/7`)
console.log(`  The shape every self-hosted harness has: the box runs whether the agent does or not.\n`)

// ── What the storage floor actually is on each of the three.
console.log(`Storage alone, at list price:`)
for (const [name, bytes, rate] of [
  ['  Hanzo, measured      ', ourBytes, S3_GB_MONTH],
  ['  Naïve, their model   ', naiveBytes, S3_GB_MONTH],
  ['  Hanzo on block store ', ourBytes, EBS_GB_MONTH],
]) {
  console.log(`${name} ${usd(gb(bytes) * rate).padStart(10)}  (${gb(bytes).toFixed(2)} GB)`)
}

console.log(`
What this does and does not say.

  It says: keeping a million agents RESIDENT is 454 MB and $0.01 of storage a
  month. Residency is not the bill. Dormancy is the only thing this measures,
  and it is the only thing Naïve claims to win on — their own page says so:
  "The advantage is dormancy, and only dormancy."

  It does not say: what the fleet costs while it WORKS. Model tokens, tool
  calls and the compute of a running turn fall on every row alike and dwarf
  all of the above. A fleet at 5% duty is a compute bill with a rounding error
  of storage attached.

  The number that matters for a price list is therefore not $/agent/month. It
  is $/agent-hour-awake, plus a storage line small enough to give away.
`)
