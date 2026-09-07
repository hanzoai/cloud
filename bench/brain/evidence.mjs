// Why the second hop does not help: where the missing evidence actually sits.
import { readFileSync } from 'node:fs'
const store = JSON.parse(readFileSync('brain-vectors.json', 'utf8'))
const dot=(a,b)=>{let s=0;for(let i=0;i<a.length;i++)s+=a[i]*b[i];return s}
const cos=(a,b)=>dot(a,b)/(Math.sqrt(dot(a,a))*Math.sqrt(dot(b,b)))

const sess = (id) => id.split(':')[0]           // "D12:3" -> "D12"
let ev = [], spans = 0, multi = 0, ranks = [], worst = []
for (const c of store) {
  for (const q of c.qa) {
    if (q.category !== 1 || !q.evidence?.length) continue
    multi++
    ev.push(q.evidence.length)
    if (new Set(q.evidence.map(sess)).size > 1) spans++
    const ranked = c.turns.map(t=>({id:t.id,s:cos(q.v,t.v)})).sort((a,b)=>b.s-a.s)
    const pos = q.evidence.map(e => ranked.findIndex(r => r.id === e)).filter(i=>i>=0)
    if (pos.length) { ranks.push(...pos); worst.push(Math.max(...pos)) }
  }
}
const med = (a) => a.sort((x,y)=>x-y)[Math.floor(a.length/2)]
console.log(`multi-hop questions        ${multi}`)
console.log(`gold turns per question    ${(ev.reduce((a,b)=>a+b,0)/ev.length).toFixed(1)} avg, max ${Math.max(...ev)}`)
console.log(`evidence spans >1 session  ${((spans/multi)*100).toFixed(1)}%`)
console.log(`median rank of a gold turn ${med([...ranks])}`)
console.log(`median rank of the WORST   ${med([...worst])}`)
console.log(`worst gold turn beyond 20  ${((worst.filter(r=>r>=20).length/worst.length)*100).toFixed(1)}%`)
console.log(`worst gold turn beyond 100 ${((worst.filter(r=>r>=100).length/worst.length)*100).toFixed(1)}%`)
