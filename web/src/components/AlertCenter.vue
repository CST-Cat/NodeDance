<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { api, type AgentNode, type AlertChannel, type AlertDelivery, type AlertEvent, type AlertItem, type AlertRule, type AlertWindow } from '../api'

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
const channelForm = reactive({ name: '', kind: 'webhook' as AlertChannel['kind'], webhookUrl: '', messageTemplate: '', smtpHost: '', smtpPort: 587, smtpFrom: '', smtpTo: '', smtpUsername: '', secret: '', enabled: true, revision: 0 })
const windowForm = reactive({ kind: 'maintenance' as AlertWindow['kind'], scopeType: 'global' as AlertWindow['scopeType'], scopeId: '', startsAt: '', endsAt: '', reason: '' })
const kindOptions: Array<{ value: AlertRule['kind']; label: string }> = [
  { value: 'node_offline', label: '节点失联' }, { value: 'cpu', label: 'CPU 使用率' }, { value: 'memory', label: '内存使用率' },
  { value: 'disk', label: '磁盘使用率' }, { value: 'docker_unavailable', label: 'Docker 不可用' },
  { value: 'container_state', label: '容器状态' }, { value: 'probe_state', label: '服务探测状态' },
]
const needsThreshold = computed(() => ['cpu', 'memory', 'disk'].includes(ruleForm.kind))
const needsSubject = computed(() => ['container_state', 'probe_state'].includes(ruleForm.kind))

function message(reason: unknown) { return reason instanceof Error ? reason.message : '请求失败，请重试。' }
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
async function acknowledge(item: AlertItem) { clearMessages(); try { await api.acknowledgeAlert(item.id); notice.value = '告警已确认。'; await refresh() } catch (reason) { error.value = message(reason) } }
async function silence(item: AlertItem) { clearMessages(); try { await api.silenceAlert(item.id, new Date(Date.now() + 60 * 60_000).toISOString(), '管理员静默 1 小时'); notice.value = '告警已静默 1 小时。'; await refresh() } catch (reason) { error.value = message(reason) } }
async function saveRule() {
  clearMessages(); saving.value = true
  try {
    const body = { name: ruleForm.name, kind: ruleForm.kind, nodeId: ruleForm.nodeId, subjectId: needsSubject.value ? ruleForm.subjectId : '', severity: ruleForm.severity,
      ...(needsThreshold.value ? { threshold: Number(ruleForm.threshold) } : {}), durationSeconds: Number(ruleForm.durationSeconds), cooldownSeconds: Number(ruleForm.cooldownSeconds),
      expectedState: needsSubject.value ? ruleForm.expectedState : '', channelIds: ruleForm.channelIds, enabled: ruleForm.enabled }
    await api.createAlertRule(body); ruleForm.name = ''; ruleForm.subjectId = ''; notice.value = '告警规则已保存。'; await refresh()
  } catch (reason) { error.value = message(reason) } finally { saving.value = false }
}
function editChannel(channel: AlertChannel) {
  editingChannel.value = channel.id; channelForm.name = channel.name; channelForm.kind = channel.kind; channelForm.enabled = channel.enabled; channelForm.revision = channel.revision; channelForm.secret = ''
  channelForm.webhookUrl = channel.config.webhookUrl ?? ''; channelForm.messageTemplate = channel.config.messageTemplate ?? ''; channelForm.smtpHost = channel.config.smtpHost ?? ''; channelForm.smtpPort = channel.config.smtpPort ?? 587; channelForm.smtpFrom = channel.config.smtpFrom ?? ''; channelForm.smtpTo = channel.config.smtpTo ?? ''; channelForm.smtpUsername = channel.config.smtpUsername ?? ''
}
function clearChannelForm() { editingChannel.value = ''; channelForm.name = ''; channelForm.kind = 'webhook'; channelForm.webhookUrl = ''; channelForm.messageTemplate = ''; channelForm.smtpHost = ''; channelForm.smtpPort = 587; channelForm.smtpFrom = ''; channelForm.smtpTo = ''; channelForm.smtpUsername = ''; channelForm.secret = ''; channelForm.enabled = true; channelForm.revision = 0 }
async function saveChannel() {
  clearMessages(); saving.value = true
  try {
    const messageTemplate = channelForm.messageTemplate || undefined
    const config = channelForm.kind === 'webhook' ? { webhookUrl: channelForm.webhookUrl, ...(messageTemplate ? { messageTemplate } : {}) } : { smtpHost: channelForm.smtpHost, smtpPort: Number(channelForm.smtpPort), smtpFrom: channelForm.smtpFrom, smtpTo: channelForm.smtpTo, smtpUsername: channelForm.smtpUsername, ...(messageTemplate ? { messageTemplate } : {}) }
    await api.saveAlertChannel({ name: channelForm.name, kind: channelForm.kind, config, secret: channelForm.secret || undefined, enabled: channelForm.enabled, ...(editingChannel.value ? { revision: channelForm.revision } : {}) }, editingChannel.value || undefined)
    notice.value = '通知渠道已保存。'; clearChannelForm(); await refresh()
  } catch (reason) { error.value = message(reason) } finally { saving.value = false }
}
async function deleteChannel(channel: AlertChannel) { clearMessages(); try { await api.deleteAlertChannel(channel.id, channel.revision); notice.value = '通知渠道已删除。'; await refresh() } catch (reason) { error.value = message(reason) } }
async function testChannel(channel: AlertChannel) { clearMessages(); try { await api.testAlertChannel(channel.id); notice.value = `已将 ${channel.name} 的测试消息加入投递队列。`; tab.value = 'deliveries'; await refresh() } catch (reason) { error.value = message(reason) } }
async function saveWindow() {
  clearMessages(); saving.value = true
  try {
    const startsAt = windowForm.startsAt ? new Date(windowForm.startsAt).toISOString() : new Date().toISOString()
    const endsAt = windowForm.endsAt ? new Date(windowForm.endsAt).toISOString() : new Date(Date.now() + 60 * 60_000).toISOString()
    await api.createAlertWindow({ kind: windowForm.kind, scopeType: windowForm.scopeType, ...(windowForm.scopeType === 'global' ? {} : { scopeId: windowForm.scopeId }), startsAt, endsAt, reason: windowForm.reason })
    notice.value = '静默或维护窗口已创建。'; windowForm.reason = ''; await refresh()
  } catch (reason) { error.value = message(reason) } finally { saving.value = false }
}
async function deleteWindow(window: AlertWindow) { try { await api.deleteAlertWindow(window.id); notice.value = '窗口已停用。'; await refresh() } catch (reason) { error.value = message(reason) } }
async function deleteRule(rule: AlertRule) { try { await api.deleteAlertRule(rule.id, rule.revision); notice.value = '规则已删除。'; await refresh() } catch (reason) { error.value = message(reason) } }
function when(value?: string) { if (!value) return '—'; const date = new Date(value); return Number.isNaN(date.getTime()) ? value : date.toLocaleString() }
function ruleLabel(kind: string) { return kindOptions.find((item) => item.value === kind)?.label ?? kind }
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
      <div><span class="eyebrow">OPERATIONS / NOTIFICATIONS</span><h1>告警中心<span>.</span></h1><p>管理触发条件、通知渠道与恢复记录。</p></div>
      <button type="button" class="alert-button subtle" :disabled="loading" @click="refresh">{{ loading ? '刷新中…' : '刷新数据' }}</button>
    </header>
    <div v-if="error" class="alert-message danger" role="alert">{{ error }}</div>
    <div v-if="notice" class="alert-message success" role="status">{{ notice }}</div>
    <nav class="alert-tabs" aria-label="告警页面">
      <button v-for="item in ([['active','活动告警'],['history','历史与事件'],['rules','告警规则'],['channels','通知渠道'],['windows','静默与维护'],['deliveries','投递记录']] as Array<[Tab,string]>)" :key="item[0]" type="button" :aria-current="tab === item[0] ? 'page' : undefined" @click="tab = item[0]">{{ item[1] }}<span v-if="item[0] === 'active' && active.length">{{ active.length }}</span></button>
    </nav>

    <section v-if="tab === 'active' || tab === 'history'" class="alert-card">
      <div class="section-title"><div><h2>{{ tab === 'active' ? '当前告警' : '已恢复告警' }}</h2><p>{{ tab === 'active' ? '失联节点会抑制其下级重复通知。' : '查看告警生命周期和确认记录。' }}</p></div></div>
      <div v-if="activeList().length" class="alert-list">
        <article v-for="item in activeList()" :key="item.id" class="alert-row" :data-severity="item.severity">
          <div class="severity-mark" aria-hidden="true"></div><div class="alert-content"><div class="alert-title"><strong>{{ item.ruleName }}</strong><span class="pill" :class="item.status">{{ item.status === 'active' ? '活动中' : '已恢复' }}</span><span v-if="item.acknowledgedAt" class="pill acknowledged">已确认</span><span v-if="item.suppressionReason" class="pill suppressed">{{ item.suppressionReason === 'node_offline' ? '节点离线抑制' : '通知已抑制' }}</span></div>
            <p>{{ item.message }}</p><small>{{ item.nodeName }} · {{ item.nodeId }} · 首次 {{ when(item.firstSeenAt) }}<template v-if="item.resolvedAt"> · 恢复 {{ when(item.resolvedAt) }}</template></small></div>
          <div v-if="item.status === 'active'" class="row-actions"><button type="button" class="alert-button" :disabled="!!item.acknowledgedAt" @click="acknowledge(item)">确认</button><button type="button" class="alert-button subtle" @click="silence(item)">静默 1 小时</button></div>
        </article>
      </div><div v-else class="empty-state">{{ tab === 'active' ? '当前没有活动告警。' : '还没有已恢复的告警。' }}</div>
      <div v-if="tab === 'history'" class="event-list"><h3>最近事件</h3><article v-for="event in events" :key="event.id"><span>{{ event.kind }}</span><span>{{ event.message }}</span><time>{{ when(event.occurredAt) }}</time></article><p v-if="!events.length" class="empty-state">暂无告警事件。</p></div>
    </section>

    <section v-else-if="tab === 'rules'" class="alert-grid">
      <div class="alert-card"><div class="section-title"><div><h2>现有规则</h2><p>节点首次进入管理后自动建立 CPU、内存、磁盘 90% / 5 分钟默认规则。</p></div></div>
        <div class="table-wrap"><table><thead><tr><th>规则</th><th>节点</th><th>条件</th><th>持续</th><th>状态</th><th></th></tr></thead><tbody><tr v-for="rule in rules" :key="rule.id"><td>{{ rule.name }}</td><td>{{ nodes.find(n => n.nodeId === rule.nodeId)?.displayName ?? rule.nodeId }}</td><td>{{ ruleLabel(rule.kind) }}<template v-if="rule.threshold !== undefined"> &gt; {{ rule.threshold }}%</template><template v-if="rule.expectedState"> ≠ {{ rule.expectedState }}</template></td><td>{{ rule.durationSeconds }} 秒</td><td>{{ rule.enabled ? '已启用' : '已停用' }}</td><td><button type="button" class="text-button danger-text" @click="deleteRule(rule)">删除</button></td></tr></tbody></table></div>
        <p v-if="!rules.length" class="empty-state">暂无规则。</p>
      </div>
      <form class="alert-card form-card" @submit.prevent="saveRule"><div class="section-title"><div><h2>新增规则</h2><p>每条规则只绑定一个节点和可选通知渠道。</p></div></div>
        <label>规则名称<input v-model="ruleForm.name" required maxlength="80" placeholder="例如：生产 Web 服务异常" /></label>
        <label>节点<select v-model="ruleForm.nodeId" required><option v-for="node in nodes" :key="node.nodeId" :value="node.nodeId">{{ node.displayName }} · {{ node.status }}</option></select></label>
        <label>条件<select v-model="ruleForm.kind"><option v-for="item in kindOptions" :key="item.value" :value="item.value">{{ item.label }}</option></select></label>
        <label v-if="needsThreshold">阈值 (%)<input v-model.number="ruleForm.threshold" type="number" min="0" max="100" step="0.1" required /></label>
        <label v-if="needsSubject">容器或探测 ID<input v-model="ruleForm.subjectId" required placeholder="UUID 或容器 ID" /></label>
        <label v-if="needsSubject">期望状态<select v-model="ruleForm.expectedState"><option value="running">运行中</option><option value="stopped">已停止</option><option value="healthy">健康</option><option value="unhealthy">不健康</option></select></label>
        <div class="inline-fields"><label>持续秒数<input v-model.number="ruleForm.durationSeconds" type="number" min="0" max="86400" /></label><label>冷却秒数<input v-model.number="ruleForm.cooldownSeconds" type="number" min="0" max="86400" /></label></div>
        <label>级别<select v-model="ruleForm.severity"><option value="info">提示</option><option value="warning">警告</option><option value="critical">严重</option></select></label>
        <fieldset><legend>通知渠道（留空时使用全部启用渠道）</legend><label v-for="channel in channels" :key="channel.id" class="check-row"><input v-model="ruleForm.channelIds" type="checkbox" :value="channel.id" />{{ channel.name }}</label><span v-if="!channels.length" class="hint">尚未配置通知渠道。</span></fieldset>
        <label class="check-row"><input v-model="ruleForm.enabled" type="checkbox" />立即启用</label><button type="submit" class="alert-button primary" :disabled="saving || !nodes.length">{{ saving ? '保存中…' : '创建规则' }}</button>
      </form>
    </section>

    <section v-else-if="tab === 'channels'" class="alert-grid">
      <div class="alert-card"><div class="section-title"><div><h2>通知渠道</h2><p>凭据只写入加密存储，页面不会回显密钥。</p></div></div><div class="channel-list"><article v-for="channel in channels" :key="channel.id"><div><strong>{{ channel.name }}</strong><span class="pill">{{ channel.kind === 'webhook' ? 'Webhook' : 'SMTP' }}</span><small>{{ channel.kind === 'webhook' ? channel.config.webhookUrl : `${channel.config.smtpHost}:${channel.config.smtpPort} → ${channel.config.smtpTo}` }}</small></div><div class="row-actions"><button type="button" class="alert-button" @click="testChannel(channel)">测试发送</button><button type="button" class="alert-button subtle" @click="editChannel(channel)">编辑</button><button type="button" class="text-button danger-text" @click="deleteChannel(channel)">删除</button></div></article><p v-if="!channels.length" class="empty-state">尚未配置渠道。</p></div></div>
      <form class="alert-card form-card" @submit.prevent="saveChannel"><div class="section-title"><div><h2>{{ editingChannel ? '编辑通知渠道' : '添加通知渠道' }}</h2><p>Webhook 支持受控的 HTTP 接收器；SMTP 建议启用 STARTTLS。</p></div></div>
        <label>名称<input v-model="channelForm.name" required maxlength="80" placeholder="运维通知" /></label><label>类型<select v-model="channelForm.kind"><option value="webhook">Webhook</option><option value="smtp">SMTP 邮件</option></select></label>
        <template v-if="channelForm.kind === 'webhook'"><label>Webhook URL<input v-model="channelForm.webhookUrl" type="url" required placeholder="https://hooks.example/…" /></label><small class="hint">仅管理员指定的 URL 会被请求，重定向不会自动跟随。</small></template>
        <template v-else><label>SMTP 主机<input v-model="channelForm.smtpHost" required placeholder="smtp.example.com" /></label><label>端口<input v-model.number="channelForm.smtpPort" type="number" min="1" max="65535" required /></label><label>发件人<input v-model="channelForm.smtpFrom" type="email" required /></label><label>收件人<input v-model="channelForm.smtpTo" type="email" required /></label><label>用户名（可选）<input v-model="channelForm.smtpUsername" autocomplete="off" /></label><label>密码 / App Password<input v-model="channelForm.secret" type="password" autocomplete="new-password" :placeholder="editingChannel && channels.find(x => x.id === editingChannel)?.hasSecret ? '留空以保留已有密码' : '留空表示无需认证'" /></label></template>
        <label>通知消息模板（可选）<textarea v-model="channelForm.messageTemplate" maxlength="2048" rows="4" spellcheck="false" placeholder="&#123;&#123;severity&#125;&#125; · &#123;&#123;ruleName&#125;&#125;：&#123;&#123;message&#125;&#125;"></textarea></label><small class="hint">留空使用默认消息。可用字段：&#123;&#123;event&#125;&#125;、&#123;&#123;alertId&#125;&#125;、&#123;&#123;ruleName&#125;&#125;、&#123;&#123;nodeName&#125;&#125;、&#123;&#123;nodeId&#125;&#125;、&#123;&#123;subjectId&#125;&#125;、&#123;&#123;severity&#125;&#125;、&#123;&#123;message&#125;&#125;、&#123;&#123;occurredAt&#125;&#125;。只支持字段替换，最多 2048 字节。</small>
        <label class="check-row"><input v-model="channelForm.enabled" type="checkbox" />启用渠道</label><div class="row-actions"><button type="submit" class="alert-button primary" :disabled="saving">{{ saving ? '保存中…' : '保存渠道' }}</button><button v-if="editingChannel" type="button" class="alert-button subtle" @click="clearChannelForm">取消编辑</button></div>
      </form>
    </section>

    <section v-else-if="tab === 'windows'" class="alert-grid">
      <div class="alert-card"><div class="section-title"><div><h2>已安排窗口</h2><p>静默与维护窗口会保留告警状态，只抑制外部通知。</p></div></div><div class="window-list"><article v-for="window in windows" :key="window.id"><div><strong>{{ window.kind === 'maintenance' ? '维护窗口' : '静默窗口' }} · {{ window.scopeType === 'global' ? '全局' : window.scopeType === 'node' ? '节点' : '规则' }}</strong><small>{{ when(window.startsAt) }} — {{ when(window.endsAt) }}</small><p>{{ window.reason }}</p></div><button type="button" class="text-button danger-text" @click="deleteWindow(window)">停用</button></article><p v-if="!windows.length" class="empty-state">没有计划中的窗口。</p></div></div>
      <form class="alert-card form-card" @submit.prevent="saveWindow"><div class="section-title"><div><h2>安排窗口</h2><p>时间按本地填写，提交时转换为 UTC。</p></div></div><label>类型<select v-model="windowForm.kind"><option value="maintenance">维护</option><option value="silence">静默</option></select></label><label>范围<select v-model="windowForm.scopeType"><option value="global">全局</option><option value="node">单节点</option><option value="rule">单规则</option></select></label><label v-if="windowForm.scopeType !== 'global'">范围 ID<input v-model="windowForm.scopeId" required placeholder="UUID" /></label><div class="inline-fields"><label>开始<input v-model="windowForm.startsAt" type="datetime-local" /></label><label>结束<input v-model="windowForm.endsAt" type="datetime-local" /></label></div><label>原因<input v-model="windowForm.reason" required maxlength="500" placeholder="计划维护" /></label><button type="submit" class="alert-button primary" :disabled="saving">创建窗口</button></form>
    </section>

    <section v-else class="alert-card"><div class="section-title"><div><h2>通知投递历史</h2><p>失败投递会进行最多三次有界尝试；重启后队列状态可恢复。</p></div></div><div class="table-wrap"><table><thead><tr><th>渠道</th><th>类别</th><th>状态</th><th>尝试</th><th>HTTP</th><th>时间</th><th>错误</th></tr></thead><tbody><tr v-for="delivery in deliveries" :key="delivery.id"><td>{{ delivery.channelName }}</td><td>{{ delivery.testSend ? '测试' : delivery.kind.toUpperCase() }}</td><td><span class="pill" :class="delivery.status">{{ delivery.status }}</span></td><td>{{ delivery.attempts }} / {{ delivery.maxAttempts }}</td><td>{{ delivery.httpStatus ?? '—' }}</td><td>{{ when(delivery.deliveredAt ?? delivery.createdAt) }}</td><td>{{ delivery.lastError || '—' }}</td></tr></tbody></table></div><p v-if="!deliveries.length" class="empty-state">暂无投递记录。</p></section>
  </section>
</template>

<style scoped>
.alerts-page{width:min(1320px,calc(100% - 48px));margin:34px auto 72px;color:var(--text-main,#e9edf5)}.alerts-heading{display:flex;align-items:flex-end;justify-content:space-between;gap:20px;margin-bottom:24px}.alerts-heading h1{margin:8px 0 5px;font-size:clamp(30px,4vw,43px);letter-spacing:-.04em}.alerts-heading h1 span{color:#66d2b2}.alerts-heading p,.section-title p{margin:0;color:var(--text-muted,#98a4b7)}.eyebrow{font-size:11px;letter-spacing:.16em;color:#7d8ba1}.alert-tabs{display:flex;gap:7px;overflow:auto;padding:5px 0 13px;margin-bottom:8px;border-bottom:1px solid rgba(150,165,190,.18)}.alert-tabs button{white-space:nowrap;border:0;border-radius:10px;padding:10px 13px;background:transparent;color:var(--text-muted,#98a4b7);font:inherit;cursor:pointer}.alert-tabs button[aria-current=page]{background:rgba(103,210,178,.13);color:#82e4c6}.alert-tabs button span{margin-left:8px;padding:1px 7px;border-radius:99px;background:#b94748;color:white;font-size:11px}.alert-card{border:1px solid rgba(150,165,190,.17);border-radius:18px;background:rgba(15,24,38,.77);padding:22px;min-width:0;box-shadow:0 18px 48px rgba(0,0,0,.13)}.section-title{display:flex;justify-content:space-between;align-items:flex-start;gap:12px;margin-bottom:17px}.section-title h2{margin:0 0 6px;font-size:19px}.alert-grid{display:grid;grid-template-columns:minmax(0,1.25fr) minmax(300px,.75fr);gap:16px}.alert-list{display:grid;gap:10px}.alert-row{display:flex;gap:13px;align-items:flex-start;border:1px solid rgba(150,165,190,.14);border-radius:14px;padding:15px;background:rgba(255,255,255,.018)}.severity-mark{width:9px;height:9px;margin-top:6px;border-radius:50%;background:#e5a755;box-shadow:0 0 0 4px rgba(229,167,85,.12);flex:0 0 auto}.alert-row[data-severity=critical] .severity-mark{background:#ff7777;box-shadow:0 0 0 4px rgba(255,119,119,.12)}.alert-row[data-severity=info] .severity-mark{background:#78a7ff;box-shadow:0 0 0 4px rgba(120,167,255,.12)}.alert-content{min-width:0;flex:1}.alert-title{display:flex;flex-wrap:wrap;align-items:center;gap:7px}.alert-content p{margin:6px 0;color:#d9e1ed;overflow-wrap:anywhere}.alert-content small,.channel-list small,.window-list small{display:block;color:#8e9cb0;font-size:12px;line-height:1.6;overflow-wrap:anywhere}.pill{display:inline-flex;padding:3px 8px;border-radius:20px;background:rgba(130,150,180,.14);font-size:11px;color:#c4cfdd}.pill.active{background:rgba(229,167,85,.15);color:#f1bf75}.pill.resolved,.pill.sent{background:rgba(90,196,151,.14);color:#8ce1bd}.pill.acknowledged{background:rgba(110,143,210,.18);color:#acc5ff}.pill.suppressed,.pill.failed{background:rgba(212,105,105,.15);color:#f59a9a}.row-actions{display:flex;flex-wrap:wrap;gap:7px;align-items:center;justify-content:flex-end}.alert-button{border:1px solid rgba(147,164,190,.25);background:rgba(147,164,190,.1);border-radius:9px;padding:8px 11px;color:#dbe5f2;font:inherit;font-size:13px;cursor:pointer}.alert-button:hover{border-color:#70d3b2}.alert-button:disabled{opacity:.5;cursor:default}.alert-button.primary{border-color:transparent;background:linear-gradient(120deg,#44b994,#3781a0);color:#fff;font-weight:650}.alert-button.subtle{background:transparent}.alert-message{margin-bottom:14px;padding:11px 14px;border-radius:10px}.alert-message.danger{color:#ffb2b2;background:rgba(190,60,65,.17)}.alert-message.success{color:#9fe5c6;background:rgba(47,154,117,.16)}.empty-state{padding:28px 12px;text-align:center;color:#8f9db1}.table-wrap{overflow:auto}table{width:100%;border-collapse:collapse;min-width:650px}th,td{text-align:left;padding:11px 9px;border-bottom:1px solid rgba(150,165,190,.12);font-size:13px}th{color:#94a3b8;font-weight:550}td{color:#dce3ee}.danger-text{color:#f29a9a}.text-button{border:0;background:transparent;font:inherit;cursor:pointer;padding:5px}.form-card{display:grid;gap:13px}.form-card label{display:grid;gap:6px;color:#b7c3d3;font-size:13px}.form-card input:not([type=checkbox]),.form-card select{width:100%;box-sizing:border-box;border:1px solid rgba(150,165,190,.23);border-radius:9px;background:#101a2a;color:#e7edf6;padding:10px 11px;font:inherit;min-height:40px}.inline-fields{display:grid;grid-template-columns:1fr 1fr;gap:10px}.form-card fieldset{border:1px solid rgba(150,165,190,.19);border-radius:10px;padding:10px 12px}.form-card legend{padding:0 5px;color:#aebace;font-size:12px}.form-card .check-row{display:flex;align-items:center;gap:8px}.form-card input[type=checkbox]{accent-color:#5bc9a5}.hint{color:#8d9cb1;font-size:12px;line-height:1.5}.channel-list,.window-list{display:grid;gap:9px}.channel-list article,.window-list article{display:flex;justify-content:space-between;align-items:center;gap:12px;padding:13px;border:1px solid rgba(150,165,190,.14);border-radius:12px}.channel-list article strong,.window-list article strong{margin-right:8px}.window-list article p{margin:4px 0 0;color:#c5cfde}.event-list{margin-top:24px}.event-list h3{font-size:15px}.event-list article{display:grid;grid-template-columns:110px 1fr auto;gap:10px;padding:9px 0;border-bottom:1px solid rgba(150,165,190,.11);font-size:13px;color:#cbd5e2}.event-list time{color:#8d9cb1;white-space:nowrap}
.form-card textarea{width:100%;box-sizing:border-box;border:1px solid rgba(150,165,190,.23);border-radius:9px;background:#101a2a;color:#e7edf6;padding:10px 11px;font:inherit;min-height:104px;resize:vertical;overflow-wrap:anywhere}
@media(max-width:920px){.alert-grid{grid-template-columns:1fr}.alerts-page{width:min(100% - 28px,760px);margin-top:20px}.alert-card{padding:16px}}
@media(max-width:620px){.alerts-heading{align-items:flex-start;flex-direction:column}.alert-row{flex-wrap:wrap}.row-actions{justify-content:flex-start;width:100%;padding-left:22px}.inline-fields{grid-template-columns:1fr}.channel-list article,.window-list article{align-items:flex-start;flex-direction:column}.event-list article{grid-template-columns:1fr;gap:3px}.event-list time{white-space:normal}}
</style>
