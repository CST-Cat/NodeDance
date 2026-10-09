export interface ComposeEditorFile {
  path: string
  content: string
  version: string
}

export interface ComposePortEdit {
  file: string
  service: string
  target: number
  protocol: 'tcp' | 'udp'
  oldHostIP?: string
  oldPublished?: number
  newHostIP?: string
  newPublished: number
}

export interface ComposeEditorInput {
  files: ComposeEditorFile[]
  expectedVersions: Record<string, string>
  portEdits: ComposePortEdit[]
}

export interface ComposeEditorPreview {
  files: ComposeEditorFile[]
  diff: string
  resolvedConfig: string
  affectedServices: string[]
  impact: string[]
  dataBackup: boolean
  rollbackConfirmed: boolean
}

export interface ComposeEditorOperation {
  operationId: string
  status: 'queued' | 'running' | 'succeeded' | 'failed' | 'unknown'
  errorCode?: string
  verified: boolean
  affectedServices?: string[]
  impact?: string[]
  rollbackConfirmed?: boolean
  dataBackup: boolean
}

interface SourceResponse {
  files: ComposeEditorFile[]
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
  return await response.json() as T
}

function base(nodeId: string, projectKey: string) {
  return `/api/v1/nodes/${encodeURIComponent(nodeId)}/compose/projects/${encodeURIComponent(projectKey)}/editor`
}

function contextQuery(envFiles: string[], profiles: string[]) {
  const query = new URLSearchParams()
  for (const file of envFiles) query.append('envFile', file)
  for (const profile of profiles) query.append('profile', profile)
  const value = query.toString()
  return value ? `?${value}` : ''
}

function body(input: ComposeEditorInput, envFiles: string[], profiles: string[]) {
  return JSON.stringify({ editor: input, envFiles, profiles })
}

export const composeEditorApi = {
  source: (nodeId: string, projectKey: string, envFiles: string[], profiles: string[]) =>
    request<SourceResponse>(`${base(nodeId, projectKey)}/source${contextQuery(envFiles, profiles)}`),
  preview: (nodeId: string, projectKey: string, input: ComposeEditorInput, envFiles: string[], profiles: string[]) =>
    request<ComposeEditorPreview>(`${base(nodeId, projectKey)}/preview`, {
      method: 'POST', body: body(input, envFiles, profiles),
    }, true),
  apply: (nodeId: string, projectKey: string, input: ComposeEditorInput, envFiles: string[], profiles: string[]) =>
    request<ComposeEditorOperation>(`${base(nodeId, projectKey)}/apply`, {
      method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: body(input, envFiles, profiles),
    }, true),
  operation: (nodeId: string, operationId: string) =>
    request<ComposeEditorOperation>(`/api/v1/nodes/${encodeURIComponent(nodeId)}/compose/editor/operations/${encodeURIComponent(operationId)}`),
}
