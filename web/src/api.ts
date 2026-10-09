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

export interface TailscalePeer {
  identity: string
  name: string
  dnsName?: string
  os: string
  class: 'linux' | 'unsupported' | 'unknown' | string
  online: boolean
  ips: string[]
  managed: boolean
  nodeId?: string
}

export interface TailscaleDiscoveryResponse {
  peers: TailscalePeer[]
  discoveredAt: string
}

export interface TailscaleDeploymentTask {
  taskId: string
  peerIdentity: string
  peerName: string
  status: 'queued' | 'running' | 'succeeded' | 'failed' | 'unknown' | string
  phase: string
  message?: string
  nodeId?: string
  createdAt: string
  updatedAt: string
  finishedAt?: string
}

export interface TailscaleDeployPayload {
  peerIdentity: string
  displayName: string
  coreUrl: string
  fallbackUrl?: string
  allowFallback: boolean
  hostFingerprint: string
  confirmHostKey: boolean
  confirmChangedHostKey: boolean
  credentials: { user: string; password?: string; privateKey?: string; passphrase?: string }
}

export interface ServiceProbe {
  id: string
  nodeId: string
  name: string
  kind: 'http' | 'https' | 'tcp'
  target: string
  expectedHttpStatus?: number
  intervalSeconds: number
  timeoutSeconds: number
  enabled: boolean
  revision: number
  status: 'unknown' | 'healthy' | 'unhealthy'
  lastErrorCode?: string
  consecutiveFailures: number
  consecutiveSuccesses: number
  lastCheckedAt?: string
  nextDueAt: string
  createdAt: string
  updatedAt: string
}

export interface ServiceProbeRun {
  runId: string
  probeId: string
  nodeId: string
  status: 'pending' | 'unknown' | 'healthy' | 'unhealthy'
  checkedAt: string
  completedAt?: string
  latencyMs?: number
  httpStatus?: number
  errorCode?: string
}

export interface ServiceProbePayload {
  nodeId: string
  name: string
  kind: ServiceProbe['kind']
  target: string
  expectedHttpStatus?: number
  intervalSeconds: number
  timeoutSeconds: number
  enabled: boolean
  revision?: number
}

export type AlertKind = 'node_offline' | 'cpu' | 'memory' | 'disk' | 'docker_unavailable' | 'container_state' | 'probe_state'
export interface AlertRule {
  id: string; name: string; kind: AlertKind; nodeId: string; subjectId?: string
  severity: 'info' | 'warning' | 'critical'; threshold?: number; durationSeconds: number
  cooldownSeconds: number; expectedState?: string; channelIds: string[]; enabled: boolean; revision: number
}
export interface AlertChannel {
  id: string; name: string; kind: 'webhook' | 'smtp'; enabled: boolean; hasSecret: boolean; revision: number
  config: { webhookUrl?: string; smtpHost?: string; smtpPort?: number; smtpFrom?: string; smtpTo?: string; smtpUsername?: string }
}
export interface AlertItem {
  id: string; ruleId: string; ruleName: string; nodeId: string; nodeName: string; subjectId?: string
  severity: string; status: 'active' | 'resolved'; message: string; currentValue?: number; firstSeenAt: string; lastSeenAt: string
  resolvedAt?: string; acknowledgedAt?: string; acknowledgedBy?: string; acknowledgedNote?: string; silencedUntil?: string; suppressionReason?: string
}
export interface AlertEvent { id: string; alertId: string; kind: string; occurredAt: string; message: string }
export interface AlertWindow { id: string; kind: 'silence' | 'maintenance'; scopeType: 'global' | 'node' | 'rule'; scopeId?: string; startsAt: string; endsAt: string; reason: string }
export interface AlertDelivery {
  id: string; alertId?: string; channelId: string; channelName: string; kind: string; testSend: boolean
  status: string; attempts: number; maxAttempts: number; nextAttemptAt?: string; deliveredAt?: string; httpStatus?: number; lastError?: string; createdAt: string
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

export interface DockerPortBinding {
  ip?: string
  port?: string
}

export interface DockerPort {
  containerPort: number
  protocol: string
  exposed: boolean
  configured: DockerPortBinding[]
  published: DockerPortBinding[]
}

export interface DockerContainer {
  id: string
  name: string
  image: string
  imageId: string
  state: string
  running: boolean
  paused: boolean
  restarting: boolean
  health: string
  healthcheckConfigured: boolean
  healthReason?: string
  unavailableReason?: string
  stale: boolean
  createdAt?: string
  startedAt?: string
  finishedAt?: string
  restartCount: number
  ports: DockerPort[]
  observedAt: string
  compose?: { project: string; service: string }
}

export interface DockerContainerRecord {
  container: DockerContainer
  generation: number
  sequence: number
  receivedAt: string
}

export interface DockerInventory {
  agentId: string
  nodeId: string
  agentOnline: boolean
  activeGeneration: number
  leaseValidUntil?: string
  dockerAvailability: 'unknown' | 'available' | 'unavailable'
  dockerEventsConnected: boolean
  dockerSnapshotFresh: boolean
  dataStale: boolean
  staleReason?: string
  health?: { errorKind?: string; reason?: string }
  containers: DockerContainerRecord[]
  serverTime: string
}

export interface DockerInventoryMessage {
  type: 'node_containers'
  nodeId: string
  state: NodeStatusResponse['state']
  inventory: DockerInventory
  preferenceIdentities?: Record<string, string>
}

export interface DashboardPreference {
  nodeId: string
  targetKind: 'node' | 'container' | 'compose_service'
  identity: string
  alias: string
  icon: string
  notes: string
  serviceUrl: string
  sortOrder: number
  visible: boolean
  pinned: boolean
}

export interface DashboardSettings {
  viewMode: 'monitor' | 'manage'
  groupBy: 'node' | 'compose' | 'state' | 'none'
  sortBy: 'custom' | 'name' | 'state'
  featuredLimit: number
  customFields: string[]
}

export interface MetricHistoryPoint {
  bucketAt: string
  value: number
  samples: number
  minimum: number
  maximum: number
}

export interface MetricHistorySeries {
  key: string
  points: MetricHistoryPoint[]
}

export interface MetricHistory {
  nodeId: string
  resolution: 'minute' | 'hour'
  from: string
  to: string
  series: MetricHistorySeries[]
}

export interface DockerImage {
  id: string
  tags: string[]
  digests: string[]
  size: number
  createdAt: number
  containers: number
}

export interface DockerImagePage {
  images: DockerImage[]
  total: number
  page: number
  errorCode?: string
}

export interface ContainerTask {
  taskId: string
  nodeId: string
  targetId: string
  action: 'start' | 'stop' | 'restart' | 'pause' | 'resume' | 'delete' | 'rename' | 'image_pull' | 'image_delete' | 'rebuild' | 'rebuild_cleanup'
  status: 'queued' | 'running' | 'succeeded' | 'failed' | 'timed_out' | 'canceled' | 'unknown'
  deliveryState: string
  reconciliationRequired: boolean
  progress: { phase: string; completed: number; total: number }
  result: { code?: string; observedState?: string; resourceRevision?: string }
  createdAt: string
  updatedAt: string
  startedAt?: string
  finishedAt?: string
  cancelRequested?: boolean
}

export interface TaskAuditEvent {
  id: number
  event: string
  fromStatus?: string
  toStatus?: string
  actorId?: number
  remoteAddress: string
  occurredAt: string
}

export type ContainerTaskAction = ContainerTask['action']
export interface RebuildPortBinding {
  containerPort: string
  hostIp?: string
  hostPort?: string
}

export interface RebuildSpec {
  portBindings?: RebuildPortBinding[]
  clearPortBindings?: boolean
  cleanupTaskId?: string
}

export interface ContainerRebuildPlan {
  containerId: string
  name: string
  imageId: string
  wasRunning: boolean
  writableLayerBytes: number
  snapshotRequired: boolean
  portsBefore: string[]
  portsAfter: string[]
  preserved: string[]
  changed: string[]
  downtime: string
  risks: string[]
  mounts: { type: string; destination: string; readWrite: boolean; volumeId?: string }[]
}

export interface CreateContainerTaskPayload {
  action: ContainerTaskAction
  newName?: string
  deleteConfirmed?: boolean
  deleteConfirmationId?: string
  rebuild?: RebuildSpec
  confirmationId?: string
}

export interface TerminalAuthorization {
  streamId: string
  ticket: string
  expiresAt: string
}

export interface NodeFileEntry {
  name: string
  path: string
  kind: 'file' | 'directory' | 'symlink' | 'other' | string
  size: number
  mode: number
  ownerUid: number
  ownerGid: number
  modifiedAt: number
  version?: string
}

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

  listNodeFiles: (nodeId: string, path = '/') => request<{ entries: NodeFileEntry[]; path: string }>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/files?path=${encodeURIComponent(path)}`,
  ),
  statNodeFile: (nodeId: string, path: string) => request<NodeFileEntry>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/files/stat?path=${encodeURIComponent(path)}`,
  ),
  readNodeText: (nodeId: string, path: string) => request<{ path: string; text: string; version: string; size: number }>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/files/text?path=${encodeURIComponent(path)}`,
  ),
  saveNodeText: (nodeId: string, payload: { path: string; version: string; text: string }) => request<{
    transferId: string
    status: string
    entry?: NodeFileEntry
    backupPath?: string
  }>(`/api/v1/nodes/${encodeURIComponent(nodeId)}/files/text`, {
    method: 'PUT',
    body: JSON.stringify(payload),
  }, true),
  createNodeDirectory: (nodeId: string, path: string) => request<{ transferId: string; status: string }>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/files/directories`, {
      method: 'POST',
      body: JSON.stringify({ path }),
    }, true),
  renameNodeFile: (nodeId: string, path: string, newPath: string) => request<{ transferId: string; status: string }>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/files/rename`, {
      method: 'POST',
      body: JSON.stringify({ path, newPath }),
    }, true),
  deleteNodeFile: (nodeId: string, path: string) => request<{ transferId: string; status: string }>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/files/delete`, {
      method: 'DELETE',
      body: JSON.stringify({ path, confirmPath: path }),
    }, true),
  uploadNodeFile: (nodeId: string, path: string, file: File, version = '', signal?: AbortSignal) => {
    const headers = new Headers({ 'Content-Type': 'application/octet-stream' })
    if (version) headers.set('X-File-Version', version)
    return request<{ transferId: string; status: string; sha256: string }>(
      `/api/v1/nodes/${encodeURIComponent(nodeId)}/files/upload?path=${encodeURIComponent(path)}`,
      { method: 'POST', headers, body: file, signal }, true,
    )
  },
  nodeFileDownloadURL: (nodeId: string, path: string) =>
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/files/download?path=${encodeURIComponent(path)}`,
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
  tailscalePeers: () => request<TailscaleDiscoveryResponse>('/api/v1/discovery/tailscale'),
  probeTailscaleSSH: (peerIdentity: string) => request<{ fingerprint: string }>('/api/v1/discovery/tailscale/host-key', {
    method: 'POST', body: JSON.stringify({ peerIdentity }),
  }, true),
  createManualEnrollment: (displayName: string) => request<{ nodeId: string; displayName: string; token: string; expiresAt: string; expiresInSeconds: number }>('/api/v1/discovery/enrollments', {
    method: 'POST', body: JSON.stringify({ displayName }),
  }, true),
  startTailscaleDeployment: (payload: TailscaleDeployPayload) => request<TailscaleDeploymentTask>('/api/v1/discovery/deployments', {
    method: 'POST', body: JSON.stringify(payload),
  }, true),
  tailscaleDeployment: (taskId: string) => request<TailscaleDeploymentTask>(`/api/v1/discovery/deployments/${encodeURIComponent(taskId)}`),
  nodes: () => request<AgentNodesResponse>('/api/v1/nodes'),
  nodeMetrics: (nodeId: string) => request<AgentMetricsResponse>(`/api/v1/nodes/${encodeURIComponent(nodeId)}/metrics`),
  nodeContainers: (nodeId: string) => request<DockerInventoryMessage>(`/api/v1/nodes/${encodeURIComponent(nodeId)}/containers`),
  dashboardSettings: () => request<DashboardSettings>('/api/v1/dashboard/settings'),
  saveDashboardSettings: (settings: DashboardSettings) => request<void>('/api/v1/dashboard/settings', {
    method: 'PUT', body: JSON.stringify(settings),
  }, true),
  nodePreferences: (nodeId: string) => request<{ preferences: DashboardPreference[] }>(`/api/v1/nodes/${encodeURIComponent(nodeId)}/preferences`),
  saveNodePreference: (nodeId: string, preference: DashboardPreference) => request<void>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/preferences`, { method: 'PUT', body: JSON.stringify(preference) }, true),
  nodeHistory: (nodeId: string, resolution: 'minute' | 'hour', from: Date, to: Date) => {
    const query = new URLSearchParams({ resolution, from: from.toISOString(), to: to.toISOString() })
    return request<MetricHistory>(`/api/v1/nodes/${encodeURIComponent(nodeId)}/history?${query}`)
  },
  nodeImages: (nodeId: string, filter = '', page = 0) => request<DockerImagePage>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/images?filter=${encodeURIComponent(filter)}&page=${page}&pageSize=50`),
  nodeTasks: (nodeId: string) => request<{ tasks: ContainerTask[]; nextCursor: string }>(`/api/v1/nodes/${encodeURIComponent(nodeId)}/tasks?limit=50`),
  nodeTask: (nodeId: string, taskId: string) => request<ContainerTask>(`/api/v1/nodes/${encodeURIComponent(nodeId)}/tasks/${encodeURIComponent(taskId)}`),
  nodeTaskAudit: (nodeId: string, taskId: string) => request<{ events: TaskAuditEvent[] }>(`/api/v1/nodes/${encodeURIComponent(nodeId)}/tasks/${encodeURIComponent(taskId)}/audit`),
  cancelNodeTask: (nodeId: string, taskId: string) => request<ContainerTask & { cancelRequested?: boolean }>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/tasks/${encodeURIComponent(taskId)}`, { method: 'DELETE' }, true),
  serviceProbes: () => request<{ probes: ServiceProbe[] }>('/api/v1/probes'),
  createServiceProbe: (payload: ServiceProbePayload) => request<ServiceProbe>('/api/v1/probes', {
    method: 'POST', body: JSON.stringify(payload),
  }, true),
  updateServiceProbe: (id: string, payload: ServiceProbePayload) => request<ServiceProbe>(`/api/v1/probes/${encodeURIComponent(id)}`, {
    method: 'PUT', body: JSON.stringify(payload),
  }, true),
  deleteServiceProbe: (id: string, revision: number) => request<void>(`/api/v1/probes/${encodeURIComponent(id)}?revision=${revision}`, {
    method: 'DELETE',
  }, true),
  serviceProbeHistory: (id: string) => request<{ runs: ServiceProbeRun[] }>(`/api/v1/probes/${encodeURIComponent(id)}/history?limit=100`),
  alertRules: (nodeId = '') => request<{ rules: AlertRule[] }>(`/api/v1/alerts/rules${nodeId ? `?nodeId=${encodeURIComponent(nodeId)}` : ''}`),
  createAlertRule: (payload: Omit<AlertRule, 'id' | 'revision'>) => request<{ rule: AlertRule }>('/api/v1/alerts/rules', { method: 'POST', body: JSON.stringify(payload) }, true),
  updateAlertRule: (id: string, payload: AlertRule) => request<{ rule: AlertRule }>(`/api/v1/alerts/rules/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(payload) }, true),
  deleteAlertRule: (id: string, revision: number) => request<void>(`/api/v1/alerts/rules/${encodeURIComponent(id)}`, { method: 'DELETE', body: JSON.stringify({ revision }) }, true),
  ensureDefaultAlertRules: (nodeId: string) => request<{ rules: AlertRule[] }>('/api/v1/alerts/rules/defaults', { method: 'POST', body: JSON.stringify({ nodeId }) }, true),
  alertChannels: () => request<{ channels: AlertChannel[] }>('/api/v1/alerts/channels'),
  saveAlertChannel: (payload: { name: string; kind: 'webhook' | 'smtp'; config: AlertChannel['config']; secret?: string; enabled: boolean; revision?: number }, id?: string) =>
    request<{ channel: AlertChannel }>(id ? `/api/v1/alerts/channels/${encodeURIComponent(id)}` : '/api/v1/alerts/channels', { method: id ? 'PUT' : 'POST', body: JSON.stringify(payload) }, true),
  deleteAlertChannel: (id: string, revision: number) => request<void>(`/api/v1/alerts/channels/${encodeURIComponent(id)}`, { method: 'DELETE', body: JSON.stringify({ revision }) }, true),
  testAlertChannel: (id: string) => request<{ status: string }>(`/api/v1/alerts/channels/${encodeURIComponent(id)}/test`, { method: 'POST' }, true),
  activeAlerts: () => request<{ alerts: AlertItem[] }>('/api/v1/alerts'),
  alertHistory: () => request<{ alerts: AlertItem[] }>('/api/v1/alerts/history'),
  alertEvents: () => request<{ events: AlertEvent[] }>('/api/v1/alerts/events'),
  acknowledgeAlert: (id: string, note = '') => request<void>(`/api/v1/alerts/${encodeURIComponent(id)}/acknowledge`, { method: 'POST', body: JSON.stringify({ note }) }, true),
  silenceAlert: (id: string, until: string, reason: string) => request<void>(`/api/v1/alerts/${encodeURIComponent(id)}/silence`, { method: 'POST', body: JSON.stringify({ until, reason }) }, true),
  alertWindows: () => request<{ windows: AlertWindow[] }>('/api/v1/alerts/windows'),
  createAlertWindow: (payload: Omit<AlertWindow, 'id'>) => request<{ window: AlertWindow }>('/api/v1/alerts/windows', { method: 'POST', body: JSON.stringify(payload) }, true),
  deleteAlertWindow: (id: string) => request<void>(`/api/v1/alerts/windows/${encodeURIComponent(id)}`, { method: 'DELETE' }, true),
  alertDeliveries: () => request<{ deliveries: AlertDelivery[] }>('/api/v1/alerts/deliveries'),
  createContainerTask: (nodeId: string, containerId: string, payload: CreateContainerTaskPayload, idempotencyKey: string) =>
    request<{ taskId: string; status: ContainerTask['status'] }>(
      `/api/v1/nodes/${encodeURIComponent(nodeId)}/containers/${encodeURIComponent(containerId)}/actions`,
      { method: 'POST', headers: { 'Idempotency-Key': idempotencyKey }, body: JSON.stringify(payload) }, true),
  createImagePullTask: (nodeId: string, payload: { imageReference: string; username?: string; password?: string }, idempotencyKey: string) =>
    request<{ taskId: string; status: ContainerTask['status'] }>(
      `/api/v1/nodes/${encodeURIComponent(nodeId)}/images/pull`,
      { method: 'POST', headers: { 'Idempotency-Key': idempotencyKey }, body: JSON.stringify(payload) }, true),
  createImageDeleteTask: (nodeId: string, imageID: string, idempotencyKey: string) =>
    request<{ taskId: string; status: ContainerTask['status'] }>(
      `/api/v1/nodes/${encodeURIComponent(nodeId)}/images/${encodeURIComponent(imageID)}`,
      { method: 'DELETE', headers: { 'Idempotency-Key': idempotencyKey },
        body: JSON.stringify({ deleteConfirmed: true, confirmationId: imageID }) }, true),
  planContainerRebuild: (nodeId: string, containerId: string, spec: RebuildSpec) =>
    request<ContainerRebuildPlan>(
      `/api/v1/nodes/${encodeURIComponent(nodeId)}/containers/${encodeURIComponent(containerId)}/rebuild/plan`,
      { method: 'POST', body: JSON.stringify({ spec }) }, true),
  nodeContainer: (nodeId: string, containerId: string) =>
    request<{ type: 'node_container'; nodeId: string; container: DockerContainerRecord }>(
      `/api/v1/nodes/${encodeURIComponent(nodeId)}/containers/${encodeURIComponent(containerId)}`,
    ),
  revokeSession: (id: string) => request<void>(`/api/v1/auth/sessions/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  }, true),
  createTerminal: (nodeId: string, target: { targetKind: 'host' } | { targetKind: 'container'; containerId: string }) =>
    request<TerminalAuthorization>(`/api/v1/nodes/${encodeURIComponent(nodeId)}/terminals`, {
      method: 'POST', body: JSON.stringify(target),
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
