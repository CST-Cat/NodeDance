<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { api, type TailscaleDeploymentTask, type TailscalePeer } from '../api'
import AgentEnrollment from './AgentEnrollment.vue'

const emit = defineEmits<{ back: [] }>()
const peers = ref<TailscalePeer[]>([])
const loading = ref(false)
const discovering = ref(false)
const error = ref('')
const notice = ref('')
const selected = ref<TailscalePeer | null>(null)
const fingerprint = ref('')
const fingerprintLoading = ref(false)
const task = ref<TailscaleDeploymentTask | null>(null)
const unresolvedDeployments = ref<TailscaleDeploymentTask[]>([])
const activeDeploymentKey = ref('')
const activeDeploymentSignature = ref('')
const resolutionOutcome = ref<'succeeded' | 'failed'>('succeeded')
const resolutionObservedState = ref('connected')
const resolutionConfirmed = ref(false)
const credentials = reactive({ user: '', password: '', privateKey: '', passphrase: '' })
const form = reactive({
  displayName: '',
  fileRoot: '',
  disableFileRoot: false,
  coreUrl: window.location.protocol === 'https:' ? window.location.origin : '',
  fallbackUrl: '',
  allowFallback: false,
  confirmHostKey: false,
  confirmChangedHostKey: false,
  usePrivateKey: false,
})

const candidates = computed(() => peers.value.filter((peer) => !peer.managed && peer.class === 'linux'))
const unsupported = computed(() => peers.value.filter((peer) => !peer.managed && peer.class !== 'linux'))
const managed = computed(() => peers.value.filter((peer) => peer.managed))
const taskFinished = computed(() => !!task.value && ['succeeded', 'failed', 'timed_out', 'canceled', 'unknown'].includes(task.value.status))
const rescueCommand = computed(() => {
  if (!selected.value?.ips.length) return ''
  const address = selected.value.ips[0]
  const host = address.includes(':') ? `[${address}]` : address
  return `ssh -- ${shellQuote(`${credentials.user || '<SSH用户名>'}@${host}`)}`
})

function peerLabel(peer: TailscalePeer): string { return peer.name || peer.dnsName || peer.identity }
function stateLabel(peer: TailscalePeer): string { return peer.online ? '在线' : '离线' }
function clearNotice() { error.value = ''; notice.value = '' }

function changeResolutionOutcome(event: Event) {
  const value = (event.target as HTMLSelectElement).value
  if (value !== 'succeeded' && value !== 'failed') return
  resolutionConfirmed.value = false
  resolutionOutcome.value = value
  resolutionObservedState.value = value === 'succeeded' ? 'connected' : 'installation_failed'
}

function changeResolutionObservedState(event: Event) {
  const value = (event.target as HTMLSelectElement).value
  if (!['connected', 'unavailable', 'absent'].includes(value)) return
  resolutionConfirmed.value = false
  resolutionObservedState.value = value
}

async function discover() {
  clearNotice()
  discovering.value = true
  try {
    const result = await api.tailscalePeers()
    peers.value = result.peers
    if (selected.value) selected.value = peers.value.find((peer) => peer.identity === selected.value?.identity) ?? null
    if (!peers.value.length) notice.value = '当前 Tailscale 账号没有 Core 可见的其他节点。'
  } catch (reason) {
    peers.value = []
    error.value = message(reason)
  } finally {
    discovering.value = false
  }
}

async function refreshDeploymentTasks() {
  try {
    const result = await api.tailscaleDeployments()
    unresolvedDeployments.value = result.tasks
    if (task.value) {
      const current = result.tasks.find((candidate) => candidate.taskId === task.value?.taskId)
      if (current) task.value = current
    } else {
      task.value = result.tasks[0] ?? null
    }
  } catch (reason) {
    error.value = `无法恢复未完成的 Agent 部署任务：${message(reason)}`
  }
}

function inspectDeploymentTask(deployment: TailscaleDeploymentTask) {
  task.value = deployment
  resolutionConfirmed.value = false
}

function choose(peer: TailscalePeer) {
  clearNotice()
  selected.value = peer
  fingerprint.value = ''
  task.value = null
  activeDeploymentKey.value = ''
  activeDeploymentSignature.value = ''
  resolutionConfirmed.value = false
  credentials.password = ''
  credentials.privateKey = ''
  credentials.passphrase = ''
  form.displayName = peerLabel(peer)
  form.fileRoot = ''
  form.disableFileRoot = false
  form.confirmHostKey = false
  form.confirmChangedHostKey = false
}

async function probeHostKey() {
  if (!selected.value) return
  clearNotice()
  fingerprintLoading.value = true
  try {
    const result = await api.probeTailscaleSSH(selected.value.identity)
    fingerprint.value = result.fingerprint
  } catch (reason) {
    error.value = message(reason)
  } finally {
    fingerprintLoading.value = false
  }
}

async function startDeployment() {
  if (!selected.value || !fingerprint.value) return
  clearNotice()
  loading.value = true
  const auth = form.usePrivateKey
    ? { user: credentials.user, privateKey: credentials.privateKey, passphrase: credentials.passphrase || undefined }
    : { user: credentials.user, password: credentials.password }
  const payload = {
    peerIdentity: selected.value.identity,
    displayName: form.displayName,
    fileRoot: form.disableFileRoot ? undefined : form.fileRoot.trim() || undefined,
    disableFileRoot: form.disableFileRoot,
    coreUrl: form.coreUrl,
    fallbackUrl: form.fallbackUrl || undefined,
    allowFallback: form.allowFallback,
    hostFingerprint: fingerprint.value,
    confirmHostKey: form.confirmHostKey,
    confirmChangedHostKey: form.confirmChangedHostKey,
    credentials: auth,
  }
  const signature = JSON.stringify({
    peerIdentity: payload.peerIdentity, displayName: payload.displayName, fileRoot: payload.fileRoot,
    disableFileRoot: payload.disableFileRoot, coreUrl: payload.coreUrl, fallbackUrl: payload.fallbackUrl,
    allowFallback: payload.allowFallback, hostFingerprint: payload.hostFingerprint,
    confirmChangedHostKey: payload.confirmChangedHostKey,
    sshUser: auth.user, authentication: form.usePrivateKey ? 'private_key' : 'password',
  })
  if (!activeDeploymentKey.value || activeDeploymentSignature.value !== signature) {
    activeDeploymentKey.value = crypto.randomUUID()
    activeDeploymentSignature.value = signature
  }
  try {
    const accepted = await api.startTailscaleDeployment(payload, activeDeploymentKey.value)
    task.value = accepted
    unresolvedDeployments.value = [accepted, ...unresolvedDeployments.value.filter((item) => item.taskId !== accepted.taskId)]
    activeDeploymentKey.value = ''
    activeDeploymentSignature.value = ''
    credentials.password = ''
    credentials.privateKey = ''
    credentials.passphrase = ''
    await pollTask(accepted.taskId)
  } catch (reason) {
    error.value = message(reason)
  } finally {
    loading.value = false
  }
}

async function resolveUnknownDeployment() {
  if (!task.value || task.value.status !== 'unknown' || !resolutionConfirmed.value) return
  clearNotice()
  try {
    task.value = await api.resolveTailscaleDeployment(task.value.taskId, {
      outcome: resolutionOutcome.value,
      observedState: resolutionObservedState.value,
    })
    resolutionConfirmed.value = false
    await refreshDeploymentTasks()
    if (task.value.status === 'succeeded') await discover()
  } catch (reason) {
    error.value = message(reason)
  }
}

async function pollTask(id: string) {
  for (let attempt = 0; attempt < 360; attempt += 1) {
    await new Promise((resolve) => window.setTimeout(resolve, 1500))
    try {
      task.value = await api.tailscaleDeployment(id)
    } catch (reason) {
      error.value = `无法读取部署任务状态：${message(reason)}`
      return
    }
    if (taskFinished.value) {
      await refreshDeploymentTasks()
      if (task.value?.status === 'succeeded') {
        const successMessage = task.value.message || 'Agent 已真实连接到 Core。'
        await discover()
        if (!error.value) notice.value = successMessage
      } else if (task.value?.status === 'unknown') {
        error.value = task.value.message || '部署结果待确认，请先检查目标机的 systemd 与 Agent 状态。'
      } else {
        error.value = task.value?.message || 'SSH 部署失败。请检查权限、主机指纹和系统状态后重试。'
      }
      return
    }
  }
  error.value = '部署任务仍在运行；可以稍后刷新任务状态，不要重复执行可能已完成的部署。'
}

function message(reason: unknown): string {
  return reason instanceof Error && reason.message ? reason.message : '请求失败，请刷新节点列表后重试。'
}

function shellQuote(value: string): string { return `'${value.replaceAll("'", "'\\''")}'` }

onMounted(() => { void discover(); void refreshDeploymentTasks() })
</script>

<template>
  <main class="tailscale-page" data-testid="tailscale-discovery">
    <header class="discovery-heading">
      <div><span class="eyebrow">NODE DISCOVERY</span><h1>Tailscale 节点发现</h1><p>读取运行 Core 的机器当前可见的 Tailnet 节点。发现不会自动安装或纳管节点。</p></div>
      <button class="secondary-button" type="button" :disabled="discovering" @click="discover">{{ discovering ? '正在读取…' : '刷新节点' }}</button>
    </header>

    <div v-if="error" class="alert alert-error" role="alert">{{ error }}</div>
    <div v-if="notice" class="alert alert-success" role="status">{{ notice }}</div>

    <section v-if="unresolvedDeployments.length" class="discovery-card" aria-labelledby="unresolved-deployments-title" data-testid="unresolved-deployments">
      <div class="discovery-section-heading"><div><h2 id="unresolved-deployments-title">未完成的 Agent 部署</h2><p>任务从 Core Tasks 恢复；未知结果需先检查目标节点，再记录核实结果。</p></div><button class="quiet-button" type="button" @click="refreshDeploymentTasks">刷新任务</button></div>
      <div class="managed-list"><article v-for="deployment in unresolvedDeployments" :key="deployment.taskId">
        <strong>{{ deployment.peerName || deployment.peerIdentity }}</strong><span>{{ deployment.phase }} · {{ deployment.status }}</span><code>{{ deployment.taskId }}</code>
        <button class="secondary-button" type="button" @click="inspectDeploymentTask(deployment)">{{ deployment.status === 'unknown' ? '核实并解除占用' : '查看任务' }}</button>
      </article></div>
    </section>

    <section class="discovery-card" aria-labelledby="candidates-title">
      <div class="discovery-section-heading"><div><h2 id="candidates-title">可部署的 Linux 节点</h2><p>部署前会逐台确认 SSH 主机指纹、系统、架构、权限及 Agent 签名。</p></div><span class="count-pill">{{ candidates.length }}</span></div>
      <div v-if="discovering" class="empty-state" role="status">正在读取 Core 本机的 Tailscale 状态…</div>
      <div v-else-if="candidates.length" class="peer-grid">
        <article v-for="peer in candidates" :key="peer.identity" class="peer-card" :data-identity="peer.identity">
          <div class="peer-main"><span class="peer-icon" aria-hidden="true">⌘</span><div><strong>{{ peerLabel(peer) }}</strong><span>{{ peer.os }} · {{ peer.ips.join(' · ') }}</span></div><span class="peer-state" :data-online="peer.online">{{ stateLabel(peer) }}</span></div>
          <small class="peer-id">稳定设备身份：{{ peer.identity }}</small>
          <button class="secondary-button" type="button" :disabled="!peer.online" @click="choose(peer)">{{ peer.online ? '查看部署选项' : '节点离线' }}</button>
        </article>
      </div>
      <div v-else-if="!error && !discovering" class="empty-state">未发现可部署的 Linux 节点。请确认 Core 已登录 Tailnet，且该节点在当前账号的可见范围内。</div>
    </section>

    <section v-if="managed.length" class="discovery-card" aria-labelledby="managed-title">
      <div class="discovery-section-heading"><div><h2 id="managed-title">已关联的受管节点</h2><p>关联使用 Tailscale 稳定设备身份，不依赖机器名或 IP。</p></div><span class="count-pill">{{ managed.length }}</span></div>
      <div class="managed-list"><article v-for="peer in managed" :key="peer.identity"><strong>{{ peerLabel(peer) }}</strong><span>{{ peer.ips.join(' · ') }}</span><code>{{ peer.nodeId }}</code><span class="peer-state" :data-online="peer.online">{{ peer.online ? 'Tailnet 在线' : 'Tailnet 离线' }}</span></article></div>
    </section>

    <section v-if="unsupported.length" class="discovery-card muted-card" aria-labelledby="unsupported-title">
      <div class="discovery-section-heading"><div><h2 id="unsupported-title">暂不支持的节点</h2><p>当前 SSH 辅助部署只支持 Linux amd64/arm64。</p></div><span class="count-pill">{{ unsupported.length }}</span></div>
      <ul class="unsupported-list"><li v-for="peer in unsupported" :key="peer.identity"><strong>{{ peerLabel(peer) }}</strong><span>{{ peer.os || '未知操作系统' }} · {{ peer.ips.join(' · ') }}</span></li></ul>
    </section>

    <section v-if="selected" class="discovery-card deploy-card" aria-labelledby="deploy-title">
      <div class="discovery-section-heading"><div><span class="eyebrow">{{ selected.name || selected.identity }}</span><h2 id="deploy-title">部署 Agent</h2><p>目标地址只从刚刚读取的 Tailscale IP 中选择。主机名和 IP 变化不会改变稳定身份。</p></div><button class="quiet-button" type="button" @click="selected = null">关闭</button></div>
      <div class="deploy-columns">
        <div class="deploy-form">
          <label class="field"><span>NodeDance 显示名称</span><input v-model="form.displayName" maxlength="80" autocomplete="off" /></label>
          <label class="field"><span>Agent 主机文件根目录（可选）</span><input v-model="form.fileRoot" :disabled="form.disableFileRoot" autocomplete="off" placeholder="/srv/nodedance-files" /><small class="field-hint">新部署留空时禁用文件管理；重装时保留目标机已明确保存的目录。配置后只开放该现有绝对目录，服务账号仍须拥有对应的 Linux 读写权限。</small></label>
          <label class="check-row"><input v-model="form.disableFileRoot" type="checkbox" /><span>即使目标机已有配置，也明确禁用主机文件管理</span></label>
          <label class="field"><span>Core HTTPS 地址</span><input v-model="form.coreUrl" inputmode="url" placeholder="https://panel.example.ts.net" /><small class="field-hint">Agent 使用 HTTPS，证书验证始终开启。HTTP 不受支持。</small></label>
          <label class="field"><span>备用 HTTPS 地址（可选）</span><input v-model="form.fallbackUrl" inputmode="url" placeholder="https://backup.example.net" /></label>
          <label class="check-row"><input v-model="form.allowFallback" type="checkbox" /><span>明确允许主地址连接失败时尝试备用地址</span></label>
          <p class="field-hint">未勾选时不会请求备用地址。主地址和备用地址都必须通过标准 TLS 证书验证。</p>
          <label class="field"><span>SSH 用户</span><input v-model="credentials.user" autocomplete="username" /></label>
          <div class="auth-mode" role="group" aria-label="SSH 认证方式"><button type="button" :aria-pressed="!form.usePrivateKey" @click="form.usePrivateKey = false">一次性密码</button><button type="button" :aria-pressed="form.usePrivateKey" @click="form.usePrivateKey = true">私钥</button></div>
          <label v-if="!form.usePrivateKey" class="field"><span>SSH 密码</span><input v-model="credentials.password" type="password" autocomplete="new-password" /><small class="field-hint">只在本次请求的内存中使用，不保存到部署历史。</small></label>
          <template v-else>
            <label class="field"><span>SSH 私钥</span><textarea v-model="credentials.privateKey" rows="6" spellcheck="false" autocomplete="off" /></label>
            <label class="field"><span>私钥口令（如需要）</span><input v-model="credentials.passphrase" type="password" autocomplete="new-password" /></label>
          </template>
        </div>
        <div class="hostkey-panel">
          <h3>SSH 主机身份验证</h3>
          <p>先读取 SSH host key 指纹，并通过你可信的独立渠道核对目标主机。首次接受及后续变化都需要明确确认。</p>
          <button class="secondary-button" type="button" :disabled="fingerprintLoading" @click="probeHostKey">{{ fingerprintLoading ? '正在读取指纹…' : '读取 SSH host key 指纹' }}</button>
          <code v-if="fingerprint" class="fingerprint" data-testid="ssh-fingerprint">{{ fingerprint }}</code>
          <label v-if="fingerprint" class="check-row"><input v-model="form.confirmHostKey" type="checkbox" /><span>我已通过独立可信渠道核对该指纹</span></label>
          <label v-if="fingerprint" class="check-row"><input v-model="form.confirmChangedHostKey" type="checkbox" /><span>若它与此前保存的指纹不同，我明确确认接受此次变更</span></label>
          <button class="primary-button full-button" type="button" :disabled="loading || !fingerprint || !form.confirmHostKey || !credentials.user || !form.displayName" @click="startDeployment">{{ loading ? '部署中…' : '启动 SSH 部署任务' }}</button>
        </div>
      </div>

      <aside class="ssh-rescue" aria-label="独立 SSH 救援信息">
        <div><h3>独立 SSH 救援</h3><p>部署失败时仍可从其他终端直接连接目标节点；此连接不依赖 NodeDance Agent。</p></div>
        <div class="rescue-addresses"><span>当前可见 Tailscale 地址</span><code>{{ selected.ips.join(' · ') || '暂无可用地址' }}</code></div>
        <pre v-if="selected.ips.length"><code>{{ rescueCommand }}</code></pre>
        <p class="field-hint">用已核对的 host key 和 SSH 用户登录后，可独立查看 <code>systemctl status nodedance-agent</code>；不要把密码、私钥或一次性注册 Token 添加到此命令。</p>
      </aside>

    </section>

    <section v-if="task" class="discovery-card" aria-labelledby="deployment-task-title" data-testid="deployment-task-section">
      <div class="discovery-section-heading"><div><h2 id="deployment-task-title">Agent 部署任务</h2><p>任务状态来自共享 Core Tasks 存储。</p></div><button class="quiet-button" type="button" @click="task = null">关闭</button></div>
      <div class="task-status" data-testid="deployment-task" :data-status="task.status" role="status"><strong>{{ task.phase }} · {{ task.status }}</strong><span>{{ task.message }}</span><small v-if="task.nodeId">Node ID：{{ task.nodeId }}</small></div>
      <div v-if="task.status === 'unknown'" class="task-status task-review" data-testid="deployment-review">
        <strong>检查目标 VPS 后解除部署占用</strong>
        <p>请先通过独立 SSH 检查 systemd、Agent 注册和节点在线状态。确认结果会写入任务审计并释放该 Tailscale peer 的任务占用。</p>
        <label class="field"><span>核实结果</span><select :value="resolutionOutcome" @change="changeResolutionOutcome">
          <option value="succeeded">Agent 已安装并上线</option><option value="failed">Agent 未安装或安装失败</option>
        </select></label>
        <label v-if="resolutionOutcome === 'succeeded'" class="field"><span>Docker 状态</span><select :value="resolutionObservedState" @change="changeResolutionObservedState">
          <option value="connected">Docker 可用</option><option value="unavailable">Docker 不可用</option><option value="absent">主机没有 Docker</option>
        </select></label>
        <label class="check-row"><input v-model="resolutionConfirmed" type="checkbox" /><span>我已检查目标 VPS，并确认上述真实结果</span></label>
        <button class="secondary-button" type="button" :disabled="!resolutionConfirmed" @click="resolveUnknownDeployment">记录核实结果</button>
      </div>
    </section>

    <AgentEnrollment compact />

    <footer class="discovery-footer"><span>只展示 Core 运行环境当前有权看到的 Tailnet 节点。</span><button class="quiet-button" type="button" @click="emit('back')">返回控制台</button></footer>
  </main>
</template>

<style scoped>
.tailscale-page{max-width:1180px;margin:0 auto;padding:2rem clamp(1rem,3vw,2.5rem) 3rem;color:var(--text-primary,#e7edf8)}
.discovery-heading,.discovery-section-heading,.peer-main,.discovery-footer{display:flex;align-items:center;justify-content:space-between;gap:1rem}
.discovery-heading{margin:0 0 1.5rem}.discovery-heading h1{font-size:clamp(1.65rem,3vw,2.25rem);margin:.35rem 0}.discovery-heading p,.discovery-section-heading p,.hostkey-panel p{color:var(--text-muted,#9ba8bd);line-height:1.55;margin:.25rem 0}
.eyebrow{font-size:.7rem;letter-spacing:.13em;color:#9daed0}.discovery-card{background:var(--surface,#151e2d);border:1px solid var(--border,#27354a);border-radius:18px;padding:1.2rem;margin:1rem 0 1.3rem;box-shadow:0 14px 42px #050b1420}.discovery-section-heading{margin-bottom:1rem}.discovery-section-heading h2{font-size:1.15rem;margin:.1rem 0 .25rem}.count-pill{border-radius:2rem;padding:.35rem .7rem;background:#243a5b;color:#bfd8ff;font-variant-numeric:tabular-nums}
.peer-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,310px),1fr));gap:.8rem}.peer-card{padding:1rem;border:1px solid var(--border,#27354a);border-radius:14px;display:grid;gap:.75rem}.peer-main{justify-content:flex-start}.peer-icon{width:2.5rem;height:2.5rem;display:grid;place-items:center;border-radius:12px;background:#213653;color:#a8caff;font-size:1.2rem;flex:none}.peer-main>div{display:grid;gap:.2rem;min-width:0}.peer-main>div span,.peer-id,.managed-list span,.unsupported-list span{font-size:.82rem;color:var(--text-muted,#9ba8bd);overflow-wrap:anywhere}.peer-state{margin-left:auto;font-size:.78rem;color:#e4bd72}.peer-state[data-online=true]{color:#7dd8aa}.peer-card>.secondary-button{justify-self:start}.peer-id{font-size:.72rem}.empty-state{border:1px dashed var(--border,#34435a);border-radius:12px;padding:1.2rem;color:var(--text-muted,#9ba8bd);line-height:1.6}.managed-list{display:grid;gap:.6rem}.managed-list article{display:flex;align-items:center;gap:1rem;flex-wrap:wrap;border-top:1px solid var(--border,#27354a);padding:.8rem 0}.managed-list code{font-size:.75rem;color:#a9bddb;overflow-wrap:anywhere}.muted-card{opacity:.87}.unsupported-list{list-style:none;padding:0;margin:0;display:grid;gap:.6rem}.unsupported-list li{display:flex;gap:1rem;flex-wrap:wrap}
.deploy-columns{display:grid;grid-template-columns:minmax(0,1fr) minmax(280px,.8fr);gap:1.3rem}.deploy-form{display:grid;gap:.8rem}.field{display:grid;gap:.4rem}.field>span{font-size:.86rem;font-weight:600}.field input,.field textarea{width:100%;box-sizing:border-box;border:1px solid var(--border,#33445e);border-radius:10px;background:#0e1725;color:inherit;padding:.72rem .8rem;font:inherit}.field textarea{resize:vertical;font-family:ui-monospace,SFMono-Regular,monospace;font-size:.8rem}.field-hint{font-size:.75rem;color:var(--text-muted,#9ba8bd);line-height:1.5}.check-row{display:flex;align-items:flex-start;gap:.6rem;font-size:.82rem;line-height:1.45}.check-row input{margin-top:.2rem;accent-color:#63a6ff}.auth-mode{display:flex;gap:.5rem}.auth-mode button{border:1px solid var(--border,#33445e);border-radius:999px;padding:.45rem .85rem;background:transparent;color:inherit}.auth-mode button[aria-pressed=true]{background:#25446c;border-color:#568fd3}.hostkey-panel{border:1px solid var(--border,#27354a);background:#101a29;border-radius:14px;padding:1rem;align-self:start;display:grid;gap:.8rem}.hostkey-panel h3{margin:.2rem 0}.fingerprint{padding:.7rem;background:#080f19;border-radius:9px;overflow-wrap:anywhere;color:#bed8ff}.task-status{display:grid;gap:.35rem;border-radius:10px;padding:.8rem;background:#0b1522;border:1px solid var(--border,#27354a);font-size:.82rem;line-height:1.5}.task-status[data-status=succeeded]{border-color:#327a56}.task-status[data-status=failed],.task-status[data-status=unknown]{border-color:#9b5d56}.task-status small{color:var(--text-muted,#9ba8bd)}re{white-space:pre-wrap;overflow-wrap:anywhere;background:#0b1320;padding:.8rem;border-radius:9px}.discovery-footer{margin-top:1.5rem;color:var(--text-muted,#9ba8bd);font-size:.82rem}.full-button{width:100%}
.ssh-rescue{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:.65rem 1rem;align-items:center;margin-top:1rem;padding:1rem;border:1px solid var(--border,#27354a);border-radius:14px;background:#111b2a}.ssh-rescue h3{font-size:1rem;margin:.1rem 0}.ssh-rescue p{color:var(--text-muted,#9ba8bd);line-height:1.5;margin:.25rem 0}.ssh-rescue pre{margin:0;white-space:pre-wrap;overflow-wrap:anywhere;background:#0b1320;padding:.7rem;border-radius:9px}.ssh-rescue .field-hint{grid-column:1/-1}.rescue-addresses{display:grid;gap:.25rem;font-size:.82rem}.rescue-addresses span{color:var(--text-muted,#9ba8bd)}.rescue-addresses code{overflow-wrap:anywhere}
@media(max-width:760px){.tailscale-page{padding-top:1.2rem}.discovery-heading{align-items:flex-start}.discovery-heading>.secondary-button{flex:none}.deploy-columns{grid-template-columns:1fr}.ssh-rescue{grid-template-columns:1fr}.ssh-rescue .field-hint{grid-column:auto}.peer-main{align-items:flex-start;flex-wrap:wrap}.peer-state{margin-left:auto}.managed-list article{align-items:flex-start}.discovery-section-heading{align-items:flex-start}.discovery-footer{align-items:flex-start;flex-direction:column}}
</style>
