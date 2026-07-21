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
import { MetricCard, Panel, PrimaryButton, StatusTag } from '@hanzo/ui'
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
  upgradeUrl?: string
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
      {/* Header reflows at narrow widths: the title block flexes to fill the row and
          the Top up button holds its intrinsic size (flexShrink 0), so it stays whole
          — never clipped to "Top" — and wraps below on a phone instead of compressing. */}
      <XStack justify="space-between" items="flex-start" gap="$4" flexWrap="wrap">
        <YStack gap="$1" flex={1} minW={200}>
          <Text fontSize="$7" fontWeight="800">
            Usage &amp; billing
          </Text>
          <Text fontSize="$3" color="$color11">
            Your org's wallet, current-period usage, and plan.
          </Text>
        </YStack>
        <PrimaryButton size="$3" flexShrink={0} onPress={() => window.open(TOPUP, '_blank', 'noopener')}>
          Top up
        </PrimaryButton>
      </XStack>
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
              ) : (
                // No paid team subscription → the org runs on the effective Free tier
                // (the login gate admits it). Offer the upgrade, never a bare dash.
                <PrimaryButton
                  size="$2"
                  onPress={() => window.open(state.plan?.upgradeUrl || TOPUP, '_blank', 'noopener')}
                >
                  Upgrade
                </PrimaryButton>
              )
            }
          >
            <XStack gap="$3" flexWrap="wrap">
              <MetricCard icon={<CreditCard size={16} />} label="Plan" value={state.plan?.plan || 'Free'} caption="hanzo.team" />
              <MetricCard icon={<Users size={16} />} label="Seats" value={count(state.plan?.seats)} caption="Members in your org" />
              <MetricCard
                icon={<Users size={16} />}
                label="Guests"
                value={count(state.plan?.guests)}
                caption={state.plan?.guestLimit ? `of ${state.plan.guestLimit} included` : 'Invited guests'}
              />
            </XStack>
            <Text color="$color10" fontSize="$2">
              {state.plan?.plan
                ? 'Manage payment methods and top-ups at billing.hanzo.ai.'
                : "You're on the Free plan — upgrade for more seats and guests at billing.hanzo.ai."}
            </Text>
          </Panel>
        </>
      )}
    </YStack>
  )
}
