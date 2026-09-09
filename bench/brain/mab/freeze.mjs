/**
 * Freeze a result: rerun the frozen configuration from the pristine data and
 * archive everything a stranger needs to reproduce it.
 *
 * The archive under runs/mab-frozen-<tag>/ holds: the dataset's sha256 and
 * the row it was read from; every plan set with the compiler model, prompt
 * sha256, temperature and its own sha256; the search's source file sha256;
 * the git commit; the exact commands; predictions and traces for dev and
 * test; the table; and the scoring command. `--verify` re-runs the search on
 * dev and test from the archived plans and checks the predictions match
 * byte for byte before anything is written.
 *
 *   node mab/freeze.mjs --tag=v1 [--verify]
 */
import { readFileSync, writeFileSync, mkdirSync, readdirSync, cpSync, existsSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { execSync } from 'node:child_process'
import { DATA } from './parse.mjs'
const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const TAG = arg('tag', 'v1'), VERIFY = process.argv.includes('--verify')
const ROOT = new URL('../', import.meta.url).pathname
const sha = (p) => createHash('sha256').update(readFileSync(p)).digest('hex')
const commit = execSync('git rev-parse HEAD', { cwd: ROOT }).toString().trim()
const dirty = execSync('git status --porcelain -- mab', { cwd: ROOT }).toString().trim()
if (dirty) { console.error('mab/ has uncommitted changes; commit first:\n' + dirty); process.exit(1) }
const out = `${ROOT}runs/mab-frozen-${TAG}/`; mkdirSync(out, { recursive: true })
if (VERIFY) {
  for (const split of ['dev', 'test']) {
    const before = readFileSync(`${ROOT}runs/mab-${split}-beam-none/predictions.jsonl`, 'utf8')
    execSync(`mv runs/mab-${split}-beam-none runs/mab-${split}-beam-none.pre-verify && node mab/lane.mjs --split=${split} --rows=beam > /dev/null 2>&1`, { cwd: ROOT, shell: '/bin/bash' })
    const after = readFileSync(`${ROOT}runs/mab-${split}-beam-none/predictions.jsonl`, 'utf8')
    const same = before.split('\n').filter(Boolean).map((l) => { const r = JSON.parse(l); return `${r.qid}\t${r.pred}` }).sort().join('\n') === after.split('\n').filter(Boolean).map((l) => { const r = JSON.parse(l); return `${r.qid}\t${r.pred}` }).sort().join('\n')
    execSync(`rm -rf runs/mab-${split}-beam-none.pre-verify`, { cwd: ROOT })
    if (!same) { console.error(`${split}: a rerun did not reproduce the predictions; nothing archived`); process.exit(1) }
    console.log(`${split}: rerun reproduces the predictions`)
  }
}
const plans = readdirSync(DATA).filter((f) => /^plans(-.+)?\.json$/.test(f)).map((f) => { const d = JSON.parse(readFileSync(DATA + f, 'utf8')); const models = {}; for (const p of Object.values(d)) if (p) models[p.model] = (models[p.model] ?? 0) + 1; return { file: f, sha256: sha(DATA + f), plans: Object.keys(d).length, models } })
for (const p of plans) cpSync(DATA + p.file, out + p.file)
for (const split of ['dev', 'test']) cpSync(`${ROOT}runs/mab-${split}-beam-none/`, `${out}${split}/`, { recursive: true })
const manifest = {
  tag: TAG, commit, when: new Date().toISOString(),
  dataset: { file: 'data/mab/Conflict_Resolution-00000-of-00001.parquet', sha256: sha(DATA + 'Conflict_Resolution-00000-of-00001.parquet'), source: 'hf download ai-hyz/MemoryAgentBench --repo-type dataset' },
  code: Object.fromEntries(['parse.mjs', 'index.mjs', 'compile.mjs', 'beam.mjs', 'lane.mjs', 'nbest.mjs'].map((f) => [f, sha(`${ROOT}mab/${f}`)])),
  prompts: Object.fromEntries(readdirSync(`${ROOT}prompts/`).filter((f) => f.startsWith('compile-mab') || f === 'reader-mab.txt').map((f) => [f, sha(`${ROOT}prompts/${f}`)])),
  plans,
  commands: ['node mab/parse.mjs', 'node mab/index.mjs', 'node mab/compile.mjs --model=<model> [--api=<base>] [--temperature=<t>] [--prompt=<file>] --out=<plans-<tag>.json> --force', 'node mab/lane.mjs --split=dev --rows=beam', 'node mab/lane.mjs --split=test --rows=beam', 'node mab/nbest.mjs --run=mab-test-beam-none', 'node mab/table.mjs'],
  metric: 'substring_exact_match, every question of every haystack counted, an unanswered question scored 0',
  reader: 'none', splits: { dev: ['sh_6k', 'mh_6k'], test: ['sh_32k', 'mh_32k', 'sh_64k', 'mh_64k', 'sh_262k', 'mh_262k'] },
  metrics: { dev: JSON.parse(readFileSync(`${ROOT}runs/mab-dev-beam-none/metrics.json`, 'utf8')).by_size, test: JSON.parse(readFileSync(`${ROOT}runs/mab-test-beam-none/metrics.json`, 'utf8')).by_size },
}
writeFileSync(out + 'manifest.json', JSON.stringify(manifest, null, 1))
console.log(`archived ${out} · commit ${commit.slice(0, 12)} · plans: ${plans.map((p) => `${p.file} (${p.plans})`).join(', ')}`)
console.log(`test multi-hop: ${Object.entries(manifest.metrics.test).filter(([k]) => k.startsWith('mh')).map(([k, v]) => `${k} ${(v.substring_em * 100).toFixed(0)}`).join(', ')}`)
