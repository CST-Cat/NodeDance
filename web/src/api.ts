import type { MetricsView } from './metrics-contract'

export interface SetupStatus {
  initialized: boolean
  setupHint?: string
}

export interface User {
  displayName: string
}

export interface Appearance {
  displayName: string
  theme: string
  backgroundColor: string
  avatarUrl: string
  backgroundUrl: string
}

export interface Session {
  id: string
  createdAt: string
  lastSeenAt: string
  device: string
  remoteAddr: string
  current: boolean
}

export interface AgentNode {
  nodeId: string
  agentId?: string
  displayName: string
  status: 'online' | 'offline' | 'revoked' | string
  generation: number
  lastSeen?: string
  leaseValidUntil?: string
  protocolVersion?: number
  agentVersion?: string
  capabilities: string[]
}

export interface AgentNodesResponse {
  nodes: AgentNode[]
  serverTime: string
}

export interface NodeStatusResponse {
  type: 'node_status'
  nodeId: string
  state: {
    nodeId: string
    agentId?: string
    status: 'online' | 'offline' | string
    generation: number
    serverTime: string
    leaseValidUntil?: string
    reason?: string
  }
}

export type AgentMetricsResponse = MetricsView | NodeStatusResponse

interface AuthResponse {
  user: User
  csrfToken: string
}

export class ApiError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

let csrfToken = ''

async function request<T>(path: string, init: RequestInit = {}, csrf = false): Promise<T> {
  const headers = new Headers(init.headers)
  const isFormData = typeof FormData !== 'undefined' && init.body instanceof FormData

  if (init.body !== undefined && !isFormData && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  if (csrf) {
    if (!csrfToken) {
      const result = await request<{ token: string }>('/api/v1/auth/csrf', { method: 'GET' })
      csrfToken = result.token
    }
    headers.set('X-CSRF-Token', csrfToken)
  }

  const response = await fetch(path, {
    ...init,
    headers,
    credentials: 'include',
    cache: 'no-store',
  })

  if (!response.ok) {
    let message = `请求失败（${response.status}）`
    try {
      const payload = await response.json() as { error?: unknown; message?: unknown }
      if (typeof payload.message === 'string') message = payload.message
      else if (typeof payload.error === 'string') message = payload.error
    } catch {
      // Keep the status-based message when the Core returns an empty or non-JSON error.
    }
    throw new ApiError(response.status, message)
  }

  if (response.status === 204) return undefined as T
  const body = await response.text()
  if (!body) return undefined as T
  let payload: unknown
  try {
    payload = JSON.parse(body)
  } catch {
    throw new ApiError(response.status, '服务器返回了无法识别的响应。')
  }
  if ((path === '/api/v1/auth/setup' || path === '/api/v1/auth/login') &&
    typeof payload === 'object' && payload !== null && 'csrfToken' in payload &&
    typeof payload.csrfToken === 'string') {
    csrfToken = payload.csrfToken
  }
  return payload as T
}

export const api = {
  setupStatus: () => request<SetupStatus>('/api/v1/auth/setup/status'),
  setup: (payload: { credential: string; password: string; displayName: string }) => {
    csrfToken = ''
    return request<AuthResponse>('/api/v1/auth/setup', {
      method: 'POST',
      body: JSON.stringify(payload),
    }, true)
  },
  login: (password: string) => {
    csrfToken = ''
    return request<AuthResponse>('/api/v1/auth/login', {
      method: 'POST',
      body: JSON.stringify({ password }),
    }, true)
  },
  me: () => request<{ user: User }>('/api/v1/auth/me'),
  logout: async () => {
    const result = await request<void>('/api/v1/auth/logout', { method: 'POST' }, true)
    csrfToken = ''
    return result
  },
  changePassword: async (currentPassword: string, newPassword: string) => {
    const result = await request<void>('/api/v1/auth/password', {
      method: 'POST',
      body: JSON.stringify({ currentPassword, newPassword }),
    }, true)
    csrfToken = ''
    return result
  },
  sessions: () => request<{ sessions: Session[] }>('/api/v1/auth/sessions'),
  agents: () => request<AgentNodesResponse>('/api/v1/agents'),
  nodes: () => request<AgentNodesResponse>('/api/v1/nodes'),
  nodeMetrics: (nodeId: string) => request<AgentMetricsResponse>(`/api/v1/nodes/${encodeURIComponent(nodeId)}/metrics`),
  revokeSession: (id: string) => request<void>(`/api/v1/auth/sessions/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  }, true),
  appearance: () => request<Appearance>('/api/v1/public/appearance'),
  saveAppearance: (payload: Pick<Appearance, 'displayName' | 'theme' | 'backgroundColor'>) =>
    request<void>('/api/v1/settings/appearance', {
      method: 'PUT',
      body: JSON.stringify(payload),
    }, true),
  uploadImage: (kind: 'avatar' | 'background', image: File) => {
    const form = new FormData()
    form.append('image', image)
    return request<void>(`/api/v1/settings/appearance/${kind}`, {
      method: 'POST',
      body: form,
    }, true)
  },
}
