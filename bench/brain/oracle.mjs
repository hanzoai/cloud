// Which link, if followed from the top of a cosine ranking, reaches the multi-hop
// evidence that cosine leaves at rank 67? Measured per link type, no model.
import { readFileSync } from 'node:fs'
const store = JSON.parse(readFileSync('brain-vectors.json','utf8')), corpus = JSON.parse(readFileSync('locomo10.json','utf8'))
const dot=(a,b)=>{let s=0;for(let i=0;i<a.length;i++)s+=a[i]*b[i];return s}, norm=v=>Math.sqrt(dot(v,v)), cos=(a,b)=>dot(a,b)/(norm(a)*norm(b))
const ENT_STOP=new Set(`I I'm I've I'll I'd The A An And But So Yes No Oh Hey Wow Thanks That This It What When Where Who How Why Also Just Really Maybe Sure Okay OK Well Great Good Nice Yeah Haha Lol`.split(/\s+/))
const ents=s=>{const o=new Set();for(const m of s.matchAll(/\b([A-Z][a-zA-Z']{2,})\b/g))if(!ENT_STOP.has(m[1]))o.add(m[1].toLowerCase());return o}
const words=s=>new Set((s.toLowerCase().match(/[a-z0-9']{3,}/g)??[]))
const STOP=new Set('the and for with what when where who how did does which that this her his she they them their from about have had was were are'.split(' '))
const R={n:0, missed:0, adj:0, sameSess:0, entTop:0, entGold:0, rareEnt:0, fact:0, lex:0, anyLink:0}
const entFreq=[]
for (const [n,c] of store.entries()) {
  const raw=corpus[n], speakers=new Set([raw.conversation.speaker_a,raw.conversation.speaker_b].map(s=>s.toLowerCase()))
  const turns=c.turns.map((t,i)=>({...t,i,sess:t.id.split(':')[0],body:t.text.replace(/^[^:]+:\s*/,''),ents:new Set([...ents(t.text.replace(/^[^:]+:\s*/,''))].filter(e=>!speakers.has(e)))}))
  const byId=new Map(turns.map(t=>[t.id,t]))
  const df=new Map(); for(const t of turns)for(const e of t.ents)df.set(e,(df.get(e)??0)+1); entFreq.push(...df.values())
  const factIdx=new Map()  // turn -> fact texts
  for(const v of Object.values(raw.observation??{}))for(const items of Object.values(v))for(const it of items){if(!Array.isArray(it))continue;for(const id of (Array.isArray(it[1])?it[1]:[it[1]]))if(byId.has(id))(factIdx.get(id)??factIdx.set(id,[]).get(id)).push(it[0])}
  for (const q of c.qa) {
    if (q.category!==1||!q.evidence.length) continue
    const ranked=turns.map(t=>({t,s:cos(q.v,t.v)})).sort((a,b)=>b.s-a.s)
    const pos=new Map(ranked.map((r,i)=>[r.t.id,i]))
    const top=ranked.slice(0,5).map(r=>r.t), topEnts=new Set(top.flatMap(t=>[...t.ents]))
    const goldIn=q.evidence.filter(e=>pos.get(e)<20).map(e=>byId.get(e)).filter(Boolean)
    const goldEnts=new Set(goldIn.flatMap(t=>[...t.ents]))
    const qw=[...words(q.question)].filter(w=>!STOP.has(w))
    for (const e of q.evidence) {
      const g=byId.get(e); if(!g) continue; R.n++
      if (pos.get(e)<20) continue
      R.missed++
      let any=false
      const near=[...top,...goldIn]
      if (near.some(t=>Math.abs(t.i-g.i)<=2&&t.sess===g.sess)) {R.adj++;any=true}
      if (near.some(t=>t.sess===g.sess)) {R.sameSess++}
      if ([...g.ents].some(x=>topEnts.has(x))) {R.entTop++;any=true}
      if ([...g.ents].some(x=>goldEnts.has(x))) {R.entGold++;any=true}
      if ([...g.ents].some(x=>(topEnts.has(x)||goldEnts.has(x))&&(df.get(x)??0)<=8)) {R.rareEnt++}
      const facts=factIdx.get(e)??[]; if (facts.some(f=>{const fw=words(f);return qw.filter(w=>fw.has(w)).length>=2})) {R.fact++;any=true}
      const bw=words(g.body); if (qw.filter(w=>bw.has(w)).length>=2) {R.lex++;any=true}
      if (any) R.anyLink++
    }
  }
}
const p=(a,b)=>((a/b)*100).toFixed(1)+'%'
console.log(`multi-hop gold turns: ${R.n} · outside cosine top-20: ${R.missed} (${p(R.missed,R.n)})\n`)
console.log(`of the MISSED gold turns, reachable by…`)
console.log(`  adjacency (±2 turns) to a top-5 or found-gold turn   ${p(R.adj,R.missed)}`)
console.log(`  same session as a top-5 or found-gold turn           ${p(R.sameSess,R.missed)}`)
console.log(`  shares a capitalised entity with top-5               ${p(R.entTop,R.missed)}`)
console.log(`  shares a capitalised entity with found gold          ${p(R.entGold,R.missed)}`)
console.log(`    …where that entity is rare (≤8 turns)              ${p(R.rareEnt,R.missed)}`)
console.log(`  cited by a LoCoMo fact that shares ≥2 query words    ${p(R.fact,R.missed)}`)
console.log(`  turn body shares ≥2 query words (lexical)            ${p(R.lex,R.missed)}`)
console.log(`  ANY of the above                                     ${p(R.anyLink,R.missed)}`)
entFreq.sort((a,b)=>a-b); console.log(`\nentity index: ${entFreq.length} names · median turns per name ${entFreq[entFreq.length>>1]} · p90 ${entFreq[Math.floor(entFreq.length*.9)]}`)
