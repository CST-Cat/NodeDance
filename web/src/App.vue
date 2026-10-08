<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ApiError, api, type Appearance, type Session, type User } from './api'
import NodesDashboard from './components/NodesDashboard.vue'

type Screen = 'loading' | 'unavailable' | 'setup' | 'login' | 'settings' | 'nodes'
type ImageKind = 'avatar' | 'background'

const screen = ref<Screen>('loading')
const busy = ref(false)
const busyLabel = ref('')
const error = ref('')
const notice = ref('')
const setupHint = ref('')
const initialized = ref(false)
const currentUser = ref<User | null>(null)
const sessions = ref<Session[]>([])
const uploading = ref<ImageKind | ''>('')
const preferredTheme = ref<'dark' | 'light'>('dark')
const avatarRevision = ref(0)
const backgroundRevision = ref(0)

const setupForm = reactive({ credential: '', password: '', displayName: '' })
const loginPassword = ref('')
const passwordForm = reactive({ currentPassword: '', newPassword: '', confirmPassword: '' })
const appearance = reactive<Appearance>({
  displayName: '',
  theme: 'dark',
  backgroundColor: '#101827',
  avatarUrl: '',
  backgroundUrl: '',
})

const safeBackgroundColor = computed(() => /^#[\da-f]{6}$/i.test(appearance.backgroundColor)
  ? appearance.backgroundColor
  : '#101827')
const shellStyle = computed(() => ({ '--page-color': safeBackgroundColor.value }))
const effectiveTheme = computed(() => appearance.theme === 'system' ? preferredTheme.value : appearance.theme)

function imageUrl(value: string, kind: ImageKind): string {
  if (!value) return ''
  try {
    const url = new URL(value, window.location.origin)
    const expectedPath = `/api/v1/public/appearance/${kind}`
    if (url.origin !== window.location.origin || url.pathname !== expectedPath) return ''
    return url.pathname
  } catch {
    return ''
  }
}

function revisionedImageURL(value: string, kind: ImageKind, revision: number): string {
  const path = imageUrl(value, kind)
  return path && revision > 0 ? `${path}?rev=${revision}` : path
}

const avatarSrc = computed(() => revisionedImageURL(appearance.avatarUrl, 'avatar', avatarRevision.value))
const backgroundSrc = computed(() => revisionedImageURL(appearance.backgroundUrl, 'background', backgroundRevision.value))
const visibleName = computed(() => appearance.displayName || currentUser.value?.displayName || '管理员')

function clearMessages() {
  error.value = ''
  notice.value = ''
}

function errorText(reason: unknown): string {
  if (reason instanceof Error && reason.message) return reason.message
  return '操作失败，请稍后重试。'
}

async function loadAppearance() {
  const result = await api.appearance()
  Object.assign(appearance, result)
  if (!/^#[\da-f]{6}$/i.test(appearance.backgroundColor)) {
    appearance.backgroundColor = '#101827'
  }
  if (appearance.theme !== 'light' && appearance.theme !== 'dark' && appearance.theme !== 'system') appearance.theme = 'dark'
}

async function loadSessions() {
  const result = await api.sessions()
  sessions.value = result.sessions
}

async function enterSettings() {
  const result = await api.me()
  currentUser.value = result.user
  screen.value = 'settings'
  const results = await Promise.allSettled([loadAppearance(), loadSessions()])
  const rejected = results.find((item) => item.status === 'rejected')
  if (rejected?.status === 'rejected') {
    notice.value = '部分设置暂时无法加载，请刷新后重试。'
  }
}

async function initializeScreen() {
  screen.value = 'loading'
  clearMessages()
  try {
    const state = await api.setupStatus()
    initialized.value = state.initialized
    setupHint.value = state.setupHint ?? ''
    try {
      await loadAppearance()
    } catch {
      // A newly initialized or older Core may not have appearance data yet.
    }

    if (!state.initialized) {
      screen.value = 'setup'
      return
    }

    try {
      await enterSettings()
    } catch (reason) {
      if (reason instanceof ApiError && reason.status === 401) {
        screen.value = 'login'
        return
      }
      throw reason
    }
  } catch (reason) {
    error.value = errorText(reason)
    screen.value = 'unavailable'
  }
}

async function submitSetup() {
  clearMessages()
  busy.value = true
  busyLabel.value = '正在初始化…'
  try {
    await api.setup({
      credential: setupForm.credential,
      password: setupForm.password,
      displayName: setupForm.displayName,
    })
    initialized.value = true
    try {
      await enterSettings()
    } catch (reason) {
      if (!(reason instanceof ApiError) || reason.status !== 401) throw reason
      await api.login(setupForm.password)
      await enterSettings()
    }
    setupForm.credential = ''
    setupForm.password = ''
    notice.value = 'NodeDance 已完成初始化。'
  } catch (reason) {
    error.value = errorText(reason)
  } finally {
    busy.value = false
    busyLabel.value = ''
  }
}

async function submitLogin() {
  clearMessages()
  busy.value = true
  busyLabel.value = '正在登录…'
  try {
    await api.login(loginPassword.value)
    await enterSettings()
    loginPassword.value = ''
  } catch (reason) {
    error.value = errorText(reason)
  } finally {
    busy.value = false
    busyLabel.value = ''
  }
}

async function saveProfile() {
  clearMessages()
  busy.value = true
  busyLabel.value = '正在保存…'
  try {
    await api.saveAppearance({
      displayName: appearance.displayName,
      theme: appearance.theme,
      backgroundColor: appearance.backgroundColor,
    })
    currentUser.value = { displayName: appearance.displayName }
    notice.value = '外观设置已保存。'
  } catch (reason) {
    error.value = errorText(reason)
  } finally {
    busy.value = false
    busyLabel.value = ''
  }
}

async function handleImageChange(kind: ImageKind, event: Event) {
  clearMessages()
  const input = event.target as HTMLInputElement
  const image = input.files?.[0]
  if (!image) return
  input.value = ''

  const acceptedTypes = new Set(['image/png', 'image/jpeg', 'image/gif'])
  if (!acceptedTypes.has(image.type.toLowerCase()) || /\.svgz?$/i.test(image.name)) {
    error.value = '请选择 PNG、JPEG 或 GIF 图片。SVG 图片不受支持。'
    return
  }

  uploading.value = kind
  try {
    await api.uploadImage(kind, image)
    if (kind === 'avatar') avatarRevision.value += 1
    else backgroundRevision.value += 1
    await loadAppearance()
    notice.value = kind === 'avatar' ? '头像已更新。' : '背景图片已更新。'
  } catch (reason) {
    error.value = errorText(reason)
  } finally {
    uploading.value = ''
  }
}

async function revokeSession(session: Session) {
  if (session.current) return
  clearMessages()
  busy.value = true
  try {
    await api.revokeSession(session.id)
    sessions.value = sessions.value.filter((item) => item.id !== session.id)
    notice.value = '已撤销该会话。'
  } catch (reason) {
    error.value = errorText(reason)
  } finally {
    busy.value = false
  }
}

async function changePassword() {
  clearMessages()
  if (passwordForm.newPassword !== passwordForm.confirmPassword) {
    error.value = '两次输入的新密码不一致。'
    return
  }

  busy.value = true
  busyLabel.value = '正在更新密码…'
  try {
    await api.changePassword(passwordForm.currentPassword, passwordForm.newPassword)
    currentUser.value = null
    sessions.value = []
    loginPassword.value = ''
    setupForm.password = ''
    passwordForm.currentPassword = ''
    passwordForm.newPassword = ''
    passwordForm.confirmPassword = ''
    screen.value = 'login'
    notice.value = '密码已更新，请使用新密码重新登录。'
  } catch (reason) {
    error.value = errorText(reason)
  } finally {
    busy.value = false
    busyLabel.value = ''
  }
}

async function logout() {
  clearMessages()
  busy.value = true
  try {
    await api.logout()
    currentUser.value = null
    sessions.value = []
    screen.value = 'login'
    notice.value = '已安全退出。'
  } catch (reason) {
    error.value = errorText(reason)
  } finally {
    busy.value = false
  }
}

function formatTime(value: string): string {
  const time = new Date(value)
  return Number.isNaN(time.getTime()) ? '—' : time.toLocaleString()
}

onMounted(() => {
  const colorScheme = window.matchMedia('(prefers-color-scheme: light)')
  preferredTheme.value = colorScheme.matches ? 'light' : 'dark'
  colorScheme.addEventListener('change', (event) => {
    preferredTheme.value = event.matches ? 'light' : 'dark'
  })
  void initializeScreen()
})
</script>

<template>
  <main class="app-shell" :data-theme="effectiveTheme" :data-screen="screen" :style="shellStyle">
    <div class="ambient ambient-one" aria-hidden="true"></div>
    <div class="ambient ambient-two" aria-hidden="true"></div>
    <div v-if="screen === 'login' && backgroundSrc" class="login-background" aria-hidden="true">
      <img :key="backgroundRevision" :src="backgroundSrc" alt="" />
      <div class="login-background-shade"></div>
    </div>

    <header class="topbar">
      <a class="wordmark" href="/" aria-label="NodeDance 首页">
        <span class="brand-mark" aria-hidden="true"><i></i><i></i><i></i></span>
        <span>NodeDance</span>
      </a>
      <div class="topbar-right">
        <span class="system-label"><span class="status-dot"></span> 本地管理</span>
        <button v-if="screen === 'settings'" class="quiet-button" type="button" @click="screen = 'nodes'">节点监控</button>
        <button v-if="screen === 'nodes'" class="quiet-button" type="button" @click="screen = 'settings'">账户设置</button>
        <button v-if="screen === 'settings' || screen === 'nodes'" class="quiet-button" type="button" :disabled="busy" @click="logout">
          退出登录
        </button>
      </div>
    </header>

    <div v-if="screen === 'loading'" class="center-stage" role="status" aria-live="polite">
      <div class="loader"></div>
      <p>正在连接 NodeDance Core…</p>
    </div>

    <section v-else-if="screen === 'unavailable'" class="center-stage">
      <div class="standalone-card">
        <span class="eyebrow">CORE CONNECTION</span>
        <h1>暂时无法连接</h1>
        <p>无法读取服务状态。请确认 NodeDance Core 正在运行，然后重试。</p>
        <div v-if="error" class="alert alert-error" role="alert">{{ error }}</div>
        <button class="primary-button" type="button" @click="initializeScreen">重新连接 <span aria-hidden="true">↗</span></button>
      </div>
    </section>

    <section v-else-if="screen === 'setup'" class="auth-layout">
      <div class="auth-copy">
        <span class="eyebrow">YOUR PRIVATE CONTROL ROOM</span>
        <h1>一处掌控，<br /><em>从容运维。</em></h1>
        <p>设置管理员账户，开始管理您的服务器与容器。初始化凭据只使用一次。</p>
        <div class="feature-row"><span class="feature-symbol">✳</span><span>由您掌控的私有管理空间</span></div>
      </div>

      <section class="form-card" aria-labelledby="form-title">
        <div class="card-heading">
          <span class="eyebrow">FIRST-TIME SETUP</span>
          <span class="step-indicator"><span class="step-current">01</span><span class="step-line"></span>02</span>
        </div>
        <h2 id="form-title">创建管理员账户</h2>
        <p class="card-intro">使用本机初始化凭据设置密码与展示名称。</p>
        <div v-if="error" class="alert alert-error" role="alert">{{ error }}</div>

        <form class="form-stack" @submit.prevent="submitSetup">
          <label class="field">
            <span>初始化凭据</span>
            <input v-model="setupForm.credential" name="credential" type="password" autocomplete="one-time-code" required placeholder="粘贴本机凭据" />
            <small class="field-hint">{{ setupHint || '请在运行 Core 的本机终端获取一次性初始化凭据。' }}</small>
          </label>
          <label class="field">
            <span>展示名称</span>
            <input v-model="setupForm.displayName" name="displayName" autocomplete="nickname" required maxlength="80" placeholder="例如：Alex" />
          </label>
          <label class="field">
            <span>管理员密码</span>
            <input v-model="setupForm.password" name="password" type="password" autocomplete="new-password" required placeholder="至少 12 个字符" />
            <small class="field-hint">至少 12 个字符；过长密码会由服务器拒绝。</small>
          </label>
          <button class="primary-button full-button" type="submit" :disabled="busy">
            {{ busy ? busyLabel : '完成初始化' }} <span aria-hidden="true">↗</span>
          </button>
        </form>
        <p class="privacy-note"><span aria-hidden="true">◈</span> 凭据仅用于首次初始化，不会上传到其他服务。</p>
      </section>
    </section>

    <section v-else-if="screen === 'login'" class="auth-layout login-layout">
      <div class="auth-copy login-copy">
        <span class="eyebrow">NODEDANCE / PRIVATE</span>
        <h1>欢迎回来<span class="title-period">.</span></h1>
        <p>输入管理员密码，继续管理您的基础设施。</p>
        <div class="login-identity">
          <div class="preview-avatar">
            <img v-if="avatarSrc" :key="avatarRevision" :src="avatarSrc" alt="" />
            <span v-else aria-hidden="true">{{ (appearance.displayName || 'N').slice(0, 1) }}</span>
          </div>
          <div><span class="preview-label">WORKSPACE OF</span><strong>{{ appearance.displayName || 'NodeDance' }}</strong></div>
        </div>
      </div>

      <section class="form-card login-card" aria-labelledby="login-title">
        <span class="eyebrow">SECURE SIGN IN</span>
        <h2 id="login-title">管理员登录</h2>
        <p class="card-intro">使用您的管理员密码继续。</p>
        <div v-if="notice" class="alert alert-success" role="status">{{ notice }}</div>
        <div v-if="error" class="alert alert-error" role="alert">{{ error }}</div>
        <form class="form-stack" @submit.prevent="submitLogin">
          <label class="field">
            <span>管理员密码</span>
            <input v-model="loginPassword" name="password" type="password" autocomplete="current-password" required autofocus placeholder="输入密码" />
          </label>
          <button class="primary-button full-button" type="submit" :disabled="busy">
            {{ busy ? busyLabel : '登录控制台' }} <span aria-hidden="true">↗</span>
          </button>
        </form>
        <p class="privacy-note"><span aria-hidden="true">◈</span> 此连接使用安全会话保护。</p>
      </section>
    </section>

    <NodesDashboard v-else-if="screen === 'nodes'" />

    <section v-else class="settings-layout">
      <aside class="settings-sidebar">
        <span class="eyebrow">WORKSPACE</span>
        <h1>设置<span class="title-period">.</span></h1>
        <p>管理您的个人资料、安全与界面。</p>
        <div class="sidebar-profile">
          <div class="profile-avatar">
            <img v-if="avatarSrc" :key="avatarRevision" :src="avatarSrc" alt="" />
            <span v-else aria-hidden="true">{{ visibleName.slice(0, 1) }}</span>
          </div>
          <div class="profile-summary"><strong>{{ visibleName }}</strong><span>管理员</span></div>
          <span class="profile-dot" aria-hidden="true"></span>
        </div>
        <div class="side-note"><span class="side-note-icon">✳</span><span>所有更改会安全地<br />保存在本机 Core。</span></div>
      </aside>

      <div class="settings-main">
        <div class="settings-heading">
          <div><span class="eyebrow">ACCOUNT & PREFERENCES</span><h2>账户与外观</h2></div>
          <span class="secure-badge"><span aria-hidden="true">◈</span> 安全连接</span>
        </div>
        <div v-if="notice" class="alert alert-success page-alert" role="status">{{ notice }}</div>
        <div v-if="error" class="alert alert-error page-alert" role="alert">{{ error }}</div>

        <section class="settings-card appearance-card" aria-labelledby="appearance-heading">
          <div class="section-heading">
            <div class="section-icon appearance-icon" aria-hidden="true">◉</div>
            <div><h3 id="appearance-heading">个人资料与外观</h3><p>调整登录页和工作区的显示方式。</p></div>
          </div>

          <form class="profile-form" @submit.prevent="saveProfile">
            <div class="profile-edit-row">
              <div class="profile-avatar large-avatar">
                <img v-if="avatarSrc" :key="avatarRevision" :src="avatarSrc" alt="" />
                <span v-else aria-hidden="true">{{ visibleName.slice(0, 1) }}</span>
              </div>
              <div class="upload-copy"><strong>头像</strong><span>PNG、JPEG 或 GIF，SVG 不受支持。</span></div>
              <label class="secondary-button upload-button">
                {{ uploading === 'avatar' ? '上传中…' : '更换头像' }}
                <input aria-label="上传头像" type="file" accept="image/png,image/jpeg,image/gif" :disabled="!!uploading" @change="handleImageChange('avatar', $event)" />
              </label>
            </div>

            <label class="field profile-name-field">
              <span>展示名称</span>
              <input v-model="appearance.displayName" name="profileDisplayName" autocomplete="nickname" required maxlength="80" />
              <small class="field-hint">此名称会显示在登录页与管理界面。</small>
            </label>

            <div class="field">
              <span>界面主题</span>
              <div class="theme-options" role="group" aria-label="界面主题">
                <button class="theme-option" :class="{ selected: appearance.theme === 'dark' }" type="button" :aria-pressed="appearance.theme === 'dark'" @click="appearance.theme = 'dark'">
                  <span class="theme-swatch dark-swatch"><i></i><i></i><i></i></span><span>深色</span>
                </button>
                <button class="theme-option" :class="{ selected: appearance.theme === 'system' }" type="button" :aria-pressed="appearance.theme === 'system'" @click="appearance.theme = 'system'">
                  <span class="theme-swatch system-swatch"><i></i><i></i><i></i></span><span>跟随系统</span>
                </button>
                <button class="theme-option" :class="{ selected: appearance.theme === 'light' }" type="button" :aria-pressed="appearance.theme === 'light'" @click="appearance.theme = 'light'">
                  <span class="theme-swatch light-swatch"><i></i><i></i><i></i></span><span>浅色</span>
                </button>
              </div>
            </div>

            <div class="field color-field">
              <span>背景颜色</span>
              <div class="color-control"><input v-model="appearance.backgroundColor" aria-label="背景颜色" type="color" /><code>{{ safeBackgroundColor.toUpperCase() }}</code></div>
            </div>

            <div class="background-upload-row">
              <div class="background-preview" :style="{ backgroundColor: safeBackgroundColor }">
                <img v-if="backgroundSrc" :key="backgroundRevision" :src="backgroundSrc" alt="当前背景图片" />
                <span v-else>WORKSPACE PREVIEW</span>
              </div>
              <div class="upload-copy"><strong>登录页背景图片</strong><span>添加一张图片作为登录页的背景。</span></div>
              <label class="secondary-button upload-button">
                {{ uploading === 'background' ? '上传中…' : '上传图片' }}
                <input aria-label="上传背景图片" type="file" accept="image/png,image/jpeg,image/gif" :disabled="!!uploading" @change="handleImageChange('background', $event)" />
              </label>
            </div>

            <div class="form-actions"><button class="primary-button" type="submit" :disabled="busy">{{ busy ? busyLabel : '保存外观' }} <span aria-hidden="true">↗</span></button></div>
          </form>
        </section>

        <section class="settings-card security-card" aria-labelledby="security-heading">
          <div class="section-heading">
            <div class="section-icon security-icon" aria-hidden="true">⌑</div>
            <div><h3 id="security-heading">安全与密码</h3><p>定期更新密码，保护您的管理账户。</p></div>
          </div>
          <form class="password-form" @submit.prevent="changePassword">
              <label class="field"><span>当前密码</span><input v-model="passwordForm.currentPassword" type="password" autocomplete="current-password" required /></label>
              <div class="password-pair">
                <label class="field"><span>新密码</span><input v-model="passwordForm.newPassword" type="password" autocomplete="new-password" required /></label>
                <label class="field"><span>确认新密码</span><input v-model="passwordForm.confirmPassword" type="password" autocomplete="new-password" required /></label>
              </div>
            <small class="field-hint">新密码至少 12 个字符；超出服务器允许长度时会提示错误。</small>
            <div class="form-actions"><button class="secondary-button" type="submit" :disabled="busy">更新密码</button></div>
          </form>
        </section>

        <section class="settings-card sessions-card" aria-labelledby="sessions-heading">
          <div class="section-heading session-heading">
            <div class="section-icon session-icon" aria-hidden="true">⌘</div>
            <div><h3 id="sessions-heading">活跃会话</h3><p>查看并撤销正在访问账户的设备。</p></div>
            <span class="session-count">{{ sessions.length }} 个</span>
          </div>
          <div v-if="sessions.length" class="session-list">
            <article v-for="session in sessions" :key="session.id" class="session-item">
              <div class="device-icon" aria-hidden="true">▣</div>
              <div class="session-info"><strong>{{ session.device || '未知设备' }}<span v-if="session.current" class="current-badge">当前设备</span></strong><span>{{ session.remoteAddr || '未知地址' }}</span><small>最近活动 {{ formatTime(session.lastSeenAt) }}</small></div>
              <button class="revoke-button" type="button" :disabled="busy || session.current" :aria-label="session.current ? '当前设备会话' : `撤销 ${session.device || '未知设备'} 会话`" @click="revokeSession(session)">{{ session.current ? '当前' : '撤销' }}</button>
            </article>
          </div>
          <p v-else class="empty-sessions">暂无会话信息。</p>
        </section>

        <footer class="settings-footer"><span>NodeDance Core</span><span>本地管理 · 安全优先</span></footer>
      </div>
    </section>
  </main>
</template>
