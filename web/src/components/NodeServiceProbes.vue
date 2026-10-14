<script setup lang="ts">
import { requestConfirm } from '../dialogs'
import { t, display, locale } from "../i18n"
import { onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { api, operationRequest, type ServiceProbe, type ServiceProbePayload, type ServiceProbeRun } from '../api'

const props = defineProps<{ nodeId: string; nodeName: string; nodeOnline: boolean }>()

const probes = ref<ServiceProbe[]>([])
const history = ref<Record<string, ServiceProbeRun[]>>({})
const quality = ref<Record<string, {packetLossPercent?:number;jitterMs?:number;failurePercent?:number;meanLatencyMs?:number;p95LatencyMs?:number;unknown:number}>>({})
const visibleHistory = ref<Record<string, boolean>>({})
const loading = ref(false)
const saving = ref(false)
const deleting = ref('')
const error = ref('')
const notice = ref('')
const formVisible = ref(false)
const editingID = ref('')
const editingRevision = ref<number>()
let refreshTimer: ReturnType<typeof setInterval> | undefined

const form = reactive({
  name: '', kind: 'http' as ServiceProbe['kind'], target: '', expectedHttpStatus: 200,
  intervalSeconds: 30, timeoutSeconds: 3, enabled: true,
})

const nodeProbes = () => probes.value.filter((probe) => probe.nodeId === props.nodeId)

function resetForm() {
  editingID.value = ''
  editingRevision.value = undefined
  Object.assign(form, { name: '', kind: 'http', target: '', expectedHttpStatus: 200, intervalSeconds: 30, timeoutSeconds: 3, enabled: true })
}

async function refresh(silent = false) {
  if (!silent) loading.value = true
  try {
    const response = await api.serviceProbes()
    probes.value = Array.isArray(response.probes) ? response.probes : []
  } catch (reason) {
    if (!silent) error.value = message(reason)
  } finally {
    loading.value = false
  }
}

function beginCreate() {
  error.value = ''
  notice.value = ''
  resetForm()
  formVisible.value = true
}

function beginEdit(probe: ServiceProbe) {
  error.value = ''
  notice.value = ''
  editingID.value = probe.id
  editingRevision.value = probe.revision
  Object.assign(form, {
    name: probe.name, kind: probe.kind, target: probe.target,
    expectedHttpStatus: probe.expectedHttpStatus || 200,
    intervalSeconds: probe.intervalSeconds, timeoutSeconds: probe.timeoutSeconds, enabled: probe.enabled,
  })
  formVisible.value = true
}

function closeForm() {
  formVisible.value = false
  resetForm()
}

function payload(revision?: number): ServiceProbePayload {
  const value: ServiceProbePayload = {
    nodeId: props.nodeId, name: form.name.trim(), kind: form.kind, target: form.target.trim(),
    intervalSeconds: Number(form.intervalSeconds), timeoutSeconds: Number(form.timeoutSeconds), enabled: form.enabled,
  }
  if (['http','https'].includes(form.kind)) value.expectedHttpStatus = Number(form.expectedHttpStatus)
  if (revision !== undefined) value.revision = revision
  return value
}

async function save() {
  error.value = ''
  notice.value = ''
  saving.value = true
  try {
    if (editingID.value) await api.updateServiceProbe(editingID.value, payload(editingRevision.value))
    else await api.createServiceProbe(payload())
    closeForm()
    notice.value = t('m.c538fb4a37f3')
    await refresh(true)
  } catch (reason) {
    error.value = message(reason)
  } finally {
    saving.value = false
  }
}

async function toggleEnabled(probe: ServiceProbe) {
  error.value = ''
  notice.value = ''
  try {
    await api.updateServiceProbe(probe.id, {
      nodeId: probe.nodeId, name: probe.name, kind: probe.kind, target: probe.target,
      expectedHttpStatus: probe.kind === 'tcp' ? undefined : probe.expectedHttpStatus,
      intervalSeconds: probe.intervalSeconds, timeoutSeconds: probe.timeoutSeconds,
      enabled: !probe.enabled, revision: probe.revision,
    })
    notice.value = probe.enabled ? t('m.95f6288e13e9') : t('m.c5ad49ccd171')
    await refresh(true)
  } catch (reason) {
    error.value = message(reason)
  }
}

async function removeProbe(probe: ServiceProbe) {
  if (!await requestConfirm(t('m.cc16da771452', {"0": (probe.name)}))) return
  deleting.value = probe.id
  error.value = ''
  notice.value = ''
  try {
    await api.deleteServiceProbe(probe.id, probe.revision)
    probes.value = probes.value.filter((item) => item.id !== probe.id)
    notice.value = t('m.cbeed1058101')
  } catch (reason) {
    error.value = message(reason)
  } finally {
    deleting.value = ''
  }
}

async function toggleHistory(probe: ServiceProbe) {
  const visible = !visibleHistory.value[probe.id]
  visibleHistory.value = { ...visibleHistory.value, [probe.id]: visible }
  if (!visible) return
  try {
    const [result, stats] = await Promise.all([api.serviceProbeHistory(probe.id),operationRequest<{packetLossPercent?:number;jitterMs?:number;failurePercent?:number;meanLatencyMs?:number;p95LatencyMs?:number;unknown:number}>('probes.quality',{params:{id:probe.id}})])
 quality.value[probe.id]=stats
    history.value = { ...history.value, [probe.id]: result.runs }
  } catch (reason) {
    error.value = message(reason)
  }
}

function statusLabel(probe: ServiceProbe): string {
  if (!probe.enabled) return t('m.a8c3698b5b8c')
  if (!props.nodeOnline || probe.status === 'unknown') return t('m.4d8c1c5b4283')
  return probe.status === 'healthy' ? t('m.296de0e31f8c') : t('m.428fb8bfeecf')
}

function errorLabel(code?: string): string {
  const labels: Record<string, string> = {
    node_offline: t('m.badb7a4e8552'), capability_unavailable: t('m.9f746c0f8354'), dispatch_unavailable: t('m.d740821f830c'),
    dispatch_timeout: t('m.71c8b847fdb7'), core_restarted: t('m.a0b5d6b0691a'), capacity: t('m.f96946fb2910'),
    timeout: t('m.ae5ead4d8907'), tls_verification: t('m.cb33e695a138'), http_status_mismatch: t('m.13ba30326078'),
    dns: t('m.3a577c9dabb1'), connection_failed: t('m.89c4766afc85'), cancelled: t('m.0294852f2c2c'),
  }
  return code ? labels[code] || t('m.c318cf5f0af4') : ''
}

function formatTime(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString(locale.value)
}

function message(reason: unknown): string {
  return reason instanceof Error && reason.message ? reason.message : t('m.ebf63ef228d2')
}

watch(() => props.nodeId, () => {
  error.value = ''
  notice.value = ''
  formVisible.value = false
  void refresh()
})

watch(() => form.kind, (kind) => {
 const old=form.target.replace(/^(https?|tcp|icmp):\/\//,'')
 if(kind==='icmp')form.target=`icmp://${old.split('/')[0]?.split(':')[0] ?? ''}`
 else form.target=`${kind}://${old}`
})

onMounted(() => {
  void refresh()
  refreshTimer = setInterval(() => { void refresh(true) }, 5000)
})

onBeforeUnmount(() => { if (refreshTimer !== undefined) clearInterval(refreshTimer) })
</script>

<template>
  <section class="service-probes" aria-labelledby="service-probes-heading" data-testid="service-probes">
    <header class="probe-heading">
      <div><span class="eyebrow">SERVICE CHECKS · {{ nodeName }}</span><h2 id="service-probes-heading">{{ t('m.3fefdc07fa14') }}</h2><p>{{ t('m.46359b5cb550') }}</p></div>
      <button class="probe-primary" type="button" @click="beginCreate">{{ t('m.41e755cbc3c5') }}</button>
    </header>
    <p v-if="!nodeOnline" class="probe-note" role="status">{{ t('m.12dc4fd08dbb') }}</p>
    <div v-if="error" class="probe-message probe-error" role="alert">{{ display(error) }}</div>
    <div v-if="notice" class="probe-message probe-notice" role="status">{{ display(notice) }}</div>

    <form v-if="formVisible" class="probe-form" data-testid="probe-form" @submit.prevent="save">
      <h3>{{ editingID ? t('m.5ffc6380c433') : t('m.4a7abfcfad36') }}</h3>
      <div class="probe-fields">
        <label><span>{{ t('m.d44e9b3d3b31') }}</span><input v-model="form.name" maxlength="80" required :placeholder="t('m.bd0242098b8e')" /></label>
        <label><span>{{ t('m.ab2f31f30acf') }}</span><UiSelect v-model="form.kind"><option value="http">HTTP</option><option value="https">HTTPS</option><option value="tcp">TCP</option><option value="icmp">ICMP</option></UiSelect></label>
        <label class="probe-target"><span>{{ t('m.fb60c9043c7a') }}</span><input v-model="form.target" required maxlength="2048" :placeholder="form.kind === 'tcp' ? 'tcp://127.0.0.1:5432' : `${form.kind}://127.0.0.1:8080/health`" /></label>
        <label v-if="['http','https'].includes(form.kind)"><span>{{ t('m.3f63bb98cd18') }}</span><input v-model.number="form.expectedHttpStatus" type="number" min="100" max="599" required /></label>
        <label><span>{{ t('m.455adcae032d') }}</span><input v-model.number="form.intervalSeconds" type="number" min="10" max="86400" required /></label>
        <label><span>{{ t('m.e814e078437b') }}</span><input v-model.number="form.timeoutSeconds" type="number" min="1" max="30" required /></label>
      </div>
      <label class="probe-enabled"><input v-model="form.enabled" type="checkbox" /><span>{{ t('m.6cdc68ea4c5f') }}</span></label>
      <p class="probe-help">{{ t('m.898892992159') }}</p>
      <div class="probe-form-actions"><button class="probe-primary" type="submit" :disabled="saving">{{ saving ? t('m.6bdb4435095e') : t('m.6e584e3d5ce6') }}</button><button class="probe-secondary" type="button" :disabled="saving" @click="closeForm">{{ t('m.2cd0f3be8738') }}</button></div>
    </form>

    <p v-if="loading && !probes.length" class="probe-empty" role="status">{{ t('m.e7985076d5fd') }}</p>
    <p v-else-if="nodeProbes().length === 0" class="probe-empty">{{ loading ? t('m.86b6d0d63062') : t('m.e0ad4994de04') }}</p>
    <div v-else class="probe-list">
      <article v-for="probe in nodeProbes()" :key="probe.id" class="probe-card" :data-probe-id="probe.id">
        <div class="probe-card-heading"><div class="probe-copy"><strong>{{ probe.name }}</strong><code>{{ probe.kind.toUpperCase() }} · {{ probe.target }}</code></div><span class="probe-status" :data-status="!nodeOnline ? 'unknown' : probe.enabled ? probe.status : 'disabled'">{{ statusLabel(probe) }}</span></div>
        <div class="probe-meta"><span>{{ t('m.4f5df723d99d') }}{{ probe.intervalSeconds }}{{ t('m.9dcdc2b289b9') }}</span><span>{{ t('m.e512cf016f96') }}{{ probe.timeoutSeconds }}{{ t('m.9dcdc2b289b9') }}</span><span v-if="['http','https'].includes(probe.kind)">{{ t('m.ed1f74c64ac9') }}{{ probe.expectedHttpStatus }}</span><span>{{ t('m.28d75d9db816') }}{{ formatTime(probe.lastCheckedAt) }}</span><span v-if="probe.lastErrorCode">{{ errorLabel(probe.lastErrorCode) }}</span></div>
        <div class="probe-actions"><button type="button" @click="toggleHistory(probe)">{{ visibleHistory[probe.id] ? t('m.a9f4bab69211') : t('m.a5783b941d7d') }}</button><button type="button" @click="beginEdit(probe)">{{ t('m.051836569928') }}</button><button type="button" @click="toggleEnabled(probe)">{{ probe.enabled ? t('m.4e6fd0e28c55') : t('m.f4f0ead1116b') }}</button><button type="button" class="probe-delete" :disabled="deleting === probe.id" @click="removeProbe(probe)">{{ deleting === probe.id ? t('m.4fd4f40094d4') : t('m.2f9daa828907') }}</button></div>
        <p v-if="visibleHistory[probe.id] && quality[probe.id]" class="probe-meta">{{ t('host.quality') }} · {{ t('host.failure') }}: {{ quality[probe.id]?.failurePercent?.toFixed(1) ?? '—' }}% · {{ t('host.packetLoss') }}: {{ quality[probe.id]?.packetLossPercent?.toFixed(1) ?? '—' }}% · {{ t('host.jitter') }}: {{ quality[probe.id]?.jitterMs?.toFixed(1) ?? '—' }} ms · P95: {{ quality[probe.id]?.p95LatencyMs ?? '—' }} ms · {{ t('host.unknown') }}: {{ quality[probe.id]?.unknown }}</p>
        <ol v-if="visibleHistory[probe.id]" class="probe-history" :aria-label="t('m.34392b76904a', {'0': (probe.name)})">
          <li v-for="run in history[probe.id] || []" :key="run.runId" :data-status="run.status"><span>{{ formatTime(run.checkedAt) }}</span><strong>{{ run.status === 'healthy' ? t('m.296de0e31f8c') : run.status === 'unhealthy' ? t('m.428fb8bfeecf') : run.status === 'pending' ? t('m.33037aadf043') : t('m.4d8c1c5b4283') }}</strong><span v-if="run.httpStatus">HTTP {{ run.httpStatus }}</span><span v-if="run.latencyMs !== undefined">{{ run.latencyMs }} ms</span><span v-if="run.errorCode">{{ errorLabel(run.errorCode) }}</span></li>
          <li v-if="history[probe.id]?.length === 0" class="probe-no-history">{{ t('m.140d051bd612') }}</li>
        </ol>
      </article>
    </div>
  </section>
</template>

<style scoped>
.service-probes { min-width: 0; margin-top: 20px; border: 1px solid var(--line); border-radius:var(--radius-card); padding: clamp(14px,2vw,20px); background: var(--surface-soft); color: var(--text); }
.probe-heading { display: flex; justify-content: space-between; align-items: center; gap: 14px; margin-bottom: 14px; }
.probe-heading h2 { margin: 4px 0 0; font-size: 17px; }
.probe-heading p,.probe-help,.probe-note,.probe-empty { color: var(--muted-strong); font-size: 12px; line-height: 1.6; }
.probe-heading p { margin: 6px 0 0; }
.probe-primary,.probe-secondary,.probe-actions button { min-height:40px; border: 1px solid var(--line); border-radius:var(--radius-control); padding: 6px 10px; color: var(--accent); background: var(--line); font: inherit; font-size: 12px; cursor: pointer; }
.probe-primary:hover:not(:disabled),.probe-actions button:hover:not(:disabled) { background: var(--line); }
.probe-primary:disabled,.probe-secondary:disabled,.probe-actions button:disabled { opacity: .5; cursor: not-allowed; }
.probe-secondary { color: var(--muted-strong); border-color: var(--line); background: var(--line); }
.probe-note,.probe-empty { margin: 10px 0; }
.probe-message { margin: 10px 0; border-radius:var(--radius-card); padding: 9px 11px; font-size: 12px; overflow-wrap: anywhere; }
.probe-error { color: var(--danger); border: 1px solid var(--danger-line); background: var(--danger-soft); }
.probe-notice { color: var(--success); border: 1px solid var(--success-line); background: var(--success-soft); }
.probe-form { margin: 12px 0 16px; border: 1px solid var(--line); border-radius:var(--radius-card); padding: 13px; background: var(--surface-soft); }
.probe-form h3 { margin: 0 0 12px; font-size: 13px; }
.probe-fields { display: grid; grid-template-columns: repeat(3,minmax(0,1fr)); gap: 11px; }
.probe-fields label { display: grid; min-width: 0; gap: 5px; color: var(--muted-strong); font-size: 12px; }
.probe-fields .probe-target { grid-column: span 2; }
.probe-fields input,.probe-fields select { box-sizing: border-box; width: 100%; min-width: 0; min-height:40px; border: 1px solid var(--line); border-radius:var(--radius-control); padding: 7px 9px; color: var(--text); background: var(--surface-soft); font: inherit; font-size: 12px; }
.probe-enabled { display: inline-flex; align-items: center; gap: 7px; margin-top: 12px; color: var(--text); font-size: 12px; }
.probe-help { margin: 7px 0; }
.probe-form-actions,.probe-actions { display: flex; flex-wrap: wrap; gap: 7px; }
.probe-list { display: grid; gap: 9px; }
.probe-card { min-width: 0; border: 1px solid var(--line); border-radius:var(--radius-card); padding: 12px; background: var(--surface-soft); }
.probe-card-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.probe-copy { display: grid; min-width: 0; gap: 5px; }
.probe-copy strong { overflow-wrap: anywhere; font-size: 12px; }
.probe-copy code { overflow-wrap: anywhere; color: var(--accent); font-size: 12px; }
.probe-status { flex: none; border: 1px solid var(--line); border-radius: 999px; padding: 5px 9px; color: var(--text); font-size: 12px; }
.probe-status[data-status='healthy'] { color: var(--success); border-color: var(--success-line); }
.probe-status[data-status='unhealthy'] { color: var(--danger); border-color: var(--danger-line); }
.probe-status[data-status='unknown'],.probe-status[data-status='disabled'] { color: var(--warning); border-color: var(--warning-line); }
.probe-meta { display: flex; flex-wrap: wrap; gap: 6px 13px; margin: 9px 0; color: var(--muted-strong); font-size: 12px; }
.probe-actions .probe-delete { color: var(--danger); border-color: var(--danger-line); background: var(--danger-soft); }
.probe-history { display: grid; gap: 5px; margin: 10px 0 0; padding: 9px 0 0; border-top: 1px solid var(--line); list-style: none; }
.probe-history li { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 4px 10px; color: var(--muted-strong); font-size: 12px; }
.probe-history li[data-status='healthy'] strong { color: var(--success); }
.probe-history li[data-status='unhealthy'] strong { color: var(--danger); }
.probe-no-history { color: var(--muted-strong); }
@media (max-width: 760px) { .probe-fields { grid-template-columns: repeat(2,minmax(0,1fr)); } .probe-fields .probe-target { grid-column: span 2; } }
@media (max-width: 480px) { .probe-heading { align-items: flex-start; flex-direction: column; } .probe-fields { grid-template-columns: minmax(0,1fr); } .probe-fields .probe-target { grid-column: auto; } .probe-card-heading { align-items: flex-start; } .probe-actions button { flex: 1 1 auto; } }
</style>
