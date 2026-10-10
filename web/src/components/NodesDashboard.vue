<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api, type AgentNode, type AgentNodesResponse, type ContainerCreateSpec, type ContainerTask, type ContainerTaskAction, type CreateContainerTaskPayload, type DashboardPreference, type DashboardSettings, type DockerContainer, type DockerInventory, type DockerInventoryMessage, type NodeStatusResponse } from '../api'
import type { MetricsView } from '../metrics-contract'
import NodeDetail from './NodeDetail.vue'
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
const selectedNodeSection = ref<'overview' | 'docker' | 'files'>('overview')
const startSelectedNodeTerminal = ref(false)
const views = ref<Record<string, MetricsView>>({})
const dockerViews = ref<Record<string, DockerInventory>>({})
const clocks = ref<Record<string, NodeClock>>({})
const nodeReasons = ref<Record<string, string>>({})
const taskViews = ref<Record<string, ContainerTask>>({})
const taskSubmitting = ref<Record<string, boolean>>({})
const taskErrors = ref<Record<string, string>>({})
const preferenceIdentities = ref<Record<string, Record<string, string>>>({})
const nodePreferences = ref<Record<string, DashboardPreference[]>>({})
const dashboardSettings = ref<DashboardSettings>({ viewMode: 'monitor', groupBy: 'node', sortBy: 'custom', nodeGroupBy: 'status', nodeSortBy: 'custom', featuredLimit: 4, customFields: ['state', 'ports', 'health'] })
const serverSearch = ref('')
const statusFilter = ref<'all' | 'online' | 'pending' | 'offline' | 'revoked' | 'hidden'>('all')
const dashboardSettingsSaving = ref(false)
const dashboardSettingsMessage = ref('')
const dashboardSettingsError = ref('')
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

const selectedNode = computed(() => nodes.value.find((node) => node.nodeId === selectedNodeID.value) ?? null)
function remainingLease(clock: NodeClock): number {
  if (!clock.leaseValidUntil) return 0
  const from = Date.parse(clock.serverTime), until = Date.parse(clock.leaseValidUntil)
  return Number.isFinite(from) && Number.isFinite(until) ? Math.max(0, until - from) : 0
}
function nodeIsOnline(node: AgentNode): boolean {
  const clock = clocks.value[node.nodeId]
  if (!clock) return node.status === 'online'
  return clock.status === 'online' && elapsed.value - clock.startedAt < remainingLease(clock)
}
function isPendingRegistration(node: AgentNode): boolean { return node.status === 'pending' || !node.agentId }
function dockerIsStale(inventory: DockerInventory): boolean {
  if (inventory.dataStale || !inventory.agentOnline) return true
  const clock = clocks.value[inventory.nodeId], node = nodes.value.find((item) => item.nodeId === inventory.nodeId)
  return !clock || clock.status !== 'online' || clock.generation !== inventory.activeGeneration ||
    Boolean(node?.agentId && inventory.agentId !== node.agentId) || elapsed.value - clock.startedAt >= remainingLease(clock)
}
function setNodeReason(nodeID: string, reason?: string) {
  if (reason) { nodeReasons.value = { ...nodeReasons.value, [nodeID]: reason }; return }
  if (!(nodeID in nodeReasons.value)) return
  const next = { ...nodeReasons.value }; delete next[nodeID]; nodeReasons.value = next
}
function setClock(nodeID: string, status: string, serverTime: string, leaseValidUntil?: string, generation = 0) {
  const incomingTime = Date.parse(serverTime)
  if (!Number.isFinite(incomingTime)) return false
  const previous = clocks.value[nodeID]
  if (previous) {
    if (generation < previous.generation) return false
    const previousTime = Date.parse(previous.serverTime)
    if (generation === previous.generation && Number.isFinite(previousTime) && incomingTime <= previousTime) return false
  }
  clocks.value = { ...clocks.value, [nodeID]: { status, serverTime, leaseValidUntil, generation, startedAt: performance.now() } }
  return true
}
function mergeNodeList(payload: AgentNodesResponse): AgentNode[] | undefined {
  const listServerTime = Date.parse(payload.serverTime)
  if (!Number.isFinite(listServerTime) || listServerTime < latestNodeListServerTime) return undefined
  latestNodeListServerTime = listServerTime
  const previousNodes = new Map(nodes.value.map((node) => [node.nodeId, node]))
  const merged = payload.nodes.map((node) => {
    const clock = clocks.value[node.nodeId], clockTime = clock ? Date.parse(clock.serverTime) : Number.NEGATIVE_INFINITY
    const stale = Boolean(clock && (node.generation < clock.generation || node.generation === clock.generation && Number.isFinite(clockTime) && listServerTime <= clockTime))
    if (!stale) { setClock(node.nodeId, node.status, payload.serverTime, node.leaseValidUntil, node.generation); return node }
    return { ...node, status: clock!.status, generation: clock!.generation, leaseValidUntil: clock!.leaseValidUntil ?? node.leaseValidUntil, lastSeen: previousNodes.get(node.nodeId)?.lastSeen ?? node.lastSeen }
  })
  const included = new Set(merged.map((node) => node.nodeId))
  for (const node of nodes.value) if (!included.has(node.nodeId) && clocks.value[node.nodeId] && Date.parse(clocks.value[node.nodeId].serverTime) > listServerTime) merged.push(node)
  return merged
}
async function loadMetrics(node: AgentNode) {
  try { acceptMetricsResponse(await api.nodeMetrics(node.nodeId)) }
  catch (reason) { error.value = reason instanceof Error ? reason.message : '无法加载主机指标。' }
}
async function loadContainers(node: AgentNode) {
  try { acceptDockerResponse(await api.nodeContainers(node.nodeId)) }
  catch (reason) { error.value = reason instanceof Error ? reason.message : '无法加载 Docker 容器。' }
}
async function loadTasks(node: AgentNode) {
  try {
    const result = await api.nodeTasks(node.nodeId), next = { ...taskViews.value }
    for (const [id, task] of Object.entries(next)) if (task.nodeId === node.nodeId) delete next[id]
    for (const task of result.tasks) next[task.taskId] = task
    taskViews.value = next
  } catch (reason) { error.value = reason instanceof Error ? reason.message : '无法读取容器操作进度。' }
}
async function loadPreferences(node: AgentNode) {
  try { nodePreferences.value = { ...nodePreferences.value, [node.nodeId]: (await api.nodePreferences(node.nodeId)).preferences } }
  catch (reason) { error.value = reason instanceof Error ? reason.message : '无法读取展示偏好。' }
}
async function loadDashboardSettings() {
  try { dashboardSettings.value = await api.dashboardSettings() }
  catch (reason) { dashboardSettingsError.value = reason instanceof Error ? reason.message : '无法读取首页设置。' }
}
async function saveDashboardSettings() {
  dashboardSettingsSaving.value = true; dashboardSettingsMessage.value = ''; dashboardSettingsError.value = ''
  try { await api.saveDashboardSettings(dashboardSettings.value); dashboardSettingsMessage.value = '首页设置已保存。' }
  catch (reason) { dashboardSettingsError.value = reason instanceof Error ? reason.message : '首页设置保存失败。' }
  finally { dashboardSettingsSaving.value = false }
}
async function refreshNodes() {
  const merged = mergeNodeList(await api.nodes())
  if (!merged) { busy.value = false; return }
  nodes.value = merged
  if (selectedNodeID.value && !merged.some((node) => node.nodeId === selectedNodeID.value)) selectedNodeID.value = ''
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
function nodePreference(node: AgentNode): DashboardPreference | undefined {
  return nodePreferences.value[node.nodeId]?.find((item) => item.targetKind === 'node' && item.identity === `node:${node.nodeId}`)
}
function nodeTitle(node: AgentNode): string { return nodePreference(node)?.alias || node.displayName }
function nodeStatusGroup(node: AgentNode): string {
  if (nodeIsOnline(node)) return '在线'
  if (isPendingRegistration(node)) return '等待注册'
  return node.status === 'revoked' ? '已撤销' : '离线'
}
const serverCardGroups = computed(() => {
  const query = serverSearch.value.trim().toLocaleLowerCase()
  const matching = nodes.value.filter((node) => {
    const preference = nodePreference(node), hostname = views.value[node.nodeId]?.metrics.system.hostname.value, status = nodeStatusGroup(node)
    const hidden = preference?.visible === false
    if (hidden !== (statusFilter.value === 'hidden')) return false
    if (statusFilter.value === 'hidden') return !query || [node.displayName, nodeTitle(node), node.nodeId, preference?.group, preference?.notes, hostname, status]
      .some((value) => value?.toLocaleLowerCase().includes(query))
    if (statusFilter.value === 'online' && status !== '在线') return false
    if (statusFilter.value === 'pending' && status !== '等待注册') return false
    if (statusFilter.value === 'offline' && status !== '离线') return false
    if (statusFilter.value === 'revoked' && status !== '已撤销') return false
    return !query || [node.displayName, nodeTitle(node), node.nodeId, preference?.group, preference?.notes, hostname, status]
      .some((value) => value?.toLocaleLowerCase().includes(query))
  })
  const title = (node: AgentNode) => nodeTitle(node)
  const statusRank = (node: AgentNode) => nodeIsOnline(node) ? 0 : isPendingRegistration(node) ? 1 : node.status === 'revoked' ? 3 : 2
  matching.sort((left, right) => {
    if (dashboardSettings.value.nodeSortBy === 'name') return title(left).localeCompare(title(right), 'zh-CN')
    if (dashboardSettings.value.nodeSortBy === 'status') { const rank = statusRank(left) - statusRank(right); if (rank) return rank }
    else {
      const lp = nodePreference(left), rp = nodePreference(right)
      if (lp?.pinned !== rp?.pinned) return lp?.pinned ? -1 : 1
      if ((lp?.sortOrder ?? 0) !== (rp?.sortOrder ?? 0)) return (lp?.sortOrder ?? 0) - (rp?.sortOrder ?? 0)
    }
    return title(left).localeCompare(title(right), 'zh-CN')
  })
  if (dashboardSettings.value.nodeGroupBy === 'none') return [{ key: 'all', label: '', nodes: matching }]
  const groups = new Map<string, AgentNode[]>()
  for (const node of matching) {
    const label = dashboardSettings.value.nodeGroupBy === 'status' ? nodeStatusGroup(node) : nodePreference(node)?.group.trim() || '未分组'
    groups.set(label, [...(groups.get(label) ?? []), node])
  }
  const statusOrder = ['在线', '等待注册', '离线', '已撤销']
  return [...groups.keys()].sort((a, b) => dashboardSettings.value.nodeGroupBy === 'status' ? statusOrder.indexOf(a) - statusOrder.indexOf(b) : a.localeCompare(b, 'zh-CN'))
    .map((label) => ({ key: label, label, nodes: groups.get(label) ?? [] }))
})
function tasksForNode(nodeID: string): ContainerTask[] { return Object.values(taskViews.value).filter((task) => task.nodeId === nodeID) }
function chooseNode(node: AgentNode) {
  openNode(node, dashboardSettings.value.viewMode === 'manage' ? 'docker' : 'overview', false)
}
function openNode(node: AgentNode, section: 'overview' | 'docker' | 'files', startTerminal: boolean) {
  selectedNodeID.value = node.nodeId
  selectedNodeSection.value = section
  startSelectedNodeTerminal.value = startTerminal
  error.value = ''
  void loadPreferences(node)
  if (node.agentId) void Promise.all([loadMetrics(node), loadContainers(node), loadTasks(node)])
  else setNodeReason(node.nodeId, 'awaiting_agent_registration')
}
function openNodeShortcut(payload: { node: AgentNode; action: 'terminal' | 'files' | 'manage' }) {
  const node = nodes.value.find((item) => item.nodeId === payload.node.nodeId)
  if (!node) return
  const section = payload.action === 'files' ? 'files' : payload.action === 'manage' ? 'docker' : 'overview'
  openNode(node, section, payload.action === 'terminal')
}
function backToNodes() {
  selectedNodeID.value = ''
  selectedNodeSection.value = dashboardSettings.value.viewMode === 'manage' ? 'docker' : 'overview'
  startSelectedNodeTerminal.value = false
}
function acceptSavedPreference(preference: DashboardPreference) {
  const existing = nodePreferences.value[preference.nodeId] ?? []
  const without = existing.filter((item) => !(item.targetKind === preference.targetKind && item.identity === preference.identity))
  nodePreferences.value = { ...nodePreferences.value, [preference.nodeId]: [...without, preference] }
}
function acceptDockerResponse(result: DockerInventoryMessage) {
  const containers = Array.isArray(result.inventory?.containers) ? result.inventory.containers : []
  result = { ...result, inventory: { ...result.inventory, containers: containers.map((record) => ({ ...record, container: {
    ...record.container, networks: Array.isArray(record.container?.networks) ? record.container.networks : [], mounts: Array.isArray(record.container?.mounts) ? record.container.mounts : [],
    ports: Array.isArray(record.container?.ports) ? record.container.ports.map((port) => ({ ...port, configured: Array.isArray(port.configured) ? port.configured : [], published: Array.isArray(port.published) ? port.published : [] })) : [],
  } })) } }
  const previous = dockerViews.value[result.nodeId]
  if (previous) {
    if (result.inventory.activeGeneration < previous.activeGeneration) return
    if (result.inventory.activeGeneration === previous.activeGeneration) {
      const incomingTime = Date.parse(result.inventory.serverTime), previousTime = Date.parse(previous.serverTime)
      if (Number.isFinite(incomingTime) && Number.isFinite(previousTime) && incomingTime < previousTime) return
      if (incomingTime === previousTime && incomingTime > 0) {
        const incomingSequence = Math.max(0, ...result.inventory.containers.map((item) => item.sequence)), previousSequence = Math.max(0, ...previous.containers.map((item) => item.sequence))
        if (incomingSequence < previousSequence) return
      }
    }
  }
  dockerViews.value = { ...dockerViews.value, [result.nodeId]: result.inventory }
  preferenceIdentities.value = { ...preferenceIdentities.value, [result.nodeId]: result.preferenceIdentities ?? {} }
  if (setClock(result.nodeId, result.state.status, result.state.serverTime, result.state.leaseValidUntil, result.state.generation)) setNodeReason(result.nodeId, result.state.reason)
}
function acceptMetricsResponse(result: MetricsView | NodeStatusResponse) {
  if (isNodeStatusResponse(result)) {
    if (setClock(result.nodeId, result.state.status, result.state.serverTime, result.state.leaseValidUntil, result.state.generation)) setNodeReason(result.nodeId, result.state.reason)
    return
  }
  const previous = views.value[result.nodeId]
  if (previous) {
    if (result.activeGeneration < previous.activeGeneration) return
    if (result.activeGeneration === previous.activeGeneration) {
      if (result.generation < previous.generation || result.generation === previous.generation && result.sequence < previous.sequence) return
      const incomingTime = Date.parse(result.serverTime), previousTime = Date.parse(previous.serverTime)
      if (result.generation === previous.generation && result.sequence === previous.sequence && Number.isFinite(incomingTime) && Number.isFinite(previousTime) && incomingTime <= previousTime) return
    }
  }
  const previousClock = clocks.value[result.nodeId]
  if (previousClock && result.activeGeneration < previousClock.generation) return
  if (previousClock && result.activeGeneration === previousClock.generation && Date.parse(result.serverTime) < Date.parse(previousClock.serverTime)) return
  const normalized: MetricsView = { ...result, metrics: {
    ...result.metrics, network: { ...result.metrics.network, interfaces: Array.isArray(result.metrics.network?.interfaces) ? result.metrics.network.interfaces : [] },
    disk: { ...result.metrics.disk, mounts: Array.isArray(result.metrics.disk?.mounts) ? result.metrics.disk.mounts : [] },
  } }
  views.value = { ...views.value, [normalized.nodeId]: normalized }; setNodeReason(normalized.nodeId)
  setClock(normalized.nodeId, normalized.nodeStatus, normalized.serverTime, normalized.leaseValidUntil, normalized.activeGeneration)
}
function isNodeStatusResponse(result: MetricsView | NodeStatusResponse): result is NodeStatusResponse { return (result as NodeStatusResponse).type === 'node_status' }
function acceptSocketMessage(raw: string) {
  let event: unknown
  try { event = JSON.parse(raw) } catch { return }
  if (typeof event !== 'object' || event === null || !('type' in event) || !('nodeId' in event)) return
  const value = event as { type?: string; nodeId?: string; metrics?: MetricsView; state?: NodeStatusResponse['state'] }
  if (!value.nodeId) return
  if (value.type === 'node_metrics' && value.metrics) { acceptMetricsResponse(value.metrics); return }
  if (value.type === 'node_status' && value.state) { if (setClock(value.nodeId, value.state.status, value.state.serverTime, value.state.leaseValidUntil, value.state.generation)) setNodeReason(value.nodeId, value.state.reason); return }
  if (value.type === 'node_containers') acceptDockerResponse(event as DockerInventoryMessage)
}
function connectDashboard() {
  if (disposed) return
  socketState.value = socketState.value === 'connected' ? 'retrying' : 'connecting'
  const url = new URL('/ws/v1/dashboard', window.location.href); url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  const current = new WebSocket(url); socket = current
  current.onopen = () => { if (disposed || socket !== current) return; reconnectDelay = 1000; socketState.value = 'connected'; void refreshNodes().catch((reason: unknown) => { busy.value = false; error.value = reason instanceof Error ? reason.message : '无法读取节点列表。' }) }
  current.onmessage = (message) => { if (!disposed && socket === current) acceptSocketMessage(String(message.data)) }
  current.onclose = () => { if (socket === current) { socket = undefined; scheduleReconnect() } }
  current.onerror = () => { if (socket === current) current.close() }
}
function scheduleReconnect() {
  if (disposed || reconnectTimer !== undefined) return
  socketState.value = 'retrying'; reconnectTimer = setTimeout(() => { reconnectTimer = undefined; connectDashboard() }, reconnectDelay); reconnectDelay = Math.min(30_000, reconnectDelay * 2)
}
async function submitContainerAction(nodeID: string, containerID: string, action: ContainerTaskAction, options: Partial<CreateContainerTaskPayload> = {}) {
  const node = nodes.value.find((item) => item.nodeId === nodeID), inventory = dockerViews.value[nodeID]
  const container = inventory?.containers.find((item) => item.container.id === containerID)?.container
  const key = `${nodeID}:${containerID}`
  if (!node || !container || !inventory || inventory.dockerAvailability !== 'available' || !node.agentId || !nodeIsOnline(node) || dockerIsStale(inventory) || container.stale || taskSubmitting.value[key]) return
  if (action === 'delete' && (container.running || container.paused || container.restarting || options.deleteConfirmationId !== containerID || options.deleteConfirmed !== true)) return
  taskSubmitting.value = { ...taskSubmitting.value, [key]: true }
  const errors = { ...taskErrors.value }; delete errors[key]; taskErrors.value = errors
  try {
    const payload = { ...options, action }
    const accepted = await api.createContainerTask(nodeID, containerID, payload, crypto.randomUUID())
    const current = await api.nodeTask(nodeID, accepted.taskId)
    taskViews.value = { ...taskViews.value, [current.taskId]: current }
  } catch (reason) { taskErrors.value = { ...taskErrors.value, [key]: reason instanceof Error ? reason.message : '无法创建容器操作任务。' }
  } finally { taskSubmitting.value = { ...taskSubmitting.value, [key]: false } }
}
function submitCardContainerAction(payload: { node: AgentNode; record: DockerInventory['containers'][number]; action: ContainerTaskAction }) {
  void submitContainerAction(payload.node.nodeId, payload.record.container.id, payload.action)
}
function submitDetailContainerAction(payload: { nodeId: string; containerId: string; task: CreateContainerTaskPayload }) {
  void submitContainerAction(payload.nodeId, payload.containerId, payload.task.action, payload.task)
}
async function submitContainerCreateTask(payload: { nodeId: string; spec: ContainerCreateSpec }) {
  const { nodeId, spec } = payload
  const node = nodes.value.find((item) => item.nodeId === nodeId), inventory = dockerViews.value[nodeId]
  const key = `${nodeId}:container-create`
  if (!node || !inventory || inventory.dockerAvailability !== 'available' || !node.agentId || !nodeIsOnline(node) || dockerIsStale(inventory) || taskSubmitting.value[key]) return
  taskSubmitting.value = { ...taskSubmitting.value, [key]: true }
  const errors = { ...taskErrors.value }; delete errors[key]; taskErrors.value = errors
  try {
    const accepted = await api.createContainerCreateTask(nodeId, spec, crypto.randomUUID())
    const current = await api.nodeTask(nodeId, accepted.taskId)
    taskViews.value = { ...taskViews.value, [current.taskId]: current }
  } catch (reason) {
    taskErrors.value = { ...taskErrors.value, [key]: reason instanceof Error ? reason.message : '无法创建容器创建任务。' }
  } finally { taskSubmitting.value = { ...taskSubmitting.value, [key]: false } }
}
onMounted(() => {
  void loadDashboardSettings(); void refreshNodes().catch((reason: unknown) => { busy.value = false; error.value = reason instanceof Error ? reason.message : '无法读取节点列表。' }); connectDashboard()
  refreshTimer = setInterval(() => { void refreshNodes().catch((reason: unknown) => { error.value = reason instanceof Error ? reason.message : '无法刷新节点列表。' }) }, 10_000)
  tickTimer = setInterval(() => { elapsed.value = performance.now(); wallClockNow.value = Date.now() }, 1000)
  taskRefreshTimer = setInterval(() => {
    const active = new Set(Object.values(taskViews.value).filter((task) => task.status === 'queued' || task.status === 'running').map((task) => task.nodeId))
    for (const node of nodes.value) if (active.has(node.nodeId)) void loadTasks(node)
  }, 1200)
})
onBeforeUnmount(() => {
  disposed = true
  if (reconnectTimer !== undefined) clearTimeout(reconnectTimer)
  if (refreshTimer !== undefined) clearInterval(refreshTimer)
  if (tickTimer !== undefined) clearInterval(tickTimer)
  if (taskRefreshTimer !== undefined) clearInterval(taskRefreshTimer)
  socket?.close()
})
</script>

<template>
  <section v-if="!selectedNode" class="nodes-dashboard" aria-labelledby="nodes-title">
    <header class="nodes-heading"><div><span class="eyebrow">SERVER & CONTAINER CONTROL</span><h1 id="nodes-title">NodeDance<span class="title-period">.</span></h1><p>在一个首页查看服务器状态、容器和历史监控。</p></div><span class="stream-state" :data-state="socketState" role="status"><i aria-hidden="true"></i>{{ socketState === 'connected' ? '实时连接' : socketState === 'retrying' ? '正在重连' : '正在连接' }}</span></header>
    <div v-if="error" class="nodes-error" role="alert">{{ error }}</div>
    <section class="vps-dashboard" aria-label="多 VPS 监控首页" data-testid="multi-vps-dashboard">
      <div class="server-view-controls" aria-label="服务器搜索、筛选、分组和排序"><label>搜索服务器 <input v-model="serverSearch" type="search" placeholder="名称、主机名、分组或备注" aria-label="搜索服务器"></label><label>状态筛选 <select v-model="statusFilter"><option value="all">所有状态</option><option value="online">在线</option><option value="pending">等待注册</option><option value="offline">离线</option><option value="revoked">已撤销</option><option value="hidden">隐藏 VPS</option></select></label><label>服务器分组 <select v-model="dashboardSettings.nodeGroupBy"><option value="status">连接状态</option><option value="group">自定义分组</option><option value="none">不分组</option></select></label><label>服务器排序 <select v-model="dashboardSettings.nodeSortBy"><option value="custom">自定义顺序</option><option value="name">显示名称</option><option value="status">连接状态</option></select></label><label>首页容器上限 <input v-model.number="dashboardSettings.featuredLimit" type="number" min="1" max="20"></label><button type="button" class="container-action" :disabled="dashboardSettingsSaving" @click="void saveDashboardSettings()">{{ dashboardSettingsSaving ? '保存中…' : '保存视图' }}</button><span v-if="dashboardSettingsMessage" role="status">{{ dashboardSettingsMessage }}</span><span v-if="dashboardSettingsError" role="alert">{{ dashboardSettingsError }}</span></div>
      <p v-if="busy && nodes.length === 0" class="dashboard-empty">正在加载 VPS…</p><p v-else-if="nodes.length === 0" class="dashboard-empty">暂无已登记的 VPS。完成 Agent 注册后，节点卡片会显示真实主机指标与 Docker 容器。</p><p v-else-if="serverCardGroups.every((group) => group.nodes.length === 0)" class="dashboard-empty">没有符合当前筛选条件的 VPS。</p>
      <section v-for="group in serverCardGroups" :key="group.key" class="vps-card-group" :data-group="group.key"><h2 v-if="group.label" class="vps-group-heading">{{ group.label }}</h2><div class="vps-card-grid"><VpsCard v-for="node in group.nodes" :key="node.nodeId" :node="node" :metrics="views[node.nodeId]" :inventory="dockerViews[node.nodeId]" :node-preference="nodePreference(node)" :container-preferences="nodePreferences[node.nodeId] ?? []" :preference-identities="preferenceIdentities[node.nodeId] ?? {}" :online="nodeIsOnline(node)" :pending="isPendingRegistration(node)" :docker-stale="dockerViews[node.nodeId] ? dockerIsStale(dockerViews[node.nodeId]) : !nodeIsOnline(node)" :preview-limit="dashboardSettings.featuredLimit" :current-time="wallClockNow" :node-reason="nodeReasons[node.nodeId]" :tasks="tasksForNode(node.nodeId)" :task-submitting="taskSubmitting" :task-errors="taskErrors" @view="chooseNode" @quick-action="openNodeShortcut" @container-action="submitCardContainerAction"/></div></section>
    </section>
  </section>
  <NodeDetail v-else :key="selectedNode.nodeId" :node="selectedNode" :metrics="views[selectedNode.nodeId]" :inventory="dockerViews[selectedNode.nodeId]" :preferences="nodePreferences[selectedNode.nodeId] ?? []" :preference-identities="preferenceIdentities[selectedNode.nodeId] ?? {}" :online="nodeIsOnline(selectedNode)" :pending="isPendingRegistration(selectedNode)" :docker-stale="dockerViews[selectedNode.nodeId] ? dockerIsStale(dockerViews[selectedNode.nodeId]) : !nodeIsOnline(selectedNode)" :node-reason="nodeReasons[selectedNode.nodeId]" :tasks="tasksForNode(selectedNode.nodeId)" :task-submitting="taskSubmitting" :task-errors="taskErrors" :dashboard-settings="dashboardSettings" :dashboard-settings-saving="dashboardSettingsSaving" :dashboard-settings-message="dashboardSettingsMessage" :dashboard-settings-error="dashboardSettingsError" :current-time="wallClockNow" :initial-section="selectedNodeSection" :start-host-terminal="startSelectedNodeTerminal" @back="backToNodes" @refresh-tasks="(id) => { const node = nodes.find((item) => item.nodeId === id); if (node) void loadTasks(node) }" @preference-saved="acceptSavedPreference" @container-action="submitDetailContainerAction" @container-create="submitContainerCreateTask" @settings-change="dashboardSettings = $event" @save-settings="void saveDashboardSettings()"/>
</template>

<style scoped>
.nodes-dashboard { width:min(1480px,100%); margin:0 auto; padding:clamp(24px,5vw,52px); color:#eaf0fa; }
.nodes-heading { display:flex; align-items:center; justify-content:space-between; gap:20px; margin-bottom:28px; }.nodes-heading h1 { margin:5px 0 0; font-size:clamp(27px,4vw,38px); letter-spacing:-.05em; }.nodes-heading p { margin:8px 0 0; color:#98a8be; font-size:13px; }
.stream-state { display:inline-flex; align-items:center; gap:8px; border:1px solid rgba(171,196,232,.13); border-radius:999px; padding:8px 12px; color:#aebbd0; font-size:11px; white-space:nowrap; }.stream-state i { width:7px; height:7px; border-radius:50%; background:#9aa9bc; }.stream-state[data-state=connected] i { background:#79d69c; box-shadow:0 0 0 3px rgba(80,186,119,.13); }
.nodes-error { margin-bottom:16px; border:1px solid rgba(255,129,116,.24); border-radius:10px; padding:12px 14px; color:#ffc1b8; background:rgba(184,77,72,.1); font-size:12px; overflow-wrap:anywhere; }.vps-dashboard { display:grid; grid-template-columns:minmax(0,1fr); align-items:start; gap:12px; }
.server-view-controls { display:flex; flex-wrap:wrap; align-items:end; gap:8px; }.server-view-controls label { display:grid; min-width:140px; gap:5px; color:#9aabc1; font-size:9px; }.server-view-controls input,.server-view-controls select { min-height:36px; min-width:0; border:1px solid rgba(171,196,232,.16); border-radius:6px; padding:7px 9px; color:#eaf0fa; background:#111b29; font:inherit; font-size:10px; }.server-view-controls input { width:min(280px,58vw); }.server-view-controls button { min-height:36px; }.vps-card-group { display:grid; gap:9px; }.vps-card-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(min(100%,360px),1fr)); align-items:start; gap:12px; }.vps-group-heading { margin:0; color:#8bb4f4; font-size:11px; font-weight:600; }.dashboard-empty { grid-column:1/-1; margin:0; border:1px dashed rgba(171,196,232,.18); border-radius:10px; padding:20px; color:#9aabc1; font-size:11px; line-height:1.6; }
@media(max-width:760px) { .vps-card-grid { grid-template-columns:repeat(auto-fit,minmax(min(100%,320px),1fr)); } }
@media(max-width:560px) { .nodes-dashboard { padding:22px 14px; }.nodes-heading { flex-direction:column; align-items:flex-start; gap:14px; }.stream-state { padding:7px 9px; font-size:9px; }.server-view-controls,.server-view-controls label,.server-view-controls input,.server-view-controls select,.server-view-controls button { width:100%; } }
</style>
