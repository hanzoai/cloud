// Usage & wallet — the hanzo.team billing page. Small, mobile-first, honest:
// every number is the caller's OWN org (the server pins the tenant from the
// validated session; see clients/team/billing.go), or an explicit state.
//
// Reads (same origin, cookie-authenticated):
//   GET /v1/billing/balance    → { balance, holds, available }  (USD cents)
//   GET /v1/usage/summary      → { spend: { mtdCents, … }, llm: { requests, tokens, … } }
//   GET /v1/team/billing/plan  → { plan, active, seats, guests, guestLimit }
import { useEffect, useState } from 'react'
import { YStack, XStack, Text } from '@hanzo/gui'
import { MetricCard, Panel, PageHeader, PrimaryButton, StatusTag } from '@hanzo/ui'
import { Activity, CreditCard, Users, Wallet } from '@hanzogui/lucide-icons-2'

const TOPUP = 'https://billing.hanzo.ai'

interface Balance {
  balance: number
  holds: number
  available: number
}
interface Summary {
  range?: string
  spend?: { available?: boolean; totalCents?: number; mtdCents?: number }
  llm?: { available?: boolean; requests?: number; tokens?: number; costCents?: number }
}
interface Plan {
  plan?: string
  active?: boolean
  seats?: number
  guests?: number
  guestLimit?: number
}

type State =
  | { kind: 'loading' }
  | { kind: 'signin' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; balance: Balance | null; summary: Summary | null; plan: Plan | null }

async function read<T>(path: string): Promise<{ status: number; body: T | null }> {
  const res = await fetch(path, { credentials: 'include', headers: { Accept: 'application/json' } })
  if (!res.ok) return { status: res.status, body: null }
  return { status: res.status, body: (await res.json()) as T }
}

function usd(cents: number | undefined): string {
  if (typeof cents !== 'number' || !Number.isFinite(cents)) return '—'
  return (cents / 100).toLocaleString('en-US', { style: 'currency', currency: 'USD' })
}

function count(n: number | undefined): string {
  if (typeof n !== 'number' || !Number.isFinite(n)) return '—'
  return n.toLocaleString('en-US')
}

export function App() {
  const [state, setState] = useState<State>({ kind: 'loading' })

  useEffect(() => {
    let live = true
    Promise.all([
      read<Balance>('/v1/billing/balance'),
      read<Summary>('/v1/usage/summary?range=30d'),
      read<Plan>('/v1/team/billing/plan'),
    ])
      .then(([balance, summary, plan]) => {
        if (!live) return
        // Every read 401ing means no session — an honest sign-in state, never zeros.
        if ([balance, summary, plan].every((r) => r.status === 401)) {
          setState({ kind: 'signin' })
          return
        }
        setState({ kind: 'ready', balance: balance.body, summary: summary.body, plan: plan.body })
      })
      .catch(() => live && setState({ kind: 'error', message: 'billing upstream unreachable' }))
    return () => {
      live = false
    }
  }, [])

  return (
    <YStack maxW={760} width="100%" self="center" p="$4" gap="$4">
      <PageHeader
        title="Usage & billing"
        subtitle="Your org's wallet, current-period usage, and plan."
        actions={
          <PrimaryButton size="$3" onPress={() => window.open(TOPUP, '_blank', 'noopener')}>
            Top up
          </PrimaryButton>
        }
      />
      {state.kind === 'loading' && <Text color="$color10">Loading…</Text>}
      {state.kind === 'signin' && (
        <Panel title="Sign in required" grow={false}>
          <Text color="$color10">Sign in to hanzo.team to view your org's billing.</Text>
        </Panel>
      )}
      {state.kind === 'error' && (
        <Panel title="Unavailable" grow={false}>
          <Text color="$color10">{state.message}</Text>
        </Panel>
      )}
      {state.kind === 'ready' && (
        <>
          <XStack gap="$3" flexWrap="wrap">
            <MetricCard icon={<Wallet size={16} />} label="Available" value={usd(state.balance?.available)} caption="Spendable now" />
            <MetricCard icon={<CreditCard size={16} />} label="Balance" value={usd(state.balance?.balance)} caption="Prepaid credit" />
            <MetricCard icon={<Activity size={16} />} label="Holds" value={usd(state.balance?.holds)} caption="Reserved in flight" />
          </XStack>
          <Panel title="This period" grow={false}>
            <XStack gap="$3" flexWrap="wrap">
              <MetricCard icon={<Activity size={16} />} label="Spend (MTD)" value={usd(state.summary?.spend?.mtdCents)} caption="Month to date" />
              <MetricCard icon={<Activity size={16} />} label="Requests" value={count(state.summary?.llm?.requests)} caption="Last 30 days" />
              <MetricCard icon={<Activity size={16} />} label="Tokens" value={count(state.summary?.llm?.tokens)} caption="Last 30 days" />
            </XStack>
          </Panel>
          <Panel
            grow={false}
            title="Plan"
            right={
              state.plan?.plan ? (
                <StatusTag status={state.plan.active ? 'active' : 'inactive'} />
              ) : undefined
            }
          >
            <XStack gap="$3" flexWrap="wrap">
              <MetricCard icon={<CreditCard size={16} />} label="Plan" value={state.plan?.plan || '—'} caption="hanzo.team" />
              <MetricCard icon={<Users size={16} />} label="Seats" value={count(state.plan?.seats)} caption="Members in your org" />
              <MetricCard
                icon={<Users size={16} />}
                label="Guests"
                value={count(state.plan?.guests)}
                caption={state.plan?.guestLimit ? `of ${state.plan.guestLimit} included` : 'Invited guests'}
              />
            </XStack>
            <Text color="$color10" fontSize="$2">
              Manage payment methods and top-ups at billing.hanzo.ai.
            </Text>
          </Panel>
        </>
      )}
    </YStack>
  )
}
