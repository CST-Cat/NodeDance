<script setup lang="ts">
import { t, display, locale } from "../i18n"
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api, type AgentNode, type ContainerCreateSpec, type ContainerTask, type ContainerTaskAction, type CreateContainerTaskPayload, type DashboardPreference, type DashboardSettings, type DockerContainerRecord, type DockerInventory, type MetricHistory, type TaskAuditEvent } from '../api'
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
import ContainerCreateForm from './ContainerCreateForm.vue'

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
  initialSection: 'overview' | 'docker' | 'files'
  startHostTerminal?: boolean
}>()
const emit = defineEmits<{
  back: []
  refreshTasks: [nodeId: string]
  preferenceSaved: [preference: DashboardPreference]
  containerAction: [payload: { nodeId: string; containerId: string; task: CreateContainerTaskPayload }]
  containerCreate: [payload: { nodeId: string; spec: ContainerCreateSpec }]
  settingsChange: [settings: DashboardSettings]
  saveSettings: []
}>()

const activeSection = ref<'overview' | 'docker' | 'images' | 'compose' | 'rebuild' | 'files' | 'history' | 'events' | 'settings'>(props.initialSection)
const sectionTabs = computed<Array<{ id: typeof activeSection.value; label: string }>>(() => [
  { id: 'overview', label: t('m.a33db5730556') }, { id: 'docker', label: t('m.eee901e4cc16') }, { id: 'images', label: t('m.176c09844e0f') },
  { id: 'compose', label: t('m.5f67dd75b01f') }, { id: 'rebuild', label: t('m.474490ab5dc3') }, { id: 'files', label: t('m.b3bd5ac7cc4d') }, { id: 'history', label: t('m.bfb41404e88b') }, { id: 'events', label: t('m.a700a47380fe') }, { id: 'settings', label: t('m.0a09fda5e2a6') },
])
const customFieldOptions = computed(() => [
  { id: 'state', label: t('m.e46b0272bd64') }, { id: 'ports', label: t('m.e71ac32b544b') }, { id: 'health', label: t('m.85788192fbeb') },
  { id: 'image', label: t('m.176c09844e0f') }, { id: 'uptime', label: t('m.5986e9d976e0') },
])
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
const selectedRebuildContainerID = ref('')
const rebuildCandidates = computed(() => orderedContainers.value.filter((record) => !record.container.compose && !rollbackTaskForContainer(record.container.id)))
const selectedRebuildContainer = computed(() => rebuildCandidates.value.find((record) => record.container.id === selectedRebuildContainerID.value) ?? rebuildCandidates.value[0])
const createTaskKey = computed(() => `${props.node.nodeId}:container-create`)
const createTasks = computed(() => selectedTasks.value.filter((task) => task.action === 'container_create').slice(0, 3))
const canCreateContainers = computed(() => Boolean(props.inventory?.dockerAvailability === 'available' && props.online && props.node.agentId && !props.dockerStale))
const createSubmitting = computed(() => Boolean(props.taskSubmitting[createTaskKey.value]))
const createError = computed(() => props.taskErrors[createTaskKey.value] || '')
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
  if (props.dashboardSettings.groupBy === 'compose') return record.container.compose ? `Compose · ${record.container.compose.project}` : t('m.ba4ddea11c26')
  if (props.dashboardSettings.groupBy === 'state') return record.container.state || t('m.93e474c68ed5')
  return nodeTitle.value
}
const orderedContainers = computed(() => {
  const containers = [...(props.inventory?.containers ?? [])]
  return containers.sort((left, right) => {
    const group = containerGroupName(left).localeCompare(containerGroupName(right), locale.value)
    if (group && props.dashboardSettings.groupBy !== 'none') return group
    const lp = preferenceForContainer(left), rp = preferenceForContainer(right)
    if (props.dashboardSettings.sortBy === 'state') {
      const state = (left.container.state || '').localeCompare(right.container.state || '')
      if (state) return state
    }
    if (props.dashboardSettings.sortBy === 'name') return containerTitle(left).localeCompare(containerTitle(right), locale.value)
    if (lp?.pinned !== rp?.pinned) return lp?.pinned ? -1 : 1
    if ((lp?.sortOrder ?? 0) !== (rp?.sortOrder ?? 0)) return (lp?.sortOrder ?? 0) - (rp?.sortOrder ?? 0)
    return containerTitle(left).localeCompare(containerTitle(right), locale.value)
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
  if (inventory.staleReason === 'docker_capability_unavailable' || inventory.dockerAvailability === 'unavailable') return t('m.d3e431f9d10b')
  if (inventory.dockerAvailability === 'available') {
    if (inventory.health?.errorKind === 'api_incompatible') return t('m.d56e55420ff6')
    if (props.dockerStale) return t('m.1a6858956811')
    if (inventory.health?.reason) return t('m.04dea0459ab7')
    return t('m.81e93ba5b0a1')
  }
  return t('m.c12a50c262ca')
}
function dockerStatusReason(inventory: DockerInventory): string { return inventory.health?.reason || inventory.staleReason || '' }
function containerHealthText(container: DockerInventory['containers'][number]['container']): string {
  if (!container.healthcheckConfigured || container.health === 'none') return t('m.5fac1c671a0d')
  return ({ healthy: t('m.aa18b6dcd893'), unhealthy: t('m.428fb8bfeecf'), starting: t('m.33439d263173'), unknown: t('m.4d8c1c5b4283') } as Record<string, string>)[container.health] ?? container.health
}
function portText(port: DockerInventory['containers'][number]['container']['ports'][number]): string {
  const published = Array.isArray(port.published) ? port.published : []
  const configured = Array.isArray(port.configured) ? port.configured : []
  if (published.length) return published.map((binding) => {
    const rawHost = binding.ip || '*'
    const host = rawHost.includes(':') && !rawHost.startsWith('[') ? `[${rawHost}]` : rawHost
    const scope = rawHost === '*' || rawHost === '0.0.0.0' || rawHost === '::' ? t('m.a54cd64d75d5') :
      rawHost === '::1' || /^127(?:\.\d{1,3}){3}$/.test(rawHost) ? t('m.b07e34088b42') : ''
    return `${host}:${binding.port} → ${port.containerPort}/${port.protocol}${scope}`
  }).join('，')
  if (configured.length) return t('m.639b66233cf4', {"0": (port.containerPort), "1": (port.protocol)})
  if (port.exposed) return t('m.c39c143634dc', {"0": (port.containerPort), "1": (port.protocol)})
  return `${port.containerPort}/${port.protocol}`
}
function containerNetworkText(container: DockerInventory['containers'][number]['container']): string {
  if (container.hostNetwork) return t('m.471522ad66e5')
  const networks = container.networks ?? []
  if (!networks.length) return t('m.6537857befe0')
  return networks.map((network) => {
    const addresses = [network.ipv4 && `IPv4 ${network.ipv4}`, network.ipv6 && `IPv6 ${network.ipv6}`].filter(Boolean)
    return t('m.838f6df66524', {"0": (network.name), "1": (addresses.length ? addresses.join(' · ') : t('m.9bf769e4c1eb'))})
  }).join('；')
}
function containerUptime(record: DockerInventory['containers'][number]): string {
  if (!record.container.running || !record.container.startedAt) return t('m.4d8c1c5b4283')
  const started = Date.parse(record.container.startedAt)
  if (!Number.isFinite(started) || started > props.currentTime) return t('m.4d8c1c5b4283')
  const minutes = Math.floor((props.currentTime - started) / 60_000)
  const days = Math.floor(minutes / 1440), hours = Math.floor(minutes % 1440 / 60), remaining = minutes % 60
  return days ? t('m.295f367bb72a', {"0": (days), "1": (hours)}) : hours ? t('m.3cde3f0ae16c', {"0": (hours), "1": (remaining)}) : t('m.c65627ead130', {"0": (remaining)})
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
    alias: '', icon: '', notes: '', serviceUrl: '', sshHost: '', sshPort: 22, sshUser: '', group: '',
    sortOrder: props.inventory?.containers.findIndex((item) => item.container.id === record.container.id) ?? 0, visible: true, pinned: false }
}
function nodePreferenceForEditor(): DashboardPreference {
  return nodePreference.value ?? { nodeId: props.node.nodeId, targetKind: 'node', identity: `node:${props.node.nodeId}`,
    alias: '', icon: 'server', notes: '', serviceUrl: '', sshHost: '', sshPort: 22, sshUser: '', group: '', sortOrder: -1, visible: true, pinned: true }
}
function serviceLinkTitle(record: DockerInventory['containers'][number]): string {
  return props.preferenceIdentities[record.container.id] ? t('m.c2ac23b68436') : t('m.3b55a14ddabf')
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
  if (task.status === 'queued') return t('m.3d531ad52ee4')
  if (task.status === 'running') return t('m.472b61dd983a', {"0": (task.progress.phase)})
  if (task.status === 'succeeded') return t('m.52a8bda11dff', {"0": (task.result.observedState || t('m.74a52b93c4f9'))})
  if (task.status === 'unknown') return t('m.2629197ac89c')
  if (task.status === 'timed_out') return t('m.7112c0bda6de')
  if (task.status === 'canceled') return t('m.6f96c2add214')
  return t('m.e6bbfaf0e706', {"0": (task.result.code || t('m.fb2425b0c8e1'))})
}
function canOperate(record: DockerInventory['containers'][number]): boolean {
  return Boolean(props.inventory?.dockerAvailability === 'available' && props.online && props.node.agentId && !props.dockerStale && !record.container.stale && !props.taskSubmitting[taskKey(record.container.id)])
}
function submitAction(record: DockerInventory['containers'][number], action: ContainerTaskAction) {
  if (!canOperate(record)) return
  if (action === 'rename') {
    if (record.container.compose) return
    const newName = window.prompt(t('m.19efbadaa133'), record.container.name)
    if (newName === null) return
    emit('containerAction', { nodeId: props.node.nodeId, containerId: record.container.id, task: { action, newName: newName.trim() } })
    return
  }
  if (action === 'delete') {
    if (record.container.running || record.container.paused || record.container.restarting) return
    const confirmation = window.prompt(t('m.cd8d2b02a176', {"0": (record.container.id)}))
    if (confirmation !== record.container.id) return
    emit('containerAction', { nodeId: props.node.nodeId, containerId: record.container.id, task: { action, deleteConfirmed: true, deleteConfirmationId: confirmation } })
    return
  }
  emit('containerAction', { nodeId: props.node.nodeId, containerId: record.container.id, task: { action } })
}
function submitContainerCreate(spec: ContainerCreateSpec) {
  if (!canCreateContainers.value || createSubmitting.value) return
  emit('containerCreate', { nodeId: props.node.nodeId, spec })
}
function openHostTerminal() { activeTerminal.value = { nodeId: props.node.nodeId, targetKind: 'host', targetLabel: t('m.d4c37e398335', {"0": (nodeTitle.value)}) } }
function openContainerTerminal(record: DockerInventory['containers'][number]) { activeTerminal.value = { nodeId: props.node.nodeId, targetKind: 'container', containerId: record.container.id, targetLabel: `${nodeTitle.value} · ${record.container.name || record.container.id.slice(0, 12)}` } }
async function loadHistory() {
  historyLoading.value = true; historyError.value = ''
  const durations = { '6h': 6, '24h': 24, '7d': 168, '30d': 720, '1y': 8760 }
  try { historyView.value = await api.nodeHistory(props.node.nodeId, historyResolution.value, new Date(Date.now() - durations[historyRange.value] * 3_600_000), new Date()) }
  catch (reason) { historyView.value = null; historyError.value = reason instanceof Error ? reason.message : t('m.7b26031b0a1a') }
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
  catch (reason) { taskAuditErrors.value = { ...taskAuditErrors.value, [task.taskId]: reason instanceof Error ? reason.message : t('m.df3eec8deda0') } }
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
      if (!preference) throw new Error(t('m.96af268964a3'))
      const next = { ...preference, sortOrder: index }
      await api.saveNodePreference(next.nodeId, next)
      emit('preferenceSaved', next)
    }
  } catch (reason) { orderError.value = reason instanceof Error ? reason.message : t('m.4f4eebba20f3') }
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
watch(rebuildCandidates, (records) => {
  if (!records.some((record) => record.container.id === selectedRebuildContainerID.value)) {
    selectedRebuildContainerID.value = records[0]?.container.id ?? ''
  }
}, { immediate: true })
onMounted(() => {
  if (props.startHostTerminal && props.online) openHostTerminal()
})
onBeforeUnmount(() => {
  document.removeEventListener('pointermove', movePointerReorder)
  document.removeEventListener('pointerup', endPointerReorder)
  document.removeEventListener('pointercancel', endPointerReorder)
})
</script>

<template>
  <section class="node-detail-page" aria-labelledby="detail-title" data-testid="node-detail">
    <header class="detail-heading">
      <div><button class="container-action back-button" type="button" @click="emit('back')">{{ t('m.40c2548e2416') }}</button><span class="eyebrow">{{ t('label.164ba94c1c7b') }}</span><h1 id="detail-title">{{ nodeTitle }}</h1><p>{{ node.displayName }} · {{ node.nodeId }}</p><p v-if="nodePreference?.notes" class="node-detail-note">{{ nodePreference.notes }}</p></div>
      <div class="detail-heading-status"><span class="node-detail-status" :data-online="online">{{ pending ? t('m.a890b3be09aa') : online ? t('m.b9086662b1df') : t('m.be1b4f3c6c1c') }}</span><span class="docker-state" :data-available="inventory?.dockerAvailability || 'unknown'" :data-stale="dockerStale">{{ inventory ? dockerAvailabilityText(inventory) : online ? t('m.c12a50c262ca') : t('m.8a6501e8198f') }}</span></div>
    </header>
    <nav class="dashboard-tabs" :aria-label="t('m.c778eb5beb54')"><button v-for="tab in sectionTabs" :key="tab.id" type="button" :aria-current="activeSection === tab.id ? 'page' : undefined" @click="selectSection(tab.id)">{{ tab.label }}</button></nav>
    <MetricsPanel v-if="activeSection === 'overview' && metrics" :key="metrics.nodeId" :view="metrics" />
    <div v-else-if="activeSection === 'overview'" class="metrics-waiting" data-testid="metrics-waiting"><span class="eyebrow">{{ node.displayName }}</span><h2>{{ pending ? t('m.270c0a7750e1') : online ? t('m.78a4908b8949') : t('m.f339ad3bc760') }}</h2><p v-if="pending">{{ t('m.31483a081b8c') }}</p><p v-else-if="online">{{ t('m.ad2b360a6731') }}</p><p v-else>{{ t('m.fcca199d47b9') }}{{ nodeReason ? t('m.fee0ec31e108', {'0': (nodeReason)}) : '' }}</p><span class="node-detail-status" :data-online="online">{{ pending ? t('m.a890b3be09aa') : online ? t('m.b9086662b1df') : t('m.be1b4f3c6c1c') }}</span></div>

    <template v-if="activeSection === 'docker'">
      <div class="node-terminal-entry"><div><strong>{{ t('m.725457c84556') }}</strong><small>{{ t('m.a7c1e29fcce5') }}</small></div><button type="button" :disabled="!online" @click="openHostTerminal">{{ t('m.b89a0e4584f2') }}</button></div>
      <section class="docker-panel" aria-labelledby="docker-title" data-testid="docker-inventory">
        <header class="docker-heading"><div><span class="eyebrow">{{ t('label.c039e720422e') }}</span><h2 id="docker-title">{{ t('m.7cabcc483eb4') }}</h2></div><span v-if="inventory" class="docker-state" :data-available="inventory.dockerAvailability" :data-stale="dockerStale">{{ dockerAvailabilityText(inventory) }}</span></header>
        <ContainerCreateForm :disabled="!canCreateContainers" :submitting="createSubmitting" :error="createError" @submit="submitContainerCreate" />
        <section v-if="createTasks.length" class="container-create-results" :aria-label="t('m.7084b942d42e')"><h3>{{ t('m.964c43bb9238') }}</h3><article v-for="task in createTasks" :key="task.taskId"><div><strong>{{ taskStatusLabel(task) }}</strong><time :datetime="task.createdAt">{{ new Date(task.createdAt).toLocaleString(locale) }}</time></div><code v-if="task.status === 'succeeded' && task.result.resourceRevision">Container ID：{{ task.result.resourceRevision }}</code><span v-else-if="task.status === 'failed'" class="container-task-error">{{ task.result.code || 'creation_failed' }}</span></article></section>
        <div class="docker-view-controls" :aria-label="t('m.4eb61dda65ee')"><label>{{ t('m.44ce7ae909bb') }}<input v-model="containerSearch" type="search" :placeholder="t('m.5a5d5c8b732a')" :aria-label="t('m.8db3ff343ebc')"></label><label>{{ t('m.399ee6934772') }}<select v-model="groupBy"><option value="node">{{ t('m.2410d860f093') }}</option><option value="compose">{{ t('m.5f67dd75b01f') }}</option><option value="state">{{ t('m.e46b0272bd64') }}</option><option value="none">{{ t('m.fcd40ee3f05a') }}</option></select></label><label>{{ t('m.a96c9a854190') }}<select v-model="sortBy"><option value="custom">{{ t('m.4eafa9e925b3') }}</option><option value="name">{{ t('m.a98585871c53') }}</option><option value="state">{{ t('m.e46b0272bd64') }}</option></select></label><button type="button" class="container-action" :disabled="dashboardSettingsSaving" @click="emit('saveSettings')">{{ t('m.b290bfe6352c') }}</button></div>
        <p v-if="orderError" class="container-task-error" role="alert">{{ display(orderError) }}</p><p v-if="!inventory" class="docker-empty">{{ t('m.8a4a01f575e0') }}</p>
        <p v-else-if="inventory.dockerAvailability === 'unavailable' || inventory.staleReason === 'docker_capability_unavailable'" class="docker-empty">{{ t('m.758a85e024fd') }}{{ dockerStatusReason(inventory) ? t('m.8504c2226f79', {'0': (dockerStatusReason(inventory))}) : '' }}</p>
        <p v-else-if="dockerStatusReason(inventory)" class="docker-empty" data-testid="docker-health-reason">{{ t('m.369cbdaf8c6d') }}{{ dockerStatusReason(inventory) }}</p>
        <p v-if="inventory && inventory.containers.length === 0" class="docker-empty">{{ dockerStale ? t('m.647742c56530') : t('m.0e149c0e416b') }}</p><p v-if="inventory && inventory.containers.length > 0 && sortedContainers.length === 0" class="docker-empty">{{ t('m.b8e005f5fbdf') }}</p>
        <div v-if="inventory && sortedContainers.length" class="docker-list" :data-group-by="dashboardSettings.groupBy">
          <article v-for="record in sortedContainers" :key="record.container.id" class="docker-row" :class="{ 'is-drop-target': dropContainerID === record.container.id, 'is-being-dragged': draggedContainerID === record.container.id }" :data-container-id="record.container.id" :data-preference-identity="preferenceIdentities[record.container.id]" :data-stale="dockerStale || record.container.stale" :data-group="containerGroupName(record)" draggable="true" @dragstart="beginNativeReorder(record, $event)" @dragover.prevent @drop="finishNativeReorder(record)" @dragend="draggedContainerID = ''">
            <div v-if="dashboardSettings.groupBy !== 'none'" class="container-group-label">{{ containerGroupName(record) }}</div>
            <div class="docker-row-title"><div class="docker-container-copy"><strong><span class="detail-container-icon" aria-hidden="true">{{ containerIcon(record) }}</span> {{ containerTitle(record) }}</strong><small><template v-if="dashboardSettings.customFields.includes('image')">{{ record.container.image || t('m.45c82e228c16') }}</template><template v-if="record.container.compose"> · {{ record.container.compose.project }}/{{ record.container.compose.service }}</template><template v-if="preferenceForContainer(record)?.notes"> · {{ preferenceForContainer(record)?.notes }}</template></small></div><div class="container-actions"><span v-if="rollbackTaskForContainer(record.container.id)" class="container-state container-rollback-state">{{ t('m.557f68233fbd') }}</span><span v-else-if="dashboardSettings.customFields.includes('state')" class="container-state" :data-running="record.container.running">{{ record.container.state || t('m.4d8c1c5b4283') }}</span><button type="button" :disabled="!record.container.running || record.container.stale || !online || dockerStale || Boolean(rollbackTaskForContainer(record.container.id))" @click="openContainerTerminal(record)">{{ t('m.d0d07d153161') }}</button></div></div>
            <div class="docker-row-meta"><span v-if="dashboardSettings.customFields.includes('health')" :data-health="record.container.health">{{ t('m.855aef6c73da') }}{{ containerHealthText(record.container) }}</span><span v-if="dashboardSettings.customFields.includes('uptime')">{{ t('m.77c1f61cd3be') }}{{ containerUptime(record) }}</span><span>{{ t('m.3bc622e323d6') }}{{ containerNetworkText(record.container) }}</span><span v-if="dockerStale || record.container.stale" class="container-stale">{{ t('m.6fe221cef061') }}<template v-if="record.container.unavailableReason">：{{ record.container.unavailableReason }}</template></span><span v-else>{{ t('m.7153f0918a93') }}{{ new Date(record.receivedAt).toLocaleTimeString(locale) }}</span></div>
            <div v-if="dashboardSettings.customFields.includes('ports') && (record.container.ports ?? []).length" class="container-ports" :aria-label="t('m.9c9980ac48fe')"><span v-for="(port, index) in (record.container.ports ?? [])" :key="`${port.containerPort}-${port.protocol}-${index}`">{{ portText(port) }}</span></div><p v-if="record.container.unavailableReason" class="container-reason">{{ record.container.unavailableReason }}</p>
            <div class="container-task-actions"><p v-if="rollbackTaskForContainer(record.container.id)" class="container-rollback-note" role="note">{{ t('m.322728a307b5') }}</p><template v-else>
              <button class="container-action drag-handle" type="button" :aria-label="t('m.ae6e894f4576')" :disabled="orderSaving || dashboardSettings.sortBy !== 'custom' || !preferenceIdentities[record.container.id]" @pointerdown="beginPointerReorder($event, record)" :title="t('m.a1eeffd3e106')">⠿</button>
              <button class="container-action" type="button" :disabled="!preferenceForEditor(record)" :title="serviceLinkTitle(record)" @click="editingPreference = editingPreference === record.container.id ? '' : record.container.id">{{ editingPreference === record.container.id ? t('m.415df73a7c07') : t('m.8299fda55003') }}</button><a v-if="safeServiceURL(record)" class="container-action service-link" :href="safeServiceURL(record)" target="_blank" rel="noopener noreferrer">{{ t('m.e4fd01edc24f') }}</a>
              <button v-if="record.container.paused" class="container-action" type="button" :disabled="!canOperate(record)" @click="submitAction(record, 'resume')">{{ t('m.e0534b8a4e46') }}</button><template v-else-if="record.container.running"><button class="container-action" type="button" :disabled="!canOperate(record)" @click="submitAction(record, 'stop')">{{ t('m.ca4d973c0b00') }}</button><button class="container-action" type="button" :disabled="!canOperate(record)" @click="submitAction(record, 'restart')">{{ t('m.f7d763a12cbf') }}</button><button class="container-action" type="button" :disabled="!canOperate(record)" @click="submitAction(record, 'pause')">{{ t('m.8d12fc0d4eb2') }}</button></template><button v-else class="container-action" type="button" :disabled="!canOperate(record)" @click="submitAction(record, 'start')">{{ t('m.56410fc65314') }}</button>
              <button class="container-action" type="button" :disabled="!canOperate(record) || Boolean(record.container.compose)" :title="record.container.compose ? t('m.fd6779f2f9fe') : t('m.629affaf8b22')" @click="submitAction(record, 'rename')">{{ t('m.0d0cbac2eee5') }}</button><button class="container-action container-action-danger" type="button" :disabled="!canOperate(record) || record.container.running || record.container.paused || record.container.restarting" :title="t('m.6176bf1e7f1a')" @click="submitAction(record, 'delete')">{{ t('m.2f9daa828907') }}</button>
              <span v-if="taskSubmitting[taskKey(record.container.id)]" class="container-task-status" role="status">{{ t('m.911ee461c36a') }}</span><span v-if="taskForContainer(record.container.id)" class="container-task-status" :data-status="taskForContainer(record.container.id)?.status" role="status">{{ taskStatusLabel(taskForContainer(record.container.id)!) }}</span><button v-if="taskForContainer(record.container.id)" class="container-action" type="button" @click="toggleTaskAudit(taskForContainer(record.container.id)!)">{{ taskAuditVisible[taskForContainer(record.container.id)!.taskId] ? t('m.54e74c1e7504') : t('m.97f395dec8ce') }}</button><span v-if="taskErrors[taskKey(record.container.id)]" class="container-task-error" role="alert">{{ display(taskErrors[taskKey(record.container.id)]) }}</span><span v-if="taskAuditErrors[taskForContainer(record.container.id)?.taskId || '']" class="container-task-error" role="alert">{{ display(taskAuditErrors[taskForContainer(record.container.id)?.taskId || '']) }}</span><ol v-if="taskForContainer(record.container.id) && taskAuditVisible[taskForContainer(record.container.id)!.taskId]" class="container-task-audit"><li v-if="taskAuditLoading[taskForContainer(record.container.id)!.taskId]">{{ t('m.cf62890bdb61') }}</li><li v-for="event in (taskAuditViews[taskForContainer(record.container.id)!.taskId] ?? [])" :key="event.id">{{ event.event }} · {{ event.fromStatus || t('m.d2bb025a2e51') }} → {{ event.toStatus || t('m.fb7e71452c04') }} · {{ new Date(event.occurredAt).toLocaleString(locale) }}</li></ol>
            </template></div>
            <PreferenceEditor v-if="editingPreference === record.container.id && preferenceForEditor(record)" :preference="preferenceForEditor(record)!" :title="record.container.compose ? t('m.a246b2532b68', {'0': (record.container.compose.project), '1': (record.container.compose.service)}) : t('m.b75c25171acc', {'0': (record.container.name || record.container.id.slice(0, 12))})" @saved="acceptPreference"/><ContainerStreams v-if="!rollbackTaskForContainer(record.container.id)" :node-id="node.nodeId" :container-id="record.container.id" :disabled="dockerStale || record.container.stale" :stats-available="record.container.running && !record.container.paused && !record.container.restarting"/>
          </article>
        </div>
      </section>
    </template>
    <ImagesPanel v-if="activeSection === 'images'" :key="node.nodeId" :node-id="node.nodeId"/><ComposeProjects v-if="activeSection === 'compose'" :key="node.nodeId" :node-id="node.nodeId"/><section v-if="activeSection === 'rebuild'" class="section-panel container-rebuild-page" :aria-label="t('m.e5634738490a')" data-testid="container-rebuild-page"><header class="section-toolbar"><div><span class="eyebrow">{{ t('label.08671c7919d1') }}</span><h2>{{ t('m.e5634738490a') }}</h2></div></header><p class="rebuild-safety-note">{{ t('m.acc2b82becc5') }}</p><p v-if="!rebuildCandidates.length" class="docker-empty">{{ t('m.59042e079f41') }}</p><template v-else><label class="rebuild-container-picker">{{ t('m.4210fc8f913a') }}<select v-model="selectedRebuildContainerID"><option v-for="record in rebuildCandidates" :key="record.container.id" :value="record.container.id">{{ containerTitle(record) }} · {{ record.container.id.slice(0, 12) }}</option></select></label><ContainerRebuildWizard v-if="selectedRebuildContainer" :node-id="node.nodeId" :container="selectedRebuildContainer.container" :stale="!canOperate(selectedRebuildContainer)" :tasks="tasks"/></template></section><NodeFiles v-if="activeSection === 'files'" :key="node.nodeId" :node-id="node.nodeId" :node-name="node.displayName"/>
    <section v-if="activeSection === 'history'" class="section-panel" :aria-label="t('m.792a0100ee53')"><div class="section-toolbar"><div><span class="eyebrow">{{ t('label.8f7889f6a879') }}</span><h2>{{ t('m.fdc402785198') }}{{ nodeTitle }}</h2></div><div class="history-controls"><label>{{ t('m.ae72b532f4b8') }}<select v-model="historyRange"><option value="6h">{{ t('m.320e9ce290e8') }}</option><option value="24h">{{ t('m.ab2c18802e55') }}</option><option value="7d">{{ t('m.2b39b90c0558') }}</option><option value="30d">{{ t('m.c5f1c28c0d5f') }}</option><option value="1y">{{ t('m.80c3d880f104') }}</option></select></label><button class="container-action" type="button" :disabled="historyLoading" @click="void loadHistory()">{{ t('m.bcd6771e08ec') }}</button></div></div><HistoricalMetrics :history="historyView" :loading="historyLoading" :error="historyError" :node-online="online"/><p class="retention-note">{{ t('m.b3b9fb911d3a') }}</p></section>
    <section v-if="activeSection === 'events'" class="section-panel" :aria-label="t('m.5661bd931598')" data-testid="node-event-history"><header class="section-toolbar"><div><span class="eyebrow">RECENT OPERATIONS</span><h2>{{ t('m.7d2e3cfc51fb') }}{{ nodeTitle }}</h2></div><button class="container-action" type="button" @click="emit('refreshTasks', node.nodeId)">{{ t('m.aee887434131') }}</button></header><p class="event-source-note">{{ t('m.30a9a37d5d32') }}</p><p v-if="selectedTasks.length === 0" class="docker-empty">{{ t('m.a9d063f3f3a9') }}</p><article v-for="task in selectedTasks" :key="task.taskId" class="event-card" :data-status="task.status"><div><strong>{{ task.action }} · {{ task.targetId.slice(0, 12) }}</strong><span class="container-task-status" :data-status="task.status">{{ taskStatusText(task) }}</span><time :datetime="task.createdAt">{{ new Date(task.createdAt).toLocaleString(locale) }}</time></div><button class="container-action" type="button" @click="toggleTaskAudit(task)">{{ taskAuditVisible[task.taskId] ? t('m.54e74c1e7504') : t('m.97f395dec8ce') }}</button><p v-if="taskAuditErrors[task.taskId]" class="container-task-error" role="alert">{{ display(taskAuditErrors[task.taskId]) }}</p><ol v-if="taskAuditVisible[task.taskId]" class="container-task-audit"><li v-if="taskAuditLoading[task.taskId]">{{ t('m.cf62890bdb61') }}</li><li v-for="event in (taskAuditViews[task.taskId] ?? [])" :key="event.id">{{ event.event }} · {{ event.fromStatus || t('m.d2bb025a2e51') }} → {{ event.toStatus || t('m.fb7e71452c04') }} · {{ new Date(event.occurredAt).toLocaleString(locale) }}</li></ol></article></section>
    <section v-if="activeSection === 'settings'" class="section-panel node-settings-panel" :aria-label="t('m.0a09fda5e2a6')" data-testid="node-settings"><header class="section-toolbar"><div><span class="eyebrow">{{ t('label.26ae0bd221f2') }}</span><h2>{{ t('m.1b8b4303f8ba') }}{{ nodeTitle }}</h2></div></header><div class="node-settings-status"><div><span>{{ t('m.f5514ad14eaa') }}</span><strong>{{ pending ? t('m.a890b3be09aa') : online ? t('m.b9086662b1df') : t('m.be1b4f3c6c1c') }}</strong></div><div><span>Docker Engine</span><strong>{{ inventory ? dockerAvailabilityText(inventory) : t('m.4d8c1c5b4283') }}</strong></div><div><span>{{ t('m.e31132b9de5a') }}</span><strong>{{ node.agentVersion || t('m.4d8c1c5b4283') }}</strong></div><div><span>{{ t('m.bf04f29ea311') }}</span><strong>{{ node.nodeId }}</strong></div></div><PreferenceEditor :preference="nodePreferenceForEditor()" :title="t('m.b8d1ded2a785', {'0': (node.displayName)})" @saved="acceptPreference"/><div class="dashboard-settings-editor"><h3>{{ t('m.f917a66dfc4f') }}</h3><div class="dashboard-settings-grid"><label>{{ t('m.b91569c6e33a') }}<select v-model="viewMode"><option value="monitor">{{ t('m.fdc390061698') }}</option><option value="manage">{{ t('m.bb6d995724f4') }}</option></select></label><label>{{ t('m.65cd544c6486') }}<select v-model="nodeGroupBy"><option value="status">{{ t('m.b8f945ea49ff') }}</option><option value="group">{{ t('m.c08b046b5470') }}</option><option value="none">{{ t('m.fcd40ee3f05a') }}</option></select></label><label>{{ t('m.dd74dd92d820') }}<select v-model="nodeSortBy"><option value="custom">{{ t('m.0f59d3742e06') }}</option><option value="name">{{ t('m.a98585871c53') }}</option><option value="status">{{ t('m.b8f945ea49ff') }}</option></select></label><label>{{ t('m.f143cf3535b6') }}<select v-model="groupBy"><option value="node">{{ t('m.2410d860f093') }}</option><option value="compose">{{ t('m.5f67dd75b01f') }}</option><option value="state">{{ t('m.e46b0272bd64') }}</option><option value="none">{{ t('m.fcd40ee3f05a') }}</option></select></label><label>{{ t('m.30d908c60571') }}<select v-model="sortBy"><option value="custom">{{ t('m.0f59d3742e06') }}</option><option value="name">{{ t('m.a98585871c53') }}</option><option value="state">{{ t('m.e46b0272bd64') }}</option></select></label><label>{{ t('m.70006f6c5230') }}<input v-model.number="featuredLimit" type="number" min="1" max="20"></label></div><fieldset class="custom-fields"><legend>{{ t('m.25ebfb95e8d6') }}</legend><label v-for="field in customFieldOptions" :key="field.id"><input v-model="customFields" type="checkbox" :value="field.id">{{ field.label }}</label></fieldset><div class="settings-save"><button class="container-action" type="button" :disabled="dashboardSettingsSaving" @click="emit('saveSettings')">{{ dashboardSettingsSaving ? t('m.ff509c9ba052') : t('m.1d6a782c4ccc') }}</button><span v-if="dashboardSettingsMessage" role="status">{{ display(dashboardSettingsMessage) }}</span><span v-if="dashboardSettingsError" role="alert">{{ display(dashboardSettingsError) }}</span></div></div><p class="preference-separation-note">{{ t('m.02f03b13557b') }}</p></section>
    <NodeServiceProbes :key="node.nodeId" :node-id="node.nodeId" :node-name="node.displayName" :node-online="online"/><TerminalConsole v-if="activeTerminal" v-bind="activeTerminal" @close="activeTerminal = null"/>
  </section>
</template>

<style scoped>
.node-detail-page { width: min(1480px,100%); min-width: 0; margin: 0 auto; padding: clamp(20px,4vw,44px); color: #eaf0fa; }
.detail-heading { display:flex; align-items:center; justify-content:space-between; gap:20px; margin-bottom:20px; }
.detail-heading h1 { margin:6px 0 4px; font-size:clamp(24px,3vw,34px); overflow-wrap:anywhere; }
.detail-heading p { margin:0; color:#91a2ba; font:10px/1.5 ui-monospace,monospace; overflow-wrap:anywhere; }
.detail-heading .node-detail-note { margin-top:7px; color:#bdcbe0; font-family:inherit; font-size:11px; line-height:1.55; white-space:pre-wrap; }
.back-button { margin-bottom:12px; }
.detail-heading-status { display:flex; flex-wrap:wrap; justify-content:flex-end; gap:8px; }
.container-create-results { display:grid; gap:6px; margin:8px 0 12px; }.container-create-results h3 { margin:0; color:#a8bdd7; font-size:10px; }.container-create-results article { display:flex; flex-wrap:wrap; align-items:center; justify-content:space-between; gap:8px; border:1px solid rgba(171,196,232,.1); border-radius:7px; padding:8px 10px; background:rgba(18,29,45,.58); }.container-create-results article>div { display:flex; flex-wrap:wrap; align-items:center; gap:9px; }.container-create-results strong,.container-create-results code,.container-create-results time { font-size:9px; overflow-wrap:anywhere; }.container-create-results time { color:#8193aa; }
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
.rebuild-safety-note { margin:0 0 12px; border-left:2px solid #efb77b; padding-left:10px; color:#efc58d; font-size:11px; line-height:1.6; }.rebuild-container-picker { display:grid; max-width:680px; gap:6px; margin:10px 0; color:#9aabc1; font-size:10px; }.rebuild-container-picker select { min-height:38px; min-width:0; border:1px solid rgba(171,196,232,.16); border-radius:6px; padding:7px 9px; color:#eaf0fa; background:#111b29; font:inherit; }
@media(max-width:900px) { .node-settings-status { grid-template-columns:repeat(2,minmax(0,1fr)); }.dashboard-settings-grid { grid-template-columns:repeat(2,minmax(0,1fr)); } }
@media(max-width:560px) { .node-detail-page { padding:20px 14px; }.detail-heading { align-items:flex-start; flex-direction:column; }.detail-heading-status { justify-content:flex-start; }.section-toolbar,.docker-heading { align-items:flex-start; flex-direction:column; }.history-controls { width:100%; }.history-controls label,.history-controls select { width:100%; }.docker-view-controls,.docker-view-controls label,.docker-view-controls input,.docker-view-controls select,.docker-view-controls button { width:100%; }.node-settings-status,.dashboard-settings-grid { grid-template-columns:minmax(0,1fr); }.docker-row-title { align-items:flex-start; flex-wrap:wrap; }.container-actions { width:100%; }.container-actions button { min-height:42px; flex:1; }.node-terminal-entry { align-items:flex-start; flex-direction:column; }.node-terminal-entry button { width:100%; min-height:42px; } }
</style>
