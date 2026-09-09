// LoCoMo's observations, embedded once: a fact index whose rows cite turns.
import { readFileSync, writeFileSync } from 'node:fs'
const corpus = JSON.parse(readFileSync('locomo10.json','utf8'))
const OLLAMA = process.env.OLLAMA ?? 'http://localhost:11434', MODEL = process.env.EMBED_MODEL ?? 'zenlm/zen-embedding-0.6b'
async function embed(texts){const r=await fetch(`${OLLAMA}/api/embed`,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({model:MODEL,input:texts})});if(!r.ok)throw new Error(await r.text());return (await r.json()).embeddings}
const out=[]
for (const [n,c] of corpus.entries()) {
  const facts=[]
  for (const [sk,v] of Object.entries(c.observation??{})) for (const [speaker,items] of Object.entries(v)) for (const it of items) {
    if (!Array.isArray(it)||it.length<2) continue
    const ids=(Array.isArray(it[1])?it[1]:[it[1]]).filter(x=>typeof x==='string')
    if (ids.length) facts.push({ text: it[0], ids, speaker, sess: sk.replace('_observation','') })
  }
  const vs=[]; for (let i=0;i<facts.length;i+=32) vs.push(...await embed(facts.slice(i,i+32).map(f=>f.text)))
  out.push(facts.map((f,i)=>({...f,v:vs[i]})))
  process.stdout.write(`conversation ${n+1}: ${facts.length} facts\n`)
}
writeFileSync('facts-vectors.json', JSON.stringify(out)); console.log('wrote facts-vectors.json')
