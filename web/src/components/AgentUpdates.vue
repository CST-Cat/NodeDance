<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { api, type AgentUpdateOverview, type AgentUpdateSettings } from '../api'

const overview = ref<AgentUpdateOverview | null>(null)
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')
const manifestFile = ref<File | null>(null)
const artifactFile = ref<File | null>(null)
const schedule = reactive<AgentUpdateSettings>({ autoEnabled: false, windowStartMinute: 0, windowEndMinute: 60, batchSize: 1, campaignPaused: false })
const selectedRelease = ref('')
const uploading = ref(false)
const eligibleNodes = computed(() => overview.value?.nodes.filter((node) => node.capabilities.includes('agent.updates.v1')) ?? [])

function toTime(value: number): string {
  return `${String(Math.floor(value / 60)).padStart(2, '0')}:${String(value % 60).padStart(2, '0')}`
}
function fromTime(value: string): number {
  const [hour = '0', minute = '0'] = value.split(':')
  return Math.max(0, Math.min(1439, Number(hour) * 60 + Number(minute)))
}

async function refresh() {
  loading.value = true
  error.value = ''
  try {
    overview.value = await api.agentUpdates()
    Object.assign(schedule, overview.value.settings)
    selectedRelease.value = selectedRelease.value || schedule.releaseId || overview.value.releases[0]?.id || ''
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法加载 Agent 更新状态。'
  } finally { loading.value = false }
}

async function upload() {
  if (!manifestFile.value || !artifactFile.value) return
  uploading.value = true; error.value = ''; notice.value = ''
  try {
    const result = await api.uploadAgentRelease(manifestFile.value, artifactFile.value)
    notice.value = `已验证并保存 Agent ${result.version} (${result.architecture})。`
    manifestFile.value = null; artifactFile.value = null
    await refresh()
    selectedRelease.value = result.id
  } catch (reason) { error.value = reason instanceof Error ? reason.message : '发布上传失败。' }
  finally { uploading.value = false }
}

async function saveSchedule() {
  saving.value = true; error.value = ''; notice.value = ''
  try {
    const result = await api.saveAgentUpdateSettings({ ...schedule, releaseId: selectedRelease.value })
    Object.assign(schedule, result.settings)
    notice.value = schedule.autoEnabled ? '自动更新窗口和批次设置已保存。' : '自动更新保持关闭，设置已保存。'
    await refresh()
  } catch (reason) { error.value = reason instanceof Error ? reason.message : '更新策略保存失败。' }
  finally { saving.value = false }
}

async function updateNode(nodeId: string) {
  if (!selectedRelease.value) return
  error.value = ''; notice.value = ''
  try { await api.requestAgentUpdate(nodeId, selectedRelease.value); notice.value = '更新任务已排入队列。'; await refresh() }
  catch (reason) { error.value = reason instanceof Error ? reason.message : '更新任务提交失败。' }
}

async function resumeCampaign() {
  error.value = ''
  try { await api.resumeAgentUpdateCampaign(); notice.value = '已解除批次暂停。'; await refresh() }
  catch (reason) { error.value = reason instanceof Error ? reason.message : '无法恢复更新批次。' }
}

onMounted(refresh)
</script>

<template>
  <main class="updates-page">
    <header class="updates-heading">
      <div><span class="eyebrow">AGENT LIFECYCLE</span><h1>Agent 更新</h1><p>上传由可信 Ed25519 密钥签名的 Linux Agent 发布包，逐节点或按窗口分批更新。</p></div>
      <button class="secondary-button" type="button" :disabled="loading" @click="refresh">{{ loading ? '刷新中…' : '刷新状态' }}</button>
    </header>
    <p v-if="error" class="update-message error" role="alert">{{ error }}</p>
    <p v-if="notice" class="update-message success" role="status">{{ notice }}</p>

    <section class="update-card">
      <div class="update-card-title"><span>01</span><div><h2>发布版本</h2><p>Core 会验证签名清单和二进制摘要；私钥不上传。</p></div></div>
      <div class="release-form">
        <label class="field"><span>签名清单 JSON</span><input type="file" accept="application/json,.json" @change="manifestFile = ($event.target as HTMLInputElement).files?.[0] ?? null" /></label>
        <label class="field"><span>Linux Agent 二进制</span><input type="file" @change="artifactFile = ($event.target as HTMLInputElement).files?.[0] ?? null" /></label>
        <button class="primary-button" type="button" :disabled="uploading || !manifestFile || !artifactFile" @click="upload">{{ uploading ? '验证并上传…' : '验证并上传发布包' }}</button>
      </div>
      <div v-if="overview?.releases.length" class="release-list">
        <h3>已验证发布</h3><ul><li v-for="release in overview.releases" :key="release.id"><strong>{{ release.version }}</strong><span>{{ release.os }}/{{ release.architecture }} · 协议 {{ release.manifest.minProtocol }}–{{ release.manifest.maxProtocol }}</span></li></ul>
      </div>
    </section>

    <section class="update-card">
      <div class="update-card-title"><span>02</span><div><h2>自动更新窗口</h2><p>默认关闭。批次内任一节点失败会暂停剩余节点，需管理员检查后恢复。</p></div></div>
      <div class="schedule-grid">
        <label class="toggle-row"><input v-model="schedule.autoEnabled" type="checkbox" /><span>启用自动更新</span></label>
        <label class="field"><span>发布版本</span><select v-model="selectedRelease"><option value="" disabled>选择已验证版本</option><option v-for="release in overview?.releases ?? []" :key="release.id" :value="release.id">{{ release.version }} · {{ release.architecture }}</option></select></label>
        <label class="field"><span>窗口开始（UTC）</span><input type="time" :value="toTime(schedule.windowStartMinute)" @input="schedule.windowStartMinute = fromTime(($event.target as HTMLInputElement).value)" /></label>
        <label class="field"><span>窗口结束（UTC）</span><input type="time" :value="toTime(schedule.windowEndMinute)" @input="schedule.windowEndMinute = fromTime(($event.target as HTMLInputElement).value)" /></label>
        <label class="field"><span>每批节点数</span><input v-model.number="schedule.batchSize" type="number" min="1" max="100" /></label>
        <button class="primary-button" type="button" :disabled="saving || !selectedRelease" @click="saveSchedule">{{ saving ? '保存中…' : '保存更新策略' }}</button>
      </div>
      <div v-if="schedule.campaignPaused" class="campaign-paused"><span>批次已暂停：{{ schedule.lastError || '请检查失败节点和任务结果。' }}</span><button class="secondary-button" type="button" @click="resumeCampaign">检查后恢复</button></div>
    </section>

    <section class="update-card">
      <div class="update-card-title"><span>03</span><div><h2>节点更新</h2><p>活动任务或数据流期间自动推迟；重复提交会复用现有节点任务。</p></div></div>
      <div class="node-update-list">
        <article v-for="node in eligibleNodes" :key="node.nodeId" class="node-update-row"><div><strong>{{ node.displayName }}</strong><span>{{ node.agentVersion || '版本未知' }} · {{ node.status }}</span></div><button class="secondary-button" type="button" :disabled="!selectedRelease" @click="updateNode(node.nodeId)">手动更新</button></article>
        <p v-if="eligibleNodes.length === 0" class="empty-state">当前没有声明更新能力的 Agent。旧版 Agent 请使用安装脚本或 SSH 重新安装以部署 helper。</p>
      </div>
    </section>

    <section class="update-card">
      <div class="update-card-title"><span>04</span><div><h2>任务历史</h2><p>结果以 Agent 回报和重新连接后的实际版本为准。</p></div></div>
      <div class="task-table-wrap"><table><thead><tr><th>节点</th><th>版本</th><th>方式</th><th>状态</th><th>说明</th><th>更新时间</th></tr></thead><tbody><tr v-for="task in overview?.tasks ?? []" :key="task.id"><td>{{ overview?.nodes.find((node) => node.nodeId === task.nodeId)?.displayName ?? task.nodeId }}</td><td>{{ task.version ?? task.releaseId }}</td><td>{{ task.mode === 'automatic' ? '自动' : '手动' }}</td><td><span class="task-status" :data-status="task.status">{{ task.status }}</span></td><td>{{ task.reason || '—' }}</td><td>{{ new Date(task.updatedAt).toLocaleString() }}</td></tr><tr v-if="(overview?.tasks.length ?? 0) === 0"><td colspan="6" class="empty-state">尚无更新任务。</td></tr></tbody></table></div>
    </section>
  </main>
</template>

<style scoped>
.updates-page{width:min(100%,1120px);margin:0 auto;padding:42px 0 70px;display:grid;gap:18px}.updates-heading{display:flex;align-items:end;justify-content:space-between;gap:20px;margin-bottom:4px}.updates-heading h1{margin:10px 0 5px;font:700 clamp(28px,4vw,38px)/1.15 Manrope,sans-serif;letter-spacing:-.055em}.updates-heading p,.update-card-title p{margin:0;color:#8e9db2;font-size:12px;line-height:1.6}.update-card{min-width:0;border:1px solid rgba(175,197,229,.14);border-radius:13px;padding:22px 24px;background:linear-gradient(145deg,rgba(19,29,44,.91),rgba(15,23,35,.87));box-shadow:0 17px 42px rgba(0,0,0,.16)}.update-card-title{display:flex;align-items:start;gap:13px;padding-bottom:16px;border-bottom:1px solid var(--line)}.update-card-title>span{color:#82a9e6;font:11px 'DM Mono',monospace}.update-card-title h2{margin:0 0 4px;font:600 16px Manrope,sans-serif}.release-form{display:grid;grid-template-columns:1fr 1fr auto;align-items:end;gap:14px;padding-top:18px}.field{display:grid;gap:8px;min-width:0;color:#c1cada;font-size:12px;font-weight:600}.field input,.field select{min-width:0;width:100%;min-height:42px;border:1px solid rgba(177,195,222,.16);border-radius:8px;padding:0 10px;color:#edf3ff;background:#101a29;font:12px inherit}.field input[type=file]{padding:8px}.field input[type=time]{color-scheme:dark}.release-list{margin-top:20px}.release-list h3{font-size:12px}.release-list ul{display:grid;gap:7px;margin:0;padding:0;list-style:none}.release-list li,.node-update-row{display:flex;align-items:center;justify-content:space-between;gap:16px;border-top:1px solid var(--line);padding:10px 0}.release-list li strong,.node-update-row strong{font-size:12px}.release-list li span,.node-update-row span{display:block;color:#8291a7;font-size:10px}.schedule-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));align-items:end;gap:16px;padding-top:18px}.toggle-row{display:flex;min-height:42px;align-items:center;gap:9px;color:#d2dced;font-size:12px}.toggle-row input{accent-color:#8db5fb}.campaign-paused{display:flex;align-items:center;justify-content:space-between;gap:14px;margin-top:16px;border:1px solid rgba(239,161,103,.23);border-radius:8px;padding:12px;color:#f1c49a;font-size:11px}.node-update-list{padding-top:8px}.node-update-row>div{min-width:0}.task-table-wrap{overflow-x:auto;margin-top:15px}table{width:100%;border-collapse:collapse;text-align:left;font-size:11px}th,td{border-bottom:1px solid var(--line);padding:11px 9px;vertical-align:top}th{color:#8494aa;font-weight:600;white-space:nowrap}td{color:#c8d3e4;overflow-wrap:anywhere}.task-status{display:inline-flex;border-radius:99px;padding:3px 8px;background:rgba(132,168,226,.12);color:#a9c8ff;font-size:10px}.task-status[data-status=failed],.task-status[data-status=paused]{background:rgba(224,97,97,.13);color:#ffb2aa}.task-status[data-status=succeeded]{background:rgba(65,173,119,.14);color:#a8e7c4}.empty-state{padding:20px 0;color:#7f8da2;font-size:11px;line-height:1.6}.update-message{border-radius:8px;padding:10px 12px;font-size:12px}.update-message.error{border:1px solid rgba(236,126,126,.2);color:#ffc0b9;background:rgba(126,39,49,.17)}.update-message.success{border:1px solid rgba(101,206,153,.2);color:#a7e8c4;background:rgba(38,120,83,.14)}
@media(max-width:760px){.updates-page{padding-top:28px}.updates-heading{align-items:start}.release-form{grid-template-columns:1fr}.schedule-grid{grid-template-columns:1fr 1fr}.update-card{padding:18px 15px}.task-table-wrap table{min-width:760px}.node-update-row{align-items:start}}
@media(max-width:460px){.updates-heading{flex-direction:column}.schedule-grid{grid-template-columns:1fr}.campaign-paused{align-items:start;flex-direction:column}.release-list li{align-items:start;flex-direction:column}}
</style>
