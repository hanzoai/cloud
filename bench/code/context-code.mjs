/**
 * The Context engine on code: RepoBench-R, Python, both settings.
 *
 * Each item is a completion point: the file's imports, the code above the
 * point, and k candidate snippets from other files of the same repository,
 * one of which is the snippet the next line needs. The ranking has the shape
 * of bench/brain/context.mjs — bounded generators, one linear scorer, every
 * contribution recorded — with typed links in place of facts:
 *
 *   dense    cosine between the last lines above the point and the candidate
 *   lexical  BM25 over the candidates, queried with the last three lines
 *   unused   the candidate DEFINES a name the file imports and the code above
 *            has not mentioned yet — in the cross-file-first setting (cff) the
 *            next line is that first use, so this is the prior
 *   used     the candidate defines an imported name the code above already
 *            mentions — a demotion in cff, a promotion in cross-file-random
 *            (cfr), where the module was used before; the sign is chosen on dev
 *
 * The link generators are regexes over `import`, `from … import`, `def`,
 * `class` and identifiers; no model. Nothing reads `next_line`: it is the answer.
 *
 *   node context-code.mjs --setting=cff|cfr --split=dev|test --rows=all|<names> [--embed=all-minilm] [--write] [--frozen]
 *   node context-code.mjs --setting=cff --sweep      # dev only; writes ablations/frozen-<setting>.json
 *   node context-code.mjs --setting=cff --split=test --embed-only   # fill the embedding cache and stop
 */
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { execSync } from 'node:child_process'
import { rank as rankScore, ci, pct, quantile } from '../brain/metrics.mjs'
import { embed } from '../brain/llm.mjs'

const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.slice(k.length + 3) : d }
const has = (k) => process.argv.includes(`--${k}`)
const here = new URL('.', import.meta.url)
export const EMBED = arg('embed', process.env.EMBED ?? 'all-minilm')
export const SETTING = arg('setting', 'cff')
const KS = [1, 3, 5]

/** The declared splits: dev = train_easy[0:100] + train_hard[0:100]; test = test_easy[0:250] + test_hard[0:250]. */
export function load(split, setting = SETTING) { return readFileSync(new URL(`./data/repobench-r/python_${setting}_${split}.jsonl`, here), 'utf8').trim().split('\n').map((l) => JSON.parse(l)) }

// ── text
export const lastLines = (s, n) => String(s).split('\n').filter((l) => l.trim()).slice(-n).join('\n')
const STOP = new Set('the a an and or of to in on at for with is are was were be been it its this that i you he she they we my your his her their our me him them us do did does have has had not so but if then than as about from by up out into over just like really also very what when where who how why which did self none true false return def class import pass'.split(' '))
export const toks = (s) => String(s).replace(/([a-z])([A-Z])/g, '$1 $2').toLowerCase().replace(/[^a-z0-9_ ]+/g, ' ').split(/[\s_]+/).filter((w) => w.length > 1 && !STOP.has(w))

/** Names the file imports (as bound in the file), and the modules they come from. */
export function imported(imports) {
  const names = new Set(), modules = new Set()
  for (const line of String(imports).split('\n')) {
    let m = line.match(/^\s*from\s+([\w.]+)\s+import\s+(.+)$/)
    if (m) { modules.add(m[1].replace(/^\.+/, '')); for (const part of m[2].replace(/[()]/g, '').split(',')) { const nm = part.trim().split(/\s+as\s+/); const n = (nm[1] ?? nm[0]).trim(); if (/^\w+$/.test(n)) names.add(n) } continue }
    m = line.match(/^\s*import\s+(.+)$/)
    if (m) for (const part of m[1].split(',')) { const nm = part.trim().split(/\s+as\s+/); const n = (nm[1] ?? nm[0]).trim().split('.').pop(); if (/^\w+$/.test(n)) { names.add(n); modules.add(nm[0].trim()) } }
  }
  return { names, modules }
}
/** Every identifier the code above the point mentions. */
export const mentioned = (code) => new Set([...String(code).matchAll(/\b([A-Za-z_]\w*)\b/g)].map((m) => m[1]))
/** Names a candidate defines at any indentation, plus module-level assignments. */
export function defined(snippet) { const o = new Set(); for (const m of String(snippet).matchAll(/^\s*(?:async\s+)?(?:def|class)\s+([A-Za-z_]\w*)/gm)) o.add(m[1]); for (const m of String(snippet).matchAll(/^([A-Za-z_]\w*)\s*=/gm)) o.add(m[1]); return o }

/** BM25 over one item's candidates. */
class BM25 {
  constructor(docs, k1 = 1.2, b = 0.75) { this.docs = docs.map(toks); this.k1 = k1; this.b = b; this.n = docs.length; this.avg = this.docs.reduce((a, d) => a + d.length, 0) / Math.max(1, this.n)
    this.df = new Map(); for (const d of this.docs) for (const w of new Set(d)) this.df.set(w, (this.df.get(w) ?? 0) + 1); this.tf = this.docs.map((d) => { const m = new Map(); for (const w of d) m.set(w, (m.get(w) ?? 0) + 1); return m }) }
  scores(query) { const q = toks(query), out = new Float64Array(this.n)
    for (const w of new Set(q)) { const df = this.df.get(w); if (!df) continue; const idf = Math.log(1 + (this.n - df + 0.5) / (df + 0.5))
      for (let i = 0; i < this.n; i++) { const tf = this.tf[i].get(w); if (!tf) continue; const dl = this.docs[i].length; out[i] += idf * (tf * (this.k1 + 1)) / (tf + this.k1 * (1 - this.b + this.b * dl / this.avg)) } }
    return out }
}

// ── embeddings: local, cached by content hash, one request per 64 texts on the shared GPU
const sha = (s) => createHash('sha256').update(s).digest('hex').slice(0, 24)
const cacheFile = new URL(`./data/embed-cache-${EMBED.replace(/[^a-z0-9]+/gi, '-')}.json`, here)
const cache = new Map(existsSync(cacheFile) ? Object.entries(JSON.parse(readFileSync(cacheFile, 'utf8'))) : [])
const dot = (a, b) => { let s = 0; for (let i = 0; i < a.length; i++) s += a[i] * b[i]; return s }
const unit = (v) => { const n = Math.sqrt(dot(v, v)) || 1; return v.map((x) => x / n) }
export const queryText = (it) => lastLines(it.code, 5)
export const candText = (s) => s.slice(0, 1200)
export async function embedAll(items) {
  const texts = new Set(); for (const it of items) { texts.add(queryText(it)); for (const s of it.context) texts.add(candText(s)) }
  const todo = [...texts].filter((t) => !cache.has(sha(t))); const t0 = Date.now()
  const flush = () => { if (existsSync(cacheFile)) { try { for (const [k, v] of Object.entries(JSON.parse(readFileSync(cacheFile, 'utf8')))) if (!cache.has(k)) cache.set(k, v) } catch {} } writeFileSync(cacheFile, JSON.stringify(Object.fromEntries(cache))) }
  for (let i = 0; i < todo.length; i += 64) { const chunk = todo.slice(i, i + 64); const vs = await embed(chunk, EMBED); chunk.forEach((t, j) => cache.set(sha(t), unit(vs[j]))); process.stderr.write(`\r  embedded ${Math.min(i + 64, todo.length)}/${todo.length}`); if ((i / 64) % 10 === 9) flush() }
  if (todo.length) { flush(); process.stderr.write('\n') }
  return { embedded: todo.length, ms: Date.now() - t0 }
}
const vec = (t) => cache.get(sha(t))

// ── the scorer
export const DEFAULT_W = { lex: 0.5, unused: 1.0, used: SETTING === 'cfr' ? 0.5 : -0.5 }
export function retrieve(it, cfg) {
  const t0 = process.hrtime.bigint(), W = { ...DEFAULT_W, ...(cfg.w ?? {}) }
  const imp = imported(it.imports), men = mentioned(it.code), n = it.context.length
  const dense = cfg.dense ? (() => { const q = vec(queryText(it)); return it.context.map((s) => { const v = vec(candText(s)); return q && v ? dot(q, v) : 0 }) })() : new Array(n).fill(0)
  const lex = cfg.lex ? (() => { const s = new BM25(it.context).scores(lastLines(it.code, 3)); const mx = Math.max(...s) || 1; return Array.from(s, (x) => x / mx) })() : new Array(n).fill(0)
  const contrib = it.context.map((s, i) => {
    const c = { dense: dense[i], lex: lex[i], unused: 0, used: 0 }
    if (cfg.links) { const defs = [...defined(s)].filter((d) => imp.names.has(d)); if (defs.some((d) => !men.has(d))) c.unused = 1; if (defs.some((d) => men.has(d))) c.used = 1 }
    c.score = c.dense + W.lex * c.lex + W.unused * c.unused + W.used * c.used
    return c })
  const order = [...contrib.keys()].sort((a, b) => contrib[b].score - contrib[a].score || a - b)
  return { order, contrib, ms: Number(process.hrtime.bigint() - t0) / 1e6 }
}

export const ROWS = {
  'dense only': { dense: true },
  'BM25 only': { lex: true },
  'typed links only (no model)': { links: true, lex: true, w: { lex: 0.05 } },
  'dense + BM25': { dense: true, lex: true },
  'dense + typed links': { dense: true, links: true },
  'BM25 + typed links (no model)': { lex: true, links: true },
  'full: dense + BM25 + typed links': { dense: true, lex: true, links: true },
}

export async function evaluate(items, cfg, o = {}) {
  if (cfg.dense) await embedAll(items)
  const rows = [], traces = []
  for (const it of items) { const r = retrieve(it, cfg); const ids = r.order.map(String); const m = rankScore(ids, [String(it.gold)], KS)
    rows.push({ id: it.id, level: it.level, ...m, examined: it.context.length, ms: r.ms })
    if (o.trace) traces.push({ id: it.id, gold: it.gold, order: r.order.slice(0, 5), contrib: r.order.slice(0, 5).map((i) => Object.fromEntries(Object.entries(r.contrib[i]).map(([k, v]) => [k, +(+v).toFixed(4)]))) }) }
  const by = { all: rows, easy: rows.filter((r) => r.level === 'easy'), hard: rows.filter((r) => r.level === 'hard') }
  const summary = {}
  for (const [name, rs] of Object.entries(by)) { if (!rs.length) continue; const s = { n: rs.length }
    for (const k of KS) { s[`recall@${k}`] = ci(rs.map((r) => r[`recall@${k}`])); s[`ndcg@${k}`] = ci(rs.map((r) => r[`ndcg@${k}`])) }
    s.mrr = ci(rs.map((r) => r.mrr)); s.examined = rs.reduce((a, r) => a + r.examined, 0) / rs.length; s.p50 = quantile(rs.map((r) => r.ms), 0.5); s.p95 = quantile(rs.map((r) => r.ms), 0.95); summary[name] = s }
  return { summary, rows, traces }
}
const commit = () => { try { return execSync('git rev-parse --short=12 HEAD', { cwd: here.pathname }).toString().trim() } catch { return 'unknown' } }
const DATA = { cff: 'tianyang/repobench-r data/python_cff.gz sha256 3ccc13b41807e061…', cfr: 'tianyang/repobench-r data/python_cfr.gz sha256 1f83ca53ee2f0662…' }

const MAIN = process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())
if (MAIN) {
  mkdirSync(new URL('./ablations/', here), { recursive: true }); mkdirSync(new URL('./runs/', here), { recursive: true })
  const split = arg('split', 'dev')
  if (has('embed-only')) { const r = await embedAll(load(split)); console.log(`embedded ${r.embedded} new texts for ${SETTING}/${split} in ${(r.ms / 1000).toFixed(0)} s`); process.exit(0) }
  if (has('sweep')) { // dev only, the full row: BM25 weight, the unused-import prior, the sign and size of the used-import term; objective MRR
    const items = load('dev'); await embedAll(items); const table = []; let best = null
    for (const lex of [0, 0.2, 0.5, 1, 2]) for (const unused of [-1, -0.5, 0, 0.5, 1, 2]) for (const used of [-1, -0.5, 0, 0.5, 1, 2]) {
      const w = { lex, unused, used }; const { summary } = await evaluate(items, { ...ROWS['full: dense + BM25 + typed links'], w }); const obj = summary.all.mrr.mean
      table.push({ w, mrr: obj, recall1: summary.all['recall@1'].mean }); if (!best || obj > best.mrr + 1e-9) best = { w, mrr: obj } }
    const frozen = { setting: SETTING, row: 'full: dense + BM25 + typed links', embed: EMBED, w: best.w, objective: best.mrr, commit: commit(), evaluations: table.length }
    writeFileSync(new URL(`./ablations/sweep-${SETTING}.json`, here), JSON.stringify({ setting: SETTING, objective: 'MRR on dev', evaluations: table }, null, 1)); writeFileSync(new URL(`./ablations/frozen-${SETTING}.json`, here), JSON.stringify(frozen, null, 1))
    console.log(`frozen on dev (${SETTING}): w=${JSON.stringify(best.w)} MRR ${pct(best.mrr)} over ${table.length} evaluations · commit ${frozen.commit}`); process.exit(0) }
  const items = load(split), want = arg('rows', 'all'), names = want === 'all' ? Object.keys(ROWS) : want.split(',').map((s) => s.trim())
  const frozenFile = new URL(`./ablations/frozen-${SETTING}.json`, here), frozen = has('frozen') && existsSync(frozenFile) ? JSON.parse(readFileSync(frozenFile, 'utf8')) : null
  console.log(`\n── RepoBench-R python ${SETTING} · split ${split} (${items.length}) · embed ${EMBED}${frozen ? ` · frozen ${frozen.commit} w=${JSON.stringify(frozen.w)}` : ''} ──`)
  console.log(`${'row'.padEnd(34)} R@1    R@3    R@5    MRR   nDCG@5   easy R@1  hard R@1  cands  p50ms`)
  for (const name of names) { const base = ROWS[name]; const cfg = frozen && base.links ? { ...base, w: { ...frozen.w, ...(base.w ?? {}) } } : base
    const t = await evaluate(items, cfg, { trace: has('write') }); const s = t.summary
    console.log(`${name.padEnd(34)} ${pct(s.all['recall@1'].mean).padStart(5)}  ${pct(s.all['recall@3'].mean).padStart(5)}  ${pct(s.all['recall@5'].mean).padStart(5)}  ${pct(s.all.mrr.mean).padStart(5)}  ${pct(s.all['ndcg@5'].mean).padStart(6)}   ${pct(s.easy?.['recall@1'].mean ?? 0).padStart(6)}    ${pct(s.hard?.['recall@1'].mean ?? 0).padStart(6)}   ${s.all.examined.toFixed(1).padStart(5)}  ${s.all.p50.toFixed(3)}`)
    if (has('write')) { const dir = new URL(`./runs/repobench-r-${SETTING}-${split}-${name.replace(/[^a-z0-9]+/gi, '-').replace(/^-|-$/g, '').toLowerCase()}/`, here); mkdirSync(dir, { recursive: true })
      writeFileSync(new URL('metrics.json', dir), JSON.stringify({ benchmark: 'repobench-r', subset: `python_${SETTING}`, row: name, cfg, split, embed: cfg.dense ? EMBED : null, frozen: frozen && base.links ? { commit: frozen.commit, w: frozen.w } : null, summary: s }, null, 1))
      writeFileSync(new URL('predictions.jsonl', dir), t.rows.map((r) => JSON.stringify(r)).join('\n')); writeFileSync(new URL('traces.jsonl', dir), t.traces.map((x) => JSON.stringify(x)).join('\n'))
      writeFileSync(new URL('meta.json', dir), JSON.stringify({ commit: commit(), embedder: cfg.dense ? EMBED : 'none', data: DATA[SETTING], split: split === 'dev' ? 'train_easy[0:100] + train_hard[0:100]' : 'test_easy[0:250] + test_hard[0:250]', when: new Date().toISOString() }, null, 1)) }
  }
}
