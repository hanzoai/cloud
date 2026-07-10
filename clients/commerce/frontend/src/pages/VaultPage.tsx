import { useCommerceFetch } from '../lib/useCommerceFetch'

type VaultToken = {
  id: string
  brand: string
  last4: string
  expMonth: number
  expYear: number
  customerId: string
  created: string
}

export default function VaultPage() {
  const { data, error, isLoading } = useCommerceFetch<{ items: VaultToken[] }>('/vault/tokens')

  if (isLoading) return <div className="p-6 text-sm text-muted-foreground">Loading…</div>
  if (error) return <div className="p-6 text-sm text-red-500">{String(error)}</div>

  const items = data?.items ?? []
  return (
    <div className="p-6 space-y-4">
      <header>
        <h1 className="text-2xl font-semibold">Vault</h1>
        <p className="text-sm text-muted-foreground">PCI-scoped card tokens. Read-only. PANs are tokenised at capture; only the brand and last 4 digits are visible here.</p>
      </header>
      <div className="rounded-lg border border-border bg-background overflow-hidden">
        <table className="w-full text-sm">
          <thead className="border-b border-border bg-muted/30">
            <tr className="text-left">
              <th className="py-2 px-4 font-medium">Token</th>
              <th className="py-2 px-4 font-medium">Card</th>
              <th className="py-2 px-4 font-medium">Expires</th>
              <th className="py-2 px-4 font-medium">Customer</th>
              <th className="py-2 px-4 font-medium">Created</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {items.length === 0 && (
              <tr><td colSpan={5} className="py-6 text-center text-muted-foreground">No tokens.</td></tr>
            )}
            {items.map((t) => (
              <tr key={t.id}>
                <td className="py-2 px-4 font-mono text-xs">{t.id}</td>
                <td className="py-2 px-4">
                  <span className="uppercase">{t.brand}</span>
                  <span className="ml-2 font-mono">•••• {t.last4}</span>
                </td>
                <td className="py-2 px-4 font-mono">{String(t.expMonth).padStart(2, '0')}/{t.expYear}</td>
                <td className="py-2 px-4 font-mono text-xs">{t.customerId}</td>
                <td className="py-2 px-4 font-mono text-xs">{t.created}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
