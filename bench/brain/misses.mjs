// Under the fact index, what is the ceiling? For every multi-hop gold turn:
// is it cited by ANY LoCoMo fact, and if so where does its best fact rank?
import { readFileSync } from 'node:fs'
const store=JSON.parse(readFileSync('brain-vectors.json','utf8')), facts=JSON.parse(readFileSync('facts-vectors.json','utf8'))
const dot=(a,b)=>{let s=0;for(let i=0;i<a.length;i++)s+=a[i]*b[i];return s}, unit=v=>{const n=Math.sqrt(dot(v,v));return v.map(x=>x/n)}
const R={gold:0, cited:0, uncited:0, citedRank:[], turnRank:[]}
for (const [n,c] of store.entries()) {
  const F=facts[n].map(f=>({...f,v:unit(f.v)})), byTurn=new Map(); F.forEach((f,fi)=>{for(const id of f.ids)(byTurn.get(id)??byTurn.set(id,[]).get(id)).push(fi)})
  const turns=c.turns.map(t=>({id:t.id,v:unit(t.v)}))
  for (const q of c.qa) { if(q.category!==1||!q.evidence.length) continue
    const qv=unit(q.v)
    const fr=[...F.keys()].sort((a,b)=>dot(qv,F[b].v)-dot(qv,F[a].v)), fpos=new Map(fr.map((fi,r)=>[fi,r]))
    const tr=turns.map(t=>({id:t.id,s:dot(qv,t.v)})).sort((a,b)=>b.s-a.s), tpos=new Map(tr.map((t,r)=>[t.id,r]))
    for (const e of q.evidence) { R.gold++
      const cf=byTurn.get(e); if(!cf){R.uncited++;continue}
      R.cited++; R.citedRank.push(Math.min(...cf.map(fi=>fpos.get(fi)))); R.turnRank.push(tpos.get(e)??999)
    }
  }
}
const med=a=>{a=[...a].sort((x,y)=>x-y);return a[a.length>>1]}
const p=(a,b)=>((a/b)*100).toFixed(1)+'%'
console.log(`multi-hop gold turns ${R.gold}: cited by a fact ${p(R.cited,R.gold)} · uncited ${p(R.uncited,R.gold)}  ← the ceiling of a LoCoMo-fact index`)
console.log(`for cited gold: median rank of its best fact ${med(R.citedRank)} · within top-30 facts ${p(R.citedRank.filter(r=>r<30).length,R.citedRank.length)} · within top-60 ${p(R.citedRank.filter(r=>r<60).length,R.citedRank.length)}`)
console.log(`same gold turns under cosine over turns: median rank ${med(R.turnRank)} · within top-20 ${p(R.turnRank.filter(r=>r<20).length,R.turnRank.length)}`)
