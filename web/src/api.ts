import { resolveOperation, operationUpload, validateSchema, parseExactJSON, decodeContractJSON, type BodySchema, type OperationInput } from './operations'
import { requestIntent, intentKey, finishIntent } from './operation-intent'
import { t, diagnostic } from "./i18n"
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
  status: 'queued' | 'running' | 'succeeded' | 'failed' | 'timed_out' | 'canceled' | 'unknown' | string
  phase: string
  message?: string
  nodeId?: string
  dockerState?: string
  createdAt: string
  updatedAt: string
  finishedAt?: string
}

export interface TailscaleDeployPayload {
  peerIdentity: string
  displayName: string
  fileRoot?: string
  disableFileRoot?: boolean
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
  kind: 'http' | 'https' | 'tcp' | 'icmp'
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

export type AlertKind = 'node_offline' | 'cpu' | 'memory' | 'disk' | 'docker_unavailable' | 'container_state' | 'probe_state' | 'metric' | 'probe_latency' | 'probe_packet_loss' | 'probe_failure' | 'asset_expiry' | 'traffic_quota'
export interface AlertRule {
  id: string; name: string; kind: AlertKind; nodeId: string; subjectId?: string
  severity: 'info' | 'warning' | 'critical'; threshold?: number; durationSeconds: number
  cooldownSeconds: number; expectedState?: string; channelIds: string[]; enabled: boolean; revision: number
}
export interface AlertChannel {
  id: string; name: string; kind: 'webhook' | 'smtp'; enabled: boolean; hasSecret: boolean; revision: number
  config: { webhookUrl?: string; messageTemplate?: string; smtpHost?: string; smtpPort?: number; smtpFrom?: string; smtpTo?: string; smtpUsername?: string; smtpSecurityMode?: 'starttls' | 'implicit_tls' }
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

export interface DockerNetwork {
  name: string
  id?: string
  ipv4?: string
  ipv6?: string
  gateway?: string
  ipv6Gateway?: string
  aliases?: string[]
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
  hostNetwork: boolean
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
  networks: DockerNetwork[]
  mounts: Array<{ type: string; name?: string; source?: string; destination: string; driver?: string; mode?: string; propagation?: string; readWrite: boolean }>
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
  sshHost: string
  sshPort: number
  sshUser: string
  group: string
  sortOrder: number
  visible: boolean
  pinned: boolean
}

export interface DashboardSettings {
  viewMode: 'monitor' | 'manage'
  groupBy: 'node' | 'compose' | 'state' | 'none'
  sortBy: 'custom' | 'name' | 'state'
  nodeGroupBy: 'group' | 'status' | 'none'
  nodeSortBy: 'custom' | 'name' | 'status'
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
  action: 'start' | 'stop' | 'restart' | 'pause' | 'resume' | 'delete' | 'rename' | 'container_create' | 'image_pull' | 'image_delete' | 'rebuild' | 'rebuild_cleanup' | 'compose_start' | 'compose_stop' | 'compose_restart' | 'compose_deploy' | 'compose_config_save'
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

export interface ContainerCreatePort {
  containerPort: number
  protocol: 'tcp' | 'udp'
  hostIp?: string
  hostPort?: string
}

export interface ContainerCreateMount {
  type: 'bind' | 'volume'
  source?: string
  target: string
  readOnly?: boolean
}

export interface ContainerCreateSpec {
  image: string
  name: string
  command?: string[]
  environment?: string[]
  ports?: ContainerCreatePort[]
  mounts?: ContainerCreateMount[]
  network?: string
  restartPolicy: 'no' | 'always' | 'unless-stopped' | 'on-failure'
  restartRetries?: number
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

export type FileTaskAction = 'mkdir' | 'rename' | 'delete' | 'save_text'
export interface CreateFileTaskPayload {
  action: FileTaskAction
  path: string
  newPath?: string
  expectedVersion?: string
  deleteConfirmed?: boolean
  confirmationPath?: string
  content?: string
}

interface AuthResponse {
  initialCredentials?: { username: string; password?: string }
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
export const SESSION_EXPIRED_EVENT = 'nodedance:session-expired'

function isAnonymousAuthRequest(name: string): boolean {
  return ['auth.csrf', 'auth.status', 'auth.setup', 'auth.login'].includes(name)
}

function clearExpiredSession(path: string) {
  csrfToken = ''
  if (!isAnonymousAuthRequest(path)) {
    window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT))
  }
}

export async function request<T>(path: string, init: RequestInit = {}, csrf = false, timeoutMs = 60_000, name = '', binary = false, responseSchema?: BodySchema): Promise<T> {
  const controller = new AbortController()
  const timeout = window.setTimeout(() => controller.abort(new DOMException('Request timed out', 'TimeoutError')), timeoutMs)
  const abortFromCaller = () => controller.abort(init.signal?.reason)
  if (init.signal?.aborted) abortFromCaller()
  else init.signal?.addEventListener('abort', abortFromCaller, { once: true })

  try {
    const headers = new Headers(init.headers)
    const isFormData = typeof FormData !== 'undefined' && init.body instanceof FormData

    if (init.body !== undefined && !isFormData && !headers.has('Content-Type')) {
      headers.set('Content-Type', 'application/json')
    }
    if (csrf) {
      if (!csrfToken) {
        const result = await operationRequest<{ token: string }>('auth.csrf', {}, { signal: controller.signal })
        csrfToken = result.token
      }
      headers.set('X-CSRF-Token', csrfToken)
    }

    const response = await fetch(path, {
      ...init,
      headers,
      signal: controller.signal,
      credentials: 'include',
      cache: 'no-store',
    })

    if (response.status === 401) clearExpiredSession(name)

    if (!response.ok) {
      let message = t('m.ad5d5a8f24fc', {"0": (response.status)})
      const body = await response.text()
      try {
        const payload = decodeContractJSON(body,responseSchema) as { error?: unknown; message?: unknown }
        if (typeof payload.message === 'string') message = diagnostic(payload.message)
        else if (typeof payload.error === 'string') message = diagnostic(payload.error)
      } catch {
        if (body.trim()) message = diagnostic(body.trim().slice(0,4096))
      }
      throw new ApiError(response.status, message)
    }

    if (response.status === 204) {
      if (responseSchema?.required?.length) throw new ApiError(response.status, t('m.e03b23b49908'))
      return undefined as T
    }
    if (binary) return await response.blob() as T
    const body = await response.text()
    if (!body.trim()) throw new ApiError(response.status, t('m.e03b23b49908'))
    let payload: unknown
    try {
      payload = decodeContractJSON(body,responseSchema)
    } catch {
      throw new ApiError(response.status, t('m.e03b23b49908'))
    }
    if ((name === 'auth.setup' || name === 'auth.login') &&
      typeof payload === 'object' && payload !== null && 'csrfToken' in payload &&
      typeof payload.csrfToken === 'string') {
      csrfToken = payload.csrfToken
    }
    return payload as T
  } finally {
    window.clearTimeout(timeout)
    init.signal?.removeEventListener('abort', abortFromCaller)
  }
}

export async function operationRequest<T>(name: string, input: OperationInput = {}, init: RequestInit = {}, timeoutMs = 60_000): Promise<T> {
  const { operation, path } = await resolveOperation(name, input)
  const mutation = operation.method !== 'GET' && operation.method !== 'HEAD'
  const headers = new Headers(init.headers)
  if(typeof init.body==='string') {
    validateSchema(operation.bodySchema,parseExactJSON(init.body))
    if(operation.maxBodyBytes && new TextEncoder().encode(init.body).length>operation.maxBodyBytes)throw new Error('Body exceeds Core request limit')
  }
  let intent: string | undefined
  if (operation.requiresIdempotencyKey && !headers.has('Idempotency-Key')) {
    intent = await requestIntent(name,input,init.body,headers)
    headers.set('Idempotency-Key',intentKey(intent))
  }
  for(const [key,definition] of Object.entries(operation.headers ?? {})){const value=headers.get(key);if(value!==null)validateSchema(definition,value,'headers.'+key)}
  if (operation.transport === 'upload' && !headers.has('Content-Type')) headers.set('Content-Type', 'application/octet-stream')
  const result = await request<T>(path, { ...init, headers, method: operation.method }, mutation, timeoutMs, name, operation.transport === 'download', operation.responseSchema)
  if(result !== undefined) validateSchema(operation.responseSchema,result)
  if(intent) finishIntent(intent)
  return result
}

export const api = {
  setupStatus: () => operationRequest<SetupStatus>("auth.status", {}),
  setup: (payload: { credential: string; username?: string; password?: string; displayName?: string }) => {
    csrfToken = ''
    return operationRequest<AuthResponse>("auth.setup", {}, {
      body: JSON.stringify(payload),
    })
  },
  login: (username: string, password: string) => {
    csrfToken = ''
    return operationRequest<AuthResponse>("auth.login", {}, {
      body: JSON.stringify({ username, password }),
    })
  },
  me: () => operationRequest<{ user: User }>("auth.me", {}),
  logout: async () => {
    const result = await operationRequest<void>("auth.logout", {})
    csrfToken = ''
    return result
  },

  changePassword: async (currentPassword: string, newPassword: string) => {
    const result = await operationRequest<void>("auth.password", {}, {
      body: JSON.stringify({ currentPassword, newPassword }),
    })
    csrfToken = ''
    return result
  },
  sessions: () => operationRequest<{ sessions: Session[] }>("sessions.list", {}),
  tailscalePeers: () => operationRequest<TailscaleDiscoveryResponse>("discovery.peers", {}),
  probeTailscaleSSH: (peerIdentity: string) => operationRequest<{ fingerprint: string }>("discovery.host-key", {}, {
    body: JSON.stringify({ peerIdentity }),
  }),
  createAgentEnrollment: (displayName: string, coreUrl: string, deploymentMethod: 'native' | 'docker') => operationRequest<{ nodeId: string; displayName: string; token: string; expiresAt: string; expiresInSeconds: number; installCommand: string; nativeInstallCommand: string; dockerInstallCommand: string; deploymentMethod: 'native' | 'docker' }>("agents.enrollment", {}, {
    body: JSON.stringify({ displayName, coreUrl, deploymentMethod }),
  }),
  startTailscaleDeployment: (payload: TailscaleDeployPayload, idempotencyKey: string) => operationRequest<TailscaleDeploymentTask>("deployments.create", {}, {
    headers: { 'Idempotency-Key': idempotencyKey }, body: JSON.stringify(payload),
  }),
  tailscaleDeployments: () => operationRequest<{ tasks: TailscaleDeploymentTask[] }>("deployments.list", {}),
  tailscaleDeployment: (taskId: string) => operationRequest<TailscaleDeploymentTask>("deployments.get", { params: { taskId: String(taskId) } }),
  resolveTailscaleDeployment: (taskId: string, payload: { outcome: 'succeeded' | 'failed'; observedState: string }) =>
    operationRequest<TailscaleDeploymentTask>("deployments.resolve", { params: { taskId: String(taskId) } }, {
      body: JSON.stringify({ ...payload, confirmed: true }),
    }),
  nodes: () => operationRequest<AgentNodesResponse>("nodes.list", {}),
  nodeMetrics: (nodeId: string) => operationRequest<AgentMetricsResponse>("nodes.metrics", { params: { nodeId: String(nodeId) } }),
  nodeContainers: (nodeId: string) => operationRequest<DockerInventoryMessage>("containers.list", { params: { nodeId: String(nodeId) } }),
  dashboardSettings: () => operationRequest<DashboardSettings>("dashboard.settings", {}),
  saveDashboardSettings: (settings: DashboardSettings) => operationRequest<void>("dashboard.save-settings", {}, {
    body: JSON.stringify(settings),
  }),
  nodePreferences: (nodeId: string) => operationRequest<{ preferences: DashboardPreference[] }>("nodes.preferences", { params: { nodeId: String(nodeId) } }),
  saveNodePreference: (nodeId: string, preference: DashboardPreference) => operationRequest<void>("nodes.save-preference", { params: { nodeId: String(nodeId) } }, { body: JSON.stringify(preference) }),
  nodeHistory: (nodeId: string, resolution: 'minute' | 'hour', from: Date, to: Date) => {
    const query = new URLSearchParams({ resolution, from: from.toISOString(), to: to.toISOString() })
    return operationRequest<MetricHistory>("nodes.history", { params: { nodeId: String(nodeId) }, query: Object.fromEntries(query) })
  },
  nodeImages: (nodeId: string, filter = '', page = 0) => operationRequest<DockerImagePage>("images.list", { params: { nodeId: String(nodeId) }, query: { filter: String(filter), page: String(page), pageSize: "50" } }),
  nodeTasks: (nodeId: string) => operationRequest<{ tasks: ContainerTask[]; nextCursor: string }>("tasks.list", { params: { nodeId: String(nodeId) }, query: { limit: "50" } }),
  nodeTask: (nodeId: string, taskId: string) => operationRequest<ContainerTask>("tasks.get", { params: { nodeId: String(nodeId), taskId: String(taskId) } }),
  nodeTaskAudit: (nodeId: string, taskId: string) => operationRequest<{ events: TaskAuditEvent[] }>("tasks.audit", { params: { nodeId: String(nodeId), taskId: String(taskId) } }),
  cancelNodeTask: (nodeId: string, taskId: string) => operationRequest<ContainerTask & { cancelRequested?: boolean }>("tasks.cancel", { params: { nodeId: String(nodeId), taskId: String(taskId) } }),
  serviceProbes: () => operationRequest<{ probes: ServiceProbe[] }>("probes.list", {}),
  createServiceProbe: (payload: ServiceProbePayload) => operationRequest<ServiceProbe>("probes.create", {}, {
    body: JSON.stringify(payload),
  }),
  updateServiceProbe: (id: string, payload: ServiceProbePayload) => operationRequest<ServiceProbe>("probes.update", { params: { id: String(id) } }, {
    body: JSON.stringify(payload),
  }),
  deleteServiceProbe: (id: string, revision: number) => operationRequest<void>("probes.delete", { params: { id: String(id) }, query: { revision: String(revision) } }),
  serviceProbeHistory: (id: string) => operationRequest<{ runs: ServiceProbeRun[] }>("probes.history", { params: { id: String(id) }, query: { limit: "100" } }),
  alertRules: (nodeId = '') => operationRequest<{ rules: AlertRule[] }>('rules.list', { query: nodeId ? { nodeId } : {} }),
  createAlertRule: (payload: Omit<AlertRule, 'id' | 'revision'>) => operationRequest<{ rule: AlertRule }>("rules.create", {}, { body: JSON.stringify(payload) }),
  updateAlertRule: (id: string, payload: AlertRule) => operationRequest<{ rule: AlertRule }>("rules.update", { params: { id: String(id) } }, { body: JSON.stringify(payload) }),
  deleteAlertRule: (id: string, revision: number) => operationRequest<void>("rules.delete", { params: { id: String(id) } }, { body: JSON.stringify({ revision }) }),
  ensureDefaultAlertRules: (nodeId: string) => operationRequest<{ rules: AlertRule[] }>("rules.defaults", {}, { body: JSON.stringify({ nodeId }) }),
  alertChannels: () => operationRequest<{ channels: AlertChannel[] }>("channels.list", {}),
  saveAlertChannel: (payload: { name: string; kind: 'webhook' | 'smtp'; config: AlertChannel['config']; secret?: string; enabled: boolean; revision?: number }, id?: string) =>
    operationRequest<{ channel: AlertChannel }>(id ? 'channels.update' : 'channels.create', { params: id ? { id } : {} }, { body: JSON.stringify(payload) }),
  deleteAlertChannel: (id: string, revision: number) => operationRequest<void>("channels.delete", { params: { id: String(id) } }, { body: JSON.stringify({ revision }) }),
  testAlertChannel: (id: string) => operationRequest<{ status: string }>("channels.test", { params: { id: String(id) } }),
  activeAlerts: () => operationRequest<{ alerts: AlertItem[] }>("alerts.list", {}),
  alertHistory: () => operationRequest<{ alerts: AlertItem[] }>("alerts.history", {}),
  alertEvents: () => operationRequest<{ events: AlertEvent[] }>("alerts.events", {}),
  acknowledgeAlert: (id: string, note = '') => operationRequest<void>("alerts.acknowledge", { params: { id: String(id) } }, { body: JSON.stringify({ note }) }),
  silenceAlert: (id: string, until: string, reason: string) => operationRequest<void>("alerts.silence", { params: { id: String(id) } }, { body: JSON.stringify({ until, reason }) }),
  alertWindows: () => operationRequest<{ windows: AlertWindow[] }>("windows.list", {}),
  createAlertWindow: (payload: Omit<AlertWindow, 'id'>) => operationRequest<{ window: AlertWindow }>("windows.create", {}, { body: JSON.stringify(payload) }),
  deleteAlertWindow: (id: string) => operationRequest<void>("windows.delete", { params: { id: String(id) } }),
  alertDeliveries: () => operationRequest<{ deliveries: AlertDelivery[] }>("deliveries.list", {}),
  createContainerTask: (nodeId: string, containerId: string, payload: CreateContainerTaskPayload) =>
    operationRequest<{ taskId: string; status: ContainerTask['status'] }>("containers.action", { params: { nodeId: String(nodeId), containerId: String(containerId) } }, { body: JSON.stringify(payload) }),
  createContainerCreateTask: (nodeId: string, payload: ContainerCreateSpec) =>
    operationRequest<{ taskId: string; status: ContainerTask['status'] }>("containers.create", { params: { nodeId: String(nodeId) } }, { body: JSON.stringify(payload) }),
  nodeFiles: (nodeId: string, path = '/') => operationRequest<{ entries: NodeFileEntry[]; path: string }>("files.list", { params: { nodeId: String(nodeId) }, query: { path: String(path) } }),
  nodeFileText: (nodeId: string, path: string) => operationRequest<{ path: string; text: string; version: string; size: number }>("files.text", { params: { nodeId: String(nodeId) }, query: { path: String(path) } }),
  nodeFileStat: (nodeId: string, path: string) => operationRequest<NodeFileEntry>("files.stat", { params: { nodeId: String(nodeId) }, query: { path: String(path) } }),
  createFileTask: (nodeId: string, payload: CreateFileTaskPayload) =>
    operationRequest<{ taskId: string; status: ContainerTask['status']; action: string }>("files.action", { params: { nodeId: String(nodeId) } }, { body: JSON.stringify(payload) }),
  uploadNodeFile: (nodeId: string, path: string, file: File, expectedVersion = '') =>
    operationRequest<ContainerTask>("files.upload", { params: { nodeId: String(nodeId) }, query: { path: String(path) } }, {
      headers: { 'Content-Type': 'application/octet-stream', 'X-File-Version': expectedVersion },
      body: file,
    }, 30 * 60_000),
  createImagePullTask: (nodeId: string, payload: { imageReference: string; username?: string; password?: string }) =>
    operationRequest<{ taskId: string; status: ContainerTask['status'] }>("images.pull", { params: { nodeId: String(nodeId) } }, { body: JSON.stringify(payload) }),
  createImageDeleteTask: (nodeId: string, imageID: string) =>
    operationRequest<{ taskId: string; status: ContainerTask['status'] }>("images.delete", { params: { nodeId: String(nodeId), imageId: String(imageID) } }, {
        body: JSON.stringify({ deleteConfirmed: true, confirmationId: imageID }) }),
  planContainerRebuild: (nodeId: string, containerId: string, spec: RebuildSpec) =>
    operationRequest<ContainerRebuildPlan>("containers.rebuild-plan", { params: { nodeId: String(nodeId), containerId: String(containerId) } }, { body: JSON.stringify({ spec }) }),
  nodeContainer: (nodeId: string, containerId: string) =>
    operationRequest<{ type: 'node_container'; nodeId: string; container: DockerContainerRecord }>("containers.get", { params: { nodeId: String(nodeId), containerId: String(containerId) } }),
  revokeSession: (id: string) => operationRequest<void>("sessions.revoke", { params: { id: String(id) } }),
  createTerminal: (nodeId: string, target: { targetKind: 'host' } | { targetKind: 'container'; containerId: string }) =>
    operationRequest<TerminalAuthorization>("terminal.create", { params: { nodeId: String(nodeId) } }, {
      body: JSON.stringify(target),
    }),
  resumeTerminal: (nodeId: string, streamId: string) =>
    operationRequest<TerminalAuthorization>("terminal.resume", { params: { nodeId, streamId } }, { body: JSON.stringify({}) }),
  cancelTerminal: (nodeId: string, streamId: string) =>
    operationRequest<void>("terminal.cancel", { params: { nodeId: String(nodeId), streamId: String(streamId) } }),
  appearance: () => operationRequest<Appearance>("appearance.get", {}),
  saveAppearance: (payload: Pick<Appearance, 'displayName' | 'theme' | 'backgroundColor'>) =>
    operationRequest<void>("appearance.update", {}, {
      body: JSON.stringify(payload),
    }),
  uploadImage: (kind: 'avatar' | 'background', image: File) => {
    return resolveOperation(`appearance.upload-${kind}`).then(({operation})=>operationRequest<void>(operation.name,{}, {body:operationUpload(operation,image)}))
  },
}
