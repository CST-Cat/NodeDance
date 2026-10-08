<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Metric, MetricStatus, MetricsView } from '../metrics-contract'

const props = defineProps<{ view: MetricsView }>()

const FAST_TTL_MS = 15_000
const DISK_TTL_MS = 90_000
const elapsedSinceView = ref(0)
let viewStartedAt = 0
let ticker: ReturnType<typeof setInterval> | undefined

watch(() => props.view, () => {
  viewStartedAt = performance.now()
  elapsedSinceView.value = 0
}, { deep: true, immediate: true })

onMounted(() => {
  ticker = setInterval(() => {
    elapsedSinceView.value = Math.max(0, performance.now() - viewStartedAt)
  }, 250)
})

onBeforeUnmount(() => {
  if (ticker !== undefined) clearInterval(ticker)
})

const leaseRemainingMs = computed(() => {
  if (props.view.nodeStatus !== 'online') return 0
  const serverTimeMs = Date.parse(props.view.serverTime)
  const leaseUntilMs = Date.parse(props.view.leaseValidUntil)
  if (!Number.isFinite(serverTimeMs) || !Number.isFinite(leaseUntilMs)) return 0
  return Math.max(0, leaseUntilMs - serverTimeMs)
})

const nodeOnline = computed(() => props.view.nodeStatus === 'online' && elapsedSinceView.value < leaseRemainingMs.value)
const nodeStatusText = computed(() => nodeOnline.value ? '在线' : '离线')
const leaseReason = computed(() => props.view.nodeStatus === 'online' ? 'lease_expired' : 'lease_inactive')

function metricState<T>(metric: Metric<T>, ttlMs = FAST_TTL_MS): MetricStatus {
  if (metric.status !== 'known' && metric.status !== 'stale') return metric.status
  if (!nodeOnline.value || metric.sampleAgeMillis + elapsedSinceView.value > ttlMs) return 'stale'
  return metric.status
}

function metricReason<T>(metric: Metric<T>, ttlMs = FAST_TTL_MS): string {
  const state = metricState(metric, ttlMs)
  if (state !== 'stale') return metric.reason ?? ''
  if (!nodeOnline.value) return appendReason(metric.reason, leaseReason.value)
  if (metric.sampleAgeMillis + elapsedSinceView.value > ttlMs) return appendReason(metric.reason, 'sample_expired')
  return metric.reason ?? 'stale'
}

function diskState(): MetricStatus {
  const status = props.view.metrics.disk.status
  if (status !== 'known' && status !== 'stale') return status
  if (!nodeOnline.value || props.view.metrics.disk.sampleAgeMillis + elapsedSinceView.value > DISK_TTL_MS) return 'stale'
  return status
}

function diskReason(): string {
  if (diskState() !== 'stale') return props.view.metrics.disk.reason ?? ''
  if (!nodeOnline.value) return appendReason(props.view.metrics.disk.reason, leaseReason.value)
  if (props.view.metrics.disk.sampleAgeMillis + elapsedSinceView.value > DISK_TTL_MS) return appendReason(props.view.metrics.disk.reason, 'sample_expired')
  return props.view.metrics.disk.reason ?? 'stale'
}

function appendReason(existing: string | undefined, added: string): string {
  if (!existing) return added
  if (existing.split('; ').includes(added)) return existing
  return `${existing}; ${added}`
}

function statusLabel(status: MetricStatus): string {
  return ({ known: '正常', unknown: '未知', error: '读取失败', stale: '已过期' })[status]
}

function formatBytes(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value) || value < 0) return '—'
  if (value < 1024) return `${value} B`
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

function formatRate(value: number | null | undefined): string {
  return value == null || !Number.isFinite(value) ? '—' : `${formatBytes(value)}/s`
}

function formatPercent(value: number | null | undefined): string {
  return value == null || !Number.isFinite(value) ? '—' : `${value.toFixed(1)}%`
}

function formatTime(value: string | undefined): string {
  if (!value) return '—'
  const parsed = new Date(value)
  if (!Number.isFinite(parsed.getTime())) return '—'
  return `${parsed.toLocaleString('zh-CN', { timeZone: 'UTC', hour12: false })} UTC`
}

function formatUptime(seconds: number | null | undefined): string {
  if (seconds == null || !Number.isFinite(seconds)) return '—'
  const days = Math.floor(seconds / 86_400)
  const hours = Math.floor((seconds % 86_400) / 3_600)
  const minutes = Math.floor((seconds % 3_600) / 60)
  return days > 0 ? `${days}天 ${hours}小时` : `${hours}小时 ${minutes}分钟`
}

const clockOffsetText = computed(() => {
  const offset = props.view.clockOffsetMs
  if (!Number.isFinite(offset)) return '未知'
  return `${offset > 0 ? '+' : ''}${Math.round(offset)} ms（Core 接收时间 − Agent 采集时间）`
})
</script>

<template>
  <section class="metrics-panel" aria-labelledby="metrics-heading" :data-node-status="nodeOnline ? 'online' : 'offline'">
    <header class="metrics-header">
      <div>
        <p class="metrics-eyebrow">主机监控</p>
        <h2 id="metrics-heading">实时指标</h2>
        <p class="metrics-subtitle">{{ view.metrics.system.hostname.value ?? view.nodeId }} · {{ view.metrics.system.platform.value ?? view.metrics.system.os.value ?? '系统信息未知' }}</p>
      </div>
      <div class="node-state" :class="nodeOnline ? 'is-online' : 'is-offline'" role="status" data-testid="node-status">
        <span class="state-dot" aria-hidden="true"></span>{{ nodeStatusText }}
      </div>
    </header>

    <div class="metrics-grid">
      <article class="metric-card" data-testid="cpu-card">
        <div class="card-heading"><h3>CPU</h3><span class="status-pill" :data-status="metricState(view.metrics.cpu.usagePercent)">{{ statusLabel(metricState(view.metrics.cpu.usagePercent)) }}</span></div>
        <strong class="primary-value" data-testid="cpu-value">{{ formatPercent(view.metrics.cpu.usagePercent.value) }}</strong>
        <p class="metric-caption">{{ view.metrics.cpu.logicalCores.value ?? '—' }} 个逻辑核心</p>
        <p v-if="metricReason(view.metrics.cpu.usagePercent)" class="metric-reason" data-testid="cpu-reason">{{ metricReason(view.metrics.cpu.usagePercent) }}</p>
      </article>

      <article class="metric-card" data-testid="memory-card">
        <div class="card-heading"><h3>内存</h3><span class="status-pill" :data-status="metricState(view.metrics.memory)">{{ statusLabel(metricState(view.metrics.memory)) }}</span></div>
        <strong class="primary-value" data-testid="memory-value">{{ formatPercent(view.metrics.memory.value?.usedPercent) }}</strong>
        <p class="metric-caption">{{ formatBytes(view.metrics.memory.value?.usedBytes) }} / {{ formatBytes(view.metrics.memory.value?.totalBytes) }} 已用</p>
        <p v-if="metricReason(view.metrics.memory)" class="metric-reason" data-testid="memory-reason">{{ metricReason(view.metrics.memory) }}</p>
      </article>

      <article class="metric-card disk-card" data-testid="disk-card">
        <div class="card-heading"><h3>磁盘</h3><span class="status-pill" :data-status="diskState()">{{ statusLabel(diskState()) }}</span></div>
        <p v-if="diskReason()" class="metric-reason">{{ diskReason() }}</p>
        <p v-if="view.metrics.disk.mounts.length === 0" class="metric-caption">暂无挂载点数据</p>
        <div v-for="mount in view.metrics.disk.mounts" :key="`${mount.device}:${mount.mountpoint}`" class="mount-row">
          <div class="mount-title"><strong>{{ mount.mountpoint }}</strong><span class="status-pill small" :data-status="metricState(mount.usage, DISK_TTL_MS)">{{ statusLabel(metricState(mount.usage, DISK_TTL_MS)) }}</span></div>
          <p>{{ formatPercent(mount.usage.value?.usedPercent) }} · {{ formatBytes(mount.usage.value?.usedBytes) }} / {{ formatBytes(mount.usage.value?.totalBytes) }}</p>
          <p class="mount-meta">{{ mount.device }} · {{ mount.filesystem }}</p>
          <p v-if="metricReason(mount.usage, DISK_TTL_MS)" class="metric-reason">{{ metricReason(mount.usage, DISK_TTL_MS) }}</p>
        </div>
      </article>

      <article class="metric-card network-card" data-testid="network-card">
        <div class="card-heading"><h3>网络</h3><span class="status-pill" :data-status="metricState(view.metrics.network.summary)">{{ statusLabel(metricState(view.metrics.network.summary)) }}</span></div>
        <div class="network-pair"><span>接收</span><strong>{{ formatRate(view.metrics.network.summary.value?.receivedBytesPerSecond) }}</strong></div>
        <div class="network-pair"><span>发送</span><strong>{{ formatRate(view.metrics.network.summary.value?.sentBytesPerSecond) }}</strong></div>
        <p v-if="metricReason(view.metrics.network.summary)" class="metric-reason">{{ metricReason(view.metrics.network.summary) }}</p>
        <div v-for="iface in view.metrics.network.interfaces" :key="iface.name" class="interface-row">
          <div><strong>{{ iface.name }}</strong><span>{{ iface.includedInSummary ? '已计入汇总' : iface.summaryReason || '未计入汇总' }}</span></div>
          <span>{{ formatRate(iface.rate.value?.receivedBytesPerSecond) }} ↓</span>
          <span>{{ formatRate(iface.rate.value?.sentBytesPerSecond) }} ↑</span>
        </div>
      </article>
    </div>

    <footer class="metrics-footer">
      <div><span>Agent 采集</span><strong>{{ formatTime(view.collectedAt) }}</strong></div>
      <div><span>Core 接收</span><strong>{{ formatTime(view.receivedAt) }}</strong></div>
      <div><span>时钟偏移</span><strong>{{ clockOffsetText }}</strong></div>
      <div><span>运行时间</span><strong>{{ formatUptime(view.metrics.uptime.value?.seconds) }}</strong></div>
      <div><span>启动 ID</span><strong class="boot-id">{{ view.metrics.uptime.value?.bootId ?? view.bootId ?? '未知' }}</strong></div>
      <div><span>连接代际 / 指标序号</span><strong>{{ view.generation }} / {{ view.sequence }}</strong></div>
      <div><span>系统 / 架构</span><strong>{{ view.metrics.system.os.value ?? '未知' }} · {{ view.metrics.system.architecture.value ?? '未知' }}</strong></div>
      <div><span>内核版本</span><strong>{{ view.metrics.system.kernelVersion.value ?? '未知' }}</strong></div>
    </footer>
  </section>
</template>

<style scoped>
.metrics-panel { --metric-bg: #111b29; --metric-card: #172438; --metric-line: rgba(171, 196, 232, .13); --metric-text: #edf3ff; --metric-muted: #91a2ba; width: 100%; color: var(--metric-text); }
.metrics-header { display: flex; align-items: center; justify-content: space-between; gap: 20px; margin-bottom: 20px; }
.metrics-eyebrow { margin: 0 0 5px; color: #8bb4f4; font: 600 10px/1.4 ui-monospace, monospace; letter-spacing: .14em; text-transform: uppercase; }
.metrics-header h2 { margin: 0; font-size: clamp(22px, 3vw, 29px); letter-spacing: -.04em; }
.metrics-subtitle { margin: 7px 0 0; color: var(--metric-muted); font-size: 12px; }
.node-state { display: inline-flex; flex: 0 0 auto; align-items: center; gap: 8px; border: 1px solid var(--metric-line); border-radius: 999px; padding: 8px 12px; font-size: 12px; }
.node-state.is-online { color: #9ce0b7; background: rgba(62, 155, 100, .11); }
.node-state.is-offline { color: #ffb4aa; background: rgba(184, 77, 72, .12); }
.state-dot { width: 7px; height: 7px; border-radius: 50%; background: currentColor; }
.metrics-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.metric-card { min-width: 0; border: 1px solid var(--metric-line); border-radius: 13px; padding: 16px; background: linear-gradient(145deg, rgba(23, 36, 56, .95), rgba(15, 25, 40, .96)); }
.card-heading { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.card-heading h3 { margin: 0; color: #cdd9ea; font-size: 12px; font-weight: 600; }
.status-pill { border: 1px solid rgba(154, 174, 204, .18); border-radius: 999px; padding: 3px 7px; color: #adc0d9; font-size: 10px; white-space: nowrap; }
.status-pill[data-status='known'] { border-color: rgba(94, 199, 140, .24); color: #9ce0b7; }
.status-pill[data-status='unknown'] { border-color: rgba(168, 179, 196, .2); color: #b5c0d1; }
.status-pill[data-status='error'] { border-color: rgba(255, 129, 116, .28); color: #ffb4aa; }
.status-pill[data-status='stale'] { border-color: rgba(238, 178, 102, .3); color: #f3c27f; }
.status-pill.small { padding: 2px 6px; font-size: 9px; }
.primary-value { display: block; margin-top: 16px; font-size: clamp(27px, 4vw, 36px); font-variant-numeric: tabular-nums; letter-spacing: -.05em; }
.metric-caption { margin: 5px 0 0; color: var(--metric-muted); font-size: 11px; }
.metric-reason { margin: 9px 0 0; color: #efb77b; font: 10px/1.5 ui-monospace, monospace; overflow-wrap: anywhere; }
.disk-card, .network-card { grid-column: span 2; }
.mount-row { margin-top: 13px; border-top: 1px solid var(--metric-line); padding-top: 11px; }
.mount-title { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.mount-title strong { font-size: 12px; }
.mount-row > p { margin: 6px 0 0; color: #c4d2e5; font-size: 11px; font-variant-numeric: tabular-nums; }
.mount-row .mount-meta { color: var(--metric-muted); font-size: 10px; }
.network-pair { display: flex; align-items: baseline; justify-content: space-between; gap: 14px; margin-top: 11px; font-size: 11px; }
.network-pair span { color: var(--metric-muted); }
.network-pair strong { font-variant-numeric: tabular-nums; }
.interface-row { display: grid; grid-template-columns: minmax(100px, 1fr) minmax(100px, auto) minmax(100px, auto); align-items: center; gap: 12px; margin-top: 12px; border-top: 1px solid var(--metric-line); padding-top: 10px; color: #c5d3e6; font-size: 10px; font-variant-numeric: tabular-nums; }
.interface-row > div { display: grid; gap: 3px; }
.interface-row > div span { color: var(--metric-muted); font-size: 9px; }
.metrics-footer { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px 18px; margin-top: 14px; border: 1px solid var(--metric-line); border-radius: 13px; padding: 14px 16px; background: rgba(13, 22, 35, .75); }
.metrics-footer > div { display: grid; min-width: 0; gap: 4px; }
.metrics-footer span { color: var(--metric-muted); font-size: 9px; }
.metrics-footer strong { min-width: 0; color: #c8d5e8; font-size: 10px; font-weight: 500; line-height: 1.5; overflow-wrap: anywhere; }
.boot-id { font-family: ui-monospace, monospace; }
@media (max-width: 560px) {
  .metrics-header { align-items: flex-start; }
  .metrics-grid { grid-template-columns: minmax(0, 1fr); }
  .disk-card, .network-card { grid-column: auto; }
  .interface-row { grid-template-columns: minmax(65px, 1fr) minmax(88px, auto) minmax(88px, auto); gap: 7px; }
  .metrics-footer { grid-template-columns: minmax(0, 1fr); }
}
</style>
