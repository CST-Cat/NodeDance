<script setup lang="ts">
import { requestPrompt, requestConfirm } from '../dialogs'
import { nodeNavigationKey, useViewTab } from '../navigation'
import { t, display, locale } from "../i18n"
import { computed, defineAsyncComponent, inject, onBeforeUnmount, ref, watch, watchEffect } from 'vue'
import { api, type AgentNode, type ContainerCreateSpec, type ContainerTask, type ContainerTaskAction, type CreateContainerTaskPayload, type DashboardPreference, type DashboardSettings, type DockerContainerRecord, type DockerInventory, type MetricHistory, type ServiceProbe, type TaskAuditEvent } from '../api'
import type { MetricsView } from '../metrics-contract'
import ContainerRebuildWizard from './ContainerRebuildWizard.vue'
import ContainerStreams from './ContainerStreams.vue'
const HistoricalMetrics = defineAsyncComponent(() => import('./HistoricalMetrics.vue'))
import ImagesPanel from './ImagesPanel.vue'
import ComposeProjects from './ComposeProjects.vue'
import MetricsPanel from './MetricsPanel.vue'
import PreferenceEditor from './PreferenceEditor.vue'
const TerminalConsole = defineAsyncComponent(() => import('./TerminalConsole.vue'))
import NodeServiceProbes from './NodeServiceProbes.vue'
const NodeFiles = defineAsyncComponent(() => import('./NodeFiles.vue'))
import AgentEnrollment from './AgentEnrollment.vue'
import NodeAssetSettings from './NodeAssetSettings.vue'
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
}>()
const emit = defineEmits<{
  deleted: []
  refreshTasks: [nodeId: string]
  preferenceSaved: [preference: DashboardPreference]
  containerAction: [payload: { nodeId: string; containerId: string; task: CreateContainerTaskPayload }]
  containerCreate: [payload: { nodeId: string; spec: ContainerCreateSpec }]
  settingsChange: [settings: DashboardSettings]
  saveSettings: []
}>()

const deletingNode = ref(false)
const nodeActionError = ref('')
const enrollmentOpen = ref(false)
function toggleEnrollment() {
 if(activeSection.value === 'settings') { selectSection('overview'); enrollmentOpen.value = true }
 else enrollmentOpen.value = !enrollmentOpen.value
}
async function deleteNode() {
 if (deletingNode.value) return
 deletingNode.value = true; nodeActionError.value = ''
 try {
  if (!await requestConfirm(t('node.deleteConfirm', {'0': props.node.displayName}))) return
  await api.deleteNode(props.node.nodeId); emit('deleted')
 }
 catch (reason) { nodeActionError.value = reason instanceof Error ? reason.message : String(reason) }
 finally { deletingNode.value = false }
}
watch(() => props.pending, pending => { if (!pending) enrollmentOpen.value = false })

const sectionTabs = computed(() => [
  { id: 'overview', icon: 'host', label: t('m.a33db5730556') }, { id: 'probes', icon: 'discovery', label: t('m.3fefdc07fa14') }, { id: 'docker', icon: 'nodes', label: t('m.eee901e4cc16') }, { id: 'images', icon: 'file', label: t('m.176c09844e0f') },
  { id: 'compose', icon: 'operations', label: t('m.5f67dd75b01f') }, { id: 'rebuild', icon: 'refresh', label: t('m.474490ab5dc3') }, { id: 'files', icon: 'folder', label: t('m.b3bd5ac7cc4d') },
  { id: 'history', icon: 'history', label: t('m.bfb41404e88b') }, { id: 'events', icon: 'alerts', label: t('m.a700a47380fe') }, { id: 'settings', icon: 'settings', label: t('m.0a09fda5e2a6') },
] as const)
const activeSection = useViewTab('section', 'overview', () => sectionTabs.value.map(tab => tab.id))
const sidebarNavigation = inject(nodeNavigationKey)
const sidebarOwner = Symbol('node-detail')
onBeforeUnmount(() => {
  if (sidebarNavigation?.value?.owner === sidebarOwner) sidebarNavigation.value = null
})
const overviewProbes = ref<ServiceProbe[]>([])
const probeSummaryError = ref('')
const probeSummaryLoadedAt = ref<Date>()
let probeSummaryEpoch = 0
const probeCounts = computed(() => {
  const enabled = overviewProbes.value.filter(probe => probe.enabled)
  const healthy = props.online ? enabled.filter(probe => probe.status === 'healthy').length : 0
  const unhealthy = props.online ? enabled.filter(probe => probe.status === 'unhealthy').length : 0
  return { total: overviewProbes.value.length, healthy, unhealthy, unknown: enabled.length - healthy - unhealthy, disabled: overviewProbes.value.length - enabled.length }
})
watch([() => props.node.nodeId, activeSection], async ([nodeId, section]) => {
  const epoch = ++probeSummaryEpoch
  overviewProbes.value = []; probeSummaryError.value = ''; probeSummaryLoadedAt.value = undefined
  if (section !== 'overview') return
  try {
    const response = await api.serviceProbes()
    if (epoch === probeSummaryEpoch) { overviewProbes.value = response.probes.filter(probe => probe.nodeId === nodeId); probeSummaryLoadedAt.value = new Date() }
  } catch (reason) {
    if (epoch === probeSummaryEpoch) probeSummaryError.value = reason instanceof Error ? reason.message : t('m.ebf63ef228d2')
  }
}, { immediate: true })
const customFieldOptions = computed(() => [
  { id: 'state', label: t('m.e46b0272bd64') }, { id: 'ports', label: t('m.e71ac32b544b') }, { id: 'health', label: t('m.85788192fbeb') },
  { id: 'image', label: t('m.176c09844e0f') }, { id: 'uptime', label: t('m.5986e9d976e0') },
])
const containerSearch = ref('')
const containerState = ref('all')
const pageSize = ref(20)
const containerPage = ref(1)
const selectedContainerIDs = ref<string[]>([])
const selectionBusy = ref(false)
const detailPanel = ref<'info' | 'streams' | 'preferences' | 'audit' | ''>('')
const primaryActions = computed(() => [
  {action: 'start' as const, label: t('m.56410fc65314')}, {action: 'stop' as const, label: t('m.ca4d973c0b00')},
  {action: 'restart' as const, label: t('m.562822892865')}, {action: 'rename' as const, label: t('m.0d0cbac2eee5')}, {action: 'delete' as const, label: t('m.2f9daa828907')},
])
const historyView = ref<MetricHistory | null>(null)
const historyLoading = ref(false)
const historyError = ref('')
const historyRange = ref<'6h' | '24h' | '7d' | '30d' | '1y'>('24h')
const taskAuditViews = ref<Record<string, TaskAuditEvent[]>>({})
const taskAuditVisible = ref<Record<string, boolean>>({})
const taskAuditLoading = ref<Record<string, boolean>>({})
const taskAuditErrors = ref<Record<string, string>>({})
const activeTerminal = ref<{ nodeId: string; targetKind: 'host' | 'container'; containerId?: string; targetLabel: string } | null>(null)
const historyResolution = computed(() => historyRange.value === '1y' ? 'hour' : 'minute')
const nodePreference = computed(() => props.preferences.find((item) => item.targetKind === 'node' && item.identity === `node:${props.node.nodeId}`))
const nodeTitle = computed(() => nodePreference.value?.alias || props.node.displayName)
watchEffect(() => {
  if (sidebarNavigation) sidebarNavigation.value = { owner: sidebarOwner, nodeId: props.node.nodeId, title: nodeTitle.value, activeSection: activeSection.value, sections: sectionTabs.value }
})
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
function containerGroupName(record: DockerContainerRecord): string {
  if (groupBy.value === 'compose') return record.container.compose?.project || ''
  if (groupBy.value === 'state') return record.container.state || ''
  return ''
}
const orderedContainers = computed(() => [...(props.inventory?.containers ?? [])].sort((left, right) => {
  const group = containerGroupName(left).localeCompare(containerGroupName(right), locale.value)
  if (group) return group
  if (sortBy.value === 'state') {
    const state = (left.container.state || '').localeCompare(right.container.state || '', locale.value)
    if (state) return state
  }
  if (sortBy.value === 'custom') {
    const lp = preferenceForContainer(left), rp = preferenceForContainer(right)
    if (lp?.pinned !== rp?.pinned) return lp?.pinned ? -1 : 1
    const order = (lp?.sortOrder ?? 0) - (rp?.sortOrder ?? 0)
    if (order) return order
  }
  return containerTitle(left).localeCompare(containerTitle(right), locale.value)
}))
const sortedContainers = computed(() => {
  const query = containerSearch.value.trim().toLocaleLowerCase()
  return orderedContainers.value.filter((record) => {
    const c = record.container
    if (containerState.value === 'running' && (!c.running || c.paused || c.restarting)) return false
    if (containerState.value === 'paused' && !c.paused) return false
    if (containerState.value === 'stopped' && (c.running || c.paused || c.restarting)) return false
    if (containerState.value === 'restarting' && !c.restarting) return false
    if (!query) return true
    const preference = preferenceForContainer(record)
    return [containerTitle(record), record.container.name, record.container.image, record.container.id,
      record.container.compose?.project, record.container.compose?.service, preference?.notes]
      .some((value) => value?.toLocaleLowerCase().includes(query))
  })
})
const pageCount = computed(() => Math.max(1, Math.ceil(sortedContainers.value.length / pageSize.value)))
const pageContainers = computed(() => sortedContainers.value.slice((containerPage.value - 1) * pageSize.value, containerPage.value * pageSize.value))
const selectedContainers = computed(() => orderedContainers.value.filter(record => selectedContainerIDs.value.includes(record.container.id)))
const singleContainer = computed(() => selectedContainers.value.length === 1 ? selectedContainers.value[0] : undefined)
const allPageSelected = computed(() => pageContainers.value.length > 0 && pageContainers.value.every(record => selectedContainerIDs.value.includes(record.container.id)))
const somePageSelected = computed(() => !allPageSelected.value && pageContainers.value.some(record => selectedContainerIDs.value.includes(record.container.id)))
function toggleContainer(id: string, selected: boolean) {
  selectedContainerIDs.value = selected ? [...new Set([...selectedContainerIDs.value, id])] : selectedContainerIDs.value.filter(value => value !== id)
}
function togglePage(selected: boolean) {
  const ids = new Set(pageContainers.value.map(record => record.container.id))
  selectedContainerIDs.value = selected ? [...new Set([...selectedContainerIDs.value, ...ids])] : selectedContainerIDs.value.filter(id => !ids.has(id))
}
function inspectContainer(record: DockerContainerRecord) {
  selectedContainerIDs.value = [record.container.id]
  detailPanel.value = 'info'
}
function closeMoreMenu(event: MouseEvent | KeyboardEvent) {
  if (event.type !== 'keydown' && !(event.target as HTMLElement).closest('button:not(:disabled), a')) return
  const menu = (event.currentTarget as HTMLElement).closest('details')
  menu?.removeAttribute('open')
  menu?.querySelector('summary')?.focus()
}
function showPanel(panel: typeof detailPanel.value) {
  if (!singleContainer.value) return
  detailPanel.value = panel
  if (panel === 'audit') {
    const task = taskForContainer(singleContainer.value.container.id)
    if (task && !taskAuditVisible.value[task.taskId]) void toggleTaskAudit(task)
  }
}
watch([containerSearch, containerState, pageSize], () => { containerPage.value = 1 })
watch(sortedContainers, records => {
  // Hidden or removed rows cannot remain targets of the toolbar.
  const ids = new Set(records.map(record => record.container.id))
  const remaining = selectedContainerIDs.value.filter(id => ids.has(id))
  if (remaining.length !== selectedContainerIDs.value.length) selectedContainerIDs.value = remaining
  containerPage.value = Math.min(containerPage.value, pageCount.value)
})
watch(selectedContainerIDs, () => { detailPanel.value = '' }, {flush: 'sync'})
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
function actionReady(record: DockerContainerRecord, action: ContainerTaskAction): boolean {
  if (!canOperate(record) || rollbackTaskForContainer(record.container.id)) return false
  const c = record.container
  if (action === 'rename') return !c.compose
  if (action === 'delete' || action === 'start') return !c.running && !c.paused && !c.restarting
  if (action === 'resume') return c.paused
  if (action === 'stop') return c.running && !c.paused && !c.restarting
  return c.running && !c.paused && !c.restarting
}
function selectionCan(action: ContainerTaskAction): boolean {
  return !selectionBusy.value && selectedContainers.value.length > 0 && (action !== 'rename' || selectedContainers.value.length === 1) && selectedContainers.value.every(record => actionReady(record, action))
}
async function submitSelection(action: ContainerTaskAction) {
  if (!selectionCan(action)) return
  const ids = selectedContainers.value.map(record => record.container.id)
  selectionBusy.value = true
  try {
    let newName: string | undefined
    if (action === 'rename') {
      const value = await requestPrompt(t('m.19efbadaa133'), selectedContainers.value[0].container.name)
      if (value === null) return
      newName = value.trim()
    }
    if (action === 'delete') {
      const value = await requestPrompt(ids.length === 1 ? t('m.cd8d2b02a176', {'0': ids[0]}) : t('docker.confirmDelete', {'0': ids.length, '1': ids.join(', ')}))
      if (value === null) return
      const confirmed = value.split(/[\s,]+/).filter(Boolean)
      if (confirmed.length !== ids.length || new Set(confirmed).size !== ids.length || !ids.every(id => confirmed.includes(id))) return
    }
    // Refresh may change state while a confirmation dialog is open.
    const records = ids.map(id => props.inventory?.containers.find(record => record.container.id === id))
    if (!records.every((record): record is DockerContainerRecord => Boolean(record && actionReady(record, action)))) return
    for (const record of records) {
      emit('containerAction', { nodeId: props.node.nodeId, containerId: record.container.id,
        task: { action, ...(newName !== undefined ? {newName} : {}), ...(action === 'delete' ? {deleteConfirmed: true, deleteConfirmationId: record.container.id} : {}) } })
    }
  } finally { selectionBusy.value = false }
}
function openSelectedRebuild() {
  if (!singleContainer.value) return
  selectedRebuildContainerID.value = singleContainer.value.container.id
  activeSection.value = 'rebuild'
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

}
watch(activeSection, section => {
  activeTerminal.value = null
  selectedContainerIDs.value = []
  if (section === 'history') void loadHistory()
  if (section === 'events') emit('refreshTasks', props.node.nodeId)
}, { immediate: true })
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
function acceptPreference(value: DashboardPreference) { emit('preferenceSaved', value) }
watch(rebuildCandidates, (records) => {
  if (!records.some((record) => record.container.id === selectedRebuildContainerID.value)) {
    selectedRebuildContainerID.value = records[0]?.container.id ?? ''
  }
}, { immediate: true })
onBeforeUnmount(() => {
  probeSummaryEpoch++
})
</script>

<template>
  <section class="node-detail-page" aria-labelledby="detail-title" data-testid="node-detail">
    <header class="detail-heading">
      <div><div class="detail-heading-navigation"><span class="eyebrow">{{ t('label.164ba94c1c7b') }}</span></div><h1 id="detail-title">{{ nodeTitle }}</h1><p v-if="nodeTitle !== node.displayName || node.agentVersion"><template v-if="nodeTitle !== node.displayName">{{ node.displayName }}<template v-if="node.agentVersion"> · </template></template>{{ node.agentVersion }}</p><p v-if="nodePreference?.notes" class="node-detail-note">{{ nodePreference.notes }}</p></div>
      <div class="detail-heading-status"><span class="node-detail-status" :data-online="online">{{ pending ? t('m.a890b3be09aa') : online ? t('m.b9086662b1df') : t('m.be1b4f3c6c1c') }}</span><span class="docker-state" :data-available="inventory?.dockerAvailability || 'unknown'" :data-stale="dockerStale">{{ inventory ? dockerAvailabilityText(inventory) : online ? t('m.c12a50c262ca') : t('m.8a6501e8198f') }}</span></div>
    </header>
    <div v-if="pending" class="node-management-actions" data-testid="node-management-actions">
      <button v-if="pending" class="secondary-button" type="button" :aria-expanded="enrollmentOpen && activeSection !== 'settings'" data-testid="node-install-command" @click="toggleEnrollment">{{ t('node.installCommand') }}</button>
    </div>
    <AgentEnrollment v-if="pending && enrollmentOpen && activeSection !== 'settings'" class="node-enrollment-panel" compact :node-id="node.nodeId" />
    <div v-if="activeSection === 'overview' && probeCounts.total" class="probe-overview-summary" data-testid="probe-overview-summary">
      <div><p>{{ t('node.probes.summary', {'0': probeCounts.total, '1': probeCounts.healthy, '2': probeCounts.unhealthy, '3': probeCounts.unknown, '4': probeCounts.disabled}) }}</p><small v-if="probeSummaryLoadedAt">{{ t('node.probes.loaded', {'0': probeSummaryLoadedAt.toLocaleString(locale)}) }}</small></div>
      <button type="button" class="secondary-button" @click="selectSection('probes')">{{ t('node.probes.open') }}</button>
    </div>
    <p v-if="activeSection === 'overview' && probeSummaryError" role="alert" class="probe-summary-error">{{ display(probeSummaryError) }}</p>
    <MetricsPanel v-if="activeSection === 'overview' && metrics" :key="metrics.nodeId" :view="metrics" />
    <div v-else-if="activeSection === 'overview'" class="metrics-waiting" data-testid="metrics-waiting"><span class="eyebrow">{{ node.displayName }}</span><h2>{{ pending ? t('m.270c0a7750e1') : online ? t('m.78a4908b8949') : t('m.f339ad3bc760') }}</h2><p v-if="pending">{{ t('m.31483a081b8c') }}</p><p v-else-if="online">{{ t('m.ad2b360a6731') }}</p><p v-else>{{ t('m.fcca199d47b9') }}{{ nodeReason ? t('m.fee0ec31e108', {'0': (nodeReason)}) : '' }}</p><span class="node-detail-status" :data-online="online">{{ pending ? t('m.a890b3be09aa') : online ? t('m.b9086662b1df') : t('m.be1b4f3c6c1c') }}</span></div>

    <template v-if="activeSection === 'docker'">
      <div class="node-terminal-entry"><div><strong>{{ t('m.725457c84556') }}</strong><small>{{ t('m.a7c1e29fcce5') }}</small></div><button type="button" :disabled="!online" @click="openHostTerminal">{{ t('m.b89a0e4584f2') }}</button></div>
      <section class="docker-panel" aria-labelledby="docker-title" data-testid="docker-inventory">
        <header class="docker-heading"><div><span class="eyebrow">{{ t('label.c039e720422e') }}</span><h2 id="docker-title">{{ t('m.7cabcc483eb4') }}</h2></div><span v-if="inventory" class="docker-state" :data-available="inventory.dockerAvailability" :data-stale="dockerStale">{{ dockerAvailabilityText(inventory) }}</span></header>
        <ContainerCreateForm :disabled="!canCreateContainers" :submitting="createSubmitting" :error="createError" @submit="submitContainerCreate" />
        <section v-if="createTasks.length" class="container-create-results" :aria-label="t('m.7084b942d42e')"><h3>{{ t('m.964c43bb9238') }}</h3><article v-for="task in createTasks" :key="task.taskId"><div><strong>{{ taskStatusLabel(task) }}</strong><time :datetime="task.createdAt">{{ new Date(task.createdAt).toLocaleString(locale) }}</time></div><code v-if="task.status === 'succeeded' && task.result.resourceRevision">Container ID：{{ task.result.resourceRevision }}</code><span v-else-if="task.status === 'failed'" class="container-task-error">{{ task.result.code || 'creation_failed' }}</span></article></section>
        <div class="docker-view-controls">
          <label>{{ t('m.44ce7ae909bb') }}<input v-model="containerSearch" type="search" :placeholder="t('m.5a5d5c8b732a')" :aria-label="t('m.8db3ff343ebc')"></label>
          <label>{{ t('docker.state') }}<select v-model="containerState"><option value="all">{{ t('docker.allStates') }}</option><option value="running">{{ t('docker.running') }}</option><option value="stopped">{{ t('docker.stopped') }}</option><option value="paused">{{ t('docker.paused') }}</option><option value="restarting">{{ t('docker.restarting') }}</option></select></label>
          <label>{{ t('m.a96c9a854190') }}<select v-model="sortBy"><option value="custom">{{ t('m.4eafa9e925b3') }}</option><option value="name">{{ t('m.a98585871c53') }}</option><option value="state">{{ t('m.e46b0272bd64') }}</option></select></label>
        </div>
        <div class="docker-selection-bar">
          <label><input type="checkbox" :checked="allPageSelected" :indeterminate="somePageSelected" :disabled="!pageContainers.length || selectionBusy" @change="togglePage(($event.target as HTMLInputElement).checked)">{{ t('docker.selectPage') }}</label>
          <span role="status">{{ t('docker.selected', {'0': selectedContainers.length, '1': sortedContainers.length}) }}</span>
          <button class="container-action" type="button" :disabled="!selectedContainers.length || selectionBusy" @click="selectedContainerIDs = []">{{ t('docker.clearSelection') }}</button>
        </div>
        <div class="docker-toolbar" role="group" :aria-label="t('docker.toolbar')" data-testid="docker-toolbar">
          <button v-for="{action, label} in primaryActions" :key="action" class="container-action" :class="{'container-action-danger': action === 'delete'}" type="button" :data-action="action" :disabled="!selectionCan(action)" :title="action === 'rename' ? t('docker.singleOnly') : t('docker.allApplicable')" @click="void submitSelection(action)">{{ label }}</button>
          <details class="docker-more" @keydown.esc.prevent="closeMoreMenu">
            <summary>{{ t('docker.more') }}</summary>
            <div class="docker-more-menu" @click="closeMoreMenu">
              <button class="container-action" type="button" :disabled="!selectionCan('pause')" @click="void submitSelection('pause')">{{ t('m.8d12fc0d4eb2') }}</button>
              <button class="container-action" type="button" :disabled="!selectionCan('resume')" @click="void submitSelection('resume')">{{ t('m.e0534b8a4e46') }}</button>
              <button class="container-action" type="button" :disabled="!singleContainer || !selectionCan('restart')" @click="singleContainer && openContainerTerminal(singleContainer)">{{ t('m.d0d07d153161') }}</button>
              <button class="container-action" type="button" :disabled="!singleContainer" @click="showPanel('info')">{{ t('docker.details') }}</button>
              <button class="container-action" type="button" :disabled="!singleContainer || Boolean(rollbackTaskForContainer(singleContainer.container.id))" @click="showPanel('streams')">{{ t('docker.streams') }}</button>
              <button class="container-action" type="button" :disabled="!singleContainer || !preferenceForEditor(singleContainer) || Boolean(rollbackTaskForContainer(singleContainer.container.id))" @click="showPanel('preferences')">{{ t('m.8299fda55003') }}</button>
              <button class="container-action" type="button" :disabled="!singleContainer || !taskForContainer(singleContainer.container.id)" @click="showPanel('audit')">{{ t('m.97f395dec8ce') }}</button>
              <button class="container-action" type="button" :disabled="!singleContainer || !canOperate(singleContainer) || Boolean(singleContainer.container.compose) || Boolean(rollbackTaskForContainer(singleContainer.container.id))" @click="openSelectedRebuild">{{ t('docker.rebuild') }}</button>
              <a v-if="singleContainer && safeServiceURL(singleContainer)" class="container-action service-link" :href="safeServiceURL(singleContainer)" target="_blank" rel="noopener noreferrer">{{ t('m.e4fd01edc24f') }}</a>
            </div>
          </details>
        </div>
        <p class="docker-selection-help">{{ t('docker.selectionHelp') }}</p>
        <section v-if="singleContainer && detailPanel" class="container-detail" data-testid="container-detail">
          <header><h3>{{ containerTitle(singleContainer) }}</h3><button class="container-action" type="button" @click="detailPanel = ''">{{ t('docker.closeDetails') }}</button></header>
          <template v-if="detailPanel === 'info'"><dl><dt>ID</dt><dd><code>{{ singleContainer.container.id }}</code></dd><dt>{{ t('docker.image') }}</dt><dd>{{ singleContainer.container.image }}</dd><dt>{{ t('docker.network') }}</dt><dd>{{ containerNetworkText(singleContainer.container) }}</dd><dt>{{ t('docker.observed') }}</dt><dd>{{ new Date(singleContainer.receivedAt).toLocaleString(locale) }}</dd></dl><p v-if="preferenceForContainer(singleContainer)?.notes">{{ preferenceForContainer(singleContainer)?.notes }}</p><p v-if="singleContainer.container.unavailableReason" class="container-task-error">{{ singleContainer.container.unavailableReason }}</p><p v-if="rollbackTaskForContainer(singleContainer.container.id)" class="container-rollback-note">{{ t('m.322728a307b5') }}</p></template>
          <ContainerStreams v-if="detailPanel === 'streams'" :key="singleContainer.container.id" :node-id="node.nodeId" :container-id="singleContainer.container.id" :disabled="!online || dockerStale || singleContainer.container.stale" :stats-available="singleContainer.container.running && !singleContainer.container.paused && !singleContainer.container.restarting"/>
          <PreferenceEditor v-if="detailPanel === 'preferences' && preferenceForEditor(singleContainer)" :key="singleContainer.container.id" :preference="preferenceForEditor(singleContainer)!" :title="containerTitle(singleContainer)" @saved="acceptPreference"/>
          <template v-if="detailPanel === 'audit' && taskForContainer(singleContainer.container.id)"><p v-if="taskAuditErrors[taskForContainer(singleContainer.container.id)!.taskId]" class="container-task-error" role="alert">{{ display(taskAuditErrors[taskForContainer(singleContainer.container.id)!.taskId]) }}</p><ol class="container-task-audit"><li v-if="taskAuditLoading[taskForContainer(singleContainer.container.id)!.taskId]">{{ t('m.cf62890bdb61') }}</li><li v-for="event in (taskAuditViews[taskForContainer(singleContainer.container.id)!.taskId] ?? [])" :key="event.id">{{ event.event }} · {{ event.fromStatus || t('m.d2bb025a2e51') }} → {{ event.toStatus || t('m.fb7e71452c04') }} · {{ new Date(event.occurredAt).toLocaleString(locale) }}</li></ol></template>
        </section>
        <p v-if="!inventory" class="docker-empty">{{ t('m.8a4a01f575e0') }}</p>
        <p v-else-if="inventory.dockerAvailability === 'unavailable' || inventory.staleReason === 'docker_capability_unavailable'" class="docker-empty">{{ t('m.758a85e024fd') }}{{ dockerStatusReason(inventory) ? t('m.8504c2226f79', {'0': dockerStatusReason(inventory)}) : '' }}</p>
        <p v-else-if="dockerStatusReason(inventory)" class="docker-empty" data-testid="docker-health-reason">{{ t('m.369cbdaf8c6d') }}{{ dockerStatusReason(inventory) }}</p>
        <p v-if="inventory && !inventory.containers.length" class="docker-empty">{{ dockerStale ? t('m.647742c56530') : t('m.0e149c0e416b') }}</p>
        <p v-else-if="inventory && !sortedContainers.length" class="docker-empty">{{ t('m.b8e005f5fbdf') }}</p>
        <div v-if="pageContainers.length" class="docker-table-scroll">
          <table class="container-table" data-testid="docker-table">
            <caption class="visually-hidden">{{ t('docker.list') }}</caption>
            <thead><tr><th scope="col"><span class="visually-hidden">{{ t('docker.selection') }}</span></th><th scope="col">{{ t('docker.name') }}</th><th v-if="dashboardSettings.customFields.includes('image')" scope="col">{{ t('docker.image') }}</th><th v-if="dashboardSettings.customFields.includes('state') || dashboardSettings.customFields.includes('health')" scope="col">{{ t('docker.state') }}</th><th v-if="dashboardSettings.customFields.includes('ports')" scope="col">{{ t('docker.ports') }}</th><th v-if="dashboardSettings.customFields.includes('uptime')" scope="col">{{ t('docker.uptime') }}</th><th scope="col">{{ t('docker.task') }}</th></tr></thead>
            <tbody><tr v-for="record in pageContainers" :key="record.container.id" class="docker-row" :data-container-id="record.container.id" :data-selected="selectedContainerIDs.includes(record.container.id)" :data-stale="dockerStale || record.container.stale">
              <td class="container-select"><input type="checkbox" :checked="selectedContainerIDs.includes(record.container.id)" :disabled="selectionBusy" :aria-label="t('docker.selectContainer', {'0': containerTitle(record)})" @change="toggleContainer(record.container.id, ($event.target as HTMLInputElement).checked)"></td>
              <td :data-label="t('docker.name')"><button class="container-name" type="button" @click="inspectContainer(record)">{{ containerTitle(record) }}</button><small>{{ record.container.id.slice(0, 12) }}<template v-if="containerTitle(record) !== record.container.name"> · {{ record.container.name }}</template></small><small v-if="record.container.compose">{{ record.container.compose.project }}/{{ record.container.compose.service }}</small></td>
              <td v-if="dashboardSettings.customFields.includes('image')" :data-label="t('docker.image')"><code>{{ record.container.image || t('m.45c82e228c16') }}</code></td>
              <td v-if="dashboardSettings.customFields.includes('state') || dashboardSettings.customFields.includes('health')" :data-label="t('docker.state')"><span v-if="rollbackTaskForContainer(record.container.id)" class="container-state container-rollback-state">{{ t('m.557f68233fbd') }}</span><span v-else-if="dashboardSettings.customFields.includes('state')" class="container-state" :data-running="record.container.running">{{ record.container.state || t('m.4d8c1c5b4283') }}</span><small v-if="dashboardSettings.customFields.includes('health')" :data-health="record.container.health">{{ containerHealthText(record.container) }}</small><small v-if="dockerStale || record.container.stale" class="container-stale">{{ t('m.6fe221cef061') }}</small></td>
              <td v-if="dashboardSettings.customFields.includes('ports')" :data-label="t('docker.ports')"><span v-for="(port, index) in (record.container.ports ?? [])" :key="index" class="container-port">{{ portText(port) }}</span><span v-if="!record.container.ports?.length">—</span></td>
              <td v-if="dashboardSettings.customFields.includes('uptime')" :data-label="t('docker.uptime')">{{ containerUptime(record) }}</td>
              <td :data-label="t('docker.task')"><span v-if="taskSubmitting[taskKey(record.container.id)]" role="status">{{ t('m.911ee461c36a') }}</span><span v-else-if="taskForContainer(record.container.id)" class="container-task-status" :data-status="taskForContainer(record.container.id)?.status">{{ taskStatusLabel(taskForContainer(record.container.id)!) }}</span><span v-else>—</span><small v-if="taskErrors[taskKey(record.container.id)]" class="container-task-error" role="alert">{{ display(taskErrors[taskKey(record.container.id)]) }}</small></td>
            </tr></tbody>
          </table>
        </div>
        <nav v-if="sortedContainers.length" class="docker-pagination" :aria-label="t('docker.pagination')">
          <label>{{ t('docker.pageSize') }}<select v-model.number="pageSize"><option :value="20">20</option><option :value="50">50</option><option :value="100">100</option></select></label>
          <span>{{ t('docker.page', {'0': containerPage, '1': pageCount}) }}</span><button class="container-action" type="button" :disabled="containerPage <= 1" @click="containerPage--">{{ t('docker.previous') }}</button><button class="container-action" type="button" :disabled="containerPage >= pageCount" @click="containerPage++">{{ t('docker.next') }}</button>
        </nav>
      </section>
    </template>
    <ImagesPanel v-if="activeSection === 'images'" :key="node.nodeId" :node-id="node.nodeId"/><ComposeProjects v-if="activeSection === 'compose'" :key="node.nodeId" :node-id="node.nodeId"/><section v-if="activeSection === 'rebuild'" class="section-panel container-rebuild-page" :aria-label="t('m.e5634738490a')" data-testid="container-rebuild-page"><header class="section-toolbar"><div><span class="eyebrow">{{ t('label.08671c7919d1') }}</span><h2>{{ t('m.e5634738490a') }}</h2></div></header><p class="rebuild-safety-note">{{ t('m.acc2b82becc5') }}</p><p v-if="!rebuildCandidates.length" class="docker-empty">{{ t('m.59042e079f41') }}</p><template v-else><label class="rebuild-container-picker">{{ t('m.4210fc8f913a') }}<select v-model="selectedRebuildContainerID"><option v-for="record in rebuildCandidates" :key="record.container.id" :value="record.container.id">{{ containerTitle(record) }} · {{ record.container.id.slice(0, 12) }}</option></select></label><ContainerRebuildWizard v-if="selectedRebuildContainer" :key="selectedRebuildContainer.container.id" :node-id="node.nodeId" :container="selectedRebuildContainer.container" :stale="!canOperate(selectedRebuildContainer)" :tasks="tasks"/></template></section><NodeFiles v-if="activeSection === 'files'" :key="node.nodeId" :node-id="node.nodeId" :node-name="node.displayName"/>
    <section v-if="activeSection === 'history'" class="section-panel" :aria-label="t('m.792a0100ee53')"><div class="section-toolbar"><div><span class="eyebrow">{{ t('label.8f7889f6a879') }}</span><h2>{{ t('m.fdc402785198') }}{{ nodeTitle }}</h2></div><div class="history-controls"><label>{{ t('m.ae72b532f4b8') }}<select v-model="historyRange"><option value="6h">{{ t('m.320e9ce290e8') }}</option><option value="24h">{{ t('m.ab2c18802e55') }}</option><option value="7d">{{ t('m.2b39b90c0558') }}</option><option value="30d">{{ t('m.c5f1c28c0d5f') }}</option><option value="1y">{{ t('m.80c3d880f104') }}</option></select></label><button class="container-action" type="button" :disabled="historyLoading" @click="void loadHistory()">{{ t('m.bcd6771e08ec') }}</button></div></div><HistoricalMetrics :history="historyView" :loading="historyLoading" :error="historyError" :node-online="online"/><p class="retention-note">{{ t('m.b3b9fb911d3a') }}</p></section>
    <section v-if="activeSection === 'events'" class="section-panel" :aria-label="t('m.5661bd931598')" data-testid="node-event-history"><header class="section-toolbar"><div><span class="eyebrow">RECENT OPERATIONS</span><h2>{{ t('m.7d2e3cfc51fb') }}{{ nodeTitle }}</h2></div><button class="container-action" type="button" @click="emit('refreshTasks', node.nodeId)">{{ t('m.aee887434131') }}</button></header><p class="event-source-note">{{ t('m.30a9a37d5d32') }}</p><p v-if="selectedTasks.length === 0" class="docker-empty">{{ t('m.a9d063f3f3a9') }}</p><article v-for="task in selectedTasks" :key="task.taskId" class="event-card" :data-status="task.status"><div><strong>{{ task.action }} · {{ task.targetId.slice(0, 12) }}</strong><span class="container-task-status" :data-status="task.status">{{ taskStatusText(task) }}</span><time :datetime="task.createdAt">{{ new Date(task.createdAt).toLocaleString(locale) }}</time></div><button class="container-action" type="button" @click="toggleTaskAudit(task)">{{ taskAuditVisible[task.taskId] ? t('m.54e74c1e7504') : t('m.97f395dec8ce') }}</button><p v-if="taskAuditErrors[task.taskId]" class="container-task-error" role="alert">{{ display(taskAuditErrors[task.taskId]) }}</p><ol v-if="taskAuditVisible[task.taskId]" class="container-task-audit"><li v-if="taskAuditLoading[task.taskId]">{{ t('m.cf62890bdb61') }}</li><li v-for="event in (taskAuditViews[task.taskId] ?? [])" :key="event.id">{{ event.event }} · {{ event.fromStatus || t('m.d2bb025a2e51') }} → {{ event.toStatus || t('m.fb7e71452c04') }} · {{ new Date(event.occurredAt).toLocaleString(locale) }}</li></ol></article></section>
    <section v-if="activeSection === 'settings'" class="section-panel node-settings-panel" :aria-label="t('m.0a09fda5e2a6')" data-testid="node-settings"><header class="section-toolbar"><div><span class="eyebrow">{{ t('label.26ae0bd221f2') }}</span><h2>{{ t('m.1b8b4303f8ba') }}{{ nodeTitle }}</h2></div></header><div class="node-settings-status"><div><span>{{ t('m.f5514ad14eaa') }}</span><strong>{{ pending ? t('m.a890b3be09aa') : online ? t('m.b9086662b1df') : t('m.be1b4f3c6c1c') }}</strong></div><div><span>Docker Engine</span><strong>{{ inventory ? dockerAvailabilityText(inventory) : t('m.4d8c1c5b4283') }}</strong></div><div><span>{{ t('m.e31132b9de5a') }}</span><strong>{{ node.agentVersion || t('m.4d8c1c5b4283') }}</strong></div><div><span>{{ t('m.bf04f29ea311') }}</span><strong>{{ node.nodeId }}</strong></div></div><PreferenceEditor :preference="nodePreferenceForEditor()" :title="t('m.b8d1ded2a785', {'0': (node.displayName)})" @saved="acceptPreference"/><div class="dashboard-settings-editor"><h3>{{ t('m.f917a66dfc4f') }}</h3><div class="dashboard-settings-grid"><label>{{ t('m.b91569c6e33a') }}<select v-model="viewMode"><option value="monitor">{{ t('m.fdc390061698') }}</option><option value="manage">{{ t('m.bb6d995724f4') }}</option></select></label><label>{{ t('m.65cd544c6486') }}<select v-model="nodeGroupBy"><option value="status">{{ t('m.b8f945ea49ff') }}</option><option value="group">{{ t('m.c08b046b5470') }}</option><option value="none">{{ t('m.fcd40ee3f05a') }}</option></select></label><label>{{ t('m.dd74dd92d820') }}<select v-model="nodeSortBy"><option value="custom">{{ t('m.0f59d3742e06') }}</option><option value="name">{{ t('m.a98585871c53') }}</option><option value="status">{{ t('m.b8f945ea49ff') }}</option></select></label><label>{{ t('m.f143cf3535b6') }}<select v-model="groupBy"><option value="node">{{ t('m.2410d860f093') }}</option><option value="compose">{{ t('m.5f67dd75b01f') }}</option><option value="state">{{ t('m.e46b0272bd64') }}</option><option value="none">{{ t('m.fcd40ee3f05a') }}</option></select></label><label>{{ t('m.30d908c60571') }}<select v-model="sortBy"><option value="custom">{{ t('m.0f59d3742e06') }}</option><option value="name">{{ t('m.a98585871c53') }}</option><option value="state">{{ t('m.e46b0272bd64') }}</option></select></label><label>{{ t('m.70006f6c5230') }}<input v-model.number="featuredLimit" type="number" min="1" max="20"></label></div><fieldset class="custom-fields"><legend>{{ t('m.25ebfb95e8d6') }}</legend><label v-for="field in customFieldOptions" :key="field.id"><input v-model="customFields" type="checkbox" :value="field.id">{{ field.label }}</label></fieldset><div class="settings-save"><button class="container-action" type="button" :disabled="dashboardSettingsSaving" @click="emit('saveSettings')">{{ dashboardSettingsSaving ? t('m.ff509c9ba052') : t('m.1d6a782c4ccc') }}</button><span v-if="dashboardSettingsMessage" role="status">{{ display(dashboardSettingsMessage) }}</span><span v-if="dashboardSettingsError" role="alert">{{ display(dashboardSettingsError) }}</span></div></div><p class="preference-separation-note">{{ t('m.02f03b13557b') }}</p>
      <NodeAssetSettings :key="node.nodeId" :node-id="node.nodeId" /><footer class="node-settings-delete" data-testid="node-settings-delete">
        <button class="secondary-button node-delete-button" type="button" :disabled="deletingNode" data-testid="node-delete" @click="deleteNode">{{ deletingNode ? t('m.ff509c9ba052') : t('node.delete') }}</button>
        <p v-if="nodeActionError" role="alert">{{ display(nodeActionError) }}</p>
      </footer>
    </section>
    <NodeServiceProbes v-if="activeSection === 'probes'" :key="node.nodeId" :node-id="node.nodeId" :node-name="node.displayName" :node-online="online"/><TerminalConsole v-if="activeTerminal" v-bind="activeTerminal" @close="activeTerminal = null"/>
  </section>
</template>

<style scoped>
.node-enrollment-panel { margin-bottom:24px; }
.node-management-actions{display:flex;flex-wrap:wrap;gap:10px;margin:16px 0}.node-delete-button{color:var(--danger,var(--accent))}.node-settings-delete{margin-top:24px;padding-top:20px;border-top:1px solid var(--line)}.node-settings-delete button{min-height:44px}.node-management-actions button{min-height:44px}

.node-detail-page { width: min(1480px,100%); min-width: 0; margin: 0 auto; padding: clamp(20px,4vw,44px); color: var(--text); }
.detail-heading { display:flex; align-items:center; justify-content:space-between; gap:20px; margin-bottom:20px; }
.probe-overview-summary { display:flex; flex-wrap:wrap; align-items:center; justify-content:space-between; gap:12px; margin:16px 0; border:1px solid var(--line); border-radius:var(--radius-card); padding:12px 16px; background:var(--surface-soft); }
.probe-overview-summary p { margin:0; color:var(--muted-strong); font-size:13px; line-height:1.6; }
.probe-overview-summary small { color:var(--muted); font-size:12px; line-height:1.6; }
.probe-summary-error { color:var(--danger); font-size:13px; line-height:1.6; }
.detail-heading h1 { margin:6px 0 4px; font-size:clamp(24px,3vw,34px); overflow-wrap:anywhere; }
.detail-heading p { margin:0; color:var(--muted); font: 11px/1.5 ui-monospace,monospace; overflow-wrap:anywhere; }
.detail-heading .node-detail-note { margin-top:7px; color:var(--muted-strong); font-family:inherit; font-size: 12px; line-height:1.55; white-space:pre-wrap; }
.detail-heading-navigation { display:flex; flex-wrap:wrap; align-items:center; gap:12px 16px; margin-bottom:12px; }
.detail-heading-status { display:flex; flex-wrap:wrap; justify-content:flex-end; gap:8px; }
.container-create-results { display:grid; gap:6px; margin:8px 0 12px; }.container-create-results h3 { margin:0; color:var(--muted-strong); font-size: 12px; }.container-create-results article { display:flex; flex-wrap:wrap; align-items:center; justify-content:space-between; gap:8px; border:1px solid var(--line); border-radius:var(--radius-card); padding:8px 10px; background:var(--surface-soft); }.container-create-results article>div { display:flex; flex-wrap:wrap; align-items:center; gap:9px; }.container-create-results strong,.container-create-results code,.container-create-results time { font-size: 12px; overflow-wrap:anywhere; }.container-create-results time { color:var(--muted); }
.metrics-waiting { position:relative; min-height:260px; display:flex; flex-direction:column; align-items:flex-start; justify-content:center; padding:clamp(20px,5vw,52px); border:1px solid var(--line); border-radius:var(--radius-card); background:var(--surface-soft); }
.metrics-waiting h2 { margin:10px 0 0; font-size: 21px; }.metrics-waiting p { max-width:440px; color:var(--muted-strong); font-size: 12px; line-height:1.7; }
.node-detail-status,.docker-state { border:1px solid var(--line); border-radius:999px; padding:6px 11px; color:var(--danger); font-size: 12px; }
.node-detail-status[data-online=true],.docker-state[data-available=available][data-stale=false] { color:var(--success); }
.node-terminal-entry { display:flex; align-items:center; justify-content:space-between; gap:12px; margin-top:16px; border:1px solid var(--surface-soft); border-radius:var(--radius-card); padding:12px 14px; background:var(--surface-soft); }
.node-terminal-entry>div { display:grid; gap:4px; }.node-terminal-entry strong { color:var(--accent); font-size: 12px; }.node-terminal-entry small { color:var(--accent); font-size: 12px; line-height:1.5; }
.node-terminal-entry button { min-height:40px; border:1px solid var(--accent-line); border-radius:var(--radius-control); padding:6px 10px; color:var(--accent); background:var(--surface-soft); font-size: 12px; cursor:pointer; }
.node-terminal-entry button:disabled { opacity:.45; cursor:not-allowed; }
.docker-panel,.section-panel { min-width:0; margin-top:16px; border:1px solid var(--line); border-radius:var(--radius-card); padding:clamp(14px,2vw,20px); background:var(--surface-soft); }
.docker-heading,.section-toolbar { display:flex; align-items:center; justify-content:space-between; gap:12px; margin-bottom:12px; }.docker-heading h2,.section-toolbar h2 { margin:4px 0 0; font-size: 17px; }
.docker-view-controls,.history-controls { display:flex; flex-wrap:wrap; align-items:end; gap:8px; margin:12px 0; }.docker-view-controls label,.history-controls label { display:grid; min-width:120px; gap:5px; color:var(--muted-strong); font-size: 12px; }
.docker-view-controls input,.docker-view-controls select,.history-controls select,.dashboard-settings-grid select,.dashboard-settings-grid input { min-height:40px; min-width:0; border:1px solid var(--line); border-radius:var(--radius-control); padding:7px 9px; color:var(--text); background:var(--surface-soft); font:inherit; font-size: 12px; }
.docker-view-controls input { width:min(360px,58vw); }
.docker-selection-bar,.docker-toolbar,.docker-pagination { display:flex; flex-wrap:wrap; align-items:center; gap:8px; margin:12px 0; }
.docker-selection-bar { color:var(--muted-strong); font-size:12px; }
.docker-selection-bar label { display:inline-flex; align-items:center; gap:8px; min-height:40px; }
.docker-selection-bar input,.container-select input { width:18px; height:18px; accent-color:var(--accent); cursor:pointer; }
.docker-selection-bar input:disabled,.container-select input:disabled { cursor:not-allowed; opacity:.5; }
.docker-selection-bar button { margin-left:auto; }
.docker-selection-help { margin:0 0 12px; color:var(--muted); font-size:12px; line-height:1.6; }
.docker-more { position:relative; }
.docker-more summary { display:flex; align-items:center; min-height:40px; border:1px solid var(--line); border-radius:var(--radius-control); padding:8px 12px; color:var(--accent); background:var(--surface); font-size:13px; cursor:pointer; list-style:none; }
.docker-more summary::after { content:'⌄'; margin-left:9px; }
.docker-more summary:hover,.docker-more[open] summary { background:var(--accent-soft); border-color:var(--accent-line); }
.docker-more summary:active { background:var(--line); }
.docker-more summary:focus-visible,.container-name:focus-visible,.docker-selection-bar input:focus-visible,.container-select input:focus-visible { outline:2px solid var(--accent); outline-offset:3px; }
.docker-more-menu { position:absolute; z-index:12; top:calc(100% + 6px); right:0; display:grid; gap:4px; width:240px; max-width:calc(100vw - 64px); border:1px solid var(--line); border-radius:var(--radius-card); padding:8px; background:var(--surface); box-shadow:0 8px 28px #0002; }
.docker-more-menu .container-action { justify-content:flex-start; }
.visually-hidden { position:absolute; width:1px; height:1px; padding:0; margin:-1px; overflow:hidden; clip-path:inset(50%); white-space:nowrap; border:0; }
.docker-table-scroll { max-width:100%; overflow-x:auto; border:1px solid var(--line); border-radius:var(--radius-card); }
.container-table { width:100%; border-collapse:collapse; table-layout:fixed; text-align:left; font-size:12px; }
.container-table th { padding:12px 10px; color:var(--muted-strong); background:var(--surface); font-weight:500; border-bottom:1px solid var(--line); }
.container-table th:first-child { width:40px; }
.container-table td { padding:12px 10px; border-bottom:1px solid var(--line); vertical-align:top; overflow-wrap:anywhere; color:var(--text); line-height:1.6; }
.container-table tbody tr:last-child td { border-bottom:0; }
.container-table tr[data-selected=true] { background:var(--accent-soft); }
.container-table tr[data-stale=true] { color:var(--muted); }
.container-table small { display:block; margin-top:4px; color:var(--muted-strong); font-size:11px; overflow-wrap:anywhere; }
.container-table code { font-size:11px; }
.container-name { display:block; max-width:100%; border:0; padding:0; color:var(--accent); background:transparent; font:inherit; text-align:left; overflow-wrap:anywhere; cursor:pointer; }
.container-name:hover { text-decoration:underline; }.container-name:active { color:var(--text); }
.container-port { display:block; margin-bottom:4px; font-size:11px; }
.container-state { display:inline-block; border:1px solid var(--line); border-radius:999px; padding:3px 8px; color:var(--muted-strong); font-size:11px; }
.container-state[data-running=true] { color:var(--success); border-color:var(--success-line); }
.container-stale,.container-task-error { color:var(--danger)!important; }.container-rollback-state,.container-rollback-note { color:var(--warning); }
.container-task-status,.container-task-error { font-size:12px; overflow-wrap:anywhere; }
.container-reason,.docker-empty,.event-source-note,.retention-note,.preference-separation-note { color:var(--muted-strong); font-size:12px; line-height:1.6; overflow-wrap:anywhere; }
.service-link { color:var(--accent); text-decoration:none; }.service-link:visited { color:var(--accent); }
.container-task-audit { margin:8px 0 0; padding:8px 8px 8px 26px; border:1px solid var(--line); border-radius:var(--radius-card); color:var(--muted-strong); font-size:12px; }.container-task-audit li { padding:3px 0; }
.docker-pagination { justify-content:flex-end; color:var(--muted-strong); font-size:12px; }
.docker-pagination label { display:flex; align-items:center; gap:8px; margin-right:auto; }
.docker-pagination select { min-height:40px; border:1px solid var(--line); border-radius:var(--radius-control); padding:7px; color:var(--text); background:var(--surface); }
.container-detail { margin-top:16px; padding:16px; border:1px solid var(--line); border-radius:var(--radius-card); background:var(--surface); }
.container-detail header { display:flex; flex-wrap:wrap; justify-content:space-between; align-items:center; gap:8px; }.container-detail h3 { margin:0; font-size:17px; font-weight:400; overflow-wrap:anywhere; }
.container-detail dl { display:grid; grid-template-columns:auto minmax(0,1fr); gap:8px 16px; color:var(--text); font-size:12px; line-height:1.6; }.container-detail dt { color:var(--muted); }.container-detail dd { margin:0; overflow-wrap:anywhere; }.container-detail p { color:var(--muted-strong); font-size:12px; line-height:1.6; overflow-wrap:anywhere; }
@media(max-width:760px) {
  .container-table,.container-table tbody { display:block; }.container-table thead { display:none; }
  .container-table tr { display:grid; grid-template-columns:34px minmax(0,1fr); padding:10px 8px; border-bottom:1px solid var(--line); }
  .container-table tr:last-child { border-bottom:0; }.container-table td { display:block; grid-column:2; padding:5px 8px; border:0; }
  .container-table td.container-select { grid-column:1; grid-row:1; padding:5px 0; }.container-table td[data-label]::before { content:attr(data-label); display:block; color:var(--muted); font-size:11px; margin-bottom:3px; }
  .docker-pagination { justify-content:flex-start; }.docker-pagination label { flex:1 0 100%; }.container-detail { padding:12px; }
}
.history-controls { margin:0; }.history-controls select { min-width:150px; }.retention-note,.event-source-note,.preference-separation-note { font-size: 12px; }.event-card { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:8px; margin-top:8px; border:1px solid var(--line); border-radius:var(--radius-card); padding:10px; background:var(--surface-soft); }.event-card>div:first-child { display:grid; gap:5px; }.event-card strong { font-size: 12px; overflow-wrap:anywhere; }.event-card time { color:var(--muted); font-size: 12px; }.event-card .container-task-audit,.event-card>p { grid-column:1/-1; }
.node-settings-status { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:8px; margin:12px 0; }.node-settings-status>div { display:grid; min-width:0; gap:5px; border:1px solid var(--line); border-radius:var(--radius-card); padding:9px; }.node-settings-status strong { color:var(--text); font-size: 12px; overflow-wrap:anywhere; }.dashboard-settings-editor { margin-top:16px; border-top:1px solid var(--line); padding-top:14px; }.dashboard-settings-editor h3 { margin:0 0 12px; font-size: 12px; }.dashboard-settings-grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:9px; }.dashboard-settings-grid label { display:grid; min-width:0; gap:5px; color:var(--muted-strong); font-size: 12px; }.custom-fields { display:flex; flex-wrap:wrap; gap:8px 14px; margin:12px 0; border:1px solid var(--line); border-radius:var(--radius-card); padding:10px; }.custom-fields legend { padding:0 5px; color:var(--muted-strong); font-size: 12px; }.custom-fields label { display:inline-flex; align-items:center; gap:5px; color:var(--muted-strong); font-size: 12px; }.settings-save { display:flex; flex-wrap:wrap; align-items:center; gap:9px; color:var(--success); font-size: 12px; }.settings-save span[role=alert] { color:var(--danger); }
.rebuild-safety-note { margin:0 0 12px; border-left:2px solid var(--warning); padding-left:10px; color:var(--warning); font-size: 12px; line-height:1.6; }.rebuild-container-picker { display:grid; max-width:680px; gap:6px; margin:10px 0; color:var(--muted-strong); font-size: 12px; }.rebuild-container-picker select { min-height:40px; min-width:0; border:1px solid var(--line); border-radius:var(--radius-control); padding:7px 9px; color:var(--text); background:var(--surface-soft); font:inherit; }
@media(max-width:900px) { .node-settings-status { grid-template-columns:repeat(2,minmax(0,1fr)); }.dashboard-settings-grid { grid-template-columns:repeat(2,minmax(0,1fr)); } }
@media(max-width:560px) { .node-detail-page { padding:20px 14px; }.detail-heading { align-items:flex-start; flex-direction:column; }.detail-heading-status { justify-content:flex-start; }.section-toolbar,.docker-heading { align-items:flex-start; flex-direction:column; }.history-controls { width:100%; }.history-controls label,.history-controls select { width:100%; }.docker-view-controls,.docker-view-controls label,.docker-view-controls input,.docker-view-controls select,.docker-view-controls button { width:100%; }.node-settings-status,.dashboard-settings-grid { grid-template-columns:minmax(0,1fr); }node-terminal-entry { align-items:flex-start; flex-direction:column; }.node-terminal-entry button { width:100%; min-height:42px; } }
</style>
