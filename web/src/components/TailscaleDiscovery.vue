<script setup lang="ts">
import { t, display, locale } from "../i18n"
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
  return t('m.9dee018adddc', {"0": (shellQuote(t('m.6a82133ca245', {"0": (credentials.user || t('m.a0b366e8b611')), "1": (host)})))})
})

function peerLabel(peer: TailscalePeer): string { return peer.name || peer.dnsName || peer.identity }
function stateLabel(peer: TailscalePeer): string { return peer.online ? t('m.b9086662b1df') : t('m.be1b4f3c6c1c') }
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
    if (!peers.value.length) notice.value = t('m.8bcf5c4107b5')
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
    error.value = t('m.5b47ff2c5947', {"0": (message(reason))})
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
      error.value = t('m.2eddbfa49d23', {"0": (message(reason))})
      return
    }
    if (taskFinished.value) {
      await refreshDeploymentTasks()
      if (task.value?.status === 'succeeded') {
        const successMessage = task.value.message || t('m.37d815f92ccd')
        await discover()
        if (!error.value) notice.value = successMessage
      } else if (task.value?.status === 'unknown') {
        error.value = task.value.message || t('m.5ef75da06b52')
      } else {
        error.value = task.value?.message || t('m.b6b1127c181b')
      }
      return
    }
  }
  error.value = t('m.e2de9b4ce02d')
}

function message(reason: unknown): string {
  return reason instanceof Error && reason.message ? reason.message : t('m.a8953ce415a5')
}

function shellQuote(value: string): string { return `'${value.replaceAll("'", "'\\''")}'` }

onMounted(() => { void discover(); void refreshDeploymentTasks() })
</script>

<template>
  <main class="tailscale-page" data-testid="tailscale-discovery">
    <header class="discovery-heading">
      <div><span class="eyebrow">{{ t('label.7aaef7e44874') }}</span><h1>{{ t('m.b5af65d9acc5') }}</h1><p>{{ t('m.76702efc45e4') }}</p></div>
      <button class="secondary-button" type="button" :disabled="discovering" @click="discover">{{ discovering ? t('m.86b6d0d63062') : t('m.778b04988d4f') }}</button>
    </header>

    <div v-if="error" class="alert alert-error" role="alert">{{ display(error) }}</div>
    <div v-if="notice" class="alert alert-success" role="status">{{ display(notice) }}</div>

    <section v-if="unresolvedDeployments.length" class="discovery-card" aria-labelledby="unresolved-deployments-title" data-testid="unresolved-deployments">
      <div class="discovery-section-heading"><div><h2 id="unresolved-deployments-title">{{ t('m.756060e81582') }}</h2><p>{{ t('m.cf0831bd17a7') }}</p></div><button class="quiet-button" type="button" @click="refreshDeploymentTasks">{{ t('m.e4c9549fe2f4') }}</button></div>
      <div class="managed-list"><article v-for="deployment in unresolvedDeployments" :key="deployment.taskId">
        <strong>{{ deployment.peerName || deployment.peerIdentity }}</strong><span>{{ deployment.phase }} · {{ deployment.status }}</span><code>{{ deployment.taskId }}</code>
        <button class="secondary-button" type="button" @click="inspectDeploymentTask(deployment)">{{ deployment.status === 'unknown' ? t('m.196d981f3b71') : t('m.36550b9c5748') }}</button>
      </article></div>
    </section>

    <section class="discovery-card" aria-labelledby="candidates-title">
      <div class="discovery-section-heading"><div><h2 id="candidates-title">{{ t('m.4bd7d4a845a6') }}</h2><p>{{ t('m.8cb6ae80b062') }}</p></div><span class="count-pill">{{ candidates.length }}</span></div>
      <div v-if="discovering" class="empty-state" role="status">{{ t('m.41ac7e25e811') }}</div>
      <div v-else-if="candidates.length" class="peer-grid">
        <article v-for="peer in candidates" :key="peer.identity" class="peer-card" :data-identity="peer.identity">
          <div class="peer-main"><span class="peer-icon" aria-hidden="true">⌘</span><div><strong>{{ peerLabel(peer) }}</strong><span>{{ peer.os }} · {{ peer.ips.join(' · ') }}</span></div><span class="peer-state" :data-online="peer.online">{{ stateLabel(peer) }}</span></div>
          <small class="peer-id">{{ t('m.314c00531b6b') }}{{ peer.identity }}</small>
          <button class="secondary-button" type="button" :disabled="!peer.online" @click="choose(peer)">{{ peer.online ? t('m.633ad558df34') : t('m.badb7a4e8552') }}</button>
        </article>
      </div>
      <div v-else-if="!error && !discovering" class="empty-state">{{ t('m.91c31647219d') }}</div>
    </section>

    <section v-if="managed.length" class="discovery-card" aria-labelledby="managed-title">
      <div class="discovery-section-heading"><div><h2 id="managed-title">{{ t('m.032d0a11f835') }}</h2><p>{{ t('m.f06dc19bae6e') }}</p></div><span class="count-pill">{{ managed.length }}</span></div>
      <div class="managed-list"><article v-for="peer in managed" :key="peer.identity"><strong>{{ peerLabel(peer) }}</strong><span>{{ peer.ips.join(' · ') }}</span><code>{{ peer.nodeId }}</code><span class="peer-state" :data-online="peer.online">{{ peer.online ? t('m.af9306d8cc6d') : t('m.b4ba6377da21') }}</span></article></div>
    </section>

    <section v-if="unsupported.length" class="discovery-card muted-card" aria-labelledby="unsupported-title">
      <div class="discovery-section-heading"><div><h2 id="unsupported-title">{{ t('m.a0eefe1a80e8') }}</h2><p>{{ t('m.bf599a08f93f') }}</p></div><span class="count-pill">{{ unsupported.length }}</span></div>
      <ul class="unsupported-list"><li v-for="peer in unsupported" :key="peer.identity"><strong>{{ peerLabel(peer) }}</strong><span>{{ peer.os || t('m.17cd83b14b04') }} · {{ peer.ips.join(' · ') }}</span></li></ul>
    </section>

    <section v-if="selected" class="discovery-card deploy-card" aria-labelledby="deploy-title">
      <div class="discovery-section-heading"><div><span class="eyebrow">{{ selected.name || selected.identity }}</span><h2 id="deploy-title">{{ t('m.3421f410ce27') }}</h2><p>{{ t('m.b5833cce47f3') }}</p></div><button class="quiet-button" type="button" @click="selected = null">{{ t('m.3fd47edce45b') }}</button></div>
      <div class="deploy-columns">
        <div class="deploy-form">
          <label class="field"><span>{{ t('m.e1290ec94f8d') }}</span><input v-model="form.displayName" maxlength="80" autocomplete="off" /></label>
          <label class="field"><span>{{ t('m.d62df2018d54') }}</span><input v-model="form.fileRoot" :disabled="form.disableFileRoot" autocomplete="off" placeholder="/srv/nodedance-files" /><small class="field-hint">{{ t('m.bb7758c4aae9') }}</small></label>
          <label class="check-row"><input v-model="form.disableFileRoot" type="checkbox" /><span>{{ t('m.63805b68bc8a') }}</span></label>
          <label class="field"><span>{{ t('m.8f7fd0e28ab3') }}</span><input v-model="form.coreUrl" inputmode="url" placeholder="https://panel.example.ts.net" /><small class="field-hint">{{ t('m.6b6c846170b3') }}</small></label>
          <label class="field"><span>{{ t('m.9ca13b13c082') }}</span><input v-model="form.fallbackUrl" inputmode="url" placeholder="https://backup.example.net" /></label>
          <label class="check-row"><input v-model="form.allowFallback" type="checkbox" /><span>{{ t('m.78c672bad4da') }}</span></label>
          <p class="field-hint">{{ t('m.29319276203d') }}</p>
          <label class="field"><span>{{ t('m.4249329ab914') }}</span><input v-model="credentials.user" autocomplete="username" /></label>
          <div class="auth-mode" role="group" :aria-label="t('m.18d16b1edffd')"><button type="button" :aria-pressed="!form.usePrivateKey" @click="form.usePrivateKey = false">{{ t('m.c9d0cf7bf3ee') }}</button><button type="button" :aria-pressed="form.usePrivateKey" @click="form.usePrivateKey = true">{{ t('m.3ffe935f9d0f') }}</button></div>
          <label v-if="!form.usePrivateKey" class="field"><span>{{ t('m.6f0890a1b0b0') }}</span><input v-model="credentials.password" type="password" autocomplete="new-password" /><small class="field-hint">{{ t('m.fe4b4ce6b8aa') }}</small></label>
          <template v-else>
            <label class="field"><span>{{ t('m.2637e8166d92') }}</span><textarea v-model="credentials.privateKey" rows="6" spellcheck="false" autocomplete="off" /></label>
            <label class="field"><span>{{ t('m.d2f245e82fd3') }}</span><input v-model="credentials.passphrase" type="password" autocomplete="new-password" /></label>
          </template>
        </div>
        <div class="hostkey-panel">
          <h3>{{ t('m.00487aceb17d') }}</h3>
          <p>{{ t('m.1f9b1895427a') }}</p>
          <button class="secondary-button" type="button" :disabled="fingerprintLoading" @click="probeHostKey">{{ fingerprintLoading ? t('m.db728a839413') : t('m.c2df7b3b46e0') }}</button>
          <code v-if="fingerprint" class="fingerprint" data-testid="ssh-fingerprint">{{ fingerprint }}</code>
          <label v-if="fingerprint" class="check-row"><input v-model="form.confirmHostKey" type="checkbox" /><span>{{ t('m.053515f793f4') }}</span></label>
          <label v-if="fingerprint" class="check-row"><input v-model="form.confirmChangedHostKey" type="checkbox" /><span>{{ t('m.d643c97eee50') }}</span></label>
          <button class="primary-button full-button" type="button" :disabled="loading || !fingerprint || !form.confirmHostKey || !credentials.user || !form.displayName" @click="startDeployment">{{ loading ? t('m.96461d04f477') : t('m.3795cc8670b9') }}</button>
        </div>
      </div>

      <aside class="ssh-rescue" :aria-label="t('m.7fe4082935b3')">
        <div><h3>{{ t('m.6e9f9850c332') }}</h3><p>{{ t('m.4771d6286323') }}</p></div>
        <div class="rescue-addresses"><span>{{ t('m.25e4ddf9ec12') }}</span><code>{{ selected.ips.join(' · ') || t('m.423dec214508') }}</code></div>
        <pre v-if="selected.ips.length"><code>{{ rescueCommand }}</code></pre>
        <p class="field-hint">{{ t('m.6392f921944a') }}<code>systemctl status nodedance-agent</code>{{ t('m.5c05a073a592') }}</p>
      </aside>

    </section>

    <section v-if="task" class="discovery-card" aria-labelledby="deployment-task-title" data-testid="deployment-task-section">
      <div class="discovery-section-heading"><div><h2 id="deployment-task-title">{{ t('m.9343a9c6612f') }}</h2><p>{{ t('m.6094de4eb887') }}</p></div><button class="quiet-button" type="button" @click="task = null">{{ t('m.3fd47edce45b') }}</button></div>
      <div class="task-status" data-testid="deployment-task" :data-status="task.status" role="status"><strong>{{ task.phase }} · {{ task.status }}</strong><span>{{ display(task.message) }}</span><small v-if="task.nodeId">Node ID：{{ task.nodeId }}</small></div>
      <div v-if="task.status === 'unknown'" class="task-status task-review" data-testid="deployment-review">
        <strong>{{ t('m.278a2d42ba92') }}</strong>
        <p>{{ t('m.e972e33c6e32') }}</p>
        <label class="field"><span>{{ t('m.6e3bdd16a1ec') }}</span><select :value="resolutionOutcome" @change="changeResolutionOutcome">
          <option value="succeeded">{{ t('m.18786701de8f') }}</option><option value="failed">{{ t('m.981ea9952f00') }}</option>
        </select></label>
        <label v-if="resolutionOutcome === 'succeeded'" class="field"><span>{{ t('m.e0c379ab2eb0') }}</span><select :value="resolutionObservedState" @change="changeResolutionObservedState">
          <option value="connected">{{ t('m.83af47139246') }}</option><option value="unavailable">{{ t('m.d3e431f9d10b') }}</option><option value="absent">{{ t('m.8c4ecd0b9c16') }}</option>
        </select></label>
        <label class="check-row"><input v-model="resolutionConfirmed" type="checkbox" /><span>{{ t('m.64aa1e37e12e') }}</span></label>
        <button class="secondary-button" type="button" :disabled="!resolutionConfirmed" @click="resolveUnknownDeployment">{{ t('m.b8dead662e24') }}</button>
      </div>
    </section>

    <AgentEnrollment compact />

    <footer class="discovery-footer"><span>{{ t('m.b4e9ecd50535') }}</span><button class="quiet-button" type="button" @click="emit('back')">{{ t('m.e0e6dc51f626') }}</button></footer>
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
