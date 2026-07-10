// useCommerceFetch — keyed GET hook bound to the configured Auto BaseClient.
// Thin wrapper over @hanzogui/admin's useFetch + makeAuthedFetcher so
// every page loads data the same way:
//
//   const flows = useCommerceFetch<{items: Flow[]}>('/flows')
//
// The URL path is relative to the Base client's apiPrefix (/v1/commerce).
// Pass `null` to skip the fetch (e.g. while a path param is loading).

import { useMemo } from 'react'
import { makeAuthedFetcher, useBaseClient, useFetch, type FetchState } from '@hanzogui/admin'

export function useCommerceFetch<T>(relativePath: string | null): FetchState<T> {
  const client = useBaseClient()
  const fetcher = useMemo(() => makeAuthedFetcher(client), [client])
  const url = relativePath === null ? null : client.path(relativePath)
  return useFetch<T>(url, { fetcher: fetcher as never })
}
