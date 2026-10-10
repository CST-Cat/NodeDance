<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '../api'

const props = withDefaults(defineProps<{ compact?: boolean }>(), { compact: false })
const emit = defineEmits<{ back: [] }>()

const displayName = ref('')
const token = ref('')
const expiresAt = ref('')
const creating = ref(false)
const error = ref('')
const notice = ref('')
const coreOrigin = window.location.origin
const development = window.location.protocol === 'http:'

const coreOriginIsSupported = computed(() => {
  try {
    const parsed = new URL(coreOrigin)
    return (parsed.protocol === 'https:' || (parsed.protocol === 'http:' && isLiteralLoopback(parsed.hostname))) &&
      !parsed.username && !parsed.password && parsed.origin === coreOrigin
  } catch {
    return false
  }
})

const installCommand = computed(() => {
  const args = [
    '--server', shellQuote(coreOrigin),
    '--display-name', shellQuote(displayName.value.trim()),
  ]
  if (development) args.push('--dev')
  const apiURL = 'https://api.github.com/repos/CST-Cat/NodeDance/releases?per_page=100'
  const releases = `releases=$(curl --fail --silent --show-error --location --proto =https --proto-redir =https --tlsv1.2 "${apiURL}") || { echo 'Could not list Agent releases.' >&2; exit 1; }; tag=$(printf '%s\\n' "$releases" | awk -F'"' '$2 == "tag_name" && $4 ~ /^agent-v[0-9]+\\.[0-9]+\\.[0-9]+$/ { print $4; exit }'); [[ "$tag" =~ ^agent-v[0-9]+\\.[0-9]+\\.[0-9]+$ ]] || { echo 'No published agent-vMAJOR.MINOR.PATCH release was found.' >&2; exit 1; }`
  const assetURL = 'https://github.com/CST-Cat/NodeDance/releases/download/$tag/install-agent.sh'
  const pipeline = `${releases}; curl --fail --silent --show-error --location --proto =https --proto-redir =https --tlsv1.2 "${assetURL}" | sudo bash -s -- --release-tag "$tag" ${args.join(' ')}`
  return `bash -o pipefail -c ${shellQuote(pipeline)}`
})

watch(displayName, () => {
  token.value = ''
  expiresAt.value = ''
  notice.value = ''
})

async function generateEnrollment() {
  error.value = ''
  notice.value = ''
  token.value = ''
  expiresAt.value = ''
  const name = displayName.value.trim()
  if (!coreOriginIsSupported.value) {
    error.value = '当前控制台地址必须使用 HTTPS；开发环境仅允许 literal loopback HTTP。'
    return
  }
  if (!name || /[\r\n\0]/.test(name)) {
    error.value = '请输入单行节点显示名称。'
    return
  }
  creating.value = true
  try {
    const result = await api.createAgentEnrollment(name)
    token.value = result.token
    expiresAt.value = result.expiresAt
    notice.value = `已为“${result.displayName}”生成一次性 Token；节点会绑定到当前 Core。`
  } catch (reason) {
    error.value = reason instanceof Error && reason.message ? reason.message : '无法创建 Agent 注册凭据。'
  } finally {
    creating.value = false
  }
}

async function copyText(value: string, label: string) {
  error.value = ''
  notice.value = ''
  try {
    await navigator.clipboard.writeText(value)
    notice.value = `${label}已复制。`
  } catch {
    error.value = '浏览器未允许剪贴板访问，请手动选择并复制内容。'
  }
}

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", "'\\''")}'`
}

function isLiteralLoopback(hostname: string): boolean {
  const host = hostname.replace(/^\[|\]$/g, '')
  if (host === '::1') return true
  const octets = host.split('.')
  return octets.length === 4 && octets.every((part) => /^\d{1,3}$/.test(part) && Number(part) <= 255) && Number(octets[0]) === 127
}
</script>

<template>
  <main :class="compact ? 'agent-enrollment-compact-page' : 'agent-enrollment-page'" data-testid="agent-enrollment">
    <header v-if="!compact" class="agent-enrollment-heading">
      <div><span class="eyebrow">AGENT ONBOARDING</span><h1>添加 Agent<span class="title-period">.</span></h1><p>生成一次性注册凭据，并复制绑定到当前 Core 的 Linux 安装命令。</p></div>
      <button class="quiet-button" type="button" @click="emit('back')">返回控制台</button>
    </header>
    <section :class="['agent-enrollment-card', { 'agent-enrollment-card-compact': compact }]">
      <div class="agent-enrollment-form">
        <div class="agent-enrollment-section-heading">
          <div><h2>{{ compact ? '手动安装 Agent' : '生成安装命令' }}</h2><p>此入口可独立使用，不需要启用 Tailscale。Agent 以 root 运行并主动连接当前 Core。</p></div>
        </div>
        <label class="field"><span>当前 Core 地址</span><input :value="coreOrigin" readonly aria-label="当前 Core 地址" /></label>
        <label class="field"><span>节点显示名称</span><input v-model="displayName" maxlength="80" autocomplete="off" placeholder="例如：production-01" /></label>
        <p v-if="!coreOriginIsSupported" class="alert alert-error" role="alert">当前页面地址不适用于 Agent：需要 HTTPS；开发时只接受 literal loopback HTTP。</p>
        <p v-if="error" class="alert alert-error" role="alert">{{ error }}</p>
        <p v-if="notice" class="alert alert-success" role="status">{{ notice }}</p>
        <button class="primary-button" type="button" :disabled="creating || !displayName.trim() || !coreOriginIsSupported" @click="generateEnrollment">{{ creating ? '正在生成…' : '生成一次性注册命令' }}</button>
        <div v-if="token" class="agent-enrollment-result">
          <p class="field-hint">Token 仅可使用一次，过期时间：{{ expiresAt }}。Token 不包含在命令、参数或环境变量中。</p>
          <label class="field"><span>目标 Linux 主机执行的一条命令</span><textarea readonly rows="3" :value="installCommand" data-testid="agent-install-command" /></label>
          <button class="secondary-button" type="button" @click="copyText(installCommand, '安装命令')">复制安装命令</button>
          <label class="field"><span>一次性 Token</span><textarea readonly rows="2" :value="token" data-testid="agent-enrollment-token" /></label>
          <button class="quiet-button" type="button" @click="copyText(token, '一次性 Token')">复制 Token</button>
          <p class="field-hint">在目标机运行命令后，脚本会在 /dev/tty 隐藏提示输入此 Token；它不会进入命令历史或进程参数。脚本校验发布 SHA256 后会自动注册、安装 systemd 并启动 Agent。</p>
        </div>
        <p class="agent-release-note">安装命令只解析并使用最新已发布的 agent-vMAJOR.MINOR.PATCH Agent tag；Core 发布不会影响 Agent 下载。若没有 Agent release，命令会在调用 sudo 安装脚本前明确退出。</p>
      </div>
    </section>
  </main>
</template>

<style scoped>
.agent-enrollment-page{width:min(980px,100%);margin:0 auto;padding:clamp(24px,5vw,52px);color:#eaf0fa}.agent-enrollment-compact-page{color:#eaf0fa}.agent-enrollment-heading,.agent-enrollment-section-heading{display:flex;align-items:center;justify-content:space-between;gap:16px}.agent-enrollment-heading{margin-bottom:20px}.agent-enrollment-heading h1{margin:5px 0;font-size:clamp(26px,4vw,38px)}.agent-enrollment-heading p,.agent-enrollment-section-heading p{color:#9aabc1;line-height:1.55}.agent-enrollment-card{border:1px solid rgba(171,196,232,.16);border-radius:16px;background:#151e2d;padding:clamp(16px,3vw,26px)}.agent-enrollment-form{display:grid;gap:13px}.agent-enrollment-section-heading h2{margin:0}.agent-enrollment-result{display:grid;gap:12px;border-top:1px solid rgba(171,196,232,.16);padding-top:16px}.agent-enrollment-form .field{display:grid;gap:6px}.agent-enrollment-form input,.agent-enrollment-form textarea{box-sizing:border-box;width:100%;border:1px solid rgba(171,196,232,.18);border-radius:8px;padding:10px;background:#0e1725;color:inherit;font:inherit}.agent-enrollment-form textarea{font-family:ui-monospace,SFMono-Regular,monospace;font-size:.8rem;overflow-wrap:anywhere;resize:vertical}.agent-enrollment-form .field-hint,.agent-release-note{margin:0;color:#9aabc1;font-size:.82rem;line-height:1.55}.agent-release-note{border-top:1px solid rgba(171,196,232,.16);padding-top:12px}.agent-enrollment-card-compact{margin-top:18px}
@media(max-width:640px){.agent-enrollment-heading,.agent-enrollment-section-heading{align-items:flex-start;flex-direction:column}.agent-enrollment-heading button{align-self:flex-start}}
</style>
