<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { t } from '../i18n'
import { operationRequest } from '../api'
import { operationCatalog, operationURL, type CoreOperation } from '../operations'

const operations = ref<CoreOperation[]>([])
const filter = ref('')
const selected = ref('core.health')
const params = reactive<Record<string, string>>({})
const query = reactive<Record<string, string>>({})
const values = reactive<Record<string, string>>({})
const file = ref<File>()
const output = ref('')
const error = ref('')
const busy = ref(false)
const connected = ref(false)
const frame = ref('{}')
const input = ref('')
let socket: WebSocket | undefined
let streamId = ''
let pendingTerminal: { nodeId: string; streamId: string } | undefined
let revision = 0
const operation = computed(() => operations.value.find(item => item.name === selected.value))
const matches = computed(() => operations.value.filter(item => `${item.name} ${item.description}`.toLowerCase().includes(filter.value.toLowerCase())))
const properties = computed(() => Object.entries(operation.value?.bodySchema?.properties ?? {}))
function disconnect() {
  revision++; socket?.close(); socket = undefined; connected.value = false; streamId = ''
  if (pendingTerminal) { void operationRequest('terminal.cancel', { params: pendingTerminal }).catch(() => {}); pendingTerminal = undefined }
}
watch(selected, () => {
  disconnect(); output.value = ''; error.value = ''; file.value = undefined
  for (const record of [params, query, values]) for (const key of Object.keys(record)) delete record[key]
})
function append(value: unknown) {
  const text = typeof value === 'string' ? value : JSON.stringify(value, null, 2)
  output.value = (output.value + text + '\n').slice(-262144)
}
async function execute() {
  const current = operation.value
  if (!current || busy.value || connected.value) return
  busy.value = true; error.value = ''; output.value = ''
  const generation = ++revision
  try {
    const body: Record<string, unknown> = {}
    for (const [key, schema] of properties.value) {
      const value = values[key] ?? ''
      if (!value && !current.bodySchema?.required?.includes(key)) continue
      if (!value) throw new Error(`${key}: ${t('operations.required')}`)
      body[key] = schema.type === 'boolean' ? value === 'true' : ['integer', 'number'].includes(schema.type ?? '') ? JSON.parse(value) : ['object', 'array'].includes(schema.type ?? '') ? JSON.parse(value) : value
    }
    const request = { params: { ...params }, query: Object.fromEntries(Object.entries(query).filter(([, value]) => value !== '')) }
    const headers: Record<string, string> = {}
    if (current.requiresIdempotencyKey) headers['Idempotency-Key'] = crypto.randomUUID()
    let requestBody: BodyInit | undefined = current.bodySchema ? JSON.stringify(body) : undefined
    if (current.transport === 'upload' || current.transport === 'multipart') {
      if (!file.value) throw new Error(t('operations.fileRequired'))
      if (current.transport === 'upload') requestBody = file.value
      else { const form = new FormData(); form.append('file', file.value); requestBody = form }
    }
    if (current.transport === 'websocket' || current.transport === 'terminal') {
      let url: URL
      if (current.transport === 'terminal') {
        const ticket = await operationRequest<{ streamId: string; ticket: string }>(current.name, request, { body: requestBody, headers })
        streamId = ticket.streamId
        pendingTerminal = { nodeId: request.params.nodeId!, streamId: ticket.streamId }
        if (generation !== revision) {
          await operationRequest('terminal.cancel', { params: { nodeId: request.params.nodeId!, streamId: ticket.streamId } }); return
        }
        url = await operationURL('terminal.connect', { query: { ticket: ticket.ticket } })
      } else url = await operationURL(current.name, request)
      if (generation !== revision) return
      const connection = new WebSocket(url); socket = connection
      connection.onopen = () => { if (generation === revision) { connected.value = true; pendingTerminal = undefined } }
      connection.onmessage = event => {
        if (generation !== revision) return
        try {
          const data = JSON.parse(String(event.data))
          if (current.transport === 'terminal' && data.frame?.action === 'output') {
            append(new TextDecoder().decode(Uint8Array.from(atob(data.frame.data), char => char.charCodeAt(0))))
          } else append(data)
        } catch { append(String(event.data)) }
      }
      connection.onclose = () => { if (generation === revision) { connected.value = false; socket = undefined } }
      connection.onerror = () => { if (generation === revision) error.value = t('operations.connectionFailed') }
    } else {
      const result = await operationRequest<unknown>(current.name, request, { body: requestBody, headers }, current.transport === 'upload' ? 300000 : 60000)
      if (generation !== revision) return
      if (result instanceof Blob) {
        const url = URL.createObjectURL(result), link = document.createElement('a')
        link.href = url; link.download = (query.path || current.name).split('/').pop()!; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000)
        append(t('operations.downloaded'))
      } else append(result ?? t('operations.completed'))
    }
  } catch (reason) { if (generation === revision) error.value = reason instanceof Error ? reason.message : String(reason) }
  finally { busy.value = false }
}
function send() {
  try {
    if (!socket || socket.readyState !== WebSocket.OPEN) return
    const data = operation.value?.transport === 'terminal'
      ? { streamId, action: 'input', data: btoa(String.fromCharCode(...new TextEncoder().encode(input.value + '\n'))) }
      : JSON.parse(frame.value)
    if (streamId && typeof data === 'object' && !data.streamId) data.streamId = streamId
    socket.send(JSON.stringify(data)); input.value = ''
  } catch (reason) { error.value = String(reason) }
}
onMounted(async () => { try { operations.value = (await operationCatalog()).operations } catch (reason) { error.value = String(reason) } })
onBeforeUnmount(disconnect)
</script>

<template>
  <section class="panel operation-console">
    <h2>{{ t('nav.operations') }}</h2>
    <p class="muted">{{ t('operations.description') }}</p>
    <div class="operation-layout">
      <aside>
        <input v-model="filter" type="search" :placeholder="t('operations.search')" :aria-label="t('operations.search')" />
        <select v-model="selected" size="18" :aria-label="t('nav.operations')"><option v-for="item in matches" :key="item.name" :value="item.name">{{ item.name }}</option></select>
      </aside>
      <div v-if="operation" class="operation-form">
        <h3>{{ operation.name }}</h3><p class="muted">{{ operation.description }}</p>
        <form @submit.prevent="execute">
          <label v-for="name in operation.parameters" :key="name">{{ name }} <input v-model="params[name]" required :disabled="busy || connected" /></label>
          <label v-for="name in operation.query" :key="name">{{ name }} <input v-model="query[name]" :type="/ticket|token/i.test(name) ? 'password' : 'text'" :disabled="busy || connected" /></label>
          <label v-for="[name, schema] in properties" :key="name">{{ name }}{{ operation.bodySchema?.required?.includes(name) ? ' *' : '' }}
            <select v-if="schema.enum || schema.type === 'boolean'" v-model="values[name]" :required="operation.bodySchema?.required?.includes(name)" :disabled="busy || connected"><option value="">—</option><option v-for="value in schema.enum ?? ['true', 'false']" :key="value" :value="value">{{ value }}</option></select>
            <textarea v-else-if="schema.type === 'object' || schema.type === 'array'" v-model="values[name]" rows="4" :required="operation.bodySchema?.required?.includes(name)" :disabled="busy || connected" />
            <input v-else v-model="values[name]" :type="schema.writeOnly ? 'password' : ['integer', 'number'].includes(schema.type ?? '') ? 'number' : 'text'" :step="schema.type === 'integer' ? '1' : 'any'" :required="operation.bodySchema?.required?.includes(name)" :disabled="busy || connected" autocomplete="off" />
          </label>
          <label v-if="['upload', 'multipart'].includes(operation.transport)">{{ t('operations.file') }}<input type="file" required @change="file = ($event.target as HTMLInputElement).files?.[0]" /></label>
          <button type="submit" class="primary-button" :disabled="busy || connected">{{ busy ? t('operations.running') : t('operations.execute') }}</button>
          <button v-if="connected" type="button" @click="disconnect">{{ t('operations.disconnect') }}</button>
        </form>
        <form v-if="connected" @submit.prevent="send"><textarea v-if="operation.transport === 'websocket'" v-model="frame" rows="3" aria-label="JSON" /><input v-else v-model="input" :aria-label="t('operations.input')" /><button type="submit">{{ t('operations.send') }}</button></form>
        <p v-if="error" role="alert" class="error">{{ error }}</p>
        <pre v-if="output" data-testid="operation-result">{{ output }}</pre>
      </div>
    </div>
  </section>
</template>

<style scoped>
.operation-console { padding: 28px; border: 1px solid var(--line); border-radius:var(--radius-card); background: var(--surface); }
h2 { margin: 0 0 10px; font-size: 24px; letter-spacing: -.03em; }
h3 { margin: 0 0 10px; font-size: 18px; }
.muted { color: var(--muted); font-size: 13px; line-height: 1.7; }
.operation-layout { display: grid; grid-template-columns: minmax(180px, 260px) minmax(0, 1fr); gap: 28px; margin-top: 24px; }
aside, form, label { display: flex; flex-direction: column; gap: 8px; min-width: 0; }
label { color: var(--text); font-size: 12px; font-weight:500; }
form { gap: 16px; margin-bottom: 20px; }
input, select, textarea { box-sizing: border-box; width: 100%; min-height: 42px; border: 1px solid var(--line); border-radius:var(--radius-control); padding: 10px 12px; background: var(--surface-soft); color: var(--text); font: inherit; }
textarea { font: 12px/1.7 ui-monospace, SFMono-Regular, Menlo, monospace; resize: vertical; }
aside select { min-height: 420px; padding: 8px; font: 12px/1.6 ui-monospace, SFMono-Regular, Menlo, monospace; }
aside option { padding: 7px 8px; border-radius: 4px; }
aside option:checked { background: var(--accent-soft); color: var(--accent); }
button { align-self: flex-start; min-height: 40px; border: 1px solid var(--line); border-radius:var(--radius-control); padding: 9px 18px; background: var(--surface-soft); color: var(--text); font: inherit; font-size: 13px; cursor: pointer; }
button.primary-button { border-color:transparent; }
button:disabled { opacity: .55; cursor: default; }
.error { color: var(--danger); font-size: 13px; }
pre { max-height: 600px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; border: 1px solid var(--line); background: var(--surface-soft); color: var(--text); padding: 18px; border-radius:var(--radius-card); font: 12px/1.7 ui-monospace, SFMono-Regular, Menlo, monospace; }
@media (max-width: 700px) { .operation-console { padding: 18px; } .operation-layout { grid-template-columns: 1fr; gap: 20px; } aside select { min-height: 170px; height: 170px; } }
</style>
