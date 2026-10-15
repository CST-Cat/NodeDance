<script setup lang="ts">
import { operationURL } from './operations'
import { operationRequest } from './api'
import HostManagement from './components/HostManagement.vue'
import UpdateCenter from './components/UpdateCenter.vue'
import FeedsCenter from './components/FeedsCenter.vue'
import OperationConsole from './components/OperationConsole.vue'
import { t, display, locale, setLocale, type Locale } from "./i18n"
import { defineAsyncComponent, computed, nextTick, onBeforeUnmount, onMounted, provide, reactive, ref, watch } from 'vue'
import { ApiError, api, SESSION_EXPIRED_EVENT, type Appearance, type Session, type User } from './api'
import NodesDashboard from './components/NodesDashboard.vue'
import TailscaleDiscovery from './components/TailscaleDiscovery.vue'
import AgentEnrollment from './components/AgentEnrollment.vue'
import AlertCenter from './components/AlertCenter.vue'
import UiIcon from './components/UiIcon.vue'
import UiDialog from './components/UiDialog.vue'
import { cancelDialogs } from './dialogs'
import { nodeNavigationKey, navigationKey, readViewRoute, viewHash, writeViewRoute, type NodeNavigation, type ViewRoute, type ViewScreen } from './navigation'

const GuestDisplay=defineAsyncComponent(()=>import('./components/GuestDisplay.vue'))
const GuestSettings=defineAsyncComponent(()=>import('./components/GuestSettings.vue'))

type Screen = ViewScreen | 'loading' | 'unavailable'
type ImageKind = 'avatar' | 'background'

const screen = ref<Screen>('loading')
watch(screen, cancelDialogs)
const navigationOpen = ref(false)
const mobileNavigation = ref(false)
const sidebarElement = ref<HTMLElement>()
const sidebarCollapsed = ref(false)
const sidebarWidth = ref(240)
const sidebarResizing = ref(false)
try {
  const saved = JSON.parse(localStorage.getItem('nodedance.sidebar') || 'null')
  if (saved) {
    sidebarCollapsed.value = saved.collapsed === true
    if (Number.isFinite(saved.width)) sidebarWidth.value = Math.max(200, Math.min(400, saved.width))
  }
} catch {}
watch([sidebarCollapsed, sidebarWidth], () => {
  try { localStorage.setItem('nodedance.sidebar', JSON.stringify({ collapsed: sidebarCollapsed.value, width: sidebarWidth.value })) } catch {}
})
function toggleNavigation() {
  if (mobileNavigation.value) navigationOpen.value = !navigationOpen.value
  else sidebarCollapsed.value = !sidebarCollapsed.value
}
function resizeSidebar(event: PointerEvent) {
  if (mobileNavigation.value || sidebarCollapsed.value || event.button !== 0) return
  const handle = event.currentTarget as HTMLElement
  sidebarResizing.value = true
  handle.setPointerCapture(event.pointerId)
  const start = event.clientX, width = sidebarWidth.value
  const move = (next: PointerEvent) => { sidebarWidth.value = Math.max(200, Math.min(400, width + next.clientX - start)) }
  const stop = () => { sidebarResizing.value = false; handle.removeEventListener('pointermove', move); handle.removeEventListener('lostpointercapture', stop) }
  handle.addEventListener('pointermove', move)
  handle.addEventListener('lostpointercapture', stop)
}
function resizeSidebarKeys(event: KeyboardEvent) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  sidebarWidth.value = event.key === 'Home' ? 200 : event.key === 'End' ? 400
    : Math.max(200, Math.min(400, sidebarWidth.value + (event.key === 'ArrowRight' ? 10 : -10)))
}
const navigationToggle = ref<HTMLButtonElement>()
let colorScheme: MediaQueryList | undefined
let navigationQuery: MediaQueryList | undefined
function updateColorScheme(event: MediaQueryListEvent) { preferredTheme.value = event.matches ? 'dark' : 'light' }
function updateNavigationViewport(event: MediaQueryListEvent) {
  mobileNavigation.value = event.matches
  navigationOpen.value = false
}
function handleNavigationKeys(event: KeyboardEvent) {
  if (!mobileNavigation.value || !navigationOpen.value) return
  if (event.key === 'Escape') {
    navigationOpen.value = false
    navigationToggle.value?.focus()
  } else if (event.key === 'Tab') {
    const items = sidebarElement.value?.querySelectorAll<HTMLElement>('a, button:not(:disabled)')
    if (!items?.length) return
    const first = items[0]!, last = items[items.length - 1]!
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
  }
}
watch(navigationOpen, async (open) => {
  if (open && mobileNavigation.value) {
    await nextTick()
    sidebarElement.value?.querySelector<HTMLElement>('[aria-current=page]')?.focus()
  }
})
const authenticated = computed(() => !!currentUser.value && !['guest', 'loading', 'unavailable', 'setup', 'login'].includes(screen.value))
const navigation = [
  { screen: 'nodes', label: 'nav.nodes', icon: 'nodes' },
  { screen: 'agent-enrollment', label: 'nav.enrollment', icon: 'add' },
  { screen: 'discovery', label: 'nav.discovery', icon: 'discovery' },
  { screen: 'alerts', label: 'nav.alerts', icon: 'alerts' },
  { screen: 'host-management', label: 'host.title', icon: 'host' },
  { screen: 'updates', label: 'nav.updates', icon: 'refresh' },
  { screen: 'feeds', label: 'feeds.title', icon: 'subscriptions' },
  { screen: 'operations', label: 'nav.operations', icon: 'operations' },
  { screen:'guest-settings',label:'guest.settings',icon:'guest' },
  { screen: 'settings', label: 'nav.settings', icon: 'settings' },
] as const

const activeRoute = ref<ViewRoute>(readViewRoute())
const nodeNavigation = ref<NodeNavigation | null>(null)
provide(nodeNavigationKey, nodeNavigation)
const detailNavigation = computed(() => screen.value === 'nodes' && activeRoute.value.nodeId === nodeNavigation.value?.nodeId ? nodeNavigation.value : null)
const screenTitle = computed(() => detailNavigation.value?.sections.find(item => item.id === detailNavigation.value?.activeSection)?.label ?? t(navigation.find(item => item.screen === screen.value)?.label ?? 'nav.workspace'))
function navigateNodeSection(section: string) {
  if (detailNavigation.value) goToRoute({ screen: 'nodes', nodeId: detailNavigation.value.nodeId, section })
}
let routeSequence = 0
let handledLocation = ''
let loginDestination: ViewRoute = { screen: 'nodes' }
provide(navigationKey, { route: activeRoute, go: goToRoute })

function displayRoute(route: ViewRoute) {
  activeRoute.value = route
  screen.value = route.screen
  navigationOpen.value = false
  writeViewRoute(route, true)
  handledLocation = window.location.href
  void nextTick(() => window.scrollTo(0, 0))
}
function goToRoute(route: ViewRoute, replace = false) {
  if (window.location.hash === viewHash(route) && screen.value === route.screen) return
  writeViewRoute(route, replace)
  handledLocation = window.location.href
  void activateRoute(readViewRoute())
}
function navigate(target: ViewScreen, replace = false) { goToRoute({ screen: target }, replace) }
function handleHistoryPageShow(event: PageTransitionEvent) {
  if (!event.persisted) return
  currentUser.value = null
  sessions.value = []
  loginPassword.value = ''
  setupForm.password = ''
  setupForm.credential = ''
  passwordForm.currentPassword = ''
  passwordForm.newPassword = ''
  passwordForm.confirmPassword = ''
  handledLocation = window.location.href
  void activateRoute(readViewRoute())
}
function handleHistoryNavigation() {
  if (handledLocation === window.location.href) return
  handledLocation = window.location.href
  void activateRoute(readViewRoute())
}

const busy = ref(false)
const busyLabel = ref('')
const error = ref('')
const notice = ref('')
const guestEnabled = ref(false)
watch(screen,async current=>{if(current!=='login')return;guestEnabled.value=false;try{guestEnabled.value=(await operationRequest<{enabled:boolean}>('guest.entry')).enabled}catch{guestEnabled.value=false}})
const initialized = ref(false)
const currentUser = ref<User | null>(null)
const sessions = ref<Session[]>([])
const uploading = ref<ImageKind | ''>('')
const preferredTheme = ref<'dark' | 'light'>(window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')
const avatarRevision = ref(0)
const backgroundRevision = ref(0)

const setupForm = reactive({ credential: '', password: '', username: '' })
const initialCredentials = ref<{ username: string; password?: string } | null>(null)
const loginUsername = ref('')
const loginPassword = ref('')
const passwordForm = reactive({ currentPassword: '', newPassword: '', confirmPassword: '' })
const appearance = reactive<Appearance>({
  displayName: '',
  theme: (document.documentElement.dataset.themePreference as Appearance['theme']) || 'system',
  backgroundColor: '#faf9f5',
  avatarUrl: '',
  backgroundUrl: '',
})

const safeBackgroundColor = computed(() => /^#[\da-f]{6}$/i.test(appearance.backgroundColor)
  ? appearance.backgroundColor
  : '#101827')
const shellStyle = computed(() => ({ '--page-color': safeBackgroundColor.value, '--sidebar-width': `${sidebarCollapsed.value && !mobileNavigation.value ? 76 : sidebarWidth.value}px` }))
const effectiveTheme = computed(() => appearance.theme === 'system' ? preferredTheme.value : appearance.theme)
watch(effectiveTheme, theme => {
  document.documentElement.dataset.theme = theme
  document.documentElement.style.colorScheme = theme
  document.documentElement.style.backgroundColor = theme === 'dark' ? '#262624' : '#faf9f5'
  document.querySelector('meta[name=theme-color]')?.setAttribute('content', theme === 'dark' ? '#262624' : '#faf9f5')
}, { immediate:true })

const imagePaths = reactive({ avatar: '', background: '' })
function imageUrl(value: string, kind: ImageKind): string {
  if (!value) return ''
  try {
    const url = new URL(value, window.location.origin)
    const expectedPath = imagePaths[kind]
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
const visibleName = computed(() => appearance.displayName || currentUser.value?.displayName || t('m.e19796712f1c'))

function clearMessages() {
  error.value = ''
  notice.value = ''
}

function errorText(reason: unknown): string {
  if (reason instanceof Error && reason.message) return reason.message
  return t('m.2c1e5ec62145')
}

function rememberTheme() {
  try { localStorage.setItem('nodedance.theme', appearance.theme) } catch {}
}

async function loadAppearance() {
  const [result, avatar, background] = await Promise.all([api.appearance(), operationURL('appearance.avatar'), operationURL('appearance.background')])
  imagePaths.avatar = avatar.pathname; imagePaths.background = background.pathname
  Object.assign(appearance, result)
  if (!/^#[\da-f]{6}$/i.test(appearance.backgroundColor)) {
    appearance.backgroundColor = '#101827'
  }
  if (appearance.theme !== 'light' && appearance.theme !== 'dark' && appearance.theme !== 'system') appearance.theme = 'system'
  rememberTheme()
}

async function loadSessions() {
  const result = await api.sessions()
  sessions.value = result.sessions
}

async function enterSettings(sequence = routeSequence) {
  const result = await api.me()
  if (sequence !== routeSequence) return false
  currentUser.value = result.user
  const results = await Promise.allSettled([loadAppearance(), loadSessions()])
  const rejected = results.find((item) => item.status === 'rejected')
  if (sequence !== routeSequence) return false
  if (rejected?.status === 'rejected') {
    notice.value = t('m.2ccd4dc3ec31')
  }
  return true
}

async function activateRoute(target: ViewRoute) {
  const sequence = ++routeSequence
  navigationOpen.value = false
  busy.value = false
  busyLabel.value = ''
  uploading.value = ''
  cancelDialogs()
  clearMessages()
  if (target.screen !== 'login') loginPassword.value = ''
  if (target.screen !== 'setup') { setupForm.password = ''; setupForm.credential = '' }
  if (target.screen === 'guest') {
    currentUser.value = null
    sessions.value = []
    displayRoute({ screen: 'guest' })
    return
  }
  if (!currentUser.value) screen.value = 'loading'
  try {
    if (!initialized.value) {
      const state = await api.setupStatus()
      if (sequence !== routeSequence) return
      initialized.value = state.initialized
      await loadAppearance()
      if (sequence !== routeSequence) return
    }
    if (!initialized.value) { displayRoute({ screen: 'setup' }); return }
    if (target.screen === 'login') {
      currentUser.value = null
      sessions.value = []
      displayRoute({ screen: 'login' })
      return
    }
    const result = await api.me()
    if (sequence !== routeSequence) return
    currentUser.value = result.user
    if (target.screen === 'settings') {
      const results = await Promise.allSettled([loadAppearance(), loadSessions()])
      if (sequence !== routeSequence) return
      if (results.some(item => item.status === 'rejected')) notice.value = t('m.2ccd4dc3ec31')
    }
    displayRoute(target.screen === 'setup' ? { screen: 'nodes' } : target)
  } catch (reason) {
    if (sequence !== routeSequence) return
    if (reason instanceof ApiError && reason.status === 401) {
      currentUser.value = null
      sessions.value = []
      loginDestination = target
      displayRoute({ screen: 'login' })
      return
    }
    error.value = errorText(reason)
    screen.value = 'unavailable'
  }
}
async function initializeScreen() { await activateRoute(readViewRoute()) }

async function submitSetup() {
  const sequence = routeSequence
  clearMessages()
  busy.value = true
  busyLabel.value = t('m.9866f869b0bd')
  try {
    const result = await api.setup({
      credential: setupForm.credential,
      password: setupForm.password,
      username: setupForm.username,
    })
    if (sequence !== routeSequence) return
    initialCredentials.value = result.initialCredentials ?? null
    initialized.value = true
    try {
      if (!await enterSettings(sequence)) return
    } catch (reason) {
      if (!(reason instanceof ApiError) || reason.status !== 401) throw reason
      await api.login(result.initialCredentials?.username ?? setupForm.username, result.initialCredentials?.password ?? setupForm.password)
      if (!await enterSettings(sequence)) return
    }
    initialCredentials.value = result.initialCredentials ?? null
    navigate('nodes', true)
    setupForm.credential = ''
    setupForm.password = ''
    setupForm.username = ''
    notice.value = t('m.ea48349b4814')
  } catch (reason) {
    if (sequence === routeSequence) error.value = errorText(reason)
  } finally {
    if (sequence === routeSequence) { busy.value = false; busyLabel.value = '' }
  }
}

async function submitLogin() {
  const sequence = routeSequence
  clearMessages()
  busy.value = true
  busyLabel.value = t('m.d8712b0d82e6')
  try {
    await api.login(loginUsername.value, loginPassword.value)
    if (sequence !== routeSequence || !await enterSettings(sequence)) return
    goToRoute(loginDestination, true)
    loginPassword.value = ''
  } catch (reason) {
    if (sequence === routeSequence) error.value = errorText(reason)
  } finally {
    if (sequence === routeSequence) { busy.value = false; busyLabel.value = '' }
  }
}

async function saveProfile() {
  clearMessages()
  busy.value = true
  busyLabel.value = t('m.6bdb4435095e')
  try {
    await api.saveAppearance({
      displayName: appearance.displayName,
      theme: appearance.theme,
      backgroundColor: appearance.backgroundColor,
    })
    rememberTheme()
    currentUser.value = { displayName: appearance.displayName }
    notice.value = t('m.e90110b8adca')
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
    error.value = t('m.37492576162f')
    return
  }

  uploading.value = kind
  try {
    await api.uploadImage(kind, image)
    if (kind === 'avatar') avatarRevision.value += 1
    else backgroundRevision.value += 1
    await loadAppearance()
    notice.value = kind === 'avatar' ? t('m.38e48df5ee98') : t('m.4e208b8c6edc')
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
    notice.value = t('m.2f372ae7fc9d')
  } catch (reason) {
    error.value = errorText(reason)
  } finally {
    busy.value = false
  }
}

async function changePassword() {
  const sequence = routeSequence
  clearMessages()
  if (passwordForm.newPassword !== passwordForm.confirmPassword) {
    error.value = t('m.a2ba2545a60e')
    return
  }

  busy.value = true
  busyLabel.value = t('m.2c62c5fc337d')
  try {
    await api.changePassword(passwordForm.currentPassword, passwordForm.newPassword)
    if (sequence !== routeSequence) { handleSessionExpired(); return }
    currentUser.value = null
    initialCredentials.value = null
    sessions.value = []
    loginPassword.value = ''
    setupForm.password = ''
    passwordForm.currentPassword = ''
    passwordForm.newPassword = ''
    passwordForm.confirmPassword = ''
    loginDestination = { screen: 'nodes' }
    navigate('login', true)
    notice.value = t('m.37a88dd4c7a5')
  } catch (reason) {
    error.value = errorText(reason)
  } finally {
    busy.value = false
    busyLabel.value = ''
  }
}

async function logout() {
  const sequence = routeSequence
  clearMessages()
  busy.value = true
  try {
    await api.logout()
    if (sequence !== routeSequence) { handleSessionExpired(); return }
    currentUser.value = null
    initialCredentials.value = null
    sessions.value = []
    loginDestination = { screen: 'nodes' }
    navigate('login', true)
    notice.value = t('m.3a7b6d2f0e58')
  } catch (reason) {
    error.value = errorText(reason)
  } finally {
    busy.value = false
  }
}

function handleSessionExpired() {
  if(screen.value==='guest'){currentUser.value=null;sessions.value=[];return}
  if(!currentUser.value)return
  initialCredentials.value = null
  currentUser.value = null
  sessions.value = []
  loginPassword.value = ''
  setupForm.password = ''
  passwordForm.currentPassword = ''
  passwordForm.newPassword = ''
  passwordForm.confirmPassword = ''
  busy.value = false
  busyLabel.value = ''
  uploading.value = ''
  clearMessages()
  const destination = readViewRoute()
  if (!['login', 'guest', 'setup'].includes(destination.screen)) loginDestination = destination
  routeSequence++
  displayRoute({ screen: 'login' })
  notice.value = t('m.bd0bb959fbb4')
}

function formatTime(value: string): string {
  const time = new Date(value)
  return Number.isNaN(time.getTime()) ? '—' : time.toLocaleString(locale.value)
}

onMounted(() => {
  window.addEventListener(SESSION_EXPIRED_EVENT, handleSessionExpired)
  colorScheme = window.matchMedia('(prefers-color-scheme: dark)')
  preferredTheme.value = colorScheme.matches ? 'dark' : 'light'
  colorScheme.addEventListener('change', updateColorScheme)
  navigationQuery = window.matchMedia('(max-width: 900px)')
  mobileNavigation.value = navigationQuery.matches
  navigationQuery.addEventListener('change', updateNavigationViewport)
  window.addEventListener('keydown', handleNavigationKeys)
  window.addEventListener('pageshow', handleHistoryPageShow)
  window.addEventListener('popstate', handleHistoryNavigation)
  window.addEventListener('hashchange', handleHistoryNavigation)
  handledLocation = window.location.href
  void initializeScreen()
})

onBeforeUnmount(() => {
  window.removeEventListener(SESSION_EXPIRED_EVENT, handleSessionExpired)
  window.removeEventListener('keydown', handleNavigationKeys)
  window.removeEventListener('pageshow', handleHistoryPageShow)
  window.removeEventListener('popstate', handleHistoryNavigation)
  window.removeEventListener('hashchange', handleHistoryNavigation)
  routeSequence++
  colorScheme?.removeEventListener('change', updateColorScheme)
  navigationQuery?.removeEventListener('change', updateNavigationViewport)
})
</script>

<template>
  <main class="app-shell" :data-theme="effectiveTheme" :data-screen="screen" :data-authenticated="authenticated" :data-sidebar-resizing="sidebarResizing" :style="shellStyle">
    <div v-if="screen === 'login' && backgroundSrc" class="login-background" aria-hidden="true">
      <img :key="backgroundRevision" :src="backgroundSrc" alt="" />
      <div class="login-background-shade"></div>
    </div>

    <button v-if="authenticated && navigationOpen" class="navigation-scrim" type="button" :aria-label="t('nav.close')" @click="navigationOpen = false"></button>
    <aside v-if="authenticated" id="workspace-navigation" ref="sidebarElement" class="workspace-sidebar" :inert="mobileNavigation && !navigationOpen" :class="{ 'is-open': navigationOpen, 'is-collapsed': sidebarCollapsed && !mobileNavigation }">
      <a class="wordmark" href="/" :aria-label="t('m.f359facd4191')">
        <span class="brand-mark" aria-hidden="true"><i></i><i></i><i></i></span><span>NodeDance</span>
      </a>
      <Transition name="sidebar-context" mode="out-in">
        <div :key="detailNavigation?.nodeId || 'workspace'" class="sidebar-context" :data-context="detailNavigation ? 'node' : 'workspace'">
          <nav v-if="detailNavigation" class="workspace-nav node-detail-navigation" :aria-label="t('nav.nodeWorkspace')" data-testid="node-detail-navigation">
            <button type="button" class="node-navigation-back" :title="t('nav.returnHome')" :aria-label="t('nav.returnHome')" @click="navigate('nodes')"><UiIcon name="back" /><span>{{ t('nav.returnHome') }}</span></button>
            <div class="workspace-label node-navigation-title" :title="detailNavigation.title">{{ detailNavigation.title }}</div>
            <button v-for="item in detailNavigation.sections" :key="item.id" type="button" :title="item.label" :aria-label="item.label" :aria-current="detailNavigation.activeSection === item.id ? 'page' : undefined" @click="navigateNodeSection(item.id)"><UiIcon :name="item.icon" /><span>{{ item.label }}</span></button>
          </nav>
          <template v-else>
            <div class="workspace-label">{{ t('nav.workspace') }}</div>
            <nav class="workspace-nav" :aria-label="t('nav.navigation')">
              <button v-for="item in navigation" :key="item.screen" type="button" :title="t(item.label)" :aria-label="t(item.label)" :aria-current="screen === item.screen ? 'page' : undefined" @click="navigate(item.screen)"><UiIcon :name="item.icon" /><span>{{ t(item.label) }}</span></button>
            </nav>
          </template>
        </div>
      </Transition>
      <div class="workspace-account">
        <div class="profile-avatar"><img v-if="avatarSrc" :src="avatarSrc" alt="" /><span v-else>{{ visibleName.slice(0, 1) }}</span></div>
        <div class="workspace-account-copy"><strong>{{ visibleName }}</strong><span>{{ t('m.e19796712f1c') }}</span></div>
        <button class="account-logout" type="button" :disabled="busy" :aria-label="t('m.3ab8cc15939f')" :title="t('m.3ab8cc15939f')" @click="logout"><UiIcon name="logout" /></button>
      </div>
      <div v-if="!mobileNavigation && !sidebarCollapsed" class="sidebar-resize" role="separator" aria-orientation="vertical" tabindex="0" :aria-label="t('nav.resize')" :aria-valuemin="200" :aria-valuemax="400" :aria-valuenow="sidebarWidth" @pointerdown="resizeSidebar" @keydown="resizeSidebarKeys"></div>
    </aside>
    <header class="topbar">
      <div v-if="authenticated" class="workspace-breadcrumb">
        <button ref="navigationToggle" class="navigation-toggle" type="button" :aria-label="t(mobileNavigation ? 'nav.navigation' : sidebarCollapsed ? 'nav.expand' : 'nav.collapse')" :aria-expanded="mobileNavigation ? navigationOpen : !sidebarCollapsed" aria-controls="workspace-navigation" @click="toggleNavigation"><UiIcon :name="navigationOpen ? 'close' : 'menu'" /></button>
        <span class="breadcrumb-root">{{ t('nav.workspace') }}</span><span class="breadcrumb-divider" aria-hidden="true">/</span><strong>{{ screenTitle }}</strong>
      </div>
      <a v-else class="wordmark" href="/" :aria-label="t('m.f359facd4191')"><span class="brand-mark" aria-hidden="true"><i></i><i></i><i></i></span><span>NodeDance</span></a>
      <div class="topbar-right">
        <span class="system-label"><span class="status-dot"></span>{{ t('m.13354cb51f27') }}</span>
        <UiSelect :key="viewHash(activeRoute)" class="language-selector" menu-class="language-options" :aria-label="locale === 'en' ? 'Language' : '语言'" :value="locale" @change="setLocale(($event.target as HTMLSelectElement).value as Locale)">
          <option value="zh-CN">简体中文</option><option value="en">English</option>
        </UiSelect>
      </div>
    </header>

    <section v-if="authenticated && initialCredentials" class="initial-credentials-card" aria-labelledby="initial-credentials-title">
      <h2 id="initial-credentials-title">{{ t('auth.generatedTitle') }}</h2><p>{{ t('auth.generatedHint') }}</p>
      <dl><div><dt>{{ t('auth.username') }}</dt><dd><code>{{ initialCredentials.username }}</code></dd></div><div v-if="initialCredentials.password"><dt>{{ t('m.c19091521d71') }}</dt><dd><code>{{ initialCredentials.password }}</code></dd></div></dl>
      <button type="button" class="secondary-button" @click="initialCredentials = null">{{ t('auth.saved') }}</button>
    </section>

    <div v-if="authenticated && screen !== 'settings' && notice" class="alert alert-success workspace-notice" role="status">{{ display(notice) }}</div>
    <div v-if="authenticated && screen !== 'settings' && error" class="alert alert-error workspace-notice" role="alert">{{ display(error) }}</div>

    <div v-if="screen === 'loading'" class="center-stage" role="status" aria-live="polite">
      <div class="loader"></div>
      <p>{{ t('m.a62a3d2a07c9') }}</p>
    </div>

    <section v-else-if="screen === 'unavailable'" class="center-stage">
      <div class="standalone-card">
        <span class="eyebrow">{{ t('label.638731745e22') }}</span>
        <h1>{{ t('m.c579038c5ec3') }}</h1>
        <p>{{ t('m.3ee483b99392') }}</p>
        <div v-if="error" class="alert alert-error" role="alert">{{ display(error) }}</div>
        <button class="primary-button" type="button" @click="initializeScreen">{{ t('m.68891ab35cab') }}<span aria-hidden="true">↗</span></button>
      </div>
    </section>

    <section v-else-if="screen === 'setup'" class="auth-layout">
      <div class="auth-copy">
        <span class="eyebrow">{{ t('label.fec911d2155d') }}</span>
        <h1>{{ t('m.b640c195b999') }}<br /><em>{{ t('m.c89b43673182') }}</em></h1>
        <p>{{ t('m.5abf65737f4c') }}</p>
        <div class="feature-row"><span class="feature-symbol">✳</span><span>{{ t('m.caa90381315a') }}</span></div>
      </div>

      <section class="form-card" aria-labelledby="form-title">
        <div class="card-heading">
          <span class="eyebrow">{{ t('label.04b7b6c5d04d') }}</span>
          <span class="step-indicator"><span class="step-current">01</span><span class="step-line"></span>02</span>
        </div>
        <h2 id="form-title">{{ t('m.bcea39d8590e') }}</h2>
        <p class="card-intro">{{ t('m.07c18f374537') }}</p>
        <div v-if="error" class="alert alert-error" role="alert">{{ display(error) }}</div>

        <form class="form-stack" @submit.prevent="submitSetup">
          <label class="field">
            <span>{{ t('m.fc482bd2d5bf') }}</span>
            <input v-model="setupForm.credential" name="credential" type="password" autocomplete="one-time-code" required :placeholder="t('m.f1f7cbf5f906')" />
            <small class="field-hint">{{ t('m.59b4ebe09d0f') }}</small>
          </label>
          <label class="field">
            <span>{{ t('auth.username') }}</span>
            <input v-model="setupForm.username" name="username" autocomplete="username" maxlength="80" :placeholder="t('auth.generateUsername')" />
            <small class="field-hint">{{ t('auth.usernameHint') }}</small>
          </label>
          <label class="field">
            <span>{{ t('m.c19091521d71') }}</span>
            <input v-model="setupForm.password" name="password" type="password" autocomplete="new-password" :placeholder="t('auth.generatePassword')" />
            <small class="field-hint">{{ t('m.eb0831f61840') }}</small>
          </label>
          <button class="primary-button full-button" type="submit" :disabled="busy">
            {{ busy ? busyLabel : t('m.b95a201cc95d') }} <span aria-hidden="true">↗</span>
          </button>
        </form>
        <p class="privacy-note"><span aria-hidden="true">◈</span>{{ t('m.1e6ae53da093') }}</p>
      </section>
    </section>

    <section v-else-if="screen === 'login'" class="auth-layout login-layout">
      <div class="auth-copy login-copy">
        <span class="eyebrow">{{ t('label.111bdc0c1ef5') }}</span>
        <h1>{{ t('m.cb1d97794707') }}<span class="title-period">.</span></h1>
        <p>{{ t('m.cd2de59b2219') }}</p>
        <div class="login-identity">
          <div class="preview-avatar">
            <img v-if="avatarSrc" :key="avatarRevision" :src="avatarSrc" alt="" />
            <span v-else aria-hidden="true">{{ (appearance.displayName || 'N').slice(0, 1) }}</span>
          </div>
          <div><span class="preview-label">{{ t('label.e00e2736dd16') }}</span><strong>{{ appearance.displayName || 'NodeDance' }}</strong></div>
        </div>
      </div>

      <section class="form-card login-card" aria-labelledby="login-title">
        <span class="eyebrow">{{ t('label.fe274f9f3a26') }}</span>
        <h2 id="login-title">{{ t('m.c454c61e78ab') }}</h2>
        <p class="card-intro">{{ t('m.d62d9f683b33') }}</p>
        <div v-if="notice" class="alert alert-success" role="status">{{ display(notice) }}</div>
        <div v-if="error" class="alert alert-error" role="alert">{{ display(error) }}</div>
        <form class="form-stack" @submit.prevent="submitLogin">
          <label class="field"><span>{{ t('auth.username') }}</span><input v-model="loginUsername" name="username" autocomplete="username" required autofocus maxlength="80" :placeholder="t('auth.username')" /></label>
          <label class="field">
            <span>{{ t('m.c19091521d71') }}</span>
            <input v-model="loginPassword" name="password" type="password" autocomplete="current-password" required :placeholder="t('m.7673702e6ff6')" />
          </label>
          <button class="primary-button full-button" type="submit" :disabled="busy">
            {{ busy ? busyLabel : t('m.d19038809234') }} <span aria-hidden="true">↗</span>
          </button>
        </form>
        <button v-if="guestEnabled" class="secondary-button full-button guest-entry" type="button" :disabled="busy" @click="navigate('guest')"><UiIcon name="guest" />{{t('guest.enter')}}</button>
        <p class="privacy-note"><span aria-hidden="true">◈</span>{{ t('m.3834cf7462ca') }}</p>
      </section>
    </section>

    <GuestDisplay v-else-if="screen==='guest'" @back="navigate('nodes')" />
    <GuestSettings v-else-if="screen==='guest-settings'" />
    <NodesDashboard v-else-if="screen === 'nodes'" @enroll="navigate('agent-enrollment')" />

    <AgentEnrollment v-else-if="screen === 'agent-enrollment'" @back="navigate('nodes')" />

    <HostManagement v-else-if="screen === 'host-management'" />
      <UpdateCenter v-else-if="screen === 'updates'" />
      <FeedsCenter v-else-if="screen === 'feeds'" />
    <OperationConsole v-else-if="screen === 'operations'" />

    <AlertCenter v-else-if="screen === 'alerts'" />

    <TailscaleDiscovery v-else-if="screen === 'discovery'" @back="navigate('nodes')" />

    <section v-else class="settings-layout">
      <aside class="settings-sidebar">
        <span class="eyebrow">{{ t('label.5493ff8e9397') }}</span>
        <h1>{{ t('m.df3d58c7d84b') }}<span class="title-period">.</span></h1>
        <p>{{ t('m.e2d752caafea') }}</p>
        <div class="sidebar-profile">
          <div class="profile-avatar">
            <img v-if="avatarSrc" :key="avatarRevision" :src="avatarSrc" alt="" />
            <span v-else aria-hidden="true">{{ visibleName.slice(0, 1) }}</span>
          </div>
          <div class="profile-summary"><strong>{{ visibleName }}</strong><span>{{ t('m.e19796712f1c') }}</span></div>
          <span class="profile-dot" aria-hidden="true"></span>
        </div>
        <div class="side-note"><span class="side-note-icon">✳</span><span>{{ t('m.0246d86fbc36') }}<br />{{ t('m.86d345181d6a') }}</span></div>
      </aside>

      <div class="settings-main">
        <div class="settings-heading">
          <div><span class="eyebrow">{{ t('label.812ff062d7b7') }}</span><h2>{{ t('m.0bb1a9d13722') }}</h2></div>
          <span class="secure-badge"><span aria-hidden="true">◈</span>{{ t('m.fb458e284aad') }}</span>
        </div>
        <div v-if="notice" class="alert alert-success page-alert" role="status">{{ display(notice) }}</div>
        <div v-if="error" class="alert alert-error page-alert" role="alert">{{ display(error) }}</div>

        <section class="settings-card appearance-card" aria-labelledby="appearance-heading">
          <div class="section-heading">
            <div class="section-icon appearance-icon" aria-hidden="true">◉</div>
            <div><h3 id="appearance-heading">{{ t('m.ad429c44450e') }}</h3><p>{{ t('m.d6fb19acbade') }}</p></div>
          </div>

          <form class="profile-form" @submit.prevent="saveProfile">
            <div class="profile-edit-row">
              <div class="profile-avatar large-avatar">
                <img v-if="avatarSrc" :key="avatarRevision" :src="avatarSrc" alt="" />
                <span v-else aria-hidden="true">{{ visibleName.slice(0, 1) }}</span>
              </div>
              <div class="upload-copy"><strong>{{ t('m.3ea2d23c902c') }}</strong><span>{{ t('m.b8ed7facdbe2') }}</span></div>
              <label class="secondary-button upload-button">
                {{ uploading === 'avatar' ? t('m.ce1c9d61565f') : t('m.42ece898483f') }}
                <input :aria-label="t('m.d7d2344d545d')" type="file" accept="image/png,image/jpeg,image/gif" :disabled="!!uploading" @change="handleImageChange('avatar', $event)" />
              </label>
            </div>

            <label class="field profile-name-field">
              <span>{{ t('m.1264cd049636') }}</span>
              <input v-model="appearance.displayName" name="profileDisplayName" autocomplete="nickname" required maxlength="80" />
              <small class="field-hint">{{ t('m.f673747ede6e') }}</small>
            </label>

            <div class="field">
              <span>{{ t('m.8f29293906e7') }}</span>
              <div class="theme-options" role="group" :aria-label="t('m.8f29293906e7')">
                <button class="theme-option" :class="{ selected: appearance.theme === 'dark' }" type="button" :aria-pressed="appearance.theme === 'dark'" @click="appearance.theme = 'dark'">
                  <span class="theme-swatch dark-swatch"><i></i><i></i><i></i></span><span>{{ t('m.a6b75d068032') }}</span>
                </button>
                <button class="theme-option" :class="{ selected: appearance.theme === 'system' }" type="button" :aria-pressed="appearance.theme === 'system'" @click="appearance.theme = 'system'">
                  <span class="theme-swatch system-swatch"><i></i><i></i><i></i></span><span>{{ t('m.217cfe7db1e3') }}</span>
                </button>
                <button class="theme-option" :class="{ selected: appearance.theme === 'light' }" type="button" :aria-pressed="appearance.theme === 'light'" @click="appearance.theme = 'light'">
                  <span class="theme-swatch light-swatch"><i></i><i></i><i></i></span><span>{{ t('m.aa0819dfc4d8') }}</span>
                </button>
              </div>
            </div>

            <div class="field color-field">
              <span>{{ t('m.882fe2e84241') }}</span>
              <div class="color-control"><input v-model="appearance.backgroundColor" :aria-label="t('m.882fe2e84241')" type="color" /><code>{{ safeBackgroundColor.toUpperCase() }}</code></div>
            </div>

            <div class="background-upload-row">
              <div class="background-preview" :style="{ backgroundColor: safeBackgroundColor }">
                <img v-if="backgroundSrc" :key="backgroundRevision" :src="backgroundSrc" :alt="t('m.ac485672fc03')" />
                <span v-else>{{ t('label.c44379727315') }}</span>
              </div>
              <div class="upload-copy"><strong>{{ t('m.99cf84163dfc') }}</strong><span>{{ t('m.b07e86c480d1') }}</span></div>
              <label class="secondary-button upload-button">
                {{ uploading === 'background' ? t('m.ce1c9d61565f') : t('m.9fbb4750bb39') }}
                <input :aria-label="t('m.276341ecaf37')" type="file" accept="image/png,image/jpeg,image/gif" :disabled="!!uploading" @change="handleImageChange('background', $event)" />
              </label>
            </div>

            <div class="form-actions"><button class="primary-button" type="submit" :disabled="busy">{{ busy ? busyLabel : t('m.200fec26bd0e') }} <span aria-hidden="true">↗</span></button></div>
          </form>
        </section>

        <section class="settings-card security-card" aria-labelledby="security-heading">
          <div class="section-heading">
            <div class="section-icon security-icon" aria-hidden="true">⌑</div>
            <div><h3 id="security-heading">{{ t('m.1b5365b35720') }}</h3><p>{{ t('m.910e765e0b31') }}</p></div>
          </div>
          <form class="password-form" @submit.prevent="changePassword">
              <label class="field"><span>{{ t('m.a114cfb687e6') }}</span><input v-model="passwordForm.currentPassword" type="password" autocomplete="current-password" required /></label>
              <div class="password-pair">
                <label class="field"><span>{{ t('m.515e9c7cf7b2') }}</span><input v-model="passwordForm.newPassword" type="password" autocomplete="new-password" required /></label>
                <label class="field"><span>{{ t('m.6fde05a916c6') }}</span><input v-model="passwordForm.confirmPassword" type="password" autocomplete="new-password" required /></label>
              </div>
            <small class="field-hint">{{ t('m.2d990ebc16e1') }}</small>
            <div class="form-actions"><button class="secondary-button" type="submit" :disabled="busy">{{ t('m.828170843089') }}</button></div>
          </form>
        </section>

        <section class="settings-card sessions-card" aria-labelledby="sessions-heading">
          <div class="section-heading session-heading">
            <div class="section-icon session-icon" aria-hidden="true">⌘</div>
            <div><h3 id="sessions-heading">{{ t('m.9fd94953b94b') }}</h3><p>{{ t('m.986f34b8224e') }}</p></div>
            <span class="session-count">{{ sessions.length }}{{ t('m.3a116815bc9d') }}</span>
          </div>
          <div v-if="sessions.length" class="session-list">
            <article v-for="session in sessions" :key="session.id" class="session-item">
              <div class="device-icon" aria-hidden="true">▣</div>
              <div class="session-info"><strong>{{ session.device || t('m.8ca01a9ba438') }}<span v-if="session.current" class="current-badge">{{ t('m.c68978537cf3') }}</span></strong><span>{{ session.remoteAddr || t('m.f4a131451fce') }}</span><small>{{ t('m.41f5cada11ed') }}{{ formatTime(session.lastSeenAt) }}</small></div>
              <button class="revoke-button" type="button" :disabled="busy || session.current" :aria-label="session.current ? t('m.62a9388a42a8') : t('m.6cfb5f2a5be8', {'0': (session.device || t('m.8ca01a9ba438'))})" @click="revokeSession(session)">{{ session.current ? t('m.cb62ebd689ee') : t('m.926a50b98ece') }}</button>
            </article>
          </div>
          <p v-else class="empty-sessions">{{ t('m.214dc45ab589') }}</p>
        </section>

        <footer class="settings-footer"><span>NodeDance Core</span><span>{{ t('m.c8f8547b3bb6') }}</span></footer>
      </div>
    </section>
    <UiDialog/>
  </main>
</template>

<style scoped>
.guest-entry{display:flex;align-items:center;justify-content:center;gap:8px;margin-top:16px}
</style>
