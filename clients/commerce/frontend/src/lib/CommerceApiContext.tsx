// CommerceApi context — exposes the typed Auto verb surface (built on
// @hanzogui/admin's BaseClient) to every page without prop-drilling.

import { createContext, useContext, useMemo, type ReactNode } from 'react'
import { useBaseClient } from '@hanzogui/admin'
import { makeCommerceApi, type CommerceApi } from './api'

const CommerceApiContext = createContext<CommerceApi | null>(null)

export function CommerceApiProvider({ children }: { children: ReactNode }) {
  const client = useBaseClient()
  const api = useMemo(() => makeCommerceApi(client), [client])
  return <CommerceApiContext.Provider value={api}>{children}</CommerceApiContext.Provider>
}

export function useCommerceApi(): CommerceApi {
  const ctx = useContext(CommerceApiContext)
  if (!ctx) {
    throw new Error(
      '[useCommerceApi] No <CommerceApiProvider> in tree. Wrap your routes: ' +
        '<CommerceApiProvider><Outlet/></CommerceApiProvider>',
    )
  }
  return ctx
}
