<script setup lang="ts">
import { t, display, locale } from "../i18n"
import { computed, ref } from 'vue'
import type { AgentNode, ContainerTask, ContainerTaskAction, DashboardPreference, DockerContainerRecord, DockerInventory } from '../api'
import type { Metric, MetricsView } from '../metrics-contract'

const dockerExpanded = ref(false)

const props = defineProps<{
  node: AgentNode
  metrics?: MetricsView
  inventory?: DockerInventory
  nodePreference?: DashboardPreference
  containerPreferences: DashboardPreference[]
  preferenceIdentities: Record<string, string>
  online: boolean
  pending: boolean
  dockerStale: boolean
  previewLimit: number
  currentTime: number
  nodeReason?: string
  tasks: ContainerTask[]
  taskSubmitting: Record<string, boolean>
  taskErrors: Record<string, string>
}>()

const emit = defineEmits<{
  view: [node: AgentNode]
  quickAction: [payload: { node: AgentNode; action: 'terminal' | 'files' | 'manage' }]
  containerAction: [payload: { node: AgentNode; record: DockerContainerRecord; action: ContainerTaskAction }]
}>()

const title = computed(() => props.nodePreference?.alias || props.node.displayName)
const sshCommand = computed(() => {
  const host = props.nodePreference?.sshHost ?? ''
  const user = props.nodePreference?.sshUser ?? ''
  const port = props.nodePreference?.sshPort || 22
  if (!host || !user || host.startsWith('-') || !/^[A-Za-z0-9._:%-]+$/.test(host) ||
      !/^[A-Za-z_][A-Za-z0-9_.-]{0,63}$/.test(user) || !Number.isInteger(port) || port < 1 || port > 65535) return ''
  return `ssh -p ${port} ${user}@${host}`
})
const sshCopyMessage = ref('')
const displayContainers = computed(() => {
  const containers = (props.inventory?.containers ?? []).filter((record) => preferenceFor(record)?.visible !== false)
  containers.sort((left, right) => {
    const leftPreference = preferenceFor(left)
    const rightPreference = preferenceFor(right)
    if (leftPreference?.pinned !== rightPreference?.pinned) return leftPreference?.pinned ? -1 : 1
    if ((leftPreference?.sortOrder ?? 0) !== (rightPreference?.sortOrder ?? 0)) {
      return (leftPreference?.sortOrder ?? 0) - (rightPreference?.sortOrder ?? 0)
    }
    return containerTitle(left).localeCompare(containerTitle(right), locale.value)
  })
  return containers.slice(0, Math.max(1, Math.min(20, props.previewLimit)))
})

const displayedMount = computed(() => {
  const mounts = props.metrics?.metrics.disk.mounts ?? []
  return mounts.find((mount) => mount.mountpoint === '/') ?? mounts.find((mount) => mount.usage.value) ?? mounts[0]
})

function preferenceFor(record: DockerContainerRecord): DashboardPreference | undefined {
  const identity = props.preferenceIdentities[record.container.id]
  if (!identity) return undefined
  const kind = record.container.compose ? 'compose_service' : 'container'
  return props.containerPreferences.find((item) => item.targetKind === kind && item.identity === identity)
}

function containerTitle(record: DockerContainerRecord): string {
  return preferenceFor(record)?.alias || record.container.name || record.container.id.slice(0, 12)
}

function containerIcon(record: DockerContainerRecord): string {
  const icons: Record<string, string> = {
    server: '▤', globe: '◎', database: '▥', shield: '⬡', terminal: '›_',
    box: '▣', cloud: '☁', folder: '▰', activity: '⌁',
  }
  return icons[preferenceFor(record)?.icon ?? ''] ?? '▣'
}

function nodeStatusLabel(): string {
  if (props.pending) return t('m.a890b3be09aa')
  return props.online ? t('m.b9086662b1df') : props.node.status === 'revoked' ? t('m.fef96e34f77b') : t('m.be1b4f3c6c1c')
}

function nodeIcon(icon?: string): string {
  const icons: Record<string, string> = {
    server: '▤', globe: '◎', database: '▥', shield: '⬡', terminal: '›_',
    box: '▣', cloud: '☁', folder: '▰', activity: '⌁',
  }
  return icon ? icons[icon] ?? '▤' : '▤'
}

function metricValue<T>(metric: Metric<T> | undefined, value: number | undefined, suffix = '%', ttlMs = 15_000): string {
  if (!metric) return props.online ? t('m.ade1506ac3ef') : t('m.497c85690c4c')
  if (value === undefined || !Number.isFinite(value)) {
    if (metric.status === 'error') return t('m.6aa27f14bff1')
    if (metric.status === 'stale') return t('m.2fe0e3339ac4')
    return metric.status === 'known' ? t('m.4d8c1c5b4283') : props.online ? t('m.ade1506ac3ef') : t('m.497c85690c4c')
  }
  const receivedAt = Date.parse(props.metrics?.receivedAt ?? '')
  const elapsed = Number.isFinite(receivedAt) ? Math.max(0, props.currentTime - receivedAt) : 0
  const stale = !props.online || metric.status === 'stale' || metric.sampleAgeMillis + elapsed > ttlMs
  return t('m.f25b2ceba8a5', {"0": (value.toFixed(1)), "1": (suffix), "2": (stale ? t('m.fb05374523d1') : '')})
}

function diskSummary(): string {
  const disk = props.metrics?.metrics.disk
  const mount = displayedMount.value
  if (!disk || !mount) return disk?.status === 'error' ? t('m.6aa27f14bff1') : props.online ? t('m.ade1506ac3ef') : t('m.497c85690c4c')
  const metric = mount.usage
  return `${mount.mountpoint} ${metricValue(metric, metric.value?.usedPercent, '%', 90_000)}`
}

function networkSummary(direction: 'receivedBytesPerSecond' | 'sentBytesPerSecond'): string {
  const metric = props.metrics?.metrics.network.summary
  const value = metric?.value?.[direction]
  if (value === undefined || !Number.isFinite(value)) {
    if (metric?.status === 'error') return t('m.6aa27f14bff1')
    return props.online ? t('m.ade1506ac3ef') : t('m.497c85690c4c')
  }
  const receivedAt = Date.parse(props.metrics?.receivedAt ?? '')
  const elapsed = Number.isFinite(receivedAt) ? Math.max(0, props.currentTime - receivedAt) : 0
  const stale = !props.online || !metric || metric.status === 'stale' || metric.sampleAgeMillis + elapsed > 20_000
  return t('m.022a917ae125', {"0": (formatBytes(value)), "1": (stale ? t('m.fb05374523d1') : '')})
}

function uptimeSummary(): string {
  const metric = props.metrics?.metrics.uptime
  const seconds = metric?.value?.seconds
  if (seconds === undefined || !Number.isFinite(seconds)) return metric?.status === 'error' ? t('m.6aa27f14bff1') : t('m.4d8c1c5b4283')
  const receivedAt = Date.parse(props.metrics?.receivedAt ?? '')
  const elapsed = props.online && Number.isFinite(receivedAt) ? Math.max(0, Math.floor((props.currentTime - receivedAt) / 1000)) : 0
  return t('m.8391da809dd9', {"0": (formatDuration(seconds + elapsed)), "1": (props.online ? '' : t('m.6360a8989089'))})
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) return t('m.4d8c1c5b4283')
  if (value < 1024) return `${Math.round(value)} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let amount = value
  let unit = 'B'
  for (const next of units) {
    amount /= 1024
    unit = next
    if (amount < 1024) break
  }
  return `${amount.toFixed(1)} ${unit}`
}

function formatDuration(seconds: number): string {
  const days = Math.floor(seconds / 86_400)
  const hours = Math.floor((seconds % 86_400) / 3_600)
  const minutes = Math.floor((seconds % 3_600) / 60)
  return days > 0 ? t('m.295f367bb72a', {"0": (days), "1": (hours)}) : hours > 0 ? t('m.3cde3f0ae16c', {"0": (hours), "1": (minutes)}) : t('m.c65627ead130', {"0": (minutes)})
}

function dockerStatus(): string {
  if (props.inventory?.dockerAvailability === 'unavailable' || props.inventory?.staleReason === 'docker_capability_unavailable') return t('m.d3e431f9d10b')
  if (props.inventory?.dockerAvailability === 'available') {
    if (props.dockerStale || props.inventory.dataStale) return t('m.1a6858956811')
    if (props.inventory.health?.errorKind === 'api_incompatible') return t('m.d56e55420ff6')
    if (props.inventory.health?.reason) return t('m.04dea0459ab7')
    return t('m.81e93ba5b0a1')
  }
  return props.pending ? t('m.270c0a7750e1') : props.online ? t('m.c12a50c262ca') : t('m.de300a2b89b7')
}

function healthText(record: DockerContainerRecord): string {
  const container = record.container
  if (!container.healthcheckConfigured || container.health === 'none') return t('m.5fac1c671a0d')
  return ({ healthy: t('m.aa18b6dcd893'), unhealthy: t('m.428fb8bfeecf'), starting: t('m.33439d263173'), unknown: t('m.4d8c1c5b4283') } as Record<string, string>)[container.health] ?? container.health
}

function serviceURL(record: DockerContainerRecord): string {
  const candidate = preferenceFor(record)?.serviceUrl
  if (!candidate) return ''
  try {
    const url = new URL(candidate)
    return ['http:', 'https:'].includes(url.protocol) && !url.username && !url.password && !url.hash ? url.href : ''
  } catch { return '' }
}

function networkText(record: DockerContainerRecord): string {
  const container = record.container
  if (container.hostNetwork) return t('m.7286fcd72f9e')
  const networks = container.networks ?? []
  if (!networks.length) return t('m.ad2c9a6cff20')
  return networks.map((network) => {
    const addresses = [network.ipv4 && `IPv4 ${network.ipv4}`, network.ipv6 && `IPv6 ${network.ipv6}`].filter(Boolean)
    return t('m.8391da809dd9', {"0": (network.name), "1": (addresses.length ? `：${addresses.join(' · ')}` : t('m.0dbb481d93ec'))})
  }).join('；')
}

function portText(record: DockerContainerRecord): string {
  const container = record.container
  const ports = container.ports ?? []
  if (container.hostNetwork) {
    const prefix = t('m.91517a7693e7')
    if (!ports.length) return t('m.74e8e0d1e8fd', {"0": (prefix)})
    const declared = ports.map((port) => {
      const label = `${port.containerPort}/${port.protocol}`
      if (port.configured.length) return t('m.a35b20b5cb32', {"0": (label)})
      if (port.exposed) return t('m.ae6cee47ad79', {"0": (label)})
      return t('m.9b985579e81e', {"0": (label)})
    })
    return `${prefix}；${declared.join('，')}`
  }
  if (!ports.length) return t('m.e1548d8f0d87')
  return ports.map((port) => {
    if (port.published.length) {
      return port.published.map((binding) => {
        const rawHost = binding.ip || '*'
        const formattedHost = rawHost.includes(':') && !rawHost.startsWith('[') ? `[${rawHost}]` : rawHost
        const scope = rawHost === '*' || rawHost === '0.0.0.0' || rawHost === '::'
          ? t('m.a54cd64d75d5')
          : rawHost === '::1' || /^127(?:\.\d{1,3}){3}$/.test(rawHost)
            ? t('m.b07e34088b42')
            : ''
        return `${formattedHost}:${binding.port} → ${port.containerPort}/${port.protocol}${scope}`
      }).join(', ')
    }
    if (port.configured.length) return t('m.639b66233cf4', {"0": (port.containerPort), "1": (port.protocol)})
    if (port.exposed) return t('m.c39c143634dc', {"0": (port.containerPort), "1": (port.protocol)})
    return `${port.containerPort}/${port.protocol}`
  }).join('；')
}

function containerUptime(record: DockerContainerRecord): string {
  const startedAt = record.container.startedAt
  if (!record.container.running || !startedAt) return t('m.4d8c1c5b4283')
  const started = Date.parse(startedAt)
  if (!Number.isFinite(started) || started > props.currentTime) return t('m.4d8c1c5b4283')
  return formatDuration(Math.floor((props.currentTime - started) / 1000))
}

function containerCreated(record: DockerContainerRecord): string {
  if (!record.container.createdAt) return t('m.4d8c1c5b4283')
  const created = Date.parse(record.container.createdAt)
  return Number.isFinite(created) ? new Date(created).toLocaleString(locale.value) : t('m.4d8c1c5b4283')
}

function taskKey(containerId: string): string {
  return `${props.node.nodeId}:${containerId}`
}

function taskFor(record: DockerContainerRecord): ContainerTask | undefined {
  return props.tasks.filter((task) => task.targetId === record.container.id ||
    task.action === 'rebuild' && task.status === 'succeeded' && task.result.resourceRevision === record.container.id)
    .sort((left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt))[0]
}

function taskLabel(task: ContainerTask | undefined): string {
  if (!task) return ''
  if (task.status === 'queued') return t('m.7ffc79834df2')
  if (task.status === 'running') return t('m.e71170d00ddc', {"0": (task.progress.phase)})
  if (task.status === 'succeeded') return t('m.0a1dae196200')
  if (task.status === 'unknown' || task.status === 'timed_out') return t('m.400d8fa7bdd9')
  return task.status === 'failed' ? t('m.9344597efd49') : t('m.6f96c2add214')
}

function canOperate(record: DockerContainerRecord): boolean {
  return Boolean(props.online && props.node.agentId && props.inventory?.dockerAvailability === 'available' &&
    props.inventory.staleReason !== 'docker_capability_unavailable' && !props.dockerStale && !record.container.stale &&
    !props.taskSubmitting[taskKey(record.container.id)])
}

function emitAction(record: DockerContainerRecord, action: ContainerTaskAction) {
  if (!canOperate(record)) return
  emit('containerAction', { node: props.node, record, action })
}

async function copySSHCommand() {
  if (!sshCommand.value) return
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(sshCommand.value)
    } else {
      const input = document.createElement('textarea')
      input.value = sshCommand.value
      input.setAttribute('readonly', '')
      input.style.position = 'fixed'
      input.style.opacity = '0'
      document.body.append(input)
      input.select()
      const copied = document.execCommand('copy')
      input.remove()
      if (!copied) throw new Error('clipboard unavailable')
    }
    sshCopyMessage.value = t('m.ef8956febccf')
  } catch {
    sshCopyMessage.value = t('m.974cd922475e', {"0": (sshCommand.value)})
  }
}
</script>

<template>
  <article class="vps-card" :data-node-id="node.nodeId" :data-online="online" :data-docker-stale="dockerStale">
    <header class="vps-card-header">
      <div class="vps-card-heading">
        <span class="vps-node-icon" aria-hidden="true">{{ nodeIcon(nodePreference?.icon) }}</span>
        <div><span class="eyebrow">VPS<template v-if="nodePreference?.group"> · {{ nodePreference.group }}</template></span><h2><button class="vps-name" type="button" @click="emit('view', node)">{{ title }}</button></h2></div>
      </div>
      <span class="vps-card-status" :data-online="online" :data-pending="pending" role="status">{{ nodeStatusLabel() }}</span>
    </header>
    <p class="vps-card-version">{{ node.agentVersion || t('m.d07581c22bb2') }}</p>
    <p v-if="nodePreference?.notes" class="vps-card-note">{{ nodePreference.notes }}</p>

    <div class="vps-card-metrics" :aria-label="t('m.e41b8d592e33')">
      <div><span>CPU</span><strong>{{ metricValue(metrics?.metrics.cpu.usagePercent, metrics?.metrics.cpu.usagePercent.value ?? undefined) }}</strong></div>
      <div><span>{{ t('m.7d8f8c37ec78') }}</span><strong>{{ metricValue(metrics?.metrics.memory, metrics?.metrics.memory.value?.usedPercent) }}</strong></div>
      <div><span>{{ t('m.de7b72a3f852') }}</span><strong>{{ diskSummary() }}</strong></div>
      <div><span>{{ t('m.d7140d0b3390') }}</span><strong>{{ networkSummary('receivedBytesPerSecond') }} / {{ networkSummary('sentBytesPerSecond') }}</strong></div>
      <div><span>{{ t('m.5986e9d976e0') }}</span><strong>{{ uptimeSummary() }}</strong></div>
    </div>

    <button class="docker-toggle" type="button" :aria-expanded="dockerExpanded" :aria-controls="`docker-${node.nodeId}`" @click="dockerExpanded = !dockerExpanded">{{ t(dockerExpanded ? 'dashboard.hideDocker' : 'dashboard.showDocker') }} <span aria-hidden="true">{{ dockerExpanded ? '−' : '+' }}</span></button>
    <section v-if="dockerExpanded" :id="`docker-${node.nodeId}`" class="vps-card-docker" :data-available="inventory?.dockerAvailability || 'unknown'" :data-stale="dockerStale">
      <header><strong>{{ t('m.7cabcc483eb4') }}</strong><span>{{ inventory?.containers.length ?? '—' }} · {{ dockerStatus() }}</span></header>
      <p v-if="!inventory" class="vps-card-empty">{{ pending ? t('m.0470e99d0fc7') : online ? t('m.5a7828919f0f') : t('m.16f23efa02f5') }}</p>
      <p v-else-if="inventory.dockerAvailability === 'unavailable' || inventory.staleReason === 'docker_capability_unavailable'" class="vps-card-empty">{{ t('m.0b7e6af1b292') }}{{ inventory.health?.reason || inventory.staleReason || '' }}</p>
      <p v-if="inventory && inventory.containers.length === 0 && inventory.dockerAvailability !== 'unavailable' && inventory.staleReason !== 'docker_capability_unavailable'" class="vps-card-empty">{{ dockerStale ? t('m.ff2c9544c1dc') : t('m.20165da39974') }}</p>
      <p v-else-if="inventory && inventory.containers.length > 0 && displayContainers.length === 0" class="vps-card-empty">{{ t('m.1cab1d9e5d00') }}</p>
      <div v-else-if="inventory && displayContainers.length > 0" class="vps-card-container-list">
        <article v-for="record in displayContainers" :key="record.container.id" class="vps-card-container" :data-stale="dockerStale || record.container.stale">
          <span class="vps-card-container-icon" aria-hidden="true">{{ containerIcon(record) }}</span>
          <div class="vps-card-container-copy">
            <strong>{{ containerTitle(record) }}</strong>
            <small>{{ record.container.name || record.container.id.slice(0, 12) }} · {{ record.container.image || t('m.45c82e228c16') }}</small>
            <small>{{ record.container.compose ? `Compose · ${record.container.compose.project}/${record.container.compose.service}` : t('m.ba4ddea11c26') }} · {{ healthText(record) }}</small>
            <small>{{ networkText(record) }}</small>
            <small>{{ portText(record) }}</small>
            <small>{{ t('m.b890b09aa795') }}{{ containerCreated(record) }}{{ t('m.f8dd4dc7e620') }}{{ containerUptime(record) }}</small>
            <small v-if="record.container.healthReason || record.container.unavailableReason" class="vps-card-reason">{{ record.container.healthReason || record.container.unavailableReason }}</small>
            <a v-if="serviceURL(record)" class="vps-card-service-link" :href="serviceURL(record)" target="_blank" rel="noopener noreferrer">{{ t('m.e4fd01edc24f') }}</a>
            <small v-if="dockerStale || record.container.stale" class="vps-card-stale">{{ t('m.6969fe6bf481') }}</small>
            <small v-if="taskLabel(taskFor(record))" class="vps-card-task" role="status">{{ taskLabel(taskFor(record)) }}</small>
            <small v-if="taskErrors[taskKey(record.container.id)]" class="vps-card-error" role="alert">{{ display(taskErrors[taskKey(record.container.id)]) }}</small>
            <div class="vps-card-actions" :aria-label="t('m.9fb9e5641587')">
              <button v-if="record.container.paused" type="button" :disabled="!canOperate(record)" @click="emitAction(record, 'resume')">{{ t('m.e0534b8a4e46') }}</button>
              <template v-else-if="record.container.running">
                <button type="button" :disabled="!canOperate(record)" @click="emitAction(record, 'stop')">{{ t('m.ca4d973c0b00') }}</button>
                <button type="button" :disabled="!canOperate(record)" @click="emitAction(record, 'restart')">{{ t('m.562822892865') }}</button>
              </template>
              <button v-else type="button" :disabled="!canOperate(record)" @click="emitAction(record, 'start')">{{ t('m.56410fc65314') }}</button>
            </div>
          </div>
          <span class="vps-card-state" :data-running="record.container.running">{{ record.container.state || t('m.4d8c1c5b4283') }}</span>
        </article>
        <p v-if="inventory.containers.filter((record) => preferenceFor(record)?.visible !== false).length > displayContainers.length" class="vps-card-more">{{ t('m.268ec6902063') }}{{ inventory.containers.filter((record) => preferenceFor(record)?.visible !== false).length - displayContainers.length }}{{ t('m.6a5b6795ed27') }}</p>
        <p v-if="dockerStale" class="vps-card-stale">{{ t('m.188cce5ffdcd') }}</p>
      </div>
    </section>
    <nav class="vps-card-shortcuts" :aria-label="t('m.c820131d2266', {'0': (title)})">
      <button type="button" :disabled="!online" @click="emit('quickAction', { node, action: 'terminal' })">{{ t('m.e2a76ef1f12e') }}</button>
      <button type="button" :disabled="!online" @click="emit('quickAction', { node, action: 'files' })">{{ t('m.39932f24fe11') }}</button>
      <button type="button" :disabled="!sshCommand" :title="sshCommand || t('m.3c1249308515')" @click="void copySSHCommand()">{{ t('m.12d40d9f555d') }}</button>
      <button type="button" @click="emit('quickAction', { node, action: 'manage' })">{{ t('m.bb6d995724f4') }}</button>
    </nav>
    <p v-if="sshCopyMessage" class="vps-card-ssh-status" role="status">{{ display(sshCopyMessage) }}</p>
    <footer class="vps-card-footer">
      <span>{{ nodeReason || (dockerExpanded && dockerStale ? t('m.5662971e4f8b') : '') }}</span>
      <button type="button" @click="emit('view', node)">{{ t('m.1ee33cf03dd4') }}</button>
    </footer>
  </article>
</template>

<style scoped>
.vps-card-heading .vps-name { padding:0; border:0; min-height:40px; color:var(--text); background:transparent; text-align:left; font:inherit; overflow-wrap:anywhere; }
.vps-card-heading .vps-name:hover { color:var(--accent); }
.docker-toggle { display:flex; justify-content:space-between; align-items:center; width:100%; margin-top:12px; color:var(--accent); }

.vps-card { min-width: 0; border: 1px solid var(--line); border-radius:var(--radius-card); padding: clamp(14px,2vw,20px); background: var(--surface); color: var(--text); }
.vps-card-header { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.vps-card-heading { display: flex; min-width: 0; align-items: center; gap: 9px; }
.vps-card-heading > div { min-width: 0; }
.vps-card-heading h2 { margin: 4px 0 0; font-size: 16px; overflow-wrap: anywhere; }
.vps-node-icon { display: grid; width: 34px; height: 34px; flex: 0 0 auto; place-items: center; border: 1px solid var(--line); border-radius:var(--radius-card); color: var(--accent); background: var(--line); font-size: 17px; }
.vps-card-status { border: 1px solid var(--line); border-radius: 999px; padding: 6px 10px; color: var(--danger); font-size: 12px; white-space: nowrap; }
.vps-card-status[data-online='true'] { color: var(--success); }
.vps-card-status[data-pending='true'] { color: var(--warning); }
.vps-card-version { margin: 6px 0 12px; color: var(--muted); font-size: 12px; line-height: 1.5; overflow-wrap: anywhere; }
.vps-card-note { margin: -5px 0 12px; color: var(--muted-strong); font-size: 12px; line-height: 1.5; white-space: pre-wrap; overflow-wrap: anywhere; }
.vps-card-metrics { display: grid; grid-template-columns: repeat(5,minmax(0,1fr)); gap: 7px; }
.vps-card-metrics > div { display: grid; min-width: 0; gap: 5px; border: 1px solid var(--line); border-radius:var(--radius-card); padding: 8px; }
.vps-card-metrics span { color: var(--muted); font-size: 12px; }
.vps-card-metrics strong { color: var(--text); font-size: 12px; line-height: 1.4; overflow-wrap: anywhere; }
.vps-card-docker { margin-top: 12px; border: 1px solid var(--line); border-radius:var(--radius-card); padding: 10px; background: var(--surface-soft); }
.vps-card-docker > header { display: flex; align-items: center; justify-content: space-between; gap: 10px; color: var(--text); font-size: 12px; }
.vps-card-docker > header span { color: var(--muted); font: 11px ui-monospace,monospace; text-align: right; }
.vps-card-empty { margin: 8px 0 0; color: var(--muted-strong); font-size: 12px; line-height: 1.5; overflow-wrap: anywhere; }
.vps-card-container-list { display: grid; gap: 7px; margin-top: 8px; }
.vps-card-container { display: grid; grid-template-columns: auto minmax(0,1fr) auto; align-items: start; gap: 7px; border: 1px solid var(--line); border-radius:var(--radius-card); padding: 8px; }
.vps-card-container[data-stale='true'] { border-color: var(--danger-line); }
.vps-card-container-icon { display: grid; width: 24px; height: 24px; place-items: center; border-radius:var(--radius-card); color: var(--accent); background: var(--line); font-size: 12px; }
.vps-card-container-copy { display: grid; min-width: 0; gap: 4px; }
.vps-card-container-copy strong, .vps-card-container-copy small { overflow-wrap: anywhere; }
.vps-card-container-copy strong { color: var(--text); font-size: 12px; }
.vps-card-container-copy small { color: var(--muted); font-size: 12px; line-height: 1.45; }
.vps-card-container-copy .vps-card-stale, .vps-card-container-copy .vps-card-reason, .vps-card-container-copy .vps-card-error { color: var(--warning); }
.vps-card-container-copy .vps-card-error { color: var(--danger); }
.vps-card-service-link { width:max-content; max-width:100%; border:1px solid var(--line); border-radius:5px; padding:4px 7px; color:var(--accent); background:var(--line); font-size: 12px; text-decoration:none; overflow-wrap:anywhere; }
.vps-card-state { align-self: start; border: 1px solid var(--line); border-radius: 999px; padding: 4px 7px; color: var(--muted-strong); font-size: 12px; white-space: nowrap; }
.vps-card-state[data-running='true'] { border-color: var(--success-line); color: var(--success); }
.vps-card-actions { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 3px; }
.vps-card-actions button, .vps-card-footer button { min-height:40px; border: 1px solid var(--line); border-radius:var(--radius-control); padding: 5px 9px; color: var(--accent); background: var(--line); font: inherit; font-size: 12px; cursor: pointer; }
.vps-card-actions button:disabled { opacity: .45; cursor: not-allowed; }
.vps-card-more { margin: 0; color: var(--muted); font-size: 12px; }
.vps-card-shortcuts { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:6px; margin-top:10px; }
.vps-card-shortcuts button { min-width:0; min-height:40px; border:1px solid var(--line); border-radius:var(--radius-control); padding:6px 8px; color:var(--accent); background:var(--line); font:inherit; font-size: 12px; cursor:pointer; }
.vps-card-shortcuts button:disabled { opacity:.45; cursor:not-allowed; }
.vps-card-ssh-status { margin:6px 0 0; color:var(--success); font-size: 12px; line-height:1.5; overflow-wrap:anywhere; }
.vps-card-footer { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-top: 10px; color: var(--muted); font-size: 12px; overflow-wrap: anywhere; }
.vps-card-footer button { flex: 0 0 auto; }
@media (max-width: 800px) { .vps-card-metrics { grid-template-columns: repeat(3,minmax(0,1fr)); } }
@media (max-width: 560px) { .vps-card-metrics { grid-template-columns: repeat(2,minmax(0,1fr)); } .vps-card-container { grid-template-columns: auto minmax(0,1fr); } .vps-card-state { grid-column: 2; justify-self: start; } .vps-card-shortcuts { grid-template-columns:repeat(2,minmax(0,1fr)); }.vps-card-shortcuts button { min-height:42px; }.vps-card-footer { align-items: flex-start; flex-direction: column; } .vps-card-footer button { width: 100%; min-height: 40px; } }
</style>
