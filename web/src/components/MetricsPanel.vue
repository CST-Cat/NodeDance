<script setup lang="ts">
import { t, locale } from "../i18n"
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Metric, MetricStatus, MetricsView } from '../metrics-contract'

const props = defineProps<{ view: MetricsView }>()

const FAST_TTL_MS = computed(() => (props.view.metrics.host?.fastIntervalSeconds ?? 5) * 3_000)
const DISK_TTL_MS = computed(() => (props.view.metrics.host?.diskIntervalSeconds ?? 30) * 3_000)
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
const nodeStatusText = computed(() => nodeOnline.value ? t('m.b9086662b1df') : t('m.be1b4f3c6c1c'))
const leaseReason = computed(() => props.view.nodeStatus === 'online' ? 'lease_expired' : 'lease_inactive')

function metricState<T>(metric: Metric<T>, ttlMs = FAST_TTL_MS.value): MetricStatus {
  if (metric.status !== 'known' && metric.status !== 'stale') return metric.status
  if (!nodeOnline.value || metric.sampleAgeMillis + elapsedSinceView.value > ttlMs) return 'stale'
  return metric.status
}

function metricReason<T>(metric: Metric<T>, ttlMs = FAST_TTL_MS.value): string {
  const state = metricState(metric, ttlMs)
  if (state !== 'stale') return metric.reason ?? ''
  if (!nodeOnline.value) return appendReason(metric.reason, leaseReason.value)
  if (metric.sampleAgeMillis + elapsedSinceView.value > ttlMs) return appendReason(metric.reason, 'sample_expired')
  return metric.reason ?? 'stale'
}

function diskState(): MetricStatus {
  const status = props.view.metrics.disk.status
  if (status !== 'known' && status !== 'stale') return status
  if (!nodeOnline.value || props.view.metrics.disk.sampleAgeMillis + elapsedSinceView.value > DISK_TTL_MS.value) return 'stale'
  return status
}

function diskReason(): string {
  if (diskState() !== 'stale') return props.view.metrics.disk.reason ?? ''
  if (!nodeOnline.value) return appendReason(props.view.metrics.disk.reason, leaseReason.value)
  if (props.view.metrics.disk.sampleAgeMillis + elapsedSinceView.value > DISK_TTL_MS.value) return appendReason(props.view.metrics.disk.reason, 'sample_expired')
  return props.view.metrics.disk.reason ?? 'stale'
}

function appendReason(existing: string | undefined, added: string): string {
  if (!existing) return added
  if (existing.split('; ').includes(added)) return existing
  return `${existing}; ${added}`
}

function statusLabel(status: MetricStatus): string {
  return ({ known: t('m.296de0e31f8c'), unknown: t('m.4d8c1c5b4283'), error: t('m.6aa27f14bff1'), stale: t('m.2fe0e3339ac4') })[status]
}

function formatBytes(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value) || value < 0) return '—'
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

function formatRate(value: number | null | undefined): string {
  return value == null || !Number.isFinite(value) || value < 0 ? '—' : `${formatBytes(value)}/s`
}

function formatPercent(value: number | null | undefined): string {
  return value == null || !Number.isFinite(value) ? '—' : `${value.toFixed(1)}%`
}

function formatTime(value: string | undefined): string {
  if (!value) return '—'
  const parsed = new Date(value)
  if (!Number.isFinite(parsed.getTime())) return '—'
  return `${parsed.toLocaleString(locale.value, { timeZone: 'UTC', hour12: false })} UTC`
}

function formatUptime(seconds: number | null | undefined): string {
  if (seconds == null || !Number.isFinite(seconds)) return '—'
  const days = Math.floor(seconds / 86_400)
  const hours = Math.floor((seconds % 86_400) / 3_600)
  const minutes = Math.floor((seconds % 3_600) / 60)
  return days > 0 ? t('m.2a9d387d65dd', {"0": (days), "1": (hours)}) : t('m.7077e2ea25bd', {"0": (hours), "1": (minutes)})
}

const primaryInterfaces = computed(() => props.view.metrics.network.interfaces.filter(iface => iface.includedInSummary))

const clockOffsetText = computed(() => {
  const offset = props.view.clockOffsetMs
  if (!Number.isFinite(offset)) return t('m.4d8c1c5b4283')
  return t('m.8d59cb484bc3', {"0": (offset > 0 ? '+' : ''), "1": (Math.round(offset))})
})
</script>

<template>
  <section class="metrics-panel" aria-labelledby="metrics-heading" :data-node-status="nodeOnline ? 'online' : 'offline'">
    <header class="metrics-header">
      <div>
        <p class="metrics-eyebrow">{{ t('m.c797a5342dcf') }}</p>
        <h2 id="metrics-heading">{{ t('m.c7d6c36fa0e6') }}</h2>
        <p class="metrics-subtitle">{{ view.metrics.system.hostname.value ?? view.nodeId }} · {{ view.metrics.system.platform.value ?? view.metrics.system.os.value ?? t('m.c6c03f12a7c7') }}</p>
      </div>
      <div class="node-state" :class="nodeOnline ? 'is-online' : 'is-offline'" role="status" data-testid="node-status">
        <span class="state-dot" aria-hidden="true"></span>{{ nodeStatusText }}
      </div>
    </header>

    <div class="metrics-grid">
      <article class="metric-card" data-testid="cpu-card">
        <div class="card-heading"><h3>CPU</h3><span class="status-pill" :data-status="metricState(view.metrics.cpu.usagePercent)">{{ statusLabel(metricState(view.metrics.cpu.usagePercent)) }}</span></div>
        <strong class="primary-value" data-testid="cpu-value">{{ formatPercent(view.metrics.cpu.usagePercent.value) }}</strong>
        <p class="metric-caption">{{ view.metrics.cpu.logicalCores.value ?? '—' }}{{ t('m.4a5d806c0374') }}</p>
        <p v-if="metricReason(view.metrics.cpu.usagePercent)" class="metric-reason" data-testid="cpu-reason">{{ metricReason(view.metrics.cpu.usagePercent) }}</p>
      </article>

      <article class="metric-card" data-testid="memory-card">
        <div class="card-heading"><h3>{{ t('m.7d8f8c37ec78') }}</h3><span class="status-pill" :data-status="metricState(view.metrics.memory)">{{ statusLabel(metricState(view.metrics.memory)) }}</span></div>
        <strong class="primary-value" data-testid="memory-value">{{ formatPercent(view.metrics.memory.value?.usedPercent) }}</strong>
        <p class="metric-caption">{{ formatBytes(view.metrics.memory.value?.usedBytes) }} / {{ formatBytes(view.metrics.memory.value?.totalBytes) }}{{ t('m.937683da32c1') }}</p>
        <p v-if="metricReason(view.metrics.memory)" class="metric-reason" data-testid="memory-reason">{{ metricReason(view.metrics.memory) }}</p>
      </article>

      <article class="metric-card disk-card" data-testid="disk-card">
        <div class="card-heading"><h3>{{ t('m.de7b72a3f852') }}</h3><span class="status-pill" :data-status="diskState()">{{ statusLabel(diskState()) }}</span></div>
        <p v-if="diskReason()" class="metric-reason">{{ diskReason() }}</p>
        <p v-if="view.metrics.disk.detailsTruncated" class="metric-reason" data-testid="disk-truncation">{{ t('m.7172e22a2c28') }}{{ view.metrics.disk.truncationReason }}</p>
        <p v-if="view.metrics.disk.mounts.length === 0" class="metric-caption">{{ t('m.1d0afbd4d7a2') }}</p>
        <div v-for="mount in view.metrics.disk.mounts" :key="`${mount.device}:${mount.mountpoint}`" class="mount-row">
          <div class="mount-title"><strong>{{ mount.mountpoint }}</strong><span class="status-pill small" :data-status="metricState(mount.usage, DISK_TTL_MS)">{{ statusLabel(metricState(mount.usage, DISK_TTL_MS)) }}</span></div>
          <p>{{ formatPercent(mount.usage.value?.usedPercent) }} · {{ formatBytes(mount.usage.value?.usedBytes) }} / {{ formatBytes(mount.usage.value?.totalBytes) }}</p>
          <p class="mount-meta">{{ mount.device }} · {{ mount.filesystem }}</p>
          <p v-if="metricReason(mount.usage, DISK_TTL_MS)" class="metric-reason">{{ metricReason(mount.usage, DISK_TTL_MS) }}</p>
        </div>
      </article>

      <article class="metric-card network-card" data-testid="network-card">
        <div class="card-heading"><h3>{{ t('m.97b31b5d63f5') }}</h3><span class="status-pill" :data-status="metricState(view.metrics.network.summary)">{{ statusLabel(metricState(view.metrics.network.summary)) }}</span></div>
        <div class="network-pair"><span>{{ t('m.6e684586884e') }}</span><strong>{{ formatRate(view.metrics.network.summary.value?.receivedBytesPerSecond) }}</strong></div>
        <div class="network-pair"><span>{{ t('m.edecf0ae6e51') }}</span><strong>{{ formatRate(view.metrics.network.summary.value?.sentBytesPerSecond) }}</strong></div>
        <p v-if="metricReason(view.metrics.network.summary)" class="metric-reason" data-testid="network-reason">{{ metricReason(view.metrics.network.summary) }}</p>
        <p v-if="view.metrics.network.detailsTruncated" class="metric-reason" data-testid="network-truncation">{{ t('m.093a185c2e51') }}{{ view.metrics.network.truncationReason }}</p>
        <div v-for="iface in primaryInterfaces" :key="iface.name" class="interface-row" data-testid="network-interface" :data-interface-name="iface.name">
          <div class="interface-title">
            <strong>{{ iface.name }}</strong>
            <span class="status-pill small" :data-status="metricState(iface.rate)" :data-testid="`interface-rate-status-${iface.name}`">{{ statusLabel(metricState(iface.rate)) }}</span>
          </div>
          <div class="interface-description"><span>{{ t('m.09f99341ca07') }}</span></div>
          <span>{{ formatRate(iface.rate.value?.receivedBytesPerSecond) }} ↓</span>
          <span>{{ formatRate(iface.rate.value?.sentBytesPerSecond) }} ↑</span>
          <p v-if="metricReason(iface.rate)" class="metric-reason interface-reason" :data-testid="`interface-rate-reason-${iface.name}`">{{ metricReason(iface.rate) }}</p>
        </div>
      </article>
    </div>

    <div v-if="view.metrics.host" class="metrics-grid host-metrics">
      <article class="metric-card"><div class="card-heading"><h3>Swap</h3><span class="status-pill" :data-status="metricState(view.metrics.host.swap)">{{ statusLabel(metricState(view.metrics.host.swap)) }}</span></div><strong class="primary-value">{{ formatPercent(view.metrics.host.swap.value?.usedPercent) }}</strong><p class="metric-caption">{{ formatBytes(view.metrics.host.swap.value?.usedBytes) }} / {{ formatBytes(view.metrics.host.swap.value?.totalBytes) }}</p><p class="metric-reason">{{ metricReason(view.metrics.host.swap) }}</p></article>
      <article class="metric-card"><div class="card-heading"><h3>{{ t('host.load') }}</h3><span class="status-pill" :data-status="metricState(view.metrics.host.load)">{{ statusLabel(metricState(view.metrics.host.load)) }}</span></div><strong class="primary-value">{{ view.metrics.host.load.value?.one.toFixed(2) ?? '—' }}</strong><p class="metric-caption">5m: {{ view.metrics.host.load.value?.five.toFixed(2) ?? '—' }} · 15m: {{ view.metrics.host.load.value?.fifteen.toFixed(2) ?? '—' }}</p><p class="metric-reason">{{ metricReason(view.metrics.host.load) }}</p></article>
      <article class="metric-card"><div class="card-heading"><h3>{{ t('host.processes') }}</h3><span class="status-pill" :data-status="metricState(view.metrics.host.processes)">{{ statusLabel(metricState(view.metrics.host.processes)) }}</span></div><strong class="primary-value">{{ view.metrics.host.processes.value ?? '—' }}</strong><p class="metric-caption">TCP: {{ view.metrics.host.sockets.value?.tcp ?? '—' }} · UDP: {{ view.metrics.host.sockets.value?.udp ?? '—' }} · {{ statusLabel(metricState(view.metrics.host.sockets)) }}</p><p class="metric-reason">{{ metricReason(view.metrics.host.processes) }}</p></article>
      <article class="metric-card"><div class="card-heading"><h3>{{ t('host.traffic') }}</h3><span class="status-pill" :data-status="metricState(view.metrics.host.traffic)">{{ statusLabel(metricState(view.metrics.host.traffic)) }}</span></div><p class="network-pair">↓ {{ formatBytes(view.metrics.host.traffic.value?.periodReceivedBytes) }} · ↑ {{ formatBytes(view.metrics.host.traffic.value?.periodSentBytes) }}</p><p class="metric-caption">{{ t('host.period') }}: {{ formatTime(view.metrics.host.traffic.value?.periodStart ?? '') }}</p><p class="metric-caption">{{ t('host.total') }}: ↓ {{ formatBytes(view.metrics.host.traffic.value?.receivedBytes) }} · ↑ {{ formatBytes(view.metrics.host.traffic.value?.sentBytes) }}</p><p v-if="view.metrics.host.traffic.reason==='partial_coverage'" class="metric-reason">{{ t('host.partialTraffic') }}</p><p v-else class="metric-reason">{{ metricReason(view.metrics.host.traffic) }}</p></article>
      <article class="metric-card"><div class="card-heading"><h3>GPU</h3><span class="status-pill" :data-status="metricState(view.metrics.host.gpus,90000)">{{ statusLabel(metricState(view.metrics.host.gpus,90000)) }}</span></div><div v-for="gpu in view.metrics.host.gpus.value" :key="gpu.id" class="mount-row"><strong>{{ gpu.name }}</strong><p>{{ formatPercent(gpu.utilization) }} · {{ formatBytes(gpu.memoryUsedBytes) }} / {{ formatBytes(gpu.memoryTotalBytes) }} · {{ gpu.temperatureCelsius ?? '—' }} °C</p></div><p class="metric-reason">{{ metricReason(view.metrics.host.gpus,90000) }}</p></article>
      <article class="metric-card"><h3>{{ t('host.hardware') }}</h3><p>{{ view.metrics.host.cpu.value?.model ?? '—' }} · {{ view.metrics.host.cpu.value?.physicalCores ?? '—' }} {{ t('host.physicalCores') }}</p><p>{{ view.metrics.host.virtualization.value ?? '—' }}</p><p class="metric-caption">{{ t('host.cache') }}: {{ formatBytes(view.metrics.host.memory.value?.cachedBytes) }} · {{ t('host.buffers') }}: {{ formatBytes(view.metrics.host.memory.value?.bufferedBytes) }} · {{ view.metrics.host.memoryMode }}</p><p v-for="address in view.metrics.host.addresses.value" :key="address.interface+address.address" class="metric-caption">{{ address.interface }} · {{ address.address }}</p><p class="metric-caption">IPv4: {{ view.metrics.host.publicIpv4.value ?? '—' }} · {{ statusLabel(metricState(view.metrics.host.publicIpv4,900000)) }}</p><p class="metric-caption">IPv6: {{ view.metrics.host.publicIpv6.value ?? '—' }} · {{ statusLabel(metricState(view.metrics.host.publicIpv6,900000)) }}</p></article>
    </div>

    <footer class="metrics-footer">
      <div><span>{{ t('m.1ed65d95a3d5') }}</span><strong>{{ formatTime(view.collectedAt) }}</strong></div>
      <div><span>{{ t('m.91a166714259') }}</span><strong>{{ formatTime(view.receivedAt) }}</strong></div>
      <div><span>{{ t('m.96ded394f11b') }}</span><strong data-testid="clock-offset">{{ clockOffsetText }}</strong></div>
      <div><span>{{ t('m.5986e9d976e0') }}</span><strong>{{ formatUptime(view.metrics.uptime.value?.seconds) }}</strong></div>
      <div><span>{{ t('m.aeae0144dae1') }}</span><strong class="boot-id" data-testid="boot-id">{{ view.metrics.uptime.value?.bootId ?? view.bootId ?? t('m.4d8c1c5b4283') }}</strong></div>
      <div><span>{{ t('m.f43eb988fa4a') }}</span><strong data-testid="sample-identity" :data-generation="view.generation" :data-active-generation="view.activeGeneration" :data-sequence="view.sequence">{{ view.generation }} / {{ view.sequence }}</strong></div>
      <div><span>{{ t('m.caea742a78f8') }}</span><strong>{{ view.metrics.system.os.value ?? t('m.4d8c1c5b4283') }} · {{ view.metrics.system.architecture.value ?? t('m.4d8c1c5b4283') }}</strong></div>
      <div><span>{{ t('m.44aadacb01fe') }}</span><strong>{{ view.metrics.system.kernelVersion.value ?? t('m.4d8c1c5b4283') }}</strong></div>
    </footer>
  </section>
</template>

<style scoped>
.host-metrics {margin-top:0;}
.metrics-panel { --metric-bg: var(--surface-soft); --metric-card: var(--surface-soft); --metric-line: var(--line); --metric-text: var(--text); --metric-muted: var(--muted); width: 100%; color: var(--metric-text); }
.metrics-header { display: flex; align-items: center; justify-content: space-between; gap: 20px; margin-bottom: 20px; }
.metrics-eyebrow { margin: 0 0 5px; color: var(--accent); font: 500 10px/1.4 ui-monospace, monospace; letter-spacing: .14em; text-transform: uppercase; }
.metrics-header h2 { margin: 0; font-size: clamp(22px, 3vw, 29px); letter-spacing: -.04em; }
.metrics-subtitle { margin: 7px 0 0; color: var(--metric-muted); font-size: 12px; }
.node-state { display: inline-flex; flex: 0 0 auto; align-items: center; gap: 8px; border: 1px solid var(--metric-line); border-radius: 999px; padding: 8px 12px; font-size: 12px; }
.node-state.is-online { color: var(--success); background: var(--success-soft); }
.node-state.is-offline { color: var(--danger); background: var(--danger-soft); }
.state-dot { width: 7px; height: 7px; border-radius: 50%; background: currentColor; }
.metrics-grid {display: grid; grid-template-columns: repeat(2, minmax(0, 1fr));  gap:0 28px;}
.metric-card {min-width: 0;     border:0;border-radius:0;background:transparent;box-shadow:none;border-top:1px solid var(--line);padding:20px 0;}
.card-heading { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.card-heading h3 { margin: 0; color: var(--text); font-size: 12px; font-weight:500; }
.status-pill { border: 1px solid var(--line); border-radius: 999px; padding: 3px 7px; color: var(--muted-strong); font-size: 12px; white-space: nowrap; }
.status-pill[data-status='known'] { border-color: var(--success-line); color: var(--success); }
.status-pill[data-status='unknown'] { border-color: var(--line); color: var(--muted-strong); }
.status-pill[data-status='error'] { border-color: var(--danger-line); color: var(--danger); }
.status-pill[data-status='stale'] { border-color: var(--danger-line); color: var(--warning); }
.status-pill.small { padding: 2px 6px; font-size: 12px; }
.primary-value { display: block; margin-top: 16px; font-size: clamp(27px, 4vw, 36px); font-variant-numeric: tabular-nums; letter-spacing: -.05em; }
.metric-caption { margin: 5px 0 0; color: var(--metric-muted); font-size: 12px; }
.metric-reason { margin: 9px 0 0; color: var(--warning); font: 11px/1.5 ui-monospace, monospace; overflow-wrap: anywhere; }
.disk-card, .network-card { grid-column: span 2; }
.mount-row { margin-top: 13px; border-top: 1px solid var(--metric-line); padding-top: 11px; }
.mount-title { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.mount-title strong { font-size: 12px; }
.mount-row > p { margin: 6px 0 0; color: var(--text); font-size: 12px; font-variant-numeric: tabular-nums; }
.mount-row .mount-meta { color: var(--metric-muted); font-size: 12px; }
.network-pair { display: flex; align-items: baseline; justify-content: space-between; gap: 14px; margin-top: 11px; font-size: 12px; }
.network-pair span { color: var(--metric-muted); }
.network-pair strong { font-variant-numeric: tabular-nums; }
.interface-row { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; align-items: center; gap: 12px; margin-top: 12px; border-top: 1px solid var(--metric-line); padding-top: 10px; color: var(--text); font-size: 12px; font-variant-numeric: tabular-nums; }
.interface-title { grid-column:1 / -1; display:flex; align-items:center; justify-content:space-between; gap:10px; min-width:0; }
.interface-title strong { min-width:0; overflow-wrap:anywhere; }
.interface-title .status-pill { flex-shrink:0; }
.interface-description { min-width:0; overflow-wrap:anywhere; }
.interface-reason { grid-column:1 / -1; margin:0; }
.interface-row > span { white-space:nowrap; text-align:right; }
.interface-description span { color: var(--metric-muted); font-size: 12px; }
.metrics-footer { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px 18px; margin-top:14px; border-top:1px solid var(--metric-line); padding:16px 0; }
.metrics-footer > div { display: grid; min-width: 0; gap: 4px; }
.metrics-footer span { color: var(--metric-muted); font-size: 12px; }
.metrics-footer strong { min-width: 0; color: var(--text); font-size: 12px; font-weight: 500; line-height: 1.5; overflow-wrap: anywhere; }
.boot-id { font-family: ui-monospace, monospace; }
@media (max-width: 560px) {
  .metrics-header { align-items: flex-start; }
  .metrics-grid { grid-template-columns: minmax(0, 1fr); }
  .disk-card, .network-card { grid-column: auto; }
  .interface-row { grid-template-columns: minmax(0, 1fr) auto auto; gap: 7px; }
  .metrics-footer { grid-template-columns: minmax(0, 1fr); }
}
</style>
