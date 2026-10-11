<script setup lang="ts">
import { t, display, locale } from "../i18n"
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts'
import type { MetricHistory } from '../api'

const props = defineProps<{
  history: MetricHistory | null
  loading: boolean
  error: string
  nodeOnline: boolean
}>()

const host = ref<HTMLElement>()
const selectedKey = ref('cpu.usage_percent')
const chartError = ref('')
let chart: echarts.ECharts | undefined
let observer: ResizeObserver | undefined

const availableKeys = computed(() => {
  const labels = new Map<string, string>([
    ['cpu.usage_percent', t('m.1c82e5547854')],
    ['memory.used_percent', t('m.ace212ad8fd3')],
    ['memory.used_bytes', t('m.688cab237917')],
    ['network.received_bytes_per_second', t('m.9cf3b70addc3')],
    ['network.sent_bytes_per_second', t('m.81c8be973451')],
  ])
  for (const series of props.history?.series ?? []) {
    if (series.key.startsWith('disk:')) {
      const suffix = series.key.endsWith(':used_percent') ? 'used_percent' : 'used_bytes'
      const mount = series.key.slice('disk:'.length, -(`:${suffix}`.length))
      labels.set(series.key, t('m.c47bd176f3ca', {"0": (mount), "1": (suffix === 'used_percent' ? t('m.9746ae777abb') : t('m.666f1c71e1e0'))}))
    }
  }
  return [...labels.entries()].map(([key, label]) => ({ key, label }))
})

const selectedLabel = computed(() => availableKeys.value.find((item) => item.key === selectedKey.value)?.label ?? selectedKey.value)
const sampleCount = computed(() => props.history?.series.find((series) => series.key === selectedKey.value)?.points.length ?? 0)

function chartOptions(): echarts.EChartsOption {
  const history = props.history
  const metric = history?.series.find((item) => item.key === selectedKey.value)
  const from = history ? Date.parse(history.from) : Number.NaN
  const to = history ? Date.parse(history.to) : Number.NaN
  const step = history?.resolution === 'hour' ? 3_600_000 : 60_000
  const first = Number.isFinite(from) ? Math.ceil(from / step) * step : 0
  const last = Number.isFinite(to) ? Math.ceil(to / step) * step : 0
  const values = new Map((metric?.points ?? []).map((point) => [Date.parse(point.bucketAt), point.value]))
  const data: Array<[number, number | null]> = []
  for (let at = first; at < last; at += step) {
    const value = values.get(at)
    data.push([at, value !== undefined && Number.isFinite(value) ? value : null])
  }
  const diskPercent = selectedKey.value.startsWith('disk:') && selectedKey.value.endsWith(':used_percent')
  const percent = selectedKey.value === 'cpu.usage_percent' || selectedKey.value === 'memory.used_percent' || diskPercent
  const byteRate = selectedKey.value.startsWith('network.')
  const bytes = selectedKey.value.endsWith('_bytes')
  let divisor = 1
  let unit = percent ? '%' : ''
  if (byteRate) { divisor = 1024 * 1024; unit = 'MiB/s' }
  else if (bytes) { divisor = 1024 ** 3; unit = 'GiB' }
  const normalized = data.map(([at, value]) => [at, value === null ? null : value / divisor] as [number, number | null])
  return {
    animation: false,
    grid: { left: 54, right: 20, top: 24, bottom: 38 },
    tooltip: { trigger: 'axis', valueFormatter: (value) => value == null ? t('m.63ef182b7603') : `${Number(value).toFixed(2)} ${unit}` },
    xAxis: { type: 'time', axisLabel: { color: '#91a2ba', formatter: (value: number) => new Date(value).toLocaleString(locale.value, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) }, axisPointer: { label: { formatter: (params: { value: string | number | Date }) => new Date(params.value).toLocaleString(locale.value) } }, axisLine: { lineStyle: { color: '#34445b' } } },
    yAxis: { type: 'value', scale: !percent, name: unit, nameTextStyle: { color: '#91a2ba' }, axisLabel: { color: '#91a2ba' }, splitLine: { lineStyle: { color: 'rgba(145,162,186,.12)' } } },
    series: [{
      name: selectedLabel.value,
      type: 'line',
      showSymbol: false,
      connectNulls: false,
      sampling: 'lttb',
      data: normalized,
      lineStyle: { width: 2, color: '#69b8ff' },
      itemStyle: { color: '#69b8ff' },
      areaStyle: { color: 'rgba(105,184,255,.08)' },
    }],
  }
}

function render() {
  if (!host.value) return
  try {
    chartError.value = ''
    chart ??= echarts.init(host.value, undefined, { renderer: 'canvas' })
    chart.setOption(chartOptions(), true)
  } catch {
    chartError.value = t('m.3ed77cde9795')
  }
}

watch(() => [props.history, selectedKey.value, locale.value], render, { deep: true })
watch(availableKeys, (items) => {
  if (!items.some((item) => item.key === selectedKey.value)) selectedKey.value = items[0]?.key ?? 'cpu.usage_percent'
})

onMounted(() => {
  render()
  if (host.value) {
    observer = new ResizeObserver(() => chart?.resize())
    observer.observe(host.value)
  }
})

onBeforeUnmount(() => {
  observer?.disconnect()
  chart?.dispose()
  chart = undefined
})
</script>

<template>
  <section class="history-panel" aria-labelledby="history-title" data-testid="history-panel">
    <header class="history-heading">
      <div>
        <span class="eyebrow">{{ t('label.aacdab5b45f0') }}</span>
        <h2 id="history-title">{{ t('m.bfb41404e88b') }}</h2>
        <p v-if="history">{{ new Date(history.from).toLocaleString(locale) }} — {{ new Date(history.to).toLocaleString(locale) }} · {{ history.resolution === 'minute' ? t('m.bd957bc497aa') : t('m.3b6fefc50feb') }}{{ t('m.a4768faecb38') }}</p>
      </div>
      <label class="history-metric-select">{{ t('m.b87e67c7dae0') }}<select v-model="selectedKey" :aria-label="t('m.4a2f28a82624')">
          <option v-for="item in availableKeys" :key="item.key" :value="item.key">{{ item.label }}</option>
        </select>
      </label>
    </header>
    <p v-if="!nodeOnline" class="history-stale" role="status">{{ t('m.b116276abf68') }}</p>
    <p v-else class="history-note">{{ t('m.7a9f26412898') }}</p>
    <div v-if="loading" class="history-state" role="status">{{ t('m.a980a5f91841') }}</div>
    <div v-else-if="error" class="history-state history-error" role="alert">{{ display(error) }}</div>
    <div v-else-if="chartError" class="history-state history-error" role="alert">{{ display(chartError) }}</div>
    <div v-else ref="host" class="history-chart" data-testid="history-chart" :data-sample-count="sampleCount" :aria-label="t('m.ea8e1f947456', {'0': (selectedLabel)})"></div>
    <p v-if="!loading && !error && sampleCount === 0" class="history-empty">{{ t('m.f3de6936eb3b') }}</p>
  </section>
</template>

<style scoped>
.history-panel { margin-top: 18px; border: 1px solid rgba(171, 196, 232, .13); border-radius: 12px; padding: clamp(14px, 2vw, 20px); background: rgba(9, 17, 29, .46); color: #eaf0fa; }
.history-heading { display: flex; align-items: flex-end; justify-content: space-between; gap: 14px; }
.history-heading h2 { margin: 4px 0 0; font-size: 17px; }
.history-heading p, .history-note, .history-stale, .history-empty { color: #9aabc1; font-size: 11px; line-height: 1.6; }
.history-metric-select { display: grid; gap: 5px; color: #9aabc1; font-size: 10px; }
.history-metric-select select { min-height: 36px; border: 1px solid rgba(171, 196, 232, .18); border-radius: 7px; padding: 5px 10px; color: #eaf0fa; background: #111b29; font: inherit; }
.history-stale { border-left: 2px solid #efb77b; padding-left: 10px; color: #efc58d; }
.history-chart { width: 100%; height: min(360px, 55vh); min-height: 250px; }
.history-state { display: grid; min-height: 250px; place-items: center; color: #9aabc1; font-size: 12px; }
.history-error { color: #ffc1b8; }
.history-empty { margin: -22px 0 12px; text-align: center; pointer-events: none; }
@media (max-width: 480px) { .history-heading { align-items: flex-start; flex-direction: column; } .history-metric-select { width: 100%; } .history-metric-select select { width: 100%; } }
</style>
