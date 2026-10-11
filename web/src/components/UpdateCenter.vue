<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { api, operationRequest, type AgentNode } from '../api'
import { t } from '../i18n'
interface ReleaseCheck { component: string; current: string; latest: string; available: boolean; configured: boolean; compatible: boolean; mode?: string; reason?: string; release: { url: string; notes: string } }
interface Job { id: string; component: string; current: string; target: string; status: string; stage: string; error?: string; started: string; finished?: string; backup?: string }
const component = ref('core'), nodeId = ref(''), version = ref('')
const nodes = ref<AgentNode[]>([]), check = ref<ReleaseCheck>(), jobs = ref<Job[]>([])
const pollError = ref('')
const error = ref(''), busy = ref(false), confirmed = ref(false), rollbackJob = ref('')
const selected = computed(() => component.value === 'core' || !!nodeId.value)
const query = computed(() => ({ component: component.value, ...(component.value === 'agent' ? { nodeId: nodeId.value } : {}) }))
let timer: ReturnType<typeof setTimeout> | undefined
let stopped = false, epoch = 0
function message(e: unknown) { return e instanceof Error ? e.message : String(e) }
async function history() {
 if (!selected.value) return
 const current = epoch
 try { const result = await operationRequest<{ jobs: Job[] }>('updates.status', { query: query.value }); if (current === epoch) { const completed = result.jobs.some(job => job.finished && jobs.value.some(previous => previous.id === job.id && !previous.finished)); jobs.value = result.jobs; pollError.value = ''; if (completed) void refresh() } }
 catch (e) { if (current === epoch) pollError.value = message(e) }
 finally { if (!stopped && current === epoch) timer = setTimeout(history, 3000) }
}
async function refresh() {
 if (!selected.value) return
 check.value = undefined; confirmed.value = false; error.value = ''; busy.value = true
 const current = epoch
 try { const result = await operationRequest<ReleaseCheck>('updates.check', { query: { ...query.value, ...(version.value ? { version: version.value } : {}) } }); if (current === epoch) check.value = result }
 catch (e) { if (current === epoch) error.value = message(e) } finally { if (current === epoch) busy.value = false }
}
async function submit() {
 if (!confirmed.value) return
 busy.value = true; error.value = ''
 const action = rollbackJob.value ? 'rollback' : 'apply'
 const body = { ...query.value, confirm: true, ...(rollbackJob.value ? { jobId: rollbackJob.value } : { version: check.value?.latest }) }
 // Keep the key for retry if Core/Agent restarts before the response arrives.
 const keyName = 'nodedance-update:' + JSON.stringify(body)
 const key = sessionStorage.getItem(keyName) ?? crypto.randomUUID()
 sessionStorage.setItem(keyName, key)
 try { const job = await operationRequest<Job>('updates.' + action, {}, { body: JSON.stringify(body), headers: { 'Idempotency-Key': key } }); jobs.value = [job, ...jobs.value.filter(item => item.id !== job.id)]; sessionStorage.removeItem(keyName); confirmed.value = false; rollbackJob.value = ''; if (job.finished) void refresh() }
 catch (e) { error.value = message(e) } finally { busy.value = false }
}
watch(query, () => { epoch++; check.value = undefined; jobs.value = []; rollbackJob.value = ''; confirmed.value = false; if (timer) clearTimeout(timer); void history(); void refresh() })
onMounted(async () => { try { nodes.value = (await api.nodes()).nodes; nodeId.value = nodes.value[0]?.nodeId ?? '' } catch (e) { error.value = message(e) }; if (timer) clearTimeout(timer); void history(); void refresh() })
onBeforeUnmount(() => { stopped = true; epoch++; if (timer) clearTimeout(timer) })
</script>

<template>
<section class="update-center">
 <header><p class="eyebrow">NODEDANCE</p><h1>{{ t('updates.title') }}</h1><p class="muted">{{ t('updates.description') }}</p></header>
 <div class="update-card">
  <div class="update-controls">
   <label>{{ t('updates.component') }}<select v-model="component"><option value="core">Core</option><option value="agent">Agent</option></select></label>
   <label v-if="component === 'agent'">{{ t('updates.node') }}<select v-model="nodeId"><option v-for="node in nodes" :key="node.nodeId" :value="node.nodeId">{{ node.displayName }} · {{ node.status }}</option></select></label>
   <label>{{ t('updates.version') }}<input v-model="version" :placeholder="t('updates.latest')"></label>
   <button class="secondary-button" :disabled="busy || !selected" @click="refresh">{{ t('updates.check') }}</button>
  </div>
  <p v-if="pollError" class="muted" role="status">{{ pollError }}</p>
  <p v-if="error" role="alert" class="update-error">{{ error }}</p>
  <div v-if="check" class="update-details">
   <div><span class="muted">{{ t('updates.current') }}</span><strong>{{ check.current || '—' }}</strong></div>
   <div><span class="muted">{{ t('updates.latest') }}</span><strong>{{ check.latest }}</strong></div>
   <div><span class="muted">{{ t('updates.deployment') }}</span><strong>{{ check.mode || '—' }}</strong></div>
   <p v-if="check.reason" class="update-error">{{ check.reason }}</p>
   <p v-if="!check.configured">{{ t('updates.install') }}</p>
   <p v-else-if="!check.available">{{ t('updates.currentNotice') }}</p>
   <a :href="check.release.url" target="_blank" rel="noopener noreferrer">{{ t('updates.notes') }}</a>
   <details><summary>{{ t('updates.notes') }}</summary><pre>{{ check.release.notes }}</pre></details>
  </div>
  <div v-if="rollbackJob || check?.available && check.configured && check.compatible" class="update-confirm">
   <p>{{ rollbackJob ? t('updates.rollbackWarning') : t('updates.updateWarning') }}</p>
   <label><input v-model="confirmed" type="checkbox"> {{ t('updates.confirm') }}</label>
   <button class="primary-button" :disabled="busy || !confirmed" @click="submit">{{ rollbackJob ? t('updates.rollback') : t('updates.apply') }}</button>
   <button class="secondary-button" v-if="rollbackJob" @click="rollbackJob = ''; confirmed = false">{{ t('updates.cancel') }}</button>
  </div>
 </div>
 <div class="update-card"><h2>{{ t('updates.history') }}</h2>
  <p v-if="!jobs.length" class="muted">{{ t('updates.empty') }}</p>
  <article v-for="job in jobs" :key="job.id" class="update-job">
   <div><strong>{{ job.current }} → {{ job.target }}</strong><span>{{ job.status }} · {{ job.stage }}</span></div>
   <p class="muted">{{ new Date(job.started).toLocaleString() }} · {{ job.id }}</p>
   <p v-if="job.error" class="update-error">{{ job.error }}</p>
   <button class="secondary-button" v-if="job.status === 'succeeded' && job.stage === 'complete'" :disabled="busy" @click="rollbackJob = job.id; confirmed = false">{{ t('updates.rollback') }}</button>
  </article>
 </div>
</section>
</template>
<style scoped>
.update-center { display:grid; gap:24px; max-width:1060px; margin:0 auto; padding:32px; }
.update-center h1 { margin:8px 0 16px; font-family:var(--display-font); font-size:36px; font-weight:400; line-height:1.15; letter-spacing:-.5px; }
.update-center h2 { margin:0 0 20px; font-size:28px; line-height:1.2; }
.update-center header>p:last-child { margin:0; font-size:16px; line-height:1.55; }
.update-card { padding:32px; border:1px solid var(--line); border-radius:var(--radius-card); background:var(--surface); }
.update-controls { display:flex; align-items:end; flex-wrap:wrap; gap:16px; }
.update-controls label { display:grid; gap:8px; flex:1; min-width:160px; font-size:14px; font-weight:500; }
.update-controls input,.update-controls select { min-height:40px; width:100%; border:1px solid var(--line); border-radius:var(--radius-control); padding:10px 14px; color:var(--text); background:var(--canvas); font:inherit; font-weight:400; }
.update-controls :is(input,select):focus-visible { border-color:var(--accent); outline:none; box-shadow:0 0 0 3px var(--accent-soft); }
.update-details { display:grid; gap:16px; margin-top:32px; font-size:14px; line-height:1.55; }
.update-details>div { display:flex; justify-content:space-between; gap:16px; }
.update-details a { color:var(--accent); }
.update-details summary { cursor:pointer; }
.update-details pre { white-space:pre-wrap; overflow-wrap:anywhere; font:inherit; }
.update-center strong { font-weight:500; }
.update-confirm { display:flex; flex-wrap:wrap; align-items:center; gap:16px; margin-top:24px; padding-top:24px; border-top:1px solid var(--line); font-size:14px; line-height:1.55; }
.update-confirm p { width:100%; margin:0; }
.update-confirm label { display:flex; align-items:center; gap:8px; min-height:40px; }
.update-confirm input { width:16px; height:16px; margin:0; }
.update-job { padding:24px 0; border-top:1px solid var(--line); font-size:14px; line-height:1.55; }
.update-job>div { display:flex; justify-content:space-between; gap:12px; flex-wrap:wrap; }
.update-job p { overflow-wrap:anywhere; }
.update-error { color:var(--danger); }
.muted { color:var(--muted); }
@media(max-width:768px) { .update-center { padding:16px; }.update-card { padding:24px; }.update-controls { display:grid; }.update-center h1 { font-size:32px; } }
</style>
