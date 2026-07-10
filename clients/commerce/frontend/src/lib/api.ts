// Auto API client. Thin typed wrapper over @hanzogui/admin's BaseClient.
//
// The backend exposes /v1/commerce/<resource> with hand-rolled DTO shapes
// that strip secrets (e.g. connection.config_encrypted never leaves the
// server). Base's generic /collections/<name>/records auto-API is NOT
// exposed for these resources — see internal/routes/routes.go.
//
// That mismatch is why we don't use @hanzogui/admin's CollectionCRUD
// directly: that component talks to base's collection schema endpoints,
// which the auto route layer intentionally hides. Instead we lean on
// BaseClient.request() for transport (auth headers, 401 handling) and
// build small typed methods around the DTO contract.

import { type BaseClient } from '@hanzogui/admin'

export interface Flow {
  id: string
  name: string
  data: { nodes?: FlowNode[]; edges?: FlowEdge[] }
  created: string
  updated: string
}

export interface FlowNode {
  id: string
  type: string
  label?: string
  data?: Record<string, unknown>
  position?: { x: number; y: number }
}

export interface FlowEdge {
  from: string
  to: string
  condition?: string
}

export interface Run {
  id: string
  flowId: string
  status: 'queued' | 'running' | 'completed' | 'failed'
  taskId: string
  input: Record<string, unknown>
  output: Record<string, unknown>
  error?: Record<string, unknown>
  started?: string
  finished?: string
  created: string
}

export interface Piece {
  type: string
  name: string
  category: string
  description: string
  inputs?: Array<{
    name: string
    label: string
    type: string
    required?: boolean
    description?: string
  }>
  outputs?: string[]
}

export interface Connection {
  id: string
  piece: string
  name: string
  created: string
}

export interface Trigger {
  id: string
  flowId: string
  type: string
  config: Record<string, unknown>
  enabled: boolean
  created: string
}

export interface BrandConfig {
  brand: {
    name: string
    title: string
    description?: string
    appDomain?: string
    primaryColor?: string
    logoUrl?: string
  }
}

interface ListResponse<T> {
  items: T[]
}

// makeCommerceApi binds a configured BaseClient (apiPrefix=/v1/commerce) into
// the Auto-specific verb surface. Single source of truth for endpoint
// paths; the rest of the SPA imports this object and never types URLs.
export function makeCommerceApi(client: BaseClient) {
  const req = <T>(p: string, init?: RequestInit) =>
    client.request<T>(p.startsWith('/') ? p : '/' + p, init)

  return {
    flows: {
      list: () => req<ListResponse<Flow>>('/flows'),
      get: (id: string) => req<Flow>(`/flows/${encodeURIComponent(id)}`),
      create: (name: string) =>
        req<Flow>('/flows', {
          method: 'POST',
          body: JSON.stringify({ name, data: { nodes: [], edges: [] } }),
        }),
      update: (id: string, patch: Partial<Flow>) =>
        req<Flow>(`/flows/${encodeURIComponent(id)}`, {
          method: 'PATCH',
          body: JSON.stringify(patch),
        }),
      remove: (id: string) =>
        req<void>(`/flows/${encodeURIComponent(id)}`, { method: 'DELETE' }),
      publish: (id: string) =>
        req<{ version: number }>(`/flows/${encodeURIComponent(id)}/publish`, {
          method: 'POST',
        }),
    },
    runs: {
      list: (flowId?: string) =>
        req<ListResponse<Run>>(
          flowId ? `/runs?flowId=${encodeURIComponent(flowId)}` : '/runs',
        ),
      get: (id: string) => req<Run>(`/runs/${encodeURIComponent(id)}`),
      create: (flowId: string, input: Record<string, unknown> = {}) =>
        req<Run>('/runs', {
          method: 'POST',
          body: JSON.stringify({ flowId, input }),
        }),
    },
    pieces: () => req<ListResponse<Piece>>('/pieces'),
    connections: {
      list: () => req<ListResponse<Connection>>('/connections'),
      create: (piece: string, name: string, config: Record<string, unknown>) =>
        req<Connection>('/connections', {
          method: 'POST',
          body: JSON.stringify({ piece, name, config }),
        }),
      remove: (id: string) =>
        req<void>(`/connections/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    },
    triggers: {
      list: () => req<ListResponse<Trigger>>('/triggers'),
      fire: (id: string, payload: Record<string, unknown> = {}) =>
        req<Run>(`/triggers/${encodeURIComponent(id)}/fire`, {
          method: 'POST',
          body: JSON.stringify(payload),
        }),
    },
    brand: () => req<BrandConfig>('/brand'),
  }
}

export type CommerceApi = ReturnType<typeof makeCommerceApi>
