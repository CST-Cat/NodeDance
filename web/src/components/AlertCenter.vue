<script setup lang="ts">
import { t, display, locale } from "../i18n"
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { api, operationRequest, type AgentNode, type AlertChannel, type AlertDelivery, type AlertEvent, type AlertItem, type AlertRule, type AlertWindow } from '../api'

type Tab = 'active' | 'history' | 'rules' | 'channels' | 'windows' | 'deliveries'
const tab = ref<Tab>('active')
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')
const nodes = ref<AgentNode[]>([])
const active = ref<AlertItem[]>([])
const history = ref<AlertItem[]>([])
const rules = ref<AlertRule[]>([])
const channels = ref<AlertChannel[]>([])
const windows = ref<AlertWindow[]>([])
const deliveries = ref<AlertDelivery[]>([])
const events = ref<AlertEvent[]>([])
const selectedAlert = ref('')
const editingChannel = ref('')
const ruleForm = reactive({ name: '', kind: 'cpu' as AlertRule['kind'], nodeId: '', subjectId: '', severity: 'warning' as AlertRule['severity'], threshold: 90, durationSeconds: 300, cooldownSeconds: 300, expectedState: 'running', enabled: true, channelIds: [] as string[] })
const channelForm = reactive({ name: '', kind: 'webhook' as AlertChannel['kind'], webhookUrl: '', messageTemplate: '', smtpHost: '', smtpPort: 587, smtpSecurityMode: 'starttls' as NonNullable<AlertChannel['config']['smtpSecurityMode']> | '', smtpFrom: '', smtpTo: '', smtpUsername: '', secret: '', enabled: true, revision: 0 })
const windowForm = reactive({ kind: 'maintenance' as AlertWindow['kind'], scopeType: 'global' as AlertWindow['scopeType'], scopeId: '', startsAt: '', endsAt: '', reason: '' })
const kindOptions = computed<Array<{ value: AlertRule['kind']; label: string }>>(() => [
  { value: 'node_offline', label: t('m.aaf5e5fef170') }, { value: 'cpu', label: t('m.1c82e5547854') }, { value: 'memory', label: t('m.ace212ad8fd3') },
  { value: 'disk', label: t('m.eef857d05e0c') }, { value: 'docker_unavailable', label: t('m.d3e431f9d10b') },
  {value:'metric',label:t('host.metricAlert')},{value:'probe_latency',label:t('host.latencyAlert')},{value:'probe_packet_loss',label:t('host.lossAlert')},{value:'probe_failure',label:t('host.failureAlert')},{value:'asset_expiry',label:t('host.expiryAlert')},{value:'traffic_quota',label:t('host.quotaAlert')},
  { value: 'container_state', label: t('m.15e1d002ad08') }, { value: 'probe_state', label: t('m.c4134bc129e8') },
])
const needsThreshold = computed(() => ['cpu', 'memory', 'disk','metric','probe_latency','probe_failure','probe_packet_loss'].includes(ruleForm.kind))
const needsSubject = computed(() => ['container_state', 'probe_state','metric','probe_latency','probe_failure','probe_packet_loss'].includes(ruleForm.kind))

const needsState = computed(()=>['container_state','probe_state'].includes(ruleForm.kind))
const metricCatalog=ref<Record<string,string>>({})
onMounted(async()=>{try{metricCatalog.value=(await operationRequest<{metrics:Record<string,string>}>('metrics.catalog')).metrics}catch(reason){error.value=message(reason)}})
function message(reason: unknown) { return reason instanceof Error ? reason.message : t('m.a472d51792cc') }
function clearMessages() { error.value = ''; notice.value = '' }
async function refresh() {
  loading.value = true; error.value = ''
  const results = await Promise.allSettled([api.nodes(), api.activeAlerts(), api.alertHistory(), api.alertRules(), api.alertChannels(), api.alertWindows(), api.alertDeliveries(), api.alertEvents()])
  const failure = results.find((item) => item.status === 'rejected')
  if (failure?.status === 'rejected') error.value = message(failure.reason)
  if (results[0].status === 'fulfilled') { nodes.value = results[0].value.nodes; if (!ruleForm.nodeId) ruleForm.nodeId = nodes.value[0]?.nodeId ?? '' }
  if (results[1].status === 'fulfilled') active.value = results[1].value.alerts
  if (results[2].status === 'fulfilled') history.value = results[2].value.alerts
  if (results[3].status === 'fulfilled') rules.value = results[3].value.rules
  if (results[4].status === 'fulfilled') channels.value = results[4].value.channels
  if (results[5].status === 'fulfilled') windows.value = results[5].value.windows
  if (results[6].status === 'fulfilled') deliveries.value = results[6].value.deliveries
  if (results[7].status === 'fulfilled') events.value = results[7].value.events
  loading.value = false
}
async function acknowledge(item: AlertItem) { clearMessages(); try { await api.acknowledgeAlert(item.id); notice.value = t('m.cea97c0a7048'); await refresh() } catch (reason) { error.value = message(reason) } }
async function silence(item: AlertItem) { clearMessages(); try { await api.silenceAlert(item.id, new Date(Date.now() + 60 * 60_000).toISOString(), t('m.8c345ca1ae04')); notice.value = t('m.2abe886a8b9e'); await refresh() } catch (reason) { error.value = message(reason) } }
async function saveRule() {
  clearMessages(); saving.value = true
  try {
    const body = { name: ruleForm.name, kind: ruleForm.kind, nodeId: ruleForm.nodeId, subjectId: needsSubject.value ? ruleForm.subjectId : '', severity: ruleForm.severity,
      ...(needsThreshold.value ? { threshold: Number(ruleForm.threshold) } : {}), durationSeconds: Number(ruleForm.durationSeconds), cooldownSeconds: Number(ruleForm.cooldownSeconds),
      expectedState: needsState.value ? ruleForm.expectedState : '', channelIds: ruleForm.channelIds, enabled: ruleForm.enabled }
    await api.createAlertRule(body); ruleForm.name = ''; ruleForm.subjectId = ''; notice.value = t('m.43fed933b297'); await refresh()
  } catch (reason) { error.value = message(reason) } finally { saving.value = false }
}
function editChannel(channel: AlertChannel) {
  editingChannel.value = channel.id; channelForm.name = channel.name; channelForm.kind = channel.kind; channelForm.enabled = channel.enabled; channelForm.revision = channel.revision; channelForm.secret = ''
  channelForm.webhookUrl = channel.config.webhookUrl ?? ''; channelForm.messageTemplate = channel.config.messageTemplate ?? ''; channelForm.smtpHost = channel.config.smtpHost ?? ''; channelForm.smtpPort = channel.config.smtpPort ?? 587; channelForm.smtpSecurityMode = channel.config.smtpSecurityMode ?? ''; channelForm.smtpFrom = channel.config.smtpFrom ?? ''; channelForm.smtpTo = channel.config.smtpTo ?? ''; channelForm.smtpUsername = channel.config.smtpUsername ?? ''
}
function clearChannelForm() { editingChannel.value = ''; channelForm.name = ''; channelForm.kind = 'webhook'; channelForm.webhookUrl = ''; channelForm.messageTemplate = ''; channelForm.smtpHost = ''; channelForm.smtpPort = 587; channelForm.smtpSecurityMode = 'starttls'; channelForm.smtpFrom = ''; channelForm.smtpTo = ''; channelForm.smtpUsername = ''; channelForm.secret = ''; channelForm.enabled = true; channelForm.revision = 0 }
async function saveChannel() {
  const smtpSecurityMode = channelForm.smtpSecurityMode
  if (channelForm.kind === 'smtp' && smtpSecurityMode !== 'starttls' && smtpSecurityMode !== 'implicit_tls') {
    error.value = t('m.4beb86e21302')
    return
  }
  const smtpConfigMode = smtpSecurityMode === 'starttls' || smtpSecurityMode === 'implicit_tls' ? smtpSecurityMode : undefined
  clearMessages(); saving.value = true
  try {
    const messageTemplate = channelForm.messageTemplate || undefined
    const config = channelForm.kind === 'webhook' ? { webhookUrl: channelForm.webhookUrl, ...(messageTemplate ? { messageTemplate } : {}) } : { smtpHost: channelForm.smtpHost, smtpPort: Number(channelForm.smtpPort), smtpSecurityMode: smtpConfigMode, smtpFrom: channelForm.smtpFrom, smtpTo: channelForm.smtpTo, smtpUsername: channelForm.smtpUsername, ...(messageTemplate ? { messageTemplate } : {}) }
    await api.saveAlertChannel({ name: channelForm.name, kind: channelForm.kind, config, secret: channelForm.secret || undefined, enabled: channelForm.enabled, ...(editingChannel.value ? { revision: channelForm.revision } : {}) }, editingChannel.value || undefined)
    notice.value = t('m.628832ec4bdb'); clearChannelForm(); await refresh()
  } catch (reason) { error.value = message(reason) } finally { saving.value = false }
}
async function deleteChannel(channel: AlertChannel) { clearMessages(); try { await api.deleteAlertChannel(channel.id, channel.revision); notice.value = t('m.ac4a5dec2790'); await refresh() } catch (reason) { error.value = message(reason) } }
async function testChannel(channel: AlertChannel) { clearMessages(); try { await api.testAlertChannel(channel.id); notice.value = t('m.852b75a2577e', {"0": (channel.name)}); tab.value = 'deliveries'; await refresh() } catch (reason) { error.value = message(reason) } }
async function saveWindow() {
  clearMessages(); saving.value = true
  try {
    const startsAt = windowForm.startsAt ? new Date(windowForm.startsAt).toISOString() : new Date().toISOString()
    const endsAt = windowForm.endsAt ? new Date(windowForm.endsAt).toISOString() : new Date(Date.now() + 60 * 60_000).toISOString()
    await api.createAlertWindow({ kind: windowForm.kind, scopeType: windowForm.scopeType, ...(windowForm.scopeType === 'global' ? {} : { scopeId: windowForm.scopeId }), startsAt, endsAt, reason: windowForm.reason })
    notice.value = t('m.1f561856659c'); windowForm.reason = ''; await refresh()
  } catch (reason) { error.value = message(reason) } finally { saving.value = false }
}
async function deleteWindow(window: AlertWindow) { try { await api.deleteAlertWindow(window.id); notice.value = t('m.7e51b232bf64'); await refresh() } catch (reason) { error.value = message(reason) } }
async function deleteRule(rule: AlertRule) { try { await api.deleteAlertRule(rule.id, rule.revision); notice.value = t('m.daa8c0f91450'); await refresh() } catch (reason) { error.value = message(reason) } }
function when(value?: string) { if (!value) return '—'; const date = new Date(value); return Number.isNaN(date.getTime()) ? value : date.toLocaleString(locale.value) }
function deliveryLabel(status: string) {const labels: Record<string, string> = {retry: t('delivery.retry'), suppressed: t('delivery.suppressed'), pending: t('delivery.pending'), sending: t('delivery.sending'), sent: t('delivery.sent'), delivered: t('delivery.delivered'), retrying: t('delivery.retrying'), failed: t('delivery.failed'), queued: t('delivery.queued')}; return labels[status] ?? status}
function ruleLabel(kind: string) { return kindOptions.value.find((item) => item.value === kind)?.label ?? kind }
function activeList() { return tab.value === 'active' ? active.value : history.value }

let refreshTimer: number | undefined
onMounted(() => {
  void refresh()
  refreshTimer = window.setInterval(() => { if (!loading.value) void refresh() }, 30_000)
})
onBeforeUnmount(() => { if (refreshTimer !== undefined) window.clearInterval(refreshTimer) })
</script>

<template>
  <section class="alerts-page">
    <header class="alerts-heading">
      <div><span class="eyebrow">{{ t('label.74b98e3066ff') }}</span><h1>{{ t('m.1b92ed81c005') }}<span>.</span></h1><p>{{ t('m.30dc14a37a07') }}</p></div>
      <button type="button" class="alert-button subtle" :disabled="loading" @click="refresh">{{ loading ? t('m.7f3c8312816f') : t('m.0468ee76ee3d') }}</button>
    </header>
    <div v-if="error" class="alert-message danger" role="alert">{{ display(error) }}</div>
    <div v-if="notice" class="alert-message success" role="status">{{ display(notice) }}</div>
    <nav class="alert-tabs" :aria-label="t('m.6c08cfefefbc')">
      <button v-for="item in ([['active',t('m.ee404f35c3ce')],['history',t('m.7fe7f1f0fb28')],['rules',t('m.7488ac33d3de')],['channels',t('m.b2354cebb5bc')],['windows',t('m.cb20b84d602a')],['deliveries',t('m.f0c5fa02c974')]] as Array<[Tab,string]>)" :key="item[0]" type="button" :aria-current="tab === item[0] ? 'page' : undefined" @click="tab = item[0]">{{ item[1] }}<span v-if="item[0] === 'active' && active.length">{{ active.length }}</span></button>
    </nav>

    <section v-if="tab === 'active' || tab === 'history'" class="alert-card">
      <div class="section-title"><div><h2>{{ tab === 'active' ? t('m.e713ce880f38') : t('m.f49736f12f80') }}</h2><p>{{ tab === 'active' ? t('m.0fa70890a3d2') : t('m.9bb92d183ceb') }}</p></div></div>
      <div v-if="activeList().length" class="alert-list">
        <article v-for="item in activeList()" :key="item.id" class="alert-row" :data-severity="item.severity">
          <div class="severity-mark" aria-hidden="true"></div><div class="alert-content"><div class="alert-title"><strong>{{ item.ruleName }}</strong><span class="pill" :class="item.status">{{ item.status === 'active' ? t('m.3c01089ea0e4') : t('m.3617f737f437') }}</span><span v-if="item.acknowledgedAt" class="pill acknowledged">{{ t('m.d56a511f498f') }}</span><span v-if="item.suppressionReason" class="pill suppressed">{{ item.suppressionReason === 'node_offline' ? t('m.c46a19c8d6ca') : t('m.5329772f4d2b') }}</span></div>
            <p>{{ display(item.message) }}</p><small>{{ item.nodeName }} · {{ item.nodeId }}{{ t('m.a2f8212e41a4') }}{{ when(item.firstSeenAt) }}<template v-if="item.resolvedAt">{{ t('m.daf142e39346') }}{{ when(item.resolvedAt) }}</template></small></div>
          <div v-if="item.status === 'active'" class="row-actions"><button type="button" class="alert-button" :disabled="!!item.acknowledgedAt" @click="acknowledge(item)">{{ t('m.36f33adaf094') }}</button><button type="button" class="alert-button subtle" @click="silence(item)">{{ t('m.d69600baef4b') }}</button></div>
        </article>
      </div><div v-else class="empty-state">{{ tab === 'active' ? t('m.b2f037729dad') : t('m.8eac1ef8ee32') }}</div>
      <div v-if="tab === 'history'" class="event-list"><h3>{{ t('m.512c5669c853') }}</h3><article v-for="event in events" :key="event.id"><span>{{ event.kind }}</span><span>{{ display(event.message) }}</span><time>{{ when(event.occurredAt) }}</time></article><p v-if="!events.length" class="empty-state">{{ t('m.d3ae2b8f4adf') }}</p></div>
    </section>

    <section v-else-if="tab === 'rules'" class="alert-grid">
      <div class="alert-card"><div class="section-title"><div><h2>{{ t('m.52ad1003d627') }}</h2><p>{{ t('m.b609ad7cd505') }}</p></div></div>
        <div class="table-wrap"><table><thead><tr><th>{{ t('m.1123245d1bed') }}</th><th>{{ t('m.2410d860f093') }}</th><th>{{ t('m.16bb1d2b098c') }}</th><th>{{ t('m.1882ae19b6a2') }}</th><th>{{ t('m.6320b4a8722a') }}</th><th></th></tr></thead><tbody><tr v-for="rule in rules" :key="rule.id"><td>{{ rule.name }}</td><td>{{ nodes.find(n => n.nodeId === rule.nodeId)?.displayName ?? rule.nodeId }}</td><td>{{ ruleLabel(rule.kind) }}<template v-if="rule.threshold !== undefined"> &gt; {{ rule.threshold }}{{ ['cpu','memory','disk','probe_failure','probe_packet_loss'].includes(rule.kind) ? '%' : '' }}</template><template v-if="rule.expectedState"> ≠ {{ rule.expectedState }}</template></td><td>{{ rule.durationSeconds }}{{ t('m.9dcdc2b289b9') }}</td><td>{{ rule.enabled ? t('m.dfb802238b38') : t('m.a8c3698b5b8c') }}</td><td><button type="button" class="text-button danger-text" @click="deleteRule(rule)">{{ t('m.2f9daa828907') }}</button></td></tr></tbody></table></div>
        <p v-if="!rules.length" class="empty-state">{{ t('m.915b0451461e') }}</p>
      </div>
      <form class="alert-card alert-form-card" @submit.prevent="saveRule"><div class="section-title"><div><h2>{{ t('m.83009fcfe6c1') }}</h2><p>{{ t('m.b8fa37ab5ac0') }}</p></div></div>
        <label>{{ t('m.18dba51b47c8') }}<input v-model="ruleForm.name" required maxlength="80" :placeholder="t('m.fb8ad27501de')" /></label>
        <label>{{ t('m.2410d860f093') }}<select v-model="ruleForm.nodeId" required><option v-for="node in nodes" :key="node.nodeId" :value="node.nodeId">{{ node.displayName }} · {{ node.status }}</option></select></label>
        <label>{{ t('m.16bb1d2b098c') }}<select v-model="ruleForm.kind"><option v-for="item in kindOptions" :key="item.value" :value="item.value">{{ item.label }}</option></select></label>
        <label v-if="needsThreshold">{{ t('m.26a4ac708409') }}<input v-model.number="ruleForm.threshold" type="number" min="0" :max="['cpu','memory','disk','probe_failure','probe_packet_loss'].includes(ruleForm.kind)?100:undefined" step="0.1" required /></label>
        <label v-if="needsSubject">{{ t('m.ad293c799482') }}<select v-if="ruleForm.kind==='metric'" v-model="ruleForm.subjectId" required><option v-for="(unit,key) in metricCatalog" :key="key" :value="key">{{ key }} · {{ unit }}</option></select><input v-else v-model="ruleForm.subjectId" required :placeholder="t('m.8a90cd8dc652')" /></label>
        <label v-if="needsState">{{ t('m.b3d94c7eff6f') }}<select v-model="ruleForm.expectedState"><option value="running">{{ t('m.1f0eb99b7ed0') }}</option><option value="stopped">{{ t('m.f006455e3baf') }}</option><option value="healthy">{{ t('m.aa18b6dcd893') }}</option><option value="unhealthy">{{ t('m.395a7bf80b24') }}</option></select></label>
        <div class="inline-fields"><label>{{ t('m.f67bd602eaa1') }}<input v-model.number="ruleForm.durationSeconds" type="number" min="0" max="86400" /></label><label>{{ t('m.c90ed7f53ce4') }}<input v-model.number="ruleForm.cooldownSeconds" type="number" min="0" max="86400" /></label></div>
        <label>{{ t('m.3d9d02e83d39') }}<select v-model="ruleForm.severity"><option value="info">{{ t('m.f56c6c82203b') }}</option><option value="warning">{{ t('m.a8b7a4480407') }}</option><option value="critical">{{ t('m.73eb0e14e307') }}</option></select></label>
        <fieldset><legend>{{ t('m.1bcd7265cd39') }}</legend><label v-for="channel in channels" :key="channel.id" class="check-row"><input v-model="ruleForm.channelIds" type="checkbox" :value="channel.id" />{{ channel.name }}</label><span v-if="!channels.length" class="hint">{{ t('m.1c5c0b7d73e4') }}</span></fieldset>
        <label class="check-row"><input v-model="ruleForm.enabled" type="checkbox" />{{ t('m.e45a3f15b735') }}</label><button type="submit" class="alert-button primary" :disabled="saving || !nodes.length">{{ saving ? t('m.ff509c9ba052') : t('m.63faeddb2a46') }}</button>
      </form>
    </section>

    <section v-else-if="tab === 'channels'" class="alert-grid">
      <div class="alert-card"><div class="section-title"><div><h2>{{ t('m.b2354cebb5bc') }}</h2><p>{{ t('m.3cb6afac5598') }}</p></div></div><div class="channel-list"><article v-for="channel in channels" :key="channel.id"><div><strong>{{ channel.name }}</strong><span class="pill">{{ channel.kind === 'webhook' ? 'Webhook' : 'SMTP' }}</span><small>{{ channel.kind === 'webhook' ? channel.config.webhookUrl : `${channel.config.smtpHost}:${channel.config.smtpPort} → ${channel.config.smtpTo}` }}</small></div><div class="row-actions"><button type="button" class="alert-button" @click="testChannel(channel)">{{ t('m.e4263450f9ec') }}</button><button type="button" class="alert-button subtle" @click="editChannel(channel)">{{ t('m.051836569928') }}</button><button type="button" class="text-button danger-text" @click="deleteChannel(channel)">{{ t('m.2f9daa828907') }}</button></div></article><p v-if="!channels.length" class="empty-state">{{ t('m.6b2cde6d4a15') }}</p></div></div>
      <form class="alert-card alert-form-card" @submit.prevent="saveChannel"><div class="section-title"><div><h2>{{ editingChannel ? t('m.9536f6b3a7da') : t('m.edb55e6632ce') }}</h2><p>{{ t('m.7582c5883aa1') }}</p></div></div>
        <label>{{ t('m.d44e9b3d3b31') }}<input v-model="channelForm.name" required maxlength="80" :placeholder="t('m.ab0113166ed7')" /></label><label>{{ t('m.ba40014ff496') }}<select v-model="channelForm.kind"><option value="webhook">Webhook</option><option value="smtp">{{ t('m.06344b933760') }}</option></select></label>
        <template v-if="channelForm.kind === 'webhook'"><label>{{ t('label.84805a7574a8') }}<input v-model="channelForm.webhookUrl" type="url" required placeholder="https://hooks.example/…" /></label><small class="hint">{{ t('m.ce3a64e47c4d') }}</small></template>
        <template v-else><label>{{ t('m.5344a59d11c5') }}<input v-model="channelForm.smtpHost" required placeholder="smtp.example.com" /></label><label>{{ t('m.e71ac32b544b') }}<input v-model.number="channelForm.smtpPort" type="number" min="1" max="65535" required /></label><label>{{ t('m.475ebf9f8ba3') }}<select v-model="channelForm.smtpSecurityMode"><option value="" disabled>{{ t('m.45fa6e264d64') }}</option><option value="starttls">{{ t('m.5b097c6cccbc') }}</option><option value="implicit_tls">{{ t('m.906148fea2fc') }}</option></select></label><small class="hint">{{ t('m.c373a9cc1577') }}</small><label>{{ t('m.bae32961a3b5') }}<input v-model="channelForm.smtpFrom" type="email" required /></label><label>{{ t('m.c237b665a6d8') }}<input v-model="channelForm.smtpTo" type="email" required /></label><label>{{ t('m.e67fea90bde6') }}<input v-model="channelForm.smtpUsername" autocomplete="off" /></label><label>{{ t('m.82170ccaa6b5') }}<input v-model="channelForm.secret" type="password" autocomplete="new-password" :placeholder="editingChannel && channels.find(x => x.id === editingChannel)?.hasSecret ? t('m.0c5bcbe51a9f') : t('m.61a7dfb3a2c4')" /></label></template>
        <label>{{ t('m.fa3b2035a83a') }}<textarea v-model="channelForm.messageTemplate" maxlength="2048" rows="4" spellcheck="false" placeholder="&#123;&#123;severity&#125;&#125; · &#123;&#123;ruleName&#125;&#125;：&#123;&#123;message&#125;&#125;"></textarea></label><small class="hint">{{ t('m.aaedf01e6fae') }}</small>
        <label class="check-row"><input v-model="channelForm.enabled" type="checkbox" />{{ t('m.848250ce08f9') }}</label><div class="row-actions"><button type="submit" class="alert-button primary" :disabled="saving">{{ saving ? t('m.ff509c9ba052') : t('m.5fb0d9558110') }}</button><button v-if="editingChannel" type="button" class="alert-button subtle" @click="clearChannelForm">{{ t('m.4b29ce8e4093') }}</button></div>
      </form>
    </section>

    <section v-else-if="tab === 'windows'" class="alert-grid">
      <div class="alert-card"><div class="section-title"><div><h2>{{ t('m.5b44b4cbe8e7') }}</h2><p>{{ t('m.9ba72a13180d') }}</p></div></div><div class="window-list"><article v-for="window in windows" :key="window.id"><div><strong>{{ window.kind === 'maintenance' ? t('m.1e9fa64d9d1e') : t('m.2edd4433070c') }} · {{ window.scopeType === 'global' ? t('m.63d6b47116de') : window.scopeType === 'node' ? t('m.2410d860f093') : t('m.1123245d1bed') }}</strong><small>{{ when(window.startsAt) }} — {{ when(window.endsAt) }}</small><p>{{ window.reason }}</p></div><button type="button" class="text-button danger-text" @click="deleteWindow(window)">{{ t('m.4e6fd0e28c55') }}</button></article><p v-if="!windows.length" class="empty-state">{{ t('m.20975dbddcfd') }}</p></div></div>
      <form class="alert-card alert-form-card" @submit.prevent="saveWindow"><div class="section-title"><div><h2>{{ t('m.ad7d42133c82') }}</h2><p>{{ t('m.e4926094a3a4') }}</p></div></div><label>{{ t('m.ba40014ff496') }}<select v-model="windowForm.kind"><option value="maintenance">{{ t('m.21138f1b0642') }}</option><option value="silence">{{ t('m.bf436f2fa459') }}</option></select></label><label>{{ t('m.ae72b532f4b8') }}<select v-model="windowForm.scopeType"><option value="global">{{ t('m.63d6b47116de') }}</option><option value="node">{{ t('m.f8c3f7bee9ee') }}</option><option value="rule">{{ t('m.dff9fdf5372b') }}</option></select></label><label v-if="windowForm.scopeType !== 'global'">{{ t('m.67cdf2db35e4') }}<input v-model="windowForm.scopeId" required placeholder="UUID" /></label><div class="inline-fields"><label>{{ t('m.d2bb025a2e51') }}<input v-model="windowForm.startsAt" type="datetime-local" /></label><label>{{ t('m.c7b24e7997e9') }}<input v-model="windowForm.endsAt" type="datetime-local" /></label></div><label>{{ t('m.f029d9cb4a76') }}<input v-model="windowForm.reason" required maxlength="500" :placeholder="t('m.440715526b90')" /></label><button type="submit" class="alert-button primary" :disabled="saving">{{ t('m.2cb77284fedd') }}</button></form>
    </section>

    <section v-else class="alert-card"><div class="section-title"><div><h2>{{ t('m.0d09685cc503') }}</h2><p>{{ t('m.da3e796d7d49') }}</p></div></div><div class="table-wrap"><table><thead><tr><th>{{ t('m.c35ffb529d5d') }}</th><th>{{ t('m.0d12cbd6562b') }}</th><th>{{ t('m.6320b4a8722a') }}</th><th>{{ t('m.58a823852f8a') }}</th><th>HTTP</th><th>{{ t('m.8b6ff498515b') }}</th><th>{{ t('m.0bc1fb72ae1b') }}</th></tr></thead><tbody><tr v-for="delivery in deliveries" :key="delivery.id"><td>{{ delivery.channelName }}</td><td>{{ delivery.testSend ? t('m.6aa8f49cc992') : delivery.kind.toUpperCase() }}</td><td><span class="pill" :class="delivery.status">{{ deliveryLabel(delivery.status) }}</span></td><td>{{ delivery.attempts }} / {{ delivery.maxAttempts }}</td><td>{{ delivery.httpStatus ?? '—' }}</td><td>{{ when(delivery.deliveredAt ?? delivery.createdAt) }}</td><td>{{ display(delivery.lastError || '—') }}</td></tr></tbody></table></div><p v-if="!deliveries.length" class="empty-state">{{ t('m.7bf308b00b96') }}</p></section>
  </section>
</template>

<style scoped>
.alerts-page{width:min(1320px,calc(100% - 48px));margin:34px auto 72px;color:var(--text)}.alerts-heading{display:flex;align-items:flex-end;justify-content:space-between;gap:20px;margin-bottom:24px}.alerts-heading h1{margin:8px 0 5px;font-size:clamp(30px,4vw,43px);letter-spacing:-.04em}.alerts-heading h1 span{color:var(--accent)}.alerts-heading p,.section-title p{margin:0;color:var(--muted)}.eyebrow{font-size: 12px;letter-spacing:.16em;color:var(--muted)}.alert-tabs{display:flex;gap:7px;overflow:auto;padding:5px 0 13px;margin-bottom:8px;border-bottom:1px solid var(--line)}.alert-tabs button{white-space:nowrap;border:0;border-radius:var(--radius-control);padding:10px 13px;background:transparent;color:var(--muted);font:inherit;cursor:pointer}.alert-tabs button[aria-current=page]{background:var(--accent-soft);color:var(--accent)}.alert-tabs button span{margin-left:8px;padding:1px 7px;border-radius:99px;background:var(--danger);color:white;font-size: 12px}.alert-card{border:1px solid var(--line);border-radius:var(--radius-card);background:var(--surface-soft);padding:22px;min-width:0;box-shadow:none}.section-title{display:flex;justify-content:space-between;align-items:flex-start;gap:12px;margin-bottom:17px}.section-title h2{margin:0 0 6px;font-size: 19px}.alert-grid{display:grid;grid-template-columns:minmax(0,1.25fr) minmax(300px,.75fr);gap:16px}.alert-list{display:grid;gap:10px}.alert-row{display:flex;gap:13px;align-items:flex-start;border:1px solid var(--line);border-radius:var(--radius-card);padding:15px;background:var(--surface)}.severity-mark{width:9px;height:9px;margin-top:6px;border-radius:50%;background:var(--warning);box-shadow:0 0 0 4px var(--danger-soft);flex:0 0 auto}.alert-row[data-severity=critical] .severity-mark{background:var(--danger);box-shadow:0 0 0 4px var(--danger-soft)}.alert-row[data-severity=info] .severity-mark{background:var(--accent);box-shadow:0 0 0 4px var(--line)}.alert-content{min-width:0;flex:1}.alert-title{display:flex;flex-wrap:wrap;align-items:center;gap:7px}.alert-content p{margin:6px 0;color:var(--text);overflow-wrap:anywhere}.alert-content small,.channel-list small,.window-list small{display:block;color:var(--muted);font-size: 12px;line-height:1.6;overflow-wrap:anywhere}.pill{display:inline-flex;padding:3px 8px;border-radius:9999px;background:var(--line);font-size: 12px;color:var(--muted-strong)}.pill.active{background:var(--danger-soft);color:var(--warning)}.pill.resolved,.pill.sent{background:var(--success-soft);color:var(--success)}.pill.acknowledged{background:var(--line);color:var(--accent)}.pill.suppressed,.pill.failed{background:var(--danger-soft);color:var(--danger)}.row-actions{display:flex;flex-wrap:wrap;gap:7px;align-items:center;justify-content:flex-end}.alert-button{min-height:40px;border:1px solid var(--line);background:var(--line);border-radius:var(--radius-control);padding:8px 11px;color:var(--text);font:inherit;font-size: 13px;cursor:pointer}.alert-button:hover:not(:disabled){border-color:var(--accent);background:var(--surface-soft)}.alert-button:disabled{opacity:.5;cursor:default}.alert-button.primary{border-color:transparent;background:var(--accent-fill);color:#fff;font-weight:500}.alert-button.primary:hover:not(:disabled){background:var(--accent-strong);color:#fff}.alert-button.subtle{background:transparent}.alert-message{margin-bottom:14px;padding:11px 14px;border-radius:var(--radius-card)}.alert-message.danger{color:var(--danger);background:var(--danger-soft)}.alert-message.success{color:var(--success);background:var(--success-soft)}.empty-state{padding:28px 12px;text-align:center;color:var(--muted)}.table-wrap{overflow:auto}table{width:100%;border-collapse:collapse;min-width:650px}th,td{text-align:left;padding:11px 9px;border-bottom:1px solid var(--line);font-size: 13px}th{color:var(--muted);font-weight:500}td{color:var(--text)}.danger-text{color:var(--danger)}.text-button{border:0;background:transparent;font:inherit;cursor:pointer;padding:5px}.alert-form-card{display:grid;gap:13px}.alert-form-card label{display:grid;gap:6px;color:var(--muted-strong);font-size: 13px}.alert-form-card input:not([type=checkbox]),.alert-form-card select{width:100%;box-sizing:border-box;border:1px solid var(--line);border-radius:var(--radius-control);background:var(--surface-soft);color:var(--text);padding:10px 11px;font:inherit;min-height:40px}.inline-fields{display:grid;grid-template-columns:1fr 1fr;gap:10px}.alert-form-card fieldset{border:1px solid var(--line);border-radius:var(--radius-card);padding:10px 12px}.alert-form-card legend{padding:0 5px;color:var(--muted-strong);font-size: 12px}.alert-form-card .check-row{display:flex;align-items:center;gap:8px}.alert-form-card input[type=checkbox]{accent-color:var(--accent-fill)}.hint{color:var(--muted);font-size: 12px;line-height:1.5}.channel-list,.window-list{display:grid;gap:9px}.channel-list article,.window-list article{display:flex;justify-content:space-between;align-items:center;gap:12px;padding:13px;border:1px solid var(--line);border-radius:var(--radius-card)}.channel-list article strong,.window-list article strong{margin-right:8px}.window-list article p{margin:4px 0 0;color:var(--text)}.event-list{margin-top:24px}.event-list h3{font-size: 15px}.event-list article{display:grid;grid-template-columns:110px 1fr auto;gap:10px;padding:9px 0;border-bottom:1px solid var(--line);font-size: 13px;color:var(--text)}.event-list time{color:var(--muted);white-space:nowrap}
.alert-form-card textarea{width:100%;box-sizing:border-box;border:1px solid var(--line);border-radius:var(--radius-control);background:var(--surface-soft);color:var(--text);padding:10px 11px;font:inherit;min-height:104px;resize:vertical;overflow-wrap:anywhere}
@media(max-width:920px){.alert-grid{grid-template-columns:1fr}.alerts-page{width:min(100% - 28px,760px);margin-top:20px}.alert-card{padding:16px}}
@media(max-width:620px){.alerts-heading{align-items:flex-start;flex-direction:column}.alert-row{flex-wrap:wrap}.row-actions{justify-content:flex-start;width:100%;padding-left:22px}.inline-fields{grid-template-columns:1fr}.channel-list article,.window-list article{align-items:flex-start;flex-direction:column}.event-list article{grid-template-columns:1fr;gap:3px}.event-list time{white-space:normal}}
</style>
