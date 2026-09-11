/**
 * The platform, as this lane needs it. Seven operations, all of them existing
 * surface, none of them invented here.
 *
 *   agent    POST/PATCH /v1/agent          the arm, carrying the bench's budget
 *   work     GET  /v1/git/issues           the period's tasks
 *   run      POST /v1/agent/:ref/run       one task
 *   spend    GET  /v1/agent/:ref/spend     what the arm has spent this period
 *   audit    GET  /v1/audit                where the trail stands
 *   changes  GET  /v1/git/changes          how the market answered
 *   record   POST /v1/research/{benchmarks,runs,studies}
 *
 * THE BUDGET IS THE PLATFORM'S, NOT THIS FILE'S. An agent carries
 * cap_micro_usd per period and max_task_micro_usd per run, and every model call
 * it makes is checked against both before it is made (apps/agents/budget.go).
 * A second ceiling here would be a second opinion about money, so there is none:
 * the runner dispatches, and a task the platform refuses comes back refused.
 *
 * local.mjs answers the same seven for a dry run. Everything above them — the
 * runner, the readers, the arithmetic — is written once and does not know which
 * of the two it is holding.
 */
import { readFileSync } from 'node:fs'

export const API = process.env.HANZO_API ?? 'https://api.hanzo.ai/v1'

/** HANZO_API_KEY, else the token `hanzo auth login` saved. Never printed, never written to a run. */
export function credential() {
  const read = (f) => { try { return JSON.parse(readFileSync(`${process.env.HOME}/.hanzo/${f}`, 'utf8')) } catch { return {} } }
  const key = process.env.HANZO_API_KEY ?? read('credentials.json').access_token ?? read('config.json').apiKey
  if (!key) throw new Error('no credential: run `hanzo auth login` or set HANZO_API_KEY')
  return key
}

/**
 * One call. `allow` names the non-2xx answers that are results rather than
 * failures: 402 is the budget refusing, and 502 from a run is the platform
 * handing back the recorded run whose inference failed. Both are things the
 * bench has to write down, and neither should end the invocation.
 */
async function call(key, method, path, { body, query, project, allow = [] } = {}) {
  const url = new URL(API + path)
  for (const [k, v] of Object.entries(query ?? {})) if (v != null) url.searchParams.set(k, String(v))
  const headers = { authorization: `Bearer ${key}`, accept: 'application/json' }
  if (project) headers['x-project-id'] = project
  if (body) headers['content-type'] = 'application/json'
  const r = await fetch(url, { method, headers, body: body ? JSON.stringify(body) : undefined })
  const text = await r.text()
  let j; try { j = JSON.parse(text) } catch { j = null }
  if (r.ok || allow.includes(r.status)) return { status: r.status, body: j }
  throw Object.assign(new Error(`${method} ${path}: ${r.status} ${text.replace(/\s+/g, ' ').slice(0, 200)}`), { status: r.status })
}

/**
 * open returns the live platform for one run. `project` is the research
 * sub-scope every record is filed under, so a bench's runs sit together and
 * apart from the retrieval lane's.
 */
export function open({ project = 'market' } = {}) {
  const key = credential()
  const c = (m, p, o) => call(key, m, p, { ...o, project })

  return {
    live: true,
    api: new URL(API).host,

    /**
     * The arm, with this bench's budget on it. Created once and thereafter
     * patched, so re-running a bench never issues a second agent with a second
     * cap — one arm, one ledger, one period counter.
     */
    async agent(bench, arm) {
      const name = `market-${bench.id}-${arm.id}`
      const budget = {
        cap_micro_usd: bench.budget.cap_micro_usd,
        max_task_micro_usd: bench.budget.max_task_micro_usd,
        period: bench.duration.period,
      }
      const got = await c('GET', `/agent/${encodeURIComponent(name)}`).catch((e) => { if (e.status === 404) return null; throw e })
      if (got) {
        await c('PATCH', `/agent/${encodeURIComponent(name)}`, { body: budget })
        return { ref: name, ...budget }
      }
      await c('POST', '/agent', {
        body: {
          name, model: arm.model, instructions: bench.firm.brief,
          description: `${bench.title} · ${arm.id}`, tools: bench.tools,
          executionMode: arm.stack, ...budget,
        },
      })
      return { ref: name, ...budget }
    },

    /**
     * The period's work. `taken` is every task id this run has already used, so
     * the queue's "each taken once" holds across invocations without relying on
     * an offset — a real queue gains and loses items while the run is running,
     * and an offset into it points somewhere different every day.
     */
    async work(bench, period, n, taken = []) {
      const { body } = await c('GET', '/git/issues', { query: { repo: bench.firm.surface, label: bench.firm.queue, state: 'open', limit: n + taken.length } })
      const seen = new Set(taken)
      return (body?.issues ?? [])
        .map((i) => ({ id: String(i.id ?? i.number), title: i.title ?? '', body: i.body ?? '' }))
        .filter((t) => !seen.has(t.id))
        .slice(0, n)
    },

    /**
     * One task. Two answers that are not failures: a 402 is the budget refusing,
     * and a 502 carries the recorded run whose inference failed. Both are
     * results the bench writes down; neither ends the invocation.
     */
    async run(ref, brief) {
      const t0 = Date.now()
      const { status, body } = await c('POST', `/agent/${encodeURIComponent(ref)}/run`, { body: { input: brief }, allow: [402, 502] })
      if (status === 402) return { refused: body?.error?.code ?? body?.code ?? 'budget_exceeded', micro_usd: 0, ms: Date.now() - t0 }
      return {
        run_id: body?.id ?? '', output: body?.output ?? '', error: body?.error ?? '',
        micro_usd: body?.micro_usd ?? body?.microUsd ?? 0, ms: body?.duration_ms ?? Date.now() - t0,
      }
    },

    /** What the arm has spent this period, from the ledger that enforced the cap. */
    async spend(ref) {
      const { body } = await c('GET', `/agent/${encodeURIComponent(ref)}/spend`, { query: { by: 'component' } })
      return body ?? {}
    },

    /** Where the org's audit trail stands. A task records the span it moved through. */
    async audit() {
      const { body } = await c('GET', '/audit', { query: { pageSize: 1 } })
      return { seq: body?.data?.[0]?.seq ?? 0 }
    },

    /** How the proposed changes stand now — the market's answer, read at the window. */
    async changes(ids) {
      if (!ids.length) return []
      const { body } = await c('GET', '/git/changes', { query: { ids: ids.join(',') } })
      return (body?.changes ?? []).map((x) => ({ id: String(x.id), merged: Boolean(x.merged), checks: x.checks ?? 'unknown' }))
    },

    /** Into /v1/research, where a number has a definition, an execution and a completion. */
    async record({ benchmark, run, study }) {
      if (benchmark) await c('POST', '/research/benchmarks', { body: { benchmarks: [benchmark] } })
      if (study) await c('POST', '/research/studies', { body: { studies: [study] } })
      if (run) await c('POST', '/research/runs', { body: { runs: [run] } })
      return { ok: true }
    },
  }
}
