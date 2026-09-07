/**
 * What a dormant agent actually costs, measured.
 *
 * Naïve publishes ~$44–60k/month for 1M agents and marks the row "MODELLED,
 * NEVER BILLED" — 100,000 tenants × 10 agents at ~1 MB of state each. That is
 * arithmetic over an assumption, and they say so, which is honest of them.
 *
 * This writes the agents instead. Hanzo Base is SQLite per organisation, and a
 * dormant agent is a row in `kv(key TEXT PRIMARY KEY, value BLOB, upd INTEGER)`
 * — the same table the local cloud already runs on. So the question "can a
 * laptop hold a million agents" has a number rather than a model, and the two
 * things that matter are both measurable: bytes on disk, and how long it takes
 * to wake one.
 */
import { execFileSync } from 'node:child_process'
import { statSync, rmSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const DIR = process.argv[2] ?? '/tmp/fleet-bench'
const TENANTS = Number(process.env.TENANTS ?? 100_000)
const PER_TENANT = Number(process.env.PER_TENANT ?? 10)
const TOTAL = TENANTS * PER_TENANT

rmSync(DIR, { recursive: true, force: true })
mkdirSync(DIR, { recursive: true })
const db = join(DIR, 'fleet.db')

// Piped, not passed: a 50,000-row INSERT is 20 MB of SQL and argv tops out
// long before that (E2BIG). stdin has no such ceiling.
const sql = (q) =>
  execFileSync('sqlite3', [db], { input: q, encoding: 'utf8', maxBuffer: 1 << 28 }).trim()

/**
 * One dormant agent, as the platform actually stores it: who it is, what model
 * it runs, its standing instruction, the tools it may call, and where its
 * history continues. Not a conversation — a conversation is history, and
 * history is not resident when nobody is talking.
 */
const agent = (i) =>
  JSON.stringify({
    id: `agt_${i.toString(36)}`,
    org: `org_${Math.floor(i / PER_TENANT).toString(36)}`,
    name: `agent-${i}`,
    model: 'zen5',
    instruction:
      'Answer from the org context. Say when a thing is not known. Ask before spending, publishing, or writing to a connected system.',
    tools: ['search', 'fs.read', 'fs.write', 'http', 'sql', 'schedule'],
    schedule: '0 */4 * * *',
    state: 'dormant',
    updated: 1788755000 + i,
    cursor: { thread: `thr_${i.toString(36)}`, seq: i % 512 },
  })

const bytes = () => statSync(db).size
const mb = (n) => (n / 1024 / 1024).toFixed(1)

console.log(`writing ${TOTAL.toLocaleString()} dormant agents (${TENANTS.toLocaleString()} tenants × ${PER_TENANT})`)
console.log(`one agent record: ${agent(1).length} bytes of JSON\n`)

sql('pragma journal_mode=wal; create table kv (key TEXT PRIMARY KEY, value BLOB NOT NULL, upd INTEGER NOT NULL);')

const BATCH = 50_000
const started = Date.now()
for (let start = 0; start < TOTAL; start += BATCH) {
  const end = Math.min(start + BATCH, TOTAL)
  const rows = []
  for (let i = start; i < end; i++) {
    // Escape for a literal; the payload is JSON, so only quotes need doubling.
    rows.push(`('agent/${i}','${agent(i).replace(/'/g, "''")}',${1788755000 + i})`)
  }
  sql(`begin; insert into kv values ${rows.join(',')}; commit;`)
  if ((end / BATCH) % 4 === 0 || end === TOTAL) {
    const secs = (Date.now() - started) / 1000
    process.stdout.write(
      `  ${end.toLocaleString().padStart(9)} agents · ${mb(bytes()).padStart(7)} MB · ${Math.round(end / secs).toLocaleString()}/s\n`
    )
  }
}

const wrote = Date.now() - started
const size = bytes()

console.log(`\n── measured ──`)
console.log(`agents            ${TOTAL.toLocaleString()}`)
console.log(`on disk           ${mb(size)} MB  (${(size / TOTAL).toFixed(0)} bytes per agent)`)
console.log(`write time        ${(wrote / 1000).toFixed(1)}s  (${Math.round(TOTAL / (wrote / 1000)).toLocaleString()} agents/s)`)

// Waking one. A dormant agent is resumed by a point read on its primary key,
// which is the whole cost of resume before the model is called.
const picks = Array.from({ length: 200 }, () => Math.floor(Math.random() * TOTAL))
const t0 = process.hrtime.bigint()
for (const i of picks) sql(`select value from kv where key='agent/${i}';`)
const t1 = process.hrtime.bigint()
const perRead = Number(t1 - t0) / 1e6 / picks.length
console.log(`resume (cold)     ${perRead.toFixed(2)} ms per agent  — includes sqlite3 process spawn`)

// The same reads inside one connection, which is how the server does it.
const many = picks.map((i) => `select value from kv where key='agent/${i}';`).join('\n')
const t2 = process.hrtime.bigint()
sql(many)
const t3 = process.hrtime.bigint()
console.log(`resume (in-proc)  ${(Number(t3 - t2) / 1e6 / picks.length).toFixed(3)} ms per agent`)

console.log(`\nstorage for ${TOTAL.toLocaleString()} dormant agents: ${(size / 1024 / 1024 / 1024).toFixed(2)} GB`)
