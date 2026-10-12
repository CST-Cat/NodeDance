<script setup lang="ts">
import { operationURL } from '../operations'
import { t, display, locale } from "../i18n"
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api, type AgentNode, type AgentNodesResponse, type ContainerCreateSpec, type ContainerTask, type ContainerTaskAction, type CreateContainerTaskPayload, type DashboardPreference, type DashboardSettings, type DockerContainer, type DockerInventory, type DockerInventoryMessage, type NodeStatusResponse } from '../api'
import type { MetricsView } from '../metrics-contract'
import NodeDetail from './NodeDetail.vue'
import VpsCard from './VpsCard.vue'
import UiIcon from './UiIcon.vue'
import { useViewNavigation } from '../navigation'

const TerminalConsole = defineAsyncComponent(() => import('./TerminalConsole.vue'))
const terminalNode = ref<AgentNode | null>(null)

const emit = defineEmits<{ enroll: [] }>()
const preferencesOpen = ref(false)
const nodeSummary = computed(() => [
  { label: 'dashboard.total', value: nodes.value.length, kind: 'total' },
  { label: 'dashboard.online', value: nodes.value.filter(nodeIsOnline).length, kind: 'online' },
  { label: 'dashboard.offline', value: nodes.value.filter(node => !nodeIsOnline(node) && !isPendingRegistration(node) && node.status !== 'revoked').length, kind: 'offline' },
  { label: 'dashboard.pending', value: nodes.value.filter(isPendingRegistration).length, kind: 'pending' },
])

interface NodeClock {
  status: string
  generation: number
  serverTime: string
  leaseValidUntil?: string
  startedAt: number
}
const nodes = ref<AgentNode[]>([])
const viewNavigation = useViewNavigation()
const selectedNodeID = computed(() => viewNavigation.route.value.nodeId ?? '')
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
  catch (reason) { error.value = reason instanceof Error ? reason.message : t('m.433d33e2e1b8') }
}
async function loadContainers(node: AgentNode) {
  try { acceptDockerResponse(await api.nodeContainers(node.nodeId)) }
  catch (reason) { error.value = reason instanceof Error ? reason.message : t('m.f7f31f8592ee') }
}
async function loadTasks(node: AgentNode) {
  try {
    const result = await api.nodeTasks(node.nodeId), next = { ...taskViews.value }
    for (const [id, task] of Object.entries(next)) if (task.nodeId === node.nodeId) delete next[id]
    for (const task of result.tasks) next[task.taskId] = task
    taskViews.value = next
  } catch (reason) { error.value = reason instanceof Error ? reason.message : t('m.f63256fb13c3') }
}
async function loadPreferences(node: AgentNode) {
  try { nodePreferences.value = { ...nodePreferences.value, [node.nodeId]: (await api.nodePreferences(node.nodeId)).preferences } }
  catch (reason) { error.value = reason instanceof Error ? reason.message : t('m.1e9242469345') }
}
async function loadDashboardSettings() {
  try { dashboardSettings.value = await api.dashboardSettings() }
  catch (reason) { dashboardSettingsError.value = reason instanceof Error ? reason.message : t('m.22d4c52f5770') }
}
async function saveDashboardSettings() {
  dashboardSettingsSaving.value = true; dashboardSettingsMessage.value = ''; dashboardSettingsError.value = ''
  try { await api.saveDashboardSettings(dashboardSettings.value); dashboardSettingsMessage.value = t('m.5729552b2210') }
  catch (reason) { dashboardSettingsError.value = reason instanceof Error ? reason.message : t('m.567fd1d27a30') }
  finally { dashboardSettingsSaving.value = false }
}
async function refreshNodes() {
  if (disposed) return
  const payload = await api.nodes()
  if (disposed) return
  const merged = mergeNodeList(payload)
  if (!merged) { busy.value = false; return }
  nodes.value = merged
  if (selectedNodeID.value && !merged.some((node) => node.nodeId === selectedNodeID.value)) viewNavigation.go({ screen: 'nodes' }, true)
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
  if (nodeIsOnline(node)) return t('m.b9086662b1df')
  if (isPendingRegistration(node)) return t('m.a890b3be09aa')
  return node.status === 'revoked' ? t('m.fef96e34f77b') : t('m.be1b4f3c6c1c')
}
const serverCardGroups = computed(() => {
  const query = serverSearch.value.trim().toLocaleLowerCase()
  const matching = nodes.value.filter((node) => {
    const preference = nodePreference(node), hostname = views.value[node.nodeId]?.metrics.system.hostname.value, status = nodeStatusGroup(node)
    const hidden = preference?.visible === false
    if (hidden !== (statusFilter.value === 'hidden')) return false
    if (statusFilter.value === 'hidden') return !query || [node.displayName, nodeTitle(node), node.nodeId, preference?.group, preference?.notes, hostname, status]
      .some((value) => value?.toLocaleLowerCase().includes(query))
    if (statusFilter.value === 'online' && status !== t('m.b9086662b1df')) return false
    if (statusFilter.value === 'pending' && status !== t('m.a890b3be09aa')) return false
    if (statusFilter.value === 'offline' && status !== t('m.be1b4f3c6c1c')) return false
    if (statusFilter.value === 'revoked' && status !== t('m.fef96e34f77b')) return false
    return !query || [node.displayName, nodeTitle(node), node.nodeId, preference?.group, preference?.notes, hostname, status]
      .some((value) => value?.toLocaleLowerCase().includes(query))
  })
  const title = (node: AgentNode) => nodeTitle(node)
  const statusRank = (node: AgentNode) => nodeIsOnline(node) ? 0 : isPendingRegistration(node) ? 1 : node.status === 'revoked' ? 3 : 2
  matching.sort((left, right) => {
    if (dashboardSettings.value.nodeSortBy === 'name') return title(left).localeCompare(title(right), locale.value)
    if (dashboardSettings.value.nodeSortBy === 'status') { const rank = statusRank(left) - statusRank(right); if (rank) return rank }
    else {
      const lp = nodePreference(left), rp = nodePreference(right)
      if (lp?.pinned !== rp?.pinned) return lp?.pinned ? -1 : 1
      if ((lp?.sortOrder ?? 0) !== (rp?.sortOrder ?? 0)) return (lp?.sortOrder ?? 0) - (rp?.sortOrder ?? 0)
    }
    return title(left).localeCompare(title(right), locale.value)
  })
  if (dashboardSettings.value.nodeGroupBy === 'none') return [{ key: 'all', label: '', nodes: matching }]
  const groups = new Map<string, AgentNode[]>()
  for (const node of matching) {
    const label = dashboardSettings.value.nodeGroupBy === 'status' ? nodeStatusGroup(node) : nodePreference(node)?.group.trim() || t('m.e86ea3f280d9')
    groups.set(label, [...(groups.get(label) ?? []), node])
  }
  const statusOrder = [t('m.b9086662b1df'), t('m.a890b3be09aa'), t('m.be1b4f3c6c1c'), t('m.fef96e34f77b')]
  return [...groups.keys()].sort((a, b) => dashboardSettings.value.nodeGroupBy === 'status' ? statusOrder.indexOf(a) - statusOrder.indexOf(b) : a.localeCompare(b, locale.value))
    .map((label) => ({ key: label, label, nodes: groups.get(label) ?? [] }))
})
function tasksForNode(nodeID: string): ContainerTask[] { return Object.values(taskViews.value).filter((task) => task.nodeId === nodeID) }
function chooseNode(node: AgentNode) {
  openNode(node, dashboardSettings.value.viewMode === 'manage' ? 'docker' : 'overview')
}
function openNode(node: AgentNode, section: 'overview' | 'docker' | 'files') {
  viewNavigation.go({ screen: 'nodes', nodeId: node.nodeId, section })
}
function openNodeShortcut(payload: { node: AgentNode; action: 'terminal' | 'files' | 'manage' }) {
  const node = nodes.value.find((item) => item.nodeId === payload.node.nodeId)
  if (!node) return
  if (payload.action === 'terminal') { terminalNode.value = node; return }
  const section = payload.action === 'files' ? 'files' : payload.action === 'manage' ? 'docker' : 'overview'
  openNode(node, section)
}
function backToNodes() { viewNavigation.go({ screen: 'nodes' }) }
watch(() => viewNavigation.route.value, route => {
  terminalNode.value = null
  const node = nodes.value.find(item => item.nodeId === route.nodeId)
  if (!node) return
  error.value = ''
  void loadPreferences(node)
  if (node.agentId) void Promise.all([loadMetrics(node), loadContainers(node), loadTasks(node)])
  else setNodeReason(node.nodeId, 'awaiting_agent_registration')
})

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
async function connectDashboard() {
  if (disposed) return
  socketState.value = socketState.value === 'connected' ? 'retrying' : 'connecting'
  let url: URL
  try { url = await operationURL('dashboard.watch') } catch { scheduleReconnect(); return }
  if (disposed) return
  const current = new WebSocket(url); socket = current
  current.onopen = () => { if (disposed || socket !== current) return; reconnectDelay = 1000; socketState.value = 'connected'; void refreshNodes().catch((reason: unknown) => { busy.value = false; error.value = reason instanceof Error ? reason.message : t('m.8fae2c449c13') }) }
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
  } catch (reason) { taskErrors.value = { ...taskErrors.value, [key]: reason instanceof Error ? reason.message : t('m.e9b2af7934ab') }
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
    taskErrors.value = { ...taskErrors.value, [key]: reason instanceof Error ? reason.message : t('m.61aabbe69bf5') }
  } finally { taskSubmitting.value = { ...taskSubmitting.value, [key]: false } }
}
onMounted(() => {
  void loadDashboardSettings(); void refreshNodes().catch((reason: unknown) => { busy.value = false; error.value = reason instanceof Error ? reason.message : t('m.8fae2c449c13') }); connectDashboard()
  refreshTimer = setInterval(() => { void refreshNodes().catch((reason: unknown) => { error.value = reason instanceof Error ? reason.message : t('m.4de430beaf29') }) }, 10_000)
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
    <header class="nodes-heading"><div><span class="eyebrow">{{ t('label.2e13dc41737f') }}</span><h1 id="nodes-title">{{ t('dashboard.title') }}</h1><p>{{ t('m.d11b82b4e5e1') }}</p></div><span class="stream-state" :data-state="socketState" role="status"><i aria-hidden="true"></i>{{ socketState === 'connected' ? t('m.a1b56411cc63') : socketState === 'retrying' ? t('m.7a58d3d9d3e3') : t('m.694186911ae3') }}</span></header>
    <div class="node-summary" :aria-label="t('dashboard.total')">
      <article v-for="item in nodeSummary" :key="item.kind" :data-kind="item.kind"><span>{{ t(item.label) }}</span><strong>{{ busy && !nodes.length ? '—' : item.value }}</strong><i aria-hidden="true"></i></article>
    </div>
    <div v-if="error" class="nodes-error" role="alert">{{ display(error) }}</div>
    <section class="vps-dashboard" :aria-label="t('m.8a3dfec291d0')" data-testid="multi-vps-dashboard">
      <div class="dashboard-toolbar"><label class="node-search"><UiIcon name="nodes" /><input v-model="serverSearch" type="search" :placeholder="t('m.800588838c17')" :aria-label="t('m.0191c754eec0')" /></label><div class="dashboard-toolbar-actions"><button type="button" class="secondary-button view-preferences-button" :aria-expanded="preferencesOpen" aria-controls="node-view-preferences" @click="preferencesOpen = !preferencesOpen"><UiIcon name="settings" />{{ t('dashboard.configure') }}</button><button type="button" class="primary-button" @click="emit('enroll')"><UiIcon name="add" />{{ t('nav.enrollment') }}</button></div></div>
      <div v-show="preferencesOpen" id="node-view-preferences" class="server-view-controls" :aria-label="t('m.3fa2b5eb40e0')"><label>{{ t('m.9a9acd39e536') }}<select v-model="statusFilter"><option value="all">{{ t('m.2b13e491c90b') }}</option><option value="online">{{ t('m.b9086662b1df') }}</option><option value="pending">{{ t('m.a890b3be09aa') }}</option><option value="offline">{{ t('m.be1b4f3c6c1c') }}</option><option value="revoked">{{ t('m.fef96e34f77b') }}</option><option value="hidden">{{ t('m.58f1163e3fc6') }}</option></select></label><label>{{ t('m.88eac6c5df32') }}<select v-model="dashboardSettings.nodeGroupBy"><option value="status">{{ t('m.b8f945ea49ff') }}</option><option value="group">{{ t('m.c08b046b5470') }}</option><option value="none">{{ t('m.fcd40ee3f05a') }}</option></select></label><label>{{ t('m.13fffdc852b8') }}<select v-model="dashboardSettings.nodeSortBy"><option value="custom">{{ t('m.0f59d3742e06') }}</option><option value="name">{{ t('m.a98585871c53') }}</option><option value="status">{{ t('m.b8f945ea49ff') }}</option></select></label><label>{{ t('m.70006f6c5230') }}<input v-model.number="dashboardSettings.featuredLimit" type="number" min="1" max="20"></label><button type="button" class="container-action" :disabled="dashboardSettingsSaving" @click="void saveDashboardSettings()">{{ dashboardSettingsSaving ? t('m.ff509c9ba052') : t('m.b290bfe6352c') }}</button><span v-if="dashboardSettingsMessage" role="status">{{ display(dashboardSettingsMessage) }}</span><span v-if="dashboardSettingsError" role="alert">{{ display(dashboardSettingsError) }}</span></div>
      <p v-if="busy && nodes.length === 0" class="dashboard-empty">{{ t('m.c4608c61f330') }}</p><div v-else-if="nodes.length === 0" class="dashboard-welcome"><span class="welcome-icon"><UiIcon name="nodes" /></span><h2>{{ t('dashboard.empty.title') }}</h2><p>{{ t('dashboard.empty.description') }}</p><button type="button" class="primary-button" @click="emit('enroll')"><UiIcon name="add" />{{ t('dashboard.add') }}</button></div><p v-else-if="serverCardGroups.every((group) => group.nodes.length === 0)" class="dashboard-empty">{{ t('m.1af4c85a8053') }}</p>
      <section v-for="group in serverCardGroups" :key="group.key" class="vps-card-group" :data-group="group.key"><h2 v-if="group.label" class="vps-group-heading">{{ group.label }}</h2><div class="vps-card-grid"><VpsCard v-for="node in group.nodes" :key="node.nodeId" :node="node" :metrics="views[node.nodeId]" :inventory="dockerViews[node.nodeId]" :node-preference="nodePreference(node)" :container-preferences="nodePreferences[node.nodeId] ?? []" :preference-identities="preferenceIdentities[node.nodeId] ?? {}" :online="nodeIsOnline(node)" :pending="isPendingRegistration(node)" :docker-stale="dockerViews[node.nodeId] ? dockerIsStale(dockerViews[node.nodeId]) : !nodeIsOnline(node)" :preview-limit="dashboardSettings.featuredLimit" :current-time="wallClockNow" :node-reason="nodeReasons[node.nodeId]" :tasks="tasksForNode(node.nodeId)" :task-submitting="taskSubmitting" :task-errors="taskErrors" @view="chooseNode" @quick-action="openNodeShortcut" @container-action="submitCardContainerAction"/></div></section>
    </section>
  </section>
  <NodeDetail v-else :key="selectedNode.nodeId" :node="selectedNode" :metrics="views[selectedNode.nodeId]" :inventory="dockerViews[selectedNode.nodeId]" :preferences="nodePreferences[selectedNode.nodeId] ?? []" :preference-identities="preferenceIdentities[selectedNode.nodeId] ?? {}" :online="nodeIsOnline(selectedNode)" :pending="isPendingRegistration(selectedNode)" :docker-stale="dockerViews[selectedNode.nodeId] ? dockerIsStale(dockerViews[selectedNode.nodeId]) : !nodeIsOnline(selectedNode)" :node-reason="nodeReasons[selectedNode.nodeId]" :tasks="tasksForNode(selectedNode.nodeId)" :task-submitting="taskSubmitting" :task-errors="taskErrors" :dashboard-settings="dashboardSettings" :dashboard-settings-saving="dashboardSettingsSaving" :dashboard-settings-message="dashboardSettingsMessage" :dashboard-settings-error="dashboardSettingsError" :current-time="wallClockNow" @back="backToNodes" @refresh-tasks="(id) => { const node = nodes.find((item) => item.nodeId === id); if (node) void loadTasks(node) }" @preference-saved="acceptSavedPreference" @container-action="submitDetailContainerAction" @container-create="submitContainerCreateTask" @settings-change="dashboardSettings = $event" @save-settings="void saveDashboardSettings()"/>
  <TerminalConsole v-if="terminalNode" :key="terminalNode.nodeId" :node-id="terminalNode.nodeId" target-kind="host" :target-label="nodeTitle(terminalNode)" @close="terminalNode = null" />
</template>

<style scoped>
.nodes-dashboard { width:min(1480px,100%); margin:0 auto; padding:clamp(24px,5vw,52px); color:var(--text); }
.nodes-heading { display:flex; align-items:center; justify-content:space-between; gap:20px; margin-bottom:28px; }.nodes-heading h1 { margin:5px 0 0; font-size:clamp(27px,4vw,38px); letter-spacing:-.05em; }.nodes-heading p { margin:8px 0 0; color:var(--muted); font-size: 13px; }
.stream-state { display:inline-flex; align-items:center; gap:8px; border:1px solid var(--line); border-radius:999px; padding:8px 12px; color:var(--muted-strong); font-size: 12px; white-space:nowrap; }.stream-state i { width:7px; height:7px; border-radius:50%; background:var(--muted); }.stream-state[data-state=connected] i { background:var(--success); box-shadow:0 0 0 3px var(--success-soft); }
.nodes-error { margin-bottom:16px; border:1px solid var(--danger-line); border-radius:var(--radius-card); padding:12px 14px; color:var(--danger); background:var(--danger-soft); font-size: 12px; overflow-wrap:anywhere; }.vps-dashboard { display:grid; grid-template-columns:minmax(0,1fr); align-items:start; gap:12px; }
.server-view-controls { display:flex; flex-wrap:wrap; align-items:end; gap:8px; }.server-view-controls label { display:grid; min-width:140px; gap:5px; color:var(--muted-strong); font-size: 12px; }.server-view-controls input,.server-view-controls select { min-height:40px; min-width:0; border:1px solid var(--line); border-radius:var(--radius-control); padding:7px 9px; color:var(--text); background:var(--surface-soft); font:inherit; font-size: 12px; }.server-view-controls input { width:min(280px,58vw); }.server-view-controls button { min-height:40px; }.vps-card-group { display:grid; gap:9px; }.vps-card-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(min(100%,360px),1fr)); align-items:start; gap:12px; }.vps-group-heading { margin:0; color:var(--accent); font-size: 12px; font-weight:500; }.dashboard-empty { grid-column:1/-1; margin:0; border:1px dashed var(--line); border-radius:var(--radius-card); padding:20px; color:var(--muted-strong); font-size: 12px; line-height:1.6; }
@media(max-width:760px) { .vps-card-grid { grid-template-columns:repeat(auto-fit,minmax(min(100%,320px),1fr)); } }
@media(max-width:560px) { .nodes-dashboard { padding:22px 14px; }.nodes-heading { flex-direction:column; align-items:flex-start; gap:14px; }.stream-state { padding:7px 9px; font-size: 12px; }.server-view-controls,.server-view-controls label,.server-view-controls input,.server-view-controls select,.server-view-controls button { width:100%; } }
.node-summary { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:16px; margin-bottom:32px; }
.node-summary article { position:relative; display:grid; gap:16px; padding:22px 24px; border:1px solid var(--line); border-radius:var(--radius-card); background:var(--surface); }
.node-summary article>span { padding-right:14px; color:var(--muted); font-size:13px; }.node-summary strong { color:var(--text); font-size:32px; font-weight:400; font-variant-numeric:tabular-nums; letter-spacing:-1px; }
.node-summary i { position:absolute; top:27px; right:24px; width:6px; height:6px; border-radius:50%; background:var(--muted); }.node-summary [data-kind=online] i { background:var(--success); }.node-summary [data-kind=pending] i { background:var(--warning); }.node-summary [data-kind=total] i { background:var(--accent-fill); }
.dashboard-toolbar { display:flex; justify-content:space-between; align-items:center; flex-wrap:wrap; gap:16px; margin-bottom:8px; }.dashboard-toolbar-actions { display:flex; flex-wrap:wrap; gap:10px; }
.node-search { display:flex; align-items:center; gap:10px; width:min(360px,100%); border:1px solid var(--line-strong); border-radius:var(--radius-control); padding:0 12px; color:var(--muted); background:var(--surface); }.node-search input { min-width:0; width:100%; min-height:44px; border:0; background:transparent; color:var(--text); font:inherit; font-size:13px; }.node-search:focus-within { outline:2px solid var(--accent); outline-offset:3px; }.node-search input:focus { outline:none; }.node-search svg { flex-shrink:0; }
.view-preferences-button { min-height:44px; font-size:13px; }.dashboard-welcome { display:flex; flex-direction:column; align-items:center; justify-content:center; min-height:350px; border:1px dashed var(--line-strong); border-radius:var(--radius-card); padding:48px 24px; text-align:center; background:var(--surface); }
.welcome-icon { display:grid; place-items:center; width:64px; height:64px; border-radius:var(--radius-card); background:var(--accent-soft); color:var(--accent); }.welcome-icon svg { width:28px; height:28px; }.dashboard-welcome h2 { margin:24px 0 12px; font:400 30px/1.3 var(--display-font); letter-spacing:-.03em; }.dashboard-welcome p { max-width:430px; margin:0 0 24px; color:var(--muted); font-size:14px; line-height:1.7; }
@media(max-width:700px) { .node-summary { grid-template-columns:repeat(2,minmax(0,1fr)); gap:12px; }.node-summary article { padding:18px; }.node-summary i { top:24px; right:18px; }.dashboard-toolbar { align-items:stretch; }.node-search { width:100%; }.dashboard-toolbar-actions { width:100%; }.dashboard-toolbar-actions button { flex:1; }.dashboard-welcome { padding:32px 20px; }.dashboard-welcome h2 { font-size:26px; } }
</style>
