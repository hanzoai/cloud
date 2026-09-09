/**
 * The same store in another embedding space.
 *
 * LoCoMo-Conv fixes all-MiniLM-L6-v2 for every memory system it compares, so
 * a retrieval number of ours is comparable to its table only when the dense
 * generator uses that model too. This re-embeds the turns, the questions and
 * the fact layer from the existing store's texts — nothing else changes — into
 * brain-vectors-<name>.json and facts-vectors-<name>.json, which context.mjs
 * loads with --embed=<name>.
 *
 *   node embed-store.mjs --model=all-minilm --name=minilm
 */
import { readFileSync, writeFileSync } from 'node:fs'
import { embed } from './llm.mjs'

const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.slice(k.length + 3) : d }
const MODEL = arg('model', 'all-minilm'), NAME = arg('name', 'minilm')
const store = JSON.parse(readFileSync(new URL('./brain-vectors.json', import.meta.url), 'utf8'))
const facts = JSON.parse(readFileSync(new URL('./facts-vectors.json', import.meta.url), 'utf8'))

async function all(texts, label) {
  const out = []
  for (let i = 0; i < texts.length; i += 64) { out.push(...(await embed(texts.slice(i, i + 64), MODEL))); process.stdout.write(`\r  ${label} ${Math.min(i + 64, texts.length)}/${texts.length}`) }
  process.stdout.write('\n'); return out
}
const store2 = [], facts2 = []
for (const [ci, c] of store.entries()) {
  console.log(`conversation ${ci + 1}/${store.length}`)
  const tv = await all(c.turns.map((t) => t.text), 'turns'), qv = await all(c.qa.map((q) => q.question), 'questions')
  store2.push({ turns: c.turns.map((t, i) => ({ id: t.id, text: t.text, v: tv[i] })), qa: c.qa.map((q, i) => ({ ...q, v: qv[i] })) })
  const fv = await all((facts[ci] ?? []).map((f) => f.text), 'facts')
  facts2.push((facts[ci] ?? []).map((f, i) => ({ ...f, v: fv[i] })))
}
writeFileSync(new URL(`./brain-vectors-${NAME}.json`, import.meta.url), JSON.stringify(store2))
writeFileSync(new URL(`./facts-vectors-${NAME}.json`, import.meta.url), JSON.stringify(facts2))
console.log(`wrote brain-vectors-${NAME}.json and facts-vectors-${NAME}.json with ${MODEL} (dim ${store2[0].turns[0].v.length})`)
