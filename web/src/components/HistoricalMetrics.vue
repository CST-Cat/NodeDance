<script setup lang="ts">
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
    ['cpu.usage_percent', 'CPU 使用率'],
    ['memory.used_percent', '内存使用率'],
    ['memory.used_bytes', '已用内存'],
    ['network.received_bytes_per_second', '网络接收速率'],
    ['network.sent_bytes_per_second', '网络发送速率'],
  ])
  for (const series of props.history?.series ?? []) {
    if (series.key.startsWith('disk:')) {
      const suffix = series.key.endsWith(':used_percent') ? 'used_percent' : 'used_bytes'
      const mount = series.key.slice('disk:'.length, -(`:${suffix}`.length))
      labels.set(series.key, `${mount} 磁盘${suffix === 'used_percent' ? '使用率' : '已用空间'}`)
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
    tooltip: { trigger: 'axis', valueFormatter: (value) => value == null ? '无样本' : `${Number(value).toFixed(2)} ${unit}` },
    xAxis: { type: 'time', axisLabel: { color: '#91a2ba' }, axisLine: { lineStyle: { color: '#34445b' } } },
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
    chartError.value = '历史图表暂时无法显示。'
  }
}

watch(() => [props.history, selectedKey.value], render, { deep: true })
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
        <span class="eyebrow">METRIC HISTORY</span>
        <h2 id="history-title">历史监控</h2>
        <p v-if="history">{{ new Date(history.from).toLocaleString() }} — {{ new Date(history.to).toLocaleString() }} · {{ history.resolution === 'minute' ? '分钟' : '小时' }}聚合</p>
      </div>
      <label class="history-metric-select">指标
        <select v-model="selectedKey" aria-label="选择历史指标">
          <option v-for="item in availableKeys" :key="item.key" :value="item.key">{{ item.label }}</option>
        </select>
      </label>
    </header>
    <p v-if="!nodeOnline" class="history-stale" role="status">节点当前离线；以下为已保存的历史数据，空白区间表示没有收到有效样本。</p>
    <p v-else class="history-note">图表只绘制已收到的有效样本；未知值和断线区间保留为空白，不补零或连线。</p>
    <div v-if="loading" class="history-state" role="status">正在读取历史指标…</div>
    <div v-else-if="error" class="history-state history-error" role="alert">{{ error }}</div>
    <div v-else-if="chartError" class="history-state history-error" role="alert">{{ chartError }}</div>
    <div v-else ref="host" class="history-chart" data-testid="history-chart" :data-sample-count="sampleCount" :aria-label="`${selectedLabel}历史图表`"></div>
    <p v-if="!loading && !error && sampleCount === 0" class="history-empty">此时间范围没有该指标的有效样本。</p>
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
