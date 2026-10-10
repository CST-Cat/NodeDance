<script setup lang="ts">
import { computed } from 'vue'
import type { AgentNode, ContainerTask, ContainerTaskAction, DashboardPreference, DockerContainerRecord, DockerInventory } from '../api'
import type { Metric, MetricsView } from '../metrics-contract'

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
  containerAction: [payload: { node: AgentNode; record: DockerContainerRecord; action: ContainerTaskAction }]
}>()

const title = computed(() => props.nodePreference?.alias || props.node.displayName)
const displayContainers = computed(() => {
  const containers = (props.inventory?.containers ?? []).filter((record) => preferenceFor(record)?.visible !== false)
  containers.sort((left, right) => {
    const leftPreference = preferenceFor(left)
    const rightPreference = preferenceFor(right)
    if (leftPreference?.pinned !== rightPreference?.pinned) return leftPreference?.pinned ? -1 : 1
    if ((leftPreference?.sortOrder ?? 0) !== (rightPreference?.sortOrder ?? 0)) {
      return (leftPreference?.sortOrder ?? 0) - (rightPreference?.sortOrder ?? 0)
    }
    return containerTitle(left).localeCompare(containerTitle(right), 'zh-CN')
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

function nodeStatusLabel(): string {
  if (props.pending) return '等待注册'
  return props.online ? '在线' : props.node.status === 'revoked' ? '已撤销' : '离线'
}

function nodeIcon(icon?: string): string {
  const icons: Record<string, string> = {
    server: '▤', globe: '◎', database: '▥', shield: '⬡', terminal: '›_',
    box: '▣', cloud: '☁', folder: '▰', activity: '⌁',
  }
  return icon ? icons[icon] ?? '▤' : '▤'
}

function metricValue<T>(metric: Metric<T> | undefined, value: number | undefined, suffix = '%', ttlMs = 15_000): string {
  if (!metric) return props.online ? '等待样本' : '暂无数据'
  if (value === undefined || !Number.isFinite(value)) {
    if (metric.status === 'error') return '读取失败'
    if (metric.status === 'stale') return '已过期'
    return metric.status === 'known' ? '未知' : props.online ? '等待样本' : '暂无数据'
  }
  const receivedAt = Date.parse(props.metrics?.receivedAt ?? '')
  const elapsed = Number.isFinite(receivedAt) ? Math.max(0, props.currentTime - receivedAt) : 0
  const stale = !props.online || metric.status === 'stale' || metric.sampleAgeMillis + elapsed > ttlMs
  return `${value.toFixed(1)}${suffix}${stale ? ' · 过期' : ''}`
}

function diskSummary(): string {
  const disk = props.metrics?.metrics.disk
  const mount = displayedMount.value
  if (!disk || !mount) return disk?.status === 'error' ? '读取失败' : props.online ? '等待样本' : '暂无数据'
  const metric = mount.usage
  return `${mount.mountpoint} ${metricValue(metric, metric.value?.usedPercent, '%', 90_000)}`
}

function networkSummary(direction: 'receivedBytesPerSecond' | 'sentBytesPerSecond'): string {
  const metric = props.metrics?.metrics.network.summary
  const value = metric?.value?.[direction]
  if (value === undefined || !Number.isFinite(value)) {
    if (metric?.status === 'error') return '读取失败'
    return props.online ? '等待样本' : '暂无数据'
  }
  const receivedAt = Date.parse(props.metrics?.receivedAt ?? '')
  const elapsed = Number.isFinite(receivedAt) ? Math.max(0, props.currentTime - receivedAt) : 0
  const stale = !props.online || !metric || metric.status === 'stale' || metric.sampleAgeMillis + elapsed > 20_000
  return `${formatBytes(value)}/s${stale ? ' · 过期' : ''}`
}

function uptimeSummary(): string {
  const metric = props.metrics?.metrics.uptime
  const seconds = metric?.value?.seconds
  if (seconds === undefined || !Number.isFinite(seconds)) return metric?.status === 'error' ? '读取失败' : '未知'
  const receivedAt = Date.parse(props.metrics?.receivedAt ?? '')
  const elapsed = props.online && Number.isFinite(receivedAt) ? Math.max(0, Math.floor((props.currentTime - receivedAt) / 1000)) : 0
  return `${formatDuration(seconds + elapsed)}${props.online ? '' : ' · 最近数据'}`
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) return '未知'
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
  return days > 0 ? `${days} 天 ${hours} 小时` : hours > 0 ? `${hours} 小时 ${minutes} 分钟` : `${minutes} 分钟`
}

function dockerStatus(): string {
  if (props.inventory?.dockerAvailability === 'unavailable' || props.inventory?.staleReason === 'docker_capability_unavailable') return 'Docker 不可用'
  if (props.inventory?.dockerAvailability === 'available') {
    if (props.dockerStale || props.inventory.dataStale) return '数据过期'
    if (props.inventory.health?.errorKind === 'api_incompatible') return 'Docker API 不兼容'
    if (props.inventory.health?.reason) return 'Engine 状态异常'
    return 'Engine 正常'
  }
  return props.pending ? '等待 Agent 注册' : props.online ? '等待 Docker 状态' : 'Agent 离线'
}

function healthText(record: DockerContainerRecord): string {
  const container = record.container
  if (!container.healthcheckConfigured || container.health === 'none') return '未配置健康检查'
  return ({ healthy: '健康', unhealthy: '异常', starting: '启动中', unknown: '未知' } as Record<string, string>)[container.health] ?? container.health
}

function networkText(record: DockerContainerRecord): string {
  const container = record.container
  if (container.hostNetwork) return 'Host Network（共享宿主机网络）'
  const networks = container.networks ?? []
  if (!networks.length) return '尚无容器网络地址'
  return networks.map((network) => {
    const addresses = [network.ipv4 && `IPv4 ${network.ipv4}`, network.ipv6 && `IPv6 ${network.ipv6}`].filter(Boolean)
    return `${network.name}${addresses.length ? `：${addresses.join(' · ')}` : '：未分配 IP'}`
  }).join('；')
}

function portText(record: DockerContainerRecord): string {
  const container = record.container
  const ports = container.ports ?? []
  if (container.hostNetwork) {
    const prefix = 'Host Network（容器与宿主机共享网络）'
    if (!ports.length) return `${prefix}；未声明容器端口`
    const declared = ports.map((port) => {
      const label = `${port.containerPort}/${port.protocol}`
      if (port.configured.length) return `${label}（配置映射不作为 Host Network 宿主机绑定）`
      if (port.exposed) return `${label}（仅声明，不代表服务实际监听）`
      return `${label}（Host Network 下非 Docker 发布映射）`
    })
    return `${prefix}；${declared.join('，')}`
  }
  if (!ports.length) return '未声明容器端口'
  return ports.map((port) => {
    if (port.published.length) {
      return port.published.map((binding) => {
        const rawHost = binding.ip || '*'
        const formattedHost = rawHost.includes(':') && !rawHost.startsWith('[') ? `[${rawHost}]` : rawHost
        const scope = rawHost === '*' || rawHost === '0.0.0.0' || rawHost === '::'
          ? '（所有宿主机接口）'
          : rawHost === '::1' || /^127(?:\.\d{1,3}){3}$/.test(rawHost)
            ? '（仅宿主机回环）'
            : ''
        return `${formattedHost}:${binding.port} → ${port.containerPort}/${port.protocol}${scope}`
      }).join(', ')
    }
    if (port.configured.length) return `${port.containerPort}/${port.protocol}（配置映射，当前未发布）`
    if (port.exposed) return `${port.containerPort}/${port.protocol}（仅声明）`
    return `${port.containerPort}/${port.protocol}`
  }).join('；')
}

function containerUptime(record: DockerContainerRecord): string {
  const startedAt = record.container.startedAt
  if (!record.container.running || !startedAt) return '未知'
  const started = Date.parse(startedAt)
  if (!Number.isFinite(started) || started > props.currentTime) return '未知'
  return formatDuration(Math.floor((props.currentTime - started) / 1000))
}

function containerCreated(record: DockerContainerRecord): string {
  if (!record.container.createdAt) return '未知'
  const created = Date.parse(record.container.createdAt)
  return Number.isFinite(created) ? new Date(created).toLocaleString() : '未知'
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
  if (task.status === 'queued') return '任务等待 Agent'
  if (task.status === 'running') return `执行中：${task.progress.phase}`
  if (task.status === 'succeeded') return '已由 Docker 状态确认'
  if (task.status === 'unknown' || task.status === 'timed_out') return '结果待确认'
  return task.status === 'failed' ? '执行失败' : '任务已取消'
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
</script>

<template>
  <article class="vps-card" :data-node-id="node.nodeId" :data-online="online" :data-docker-stale="dockerStale">
    <header class="vps-card-header">
      <div class="vps-card-heading">
        <span class="vps-node-icon" aria-hidden="true">{{ nodeIcon(nodePreference?.icon) }}</span>
        <div><span class="eyebrow">VPS<template v-if="nodePreference?.group"> · {{ nodePreference.group }}</template></span><h2>{{ title }}</h2></div>
      </div>
      <span class="vps-card-status" :data-online="online" role="status">{{ nodeStatusLabel() }}</span>
    </header>
    <p class="vps-card-id">{{ node.nodeId }} · {{ node.agentVersion || 'Agent 未连接' }}</p>

    <div class="vps-card-metrics" aria-label="主机监控指标">
      <div><span>CPU</span><strong>{{ metricValue(metrics?.metrics.cpu.usagePercent, metrics?.metrics.cpu.usagePercent.value ?? undefined) }}</strong></div>
      <div><span>内存</span><strong>{{ metricValue(metrics?.metrics.memory, metrics?.metrics.memory.value?.usedPercent) }}</strong></div>
      <div><span>磁盘</span><strong>{{ diskSummary() }}</strong></div>
      <div><span>网络 ↓ / ↑</span><strong>{{ networkSummary('receivedBytesPerSecond') }} / {{ networkSummary('sentBytesPerSecond') }}</strong></div>
      <div><span>运行时间</span><strong>{{ uptimeSummary() }}</strong></div>
    </div>

    <section class="vps-card-docker" :data-available="inventory?.dockerAvailability || 'unknown'" :data-stale="dockerStale">
      <header><strong>Docker 容器</strong><span>{{ inventory?.containers.length ?? '—' }} · {{ dockerStatus() }}</span></header>
      <p v-if="!inventory" class="vps-card-empty">{{ pending ? '等待 Agent 注册。' : online ? '正在读取真实容器状态。' : 'Agent 离线，尚无可确认的容器快照。' }}</p>
      <p v-else-if="inventory.dockerAvailability === 'unavailable' || inventory.staleReason === 'docker_capability_unavailable'" class="vps-card-empty">Engine 或 Agent 的 Docker 功能不可用。{{ inventory.health?.reason || inventory.staleReason || '' }}</p>
      <p v-if="inventory && inventory.containers.length === 0 && inventory.dockerAvailability !== 'unavailable' && inventory.staleReason !== 'docker_capability_unavailable'" class="vps-card-empty">{{ dockerStale ? '历史容器数据已过期。' : '此节点当前没有容器。' }}</p>
      <p v-else-if="inventory && inventory.containers.length > 0 && displayContainers.length === 0" class="vps-card-empty">容器均已隐藏。</p>
      <div v-else-if="inventory && displayContainers.length > 0" class="vps-card-container-list">
        <article v-for="record in displayContainers" :key="record.container.id" class="vps-card-container" :data-stale="dockerStale || record.container.stale">
          <span class="vps-card-container-icon" aria-hidden="true">{{ preferenceFor(record)?.icon || '▣' }}</span>
          <div class="vps-card-container-copy">
            <strong>{{ containerTitle(record) }}</strong>
            <small>{{ record.container.name || record.container.id.slice(0, 12) }} · {{ record.container.image || '未知镜像' }}</small>
            <small>{{ record.container.compose ? `Compose · ${record.container.compose.project}/${record.container.compose.service}` : '独立容器' }} · {{ healthText(record) }}</small>
            <small>{{ networkText(record) }}</small>
            <small>{{ portText(record) }}</small>
            <small>创建：{{ containerCreated(record) }} · 运行：{{ containerUptime(record) }}</small>
            <small v-if="record.container.healthReason || record.container.unavailableReason" class="vps-card-reason">{{ record.container.healthReason || record.container.unavailableReason }}</small>
            <small v-if="dockerStale || record.container.stale" class="vps-card-stale">历史状态已过期</small>
            <small v-if="taskLabel(taskFor(record))" class="vps-card-task" role="status">{{ taskLabel(taskFor(record)) }}</small>
            <small v-if="taskErrors[taskKey(record.container.id)]" class="vps-card-error" role="alert">{{ taskErrors[taskKey(record.container.id)] }}</small>
            <div class="vps-card-actions" aria-label="容器操作">
              <button v-if="record.container.paused" type="button" :disabled="!canOperate(record)" @click="emitAction(record, 'resume')">恢复</button>
              <template v-else-if="record.container.running">
                <button type="button" :disabled="!canOperate(record)" @click="emitAction(record, 'stop')">停止</button>
                <button type="button" :disabled="!canOperate(record)" @click="emitAction(record, 'restart')">重启</button>
              </template>
              <button v-else type="button" :disabled="!canOperate(record)" @click="emitAction(record, 'start')">启动</button>
            </div>
          </div>
          <span class="vps-card-state" :data-running="record.container.running">{{ record.container.state || '未知' }}</span>
        </article>
        <p v-if="inventory.containers.filter((record) => preferenceFor(record)?.visible !== false).length > displayContainers.length" class="vps-card-more">
          另有 {{ inventory.containers.filter((record) => preferenceFor(record)?.visible !== false).length - displayContainers.length }} 个已展示容器
        </p>
        <p v-if="dockerStale" class="vps-card-stale">Agent 离线或租约过期；显示最近已知数据。</p>
      </div>
    </section>
    <footer class="vps-card-footer">
      <span>{{ nodeReason || (dockerStale ? 'Agent 离线，容器状态已过期' : '') }}</span>
      <button type="button" @click="emit('view', node)">查看完整详情</button>
    </footer>
  </article>
</template>

<style scoped>
.vps-card { min-width: 0; border: 1px solid rgba(171,196,232,.13); border-radius: 12px; padding: clamp(14px,2vw,20px); background: linear-gradient(145deg,rgba(23,36,56,.88),rgba(9,17,29,.72)); color: #eaf0fa; }
.vps-card-header { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.vps-card-heading { display: flex; min-width: 0; align-items: center; gap: 9px; }
.vps-card-heading > div { min-width: 0; }
.vps-card-heading h2 { margin: 4px 0 0; font-size: 16px; overflow-wrap: anywhere; }
.vps-node-icon { display: grid; width: 34px; height: 34px; flex: 0 0 auto; place-items: center; border: 1px solid rgba(141,201,255,.2); border-radius: 9px; color: #a9d5ff; background: rgba(62,119,170,.16); font-size: 17px; }
.vps-card-status { border: 1px solid rgba(171,196,232,.15); border-radius: 999px; padding: 6px 10px; color: #ffb4aa; font-size: 10px; white-space: nowrap; }
.vps-card-status[data-online='true'] { color: #9ce0b7; }
.vps-card-id { margin: 6px 0 12px; color: #8193aa; font: 9px/1.5 ui-monospace,monospace; overflow-wrap: anywhere; }
.vps-card-metrics { display: grid; grid-template-columns: repeat(5,minmax(0,1fr)); gap: 7px; }
.vps-card-metrics > div { display: grid; min-width: 0; gap: 5px; border: 1px solid rgba(171,196,232,.1); border-radius: 7px; padding: 8px; }
.vps-card-metrics span { color: #91a2ba; font-size: 9px; }
.vps-card-metrics strong { color: #eaf0fa; font-size: 10px; line-height: 1.4; overflow-wrap: anywhere; }
.vps-card-docker { margin-top: 12px; border: 1px solid rgba(171,196,232,.1); border-radius: 8px; padding: 10px; background: rgba(18,29,45,.54); }
.vps-card-docker > header { display: flex; align-items: center; justify-content: space-between; gap: 10px; color: #cbd8ea; font-size: 10px; }
.vps-card-docker > header span { color: #91a2ba; font: 9px ui-monospace,monospace; text-align: right; }
.vps-card-empty { margin: 8px 0 0; color: #9aabc1; font-size: 10px; line-height: 1.5; overflow-wrap: anywhere; }
.vps-card-container-list { display: grid; gap: 7px; margin-top: 8px; }
.vps-card-container { display: grid; grid-template-columns: auto minmax(0,1fr) auto; align-items: start; gap: 7px; border: 1px solid rgba(171,196,232,.08); border-radius: 7px; padding: 8px; }
.vps-card-container[data-stale='true'] { border-color: rgba(255,176,129,.22); }
.vps-card-container-icon { display: grid; width: 24px; height: 24px; place-items: center; border-radius: 6px; color: #c5e4ff; background: rgba(105,184,255,.12); font-size: 12px; }
.vps-card-container-copy { display: grid; min-width: 0; gap: 4px; }
.vps-card-container-copy strong, .vps-card-container-copy small { overflow-wrap: anywhere; }
.vps-card-container-copy strong { color: #eaf0fa; font-size: 10px; }
.vps-card-container-copy small { color: #91a2ba; font-size: 8px; line-height: 1.45; }
.vps-card-container-copy .vps-card-stale, .vps-card-container-copy .vps-card-reason, .vps-card-container-copy .vps-card-error { color: #efc58d; }
.vps-card-container-copy .vps-card-error { color: #ffc1b8; }
.vps-card-state { align-self: start; border: 1px solid rgba(171,196,232,.16); border-radius: 999px; padding: 4px 7px; color: #aebbd0; font-size: 8px; white-space: nowrap; }
.vps-card-state[data-running='true'] { border-color: rgba(121,214,156,.24); color: #9ce0b7; }
.vps-card-actions { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 3px; }
.vps-card-actions button, .vps-card-footer button { min-height: 30px; border: 1px solid rgba(141,201,255,.25); border-radius: 6px; padding: 5px 9px; color: #cce6ff; background: rgba(62,119,170,.16); font: inherit; font-size: 9px; cursor: pointer; }
.vps-card-actions button:disabled { opacity: .45; cursor: not-allowed; }
.vps-card-more { margin: 0; color: #91a2ba; font-size: 9px; }
.vps-card-footer { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-top: 10px; color: #91a2ba; font-size: 9px; overflow-wrap: anywhere; }
.vps-card-footer button { flex: 0 0 auto; }
@media (max-width: 800px) { .vps-card-metrics { grid-template-columns: repeat(3,minmax(0,1fr)); } }
@media (max-width: 560px) { .vps-card-metrics { grid-template-columns: repeat(2,minmax(0,1fr)); } .vps-card-container { grid-template-columns: auto minmax(0,1fr); } .vps-card-state { grid-column: 2; justify-self: start; } .vps-card-footer { align-items: flex-start; flex-direction: column; } .vps-card-footer button { width: 100%; min-height: 40px; } }
</style>
