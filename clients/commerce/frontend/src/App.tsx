// Hanzo Commerce admin SPA — composed from @hanzogui/admin chrome.
//
// Single wiring path: BrowserRouter → AuthGate → BaseClientProvider →
// CommerceApiProvider → Routes → Chrome (AdminApp shell) → page.
//
// Backend serves /v1/commerce/* on the same origin as the SPA. IAM at
// /v1/iam (mounted by base's platform plugin on commerce.hanzo.ai).

import { useMemo } from 'react'
import {
  BrowserRouter,
  Navigate,
  Outlet,
  Route,
  Routes,
} from 'react-router-dom'
import { IAM } from '@hanzo/iam/browser'
import {
  AccountChip,
  AdminApp,
  AuthGate,
  BaseClientProvider,
  HanzoMark,
  LocalTimeIndicator,
  PageShell,
  Sidebar,
  ThemeToggle,
  TopBar,
  createBaseClient,
  useAuth,
  type SidebarConfig,
} from '@hanzogui/admin'
import { BookOpen } from '@hanzogui/lucide-icons-2/icons/BookOpen'
import { CreditCard } from '@hanzogui/lucide-icons-2/icons/CreditCard'
import { FileText } from '@hanzogui/lucide-icons-2/icons/FileText'
import { Heart } from '@hanzogui/lucide-icons-2/icons/Heart'
import { Lock } from '@hanzogui/lucide-icons-2/icons/Lock'
import { Package } from '@hanzogui/lucide-icons-2/icons/Package'
import { Receipt } from '@hanzogui/lucide-icons-2/icons/Receipt'
import { Repeat } from '@hanzogui/lucide-icons-2/icons/Repeat'
import { Users } from '@hanzogui/lucide-icons-2/icons/Users'
import { CommerceApiProvider } from './lib/CommerceApiContext'
import CustomersPage from './pages/CustomersPage'
import InvoicesPage from './pages/InvoicesPage'
import OrderDetailPage from './pages/OrderDetailPage'
import OrdersPage from './pages/OrdersPage'
import ProductsPage from './pages/ProductsPage'
import SubscriptionsPage from './pages/SubscriptionsPage'
import VaultPage from './pages/VaultPage'

const APP_VERSION = '1.34.0-dev'

const origin =
  typeof window !== 'undefined' ? window.location.origin : 'https://commerce.hanzo.ai'

const iam = new IAM({
  serverUrl: `${origin}/v1/iam`,
  clientId: 'hanzo-commerce',
  appName: 'hanzo-commerce',
  orgName: 'hanzo',
  redirectUri: `${origin}/callback`,
})

const baseClient = createBaseClient({
  apiPrefix: '/v1/commerce',
  getToken: () =>
    (iam as unknown as { getAccessToken?: () => string }).getAccessToken?.() ?? '',
})

const sidebar: SidebarConfig = {
  brand: { mark: <HanzoMark />, title: 'Hanzo Commerce', subtitle: 'Payments' },
  sections: [
    {
      items: [
        { to: '/orders', icon: Receipt, label: 'Orders', end: true },
        { to: '/products', icon: Package, label: 'Products' },
        { to: '/customers', icon: Users, label: 'Customers' },
        { to: '/invoices', icon: FileText, label: 'Invoices' },
        { to: '/subscriptions', icon: Repeat, label: 'Subscriptions' },
        { to: '/vault', icon: Lock, label: 'Vault' },
      ],
    },
    {
      items: [{ href: 'https://docs.hanzo.ai/commerce', icon: BookOpen, label: 'Docs' }],
    },
  ],
  footer: {
    feedback: {
      href: 'https://github.com/hanzoai/commerce/issues/new',
      label: 'Feedback',
      icon: Heart,
    },
    version: `v${APP_VERSION}`,
  },
}

function Chrome() {
  const { user, signOut } = useAuth()
  const email = (user?.email as string | undefined) ?? ''
  const initials = email.slice(0, 2).toUpperCase() || 'HC'
  return (
    <AdminApp
      sidebar={<Sidebar config={sidebar} />}
      topBar={
        <TopBar
          right={
            <>
              <LocalTimeIndicator />
              <ThemeToggle storageKey="commerce.theme" />
              <AccountChip
                initials={initials}
                name={email || 'Anonymous'}
                subtitle="hanzo-commerce"
                onSignOut={signOut}
              />
            </>
          }
        />
      }
    >
      <Outlet />
    </AdminApp>
  )
}

export default function App() {
  const stableIam = useMemo(() => iam, [])
  return (
    <BrowserRouter>
      <AuthGate iam={stableIam} appTitle="Hanzo Commerce" defaultLandingPath="/orders">
        <BaseClientProvider client={baseClient}>
          <CommerceApiProvider>
            <Routes>
              <Route element={<Chrome />}>
                <Route path="/" element={<Navigate to="/orders" replace />} />
                <Route path="/orders" element={<PageShell><OrdersPage /></PageShell>} />
                <Route path="/orders/:id" element={<PageShell><OrderDetailPage /></PageShell>} />
                <Route path="/products" element={<PageShell><ProductsPage /></PageShell>} />
                <Route path="/customers" element={<PageShell><CustomersPage /></PageShell>} />
                <Route path="/invoices" element={<PageShell><InvoicesPage /></PageShell>} />
                <Route path="/subscriptions" element={<PageShell><SubscriptionsPage /></PageShell>} />
                <Route path="/vault" element={<PageShell><VaultPage /></PageShell>} />
                <Route path="*" element={<Navigate to="/orders" replace />} />
              </Route>
            </Routes>
          </CommerceApiProvider>
        </BaseClientProvider>
      </AuthGate>
    </BrowserRouter>
  )
}
