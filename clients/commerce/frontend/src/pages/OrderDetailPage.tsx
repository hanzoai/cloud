import { useParams } from 'react-router-dom'
import { useCommerceFetch } from '../lib/useCommerceFetch'

const STAGES = ['placed', 'paid', 'fulfilled', 'shipped', 'delivered'] as const

export default function OrderDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { data, error, isLoading } = useCommerceFetch<{
    id: string
    status: string
    total: number
    currency: string
    customerId: string
    timeline?: { stage: string; at: string; note?: string }[]
  }>(`/orders/${id}`)

  if (isLoading) return <div className="p-6 text-sm text-muted-foreground">Loading…</div>
  if (error) return <div className="p-6 text-sm text-red-500">{String(error)}</div>
  if (!data) return null

  const currentIdx = STAGES.indexOf(data.status as typeof STAGES[number])

  return (
    <div className="p-6 max-w-4xl mx-auto space-y-6">
      <header className="flex items-center justify-between">
        <div>
          <div className="text-xs font-mono text-muted-foreground">{data.id}</div>
          <h1 className="text-2xl font-semibold">Order</h1>
        </div>
        <div className="text-right">
          <div className="text-2xl font-mono">{(data.total / 100).toFixed(2)}</div>
          <div className="text-xs uppercase text-muted-foreground">{data.currency}</div>
        </div>
      </header>

      <section className="rounded-lg border border-border bg-background p-4">
        <h2 className="text-sm font-semibold mb-4">Status</h2>
        <ol className="flex items-center justify-between">
          {STAGES.map((stage, i) => {
            const reached = i <= currentIdx
            return (
              <li key={stage} className="flex-1 flex items-center">
                <div className={`flex-1 flex flex-col items-center text-center ${reached ? 'text-foreground' : 'text-muted-foreground'}`}>
                  <div className={`w-3 h-3 rounded-full mb-2 ${reached ? 'bg-primary' : 'bg-muted'}`} />
                  <span className="text-xs capitalize">{stage}</span>
                </div>
                {i < STAGES.length - 1 && (
                  <div className={`flex-1 h-px ${i < currentIdx ? 'bg-primary' : 'bg-muted'}`} />
                )}
              </li>
            )
          })}
        </ol>
      </section>

      {data.timeline && data.timeline.length > 0 && (
        <section className="rounded-lg border border-border bg-background p-4">
          <h2 className="text-sm font-semibold mb-3">History</h2>
          <ul className="space-y-2">
            {data.timeline.map((evt, i) => (
              <li key={i} className="flex items-start gap-3 text-sm">
                <span className="text-xs font-mono text-muted-foreground w-32 shrink-0">{evt.at}</span>
                <span className="font-medium capitalize">{evt.stage}</span>
                {evt.note && <span className="text-muted-foreground">— {evt.note}</span>}
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  )
}
