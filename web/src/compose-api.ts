export type ComposeAction = 'validate' | 'up' | 'start' | 'stop' | 'restart' | 'down'

export interface ComposeProjectRef {
  key: string
  name: string
  workingDirectory: string
  configFiles: string[]
}

export interface ComposeServiceInstance {
  containerId: string
  containerName: string
  state: string
  health: string
}

export interface ComposeService {
  name: string
  instances: ComposeServiceInstance[]
}

export interface ComposeProject {
  ref: ComposeProjectRef
  configAvailable: boolean
  configReason?: string
  dataStale?: boolean
  services: ComposeService[]
}

export interface ComposeProjectsResponse {
  projects: ComposeProject[]
  serverTime: string
  dataStale?: boolean
}

export interface ComposeOperationResponse {
  operationId: string
  status: 'queued' | 'running' | 'succeeded' | 'failed' | 'timed_out' | 'unknown'
  errorCode?: string
  verified: boolean
  project?: ComposeProject
}

async function request<T>(url: string, init: RequestInit = {}, csrf = false): Promise<T> {
  const headers = new Headers(init.headers)
  if (init.body !== undefined && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  if (csrf) {
    const response = await fetch('/api/v1/auth/csrf', { credentials: 'include', cache: 'no-store' })
    if (!response.ok) throw new Error(`CSRF token request failed (${response.status})`)
    const body = await response.json() as { token?: unknown }
    if (typeof body.token !== 'string' || !body.token) throw new Error('CSRF token response is invalid')
    headers.set('X-CSRF-Token', body.token)
  }
  const response = await fetch(url, { ...init, headers, credentials: 'include', cache: 'no-store' })
  if (!response.ok) {
    let message = `Request failed (${response.status})`
    try {
      const body = await response.json() as { message?: unknown }
      if (typeof body.message === 'string') message = body.message
    } catch { /* keep status fallback */ }
    throw new Error(message)
  }
  if (response.status === 204) return undefined as T
  return await response.json() as T
}

export const composeApi = {
  projects: (nodeId: string) => request<ComposeProjectsResponse>(`/api/v1/nodes/${encodeURIComponent(nodeId)}/compose/projects`),
  action: (nodeId: string, project: ComposeProjectRef, action: ComposeAction, options: { profiles?: string[]; envFiles?: string[] } = {}) => {
    const idempotencyKey = crypto.randomUUID()
    return request<ComposeOperationResponse>(
      `/api/v1/nodes/${encodeURIComponent(nodeId)}/compose/projects/${encodeURIComponent(project.key)}/actions`,
      {
        method: 'POST',
        headers: { 'Idempotency-Key': idempotencyKey },
        body: JSON.stringify({ action, project, envFiles: options.envFiles ?? [], profiles: options.profiles ?? [] }),
      },
      true,
    )
  },
  operation: (nodeId: string, operationId: string) => request<ComposeOperationResponse>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/compose/operations/${encodeURIComponent(operationId)}`,
  ),
}
