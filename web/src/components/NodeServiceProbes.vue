<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { api, type ServiceProbe, type ServiceProbePayload, type ServiceProbeRun } from '../api'

const props = defineProps<{ nodeId: string; nodeName: string; nodeOnline: boolean }>()

const probes = ref<ServiceProbe[]>([])
const history = ref<Record<string, ServiceProbeRun[]>>({})
const visibleHistory = ref<Record<string, boolean>>({})
const loading = ref(false)
const saving = ref(false)
const deleting = ref('')
const error = ref('')
const notice = ref('')
const formVisible = ref(false)
const editingID = ref('')
let refreshTimer: ReturnType<typeof setInterval> | undefined

const form = reactive({
  name: '', kind: 'http' as ServiceProbe['kind'], target: '', expectedHttpStatus: 200,
  intervalSeconds: 30, timeoutSeconds: 3, enabled: true,
})

const nodeProbes = () => probes.value.filter((probe) => probe.nodeId === props.nodeId)

function resetForm() {
  editingID.value = ''
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
  if (form.kind !== 'tcp') value.expectedHttpStatus = Number(form.expectedHttpStatus)
  if (revision !== undefined) value.revision = revision
  return value
}

async function save() {
  error.value = ''
  notice.value = ''
  if (!form.name.trim() || !form.target.trim()) {
    error.value = '请填写探测名称和目标地址。'
    return
  }
  if (form.timeoutSeconds < 1 || form.intervalSeconds < 10 || form.intervalSeconds > 86400 || form.timeoutSeconds >= form.intervalSeconds) {
    error.value = '间隔须为 10–86400 秒，超时须为 1–30 秒且小于间隔。'
    return
  }
  saving.value = true
  try {
    const selected = probes.value.find((probe) => probe.id === editingID.value)
    if (editingID.value && selected) await api.updateServiceProbe(editingID.value, payload(selected.revision))
    else await api.createServiceProbe(payload())
    closeForm()
    notice.value = '探测配置已保存；状态由目标节点上的 Agent 按计划更新。'
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
    notice.value = probe.enabled ? '探测已停用，历史记录已保留。' : '探测已启用，将从此节点执行。'
    await refresh(true)
  } catch (reason) {
    error.value = message(reason)
  }
}

async function removeProbe(probe: ServiceProbe) {
  if (!window.confirm(`删除“${probe.name}”配置？已保存的历史记录将保留。`)) return
  deleting.value = probe.id
  error.value = ''
  notice.value = ''
  try {
    await api.deleteServiceProbe(probe.id, probe.revision)
    probes.value = probes.value.filter((item) => item.id !== probe.id)
    notice.value = '探测已删除，历史记录仍可查看。'
  } catch (reason) {
    error.value = message(reason)
  } finally {
    deleting.value = ''
  }
}

async function toggleHistory(probe: ServiceProbe) {
  const visible = !visibleHistory.value[probe.id]
  visibleHistory.value = { ...visibleHistory.value, [probe.id]: visible }
  if (!visible || history.value[probe.id]) return
  try {
    const result = await api.serviceProbeHistory(probe.id)
    history.value = { ...history.value, [probe.id]: result.runs }
  } catch (reason) {
    error.value = message(reason)
  }
}

function statusLabel(probe: ServiceProbe): string {
  if (!probe.enabled) return '已停用'
  if (!props.nodeOnline || probe.status === 'unknown') return '未知'
  return probe.status === 'healthy' ? '正常' : '异常'
}

function errorLabel(code?: string): string {
  const labels: Record<string, string> = {
    node_offline: '节点离线', capability_unavailable: 'Agent 不支持探测', dispatch_unavailable: 'Agent 暂不可调度',
    dispatch_timeout: '等待 Agent 结果超时', core_restarted: 'Core 重启后结果待确认', capacity: 'Agent 探测并发已满',
    timeout: '连接超时', tls_verification: 'TLS 证书验证失败', http_status_mismatch: 'HTTP 状态码不匹配',
    dns: 'DNS 解析失败', connection_failed: '连接失败', cancelled: '探测已取消',
  }
  return code ? labels[code] || '探测失败' : ''
}

function formatTime(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString()
}

function message(reason: unknown): string {
  return reason instanceof Error && reason.message ? reason.message : '探测操作失败，请稍后重试。'
}

watch(() => props.nodeId, () => {
  error.value = ''
  notice.value = ''
  formVisible.value = false
  void refresh()
})

watch(() => form.kind, (kind) => {
  if (kind === 'tcp' && !form.target.startsWith('tcp://')) form.target = form.target ? `tcp://${form.target.replace(/^https?:\/\//, '')}` : ''
  if (kind !== 'tcp' && form.target.startsWith('tcp://')) form.target = `http${kind === 'https' ? 's' : ''}://${form.target.slice(6)}`
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
      <div><span class="eyebrow">SERVICE CHECKS · {{ nodeName }}</span><h2 id="service-probes-heading">服务探测</h2><p>探测从此节点的 Agent 发起；目标节点离线时状态显示为未知。</p></div>
      <button class="probe-primary" type="button" @click="beginCreate">添加探测</button>
    </header>
    <p v-if="!nodeOnline" class="probe-note" role="status">此节点当前离线，探测结果不会被记作失败。</p>
    <div v-if="error" class="probe-message probe-error" role="alert">{{ error }}</div>
    <div v-if="notice" class="probe-message probe-notice" role="status">{{ notice }}</div>

    <form v-if="formVisible" class="probe-form" data-testid="probe-form" @submit.prevent="save">
      <h3>{{ editingID ? '编辑探测' : '新增探测' }}</h3>
      <div class="probe-fields">
        <label><span>名称</span><input v-model="form.name" maxlength="80" required placeholder="例如：主站健康检查" /></label>
        <label><span>协议</span><select v-model="form.kind"><option value="http">HTTP</option><option value="https">HTTPS</option><option value="tcp">TCP</option></select></label>
        <label class="probe-target"><span>目标地址</span><input v-model="form.target" required maxlength="2048" :placeholder="form.kind === 'tcp' ? 'tcp://127.0.0.1:5432' : `${form.kind}://127.0.0.1:8080/health`" /></label>
        <label v-if="form.kind !== 'tcp'"><span>预期 HTTP 状态码</span><input v-model.number="form.expectedHttpStatus" type="number" min="100" max="599" required /></label>
        <label><span>探测间隔（秒）</span><input v-model.number="form.intervalSeconds" type="number" min="10" max="86400" required /></label>
        <label><span>超时（秒）</span><input v-model.number="form.timeoutSeconds" type="number" min="1" max="30" required /></label>
      </div>
      <label class="probe-enabled"><input v-model="form.enabled" type="checkbox" /><span>启用定时探测</span></label>
      <p class="probe-help">TLS 证书验证默认开启。失败 3 次后标记异常，成功 2 次后恢复正常。</p>
      <div class="probe-form-actions"><button class="probe-primary" type="submit" :disabled="saving">{{ saving ? '正在保存…' : '保存配置' }}</button><button class="probe-secondary" type="button" :disabled="saving" @click="closeForm">取消</button></div>
    </form>

    <p v-if="loading && !probes.length" class="probe-empty" role="status">正在读取服务探测配置…</p>
    <p v-else-if="nodeProbes().length === 0" class="probe-empty">{{ loading ? '正在读取…' : '此节点还没有服务探测。' }}</p>
    <div v-else class="probe-list">
      <article v-for="probe in nodeProbes()" :key="probe.id" class="probe-card" :data-probe-id="probe.id">
        <div class="probe-card-heading"><div class="probe-copy"><strong>{{ probe.name }}</strong><code>{{ probe.kind.toUpperCase() }} · {{ probe.target }}</code></div><span class="probe-status" :data-status="!nodeOnline ? 'unknown' : probe.enabled ? probe.status : 'disabled'">{{ statusLabel(probe) }}</span></div>
        <div class="probe-meta"><span>间隔 {{ probe.intervalSeconds }} 秒</span><span>超时 {{ probe.timeoutSeconds }} 秒</span><span v-if="probe.kind !== 'tcp'">期望 {{ probe.expectedHttpStatus }}</span><span>最近检查 {{ formatTime(probe.lastCheckedAt) }}</span><span v-if="probe.lastErrorCode">{{ errorLabel(probe.lastErrorCode) }}</span></div>
        <div class="probe-actions"><button type="button" @click="toggleHistory(probe)">{{ visibleHistory[probe.id] ? '收起历史' : '查看历史' }}</button><button type="button" @click="beginEdit(probe)">编辑</button><button type="button" @click="toggleEnabled(probe)">{{ probe.enabled ? '停用' : '启用' }}</button><button type="button" class="probe-delete" :disabled="deleting === probe.id" @click="removeProbe(probe)">{{ deleting === probe.id ? '正在删除…' : '删除' }}</button></div>
        <ol v-if="visibleHistory[probe.id]" class="probe-history" :aria-label="`${probe.name} 探测历史`">
          <li v-for="run in history[probe.id] || []" :key="run.runId" :data-status="run.status"><span>{{ formatTime(run.checkedAt) }}</span><strong>{{ run.status === 'healthy' ? '正常' : run.status === 'unhealthy' ? '异常' : run.status === 'pending' ? '等待结果' : '未知' }}</strong><span v-if="run.httpStatus">HTTP {{ run.httpStatus }}</span><span v-if="run.latencyMs !== undefined">{{ run.latencyMs }} ms</span><span v-if="run.errorCode">{{ errorLabel(run.errorCode) }}</span></li>
          <li v-if="history[probe.id]?.length === 0" class="probe-no-history">暂无已完成的探测记录。</li>
        </ol>
      </article>
    </div>
  </section>
</template>

<style scoped>
.service-probes { min-width: 0; margin-top: 20px; border: 1px solid rgba(171,196,232,.13); border-radius: 12px; padding: clamp(14px,2vw,20px); background: rgba(9,17,29,.46); color: #eaf0fa; }
.probe-heading { display: flex; justify-content: space-between; align-items: center; gap: 14px; margin-bottom: 14px; }
.probe-heading h2 { margin: 4px 0 0; font-size: 17px; }
.probe-heading p,.probe-help,.probe-note,.probe-empty { color: #9aabc1; font-size: 11px; line-height: 1.6; }
.probe-heading p { margin: 6px 0 0; }
.probe-primary,.probe-secondary,.probe-actions button { min-height: 34px; border: 1px solid rgba(141,201,255,.25); border-radius: 7px; padding: 6px 10px; color: #cce6ff; background: rgba(62,119,170,.16); font: inherit; font-size: 10px; cursor: pointer; }
.probe-primary:hover:not(:disabled),.probe-actions button:hover:not(:disabled) { background: rgba(62,119,170,.3); }
.probe-primary:disabled,.probe-secondary:disabled,.probe-actions button:disabled { opacity: .5; cursor: not-allowed; }
.probe-secondary { color: #b5c1d2; border-color: rgba(171,196,232,.16); background: rgba(171,196,232,.06); }
.probe-note,.probe-empty { margin: 10px 0; }
.probe-message { margin: 10px 0; border-radius: 7px; padding: 9px 11px; font-size: 11px; overflow-wrap: anywhere; }
.probe-error { color: #ffc1b8; border: 1px solid rgba(255,129,116,.24); background: rgba(184,77,72,.1); }
.probe-notice { color: #9ce0b7; border: 1px solid rgba(121,214,156,.2); background: rgba(80,186,119,.08); }
.probe-form { margin: 12px 0 16px; border: 1px solid rgba(141,201,255,.2); border-radius: 9px; padding: 13px; background: rgba(18,29,45,.62); }
.probe-form h3 { margin: 0 0 12px; font-size: 13px; }
.probe-fields { display: grid; grid-template-columns: repeat(3,minmax(0,1fr)); gap: 11px; }
.probe-fields label { display: grid; min-width: 0; gap: 5px; color: #aab8cb; font-size: 10px; }
.probe-fields .probe-target { grid-column: span 2; }
.probe-fields input,.probe-fields select { box-sizing: border-box; width: 100%; min-width: 0; min-height: 36px; border: 1px solid rgba(171,196,232,.16); border-radius: 6px; padding: 7px 9px; color: #edf3fb; background: rgba(7,13,23,.72); font: inherit; font-size: 11px; }
.probe-enabled { display: inline-flex; align-items: center; gap: 7px; margin-top: 12px; color: #c7d2e2; font-size: 10px; }
.probe-help { margin: 7px 0; }
.probe-form-actions,.probe-actions { display: flex; flex-wrap: wrap; gap: 7px; }
.probe-list { display: grid; gap: 9px; }
.probe-card { min-width: 0; border: 1px solid rgba(171,196,232,.1); border-radius: 9px; padding: 12px; background: rgba(18,29,45,.55); }
.probe-card-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.probe-copy { display: grid; min-width: 0; gap: 5px; }
.probe-copy strong { overflow-wrap: anywhere; font-size: 12px; }
.probe-copy code { overflow-wrap: anywhere; color: #8dc9ff; font-size: 10px; }
.probe-status { flex: none; border: 1px solid rgba(171,196,232,.16); border-radius: 999px; padding: 5px 9px; color: #c4d0e0; font-size: 10px; }
.probe-status[data-status='healthy'] { color: #9ce0b7; border-color: rgba(121,214,156,.28); }
.probe-status[data-status='unhealthy'] { color: #ffc1b8; border-color: rgba(255,129,116,.28); }
.probe-status[data-status='unknown'],.probe-status[data-status='disabled'] { color: #f2ce8f; border-color: rgba(242,206,143,.22); }
.probe-meta { display: flex; flex-wrap: wrap; gap: 6px 13px; margin: 9px 0; color: #a5b3c8; font-size: 10px; }
.probe-actions .probe-delete { color: #ffc1b8; border-color: rgba(255,129,116,.25); background: rgba(184,77,72,.09); }
.probe-history { display: grid; gap: 5px; margin: 10px 0 0; padding: 9px 0 0; border-top: 1px solid rgba(171,196,232,.1); list-style: none; }
.probe-history li { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 4px 10px; color: #a5b3c8; font-size: 10px; }
.probe-history li[data-status='healthy'] strong { color: #9ce0b7; }
.probe-history li[data-status='unhealthy'] strong { color: #ffc1b8; }
.probe-no-history { color: #9aabc1; }
@media (max-width: 760px) { .probe-fields { grid-template-columns: repeat(2,minmax(0,1fr)); } .probe-fields .probe-target { grid-column: span 2; } }
@media (max-width: 480px) { .probe-heading { align-items: flex-start; flex-direction: column; } .probe-fields { grid-template-columns: minmax(0,1fr); } .probe-fields .probe-target { grid-column: auto; } .probe-card-heading { align-items: flex-start; } .probe-actions button { flex: 1 1 auto; } }
</style>
