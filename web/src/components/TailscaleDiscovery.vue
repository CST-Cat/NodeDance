<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { api, type TailscaleDeploymentTask, type TailscalePeer } from '../api'

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
const manualToken = ref('')
const manualExpiry = ref('')
const credentials = reactive({ user: '', password: '', privateKey: '', passphrase: '' })
const form = reactive({
  displayName: '',
  coreUrl: window.location.protocol === 'https:' ? window.location.origin : '',
  fallbackUrl: '',
  allowFallback: false,
  confirmHostKey: false,
  confirmChangedHostKey: false,
  usePrivateKey: false,
})

watch(() => form.coreUrl, () => {
  manualToken.value = ''
  manualExpiry.value = ''
})

const candidates = computed(() => peers.value.filter((peer) => !peer.managed && peer.class === 'linux'))
const unsupported = computed(() => peers.value.filter((peer) => !peer.managed && peer.class !== 'linux'))
const managed = computed(() => peers.value.filter((peer) => peer.managed))
const taskFinished = computed(() => !!task.value && ['succeeded', 'failed', 'unknown'].includes(task.value.status))
const rescueCommand = computed(() => {
  if (!selected.value?.ips.length) return ''
  const address = selected.value.ips[0]
  const host = address.includes(':') ? `[${address}]` : address
  return `ssh -- ${shellQuote(`${credentials.user || '<SSH用户名>'}@${host}`)}`
})

function peerLabel(peer: TailscalePeer): string { return peer.name || peer.dnsName || peer.identity }
function stateLabel(peer: TailscalePeer): string { return peer.online ? '在线' : '离线' }
function clearNotice() { error.value = ''; notice.value = '' }

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

function choose(peer: TailscalePeer) {
  clearNotice()
  selected.value = peer
  fingerprint.value = ''
  task.value = null
  manualToken.value = ''
  credentials.password = ''
  credentials.privateKey = ''
  credentials.passphrase = ''
  form.displayName = peerLabel(peer)
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

async function createManualCommand() {
  if (!selected.value) return
  clearNotice()
  if (!isCoreHTTPSOrigin(form.coreUrl)) {
    error.value = '请输入可从目标节点访问的 HTTPS Core 地址，再生成手动注册凭据。'
    return
  }
  try {
    const result = await api.createManualEnrollment(form.displayName || peerLabel(selected.value))
    manualToken.value = result.token
    manualExpiry.value = result.expiresAt
    notice.value = '一次性注册凭据已生成。它只能使用一次，并会在十分钟后过期。'
  } catch (reason) {
    error.value = message(reason)
  }
}

async function startDeployment() {
  if (!selected.value || !fingerprint.value) return
  clearNotice()
  loading.value = true
  const auth = form.usePrivateKey
    ? { user: credentials.user, privateKey: credentials.privateKey, passphrase: credentials.passphrase || undefined }
    : { user: credentials.user, password: credentials.password }
  try {
    const accepted = await api.startTailscaleDeployment({
      peerIdentity: selected.value.identity,
      displayName: form.displayName,
      coreUrl: form.coreUrl,
      fallbackUrl: form.fallbackUrl || undefined,
      allowFallback: form.allowFallback,
      hostFingerprint: fingerprint.value,
      confirmHostKey: form.confirmHostKey,
      confirmChangedHostKey: form.confirmChangedHostKey,
      credentials: auth,
    })
    task.value = accepted
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

function copyCommand() {
  if (!selected.value || !manualToken.value) return
  const command = copyEnrollmentCommand()
  void navigator.clipboard?.writeText(command).then(() => { notice.value = '手动安装命令已复制。注册 Token 需在终端提示时粘贴，勿放入命令参数。' })
}

function shellQuote(value: string): string { return `'${value.replaceAll("'", "'\\''")}'` }
function isCoreHTTPSOrigin(value: string): boolean {
  try {
    const parsed = new URL(value.trim())
    return parsed.protocol === 'https:' && !!parsed.hostname && !parsed.username && !parsed.password &&
      (parsed.pathname === '' || parsed.pathname === '/') && !parsed.search && !parsed.hash
  } catch {
    return false
  }
}
function copyEnrollmentCommand(): string {
  return `sudo -u nodedance-agent /usr/local/bin/nodedance-agent enroll --server ${shellQuote(form.coreUrl)} --token-stdin --config /var/lib/nodedance-agent/agent.json`
}

onMounted(() => { void discover() })
</script>

<template>
  <main class="tailscale-page" data-testid="tailscale-discovery">
    <header class="discovery-heading">
      <div><span class="eyebrow">NODE DISCOVERY</span><h1>Tailscale 节点发现</h1><p>读取运行 Core 的机器当前可见的 Tailnet 节点。发现不会自动安装或纳管节点。</p></div>
      <button class="secondary-button" type="button" :disabled="discovering" @click="discover">{{ discovering ? '正在读取…' : '刷新节点' }}</button>
    </header>

    <div v-if="error" class="alert alert-error" role="alert">{{ error }}</div>
    <div v-if="notice" class="alert alert-success" role="status">{{ notice }}</div>

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
          <div v-if="task" class="task-status" data-testid="deployment-task" :data-status="task.status" role="status"><strong>{{ task.phase }} · {{ task.status }}</strong><span>{{ task.message }}</span><small v-if="task.nodeId">Node ID：{{ task.nodeId }}</small></div>
        </div>
      </div>

      <aside class="ssh-rescue" aria-label="独立 SSH 救援信息">
        <div><h3>独立 SSH 救援</h3><p>部署失败时仍可从其他终端直接连接目标节点；此连接不依赖 NodeDance Agent。</p></div>
        <div class="rescue-addresses"><span>当前可见 Tailscale 地址</span><code>{{ selected.ips.join(' · ') || '暂无可用地址' }}</code></div>
        <pre v-if="selected.ips.length"><code>{{ rescueCommand }}</code></pre>
        <p class="field-hint">用已核对的 host key 和 SSH 用户登录后，可独立查看 <code>systemctl status nodedance-agent</code>；不要把密码、私钥或一次性注册 Token 添加到此命令。</p>
      </aside>

      <details class="manual-install">
        <summary>手动安装或通过 SSH 救援</summary>
        <p>从可信发布渠道下载目标架构 Agent，并独立核对发布签名。下面的手动流程在目标机终端执行，不会通过 SSH 运行命令；Core HTTPS 地址使用上方部署表单的值。</p>
        <button class="secondary-button" type="button" :disabled="!isCoreHTTPSOrigin(form.coreUrl)" @click="createManualCommand">生成一次性注册凭据</button>
        <template v-if="manualToken">
          <pre><code>id -u nodedance-agent >/dev/null 2>&1 || sudo useradd --system --home-dir /var/lib/nodedance-agent --shell /usr/sbin/nologin nodedance-agent
sudo install -d -m 0700 -o nodedance-agent -g nodedance-agent /var/lib/nodedance-agent
sudo install -m 0755 ./nodedance-agent /usr/local/bin/nodedance-agent
{{ copyEnrollmentCommand() }}
sudo /usr/local/bin/nodedance-agent install-systemd --user nodedance-agent --config /var/lib/nodedance-agent/agent.json
sudo systemctl enable --now nodedance-agent.service</code></pre>
          <label class="field"><span>一次性 Token（到期 {{ manualExpiry }}）</span><textarea readonly rows="2" :value="manualToken" /></label>
          <button class="quiet-button" type="button" @click="copyCommand">复制不含 Token 的安全命令</button>
          <p class="field-hint">在终端提示时粘贴 Token。不要将 Token 放在命令参数、shell history 或工单中。启用服务前，检查 Docker socket 的 type/mode/UID/GID；若用组读写授权，必须将 socket 的数字 supplementary GID 配入 service unit，不能用 world-writable socket 放行。成功注册后，可独立通过 SSH 检查 <code>systemctl status nodedance-agent</code>。</p>
        </template>
      </details>
    </section>

    <footer class="discovery-footer"><span>只展示 Core 运行环境当前有权看到的 Tailnet 节点。</span><button class="quiet-button" type="button" @click="emit('back')">返回控制台</button></footer>
  </main>
</template>

<style scoped>
.tailscale-page{max-width:1180px;margin:0 auto;padding:2rem clamp(1rem,3vw,2.5rem) 3rem;color:var(--text-primary,#e7edf8)}
.discovery-heading,.discovery-section-heading,.peer-main,.discovery-footer{display:flex;align-items:center;justify-content:space-between;gap:1rem}
.discovery-heading{margin:0 0 1.5rem}.discovery-heading h1{font-size:clamp(1.65rem,3vw,2.25rem);margin:.35rem 0}.discovery-heading p,.discovery-section-heading p,.hostkey-panel p,.manual-install p{color:var(--text-muted,#9ba8bd);line-height:1.55;margin:.25rem 0}
.eyebrow{font-size:.7rem;letter-spacing:.13em;color:#9daed0}.discovery-card{background:var(--surface,#151e2d);border:1px solid var(--border,#27354a);border-radius:18px;padding:1.2rem;margin:1rem 0 1.3rem;box-shadow:0 14px 42px #050b1420}.discovery-section-heading{margin-bottom:1rem}.discovery-section-heading h2{font-size:1.15rem;margin:.1rem 0 .25rem}.count-pill{border-radius:2rem;padding:.35rem .7rem;background:#243a5b;color:#bfd8ff;font-variant-numeric:tabular-nums}
.peer-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,310px),1fr));gap:.8rem}.peer-card{padding:1rem;border:1px solid var(--border,#27354a);border-radius:14px;display:grid;gap:.75rem}.peer-main{justify-content:flex-start}.peer-icon{width:2.5rem;height:2.5rem;display:grid;place-items:center;border-radius:12px;background:#213653;color:#a8caff;font-size:1.2rem;flex:none}.peer-main>div{display:grid;gap:.2rem;min-width:0}.peer-main>div span,.peer-id,.managed-list span,.unsupported-list span{font-size:.82rem;color:var(--text-muted,#9ba8bd);overflow-wrap:anywhere}.peer-state{margin-left:auto;font-size:.78rem;color:#e4bd72}.peer-state[data-online=true]{color:#7dd8aa}.peer-card>.secondary-button{justify-self:start}.peer-id{font-size:.72rem}.empty-state{border:1px dashed var(--border,#34435a);border-radius:12px;padding:1.2rem;color:var(--text-muted,#9ba8bd);line-height:1.6}.managed-list{display:grid;gap:.6rem}.managed-list article{display:flex;align-items:center;gap:1rem;flex-wrap:wrap;border-top:1px solid var(--border,#27354a);padding:.8rem 0}.managed-list code{font-size:.75rem;color:#a9bddb;overflow-wrap:anywhere}.muted-card{opacity:.87}.unsupported-list{list-style:none;padding:0;margin:0;display:grid;gap:.6rem}.unsupported-list li{display:flex;gap:1rem;flex-wrap:wrap}
.deploy-columns{display:grid;grid-template-columns:minmax(0,1fr) minmax(280px,.8fr);gap:1.3rem}.deploy-form{display:grid;gap:.8rem}.field{display:grid;gap:.4rem}.field>span{font-size:.86rem;font-weight:600}.field input,.field textarea{width:100%;box-sizing:border-box;border:1px solid var(--border,#33445e);border-radius:10px;background:#0e1725;color:inherit;padding:.72rem .8rem;font:inherit}.field textarea{resize:vertical;font-family:ui-monospace,SFMono-Regular,monospace;font-size:.8rem}.field-hint{font-size:.75rem;color:var(--text-muted,#9ba8bd);line-height:1.5}.check-row{display:flex;align-items:flex-start;gap:.6rem;font-size:.82rem;line-height:1.45}.check-row input{margin-top:.2rem;accent-color:#63a6ff}.auth-mode{display:flex;gap:.5rem}.auth-mode button{border:1px solid var(--border,#33445e);border-radius:999px;padding:.45rem .85rem;background:transparent;color:inherit}.auth-mode button[aria-pressed=true]{background:#25446c;border-color:#568fd3}.hostkey-panel{border:1px solid var(--border,#27354a);background:#101a29;border-radius:14px;padding:1rem;align-self:start;display:grid;gap:.8rem}.hostkey-panel h3{margin:.2rem 0}.fingerprint{padding:.7rem;background:#080f19;border-radius:9px;overflow-wrap:anywhere;color:#bed8ff}.task-status{display:grid;gap:.35rem;border-radius:10px;padding:.8rem;background:#0b1522;border:1px solid var(--border,#27354a);font-size:.82rem;line-height:1.5}.task-status[data-status=succeeded]{border-color:#327a56}.task-status[data-status=failed],.task-status[data-status=unknown]{border-color:#9b5d56}.task-status small{color:var(--text-muted,#9ba8bd)}.manual-install{border-top:1px solid var(--border,#27354a);margin-top:1.3rem;padding-top:1rem}.manual-install summary{cursor:pointer;font-weight:650}.manual-install pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#0b1320;padding:.8rem;border-radius:9px}.discovery-footer{margin-top:1.5rem;color:var(--text-muted,#9ba8bd);font-size:.82rem}.full-button{width:100%}
.ssh-rescue{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:.65rem 1rem;align-items:center;margin-top:1rem;padding:1rem;border:1px solid var(--border,#27354a);border-radius:14px;background:#111b2a}.ssh-rescue h3{font-size:1rem;margin:.1rem 0}.ssh-rescue p{color:var(--text-muted,#9ba8bd);line-height:1.5;margin:.25rem 0}.ssh-rescue pre{margin:0;white-space:pre-wrap;overflow-wrap:anywhere;background:#0b1320;padding:.7rem;border-radius:9px}.ssh-rescue .field-hint{grid-column:1/-1}.rescue-addresses{display:grid;gap:.25rem;font-size:.82rem}.rescue-addresses span{color:var(--text-muted,#9ba8bd)}.rescue-addresses code{overflow-wrap:anywhere}
@media(max-width:760px){.tailscale-page{padding-top:1.2rem}.discovery-heading{align-items:flex-start}.discovery-heading>.secondary-button{flex:none}.deploy-columns{grid-template-columns:1fr}.ssh-rescue{grid-template-columns:1fr}.ssh-rescue .field-hint{grid-column:auto}.peer-main{align-items:flex-start;flex-wrap:wrap}.peer-state{margin-left:auto}.managed-list article{align-items:flex-start}.discovery-section-heading{align-items:flex-start}.discovery-footer{align-items:flex-start;flex-direction:column}}
</style>
