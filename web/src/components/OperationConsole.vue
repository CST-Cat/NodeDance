<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch, nextTick } from 'vue'
import { t } from '../i18n'
import { operationRequest } from '../api'
import { operationCatalog, operationURL, operationBody, exactJSON, operationUpload, schemaHasSecrets, completionInput, completionStatus, type CoreOperation, type OperationInput } from '../operations'
import { requestIntent, intentKey } from '../operation-intent'

const operations = ref<CoreOperation[]>([])
const filter = ref('')
const selected = ref('core.health')
const params = reactive<Record<string, string>>({})
const query = reactive<Record<string, string>>({})
const values = reactive<Record<string, string>>({})
const requestHeaders = reactive<Record<string,string>>({})
const rawBody = ref('')
const rawMode = ref(false)
const waitForCompletion = ref(false)
interface PendingObservation { operation:string; name:string; input:OperationInput }
const pendingObservations=ref<PendingObservation[]>([])
const observationStorage='nodedance.pending-observations'
interface PendingSubmission {operation:string;key:string;input:OperationInput}
const pendingSubmissions=ref<PendingSubmission[]>([])
const submissionStorage='nodedance.pending-submissions'
function saveSubmissions(){try{sessionStorage.setItem(submissionStorage,JSON.stringify(pendingSubmissions.value.slice(-100)))}catch{}}
function forgetSubmission(item:PendingSubmission){pendingSubmissions.value=pendingSubmissions.value.filter(v=>v.operation!==item.operation||v.key!==item.key);saveSubmissions()}
function rememberSubmission(item:PendingSubmission){if(Object.keys({...item.input.params,...item.input.query}).some(k=>/ticket|token|password|secret|credential/i.test(k)))return;forgetSubmission(item);pendingSubmissions.value.push(item);saveSubmissions()}
async function restoreSubmission(item:PendingSubmission){selected.value=item.operation;await nextTick();Object.assign(params,item.input.params);Object.assign(query,item.input.query);requestHeaders['Idempotency-Key']=item.key;lastKey='';error.value=t('operations.restoreHint')}

function saveObservations(){try{sessionStorage.setItem(observationStorage,JSON.stringify(pendingObservations.value.slice(-100)))}catch{}}
function observationKey(item:PendingObservation){return item.operation+JSON.stringify(item.input)}
function forgetObservation(item:PendingObservation){pendingObservations.value=pendingObservations.value.filter(v=>observationKey(v)!==observationKey(item));saveObservations()}
function rememberObservation(item:PendingObservation){
 if(Object.keys({...item.input.params,...item.input.query}).some(k=>/ticket|token|password|secret|credential/i.test(k)))return
 forgetObservation(item);pendingObservations.value.push(item);saveObservations()
}

let lastIntent = '', lastKey = ''
let requestAbort: AbortController | undefined
const file = ref<File>()
const output = ref('')
const error = ref('')
const busy = ref(false)
const connected = ref(false)
const frame = ref('{}')
const input = ref('')
let socket: WebSocket | undefined
let streamId = ''
const pendingTerminal = ref<{ nodeId: string; streamId: string }>()
let terminalIntent = ''
let revision = 0
const operation = computed(() => operations.value.find(item => item.name === selected.value))
const matches = computed(() => operations.value.filter(item => `${item.name} ${item.description}`.toLowerCase().includes(filter.value.toLowerCase())))
const properties = computed(() => rawMode.value ? [] : Object.entries(operation.value?.bodySchema?.properties ?? {}))
function disconnect() {
  requestAbort?.abort(); requestAbort = undefined; busy.value = false
  revision++; socket?.close(); socket = undefined; connected.value = false; streamId = ''
  if (pendingTerminal.value) { void operationRequest('terminal.cancel', { params: pendingTerminal.value }).catch(() => {}); pendingTerminal.value = undefined }
}
watch(selected, () => {
  disconnect(); output.value = ''; error.value = ''; file.value = undefined
  rawBody.value = ''; rawMode.value = false; waitForCompletion.value = false; lastIntent = ''; lastKey = ''
  for (const record of [params, query, values, requestHeaders]) for (const key of Object.keys(record)) delete record[key]
})
watch(operation, current=>{for(const [key,field] of Object.entries(current?.bodySchema?.properties ?? {})) if(field.default!==undefined)values[key]=field.type==='string'?String(field.default):JSON.stringify(field.default)})
function append(value: unknown) {
  const text = typeof value === 'string' ? value : exactJSON(value)
  output.value = (output.value + text + '\n').slice(-262144)
}
async function execute() {
  const current = operation.value
  if (!current || busy.value || connected.value) return
  busy.value = true; error.value = ''; output.value = ''
  const generation = ++revision
  try {
    const request = { params: { ...params }, query: Object.fromEntries(Object.entries(query).filter(([, value]) => value !== '')) }
    const headers: Record<string, string> = Object.fromEntries(Object.entries(requestHeaders).filter(([,value])=>value!==''))
    const jsonBody = operationBody(current, values, rawBody.value,rawMode.value)
    let requestBody: BodyInit | undefined = jsonBody
    if (current.transport === 'upload' || current.transport === 'multipart') {
      if (!file.value) throw new Error(t('operations.fileRequired'))
      requestBody = operationUpload(current,file.value)
    }
    if (current.requiresIdempotencyKey) {
      const signature = await requestIntent(current.name, request, requestBody, new Headers(headers))
      if (!headers['Idempotency-Key'] || (signature !== lastIntent && headers['Idempotency-Key'] === lastKey)) headers['Idempotency-Key'] = intentKey(signature)
      requestHeaders['Idempotency-Key'] = headers['Idempotency-Key']!
      lastIntent = signature; lastKey = headers['Idempotency-Key']!
      rememberSubmission({operation:current.name,key:lastKey,input:request})
    }
    if(generation !== revision)return
    const controller = new AbortController()
    requestAbort = controller
    if (current.transport === 'websocket' || current.transport === 'terminal') {
      let url: URL
      if (current.transport === 'terminal') {
        const signature=await requestIntent(current.name,request,requestBody,new Headers(headers))
        if(generation !== revision)return
        if(pendingTerminal.value && terminalIntent!==signature) {await operationRequest('terminal.cancel',{params:pendingTerminal.value});pendingTerminal.value=undefined}
        const ticket = pendingTerminal.value
          ? await operationRequest<{streamId:string;ticket:string}>('terminal.resume',{params:pendingTerminal.value},{body:'{}',signal:controller.signal})
          : await operationRequest<{ streamId: string; ticket: string }>(current.name, request, { body: requestBody, headers,signal:controller.signal })
        terminalIntent=signature
        streamId = ticket.streamId
        pendingTerminal.value = { nodeId: request.params.nodeId!, streamId: ticket.streamId }
        if (generation !== revision) {
          await operationRequest('terminal.cancel', { params: { nodeId: request.params.nodeId!, streamId: ticket.streamId } }); return
        }
        url = await operationURL('terminal.connect', { query: { ticket: ticket.ticket } })
      } else url = await operationURL(current.name, request)
      if (generation !== revision) return
      const connection = new WebSocket(url); socket = connection
      connection.onopen = () => { if (generation === revision) connected.value = true }
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
      const result = await operationRequest<unknown>(current.name, request, { body: requestBody, headers, signal: controller.signal }, current.transport === 'upload' ? 1800000 : 60000)
      if (generation !== revision) return
      if(current.requiresIdempotencyKey)forgetSubmission({operation:current.name,key:headers['Idempotency-Key']!,input:request})
      if (result instanceof Blob) {
        const url = URL.createObjectURL(result), link = document.createElement('a')
        link.href = url; link.download = (query.path || current.name).split('/').pop()!; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000)
        append(t('operations.downloaded'))
      } else {
        append(result ?? t('operations.completed'))
        if (current.completion) {
          const follow=completionInput(current,request,jsonBody,result)
          const pending={operation:current.name,...follow};if(!current.completion.terminalStatuses.includes(completionStatus(current,result)))rememberObservation(pending)
          if(waitForCompletion.value)await observe(current,pending,generation,controller)
        }
      }
    }
  } catch (reason) { if (generation === revision) error.value = reason instanceof Error ? reason.message : String(reason) }
  finally { if(generation === revision) busy.value = false }
}
async function observe(current:CoreOperation,pending:PendingObservation,generation:number,controller:AbortController){
 const follow=operations.value.find(o=>o.name===pending.name)
 if(!follow||!['GET','HEAD'].includes(follow.method))throw new Error('Completion must observe a discovered read operation')
 while(generation===revision){
  let observed:unknown
  try{observed=await operationRequest(pending.name,pending.input,{signal:controller.signal})}
  catch(reason){if(controller.signal.aborted)throw reason;if(reason&&typeof reason==='object'&&'status'in reason&&Number(reason.status)<500)throw reason;await new Promise(resolve=>setTimeout(resolve,1000));continue}
  if(generation!==revision)return
  append(observed)
  const status=completionStatus(current,observed)
  if(current.completion!.terminalStatuses.includes(status)){forgetObservation(pending);if(!current.completion!.successStatuses.includes(status))throw new Error(`${t('operations.finishedStatus')}: ${status}`);return}
  await new Promise(resolve=>setTimeout(resolve,1000))
 }
}
async function resumeObservation(pending:PendingObservation){
 if(busy.value)return
 const current=operations.value.find(o=>o.name===pending.operation)
 if(!current?.completion){error.value='Core no longer offers this completion contract';return}
 const generation=++revision;const controller=new AbortController();requestAbort=controller;busy.value=true;error.value='';output.value=''
 try{await observe(current,pending,generation,controller)}catch(reason){if(generation===revision)error.value=String(reason)}finally{if(generation===revision)busy.value=false}
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
function newExecution() {
  if(lastIntent) requestHeaders['Idempotency-Key']=intentKey(lastIntent,true)
  else requestHeaders['Idempotency-Key']=crypto.randomUUID()
  lastKey=requestHeaders['Idempotency-Key']!
}
onMounted(async () => { try { operations.value = (await operationCatalog()).operations
 try{const records=JSON.parse(sessionStorage.getItem(submissionStorage)??'[]');if(Array.isArray(records))pendingSubmissions.value=records.slice(-100).filter(v=>v&&typeof v.operation==='string'&&typeof v.key==='string'&&v.key.length<=200&&v.input&&typeof v.input==='object')}catch{}
 try{const items=JSON.parse(sessionStorage.getItem(observationStorage)??'[]');if(Array.isArray(items))pendingObservations.value=items.slice(-100).filter(v=>v&&typeof v.operation==='string'&&typeof v.name==='string'&&v.input&&typeof v.input==='object')}catch{} } catch (reason) { error.value = String(reason) } })
onBeforeUnmount(disconnect)
</script>

<template>
  <section class="panel operation-console">
    <h2>{{ t('nav.operations') }}</h2>
    <p class="muted">{{ t('operations.description') }}</p>
    <div v-if="pendingSubmissions.length" class="pending-observations"><p class="muted">{{ t('operations.uncertain') }}</p><div v-for="item in pendingSubmissions" :key="item.operation+item.key"><span>{{ item.operation }} · {{ Object.values({ ...item.input.params, ...item.input.query }).join(' / ') }}</span><button type="button" :disabled="busy" @click="restoreSubmission(item)">{{ t('operations.restoreKey') }}</button><button type="button" :disabled="busy" @click="forgetSubmission(item)">{{ t('operations.forget') }}</button></div></div>
    <div v-if="pendingObservations.length" class="pending-observations"><p class="muted">{{ t('operations.pending') }}</p><div v-for="item in pendingObservations" :key="observationKey(item)"><span>{{ item.operation }} · {{ Object.values({ ...item.input.params, ...item.input.query }).join(' / ') }}</span> <button type="button" :disabled="busy" @click="resumeObservation(item)">{{ t('operations.resume') }}</button> <button type="button" :disabled="busy" @click="forgetObservation(item)">{{ t('operations.forget') }}</button></div></div>
    <div class="operation-layout">
      <aside>
        <input v-model="filter" type="search" :placeholder="t('operations.search')" :aria-label="t('operations.search')" />
        <UiSelect v-model="selected" size="18" :aria-label="t('nav.operations')"><option v-for="item in matches" :key="item.name" :value="item.name">{{ item.name }}</option></UiSelect>
      </aside>
      <div v-if="operation" class="operation-form">
        <h3>{{ operation.name }}</h3><p class="muted">{{ operation.description }}</p>
        <form @submit.prevent="execute">
          <label v-for="name in operation.parameters" :key="name">{{ name }} <input v-model="params[name]" required :disabled="busy || connected" /></label>
          <label v-for="name in operation.query" :key="name">{{ name }} <input v-model="query[name]" :type="/ticket|token/i.test(name) ? 'password' : 'text'" :disabled="busy || connected" /></label>
          <label v-if="operation.bodySchema?.type==='object'" class="check-row"><input v-model="rawMode" type="checkbox" :disabled="busy" />{{ t('operations.rawJSON') }}</label>
          <label v-for="[name, schema] in properties" :key="name">{{ name }}{{ operation.bodySchema?.required?.includes(name) ? ' *' : '' }}
            <input v-if="schemaHasSecrets(schema)" v-model="values[name]" type="password" :disabled="busy || connected" autocomplete="off" />
            <UiSelect v-else-if="schema.enum || schema.type === 'boolean'" v-model="values[name]" :required="operation.bodySchema?.required?.includes(name)" :disabled="busy || connected"><option value="">—</option><option v-for="value in schema.enum ?? ['true', 'false']" :key="String(value)" :value="String(value)">{{ value }}</option></UiSelect>
            <textarea v-else-if="schema.type === 'object' || schema.type === 'array'" v-model="values[name]" rows="4" :required="operation.bodySchema?.required?.includes(name)" :disabled="busy || connected" />
            <input v-else v-model="values[name]" type="text" :inputmode="schema.type === 'integer' ? 'numeric' : schema.type === 'number' ? 'decimal' : undefined" :minlength="schema.minLength" :maxlength="schema.maxLength" :disabled="busy || connected" autocomplete="off" />
          </label>
          <label v-if="operation.bodySchema && (rawMode || operation.bodySchema.type !== 'object')">JSON <input v-if="schemaHasSecrets(operation.bodySchema)" v-model="rawBody" type="password" :disabled="busy" autocomplete="off" /><textarea v-else v-model="rawBody" rows="5" :disabled="busy" /></label>
          <label v-for="[name,schema] in Object.entries(operation.headers ?? {})" :key="name">{{ name }}<input v-model="requestHeaders[name]" :type="schemaHasSecrets(schema)?'password':'text'" :disabled="busy || connected" autocomplete="off" /><small>{{ schema.description }}</small></label>
          <template v-if="operation.requiresIdempotencyKey"><small>{{ t('operations.retryIntent') }}</small><button type="button" :disabled="busy || connected" @click="newExecution">{{ t('operations.newExecution') }}</button></template>
          <label v-if="operation.completion" class="check-row"><input v-model="waitForCompletion" type="checkbox" :disabled="busy" />{{ t('operations.wait') }}</label>
          <label v-if="['upload', 'multipart'].includes(operation.transport)">{{ t('operations.file') }}<input type="file" required :disabled="busy || connected" @change="file = ($event.target as HTMLInputElement).files?.[0]" /></label>
          <button type="submit" class="primary-button" :disabled="busy || connected">{{ busy ? t('operations.running') : t('operations.execute') }}</button>
          <button v-if="connected || pendingTerminal" type="button" @click="disconnect">{{ operation.transport==='terminal'?t('operations.endTerminal'):t('operations.disconnect') }}</button>
          <button v-if="busy" type="button" @click="disconnect">{{ t('operations.stopWaiting') }}</button>
        </form>
        <form v-if="connected && operation.duplex" @submit.prevent="send"><textarea v-if="operation.transport === 'websocket'" v-model="frame" rows="3" aria-label="JSON" /><input v-else v-model="input" :aria-label="t('operations.input')" /><button type="submit">{{ t('operations.send') }}</button></form>
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
.check-row { flex-direction:row; align-items:center; }.check-row input { width:18px; min-height:18px; }small { color:var(--muted); font-weight:400; line-height:1.6; }
pre { max-height: 600px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; border: 1px solid var(--line); background: var(--surface-soft); color: var(--text); padding: 18px; border-radius:var(--radius-card); font: 12px/1.7 ui-monospace, SFMono-Regular, Menlo, monospace; }
@media (max-width: 700px) { .operation-console { padding: 18px; } .operation-layout { grid-template-columns: 1fr; gap: 20px; } aside select { min-height: 170px; height: 170px; } }
</style>

<style scoped>
.pending-observations{display:grid;gap:8px;margin-block:16px}.pending-observations>div{display:flex;flex-wrap:wrap;align-items:center;gap:8px}.pending-observations span{flex-basis:100%;overflow-wrap:anywhere}
</style>
