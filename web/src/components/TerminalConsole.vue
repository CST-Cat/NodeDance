<script setup lang="ts">
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
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
const status = ref('正在授权终端…')
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
    const url = new URL('/ws/v1/streams/terminal', window.location.href)
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
    url.searchParams.set('ticket', authorization.ticket)
    socket = new WebSocket(url)
    socket.onopen = () => { status.value = '正在连接目标…' }
    socket.onmessage = (event) => {
      if (disposed) return
      let message: { type?: string; frame?: TerminalFrame }
      try { message = JSON.parse(String(event.data)) as typeof message } catch { return }
      const frame = message.frame
      if (message.type !== 'terminal' || !frame || frame.streamId !== streamId) return
      if (frame.action === 'ready') {
        ready = true
        status.value = '已连接'
        fitTerminal()
        terminal?.focus()
      } else if (frame.action === 'output' && frame.data) {
        const bytes = Uint8Array.from(atob(frame.data), (character) => character.charCodeAt(0))
        terminal?.write(decoder.decode(bytes, { stream: true }))
      } else if (frame.action === 'error') {
        error.value = frame.message || '终端连接失败。'
        status.value = '连接失败'
      } else if (frame.action === 'exit') {
        status.value = `进程已退出（${frame.exitCode ?? '未知'}）`
      } else if (frame.action === 'closed') {
        status.value = '终端已关闭'
      }
    }
    socket.onerror = () => {
      if (!disposed) {
        status.value = '连接中断'
        error.value = '终端数据通道不可用。'
      }
    }
    socket.onclose = () => {
      ready = false
      if (!disposed && !error.value) status.value = '连接已关闭'
    }
  } catch (reason) {
    if (!disposed) {
      status.value = '连接失败'
      error.value = reason instanceof Error ? reason.message : '无法打开终端。'
    }
  }
}

onMounted(async () => {
  terminal = new Terminal({
    cursorBlink: true,
    convertEol: false,
    scrollback: 3000,
    fontSize: 13,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
    theme: { background: '#08111d', foreground: '#e4edf8', cursor: '#7ce0a2', selectionBackground: '#33506f' },
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
  dispose()
})
</script>

<template>
  <div class="terminal-backdrop" data-testid="terminal-backdrop" @click.self="close">
    <section class="terminal-dialog" role="dialog" aria-modal="true" aria-labelledby="terminal-title">
      <header class="terminal-heading">
        <div class="terminal-heading-copy">
          <span class="terminal-eyebrow">SECURE TERMINAL</span>
          <h2 id="terminal-title">{{ targetLabel }}</h2>
          <p>{{ status }}</p>
        </div>
        <button class="terminal-close" type="button" aria-label="关闭终端" @click="close">×</button>
      </header>
      <p v-if="error" class="terminal-error" role="alert">{{ error }}</p>
      <div ref="terminalElement" class="terminal-screen" data-testid="terminal-screen" aria-label="交互式终端"></div>
      <nav class="terminal-keys" aria-label="终端辅助按键">
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
        <span>输入内容不会写入审计日志。</span>
        <button type="button" @click="close">结束会话</button>
      </footer>
    </section>
  </div>
</template>

<style>
@import '@xterm/xterm/css/xterm.css';
</style>

<style scoped>
.terminal-backdrop { position: fixed; inset: 0; z-index: 1000; display: grid; place-items: center; padding: 20px; background: rgba(3, 8, 15, .78); backdrop-filter: blur(8px); }
.terminal-dialog { display: grid; width: min(1040px, 100%); max-height: min(820px, 94dvh); grid-template-rows: auto minmax(240px, 1fr) auto auto; overflow: hidden; border: 1px solid rgba(153, 185, 226, .22); border-radius: 14px; color: #eaf0fa; background: #0d1725; box-shadow: 0 24px 90px rgba(0, 0, 0, .5); }
.terminal-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 14px; padding: 16px 20px 12px; border-bottom: 1px solid rgba(153, 185, 226, .13); }
.terminal-heading-copy { min-width: 0; }
.terminal-eyebrow { color: #88a4c5; font: 9px ui-monospace, monospace; letter-spacing: .14em; }
.terminal-heading h2 { overflow: hidden; margin: 4px 0; font-size: 16px; text-overflow: ellipsis; white-space: nowrap; }
.terminal-heading p { margin: 0; color: #9aabc1; font-size: 11px; }
.terminal-close { width: 36px; height: 36px; border: 1px solid rgba(153, 185, 226, .18); border-radius: 8px; color: #dce8f7; background: rgba(255, 255, 255, .04); font-size: 22px; cursor: pointer; }
.terminal-screen { min-height: 0; padding: 12px; background: #08111d; touch-action: pan-x pan-y; }
.terminal-screen :deep(.xterm) { height: 100%; }
.terminal-error { margin: 0; padding: 9px 20px; color: #ffc1b8; background: rgba(184, 77, 72, .12); font-size: 11px; overflow-wrap: anywhere; }
.terminal-keys { display: flex; flex-wrap: wrap; gap: 7px; padding: 10px 14px; border-top: 1px solid rgba(153, 185, 226, .1); background: #0d1725; }
.terminal-keys button, .terminal-footer button { min-width: 40px; min-height: 36px; border: 1px solid rgba(153, 185, 226, .2); border-radius: 7px; padding: 6px 10px; color: #dce8f7; background: rgba(255, 255, 255, .045); font: 11px ui-monospace, monospace; cursor: pointer; touch-action: manipulation; }
.terminal-keys button:active, .terminal-footer button:active { background: rgba(89, 142, 201, .24); }
.terminal-footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 14px; border-top: 1px solid rgba(153, 185, 226, .1); }
.terminal-footer span { color: #8294aa; font-size: 10px; }
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
