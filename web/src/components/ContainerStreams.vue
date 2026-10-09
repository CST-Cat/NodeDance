<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import {
  createContainerStreamSocket,
  decodeContainerLogData,
  type ContainerLogFrame,
  type ContainerStatsFrame,
  type ContainerStreamEnvelope,
  type ContainerStreamKind,
  type ContainerStreamReady,
} from '../streamApi'

const props = defineProps<{
  nodeId: string
  containerId: string
  disabled?: boolean
  statsAvailable?: boolean
}>()

type StreamState = 'idle' | 'connecting' | 'connected' | 'error'
const logState = ref<StreamState>('idle')
const statsState = ref<StreamState>('idle')
const logError = ref('')
const statsError = ref('')
const logText = ref('')
const logBytes = ref(0)
const logChannels = ref(new Set<'stdout' | 'stderr'>())
const statsSnapshot = ref<ContainerStatsFrame['snapshot']>()
const statsSamples = ref(0)
const sockets: Partial<Record<ContainerStreamKind, WebSocket>> = {}
const expectedClose = new Set<ContainerStreamKind>()
const sequence: Partial<Record<ContainerStreamKind, number>> = {}
const logDecoders = { stdout: new TextDecoder(), stderr: new TextDecoder() }
let pendingLogChunks: string[] = []
let pendingLogLength = 0
let pendingLogBytes = 0
let logFlushTimer: ReturnType<typeof setInterval> | undefined
const statsTime = computed(() => statsSnapshot.value?.observed_at ? new Date(statsSnapshot.value.observed_at).toLocaleTimeString() : '等待采样')

const LOG_TEXT_LIMIT = 256 * 1024
const PENDING_LOG_TEXT_LIMIT = 64 * 1024

function stateFor(kind: ContainerStreamKind) {
  return kind === 'logs' ? logState : statsState
}

function errorFor(kind: ContainerStreamKind) {
  return kind === 'logs' ? logError : statsError
}

function stateLabel(state: StreamState): string {
  if (state === 'connecting') return '正在连接'
  if (state === 'connected') return '实时接收'
  if (state === 'error') return '连接已关闭'
  return '未打开'
}

function explainStreamError(code: string): string {
  const messages: Record<string, string> = {
    unavailable: '目标暂时不可用。',
    not_running: '容器当前未运行。',
    invalid_request: '请求参数无效。',
    stream_limit: '当前流数量已达上限。',
    slow_consumer: '接收速度不足，已为保护连接关闭此流。',
    engine_error: 'Docker Engine 未能提供此数据。',
  }
  return messages[code] ?? '数据流发生错误。'
}

function fail(kind: ContainerStreamKind, message: string) {
  errorFor(kind).value = message
  stateFor(kind).value = 'error'
  expectedClose.add(kind)
  sockets[kind]?.close(1000, 'stream ended')
}

function appendLog(frame: ContainerLogFrame) {
  if (frame.containerId !== props.containerId || (frame.channel !== 'stdout' && frame.channel !== 'stderr')) {
    fail('logs', '日志流返回了其他容器的数据。')
    return
  }
  if (!logChannels.value.has(frame.channel)) logChannels.value = new Set([...logChannels.value, frame.channel])
  let bytes: number
  try {
    bytes = atob(frame.data).length
    const decoded = decodeContainerLogData(frame.data, logDecoders[frame.channel])
    pendingLogBytes += bytes
    if (decoded) appendPendingLogText(`[${frame.channel}] ${decoded}`)
  } catch {
    fail('logs', '日志内容无法解码。')
  }
}

function appendPendingLogText(text: string) {
  pendingLogChunks.push(text)
  pendingLogLength += text.length
  while (pendingLogLength > PENDING_LOG_TEXT_LIMIT && pendingLogChunks.length > 0) {
    const excess = pendingLogLength - PENDING_LOG_TEXT_LIMIT
    const first = pendingLogChunks[0]
    if (first.length <= excess) {
      pendingLogChunks.shift()
      pendingLogLength -= first.length
    } else {
      pendingLogChunks[0] = first.slice(excess)
      pendingLogLength -= excess
    }
  }
}

function flushLogBuffer() {
  if (pendingLogBytes > 0) {
    logBytes.value += pendingLogBytes
    pendingLogBytes = 0
  }
  if (pendingLogChunks.length === 0) return
  logText.value += pendingLogChunks.join('')
  pendingLogChunks = []
  pendingLogLength = 0
  if (logText.value.length > LOG_TEXT_LIMIT) {
    const trimAt = logText.value.indexOf('\n', logText.value.length - LOG_TEXT_LIMIT)
    logText.value = logText.value.slice(trimAt >= 0 ? trimAt + 1 : -LOG_TEXT_LIMIT)
  }
}

function startLogFlush() {
  if (logFlushTimer !== undefined) return
  logFlushTimer = setInterval(flushLogBuffer, 50)
}

function stopLogFlush() {
  if (logFlushTimer !== undefined) {
    clearInterval(logFlushTimer)
    logFlushTimer = undefined
  }
  flushLogBuffer()
}

function acceptStats(frame: ContainerStatsFrame) {
  if (!frame.snapshot || frame.snapshot.container_id !== props.containerId) {
    fail('stats', '实时统计返回了其他容器的数据。')
    return
  }
  statsSnapshot.value = frame.snapshot
  statsSamples.value++
}

function flushLogDecoders() {
  for (const channel of ['stdout', 'stderr'] as const) {
    const text = logDecoders[channel].decode()
    if (text) appendPendingLogText(`[${channel}] ${text}`)
  }
  flushLogBuffer()
}

function open(kind: ContainerStreamKind) {
  if (props.disabled || (kind === 'stats' && !props.statsAvailable) || stateFor(kind).value === 'connecting' || stateFor(kind).value === 'connected') return
  errorFor(kind).value = ''
  if (kind === 'logs') {
    logText.value = ''
    logBytes.value = 0
    logChannels.value = new Set()
    pendingLogChunks = []
    pendingLogLength = 0
    pendingLogBytes = 0
    logDecoders.stdout = new TextDecoder()
    logDecoders.stderr = new TextDecoder()
  } else {
    statsSnapshot.value = undefined
    statsSamples.value = 0
  }
  sequence[kind] = 0
  expectedClose.delete(kind)
  stateFor(kind).value = 'connecting'
  let socket: WebSocket
  try {
    socket = createContainerStreamSocket(kind, props.nodeId, props.containerId)
  } catch {
    stateFor(kind).value = 'error'
    errorFor(kind).value = '无法创建数据流连接。'
    return
  }
  sockets[kind] = socket
  if (kind === 'logs') startLogFlush()
  socket.onmessage = (event) => {
    let envelope: ContainerStreamEnvelope
    try {
      envelope = JSON.parse(String(event.data)) as ContainerStreamEnvelope
    } catch {
      fail(kind, '服务器返回了无法识别的数据。')
      return
    }
    if (!envelope || typeof envelope.type !== 'string' || !Number.isSafeInteger(envelope.sequence) || envelope.sequence <= (sequence[kind] ?? 0)) {
      fail(kind, '数据流顺序无效。')
      return
    }
    sequence[kind] = envelope.sequence
    if (envelope.type === 'container_stream_ready') {
      const ready = envelope.payload as ContainerStreamReady
      if (ready.kind !== kind || ready.containerId !== props.containerId) {
        fail(kind, '数据流连接到了其他目标。')
        return
      }
      stateFor(kind).value = 'connected'
      return
    }
    if (envelope.type === 'container_stream_error') {
      fail(kind, explainStreamError((envelope.payload as { code?: string })?.code ?? 'engine_error'))
      return
    }
    if (envelope.type === 'container_stream_end') {
      expectedClose.add(kind)
      socket.close(1000, 'stream ended')
      return
    }
    if (envelope.type === 'container_stream_heartbeat') return
    if (envelope.type === 'container_log' && kind === 'logs') {
      appendLog(envelope.payload as ContainerLogFrame)
      return
    }
    if (envelope.type === 'container_stats' && kind === 'stats') {
      acceptStats(envelope.payload as ContainerStatsFrame)
      return
    }
    fail(kind, '服务器返回了不匹配的数据流。')
  }
  socket.onerror = () => {
    if (stateFor(kind).value === 'connecting' || stateFor(kind).value === 'connected') fail(kind, '数据流连接失败。')
  }
  socket.onclose = () => {
    if (sockets[kind] !== socket) return
    delete sockets[kind]
    if (kind === 'logs') {
      flushLogDecoders()
      stopLogFlush()
    }
    if (expectedClose.has(kind)) {
      expectedClose.delete(kind)
      if (stateFor(kind).value !== 'error') stateFor(kind).value = 'idle'
      return
    }
    if (stateFor(kind).value === 'connecting' || stateFor(kind).value === 'connected') {
      stateFor(kind).value = 'error'
      errorFor(kind).value = '连接已关闭。请确认节点在线和 Docker 状态新鲜后重试。'
    }
  }
}

function close(kind: ContainerStreamKind) {
  const socket = sockets[kind]
  if (!socket) {
    if (kind === 'logs') stopLogFlush()
    stateFor(kind).value = 'idle'
    errorFor(kind).value = ''
    return
  }
  expectedClose.add(kind)
  stateFor(kind).value = 'idle'
  errorFor(kind).value = ''
  if (kind === 'logs') {
    flushLogDecoders()
    stopLogFlush()
  }
  socket.close(1000, 'user closed stream')
}

function metric(value: { state: string; value?: number; reason?: string } | undefined, suffix = ''): string {
  if (!value || value.state !== 'ok' || typeof value.value !== 'number' || !Number.isFinite(value.value)) {
    return value?.reason ? `未知：${value.reason}` : '未知'
  }
  return `${value.value.toFixed(1)}${suffix}`
}

function memoryMetric(): string {
  const memory = statsSnapshot.value?.memory
  if (!memory || memory.state !== 'ok' || !memory.value) return memory?.reason ? `未知：${memory.reason}` : '未知'
  return `${memory.value.used_percent.toFixed(1)}% · ${formatBytes(memory.value.used_bytes)} / ${formatBytes(memory.value.limit_bytes)}`
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) return '未知'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let scaled = value
  let index = 0
  while (scaled >= 1024 && index < units.length - 1) { scaled /= 1024; index++ }
  return `${scaled.toFixed(index === 0 ? 0 : 1)} ${units[index]}`
}

function rateMetric(metricValue: { state: string; value?: { received_bytes_per_second: number; sent_bytes_per_second: number }; reason?: string } | undefined): string {
  if (!metricValue || metricValue.state !== 'ok' || !metricValue.value) return metricValue?.reason ? `未知：${metricValue.reason}` : '未知'
  return `↓ ${formatBytes(metricValue.value.received_bytes_per_second)}/s · ↑ ${formatBytes(metricValue.value.sent_bytes_per_second)}/s`
}

function blockIOMetric(metricValue: { state: string; value?: { read_bytes_per_second: number; write_bytes_per_second: number }; reason?: string } | undefined): string {
  if (!metricValue || metricValue.state !== 'ok' || !metricValue.value) return metricValue?.reason ? `未知：${metricValue.reason}` : '未知'
  return `读 ${formatBytes(metricValue.value.read_bytes_per_second)}/s · 写 ${formatBytes(metricValue.value.write_bytes_per_second)}/s`
}

onBeforeUnmount(() => {
  for (const kind of ['logs', 'stats'] as const) close(kind)
  stopLogFlush()
})
</script>

<template>
  <section class="container-streams" data-testid="container-streams" :data-container-id="containerId" aria-label="容器实时数据流">
    <div class="container-stream-actions">
      <button v-if="logState !== 'connecting' && logState !== 'connected'" type="button" :disabled="disabled" @click="open('logs')">查看日志</button>
      <button v-else type="button" class="active" @click="close('logs')">关闭日志流</button>
      <span class="stream-label" :data-state="logState" data-testid="stream-log-state" role="status">日志：{{ stateLabel(logState) }}</span>
      <button v-if="statsState !== 'connecting' && statsState !== 'connected'" type="button" :disabled="disabled || !statsAvailable" @click="open('stats')">实时统计</button>
      <button v-else type="button" class="active" @click="close('stats')">关闭统计</button>
      <span class="stream-label" :data-state="statsState" data-testid="stream-stats-state" role="status">统计：{{ stateLabel(statsState) }}</span>
    </div>

    <p v-if="disabled" class="stream-hint" data-testid="stream-disabled">节点或容器状态过期，恢复实时连接后可打开数据流。</p>
    <p v-else-if="!statsAvailable" class="stream-hint">容器运行后可查看实时统计。</p>
    <p v-if="logError" class="stream-error" role="alert" data-testid="stream-log-error">{{ logError }}</p>
    <p v-if="statsError" class="stream-error" role="alert" data-testid="stream-stats-error">{{ statsError }}</p>

    <div v-if="logState === 'connected' || logText" class="stream-log-panel">
      <div class="stream-panel-heading"><strong>容器日志</strong><span data-testid="stream-log-bytes" :data-bytes="logBytes">{{ formatBytes(logBytes) }}</span></div>
      <pre data-testid="stream-log-output" :data-bytes="logBytes" :data-channels="Array.from(logChannels).join(',')">{{ logText || '等待日志…' }}</pre>
    </div>

    <div v-if="statsState === 'connected' || statsSnapshot" class="stream-stats-panel" data-testid="stream-stats-snapshot" :data-samples="statsSamples">
      <div class="stream-panel-heading"><strong>实时资源统计</strong><span>{{ statsTime }}</span></div>
      <dl>
        <div><dt>CPU</dt><dd data-testid="stream-cpu">{{ metric(statsSnapshot?.cpu_percent, '%') }}</dd></div>
        <div><dt>内存</dt><dd data-testid="stream-memory">{{ memoryMetric() }}</dd></div>
        <div><dt>网络</dt><dd data-testid="stream-network">{{ rateMetric(statsSnapshot?.network) }}</dd></div>
        <div><dt>磁盘读写</dt><dd data-testid="stream-block-io">{{ blockIOMetric(statsSnapshot?.block_io) }}</dd></div>
      </dl>
    </div>
  </section>
</template>

<style scoped>
.container-streams { display: grid; gap: 8px; min-width: 0; margin-top: 10px; }
.container-stream-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.container-stream-actions button { min-height: 34px; border: 1px solid rgba(141, 201, 255, .25); border-radius: 7px; padding: 6px 10px; color: #cce6ff; background: rgba(62, 119, 170, .16); font: inherit; font-size: 10px; cursor: pointer; }
.container-stream-actions button:hover:not(:disabled) { background: rgba(62, 119, 170, .3); }
.container-stream-actions button:disabled { opacity: .45; cursor: not-allowed; }
.container-stream-actions button.active { border-color: rgba(255, 184, 128, .25); color: #ffd2a5; }
.stream-label, .stream-hint, .stream-error { color: #9eafc5; font-size: 10px; overflow-wrap: anywhere; }
.stream-label[data-state='connected'] { color: #9ce0b7; }
.stream-label[data-state='error'], .stream-error { color: #ffc1b8; }
.stream-hint { margin: 0; }
.stream-error { margin: 0; }
.stream-log-panel, .stream-stats-panel { min-width: 0; border: 1px solid rgba(171, 196, 232, .13); border-radius: 8px; padding: 9px; background: rgba(8, 15, 26, .52); }
.stream-panel-heading { display: flex; justify-content: space-between; gap: 8px; color: #c9d8eb; font-size: 10px; }
.stream-panel-heading span { color: #94a7bf; font-variant-numeric: tabular-nums; }
.stream-log-panel pre { max-height: 240px; overflow: auto; margin: 8px 0 0; white-space: pre-wrap; overflow-wrap: anywhere; color: #d7e4f4; font: 10px/1.55 ui-monospace, SFMono-Regular, Menlo, monospace; }
.stream-stats-panel dl { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; margin: 9px 0 0; }
.stream-stats-panel dl > div { min-width: 0; border-radius: 6px; padding: 7px; background: rgba(70, 97, 130, .15); }
.stream-stats-panel dt { color: #93a8c3; font-size: 9px; }
.stream-stats-panel dd { margin: 4px 0 0; color: #e2edf9; font-size: 10px; overflow-wrap: anywhere; font-variant-numeric: tabular-nums; }
@media (max-width: 560px) {
  .container-stream-actions { align-items: flex-start; }
  .stream-stats-panel dl { grid-template-columns: minmax(0, 1fr); }
  .stream-log-panel pre { max-height: 180px; }
}
</style>
