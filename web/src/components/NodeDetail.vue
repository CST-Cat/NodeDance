<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { api, type AgentNode, type ContainerTask, type ContainerTaskAction, type CreateContainerTaskPayload, type DashboardPreference, type DashboardSettings, type DockerContainerRecord, type DockerInventory, type MetricHistory, type TaskAuditEvent } from '../api'
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

const props = defineProps<{
  node: AgentNode
  metrics?: MetricsView
  inventory?: DockerInventory
  preferences: DashboardPreference[]
  preferenceIdentities: Record<string, string>
  online: boolean
  pending: boolean
  dockerStale: boolean
  nodeReason?: string
  tasks: ContainerTask[]
  taskSubmitting: Record<string, boolean>
  taskErrors: Record<string, string>
  dashboardSettings: DashboardSettings
  dashboardSettingsSaving: boolean
  dashboardSettingsMessage: string
  dashboardSettingsError: string
  currentTime: number
  initialSection: 'overview' | 'docker'
}>()
const emit = defineEmits<{
  back: []
  refreshTasks: [nodeId: string]
  preferenceSaved: [preference: DashboardPreference]
  containerAction: [payload: { nodeId: string; containerId: string; task: CreateContainerTaskPayload }]
  settingsChange: [settings: DashboardSettings]
  saveSettings: []
}>()

const activeSection = ref<'overview' | 'docker' | 'images' | 'compose' | 'files' | 'history' | 'events' | 'settings'>(props.initialSection)
const sectionTabs: Array<{ id: typeof activeSection.value; label: string }> = [
  { id: 'overview', label: '总览' }, { id: 'docker', label: 'Docker 详情' }, { id: 'images', label: '镜像' },
  { id: 'compose', label: 'Compose 项目' }, { id: 'files', label: '文件管理' }, { id: 'history', label: '历史监控' }, { id: 'events', label: '事件历史' }, { id: 'settings', label: '节点设置' },
]
const customFieldOptions = [
  { id: 'state', label: '运行状态' }, { id: 'ports', label: '端口' }, { id: 'health', label: '健康状态' },
  { id: 'image', label: '镜像' }, { id: 'uptime', label: '运行时间' },
]
const containerSearch = ref('')
const editingPreference = ref('')
const historyView = ref<MetricHistory | null>(null)
const historyLoading = ref(false)
const historyError = ref('')
const historyRange = ref<'6h' | '24h' | '7d' | '30d' | '1y'>('24h')
const draggedContainerID = ref('')
const dropContainerID = ref('')
const orderSaving = ref(false)
const orderError = ref('')
const taskAuditViews = ref<Record<string, TaskAuditEvent[]>>({})
const taskAuditVisible = ref<Record<string, boolean>>({})
const taskAuditLoading = ref<Record<string, boolean>>({})
const taskAuditErrors = ref<Record<string, string>>({})
const activeTerminal = ref<{ nodeId: string; targetKind: 'host' | 'container'; containerId?: string; targetLabel: string } | null>(null)
const pointerDrag = ref<{ pointerId: number; sourceID: string; targetID: string; startX: number; startY: number; armed: boolean } | null>(null)
const historyResolution = computed(() => historyRange.value === '1y' ? 'hour' : 'minute')
const nodePreference = computed(() => props.preferences.find((item) => item.targetKind === 'node' && item.identity === `node:${props.node.nodeId}`))
const nodeTitle = computed(() => nodePreference.value?.alias || props.node.displayName)
const selectedTasks = computed(() => [...props.tasks].sort((left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt)))
const viewMode = settingsField('viewMode')
const nodeGroupBy = settingsField('nodeGroupBy')
const nodeSortBy = settingsField('nodeSortBy')
const groupBy = settingsField('groupBy')
const sortBy = settingsField('sortBy')
const featuredLimit = settingsField('featuredLimit')
const customFields = computed({
  get: () => props.dashboardSettings.customFields,
  set: (value: string[]) => emit('settingsChange', { ...props.dashboardSettings, customFields: value }),
})

function settingsField<K extends keyof DashboardSettings>(key: K) {
  return computed({
    get: () => props.dashboardSettings[key],
    set: (value: DashboardSettings[K]) => emit('settingsChange', { ...props.dashboardSettings, [key]: value }),
  })
}

function preferenceForContainer(record: DockerInventory['containers'][number]): DashboardPreference | undefined {
  const identity = props.preferenceIdentities[record.container.id]
  if (!identity) return undefined
  const kind = record.container.compose ? 'compose_service' : 'container'
  return props.preferences.find((item) => item.targetKind === kind && item.identity === identity)
}
function containerTitle(record: DockerInventory['containers'][number]): string {
  return preferenceForContainer(record)?.alias || record.container.name || record.container.id.slice(0, 12)
}
function containerIcon(record: DockerInventory['containers'][number]): string {
  const icons: Record<string, string> = { server: '▤', globe: '◎', database: '▥', shield: '⬡', terminal: '›_', box: '▣', cloud: '☁', folder: '▰', activity: '⌁' }
  return icons[preferenceForContainer(record)?.icon ?? ''] ?? '▣'
}
function containerGroupName(record: DockerInventory['containers'][number]): string {
  if (props.dashboardSettings.groupBy === 'none') return ''
  if (props.dashboardSettings.groupBy === 'compose') return record.container.compose ? `Compose · ${record.container.compose.project}` : '独立容器'
  if (props.dashboardSettings.groupBy === 'state') return record.container.state || '状态未知'
  return nodeTitle.value
}
const orderedContainers = computed(() => {
  const containers = [...(props.inventory?.containers ?? [])]
  return containers.sort((left, right) => {
    const group = containerGroupName(left).localeCompare(containerGroupName(right), 'zh-CN')
    if (group && props.dashboardSettings.groupBy !== 'none') return group
    const lp = preferenceForContainer(left), rp = preferenceForContainer(right)
    if (props.dashboardSettings.sortBy === 'state') {
      const state = (left.container.state || '').localeCompare(right.container.state || '')
      if (state) return state
    }
    if (props.dashboardSettings.sortBy === 'name') return containerTitle(left).localeCompare(containerTitle(right), 'zh-CN')
    if (lp?.pinned !== rp?.pinned) return lp?.pinned ? -1 : 1
    if ((lp?.sortOrder ?? 0) !== (rp?.sortOrder ?? 0)) return (lp?.sortOrder ?? 0) - (rp?.sortOrder ?? 0)
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
function dockerAvailabilityText(inventory: DockerInventory): string {
  if (inventory.staleReason === 'docker_capability_unavailable' || inventory.dockerAvailability === 'unavailable') return 'Docker 不可用'
  if (inventory.dockerAvailability === 'available') {
    if (inventory.health?.errorKind === 'api_incompatible') return 'Docker API 不兼容'
    if (props.dockerStale) return '数据过期'
    if (inventory.health?.reason) return 'Engine 状态异常'
    return 'Engine 正常'
  }
  return '等待 Docker 状态'
}
function dockerStatusReason(inventory: DockerInventory): string { return inventory.health?.reason || inventory.staleReason || '' }
function containerHealthText(container: DockerInventory['containers'][number]['container']): string {
  if (!container.healthcheckConfigured || container.health === 'none') return '未配置健康检查'
  return ({ healthy: '健康', unhealthy: '异常', starting: '启动中', unknown: '未知' } as Record<string, string>)[container.health] ?? container.health
}
function portText(port: DockerInventory['containers'][number]['container']['ports'][number]): string {
  const published = Array.isArray(port.published) ? port.published : []
  const configured = Array.isArray(port.configured) ? port.configured : []
  if (published.length) return published.map((binding) => {
    const rawHost = binding.ip || '*'
    const host = rawHost.includes(':') && !rawHost.startsWith('[') ? `[${rawHost}]` : rawHost
    const scope = rawHost === '*' || rawHost === '0.0.0.0' || rawHost === '::' ? '（所有宿主机接口）' :
      rawHost === '::1' || /^127(?:\.\d{1,3}){3}$/.test(rawHost) ? '（仅宿主机回环）' : ''
    return `${host}:${binding.port} → ${port.containerPort}/${port.protocol}${scope}`
  }).join('，')
  if (configured.length) return `${port.containerPort}/${port.protocol}（配置映射，当前未发布）`
  if (port.exposed) return `${port.containerPort}/${port.protocol}（仅声明）`
  return `${port.containerPort}/${port.protocol}`
}
function containerNetworkText(container: DockerInventory['containers'][number]['container']): string {
  if (container.hostNetwork) return 'Host Network：容器共享宿主机网络地址'
  const networks = container.networks ?? []
  if (!networks.length) return '容器网络地址：未分配或不可用'
  return networks.map((network) => {
    const addresses = [network.ipv4 && `IPv4 ${network.ipv4}`, network.ipv6 && `IPv6 ${network.ipv6}`].filter(Boolean)
    return `${network.name}：${addresses.length ? addresses.join(' · ') : '未分配 IP'}`
  }).join('；')
}
function containerUptime(record: DockerInventory['containers'][number]): string {
  if (!record.container.running || !record.container.startedAt) return '未知'
  const started = Date.parse(record.container.startedAt)
  if (!Number.isFinite(started) || started > props.currentTime) return '未知'
  const minutes = Math.floor((props.currentTime - started) / 60_000)
  const days = Math.floor(minutes / 1440), hours = Math.floor(minutes % 1440 / 60), remaining = minutes % 60
  return days ? `${days} 天 ${hours} 小时` : hours ? `${hours} 小时 ${remaining} 分钟` : `${remaining} 分钟`
}
function safeServiceURL(record: DockerInventory['containers'][number]): string {
  const candidate = preferenceForContainer(record)?.serviceUrl
  if (!candidate) return ''
  try { const url = new URL(candidate); return ['http:', 'https:'].includes(url.protocol) && !url.username && !url.password && !url.hash ? url.href : '' } catch { return '' }
}
function preferenceForEditor(record: DockerInventory['containers'][number]): DashboardPreference | undefined {
  const identity = props.preferenceIdentities[record.container.id]
  if (!identity) return undefined
  const existing = preferenceForContainer(record)
  if (existing) return existing
  return { nodeId: props.node.nodeId, targetKind: record.container.compose ? 'compose_service' : 'container', identity,
    alias: '', icon: '', notes: '', serviceUrl: '', group: '',
    sortOrder: props.inventory?.containers.findIndex((item) => item.container.id === record.container.id) ?? 0, visible: true, pinned: false }
}
function nodePreferenceForEditor(): DashboardPreference {
  return nodePreference.value ?? { nodeId: props.node.nodeId, targetKind: 'node', identity: `node:${props.node.nodeId}`,
    alias: '', icon: 'server', notes: '', serviceUrl: '', group: '', sortOrder: -1, visible: true, pinned: true }
}
function serviceLinkTitle(record: DockerInventory['containers'][number]): string {
  return props.preferenceIdentities[record.container.id] ? '为此容器或 Compose 服务设置展示偏好' : 'Docker 未提供稳定身份，不能绑定展示偏好'
}
function taskKey(containerID: string): string { return `${props.node.nodeId}:${containerID}` }
function taskForContainer(containerID: string): ContainerTask | undefined {
  return [...props.tasks].filter((task) => task.targetId === containerID || task.action === 'rebuild' && task.status === 'succeeded' && task.result.resourceRevision === containerID)
    .sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))[0]
}
function rollbackTaskForContainer(containerID: string): ContainerTask | undefined {
  return props.tasks.filter((task) => task.action === 'rebuild' && task.status === 'succeeded' && task.targetId === containerID && Boolean(task.result.resourceRevision) && task.result.resourceRevision !== containerID)
    .sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))[0]
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
function canOperate(record: DockerInventory['containers'][number]): boolean {
  return Boolean(props.inventory?.dockerAvailability === 'available' && props.online && props.node.agentId && !props.dockerStale && !record.container.stale && !props.taskSubmitting[taskKey(record.container.id)])
}
function submitAction(record: DockerInventory['containers'][number], action: ContainerTaskAction) {
  if (!canOperate(record)) return
  if (action === 'rename') {
    if (record.container.compose) return
    const newName = window.prompt('输入新的独立容器名称', record.container.name)
    if (newName === null) return
    emit('containerAction', { nodeId: props.node.nodeId, containerId: record.container.id, task: { action, newName: newName.trim() } })
    return
  }
  if (action === 'delete') {
    if (record.container.running || record.container.paused || record.container.restarting) return
    const confirmation = window.prompt(`删除不会删除数据卷。请输入完整容器 ID 以确认：\n${record.container.id}`)
    if (confirmation !== record.container.id) return
    emit('containerAction', { nodeId: props.node.nodeId, containerId: record.container.id, task: { action, deleteConfirmed: true, deleteConfirmationId: confirmation } })
    return
  }
  emit('containerAction', { nodeId: props.node.nodeId, containerId: record.container.id, task: { action } })
}
function openHostTerminal() { activeTerminal.value = { nodeId: props.node.nodeId, targetKind: 'host', targetLabel: `${nodeTitle.value} · 主机终端` } }
function openContainerTerminal(record: DockerInventory['containers'][number]) { activeTerminal.value = { nodeId: props.node.nodeId, targetKind: 'container', containerId: record.container.id, targetLabel: `${nodeTitle.value} · ${record.container.name || record.container.id.slice(0, 12)}` } }
async function loadHistory() {
  historyLoading.value = true; historyError.value = ''
  const durations = { '6h': 6, '24h': 24, '7d': 168, '30d': 720, '1y': 8760 }
  try { historyView.value = await api.nodeHistory(props.node.nodeId, historyResolution.value, new Date(Date.now() - durations[historyRange.value] * 3_600_000), new Date()) }
  catch (reason) { historyView.value = null; historyError.value = reason instanceof Error ? reason.message : '无法读取历史指标。' }
  finally { historyLoading.value = false }
}
function selectSection(section: typeof activeSection.value) {
  activeSection.value = section
  if (section === 'history') void loadHistory()
  if (section === 'events') emit('refreshTasks', props.node.nodeId)
}
function taskStatusText(task: ContainerTask): string { return taskStatusLabel(task) }
async function toggleTaskAudit(task: ContainerTask) {
  const showing = Boolean(taskAuditVisible.value[task.taskId])
  taskAuditVisible.value = { ...taskAuditVisible.value, [task.taskId]: !showing }
  if (showing || taskAuditViews.value[task.taskId] || taskAuditLoading.value[task.taskId]) return
  taskAuditLoading.value = { ...taskAuditLoading.value, [task.taskId]: true }
  try { taskAuditViews.value = { ...taskAuditViews.value, [task.taskId]: (await api.nodeTaskAudit(task.nodeId, task.taskId)).events } }
  catch (reason) { taskAuditErrors.value = { ...taskAuditErrors.value, [task.taskId]: reason instanceof Error ? reason.message : '无法读取任务审计记录。' } }
  finally { taskAuditLoading.value = { ...taskAuditLoading.value, [task.taskId]: false } }
}
async function persistReorder(sourceID: string, targetID: string) {
  if (sourceID === targetID || props.dashboardSettings.sortBy !== 'custom') return
  const source = orderedContainers.value.find((item) => item.container.id === sourceID), target = orderedContainers.value.find((item) => item.container.id === targetID)
  const sourceIdentity = props.preferenceIdentities[sourceID], targetIdentity = props.preferenceIdentities[targetID]
  if (!source || !target || !sourceIdentity || !targetIdentity || sourceIdentity === targetIdentity || containerGroupName(source) !== containerGroupName(target)) return
  const identities = [...new Set(orderedContainers.value.map((item) => props.preferenceIdentities[item.container.id]).filter((item): item is string => Boolean(item)))]
  const from = identities.indexOf(sourceIdentity), to = identities.indexOf(targetIdentity)
  if (from < 0 || to < 0) return
  const [moved] = identities.splice(from, 1); identities.splice(to, 0, moved)
  orderSaving.value = true; orderError.value = ''
  try {
    for (let index = 0; index < identities.length; index += 1) {
      const record = orderedContainers.value.find((item) => props.preferenceIdentities[item.container.id] === identities[index])
      const preference = record && preferenceForEditor(record)
      if (!preference) throw new Error('缺少稳定容器身份，无法保存顺序。')
      const next = { ...preference, sortOrder: index }
      await api.saveNodePreference(next.nodeId, next)
      emit('preferenceSaved', next)
    }
  } catch (reason) { orderError.value = reason instanceof Error ? reason.message : '容器顺序保存失败。' }
  finally { orderSaving.value = false }
}
function beginNativeReorder(record: DockerInventory['containers'][number], event: DragEvent) {
  if (props.dashboardSettings.sortBy !== 'custom') { event.preventDefault(); return }
  draggedContainerID.value = record.container.id
  event.dataTransfer?.setData('text/plain', record.container.id)
  if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move'
}
function finishNativeReorder(record: DockerInventory['containers'][number]) {
  if (draggedContainerID.value) void persistReorder(draggedContainerID.value, record.container.id)
  draggedContainerID.value = ''
}
function beginPointerReorder(event: PointerEvent, record: DockerInventory['containers'][number]) {
  if (props.dashboardSettings.sortBy !== 'custom') return
  event.preventDefault()
  pointerDrag.value = { pointerId: event.pointerId, sourceID: record.container.id, targetID: record.container.id, startX: event.clientX, startY: event.clientY, armed: false }
  document.addEventListener('pointermove', movePointerReorder)
  document.addEventListener('pointerup', endPointerReorder, { once: true })
  document.addEventListener('pointercancel', endPointerReorder, { once: true })
}
function movePointerReorder(event: PointerEvent) {
  const drag = pointerDrag.value
  if (!drag || event.pointerId !== drag.pointerId) return
  if (Math.abs(event.clientX - drag.startX) + Math.abs(event.clientY - drag.startY) > 12) drag.armed = true
  if (!drag.armed) return
  const target = document.elementFromPoint(event.clientX, event.clientY)?.closest<HTMLElement>('[data-container-id]')
  if (target?.dataset.containerId) { drag.targetID = target.dataset.containerId; dropContainerID.value = drag.targetID }
}
function endPointerReorder(event: PointerEvent) {
  const drag = pointerDrag.value
  if (!drag || event.pointerId !== drag.pointerId) return
  pointerDrag.value = null
  document.removeEventListener('pointermove', movePointerReorder)
  document.removeEventListener('pointerup', endPointerReorder)
  document.removeEventListener('pointercancel', endPointerReorder)
  dropContainerID.value = ''
  if (drag.armed && drag.targetID !== drag.sourceID) void persistReorder(drag.sourceID, drag.targetID)
}
function acceptPreference(value: DashboardPreference) { emit('preferenceSaved', value) }
onBeforeUnmount(() => {
  document.removeEventListener('pointermove', movePointerReorder)
  document.removeEventListener('pointerup', endPointerReorder)
  document.removeEventListener('pointercancel', endPointerReorder)
})
</script>

<template>
  <section class="node-detail-page" aria-labelledby="detail-title" data-testid="node-detail">
    <header class="detail-heading">
      <div><button class="container-action back-button" type="button" @click="emit('back')">← 所有 VPS</button><span class="eyebrow">SINGLE VPS DETAIL</span><h1 id="detail-title">{{ nodeTitle }}</h1><p>{{ node.displayName }} · {{ node.nodeId }}</p></div>
      <div class="detail-heading-status"><span class="node-detail-status" :data-online="online">{{ pending ? '等待注册' : online ? '在线' : '离线' }}</span><span class="docker-state" :data-available="inventory?.dockerAvailability || 'unknown'" :data-stale="dockerStale">{{ inventory ? dockerAvailabilityText(inventory) : online ? '等待 Docker 状态' : 'Docker 状态未知' }}</span></div>
    </header>
    <nav class="dashboard-tabs" aria-label="节点管理视图"><button v-for="tab in sectionTabs" :key="tab.id" type="button" :aria-current="activeSection === tab.id ? 'page' : undefined" @click="selectSection(tab.id)">{{ tab.label }}</button></nav>
    <MetricsPanel v-if="activeSection === 'overview' && metrics" :key="metrics.nodeId" :view="metrics" />
    <div v-else-if="activeSection === 'overview'" class="metrics-waiting" data-testid="metrics-waiting"><span class="eyebrow">{{ node.displayName }}</span><h2>{{ pending ? '等待 Agent 注册' : online ? '等待首份主机样本' : '节点当前离线' }}</h2><p v-if="pending">节点已创建，Agent 完成注册后才会开始采集主机数据。</p><p v-else-if="online">CPU、内存、网络与磁盘数据会在 Agent 首次采样后显示。</p><p v-else>保留的最近数据会在重新连接时显示，并明确标记为过期。{{ nodeReason ? `状态原因：${nodeReason}` : '' }}</p><span class="node-detail-status" :data-online="online">{{ pending ? '等待注册' : online ? '在线' : '离线' }}</span></div>

    <template v-if="activeSection === 'docker'">
      <div class="node-terminal-entry"><div><strong>主机终端</strong><small>通过已连接 Agent 打开目标节点的配置 shell。</small></div><button type="button" :disabled="!online" @click="openHostTerminal">打开终端</button></div>
      <section class="docker-panel" aria-labelledby="docker-title" data-testid="docker-inventory">
        <header class="docker-heading"><div><span class="eyebrow">CONTAINER INVENTORY</span><h2 id="docker-title">Docker 容器</h2></div><span v-if="inventory" class="docker-state" :data-available="inventory.dockerAvailability" :data-stale="dockerStale">{{ dockerAvailabilityText(inventory) }}</span></header>
        <div class="docker-view-controls" aria-label="容器筛选和排序"><label>搜索 <input v-model="containerSearch" type="search" placeholder="名称、镜像、服务或备注" aria-label="搜索容器"></label><label>分组 <select v-model="groupBy"><option value="node">节点</option><option value="compose">Compose 项目</option><option value="state">运行状态</option><option value="none">不分组</option></select></label><label>排序 <select v-model="sortBy"><option value="custom">自定义</option><option value="name">显示名称</option><option value="state">运行状态</option></select></label><button type="button" class="container-action" :disabled="dashboardSettingsSaving" @click="emit('saveSettings')">保存视图</button></div>
        <p v-if="orderError" class="container-task-error" role="alert">{{ orderError }}</p><p v-if="!inventory" class="docker-empty">正在读取此节点的 Docker 状态。</p>
        <p v-else-if="inventory.dockerAvailability === 'unavailable' || inventory.staleReason === 'docker_capability_unavailable'" class="docker-empty">Agent 仍可在线采集主机指标；Docker Engine 当前不可用。{{ dockerStatusReason(inventory) ? `原因：${dockerStatusReason(inventory)}` : '' }}</p>
        <p v-else-if="dockerStatusReason(inventory)" class="docker-empty" data-testid="docker-health-reason">Docker Engine 当前不能提供完整容器状态，下面保留最近已知容器并标记为过期。原因：{{ dockerStatusReason(inventory) }}</p>
        <p v-if="inventory && inventory.containers.length === 0" class="docker-empty">{{ dockerStale ? '当前没有可确认的容器列表，等待完整扫描。' : '此节点没有容器。' }}</p><p v-if="inventory && inventory.containers.length > 0 && sortedContainers.length === 0" class="docker-empty">没有符合搜索条件的容器。</p>
        <div v-if="inventory && sortedContainers.length" class="docker-list" :data-group-by="dashboardSettings.groupBy">
          <article v-for="record in sortedContainers" :key="record.container.id" class="docker-row" :class="{ 'is-drop-target': dropContainerID === record.container.id, 'is-being-dragged': draggedContainerID === record.container.id }" :data-container-id="record.container.id" :data-preference-identity="preferenceIdentities[record.container.id]" :data-stale="dockerStale || record.container.stale" :data-group="containerGroupName(record)" draggable="true" @dragstart="beginNativeReorder(record, $event)" @dragover.prevent @drop="finishNativeReorder(record)" @dragend="draggedContainerID = ''">
            <div v-if="dashboardSettings.groupBy !== 'none'" class="container-group-label">{{ containerGroupName(record) }}</div>
            <div class="docker-row-title"><div class="docker-container-copy"><strong><span class="detail-container-icon" aria-hidden="true">{{ containerIcon(record) }}</span> {{ containerTitle(record) }}</strong><small><template v-if="dashboardSettings.customFields.includes('image')">{{ record.container.image || '未知镜像' }}</template><template v-if="record.container.compose"> · {{ record.container.compose.project }}/{{ record.container.compose.service }}</template><template v-if="preferenceForContainer(record)?.notes"> · {{ preferenceForContainer(record)?.notes }}</template></small></div><div class="container-actions"><span v-if="rollbackTaskForContainer(record.container.id)" class="container-state container-rollback-state">回滚副本</span><span v-else-if="dashboardSettings.customFields.includes('state')" class="container-state" :data-running="record.container.running">{{ record.container.state || '未知' }}</span><button type="button" :disabled="!record.container.running || record.container.stale || !online || dockerStale || Boolean(rollbackTaskForContainer(record.container.id))" @click="openContainerTerminal(record)">控制台</button></div></div>
            <div class="docker-row-meta"><span v-if="dashboardSettings.customFields.includes('health')" :data-health="record.container.health">健康：{{ containerHealthText(record.container) }}</span><span v-if="dashboardSettings.customFields.includes('uptime')">运行时间：{{ containerUptime(record) }}</span><span>网络：{{ containerNetworkText(record.container) }}</span><span v-if="dockerStale || record.container.stale" class="container-stale">过期数据<template v-if="record.container.unavailableReason">：{{ record.container.unavailableReason }}</template></span><span v-else>更新于 {{ new Date(record.receivedAt).toLocaleTimeString() }}</span></div>
            <div v-if="dashboardSettings.customFields.includes('ports') && (record.container.ports ?? []).length" class="container-ports" aria-label="容器端口"><span v-for="(port, index) in (record.container.ports ?? [])" :key="`${port.containerPort}-${port.protocol}-${index}`">{{ portText(port) }}</span></div><p v-if="record.container.unavailableReason" class="container-reason">{{ record.container.unavailableReason }}</p>
            <div class="container-task-actions"><p v-if="rollbackTaskForContainer(record.container.id)" class="container-rollback-note" role="note">此实例是已验证重建留下的停止回滚副本，不会作为主服务提供生命周期操作。</p><template v-else>
              <button class="container-action drag-handle" type="button" aria-label="拖动调整容器顺序" :disabled="orderSaving || dashboardSettings.sortBy !== 'custom' || !preferenceIdentities[record.container.id]" @pointerdown="beginPointerReorder($event, record)" title="按住并拖动可调整顺序">⠿</button>
              <button class="container-action" type="button" :disabled="!preferenceForEditor(record)" :title="serviceLinkTitle(record)" @click="editingPreference = editingPreference === record.container.id ? '' : record.container.id">{{ editingPreference === record.container.id ? '关闭偏好' : '编辑偏好' }}</button><a v-if="safeServiceURL(record)" class="container-action service-link" :href="safeServiceURL(record)" target="_blank" rel="noopener noreferrer">打开服务 ↗</a>
              <button v-if="record.container.paused" class="container-action" type="button" :disabled="!canOperate(record)" @click="submitAction(record, 'resume')">恢复</button><template v-else-if="record.container.running"><button class="container-action" type="button" :disabled="!canOperate(record)" @click="submitAction(record, 'stop')">停止</button><button class="container-action" type="button" :disabled="!canOperate(record)" @click="submitAction(record, 'restart')">重启容器</button><button class="container-action" type="button" :disabled="!canOperate(record)" @click="submitAction(record, 'pause')">暂停</button></template><button v-else class="container-action" type="button" :disabled="!canOperate(record)" @click="submitAction(record, 'start')">启动</button>
              <button class="container-action" type="button" :disabled="!canOperate(record) || Boolean(record.container.compose)" :title="record.container.compose ? 'Compose 项目容器不能通过实际重命名修改服务身份' : '重命名独立容器'" @click="submitAction(record, 'rename')">重命名</button><button class="container-action container-action-danger" type="button" :disabled="!canOperate(record) || record.container.running || record.container.paused || record.container.restarting" title="删除容器并保留数据卷" @click="submitAction(record, 'delete')">删除</button>
              <ContainerRebuildWizard :node-id="node.nodeId" :container="record.container" :stale="!canOperate(record)" :tasks="tasks"/><span v-if="taskSubmitting[taskKey(record.container.id)]" class="container-task-status" role="status">正在提交任务…</span><span v-if="taskForContainer(record.container.id)" class="container-task-status" :data-status="taskForContainer(record.container.id)?.status" role="status">{{ taskStatusLabel(taskForContainer(record.container.id)!) }}</span><button v-if="taskForContainer(record.container.id)" class="container-action" type="button" @click="toggleTaskAudit(taskForContainer(record.container.id)!)">{{ taskAuditVisible[taskForContainer(record.container.id)!.taskId] ? '隐藏审计' : '查看审计' }}</button><span v-if="taskErrors[taskKey(record.container.id)]" class="container-task-error" role="alert">{{ taskErrors[taskKey(record.container.id)] }}</span><span v-if="taskAuditErrors[taskForContainer(record.container.id)?.taskId || '']" class="container-task-error" role="alert">{{ taskAuditErrors[taskForContainer(record.container.id)?.taskId || ''] }}</span><ol v-if="taskForContainer(record.container.id) && taskAuditVisible[taskForContainer(record.container.id)!.taskId]" class="container-task-audit"><li v-if="taskAuditLoading[taskForContainer(record.container.id)!.taskId]">正在读取审计记录…</li><li v-for="event in (taskAuditViews[taskForContainer(record.container.id)!.taskId] ?? [])" :key="event.id">{{ event.event }} · {{ event.fromStatus || '开始' }} → {{ event.toStatus || '结果未知' }} · {{ new Date(event.occurredAt).toLocaleString() }}</li></ol>
            </template></div>
            <PreferenceEditor v-if="editingPreference === record.container.id && preferenceForEditor(record)" :preference="preferenceForEditor(record)!" :title="record.container.compose ? `Compose 服务偏好：${record.container.compose.project}/${record.container.compose.service}` : `容器偏好：${record.container.name || record.container.id.slice(0, 12)}`" @saved="acceptPreference"/><ContainerStreams v-if="!rollbackTaskForContainer(record.container.id)" :node-id="node.nodeId" :container-id="record.container.id" :disabled="dockerStale || record.container.stale" :stats-available="record.container.running && !record.container.paused && !record.container.restarting"/>
          </article>
        </div>
      </section>
    </template>
    <ImagesPanel v-if="activeSection === 'images'" :key="node.nodeId" :node-id="node.nodeId"/><ComposeProjects v-if="activeSection === 'compose'" :key="node.nodeId" :node-id="node.nodeId"/><NodeFiles v-if="activeSection === 'files'" :key="node.nodeId" :node-id="node.nodeId" :node-name="node.displayName"/>
    <section v-if="activeSection === 'history'" class="section-panel" aria-label="历史监控设置"><div class="section-toolbar"><div><span class="eyebrow">PERSISTED METRICS</span><h2>历史监控 · {{ nodeTitle }}</h2></div><div class="history-controls"><label>范围<select v-model="historyRange"><option value="6h">最近 6 小时</option><option value="24h">最近 24 小时</option><option value="7d">最近 7 天</option><option value="30d">最近 30 天</option><option value="1y">最近一年</option></select></label><button class="container-action" type="button" :disabled="historyLoading" @click="void loadHistory()">查询</button></div></div><HistoricalMetrics :history="historyView" :loading="historyLoading" :error="historyError" :node-online="online"/><p class="retention-note">分钟聚合保留 30 天，小时聚合保留一年。Agent 失联期间没有有效样本的区间会显示为空白。</p></section>
    <section v-if="activeSection === 'events'" class="section-panel" aria-label="节点操作事件历史" data-testid="node-event-history"><header class="section-toolbar"><div><span class="eyebrow">RECENT OPERATIONS</span><h2>操作事件历史 · {{ nodeTitle }}</h2></div><button class="container-action" type="button" @click="emit('refreshTasks', node.nodeId)">刷新</button></header><p class="event-source-note">这里展示 Core 已持久化的容器操作任务及其审计状态；Agent 失联时不推断操作成功。</p><p v-if="selectedTasks.length === 0" class="docker-empty">暂无已记录的容器操作事件。</p><article v-for="task in selectedTasks" :key="task.taskId" class="event-card" :data-status="task.status"><div><strong>{{ task.action }} · {{ task.targetId.slice(0, 12) }}</strong><span class="container-task-status" :data-status="task.status">{{ taskStatusText(task) }}</span><time :datetime="task.createdAt">{{ new Date(task.createdAt).toLocaleString() }}</time></div><button class="container-action" type="button" @click="toggleTaskAudit(task)">{{ taskAuditVisible[task.taskId] ? '隐藏审计' : '查看审计' }}</button><p v-if="taskAuditErrors[task.taskId]" class="container-task-error" role="alert">{{ taskAuditErrors[task.taskId] }}</p><ol v-if="taskAuditVisible[task.taskId]" class="container-task-audit"><li v-if="taskAuditLoading[task.taskId]">正在读取审计记录…</li><li v-for="event in (taskAuditViews[task.taskId] ?? [])" :key="event.id">{{ event.event }} · {{ event.fromStatus || '开始' }} → {{ event.toStatus || '结果未知' }} · {{ new Date(event.occurredAt).toLocaleString() }}</li></ol></article></section>
    <section v-if="activeSection === 'settings'" class="section-panel node-settings-panel" aria-label="节点设置" data-testid="node-settings"><header class="section-toolbar"><div><span class="eyebrow">DISPLAY & MONITORING</span><h2>节点设置 · {{ nodeTitle }}</h2></div></header><div class="node-settings-status"><div><span>Agent 连接</span><strong>{{ pending ? '等待注册' : online ? '在线' : '离线' }}</strong></div><div><span>Docker Engine</span><strong>{{ inventory ? dockerAvailabilityText(inventory) : '未知' }}</strong></div><div><span>Agent 版本</span><strong>{{ node.agentVersion || '未知' }}</strong></div><div><span>节点身份</span><strong>{{ node.nodeId }}</strong></div></div><PreferenceEditor :preference="nodePreferenceForEditor()" :title="`节点显示偏好：${node.displayName}`" @saved="acceptPreference"/><div class="dashboard-settings-editor"><h3>首页和容器视图</h3><div class="dashboard-settings-grid"><label>默认视图<select v-model="viewMode"><option value="monitor">监控</option><option value="manage">管理</option></select></label><label>VPS 首页分组<select v-model="nodeGroupBy"><option value="status">连接状态</option><option value="group">自定义分组</option><option value="none">不分组</option></select></label><label>VPS 首页排序<select v-model="nodeSortBy"><option value="custom">自定义顺序</option><option value="name">显示名称</option><option value="status">连接状态</option></select></label><label>容器分组方式<select v-model="groupBy"><option value="node">节点</option><option value="compose">Compose 项目</option><option value="state">运行状态</option><option value="none">不分组</option></select></label><label>容器排序方式<select v-model="sortBy"><option value="custom">自定义顺序</option><option value="name">显示名称</option><option value="state">运行状态</option></select></label><label>首页容器上限<input v-model.number="featuredLimit" type="number" min="1" max="20"></label></div><fieldset class="custom-fields"><legend>Docker 详情字段</legend><label v-for="field in customFieldOptions" :key="field.id"><input v-model="customFields" type="checkbox" :value="field.id">{{ field.label }}</label></fieldset><div class="settings-save"><button class="container-action" type="button" :disabled="dashboardSettingsSaving" @click="emit('saveSettings')">{{ dashboardSettingsSaving ? '保存中…' : '保存首页设置' }}</button><span v-if="dashboardSettingsMessage" role="status">{{ dashboardSettingsMessage }}</span><span v-if="dashboardSettingsError" role="alert">{{ dashboardSettingsError }}</span></div></div><p class="preference-separation-note">别名、图标、备注和顺序保存在 NodeDance SQLite 中。容器运行状态、健康状态和端口始终读取 Docker Engine。</p></section>
    <NodeServiceProbes :key="node.nodeId" :node-id="node.nodeId" :node-name="node.displayName" :node-online="online"/><TerminalConsole v-if="activeTerminal" v-bind="activeTerminal" @close="activeTerminal = null"/>
  </section>
</template>

<style scoped>
.node-detail-page { width: min(1480px,100%); min-width: 0; margin: 0 auto; padding: clamp(20px,4vw,44px); color: #eaf0fa; }
.detail-heading { display:flex; align-items:center; justify-content:space-between; gap:20px; margin-bottom:20px; }
.detail-heading h1 { margin:6px 0 4px; font-size:clamp(24px,3vw,34px); overflow-wrap:anywhere; }
.detail-heading p { margin:0; color:#91a2ba; font:10px/1.5 ui-monospace,monospace; overflow-wrap:anywhere; }
.back-button { margin-bottom:12px; }
.detail-heading-status { display:flex; flex-wrap:wrap; justify-content:flex-end; gap:8px; }
.dashboard-tabs { display:flex; gap:6px; overflow-x:auto; margin:0 0 16px; border-bottom:1px solid rgba(171,196,232,.12); padding-bottom:8px; scrollbar-width:thin; }
.dashboard-tabs button { flex:0 0 auto; min-height:38px; border:1px solid transparent; border-radius:7px; padding:7px 12px; color:#aebbd0; background:transparent; font:inherit; font-size:10px; cursor:pointer; }
.dashboard-tabs button[aria-current=page] { border-color:rgba(141,201,255,.22); color:#d6ebff; background:rgba(62,119,170,.18); }
.metrics-waiting { position:relative; min-height:260px; display:flex; flex-direction:column; align-items:flex-start; justify-content:center; padding:clamp(20px,5vw,52px); border:1px solid rgba(171,196,232,.13); border-radius:12px; background:rgba(15,25,40,.62); }
.metrics-waiting h2 { margin:10px 0 0; font-size:21px; }.metrics-waiting p { max-width:440px; color:#9aabc1; font-size:12px; line-height:1.7; }
.node-detail-status,.docker-state { border:1px solid rgba(171,196,232,.15); border-radius:999px; padding:6px 11px; color:#ffb4aa; font-size:10px; }
.node-detail-status[data-online=true],.docker-state[data-available=available][data-stale=false] { color:#9ce0b7; }
.node-terminal-entry { display:flex; align-items:center; justify-content:space-between; gap:12px; margin-top:16px; border:1px solid rgba(121,214,156,.18); border-radius:10px; padding:12px 14px; background:rgba(31,89,62,.1); }
.node-terminal-entry>div { display:grid; gap:4px; }.node-terminal-entry strong { color:#cfe9d7; font-size:11px; }.node-terminal-entry small { color:#94aa9d; font-size:10px; line-height:1.5; }
.node-terminal-entry button,.container-actions button { min-height:34px; border:1px solid rgba(121,214,156,.28); border-radius:7px; padding:6px 10px; color:#a9e7bd; background:rgba(73,152,99,.12); font-size:10px; cursor:pointer; }
.node-terminal-entry button:disabled,.container-actions button:disabled { opacity:.45; cursor:not-allowed; }
.docker-panel,.section-panel { min-width:0; margin-top:16px; border:1px solid rgba(171,196,232,.13); border-radius:12px; padding:clamp(14px,2vw,20px); background:rgba(9,17,29,.46); }
.docker-heading,.section-toolbar { display:flex; align-items:center; justify-content:space-between; gap:12px; margin-bottom:12px; }.docker-heading h2,.section-toolbar h2 { margin:4px 0 0; font-size:17px; }
.docker-view-controls,.history-controls { display:flex; flex-wrap:wrap; align-items:end; gap:8px; margin:12px 0; }.docker-view-controls label,.history-controls label { display:grid; min-width:120px; gap:5px; color:#9aabc1; font-size:9px; }
.docker-view-controls input,.docker-view-controls select,.history-controls select,.dashboard-settings-grid select,.dashboard-settings-grid input { min-height:36px; min-width:0; border:1px solid rgba(171,196,232,.16); border-radius:6px; padding:7px 9px; color:#eaf0fa; background:#111b29; font:inherit; font-size:10px; }
.docker-view-controls input { width:min(320px,58vw); }.docker-list { display:grid; gap:9px; }.docker-row { min-width:0; border:1px solid rgba(171,196,232,.1); border-radius:9px; padding:12px; background:rgba(18,29,45,.68); }.docker-row[data-stale=true] { border-color:rgba(255,176,129,.22); }
.docker-row.is-drop-target { outline:2px solid rgba(105,184,255,.72); outline-offset:2px; }.docker-row.is-being-dragged { opacity:.55; }.drag-handle { touch-action:none; user-select:none; cursor:grab; }.drag-handle:active { cursor:grabbing; }
.docker-row-title { display:flex; justify-content:space-between; align-items:flex-start; gap:12px; }.docker-container-copy { display:grid; min-width:0; gap:4px; }.docker-container-copy strong { overflow-wrap:anywhere; font-size:12px; }.detail-container-icon { color:#a9d5ff; }.docker-container-copy small { overflow-wrap:anywhere; color:#93a4bb; font-size:10px; }.container-actions { display:flex; flex-wrap:wrap; align-items:center; gap:8px; }
.container-group-label { margin:0 0 8px; color:#8bb4f4; font-size:9px; font-weight:600; }.docker-row-meta { display:flex; flex-wrap:wrap; gap:7px 14px; margin-top:9px; color:#a5b3c8; font-size:10px; }.container-ports { display:grid; gap:4px; margin-top:8px; color:#8dc9ff; font-size:10px; overflow-wrap:anywhere; }
.container-state { border:1px solid rgba(171,196,232,.16); border-radius:999px; padding:5px 9px; color:#aebbd0; font-size:10px; white-space:nowrap; }.container-state[data-running=true] { color:#9ce0b7; border-color:rgba(121,214,156,.24); }.container-stale,.container-task-error { color:#ffc1b8; }.container-rollback-state,.container-rollback-note { color:#ffd092; }.container-rollback-note { flex:1 0 100%; margin:4px 0; font-size:11px; line-height:1.6; }
.container-reason,.docker-empty,.event-source-note,.retention-note,.preference-separation-note { color:#9aabc1; font-size:11px; line-height:1.6; overflow-wrap:anywhere; }.container-task-actions { display:flex; flex-wrap:wrap; align-items:center; gap:8px; margin-top:10px; }.container-action { min-height:34px; border:1px solid rgba(141,201,255,.25); border-radius:7px; padding:6px 10px; color:#cce6ff; background:rgba(62,119,170,.16); font:inherit; font-size:10px; cursor:pointer; }.container-action:disabled { opacity:.48; cursor:not-allowed; }.container-action-danger { border-color:rgba(255,129,116,.3); color:#ffc1b8; background:rgba(184,77,72,.1); }.service-link { color:#cce6ff; text-decoration:none; }.container-task-status,.container-task-error { font-size:10px; overflow-wrap:anywhere; }.container-task-audit { flex-basis:100%; margin:4px 0 0; padding:8px 8px 8px 26px; border:1px solid rgba(171,196,232,.1); border-radius:7px; color:#a8bdd7; font-size:10px; }.container-task-audit li { padding:3px 0; }
.history-controls { margin:0; }.history-controls select { min-width:150px; }.retention-note,.event-source-note,.preference-separation-note { font-size:10px; }.event-card { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:8px; margin-top:8px; border:1px solid rgba(171,196,232,.1); border-radius:8px; padding:10px; background:rgba(18,29,45,.68); }.event-card>div:first-child { display:grid; gap:5px; }.event-card strong { font-size:11px; overflow-wrap:anywhere; }.event-card time { color:#8193aa; font-size:9px; }.event-card .container-task-audit,.event-card>p { grid-column:1/-1; }
.node-settings-status { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:8px; margin:12px 0; }.node-settings-status>div { display:grid; min-width:0; gap:5px; border:1px solid rgba(171,196,232,.1); border-radius:7px; padding:9px; }.node-settings-status strong { color:#dce6f5; font-size:10px; overflow-wrap:anywhere; }.dashboard-settings-editor { margin-top:16px; border-top:1px solid rgba(171,196,232,.1); padding-top:14px; }.dashboard-settings-editor h3 { margin:0 0 12px; font-size:12px; }.dashboard-settings-grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:9px; }.dashboard-settings-grid label { display:grid; min-width:0; gap:5px; color:#9aabc1; font-size:9px; }.custom-fields { display:flex; flex-wrap:wrap; gap:8px 14px; margin:12px 0; border:1px solid rgba(171,196,232,.1); border-radius:7px; padding:10px; }.custom-fields legend { padding:0 5px; color:#9aabc1; font-size:9px; }.custom-fields label { display:inline-flex; align-items:center; gap:5px; color:#aebbd0; font-size:9px; }.settings-save { display:flex; flex-wrap:wrap; align-items:center; gap:9px; color:#9ce0b7; font-size:9px; }.settings-save span[role=alert] { color:#ffc1b8; }
@media(max-width:900px) { .node-settings-status { grid-template-columns:repeat(2,minmax(0,1fr)); }.dashboard-settings-grid { grid-template-columns:repeat(2,minmax(0,1fr)); } }
@media(max-width:560px) { .node-detail-page { padding:20px 14px; }.detail-heading { align-items:flex-start; flex-direction:column; }.detail-heading-status { justify-content:flex-start; }.section-toolbar,.docker-heading { align-items:flex-start; flex-direction:column; }.history-controls { width:100%; }.history-controls label,.history-controls select { width:100%; }.docker-view-controls,.docker-view-controls label,.docker-view-controls input,.docker-view-controls select,.docker-view-controls button { width:100%; }.node-settings-status,.dashboard-settings-grid { grid-template-columns:minmax(0,1fr); }.docker-row-title { align-items:flex-start; flex-wrap:wrap; }.container-actions { width:100%; }.container-actions button { min-height:42px; flex:1; }.node-terminal-entry { align-items:flex-start; flex-direction:column; }.node-terminal-entry button { width:100%; min-height:42px; } }
</style>
