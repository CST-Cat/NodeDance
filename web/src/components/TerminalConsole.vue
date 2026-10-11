<script setup lang="ts">
import { operationURL } from '../operations'
import { t, display, locale } from "../i18n"
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import { nextTick, onBeforeUnmount, onMounted, ref, computed } from 'vue'
import { api } from '../api'

const props = defineProps<{
  nodeId: string
  targetKind: 'host' | 'container'
  containerId?: string
  targetLabel: string
}>()
const emit = defineEmits<{ close: [] }>()

interface TerminalFrame {
  streamId: string
  action: string
  data?: string
  message?: string
  rows?: number
  columns?: number
  exitCode?: number
}

const terminalElement = ref<HTMLDivElement>()
const dialogElement = ref<HTMLElement>()
let previousFocus: HTMLElement | null = null
let previousOverflow = ''

function handleDialogKey(event: KeyboardEvent) {
  // Terminal keystrokes (including Escape and Tab) belong to the remote shell.
  if ((event.target as HTMLElement).closest('.terminal-screen')) return
  if (event.key === 'Escape') { event.preventDefault(); close(); return }
  if (event.key !== 'Tab') return
  const controls = dialogElement.value?.querySelectorAll<HTMLElement>('button:not(:disabled), textarea, [tabindex="0"]')
  if (!controls?.length) return
  const first = controls[0]!, last = controls[controls.length - 1]!
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}
const statusKey = ref('m.389cde8ab951')
const statusParams = ref<Record<string, unknown>>({})
const status = computed(() => t(statusKey.value, statusParams.value))
const error = ref('')
let terminal: Terminal | undefined
let fit: FitAddon | undefined
let socket: WebSocket | undefined
let resizeObserver: ResizeObserver | undefined
let disposed = false
let ready = false
let streamId = ''
const encoder = new TextEncoder()
const decoder = new TextDecoder('utf-8')

function encodeBase64(data: Uint8Array): string {
  let binary = ''
  for (const byte of data) binary += String.fromCharCode(byte)
  return btoa(binary)
}

function sendInput(data: string) {
  if (!ready || !socket || socket.readyState !== WebSocket.OPEN) return
  socket.send(JSON.stringify({ streamId, action: 'input', data: encodeBase64(encoder.encode(data)) }))
}

function fitTerminal() {
  if (!fit || !terminal || !ready) return
  fit.fit()
  socket?.send(JSON.stringify({ streamId, action: 'resize', rows: terminal.rows, columns: terminal.cols }))
}

function sendAuxiliary(value: string) {
  sendInput(value)
  terminal?.focus()
}

function close() {
  dispose()
  emit('close')
}

function revokePendingTicket(nodeId: string, id: string) {
  if (!id) return
  // The Core endpoint is idempotent and only removes an unconsumed ticket.
  // A consumed stream is still closed through the WebSocket below.
  void api.cancelTerminal(nodeId, id).catch(() => undefined)
}

function dispose() {
  if (disposed) return
  disposed = true
  ready = false
  resizeObserver?.disconnect()
  if (socket?.readyState === WebSocket.OPEN) {
    socket.send(JSON.stringify({ streamId, action: 'close' }))
  }
  socket?.close()
  terminal?.dispose()
  revokePendingTicket(props.nodeId, streamId)
}

async function connect() {
  try {
    const target = props.targetKind === 'host'
      ? { targetKind: 'host' as const }
      : { targetKind: 'container' as const, containerId: props.containerId ?? '' }
    const authorization = await api.createTerminal(props.nodeId, target)
    streamId = authorization.streamId
    if (disposed) {
      // Unmount can race the POST response. Once the stream ID is known, revoke
      // the exact unused ticket so it does not hold a per-node slot until expiry.
      revokePendingTicket(props.nodeId, streamId)
      return
    }
    const url = await operationURL('terminal.connect', { query: { ticket: authorization.ticket } })
    if (disposed) { revokePendingTicket(props.nodeId, streamId); return }
    socket = new WebSocket(url)
    socket.onopen = () => { statusKey.value = 'm.790beabf1d6b' }
    socket.onmessage = (event) => {
      if (disposed) return
      let message: { type?: string; frame?: TerminalFrame }
      try { message = JSON.parse(String(event.data)) as typeof message } catch { return }
      const frame = message.frame
      if (message.type !== 'terminal' || !frame || frame.streamId !== streamId) return
      if (frame.action === 'ready') {
        ready = true
        statusKey.value = 'm.5be0323e8adc'
        fitTerminal()
        terminal?.focus()
      } else if (frame.action === 'output' && frame.data) {
        const bytes = Uint8Array.from(atob(frame.data), (character) => character.charCodeAt(0))
        terminal?.write(decoder.decode(bytes, { stream: true }))
      } else if (frame.action === 'error') {
        error.value = frame.message || t('m.df75c0b36f8c')
        statusKey.value = 'm.89c4766afc85'
      } else if (frame.action === 'exit') {
        statusKey.value = 'm.d75d84cb855d'
        statusParams.value = {'0': frame.exitCode ?? '—'}
      } else if (frame.action === 'closed') {
        statusKey.value = 'm.fdfff0a9c49a'
      }
    }
    socket.onerror = () => {
      if (!disposed) {
        statusKey.value = 'm.c306f7fafb4f'
        error.value = t('m.6bf6e5cd60e6')
      }
    }
    socket.onclose = () => {
      ready = false
      if (!disposed && !error.value) statusKey.value = 'm.30f12a33e524'
    }
  } catch (reason) {
    if (!disposed) {
      statusKey.value = 'm.89c4766afc85'
      error.value = reason instanceof Error ? reason.message : t('m.9e8a26146154')
    }
  }
}

onMounted(async () => {
  previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  previousOverflow = document.body.style.overflow
  document.body.style.overflow = 'hidden'
  dialogElement.value?.querySelector<HTMLElement>('button')?.focus()
  const palette = getComputedStyle(terminalElement.value ?? document.documentElement)
  terminal = new Terminal({
    cursorBlink: true,
    convertEol: false,
    scrollback: 3000,
    fontSize: 13,
    fontFamily: 'JetBrains Mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
    theme: { background: palette.getPropertyValue('--product-canvas').trim(), foreground: palette.getPropertyValue('--product-text').trim(), cursor: palette.getPropertyValue('--accent-fill').trim(), selectionBackground: palette.getPropertyValue('--product-line-strong').trim() },
    allowProposedApi: false,
  })
  fit = new FitAddon()
  terminal.loadAddon(fit)
  if (terminalElement.value) terminal.open(terminalElement.value)
  terminal.onData(sendInput)
  resizeObserver = new ResizeObserver(() => {
    if (ready) fitTerminal()
    else fit?.fit()
  })
  if (terminalElement.value) resizeObserver.observe(terminalElement.value)
  await nextTick()
  fit.fit()
  await connect()
})

onBeforeUnmount(() => {
  document.body.style.overflow = previousOverflow
  if (previousFocus?.isConnected) previousFocus.focus()
  dispose()
})
</script>

<template>
  <div class="terminal-backdrop" data-testid="terminal-backdrop" @click.self="close">
    <section ref="dialogElement" class="terminal-dialog" @keydown="handleDialogKey" role="dialog" aria-modal="true" aria-labelledby="terminal-title">
      <header class="terminal-heading">
        <div class="terminal-heading-copy">
          <span class="terminal-eyebrow">{{ t('label.17001d87d5f0') }}</span>
          <h2 id="terminal-title">{{ targetLabel }}</h2>
          <p>{{ status }}</p>
        </div>
        <button class="terminal-close" type="button" :aria-label="t('m.16707b59cf5e')" @click="close">×</button>
      </header>
      <p v-if="error" class="terminal-error" role="alert">{{ display(error) }}</p>
      <div ref="terminalElement" class="terminal-screen" data-testid="terminal-screen" :aria-label="t('m.ce59564793be')"></div>
      <nav class="terminal-keys" :aria-label="t('m.151b2f2ae35f')">
        <button type="button" @click="sendAuxiliary('\t')">Tab</button>
        <button type="button" @click="sendAuxiliary('\x03')">Ctrl+C</button>
        <button type="button" @click="sendAuxiliary('\x04')">Ctrl+D</button>
        <button type="button" @click="sendAuxiliary('\x1b')">Esc</button>
        <button type="button" @click="sendAuxiliary('\x1b[A')">↑</button>
        <button type="button" @click="sendAuxiliary('\x1b[B')">↓</button>
        <button type="button" @click="sendAuxiliary('\x1b[D')">←</button>
        <button type="button" @click="sendAuxiliary('\x1b[C')">→</button>
      </nav>
      <footer class="terminal-footer">
        <span>{{ t('m.b8bd8d5f7f2f') }}</span>
        <button type="button" @click="close">{{ t('m.3eaf8d1b8148') }}</button>
      </footer>
    </section>
  </div>
</template>

<style>
@import '@xterm/xterm/css/xterm.css';
</style>

<style scoped>
.terminal-backdrop { position: fixed; inset: 0; z-index: 1000; display: grid; place-items: center; padding: 20px; background: rgba(24, 23, 21, .78); backdrop-filter: blur(8px); }
.terminal-dialog { display: grid; width: min(1040px, 100%); max-height: min(820px, 94dvh); grid-template-rows: auto minmax(240px, 1fr) auto auto; overflow: hidden; border: 1px solid var(--product-line-strong); border-radius:var(--radius-card); color: var(--product-text); background: var(--product-surface); box-shadow:none; }
.terminal-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 14px; padding: 16px 20px 12px; border-bottom: 1px solid var(--product-line); }
.terminal-heading-copy { min-width: 0; }
.terminal-eyebrow { color: var(--product-muted); font: 9px ui-monospace, monospace; letter-spacing: .14em; }
.terminal-heading h2 { color:var(--product-text); overflow: hidden; margin: 4px 0; font-size: 16px; text-overflow: ellipsis; white-space: nowrap; }
.terminal-heading p { margin: 0; color: var(--product-muted); font-size: 11px; }
.terminal-close { width: 36px; height: 36px; border: 1px solid var(--product-line); border-radius:var(--radius-card); color: var(--product-text); background: var(--product-elevated); font-size: 22px; cursor: pointer; }
.terminal-screen { min-height: 0; padding: 12px; background: var(--product-canvas); touch-action: pan-x pan-y; }
.terminal-screen :deep(.xterm) { height: 100%; }
.terminal-error { margin: 0; padding: 9px 20px; color: #ffc1b8; background: rgba(184, 77, 72, .12); font-size: 11px; overflow-wrap: anywhere; }
.terminal-keys { display: flex; flex-wrap: wrap; gap: 7px; padding: 10px 14px; border-top: 1px solid var(--product-line); background: var(--product-surface); }
.terminal-keys button, .terminal-footer button { min-width: 40px; min-height:40px; border: 1px solid var(--product-line); border-radius:var(--radius-control); padding: 6px 10px; color: var(--product-text); background: var(--product-elevated); font: 11px ui-monospace, monospace; cursor: pointer; touch-action: manipulation; }
.terminal-keys button:active, .terminal-footer button:active { background: #534338; }
.terminal-footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 14px; border-top: 1px solid var(--product-line); }
.terminal-footer span { color: var(--product-muted); font-size: 10px; }
.terminal-footer button { color: #ffc1b8; border-color: rgba(255, 129, 116, .25); }
@media (max-width: 600px) {
  .terminal-backdrop { place-items: stretch; padding: 0; }
  .terminal-dialog { width: 100%; max-height: 100dvh; min-height: 0; grid-template-rows: auto minmax(180px, 1fr) auto auto; border-radius: 0; }
  .terminal-heading { padding: 12px 14px 10px; }
  .terminal-screen { padding: 8px; font-size: 11px; }
  .terminal-keys { gap: 6px; padding: 8px; }
  .terminal-keys button { min-height: 42px; min-width: 42px; flex: 1 0 auto; }
  .terminal-footer { padding: 8px; }
  .terminal-footer button { min-height: 42px; }
}
</style>
