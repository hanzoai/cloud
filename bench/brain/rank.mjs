// Ranked turn ids per question for one policy, as JSON — what answer.mjs reads.
import { ixs, retrieve, store, SHIPPED } from './cer2.mjs'
const arg = (k, d) => { const m = process.argv.find((a) => a.startsWith(`--${k}=`)); return m ? m.split('=')[1] : d }
const policy = arg('policy', 'cer'), k = Number(arg('k', 20))
const o = policy === 'single' ? { pool: k } : SHIPPED
process.stdout.write(JSON.stringify(store.map((c, ci) => c.qa.map((q) => q.evidence.length ? retrieve(ixs[ci], q, o).slice(0, k) : []))))
