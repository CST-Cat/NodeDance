<script setup lang="ts">
import { t, display } from "../i18n"
import { computed, onMounted, ref, watch } from 'vue'
import { api, operationRequest } from '../api'
import { beijingTime } from '../time'
import { resolveOperation, type BodySchema } from '../operations'
import NodeAssetFields from './NodeAssetFields.vue'
const asset = ref<Record<string, unknown>>({})
const assetSchema = ref<BodySchema>()
const assetReady = ref(false)

const props = withDefaults(defineProps<{ compact?: boolean; nodeId?: string }>(), { compact: false })
const emit = defineEmits<{ back: [] }>()

const displayName = ref('')
const expiresAt = ref('')
const creating = ref(false)
const error = ref('')
const notice = ref('')
const agentServer = ref(window.location.origin)
const development = window.location.protocol === 'http:'

const agentOrigin = computed(() => {
  try {
    const parsed = new URL(agentServer.value.trim())
    return (parsed.protocol === 'https:' || parsed.protocol === 'http:') &&
      !parsed.username && !parsed.password && !parsed.search && !parsed.hash &&
      (parsed.pathname === '/' || parsed.pathname === '')
      ? parsed.origin
      : ''
  } catch {
    return ''
  }
})
const agentOriginIsSupported = computed(() => agentOrigin.value !== '')
const agentOriginIsLoopback = computed(() => {
  if (!agentOrigin.value) return false
  return isLiteralLoopback(new URL(agentOrigin.value).hostname)
})
const agentOriginUsesAllowedTransport = computed(() =>
  agentOrigin.value.startsWith('https://') ||
  (development && agentOrigin.value.startsWith('http://') && agentOriginIsLoopback.value),
)

const installCommand = ref('')
const dockerInstallCommand = ref('')
const deploymentMethod = ref<'native' | 'docker'>('native')
onMounted(async () => {
 try {
  const {operation} = await resolveOperation('agents.enrollment')
  const value = operation.bodySchema?.properties?.deploymentMethod?.default
  if(value === 'native' || value === 'docker') deploymentMethod.value = value
  assetSchema.value = operation.bodySchema?.properties?.asset
  asset.value = props.nodeId
   ? await operationRequest<Record<string, unknown>>('nodes.asset', {params:{nodeId:props.nodeId}})
   : Object.fromEntries(Object.entries(assetSchema.value?.properties ?? {}).map(([key, field]) => [key, field.default ?? (field.type === 'integer' || field.type === 'number' ? 0 : null)]))
  if(!props.nodeId) asset.value.billingCycleUnit = 'month'
  assetReady.value = true
 } catch(reason) { error.value = reason instanceof Error ? reason.message : String(reason) }
})
const selectedInstallCommand = computed(() => deploymentMethod.value === 'docker' ? dockerInstallCommand.value : installCommand.value)

let requestEpoch = 0
watch([displayName, agentServer, asset], () => {
  requestEpoch++
  installCommand.value = ''
  dockerInstallCommand.value = ''
  expiresAt.value = ''
  notice.value = ''
}, {deep:true})

async function generateEnrollment() {
  error.value = ''
  notice.value = ''
  installCommand.value = ''
  dockerInstallCommand.value = ''
  expiresAt.value = ''
  const name = displayName.value.trim()
  if (!agentOriginIsSupported.value || !agentOriginUsesAllowedTransport.value) {
    error.value = t('m.70226817387f')
    return
  }
  if (!props.nodeId && (!name || /[\r\n\0]/.test(name))) {
    error.value = t('m.bd8975b069f6')
    return
  }
  const current = requestEpoch
  creating.value = true
  try {
    const result = props.nodeId
      ? await api.renewAgentEnrollment(props.nodeId, agentOrigin.value, deploymentMethod.value, asset.value)
      : await api.createAgentEnrollment(name, agentOrigin.value, deploymentMethod.value, asset.value)
    if (current !== requestEpoch) return
    installCommand.value = result.nativeInstallCommand
    dockerInstallCommand.value = result.dockerInstallCommand
    expiresAt.value = result.expiresAt
    notice.value = props.nodeId ? t('node.enrollmentRenewed') : t('m.d04e8d433a67', {"0": (result.displayName)})
  } catch (reason) {
    if (current === requestEpoch) error.value = reason instanceof Error && reason.message ? reason.message : t('m.9ecada108145')
  } finally {
    creating.value = false
  }
}

async function copyText(value: string, label: string) {
  error.value = ''
  notice.value = ''
  try {
    await navigator.clipboard.writeText(value)
    notice.value = t('m.e3852e382b5b', {"0": (label)})
  } catch {
    error.value = t('m.e00feb7956c7')
  }
}

function isLiteralLoopback(hostname: string): boolean {
  const host = hostname.replace(/^\[|\]$/g, '')
  if (host === '::1') return true
  const octets = host.split('.')
  return octets.length === 4 && octets.every((part) => /^\d{1,3}$/.test(part) && Number(part) <= 255) && Number(octets[0]) === 127
}
</script>

<template>
  <main :class="compact ? 'agent-enrollment-compact-page' : 'agent-enrollment-page workspace-page'" data-testid="agent-enrollment">
    <header v-if="!compact" class="agent-enrollment-heading">
      <div><span class="eyebrow">{{ t('label.f635eb3c2462') }}</span><h1>{{ t('m.a29c23de8cc4') }}<span class="title-period">.</span></h1><p>{{ t('m.779760698929') }}</p></div>
      <button class="quiet-button" type="button" @click="emit('back')">{{ t('m.e0e6dc51f626') }}</button>
    </header>
    <section :class="['agent-enrollment-card', { 'agent-enrollment-card-compact': compact }]">
      <div class="agent-enrollment-form">
        <div class="agent-enrollment-section-heading">
          <div><h2>{{ compact ? t('m.11e4978edcfe') : t('m.8e80901344c8') }}</h2><p>{{ t('m.ef7b7a7a77a6') }}</p></div>
        </div>
        <label class="field"><span>{{ t('m.b66f55e3dc3d') }}</span><input v-model="agentServer" autocomplete="url" placeholder="https://core.example:8180" :aria-label="t('m.b66f55e3dc3d')" /></label>
        <fieldset class="deployment-choices" data-testid="agent-deployment-method"><legend>{{ t('agent.deploymentMethod') }}</legend><div><button type="button" :aria-pressed="deploymentMethod === 'native'" @click="deploymentMethod = 'native'">{{ t('agent.nativeDeployment') }}</button><button type="button" :aria-pressed="deploymentMethod === 'docker'" @click="deploymentMethod = 'docker'">{{ t('agent.dockerDeployment') }}</button></div></fieldset>
        <p v-if="deploymentMethod === 'docker'" class="field-hint">{{ t('agent.dockerRequirements') }}</p>
        <p v-if="nodeId" class="field-hint">{{ t('node.enrollmentRenewHint') }}</p>
        <label v-else class="field"><span>{{ t('m.a09f47188571') }}</span><input v-model="displayName" maxlength="80" autocomplete="off" :placeholder="t('m.1b086a50d7b3')" /></label>
        <NodeAssetFields v-if="assetReady" :values="asset" :schema="assetSchema" :disabled="creating" />
        <p v-if="!agentOriginIsSupported || !agentOriginUsesAllowedTransport" class="alert alert-error" role="alert">{{ t('m.1d5964dbb8d8') }}</p>
        <p v-if="error" class="alert alert-error" role="alert">{{ display(error) }}</p>
        <p v-if="notice" class="alert alert-success" role="status">{{ display(notice) }}</p>
        <button class="primary-button" type="button" :disabled="!assetReady || creating || (!nodeId && !displayName.trim()) || !agentOriginIsSupported || !agentOriginUsesAllowedTransport" @click="generateEnrollment">{{ creating ? t('m.a8b8730c9611') : nodeId ? t('node.generateInstall') : t('m.92840286f8cd') }}</button>
        <div v-if="selectedInstallCommand" class="agent-enrollment-result">
          <p class="field-hint">{{ t('m.80ee590ed729') }}<time :datetime="expiresAt" data-testid="agent-enrollment-expiry">{{ beijingTime(expiresAt) }}</time> · {{ t('time.beijing') }}</p>
          <label class="field"><span>{{ t('m.304f2c052c67') }}</span><textarea readonly rows="3" :value="selectedInstallCommand" data-testid="agent-install-command" /></label>
          <button class="secondary-button" type="button" @click="copyText(selectedInstallCommand, t('m.bdf7d59b3dc2'))">{{ t('m.fe95745d1ef4') }}</button>
          <p class="field-hint">{{ t('m.b5debb33710e') }}</p>
        </div>
        <p class="agent-release-note">{{ t('m.f48ceda113e7') }}</p>
      </div>
    </section>
  </main>
</template>

<style scoped>
.deployment-choices{margin:0;padding:0;border:0;min-width:0}.deployment-choices legend{padding:0;margin-bottom:8px;color:var(--muted-strong);font-size:13px}.deployment-choices>div{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px}.deployment-choices button{min-height:44px;border:1px solid var(--line);border-radius:var(--radius-control);padding:10px;color:var(--muted-strong);background:var(--surface);font:inherit;cursor:pointer}.deployment-choices button[aria-pressed=true]{border-color:var(--accent);color:var(--accent);background:var(--accent-soft)}.deployment-choices button:hover{border-color:var(--accent-line)}.deployment-choices button:active{background:var(--line)}.deployment-choices button:focus-visible{outline:2px solid var(--accent);outline-offset:3px}

.agent-enrollment-page{width:min(980px,100%);margin:0 auto;padding:clamp(24px,5vw,52px);color:var(--text)}.agent-enrollment-compact-page{color:var(--text)}.agent-enrollment-heading,.agent-enrollment-section-heading{display:flex;align-items:center;justify-content:space-between;gap:16px}.agent-enrollment-heading{margin-bottom:20px}.agent-enrollment-heading h1{margin:5px 0;font-size:clamp(26px,4vw,38px)}.agent-enrollment-heading p,.agent-enrollment-section-heading p{color:var(--muted-strong);line-height:1.55}.agent-enrollment-card {border-radius:0;background:transparent;box-shadow:none;padding:20px 0;border-top:1px solid var(--line);}.agent-enrollment-form{display:grid;gap:13px}.agent-enrollment-section-heading h2{margin:0}.agent-enrollment-result{display:grid;gap:12px;border-top:1px solid var(--line);padding-top:16px}.agent-enrollment-form .field{display:grid;gap:6px}.agent-enrollment-form input,.agent-enrollment-form textarea,.agent-enrollment-form select{box-sizing:border-box;width:100%;border:1px solid var(--line);border-radius:var(--radius-control);padding:10px;background:var(--surface-soft);color:inherit;font:inherit}.agent-enrollment-form textarea{font-family:ui-monospace,SFMono-Regular,monospace;font-size:.8rem;overflow-wrap:anywhere;resize:vertical}.agent-enrollment-form .field-hint,.agent-release-note{margin:0;color:var(--muted-strong);font-size:.82rem;line-height:1.55}.agent-release-note{border-top:1px solid var(--line);padding-top:12px}.agent-enrollment-card-compact{margin-top:18px}
@media(max-width:640px){.agent-enrollment-heading,.agent-enrollment-section-heading{align-items:flex-start;flex-direction:column}.agent-enrollment-heading button{align-self:flex-start}}
</style>
