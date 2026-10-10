<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api, type AgentNode, type AgentNodesResponse, type ContainerTask, type ContainerTaskAction, type CreateContainerTaskPayload, type DashboardPreference, type DashboardSettings, type DockerContainer, type DockerInventory, type DockerInventoryMessage, type MetricHistory, type NodeStatusResponse, type TaskAuditEvent } from '../api'
import type { MetricsView } from '../metrics-contract'
import ContainerRebuildWizard from './ContainerRebuildWizard.vue'
import ContainerStreams from './ContainerStreams.vue'
import HistoricalMetrics from './HistoricalMetrics.vue'
import ImagesPanel from './ImagesPanel.vue'
import ComposeProjects from './ComposeProjects.vue'
import MetricsPanel from './MetricsPanel.vue'
import PreferenceEditor from './PreferenceEditor.vue'
import TerminalConsole from './TerminalConsole.vue'
import NodeServiceProbes from './NodeServiceProbes.vue'
import NodeFiles from './NodeFiles.vue'
import VpsCard from './VpsCard.vue'

interface NodeClock {
  status: string
  generation: number
  serverTime: string
  leaseValidUntil?: string
  startedAt: number
}

const nodes = ref<AgentNode[]>([])
const selectedNodeID = ref('')
const views = ref<Record<string, MetricsView>>({})
const dockerViews = ref<Record<string, DockerInventory>>({})
const clocks = ref<Record<string, NodeClock>>({})
const nodeReasons = ref<Record<string, string>>({})
const taskViews = ref<Record<string, ContainerTask>>({})
const taskSubmitting = ref<Record<string, boolean>>({})
const taskErrors = ref<Record<string, string>>({})
const taskAuditViews = ref<Record<string, TaskAuditEvent[]>>({})
const taskAuditVisible = ref<Record<string, boolean>>({})
const taskAuditLoading = ref<Record<string, boolean>>({})
const taskAuditErrors = ref<Record<string, string>>({})
const preferenceIdentities = ref<Record<string, Record<string, string>>>({})
const nodePreferences = ref<Record<string, DashboardPreference[]>>({})
const dashboardSettings = ref<DashboardSettings>({ viewMode: 'monitor', groupBy: 'node', sortBy: 'custom', nodeGroupBy: 'status', nodeSortBy: 'custom', featuredLimit: 4, customFields: ['state', 'ports', 'health'] })
const activeSection = ref<'overview' | 'docker' | 'images' | 'compose' | 'files' | 'history' | 'events' | 'settings'>('overview')
const sectionTabs: Array<{ id: typeof activeSection.value; label: string }> = [
  { id: 'overview', label: '总览' }, { id: 'docker', label: 'Docker 详情' }, { id: 'images', label: '镜像' },
  { id: 'compose', label: 'Compose 项目' }, { id: 'files', label: '文件管理' }, { id: 'history', label: '历史监控' }, { id: 'events', label: '事件历史' }, { id: 'settings', label: '节点设置' },
]
const customFieldOptions = [
  { id: 'state', label: '运行状态' }, { id: 'ports', label: '端口' }, { id: 'health', label: '健康状态' },
  { id: 'image', label: '镜像' }, { id: 'uptime', label: '运行时间' },
]
const serverSearch = ref('')
const containerSearch = ref('')
const editingPreference = ref('')
const historyView = ref<MetricHistory | null>(null)
const historyLoading = ref(false)
const historyError = ref('')
const historyRange = ref<'6h' | '24h' | '7d' | '30d' | '1y'>('24h')
const dashboardSettingsSaving = ref(false)
const dashboardSettingsMessage = ref('')
const dashboardSettingsError = ref('')
const draggedContainerID = ref('')
const dropContainerID = ref('')
const orderSaving = ref(false)
const orderError = ref('')
const busy = ref(true)
const error = ref('')
const elapsed = ref(0)
const wallClockNow = ref(Date.now())
const socketState = ref<'connecting' | 'connected' | 'retrying'>('connecting')
let socket: WebSocket | undefined
let reconnectTimer: ReturnType<typeof setTimeout> | undefined
let refreshTimer: ReturnType<typeof setInterval> | undefined
let tickTimer: ReturnType<typeof setInterval> | undefined
let taskRefreshTimer: ReturnType<typeof setInterval> | undefined
let reconnectDelay = 1000
let disposed = false
let latestNodeListServerTime = Number.NEGATIVE_INFINITY
let pointerDrag: { pointerId: number; sourceID: string; targetID: string; startX: number; startY: number; armed: boolean } | undefined

const selectedNode = computed(() => nodes.value.find((node) => node.nodeId === selectedNodeID.value) ?? null)
const selectedView = computed(() => views.value[selectedNodeID.value] ?? null)
const selectedDocker = computed(() => dockerViews.value[selectedNodeID.value] ?? null)
const selectedPreferences = computed(() => nodePreferences.value[selectedNodeID.value] ?? [])
const selectedNodePreference = computed(() => selectedPreferences.value.find((item) => item.targetKind === 'node' && item.identity === `node:${selectedNodeID.value}`) ?? null)
const selectedNodeTitle = computed(() => selectedNodePreference.value?.alias || selectedNode.value?.displayName || '')
const selectedTasks = computed(() => Object.values(taskViews.value).filter((task) => task.nodeId === selectedNodeID.value)
  .sort((left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt)))
const historyResolution = computed(() => historyRange.value === '1y' ? 'hour' : 'minute')
const activeTerminal = ref<{ nodeId: string; targetKind: 'host' | 'container'; containerId?: string; targetLabel: string } | null>(null)

function preferenceForContainer(record: DockerInventory['containers'][number]): DashboardPreference | undefined {
  const identity = preferenceIdentities.value[selectedNodeID.value]?.[record.container.id]
  if (!identity) return undefined
  const kind = record.container.compose ? 'compose_service' : 'container'
  return selectedPreferences.value.find((item) => item.targetKind === kind && item.identity === identity)
}

function containerTitle(record: DockerInventory['containers'][number]): string {
  return preferenceForContainer(record)?.alias || record.container.name || record.container.id.slice(0, 12)
}

function containerGroupName(record: DockerInventory['containers'][number]): string {
  const container = record.container
  if (dashboardSettings.value.groupBy === 'none') return ''
  if (dashboardSettings.value.groupBy === 'compose') return container.compose ? `Compose · ${container.compose.project}` : '独立容器'
  if (dashboardSettings.value.groupBy === 'state') return container.state || '状态未知'
  return selectedNodeTitle.value
}

const orderedContainers = computed(() => {
  const containers = [...(selectedDocker.value?.containers ?? [])]
  const groupOrder = (record: DockerInventory['containers'][number]) => containerGroupName(record)
  return containers.sort((left, right) => {
    const group = groupOrder(left).localeCompare(groupOrder(right), 'zh-CN')
    if (group !== 0 && dashboardSettings.value.groupBy !== 'none') return group
    const leftPreference = preferenceForContainer(left)
    const rightPreference = preferenceForContainer(right)
    if (dashboardSettings.value.sortBy === 'state') {
      const state = (left.container.state || '').localeCompare(right.container.state || '')
      if (state !== 0) return state
    }
    if (dashboardSettings.value.sortBy === 'name') return containerTitle(left).localeCompare(containerTitle(right), 'zh-CN')
    if (leftPreference?.pinned !== rightPreference?.pinned) return leftPreference?.pinned ? -1 : 1
    if ((leftPreference?.sortOrder ?? 0) !== (rightPreference?.sortOrder ?? 0)) return (leftPreference?.sortOrder ?? 0) - (rightPreference?.sortOrder ?? 0)
    return containerTitle(left).localeCompare(containerTitle(right), 'zh-CN')
  })
})

const sortedContainers = computed(() => {
  const query = containerSearch.value.trim().toLocaleLowerCase()
  if (!query) return orderedContainers.value
  return orderedContainers.value.filter((record) => {
    const preference = preferenceForContainer(record)
    return [containerTitle(record), record.container.name, record.container.image, record.container.id,
      record.container.compose?.project, record.container.compose?.service, preference?.notes]
      .some((value) => value?.toLocaleLowerCase().includes(query))
  })
})

const serverCardGroups = computed(() => {
  const query = serverSearch.value.trim().toLocaleLowerCase()
  const matching = nodes.value.filter((node) => {
    const preference = nodePreference(node)
    if (preference?.visible === false) return false
    const hostname = views.value[node.nodeId]?.metrics.system.hostname.value
    const status = nodeStatusGroup(node)
    return !query || [node.displayName, nodeTitle(node), node.nodeId, preference?.group, preference?.notes, hostname, status]
      .some((value) => value?.toLocaleLowerCase().includes(query))
  })
  const title = (node: AgentNode) => nodeTitle(node)
  const statusRank = (node: AgentNode) => nodeIsOnline(node) ? 0 : isPendingRegistration(node) ? 1 : node.status === 'revoked' ? 3 : 2
  matching.sort((left, right) => {
    if (dashboardSettings.value.nodeSortBy === 'name') return title(left).localeCompare(title(right), 'zh-CN')
    if (dashboardSettings.value.nodeSortBy === 'status') {
      const difference = statusRank(left) - statusRank(right)
      if (difference !== 0) return difference
    } else {
      const leftPreference = nodePreference(left)
      const rightPreference = nodePreference(right)
      const leftPinned = leftPreference?.pinned ?? false
      const rightPinned = rightPreference?.pinned ?? false
      if (leftPinned !== rightPinned) return leftPinned ? -1 : 1
      if ((leftPreference?.sortOrder ?? 0) !== (rightPreference?.sortOrder ?? 0)) {
        return (leftPreference?.sortOrder ?? 0) - (rightPreference?.sortOrder ?? 0)
      }
    }
    return title(left).localeCompare(title(right), 'zh-CN')
  })
  if (dashboardSettings.value.nodeGroupBy === 'none') return [{ key: 'all', label: '', nodes: matching }]
  const groups = new Map<string, AgentNode[]>()
  for (const node of matching) {
    const label = dashboardSettings.value.nodeGroupBy === 'status'
      ? nodeStatusGroup(node)
      : nodePreference(node)?.group.trim() || '未分组'
    const current = groups.get(label) ?? []
    current.push(node)
    groups.set(label, current)
  }
  const statusOrder = ['在线', '等待注册', '离线', '已撤销']
  const labels = [...groups.keys()].sort((left, right) => dashboardSettings.value.nodeGroupBy === 'status'
    ? statusOrder.indexOf(left) - statusOrder.indexOf(right)
    : left.localeCompare(right, 'zh-CN'))
  return labels.map((label) => ({ key: label, label, nodes: groups.get(label) ?? [] }))
})

const selectedNodeTasks = computed(() => selectedTasks.value)

function dockerAvailabilityText(inventory: DockerInventory): string {
  if (inventory.staleReason === 'docker_capability_unavailable') return 'Docker 不可用'
  if (inventory.dockerAvailability === 'available') {
    if (inventory.health?.errorKind === 'api_incompatible') return 'Docker API 不兼容'
    if (dockerIsStale(inventory)) return '数据过期'
    if (inventory.health?.reason) return 'Engine 状态异常'
    return 'Engine 正常'
  }
  if (inventory.dockerAvailability === 'unavailable') return 'Docker 不可用'
  return '等待 Docker 状态'
}

function dockerStatusReason(inventory: DockerInventory): string {
  return inventory.health?.reason || inventory.staleReason || ''
}

function dockerIsStale(inventory: DockerInventory): boolean {
  if (inventory.dataStale || !inventory.agentOnline) return true
  const clock = clocks.value[inventory.nodeId]
  const node = nodes.value.find((item) => item.nodeId === inventory.nodeId)
  return !clock || clock.status !== 'online' ||
    clock.generation !== inventory.activeGeneration ||
    Boolean(node?.agentId && inventory.agentId !== node.agentId) ||
    elapsed.value - clock.startedAt >= remainingLease(clock)
}

function containerHealthText(container: DockerInventory['containers'][number]['container']): string {
  if (!container.healthcheckConfigured || container.health === 'none') return '未配置健康检查'
  const labels: Record<string, string> = {
    healthy: '健康', unhealthy: '异常', starting: '启动中', unknown: '未知',
  }
  return labels[container.health] ?? container.health
}

function portText(port: DockerInventory['containers'][number]['container']['ports'][number]): string {
  const published = Array.isArray(port.published) ? port.published : []
  const configured = Array.isArray(port.configured) ? port.configured : []
  if (published.length > 0) {
    return published.map((binding) => {
      const rawHost = binding.ip || '*'
      const host = rawHost.includes(':') && !rawHost.startsWith('[') ? `[${rawHost}]` : rawHost
      const formattedHost = host.includes(':') && !host.startsWith('[') ? `[${host}]` : host
      const scope = rawHost === '*' || rawHost === '0.0.0.0' || rawHost === '::'
        ? '（所有宿主机接口）'
        : rawHost === '::1' || /^127(?:\.\d{1,3}){3}$/.test(rawHost)
          ? '（仅宿主机回环）'
          : ''
      return `${formattedHost}:${binding.port} → ${port.containerPort}/${port.protocol}${scope}`
    }).join('，')
  }
  if (configured.length > 0) return `${port.containerPort}/${port.protocol}（配置映射，当前未发布）`
  if (port.exposed) return `${port.containerPort}/${port.protocol}（仅声明）`
  return `${port.containerPort}/${port.protocol}`
}

function dockerCanOperate(inventory: DockerInventory | undefined): boolean {
  return Boolean(inventory?.dockerAvailability === 'available' && inventory.staleReason !== 'docker_capability_unavailable')
}

function containerNetworkText(container: DockerInventory['containers'][number]['container']): string {
  if (container.hostNetwork) return 'Host Network：容器共享宿主机网络地址'
  const networks = container.networks ?? []
  if (networks.length === 0) return '容器网络地址：未分配或不可用'
  return networks.map((network) => {
    const addresses = [network.ipv4 && `IPv4 ${network.ipv4}`, network.ipv6 && `IPv6 ${network.ipv6}`].filter(Boolean)
    return `${network.name}：${addresses.length ? addresses.join(' · ') : '未分配 IP'}`
  }).join('；')
}

function remainingLease(clock: NodeClock): number {
  if (!clock.leaseValidUntil) return 0
  const from = Date.parse(clock.serverTime)
  const until = Date.parse(clock.leaseValidUntil)
  if (!Number.isFinite(from) || !Number.isFinite(until)) return 0
  return Math.max(0, until - from)
}

function nodeIsOnline(node: AgentNode): boolean {
  const clock = clocks.value[node.nodeId]
  if (!clock) return node.status === 'online'
  return clock.status === 'online' && elapsed.value - clock.startedAt < remainingLease(clock)
}

function setNodeReason(nodeID: string, reason?: string) {
  if (reason) {
    nodeReasons.value = { ...nodeReasons.value, [nodeID]: reason }
    return
  }
  if (!(nodeID in nodeReasons.value)) return
  const next = { ...nodeReasons.value }
  delete next[nodeID]
  nodeReasons.value = next
}

function setClock(nodeID: string, status: string, serverTime: string, leaseValidUntil?: string, generation = 0) {
  const incomingTime = Date.parse(serverTime)
  if (!Number.isFinite(incomingTime)) return false
  const previous = clocks.value[nodeID]
  if (previous) {
    if (generation < previous.generation) return false
    const previousTime = Date.parse(previous.serverTime)
    // Reapplying an identical Core snapshot must not restart its client-side
    // lease countdown, especially when an HTTP response arrives late.
    if (generation === previous.generation && Number.isFinite(previousTime) && incomingTime <= previousTime) return false
  }
	clocks.value = {
		...clocks.value,
		[nodeID]: { status, serverTime, leaseValidUntil, generation, startedAt: performance.now() },
	}
	return true
}

function mergeNodeList(payload: AgentNodesResponse): AgentNode[] | undefined {
  const listServerTime = Date.parse(payload.serverTime)
  if (!Number.isFinite(listServerTime) || listServerTime < latestNodeListServerTime) return undefined
  latestNodeListServerTime = listServerTime

  const previousNodes = new Map(nodes.value.map((node) => [node.nodeId, node]))
  const merged = payload.nodes.map((node) => {
    const clock = clocks.value[node.nodeId]
    const clockTime = clock ? Date.parse(clock.serverTime) : Number.NEGATIVE_INFINITY
    const stale = Boolean(clock && (
      node.generation < clock.generation ||
      node.generation === clock.generation && Number.isFinite(clockTime) && listServerTime <= clockTime
    ))
    if (!stale) {
      setClock(node.nodeId, node.status, payload.serverTime, node.leaseValidUntil, node.generation)
      return node
    }

    // Preserve current Core state while retaining node metadata returned by
    // the list endpoint. The per-node clock is newer than this list snapshot.
    return {
      ...node,
      status: clock!.status,
      generation: clock!.generation,
      leaseValidUntil: clock!.leaseValidUntil ?? node.leaseValidUntil,
      lastSeen: previousNodes.get(node.nodeId)?.lastSeen ?? node.lastSeen,
    }
  })
  const included = new Set(merged.map((node) => node.nodeId))
  for (const node of nodes.value) {
    if (included.has(node.nodeId)) continue
    const clock = clocks.value[node.nodeId]
    if (clock && Date.parse(clock.serverTime) > listServerTime) merged.push(node)
  }
  return merged
}

async function refreshNodes() {
  const result = await api.nodes()
  const merged = mergeNodeList(result)
  if (!merged) {
    busy.value = false
    return
  }
  nodes.value = merged
  if (!selectedNodeID.value || !merged.some((node) => node.nodeId === selectedNodeID.value)) {
    selectedNodeID.value = merged[0]?.nodeId ?? ''
  }
  busy.value = false
  const requests: Promise<unknown>[] = []
  for (const node of merged) {
    const selected = node.nodeId === selectedNodeID.value
    if (!(node.nodeId in nodePreferences.value)) requests.push(loadPreferences(node))
    if (!node.agentId) continue
    if (selected || !(node.nodeId in views.value)) requests.push(loadMetrics(node))
    if (selected || !(node.nodeId in dockerViews.value)) requests.push(loadContainers(node))
    requests.push(loadTasks(node))
  }
  await Promise.all(requests)
}

async function loadMetrics(node: AgentNode) {
	try {
		const result = await api.nodeMetrics(node.nodeId)
    acceptMetricsResponse(result)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法加载主机指标。'
  }
}

async function loadContainers(node: AgentNode) {
  try {
    const result = await api.nodeContainers(node.nodeId)
    acceptDockerResponse(result)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法加载 Docker 容器。'
  }
}

async function loadTasks(node: AgentNode) {
  try {
    const result = await api.nodeTasks(node.nodeId)
    const next = { ...taskViews.value }
    for (const [taskId, task] of Object.entries(next)) {
      if (task.nodeId === node.nodeId) delete next[taskId]
    }
    for (const task of result.tasks) next[task.taskId] = task
    taskViews.value = next
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法读取容器操作进度。'
  }
}

async function loadPreferences(node: AgentNode) {
  try {
    const result = await api.nodePreferences(node.nodeId)
    nodePreferences.value = { ...nodePreferences.value, [node.nodeId]: result.preferences }
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法读取展示偏好。'
  }
}

async function loadDashboardSettings() {
  try {
    dashboardSettings.value = await api.dashboardSettings()
  } catch (reason) {
    dashboardSettingsError.value = reason instanceof Error ? reason.message : '无法读取首页设置。'
  }
}

async function saveDashboardSettings() {
  dashboardSettingsSaving.value = true
  dashboardSettingsMessage.value = ''
  dashboardSettingsError.value = ''
  try {
    await api.saveDashboardSettings(dashboardSettings.value)
    dashboardSettingsMessage.value = '首页设置已保存。'
  } catch (reason) {
    dashboardSettingsError.value = reason instanceof Error ? reason.message : '首页设置保存失败。'
  } finally {
    dashboardSettingsSaving.value = false
  }
}

function nodePreferenceForEditor(): DashboardPreference | undefined {
  if (!selectedNode.value) return undefined
  return selectedNodePreference.value ?? {
    nodeId: selectedNode.value.nodeId,
    targetKind: 'node',
    identity: `node:${selectedNode.value.nodeId}`,
    alias: '', icon: 'server', notes: '', serviceUrl: '', group: '', sortOrder: -1,
    visible: true, pinned: true,
  }
}

function containerPreferenceForEditor(record: DockerInventory['containers'][number]): DashboardPreference | undefined {
  const identity = preferenceIdentities.value[selectedNodeID.value]?.[record.container.id]
  if (!selectedNode.value || !identity) return undefined
  const targetKind = record.container.compose ? 'compose_service' : 'container'
  return preferenceForContainer(record) ?? {
    nodeId: selectedNode.value.nodeId,
    targetKind,
    identity,
    alias: '', icon: '', notes: '', serviceUrl: '', group: '',
    sortOrder: selectedDocker.value?.containers.findIndex((item) => item.container.id === record.container.id) ?? 0,
    visible: true, pinned: false,
  }
}

function acceptSavedPreference(preference: DashboardPreference) {
  const existing = nodePreferences.value[preference.nodeId] ?? []
  const without = existing.filter((item) => !(item.targetKind === preference.targetKind && item.identity === preference.identity))
  nodePreferences.value = { ...nodePreferences.value, [preference.nodeId]: [...without, preference] }
}

function safeServiceURL(record: DockerInventory['containers'][number]): string {
  const candidate = preferenceForContainer(record)?.serviceUrl
  if (!candidate) return ''
  try {
    const url = new URL(candidate)
    return (url.protocol === 'http:' || url.protocol === 'https:') && !url.username && !url.password && !url.hash ? url.href : ''
  } catch {
    return ''
  }
}

function nodePreference(node: AgentNode): DashboardPreference | undefined {
  return nodePreferences.value[node.nodeId]?.find((item) => item.targetKind === 'node' && item.identity === `node:${node.nodeId}`)
}

function nodeTitle(node: AgentNode): string {
  return nodePreference(node)?.alias || node.displayName
}

function nodeStatusGroup(node: AgentNode): string {
  if (nodeIsOnline(node)) return '在线'
  if (isPendingRegistration(node)) return '等待注册'
  return node.status === 'revoked' ? '已撤销' : '离线'
}

function containerUptime(record: DockerInventory['containers'][number]): string {
  if (!record.container.running || !record.container.startedAt) return '未知'
  const started = Date.parse(record.container.startedAt)
  if (!Number.isFinite(started) || started > Date.now()) return '未知'
  const minutes = Math.floor((Date.now() - started) / 60_000)
  const days = Math.floor(minutes / (60 * 24))
  const hours = Math.floor((minutes % (60 * 24)) / 60)
  const remaining = minutes % 60
  if (days > 0) return `${days} 天 ${hours} 小时`
  if (hours > 0) return `${hours} 小时 ${remaining} 分钟`
  return `${remaining} 分钟`
}

function tasksForNode(nodeID: string): ContainerTask[] {
  return Object.values(taskViews.value).filter((task) => task.nodeId === nodeID)
}

function containerActionKey(nodeID: string, containerID: string): string {
  return `${nodeID}:${containerID}`
}

async function loadHistory() {
  const node = selectedNode.value
  if (!node) return
  historyLoading.value = true
  historyError.value = ''
  const durations: Record<typeof historyRange.value, number> = {
    '6h': 6 * 60 * 60_000,
    '24h': 24 * 60 * 60_000,
    '7d': 7 * 24 * 60 * 60_000,
    '30d': 30 * 24 * 60 * 60_000,
    '1y': 365 * 24 * 60 * 60_000,
  }
  try {
    historyView.value = await api.nodeHistory(node.nodeId, historyResolution.value, new Date(Date.now() - durations[historyRange.value]), new Date())
  } catch (reason) {
    historyView.value = null
    historyError.value = reason instanceof Error ? reason.message : '无法读取历史指标。'
  } finally {
    historyLoading.value = false
  }
}

function selectSection(section: typeof activeSection.value) {
  activeSection.value = section
  if (section === 'history') void loadHistory()
  if (section === 'events' && selectedNode.value) void loadTasks(selectedNode.value)
}

function serviceLinkTitle(record: DockerInventory['containers'][number]): string {
  const identity = preferenceIdentities.value[selectedNodeID.value]?.[record.container.id]
  return identity ? '为此容器或 Compose 服务设置展示偏好' : 'Docker 未提供稳定身份，不能绑定展示偏好'
}

async function persistReorder(sourceID: string, targetID: string) {
  if (!selectedNode.value || sourceID === targetID || dashboardSettings.value.sortBy !== 'custom') return
  const source = orderedContainers.value.find((item) => item.container.id === sourceID)
  const target = orderedContainers.value.find((item) => item.container.id === targetID)
  const sourceIdentity = preferenceIdentities.value[selectedNodeID.value]?.[sourceID]
  const targetIdentity = preferenceIdentities.value[selectedNodeID.value]?.[targetID]
  if (!source || !target || !sourceIdentity || !targetIdentity || sourceIdentity === targetIdentity ||
    containerGroupName(source) !== containerGroupName(target)) return
  // Compose replicas share a service preference, so reorder stable preference
  // identities rather than container IDs. This also keeps hidden search rows
  // in their saved positions when the visible subset is dragged.
  const identities = [...new Set(orderedContainers.value
    .map((record) => preferenceIdentities.value[selectedNodeID.value]?.[record.container.id])
    .filter((identity): identity is string => Boolean(identity)))]
  const from = identities.indexOf(sourceIdentity)
  const to = identities.indexOf(targetIdentity)
  if (from < 0 || to < 0) return
  const [moved] = identities.splice(from, 1)
  identities.splice(to, 0, moved)
  orderSaving.value = true
  orderError.value = ''
  try {
    for (let index = 0; index < identities.length; index += 1) {
      const record = orderedContainers.value.find((item) =>
        preferenceIdentities.value[selectedNodeID.value]?.[item.container.id] === identities[index])
      const preference = record ? containerPreferenceForEditor(record) : undefined
      if (!preference) throw new Error('缺少稳定容器身份，无法保存顺序。')
      const next = { ...preference, sortOrder: index }
      await api.saveNodePreference(next.nodeId, next)
      acceptSavedPreference(next)
    }
  } catch (reason) {
    orderError.value = reason instanceof Error ? reason.message : '容器顺序保存失败。'
  } finally {
    orderSaving.value = false
  }
}

function beginNativeReorder(record: DockerInventory['containers'][number], event: DragEvent) {
  if (dashboardSettings.value.sortBy !== 'custom') {
    event.preventDefault()
    return
  }
  draggedContainerID.value = record.container.id
  event.dataTransfer?.setData('text/plain', record.container.id)
  if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move'
}

function finishNativeReorder(record: DockerInventory['containers'][number]) {
  if (draggedContainerID.value) void persistReorder(draggedContainerID.value, record.container.id)
  draggedContainerID.value = ''
}

function beginPointerReorder(event: PointerEvent, record: DockerInventory['containers'][number]) {
  if (dashboardSettings.value.sortBy !== 'custom') return
  event.preventDefault()
  pointerDrag = { pointerId: event.pointerId, sourceID: record.container.id, targetID: record.container.id, startX: event.clientX, startY: event.clientY, armed: false }
  document.addEventListener('pointermove', movePointerReorder)
  document.addEventListener('pointerup', endPointerReorder, { once: true })
  document.addEventListener('pointercancel', endPointerReorder, { once: true })
}

function movePointerReorder(event: PointerEvent) {
  if (!pointerDrag || event.pointerId !== pointerDrag.pointerId) return
  if (Math.abs(event.clientX - pointerDrag.startX) + Math.abs(event.clientY - pointerDrag.startY) > 12) pointerDrag.armed = true
  if (!pointerDrag.armed) return
  const target = document.elementFromPoint(event.clientX, event.clientY)?.closest<HTMLElement>('[data-container-id]')
  const targetID = target?.dataset.containerId
  if (targetID) {
    pointerDrag.targetID = targetID
    dropContainerID.value = targetID
  }
}

function endPointerReorder(event: PointerEvent) {
  if (!pointerDrag || event.pointerId !== pointerDrag.pointerId) return
  const drag = pointerDrag
  pointerDrag = undefined
  document.removeEventListener('pointermove', movePointerReorder)
  document.removeEventListener('pointerup', endPointerReorder)
  document.removeEventListener('pointercancel', endPointerReorder)
  dropContainerID.value = ''
  if (drag.armed && drag.targetID !== drag.sourceID) void persistReorder(drag.sourceID, drag.targetID)
}

function taskForContainer(containerID: string, nodeID = selectedNodeID.value): ContainerTask | undefined {
  return Object.values(taskViews.value)
    .filter((task) => task.nodeId === nodeID && (task.targetId === containerID ||
      task.action === 'rebuild' && task.status === 'succeeded' && task.result.resourceRevision === containerID))
    .sort((left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt))[0]
}

function rollbackTaskForContainer(containerID: string, nodeID = selectedNodeID.value): ContainerTask | undefined {
  return tasksForNode(nodeID)
    .filter((task) => task.action === 'rebuild' && task.status === 'succeeded' &&
      task.targetId === containerID && Boolean(task.result.resourceRevision) && task.result.resourceRevision !== containerID)
    .sort((left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt))[0]
}

function taskStatusLabel(task: ContainerTask): string {
  if (task.status === 'queued') return '任务已保存，等待 Agent'
  if (task.status === 'running') return `正在执行：${task.progress.phase}`
  if (task.status === 'succeeded') return `已验证完成：${task.result.observedState || 'Docker 状态已确认'}`
  if (task.status === 'unknown') return '结果待确认，系统不会自动重试'
  if (task.status === 'timed_out') return '超时，等待实际状态确认'
  if (task.status === 'canceled') return '任务已取消'
  return `执行失败：${task.result.code || '原因未分类'}`
}

async function toggleTaskAudit(task: ContainerTask) {
  const showing = Boolean(taskAuditVisible.value[task.taskId])
  taskAuditVisible.value = { ...taskAuditVisible.value, [task.taskId]: !showing }
  if (showing || taskAuditViews.value[task.taskId] || taskAuditLoading.value[task.taskId]) return
  taskAuditLoading.value = { ...taskAuditLoading.value, [task.taskId]: true }
  try {
    const response = await api.nodeTaskAudit(task.nodeId, task.taskId)
    taskAuditViews.value = { ...taskAuditViews.value, [task.taskId]: response.events }
  } catch (reason) {
    taskAuditErrors.value = { ...taskAuditErrors.value, [task.taskId]: reason instanceof Error ? reason.message : '无法读取任务审计记录。' }
  } finally {
    taskAuditLoading.value = { ...taskAuditLoading.value, [task.taskId]: false }
  }
}

async function submitContainerAction(node: AgentNode, inventory: DockerInventory | undefined, container: DockerInventory['containers'][number]['container'], action: ContainerTaskAction) {
  const key = containerActionKey(node.nodeId, container.id)
  if (!inventory || inventory.dockerAvailability !== 'available' || !node.agentId || !nodeIsOnline(node) || dockerIsStale(inventory) || container.stale || taskSubmitting.value[key]) return

  let payload: CreateContainerTaskPayload = { action }
  if (action === 'rename') {
    if (container.compose) return
    const newName = window.prompt('输入新的独立容器名称', container.name)
    if (newName === null) return
    payload = { action, newName: newName.trim() }
  } else if (action === 'delete') {
    if (container.running || container.paused) {
      taskErrors.value = { ...taskErrors.value, [key]: '请先单独停止容器，再提交删除任务。' }
      return
    }
    const confirmation = window.prompt(`删除不会删除数据卷。请输入完整容器 ID 以确认：\n${container.id}`)
    if (confirmation === null) return
    if (confirmation.trim() !== container.id) {
      taskErrors.value = { ...taskErrors.value, [key]: '删除未提交：确认内容必须与当前完整容器 ID 完全一致。' }
      return
    }
    payload = { action, deleteConfirmed: true, deleteConfirmationId: confirmation.trim() }
  }

  taskSubmitting.value = { ...taskSubmitting.value, [key]: true }
  const errors = { ...taskErrors.value }
  delete errors[key]
  taskErrors.value = errors
  try {
    const accepted = await api.createContainerTask(node.nodeId, container.id, payload, crypto.randomUUID())
    const current = await api.nodeTask(node.nodeId, accepted.taskId)
    taskViews.value = { ...taskViews.value, [current.taskId]: current }
  } catch (reason) {
    taskErrors.value = { ...taskErrors.value, [key]: reason instanceof Error ? reason.message : '无法创建容器操作任务。' }
  } finally {
    taskSubmitting.value = { ...taskSubmitting.value, [key]: false }
  }
}

function submitCardContainerAction(payload: { node: AgentNode; record: DockerInventory['containers'][number]; action: ContainerTaskAction }) {
  void submitContainerAction(payload.node, dockerViews.value[payload.node.nodeId], payload.record.container, payload.action)
}

function acceptDockerResponse(result: DockerInventoryMessage) {
  const containers = Array.isArray(result.inventory?.containers) ? result.inventory.containers : []
  result = {
    ...result,
    inventory: {
      ...result.inventory,
      containers: containers.map((record) => ({
        ...record,
        container: {
          ...record.container,
          networks: Array.isArray(record.container?.networks) ? record.container.networks : [],
          mounts: Array.isArray(record.container?.mounts) ? record.container.mounts : [],
          ports: Array.isArray(record.container?.ports) ? record.container.ports.map((port) => ({
            ...port,
            configured: Array.isArray(port.configured) ? port.configured : [],
            published: Array.isArray(port.published) ? port.published : [],
          })) : [],
        },
      })),
    },
  }
  const previous = dockerViews.value[result.nodeId]
  if (previous) {
    if (result.inventory.activeGeneration < previous.activeGeneration) return
    if (result.inventory.activeGeneration === previous.activeGeneration) {
      const incomingTime = Date.parse(result.inventory.serverTime)
      const previousTime = Date.parse(previous.serverTime)
      if (Number.isFinite(incomingTime) && Number.isFinite(previousTime) && incomingTime < previousTime) return
      if (incomingTime === previousTime && incomingTime > 0) {
        const incomingSequence = Math.max(0, ...result.inventory.containers.map((item) => item.sequence))
        const previousSequence = Math.max(0, ...previous.containers.map((item) => item.sequence))
        if (incomingSequence < previousSequence) return
      }
    }
  }
  dockerViews.value = { ...dockerViews.value, [result.nodeId]: result.inventory }
  preferenceIdentities.value = { ...preferenceIdentities.value, [result.nodeId]: result.preferenceIdentities ?? {} }
  if (setClock(result.nodeId, result.state.status, result.state.serverTime, result.state.leaseValidUntil, result.state.generation)) {
    setNodeReason(result.nodeId, result.state.reason)
  }
}

function acceptMetricsResponse(result: MetricsView | NodeStatusResponse) {
  if (isNodeStatusResponse(result)) {
    if (setClock(result.nodeId, result.state.status, result.state.serverTime, result.state.leaseValidUntil, result.state.generation)) {
      setNodeReason(result.nodeId, result.state.reason)
    }
    return
  }
	const previous = views.value[result.nodeId]
	if (previous) {
		if (result.activeGeneration < previous.activeGeneration) return
		if (result.activeGeneration === previous.activeGeneration) {
			if (result.generation < previous.generation) return
			if (result.generation === previous.generation && result.sequence < previous.sequence) return
			const incomingTime = Date.parse(result.serverTime)
			const previousTime = Date.parse(previous.serverTime)
			if (result.generation === previous.generation && result.sequence === previous.sequence &&
				Number.isFinite(incomingTime) && Number.isFinite(previousTime) && incomingTime <= previousTime) return
		}
	}
	const previousClock = clocks.value[result.nodeId]
	if (previousClock && result.activeGeneration < previousClock.generation) return
	if (previousClock && result.activeGeneration === previousClock.generation) {
		const incomingTime = Date.parse(result.serverTime)
		const previousTime = Date.parse(previousClock.serverTime)
		if (Number.isFinite(incomingTime) && Number.isFinite(previousTime) && incomingTime < previousTime) return
	}
  const normalized: MetricsView = {
    ...result,
    metrics: {
      ...result.metrics,
      network: {
        ...result.metrics.network,
        interfaces: Array.isArray(result.metrics.network?.interfaces) ? result.metrics.network.interfaces : [],
      },
      disk: {
        ...result.metrics.disk,
        mounts: Array.isArray(result.metrics.disk?.mounts) ? result.metrics.disk.mounts : [],
      },
    },
  }
  views.value = { ...views.value, [normalized.nodeId]: normalized }
  setNodeReason(normalized.nodeId)
  setClock(normalized.nodeId, normalized.nodeStatus, normalized.serverTime, normalized.leaseValidUntil, normalized.activeGeneration)
}

function isNodeStatusResponse(result: MetricsView | NodeStatusResponse): result is NodeStatusResponse {
  return (result as NodeStatusResponse).type === 'node_status'
}

function acceptSocketMessage(raw: string) {
  let event: unknown
  try {
    event = JSON.parse(raw)
  } catch {
    return
  }
  if (typeof event !== 'object' || event === null || !('type' in event) || !('nodeId' in event)) return
  const value = event as { type?: string; nodeId?: string; metrics?: MetricsView; state?: NodeStatusResponse['state'] }
  if (!value.nodeId) return
  if (value.type === 'node_metrics' && value.metrics) {
    acceptMetricsResponse(value.metrics)
    return
  }
  if (value.type === 'node_status' && value.state) {
    if (setClock(value.nodeId, value.state.status, value.state.serverTime, value.state.leaseValidUntil, value.state.generation)) {
      setNodeReason(value.nodeId, value.state.reason)
    }
    return
  }
  if (value.type === 'node_containers') {
    acceptDockerResponse(event as DockerInventoryMessage)
  }
}

function connectDashboard() {
  if (disposed) return
  socketState.value = socketState.value === 'connected' ? 'retrying' : 'connecting'
  const url = new URL('/ws/v1/dashboard', window.location.href)
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  const current = new WebSocket(url)
  socket = current
  current.onopen = () => {
    if (disposed || socket !== current) return
    reconnectDelay = 1000
    socketState.value = 'connected'
    void refreshNodes().catch((reason: unknown) => {
      busy.value = false
      error.value = reason instanceof Error ? reason.message : '无法读取节点列表。'
    })
  }
  current.onmessage = (message) => {
    if (disposed || socket !== current) return
    acceptSocketMessage(String(message.data))
  }
  current.onclose = () => {
    if (socket === current) {
      socket = undefined
      scheduleReconnect()
    }
  }
  current.onerror = () => {
    if (socket === current) current.close()
  }
}

function scheduleReconnect() {
  if (disposed || reconnectTimer !== undefined) return
  socketState.value = 'retrying'
  reconnectTimer = setTimeout(() => {
    reconnectTimer = undefined
    connectDashboard()
  }, reconnectDelay)
  reconnectDelay = Math.min(30_000, reconnectDelay * 2)
}

function chooseNode(node: AgentNode) {
  selectedNodeID.value = node.nodeId
  activeSection.value = dashboardSettings.value.viewMode === 'manage' ? 'docker' : 'overview'
  error.value = ''
  historyView.value = null
  void loadPreferences(node)
  if (node.agentId) {
    void Promise.all([loadMetrics(node), loadContainers(node), loadTasks(node)])
  } else {
    setNodeReason(node.nodeId, 'awaiting_agent_registration')
  }
}

function openHostTerminal(node: AgentNode) {
  activeTerminal.value = { nodeId: node.nodeId, targetKind: 'host', targetLabel: `${node.displayName} · 主机终端` }
}

function openContainerTerminal(node: AgentNode, container: DockerContainer) {
  activeTerminal.value = { nodeId: node.nodeId, targetKind: 'container', containerId: container.id, targetLabel: `${node.displayName} · ${container.name || container.id.slice(0, 12)}` }
}

function isPendingRegistration(node: AgentNode): boolean {
  return node.status === 'pending' || !node.agentId
}

onMounted(() => {
  void loadDashboardSettings()
  void refreshNodes().catch((reason: unknown) => {
    busy.value = false
    error.value = reason instanceof Error ? reason.message : '无法读取节点列表。'
  })
  connectDashboard()
  refreshTimer = setInterval(() => {
    void refreshNodes().catch((reason: unknown) => {
      error.value = reason instanceof Error ? reason.message : '无法刷新节点列表。'
    })
  }, 10_000)
  tickTimer = setInterval(() => {
    elapsed.value = performance.now()
    wallClockNow.value = Date.now()
  }, 1000)
  taskRefreshTimer = setInterval(() => {
    const activeNodeIDs = new Set(Object.values(taskViews.value)
      .filter((task) => task.status === 'queued' || task.status === 'running')
      .map((task) => task.nodeId))
    for (const node of nodes.value) {
      if (activeNodeIDs.has(node.nodeId)) void loadTasks(node)
    }
  }, 1200)
})

onBeforeUnmount(() => {
  disposed = true
  if (reconnectTimer !== undefined) clearTimeout(reconnectTimer)
  if (refreshTimer !== undefined) clearInterval(refreshTimer)
  if (tickTimer !== undefined) clearInterval(tickTimer)
  if (taskRefreshTimer !== undefined) clearInterval(taskRefreshTimer)
  document.removeEventListener('pointermove', movePointerReorder)
  document.removeEventListener('pointerup', endPointerReorder)
  document.removeEventListener('pointercancel', endPointerReorder)
  pointerDrag = undefined
  socket?.close()
})
</script>

<template>
  <section class="nodes-dashboard" aria-labelledby="nodes-title">
    <header class="nodes-heading">
      <div>
        <span class="eyebrow">SERVER & CONTAINER CONTROL</span>
        <h1 id="nodes-title">NodeDance<span class="title-period">.</span></h1>
        <p>在一个首页查看服务器状态、容器和历史监控。</p>
      </div>
      <span class="stream-state" :data-state="socketState" role="status">
        <i aria-hidden="true"></i>{{ socketState === 'connected' ? '实时连接' : socketState === 'retrying' ? '正在重连' : '正在连接' }}
      </span>
    </header>

    <div v-if="error" class="nodes-error" role="alert">{{ error }}</div>
    <div class="nodes-layout">
      <aside class="node-list" aria-label="节点列表">
        <div class="node-list-heading"><strong>节点</strong><span>{{ nodes.length }}</span></div>
        <p v-if="busy && nodes.length === 0" class="node-empty">正在加载节点…</p>
        <p v-else-if="nodes.length === 0" class="node-empty">暂无已登记的节点。</p>
        <button v-for="node in nodes" :key="node.nodeId" class="node-option" :class="{ selected: node.nodeId === selectedNodeID }" type="button" @click="chooseNode(node)">
          <span class="node-option-dot" :data-status="nodeIsOnline(node) ? 'online' : 'offline'"></span>
          <span class="node-option-copy"><strong>{{ nodePreferences[node.nodeId]?.find((item) => item.targetKind === 'node')?.alias || node.displayName }}</strong><small>{{ nodeIsOnline(node) ? '在线' : isPendingRegistration(node) ? '等待 Agent 注册' : node.status === 'revoked' ? '已撤销' : '离线' }} · {{ node.agentVersion || 'Agent 未连接' }}</small></span>
          <span class="node-option-arrow" aria-hidden="true">›</span>
        </button>
      </aside>

      <main class="node-detail" aria-live="polite">
        <nav v-if="selectedNode" class="dashboard-tabs" aria-label="节点管理视图">
          <button v-for="tab in sectionTabs" :key="tab.id" type="button" :aria-current="activeSection === tab.id ? 'page' : undefined" @click="selectSection(tab.id)">{{ tab.label }}</button>
        </nav>

        <section class="vps-dashboard" aria-label="多 VPS 监控首页" data-testid="multi-vps-dashboard">
          <div class="server-view-controls" aria-label="服务器筛选和排序">
            <label>搜索服务器 <input v-model="serverSearch" type="search" placeholder="名称、主机名、分组或备注" aria-label="搜索服务器"></label>
            <label>服务器分组 <select v-model="dashboardSettings.nodeGroupBy"><option value="status">连接状态</option><option value="group">自定义分组</option><option value="none">不分组</option></select></label>
            <label>服务器排序 <select v-model="dashboardSettings.nodeSortBy"><option value="custom">自定义顺序</option><option value="name">显示名称</option><option value="status">连接状态</option></select></label>
            <button type="button" class="container-action" :disabled="dashboardSettingsSaving" @click="void saveDashboardSettings()">{{ dashboardSettingsSaving ? '保存中…' : '保存视图' }}</button>
          </div>
          <p v-if="nodes.length === 0" class="dashboard-empty">暂无已登记的 VPS。完成 Agent 注册后，节点卡片会显示真实主机指标与 Docker 容器。</p>
          <p v-else-if="serverCardGroups.every((group) => group.nodes.length === 0)" class="dashboard-empty">没有符合当前筛选条件的 VPS。</p>
          <section v-for="group in serverCardGroups" :key="group.key" class="vps-card-group" :data-group="group.key">
            <h2 v-if="group.label" class="vps-group-heading">{{ group.label }}</h2>
            <div class="vps-card-grid">
            <VpsCard
              v-for="node in group.nodes"
              :key="node.nodeId"
              :node="node"
              :metrics="views[node.nodeId]"
              :inventory="dockerViews[node.nodeId]"
              :node-preference="nodePreferences[node.nodeId]?.find((item) => item.targetKind === 'node')"
              :container-preferences="nodePreferences[node.nodeId] ?? []"
              :preference-identities="preferenceIdentities[node.nodeId] ?? {}"
              :online="nodeIsOnline(node)"
              :pending="isPendingRegistration(node)"
              :docker-stale="dockerViews[node.nodeId] ? dockerIsStale(dockerViews[node.nodeId]) : !nodeIsOnline(node)"
              :preview-limit="dashboardSettings.featuredLimit"
              :current-time="wallClockNow"
              :node-reason="nodeReasons[node.nodeId]"
              :tasks="tasksForNode(node.nodeId)"
              :task-submitting="taskSubmitting"
              :task-errors="taskErrors"
              @view="chooseNode"
              @container-action="submitCardContainerAction"
            />
            </div>
          </section>
        </section>

        <MetricsPanel v-if="selectedNode && activeSection === 'overview' && selectedView" :key="selectedView.nodeId" :view="selectedView" />
        <div v-else-if="selectedNode && activeSection === 'overview'" class="metrics-waiting" data-testid="metrics-waiting">
          <span class="eyebrow">{{ selectedNode.displayName }}</span>
          <h2>{{ isPendingRegistration(selectedNode) ? '等待 Agent 注册' : nodeIsOnline(selectedNode) ? '等待首份主机样本' : '节点当前离线' }}</h2>
          <p v-if="isPendingRegistration(selectedNode)">节点已创建，Agent 完成注册后才会开始采集主机数据。</p>
          <p v-else-if="nodeIsOnline(selectedNode)">CPU、内存、网络与磁盘数据会在 Agent 首次采样后显示。</p>
          <p v-else>保留的最近数据会在重新连接时显示，并明确标记为过期。{{ nodeReasons[selectedNode.nodeId] ? `状态原因：${nodeReasons[selectedNode.nodeId]}` : '' }}</p>
          <span class="node-detail-status" :data-online="nodeIsOnline(selectedNode)">{{ isPendingRegistration(selectedNode) ? '等待注册' : nodeIsOnline(selectedNode) ? '在线' : '离线' }}</span>
        </div>
        <div v-else-if="!selectedNode" class="metrics-waiting"><h2>选择一个节点</h2><p>节点的实时与最近指标会显示在这里。</p></div>

        <div v-if="selectedNode && activeSection === 'docker'" class="node-terminal-entry">
          <div><strong>主机终端</strong><small>通过已连接 Agent 打开目标节点的配置 shell。</small></div>
          <button type="button" :disabled="!nodeIsOnline(selectedNode)" @click="openHostTerminal(selectedNode)">打开终端</button>
        </div>

        <section v-if="selectedNode && activeSection === 'docker'" class="docker-panel" aria-labelledby="docker-title" data-testid="docker-inventory">
          <header class="docker-heading">
            <div><span class="eyebrow">CONTAINER INVENTORY</span><h2 id="docker-title">Docker 容器</h2></div>
            <span v-if="selectedDocker" class="docker-state" :data-available="selectedDocker.dockerAvailability" :data-stale="dockerIsStale(selectedDocker)">
              {{ dockerAvailabilityText(selectedDocker) }}
            </span>
          </header>
          <div class="docker-view-controls" aria-label="容器筛选和排序">
            <label>搜索 <input v-model="containerSearch" type="search" placeholder="名称、镜像、服务或备注" aria-label="搜索容器"></label>
            <label>分组 <select v-model="dashboardSettings.groupBy"><option value="node">节点</option><option value="compose">Compose 项目</option><option value="state">运行状态</option><option value="none">不分组</option></select></label>
            <label>排序 <select v-model="dashboardSettings.sortBy"><option value="custom">自定义</option><option value="name">显示名称</option><option value="state">运行状态</option></select></label>
            <button type="button" class="container-action" @click="void saveDashboardSettings()">保存视图</button>
          </div>
          <p v-if="orderError" class="container-task-error" role="alert">{{ orderError }}</p>
          <p v-if="!selectedDocker" class="docker-empty">正在读取此节点的 Docker 状态。</p>
          <p v-else-if="selectedDocker.dockerAvailability === 'unavailable' || selectedDocker.staleReason === 'docker_capability_unavailable'" class="docker-empty">
            Agent 仍可在线采集主机指标；Docker Engine 当前不可用。{{ dockerStatusReason(selectedDocker) ? `原因：${dockerStatusReason(selectedDocker)}` : '' }}
          </p>
          <p v-else-if="selectedDocker && dockerStatusReason(selectedDocker)" class="docker-empty" data-testid="docker-health-reason">
            Docker Engine 当前不能提供完整容器状态，下面保留最近已知容器并标记为过期。原因：{{ dockerStatusReason(selectedDocker) }}
          </p>
          <p v-if="selectedDocker && selectedDocker.containers.length === 0" class="docker-empty">
            {{ dockerIsStale(selectedDocker) ? '当前没有可确认的容器列表，等待完整扫描。' : '此节点没有容器。' }}
          </p>
          <p v-if="selectedDocker && selectedDocker.containers.length > 0 && sortedContainers.length === 0" class="docker-empty">没有符合搜索条件的容器。</p>
          <div v-if="selectedDocker && sortedContainers.length > 0" class="docker-list" :data-group-by="dashboardSettings.groupBy">
            <article v-for="record in sortedContainers" :key="record.container.id" class="docker-row" :class="{ 'is-drop-target': dropContainerID === record.container.id, 'is-being-dragged': draggedContainerID === record.container.id }" :data-container-id="record.container.id" :data-preference-identity="preferenceIdentities[selectedNodeID]?.[record.container.id]" :data-stale="dockerIsStale(selectedDocker) || record.container.stale" :data-group="containerGroupName(record)" draggable="true" @dragstart="beginNativeReorder(record, $event)" @dragover.prevent @drop="finishNativeReorder(record)" @dragend="draggedContainerID = ''">
              <div v-if="dashboardSettings.groupBy !== 'none'" class="container-group-label">{{ containerGroupName(record) }}</div>
              <div class="docker-row-title">
                <div class="docker-container-copy"><strong>{{ containerTitle(record) }}</strong><small><template v-if="dashboardSettings.customFields.includes('image')">{{ record.container.image || '未知镜像' }}</template><template v-if="record.container.compose"> · {{ record.container.compose.project }}/{{ record.container.compose.service }}</template><template v-if="preferenceForContainer(record)?.notes"> · {{ preferenceForContainer(record)?.notes }}</template></small></div>
                <div class="container-actions">
                  <span v-if="rollbackTaskForContainer(record.container.id)" class="container-state container-rollback-state" data-role="rollback">回滚副本</span>
                  <span v-else-if="dashboardSettings.customFields.includes('state')" class="container-state" :data-running="record.container.running">{{ record.container.state || '未知' }}</span>
                  <button type="button" :disabled="!record.container.running || record.container.stale || !dockerCanOperate(selectedDocker) || dockerIsStale(selectedDocker) || !nodeIsOnline(selectedNode) || Boolean(rollbackTaskForContainer(record.container.id))" @click="openContainerTerminal(selectedNode, record.container)">控制台</button>
                </div>
              </div>
              <div class="docker-row-meta">
                <span v-if="dashboardSettings.customFields.includes('health')" :data-health="record.container.health">健康：{{ containerHealthText(record.container) }}</span>
                <span v-if="dashboardSettings.customFields.includes('uptime')">运行时间：{{ containerUptime(record) }}</span>
                <span>网络：{{ containerNetworkText(record.container) }}</span>
                <span v-if="dockerIsStale(selectedDocker) || record.container.stale" class="container-stale">
                  过期数据<template v-if="record.container.unavailableReason">：{{ record.container.unavailableReason }}</template>
                </span>
                <span v-else>更新于 {{ new Date(record.receivedAt).toLocaleTimeString() }}</span>
              </div>
              <div v-if="dashboardSettings.customFields.includes('ports') && (record.container.ports ?? []).length" class="container-ports" aria-label="容器端口">
                <span v-for="(port, index) in (record.container.ports ?? [])" :key="`${port.containerPort}-${port.protocol}-${index}`">{{ portText(port) }}</span>
              </div>
              <p v-if="record.container.unavailableReason" class="container-reason">{{ record.container.unavailableReason }}</p>
              <div class="container-task-actions">
                <p v-if="rollbackTaskForContainer(record.container.id)" class="container-rollback-note" role="note">
                  此实例是已验证重建留下的停止回滚副本（任务 {{ rollbackTaskForContainer(record.container.id)?.taskId }}），不会作为主服务提供生命周期操作。
                </p>
                <template v-else>
                <button class="container-action drag-handle" type="button" aria-label="拖动调整容器顺序" :disabled="orderSaving || dashboardSettings.sortBy !== 'custom' || !preferenceIdentities[selectedNodeID]?.[record.container.id]" @pointerdown="beginPointerReorder($event, record)" title="按住并拖动可调整顺序">⠿</button>
                <button class="container-action" type="button" :disabled="!containerPreferenceForEditor(record)" :title="serviceLinkTitle(record)" @click="editingPreference = editingPreference === record.container.id ? '' : record.container.id">{{ editingPreference === record.container.id ? '关闭偏好' : '编辑偏好' }}</button>
                <a v-if="safeServiceURL(record)" class="container-action service-link" :href="safeServiceURL(record)" target="_blank" rel="noopener noreferrer">打开服务 ↗</a>
                <button v-if="record.container.paused" class="container-action" type="button" :disabled="!dockerCanOperate(selectedDocker) || dockerIsStale(selectedDocker) || record.container.stale || taskSubmitting[containerActionKey(selectedNodeID, record.container.id)]" @click="submitContainerAction(selectedNode, selectedDocker, record.container, 'resume')">恢复</button>
                <template v-else-if="record.container.running">
                  <button class="container-action" type="button" :disabled="!dockerCanOperate(selectedDocker) || dockerIsStale(selectedDocker) || record.container.stale || taskSubmitting[containerActionKey(selectedNodeID, record.container.id)]" @click="submitContainerAction(selectedNode, selectedDocker, record.container, 'stop')">停止</button>
                  <button class="container-action" type="button" :disabled="!dockerCanOperate(selectedDocker) || dockerIsStale(selectedDocker) || record.container.stale || taskSubmitting[containerActionKey(selectedNodeID, record.container.id)]" @click="submitContainerAction(selectedNode, selectedDocker, record.container, 'restart')">重启容器</button>
                  <button class="container-action" type="button" :disabled="!dockerCanOperate(selectedDocker) || dockerIsStale(selectedDocker) || record.container.stale || taskSubmitting[containerActionKey(selectedNodeID, record.container.id)]" @click="submitContainerAction(selectedNode, selectedDocker, record.container, 'pause')">暂停</button>
                </template>
                <button v-else class="container-action" type="button" :disabled="!dockerCanOperate(selectedDocker) || dockerIsStale(selectedDocker) || record.container.stale || taskSubmitting[containerActionKey(selectedNodeID, record.container.id)]" @click="submitContainerAction(selectedNode, selectedDocker, record.container, 'start')">启动</button>
                <button class="container-action" type="button" :disabled="!dockerCanOperate(selectedDocker) || dockerIsStale(selectedDocker) || record.container.stale || taskSubmitting[containerActionKey(selectedNodeID, record.container.id)] || Boolean(record.container.compose)" :title="record.container.compose ? 'Compose 项目容器不能通过实际重命名修改服务身份' : '重命名独立容器'" @click="submitContainerAction(selectedNode, selectedDocker, record.container, 'rename')">重命名</button>
                <button class="container-action container-action-danger" type="button" :disabled="!dockerCanOperate(selectedDocker) || dockerIsStale(selectedDocker) || record.container.stale || taskSubmitting[containerActionKey(selectedNodeID, record.container.id)] || record.container.running || record.container.paused || record.container.restarting" :title="record.container.running || record.container.paused || record.container.restarting ? '请先确认容器已停止' : '删除容器并保留数据卷'" @click="submitContainerAction(selectedNode, selectedDocker, record.container, 'delete')">删除</button>
                <ContainerRebuildWizard :node-id="selectedNode.nodeId" :container="record.container" :stale="!dockerCanOperate(selectedDocker) || dockerIsStale(selectedDocker)" :tasks="selectedNodeTasks" />
                <span v-if="taskSubmitting[containerActionKey(selectedNodeID, record.container.id)]" class="container-task-status" role="status">正在提交任务…</span>
                <span v-if="taskForContainer(record.container.id)" class="container-task-status" :data-status="taskForContainer(record.container.id)?.status" role="status">
                  {{ taskStatusLabel(taskForContainer(record.container.id)!) }}
                </span>
                <button v-if="taskForContainer(record.container.id)" class="container-action" type="button" @click="toggleTaskAudit(taskForContainer(record.container.id)!)">
                  {{ taskAuditVisible[taskForContainer(record.container.id)!.taskId] ? '隐藏审计' : '查看审计' }}
                </button>
                <span v-if="taskErrors[containerActionKey(selectedNodeID, record.container.id)]" class="container-task-error" role="alert">{{ taskErrors[containerActionKey(selectedNodeID, record.container.id)] }}</span>
                <span v-if="taskAuditErrors[taskForContainer(record.container.id)?.taskId || '']" class="container-task-error" role="alert">{{ taskAuditErrors[taskForContainer(record.container.id)?.taskId || ''] }}</span>
                <ol v-if="taskForContainer(record.container.id) && taskAuditVisible[taskForContainer(record.container.id)!.taskId]" class="container-task-audit" aria-label="容器任务审计">
                  <li v-if="taskAuditLoading[taskForContainer(record.container.id)!.taskId]">正在读取审计记录…</li>
                  <li v-for="event in (taskAuditViews[taskForContainer(record.container.id)!.taskId] ?? [])" :key="event.id">
                    <span>{{ event.event }}<template v-if="event.toStatus"> · {{ event.fromStatus || '开始' }} → {{ event.toStatus }}</template></span>
                    <time :datetime="event.occurredAt">{{ new Date(event.occurredAt).toLocaleString() }}</time>
                  </li>
                </ol>
                </template>
              </div>
              <PreferenceEditor v-if="editingPreference === record.container.id && containerPreferenceForEditor(record)" :preference="containerPreferenceForEditor(record)!" :title="record.container.compose ? `Compose 服务偏好：${record.container.compose.project}/${record.container.compose.service}` : `容器偏好：${record.container.name || record.container.id.slice(0, 12)}`" @saved="acceptSavedPreference" />
              <ContainerStreams
                v-if="selectedNode && selectedDocker && !rollbackTaskForContainer(record.container.id)"
                :node-id="selectedNode.nodeId"
                :container-id="record.container.id"
                :disabled="dockerIsStale(selectedDocker) || record.container.stale"
                :stats-available="record.container.running && !record.container.paused && !record.container.restarting"
              />
            </article>
          </div>
        </section>


        <ImagesPanel v-if="selectedNode && activeSection === 'images'" :key="selectedNode.nodeId" :node-id="selectedNode.nodeId" />
        <ComposeProjects v-if="selectedNode && activeSection === 'compose'" :key="selectedNode.nodeId" :node-id="selectedNode.nodeId" />
        <NodeFiles v-if="selectedNode && activeSection === 'files'" :key="selectedNode.nodeId" :node-id="selectedNode.nodeId" :node-name="selectedNode.displayName" />

        <section v-if="selectedNode && activeSection === 'history'" class="section-panel" aria-label="历史监控设置">
          <div class="section-toolbar"><div><span class="eyebrow">PERSISTED METRICS</span><h2>历史监控 · {{ selectedNodeTitle }}</h2></div>
            <div class="history-controls"><label>范围<select v-model="historyRange"><option value="6h">最近 6 小时</option><option value="24h">最近 24 小时</option><option value="7d">最近 7 天</option><option value="30d">最近 30 天</option><option value="1y">最近一年</option></select></label><button class="container-action" type="button" @click="void loadHistory()">查询</button></div>
          </div>
          <HistoricalMetrics :history="historyView" :loading="historyLoading" :error="historyError" :node-online="nodeIsOnline(selectedNode)" />
          <p class="retention-note">分钟聚合保留 30 天，小时聚合保留一年。Agent 失联期间没有有效样本的区间会显示为空白。</p>
        </section>

        <section v-if="selectedNode && activeSection === 'events'" class="section-panel" aria-label="节点操作事件历史" data-testid="node-event-history">
          <header class="section-toolbar"><div><span class="eyebrow">RECENT OPERATIONS</span><h2>操作事件历史 · {{ selectedNodeTitle }}</h2></div><button class="container-action" type="button" @click="void loadTasks(selectedNode)">刷新</button></header>
          <p class="event-source-note">这里展示 Core 已持久化的容器操作任务及其审计状态；Agent 失联时不推断操作成功。</p>
          <p v-if="selectedTasks.length === 0" class="docker-empty">暂无已记录的容器操作事件。</p>
          <article v-for="task in selectedTasks" :key="task.taskId" class="event-card" :data-status="task.status">
            <div><strong>{{ task.action }} · {{ task.targetId.slice(0, 12) }}</strong><span class="container-task-status" :data-status="task.status">{{ taskStatusLabel(task) }}</span><time :datetime="task.createdAt">{{ new Date(task.createdAt).toLocaleString() }}</time></div>
            <button class="container-action" type="button" @click="toggleTaskAudit(task)">{{ taskAuditVisible[task.taskId] ? '隐藏审计' : '查看审计' }}</button>
            <p v-if="taskAuditErrors[task.taskId]" class="container-task-error" role="alert">{{ taskAuditErrors[task.taskId] }}</p>
            <ol v-if="taskAuditVisible[task.taskId]" class="container-task-audit">
              <li v-if="taskAuditLoading[task.taskId]">正在读取审计记录…</li>
              <li v-for="event in (taskAuditViews[task.taskId] ?? [])" :key="event.id"><span>{{ event.event }} · {{ event.fromStatus || '开始' }} → {{ event.toStatus || '结果未知' }}</span><time :datetime="event.occurredAt">{{ new Date(event.occurredAt).toLocaleString() }}</time></li>
            </ol>
          </article>
        </section>

        <section v-if="selectedNode && activeSection === 'settings'" class="section-panel node-settings-panel" aria-label="节点设置" data-testid="node-settings">
          <header class="section-toolbar"><div><span class="eyebrow">DISPLAY & MONITORING</span><h2>节点设置 · {{ selectedNodeTitle }}</h2></div></header>
          <div class="node-settings-status"><div><span>Agent 连接</span><strong>{{ nodeIsOnline(selectedNode) ? '在线' : isPendingRegistration(selectedNode) ? '等待注册' : '离线' }}</strong></div><div><span>Docker Engine</span><strong>{{ selectedDocker ? dockerAvailabilityText(selectedDocker) : '未知' }}</strong></div><div><span>Agent 版本</span><strong>{{ selectedNode.agentVersion || '未知' }}</strong></div><div><span>节点身份</span><strong>{{ selectedNode.nodeId }}</strong></div></div>
          <PreferenceEditor v-if="nodePreferenceForEditor()" :preference="nodePreferenceForEditor()!" :title="`节点显示偏好：${selectedNode.displayName}`" @saved="acceptSavedPreference" />
          <div class="dashboard-settings-editor">
            <h3>首页和容器视图</h3>
            <div class="dashboard-settings-grid">
              <label>默认视图<select v-model="dashboardSettings.viewMode"><option value="monitor">监控</option><option value="manage">管理</option></select></label>
              <label>VPS 首页分组<select v-model="dashboardSettings.nodeGroupBy"><option value="status">连接状态</option><option value="group">自定义分组</option><option value="none">不分组</option></select></label>
              <label>VPS 首页排序<select v-model="dashboardSettings.nodeSortBy"><option value="custom">自定义顺序</option><option value="name">显示名称</option><option value="status">连接状态</option></select></label>
              <label>分组方式<select v-model="dashboardSettings.groupBy"><option value="node">节点</option><option value="compose">Compose 项目</option><option value="state">运行状态</option><option value="none">不分组</option></select></label>
              <label>排序方式<select v-model="dashboardSettings.sortBy"><option value="custom">自定义顺序</option><option value="name">显示名称</option><option value="state">运行状态</option></select></label>
              <label>首页容器上限<input v-model.number="dashboardSettings.featuredLimit" type="number" min="1" max="20"></label>
            </div>
            <fieldset class="custom-fields"><legend>Docker 详情字段</legend><label v-for="field in customFieldOptions" :key="field.id"><input v-model="dashboardSettings.customFields" type="checkbox" :value="field.id">{{ field.label }}</label></fieldset>
            <div class="settings-save"><button class="container-action" type="button" :disabled="dashboardSettingsSaving" @click="void saveDashboardSettings()">{{ dashboardSettingsSaving ? '保存中…' : '保存首页设置' }}</button><span v-if="dashboardSettingsMessage" role="status">{{ dashboardSettingsMessage }}</span><span v-if="dashboardSettingsError" role="alert">{{ dashboardSettingsError }}</span></div>
          </div>
          <p class="preference-separation-note">别名、图标、备注和顺序仅保存在 NodeDance SQLite 中。容器运行状态、健康状态和端口始终读取 Docker Engine。</p>
        </section>
        <NodeServiceProbes v-if="selectedNode" :key="selectedNode.nodeId" :node-id="selectedNode.nodeId"
          :node-name="selectedNode.displayName" :node-online="nodeIsOnline(selectedNode)" />
      </main>
    </div>
    <TerminalConsole v-if="activeTerminal" v-bind="activeTerminal" @close="activeTerminal = null" />
  </section>
</template>

<style scoped>
.nodes-dashboard { width: min(1480px, 100%); margin: 0 auto; padding: clamp(24px, 5vw, 52px); color: #eaf0fa; }
.nodes-heading { display: flex; align-items: center; justify-content: space-between; gap: 20px; margin-bottom: 28px; }
.nodes-heading h1 { margin: 5px 0 0; font-size: clamp(27px, 4vw, 38px); letter-spacing: -.05em; }
.nodes-heading p { margin: 8px 0 0; color: #98a8be; font-size: 13px; }
.stream-state { display: inline-flex; align-items: center; gap: 8px; border: 1px solid rgba(171, 196, 232, .13); border-radius: 999px; padding: 8px 12px; color: #aebbd0; font-size: 11px; white-space: nowrap; }
.stream-state i, .node-option-dot { width: 7px; height: 7px; border-radius: 50%; background: #9aa9bc; }
.stream-state[data-state='connected'] i, .node-option-dot[data-status='online'] { background: #79d69c; box-shadow: 0 0 0 3px rgba(80, 186, 119, .13); }
.nodes-error { margin-bottom: 16px; border: 1px solid rgba(255, 129, 116, .24); border-radius: 10px; padding: 12px 14px; color: #ffc1b8; background: rgba(184, 77, 72, .1); font-size: 12px; overflow-wrap: anywhere; }
.nodes-layout { display: grid; grid-template-columns: minmax(220px, 280px) minmax(0, 1fr); gap: 20px; align-items: start; }
.dashboard-tabs { display: flex; gap: 6px; overflow-x: auto; margin: 0 0 16px; border-bottom: 1px solid rgba(171,196,232,.12); padding-bottom: 8px; scrollbar-width: thin; }
.dashboard-tabs button { flex: 0 0 auto; min-height: 38px; border: 1px solid transparent; border-radius: 7px; padding: 7px 12px; color: #aebbd0; background: transparent; font: inherit; font-size: 10px; cursor: pointer; }
.dashboard-tabs button[aria-current='page'] { border-color: rgba(141,201,255,.22); color: #d6ebff; background: rgba(62,119,170,.18); }
.vps-dashboard { display: grid; grid-template-columns: minmax(0,1fr); align-items: start; gap: 12px; margin-bottom: 18px; }
.server-view-controls { display: flex; flex-wrap: wrap; align-items: end; gap: 8px; }
.server-view-controls label { display: grid; min-width: 140px; gap: 5px; color: #9aabc1; font-size: 9px; }
.server-view-controls input, .server-view-controls select { min-height: 36px; min-width: 0; border: 1px solid rgba(171,196,232,.16); border-radius: 6px; padding: 7px 9px; color: #eaf0fa; background: #111b29; font: inherit; font-size: 10px; }
.server-view-controls input { width: min(320px,58vw); }
.server-view-controls button { min-height: 36px; }
.vps-card-group { display: grid; gap: 9px; }
.vps-card-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 360px), 1fr)); align-items: start; gap: 12px; }
.vps-group-heading { margin: 0; color: #8bb4f4; font-size: 11px; font-weight: 600; }
.dashboard-empty { grid-column: 1 / -1; margin: 0; border: 1px dashed rgba(171,196,232,.18); border-radius: 10px; padding: 20px; color: #9aabc1; font-size: 11px; line-height: 1.6; }
.node-list { overflow: hidden; border: 1px solid rgba(171, 196, 232, .13); border-radius: 14px; background: rgba(15, 25, 40, .72); }
.node-list-heading { display: flex; align-items: center; justify-content: space-between; border-bottom: 1px solid rgba(171, 196, 232, .1); padding: 14px 16px; color: #dce6f5; font-size: 12px; }
.node-list-heading span { color: #8fa1b9; font-size: 10px; }
.node-option { display: flex; width: 100%; align-items: center; gap: 10px; border: 0; border-bottom: 1px solid rgba(171, 196, 232, .08); padding: 13px 14px; color: #e3ebf7; background: transparent; text-align: left; cursor: pointer; }
.node-option:last-child { border-bottom: 0; }
.node-option:hover, .node-option.selected { background: rgba(87, 133, 195, .13); }
.node-option-copy { display: grid; min-width: 0; flex: 1; gap: 4px; }
.node-option-copy strong { overflow: hidden; font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.node-option-copy small { color: #93a4bb; font-size: 9px; }
.node-option-arrow { color: #8294ac; font-size: 20px; }
.node-empty { padding: 14px; color: #94a5bc; font-size: 11px; }
.node-detail { min-width: 0; border: 1px solid rgba(171, 196, 232, .13); border-radius: 14px; padding: clamp(15px, 2.5vw, 24px); background: rgba(15, 25, 40, .62); }
.metrics-waiting { position: relative; min-height: 260px; display: flex; flex-direction: column; align-items: flex-start; justify-content: center; padding: clamp(20px, 5vw, 52px); }
.metrics-waiting h2 { margin: 10px 0 0; font-size: 21px; }
.metrics-waiting p { max-width: 440px; color: #9aabc1; font-size: 12px; line-height: 1.7; }
.node-detail-status { margin-top: 12px; border: 1px solid rgba(171, 196, 232, .15); border-radius: 999px; padding: 6px 11px; color: #ffb4aa; font-size: 10px; }
.node-detail-status[data-online='true'] { color: #9ce0b7; }
.node-terminal-entry { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-top: 16px; border: 1px solid rgba(121, 214, 156, .18); border-radius: 10px; padding: 12px 14px; background: rgba(31, 89, 62, .1); }
.node-terminal-entry > div { display: grid; gap: 4px; }
.node-terminal-entry strong { color: #cfe9d7; font-size: 11px; }
.node-terminal-entry small { color: #94aa9d; font-size: 10px; line-height: 1.5; }
.node-terminal-entry button, .container-actions button { min-height: 34px; border: 1px solid rgba(121, 214, 156, .28); border-radius: 7px; padding: 6px 10px; color: #a9e7bd; background: rgba(73, 152, 99, .12); font-size: 10px; white-space: nowrap; cursor: pointer; }
.node-terminal-entry button:disabled, .container-actions button:disabled { opacity: .45; cursor: not-allowed; }
.docker-panel { margin-top: 20px; border: 1px solid rgba(171, 196, 232, .13); border-radius: 12px; padding: clamp(14px, 2vw, 20px); background: rgba(9, 17, 29, .46); }
.docker-heading { display: flex; justify-content: space-between; align-items: center; gap: 12px; margin-bottom: 14px; }
.docker-heading h2 { margin: 4px 0 0; font-size: 17px; }
.docker-view-controls { display: flex; flex-wrap: wrap; align-items: end; gap: 8px; margin: 12px 0; }
.docker-view-controls label, .history-controls label { display: grid; min-width: 120px; gap: 5px; color: #9aabc1; font-size: 9px; }
.docker-view-controls input, .docker-view-controls select, .history-controls select, .dashboard-settings-grid select, .dashboard-settings-grid input { min-height: 36px; min-width: 0; border: 1px solid rgba(171,196,232,.16); border-radius: 6px; padding: 7px 9px; color: #eaf0fa; background: #111b29; font: inherit; font-size: 10px; }
.docker-view-controls input { width: min(320px,58vw); }
.container-group-label { margin: 0 0 8px; color: #8bb4f4; font-size: 9px; font-weight: 600; }
.docker-row.is-drop-target { outline: 2px solid rgba(105,184,255,.72); outline-offset: 2px; }
.docker-row.is-being-dragged { opacity: .55; }
.drag-handle { touch-action: none; user-select: none; cursor: grab; font-size: 15px; }
.drag-handle:active { cursor: grabbing; }
.docker-state, .container-state { border: 1px solid rgba(171, 196, 232, .16); border-radius: 999px; padding: 5px 9px; color: #aebbd0; font-size: 10px; white-space: nowrap; }
.docker-state[data-available='available'][data-stale='false'], .container-state[data-running='true'] { color: #9ce0b7; border-color: rgba(121, 214, 156, .24); }
.docker-state[data-available='unavailable'], .docker-state[data-stale='true'], .container-stale { color: #ffc1b8; border-color: rgba(255, 129, 116, .25); }
.docker-list { display: grid; gap: 9px; }
.docker-row { min-width: 0; border: 1px solid rgba(171, 196, 232, .1); border-radius: 9px; padding: 12px; background: rgba(18, 29, 45, .68); }
.docker-row[data-stale='true'] { border-color: rgba(255, 176, 129, .22); }
.container-rollback-state { color: #ffd092; border-color: rgba(255, 208, 146, .3); }
.container-rollback-note { flex: 1 0 100%; margin: 4px 0; color: #ffd092; font-size: 11px; line-height: 1.6; }
.docker-row-title { display: flex; justify-content: space-between; align-items: start; gap: 12px; }
.container-actions { display: flex; align-items: center; gap: 8px; }
.docker-container-copy { display: grid; min-width: 0; gap: 4px; }
.docker-container-copy strong { overflow-wrap: anywhere; font-size: 12px; }
.docker-container-copy small { overflow-wrap: anywhere; color: #93a4bb; font-size: 10px; }
.docker-row-meta { display: flex; flex-wrap: wrap; gap: 7px 14px; margin-top: 9px; color: #a5b3c8; font-size: 10px; }
.container-ports { display: grid; gap: 4px; margin-top: 8px; color: #8dc9ff; font-size: 10px; overflow-wrap: anywhere; }
.container-reason, .docker-empty { color: #9aabc1; font-size: 11px; line-height: 1.6; overflow-wrap: anywhere; }
.container-reason { margin: 8px 0 0; }
.container-task-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-top: 10px; }
.container-action { min-height: 34px; border: 1px solid rgba(141, 201, 255, .25); border-radius: 7px; padding: 6px 10px; color: #cce6ff; background: rgba(62, 119, 170, .16); font: inherit; font-size: 10px; cursor: pointer; }
.service-link { color: #cce6ff; text-decoration: none; }
.container-action:hover:not(:disabled) { background: rgba(62, 119, 170, .3); }
.container-action-danger { border-color: rgba(255, 129, 116, .3); color: #ffc1b8; background: rgba(184, 77, 72, .1); }
.container-action:disabled { opacity: .48; cursor: not-allowed; }
.container-task-status, .container-task-error { color: #a8bdd7; font-size: 10px; overflow-wrap: anywhere; }
.container-task-audit { flex-basis: 100%; margin: 4px 0 0; padding: 8px 8px 8px 26px; border: 1px solid rgba(171, 196, 232, .1); border-radius: 7px; color: #a8bdd7; font-size: 10px; }
.container-task-audit li { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 5px 12px; padding: 3px 0; }
.container-task-audit time { color: #8193aa; }
.container-task-status[data-status='succeeded'] { color: #9ce0b7; }
.container-task-status[data-status='failed'], .container-task-status[data-status='unknown'], .container-task-status[data-status='timed_out'], .container-task-error { color: #ffc1b8; }
.section-panel { min-width: 0; margin-top: 16px; border: 1px solid rgba(171,196,232,.13); border-radius: 12px; padding: clamp(14px,2vw,20px); background: rgba(9,17,29,.46); }
.section-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.history-controls { display: flex; align-items: end; gap: 8px; }
.retention-note, .event-source-note, .preference-separation-note { color: #91a2ba; font-size: 10px; line-height: 1.6; }
.event-card { display: grid; grid-template-columns: minmax(0,1fr) auto; gap: 8px; margin-top: 8px; border: 1px solid rgba(171,196,232,.1); border-radius: 8px; padding: 10px; background: rgba(18,29,45,.68); }
.event-card > div:first-child { display: grid; gap: 5px; }
.event-card strong { font-size: 11px; overflow-wrap: anywhere; }
.event-card time { color: #8193aa; font-size: 9px; }
.event-card .container-task-audit, .event-card > p { grid-column: 1 / -1; }
.node-settings-status { display: grid; grid-template-columns: repeat(4,minmax(0,1fr)); gap: 8px; margin: 12px 0; }
.node-settings-status > div { display: grid; min-width: 0; gap: 5px; border: 1px solid rgba(171,196,232,.1); border-radius: 7px; padding: 9px; }
.node-settings-status strong { color: #dce6f5; font-size: 10px; overflow-wrap: anywhere; }
.dashboard-settings-editor { margin-top: 16px; border-top: 1px solid rgba(171,196,232,.1); padding-top: 14px; }
.dashboard-settings-editor h3 { margin: 0 0 12px; font-size: 12px; }
.dashboard-settings-grid { display: grid; grid-template-columns: repeat(4,minmax(0,1fr)); gap: 9px; }
.dashboard-settings-grid label { display: grid; min-width: 0; gap: 5px; color: #9aabc1; font-size: 9px; }
.custom-fields { display: flex; flex-wrap: wrap; gap: 8px 14px; margin: 12px 0; border: 1px solid rgba(171,196,232,.1); border-radius: 7px; padding: 10px; }
.custom-fields legend { padding: 0 5px; color: #9aabc1; font-size: 9px; }
.custom-fields label { display: inline-flex; align-items: center; gap: 5px; color: #aebbd0; font-size: 9px; }
.settings-save { display: flex; flex-wrap: wrap; align-items: center; gap: 9px; color: #9ce0b7; font-size: 9px; }
.settings-save span[role='alert'] { color: #ffc1b8; }
.preference-separation-note { margin-top: 12px; }
@media (max-width: 760px) { .nodes-layout { grid-template-columns: minmax(0, 1fr); } .node-list { max-height: 270px; overflow: auto; } }
@media (max-width: 900px) { .node-settings-status { grid-template-columns: repeat(2,minmax(0,1fr)); } .dashboard-settings-grid { grid-template-columns: repeat(2,minmax(0,1fr)); } }
@media (max-width: 560px) { .nodes-dashboard { padding: 22px 14px; } .nodes-heading { flex-direction: column; align-items: flex-start; gap: 14px; } .nodes-heading p { max-width: 250px; } .stream-state { padding: 7px 9px; font-size: 9px; } .node-detail { padding: 12px; } .dashboard-tabs { margin-right: -12px; padding-right: 12px; } .section-toolbar { align-items: flex-start; flex-direction: column; } .history-controls { width: 100%; } .history-controls label { flex: 1; } .server-view-controls, .server-view-controls label, .server-view-controls input, .server-view-controls select, .server-view-controls button { width: 100%; } .docker-view-controls label, .docker-view-controls input, .docker-view-controls select { width: 100%; } .node-settings-status, .dashboard-settings-grid { grid-template-columns: minmax(0,1fr); } .docker-row-title { align-items: flex-start; flex-wrap: wrap; } .node-terminal-entry { align-items: flex-start; flex-direction: column; } .node-terminal-entry button { width: 100%; min-height: 42px; } .container-actions { width: 100%; justify-content: space-between; } .container-actions button { min-height: 42px; flex: 1; } }
</style>
