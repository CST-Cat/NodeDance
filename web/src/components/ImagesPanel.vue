<script setup lang="ts">
import { requestPrompt } from '../dialogs'
import { t, display, locale } from "../i18n"
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api, type ContainerTask, type DockerImage, type TaskAuditEvent } from '../api'

const props = defineProps<{ nodeId: string }>()
const images = ref<DockerImage[]>([])
const total = ref(0)
const page = ref(0)
const filterInput = ref('')
const filter = ref('')
const reference = ref('')
const username = ref('')
const password = ref('')
const loading = ref(false)
const submitting = ref(false)
const deleting = ref<Record<string, boolean>>({})
const error = ref('')
const notice = ref('')
const activeTask = ref<ContainerTask>()
const cancelPending = ref(false)
const auditEvents = ref<TaskAuditEvent[]>([])
const auditVisible = ref(false)
const auditLoading = ref(false)
const auditError = ref('')
const taskError = ref('')
const pageCount = computed(() => Math.max(1, Math.ceil(total.value / 50)))
let pollTimer: ReturnType<typeof setInterval> | undefined
let disposed = false

function taskIsActive(task?: ContainerTask): boolean {
  return Boolean(task && ['queued', 'running', 'unknown'].includes(task.status))
}

function setActiveTask(task: ContainerTask) {
  if (activeTask.value?.taskId !== task.taskId) {
    cancelPending.value = false
    auditEvents.value = []
    auditVisible.value = false
    auditError.value = ''
  }
  activeTask.value = task
}

function taskStateLabel(task: ContainerTask): string {
  if (task.status === 'queued') return t('m.165cd916f553')
  if (task.status === 'running') return task.progress.total > 0
    ? t('m.f4854b2456e3', {"0": (task.progress.completed.toLocaleString(locale.value)), "1": (task.progress.total.toLocaleString(locale.value))})
    : t('m.472b61dd983a', {"0": (task.progress.phase || t('m.694b71bc8013'))})
  if (task.status === 'unknown') return t('m.e04f5d26341a')
  if (task.status === 'succeeded') return t('m.52a8bda11dff', {"0": (task.result.observedState || t('m.5b67afa89eb2'))})
  if (task.status === 'canceled') return t('m.d2fbf2ec8c2d')
  if (task.status === 'timed_out') return t('m.7112c0bda6de')
  return t('m.5bc4c4816eeb', {"0": (task.result.observedState || task.result.code || t('m.d885659ddd84'))})
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let value = bytes
  let unit = -1
  do { value /= 1024; unit++ } while (value >= 1024 && unit < units.length - 1)
  return `${value.toFixed(value >= 100 ? 0 : value >= 10 ? 1 : 2)} ${units[unit]}`
}

async function loadImages() {
  if (loading.value || disposed) return
  loading.value = true
  error.value = ''
  try {
    const result = await api.nodeImages(props.nodeId, filter.value, page.value)
    if (disposed) return
    if (result.errorCode) throw new Error(t('m.159467f4355b'))
    images.value = Array.isArray(result.images) ? result.images : []
    total.value = result.total
  } catch (reason) {
    if (!disposed) error.value = reason instanceof Error ? reason.message : t('m.e7515c35320f')
  } finally {
    loading.value = false
  }
}

async function loadLatestImageTask() {
  try {
    const result = await api.nodeTasks(props.nodeId)
    if (disposed) return
    const latest = result.tasks
      .filter((task) => task.action === 'image_pull' || task.action === 'image_delete')
      .sort((left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt))[0]
    if (latest) {
      setActiveTask(latest)
    }
  } catch (reason) {
    if (!disposed) taskError.value = reason instanceof Error ? reason.message : t('m.77293f47bbd9')
  }
}

async function refreshTask() {
  if (!activeTask.value || disposed) return
  try {
    const next = await api.nodeTask(props.nodeId, activeTask.value.taskId)
    if (disposed) return
    const wasActive = taskIsActive(activeTask.value)
    setActiveTask(next)
    if (wasActive && !taskIsActive(next)) {
      cancelPending.value = false
      await loadImages()
    }
  } catch (reason) {
    if (!disposed) taskError.value = reason instanceof Error ? reason.message : t('m.cac85b60b94d')
  }
}

async function submitPull() {
  const imageReference = reference.value.trim()
  if (!imageReference || submitting.value) return
  submitting.value = true
  error.value = ''
  notice.value = ''
  taskError.value = ''
  const auth = { username: username.value, password: password.value }
  username.value = ''
  password.value = ''
  try {
    const accepted = await api.createImagePullTask(props.nodeId, {
      imageReference,
      ...(auth.username || auth.password ? { username: auth.username, password: auth.password } : {}),
    })
    reference.value = ''
    setActiveTask(await api.nodeTask(props.nodeId, accepted.taskId))
    notice.value = t('m.ddaed4324a4e')
    await loadImages()
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : t('m.c831f021d9c3')
  } finally {
    auth.username = ''
    auth.password = ''
    username.value = ''
    password.value = ''
    submitting.value = false
  }
}

async function submitDelete(image: DockerImage) {
  if (image.containers > 0 || deleting.value[image.id]) return
  const confirmation = await requestPrompt(t('m.90829b35e800', {"0": (image.id)}))
  if (confirmation === null) return
  if (confirmation.trim() !== image.id) {
    error.value = t('m.b64864a55a31')
    return
  }
  deleting.value = { ...deleting.value, [image.id]: true }
  error.value = ''
  try {
    const accepted = await api.createImageDeleteTask(props.nodeId, image.id)
    setActiveTask(await api.nodeTask(props.nodeId, accepted.taskId))
    notice.value = t('m.3ea800a3051c')
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : t('m.2cd1ecc3647f')
  } finally {
    deleting.value = { ...deleting.value, [image.id]: false }
  }
}

async function cancelPull() {
  const task = activeTask.value
  if (!task || task.action !== 'image_pull' || task.status !== 'running') return
  taskError.value = ''
  try {
    setActiveTask(await api.cancelNodeTask(props.nodeId, task.taskId))
    cancelPending.value = true
    notice.value = t('m.0469be03574e')
  } catch (reason) {
    taskError.value = reason instanceof Error ? reason.message : t('m.c32c64a98d10')
  }
}

async function toggleAudit() {
  if (!activeTask.value) return
  auditVisible.value = !auditVisible.value
  if (!auditVisible.value || auditEvents.value.length || auditLoading.value) return
  auditLoading.value = true
  auditError.value = ''
  try {
    const result = await api.nodeTaskAudit(props.nodeId, activeTask.value.taskId)
    if (!disposed) auditEvents.value = result.events
  } catch (reason) {
    if (!disposed) auditError.value = reason instanceof Error ? reason.message : t('m.74c2941dde99')
  } finally {
    auditLoading.value = false
  }
}

function applyFilter() {
  page.value = 0
  filter.value = filterInput.value.trim()
  void loadImages()
}

function movePage(offset: number) {
  const next = page.value + offset
  if (next < 0 || next >= pageCount.value) return
  page.value = next
  void loadImages()
}

onMounted(() => {
  void Promise.all([loadImages(), loadLatestImageTask()])
  pollTimer = setInterval(() => {
    if (taskIsActive(activeTask.value)) void refreshTask()
    else void loadLatestImageTask()
  }, 1500)
})

onBeforeUnmount(() => {
  disposed = true
  if (pollTimer !== undefined) clearInterval(pollTimer)
  reference.value = ''
  username.value = ''
  password.value = ''
})
</script>

<template>
  <section class="images-panel" aria-labelledby="images-title" data-testid="docker-images">
    <header class="images-heading">
      <div><span class="eyebrow">{{ t('label.525f325a82b5') }}</span><h2 id="images-title">{{ t('m.176c09844e0f') }}</h2></div>
      <button class="image-button" type="button" :disabled="loading" @click="loadImages">{{ loading ? t('m.86b6d0d63062') : t('m.aee887434131') }}</button>
    </header>

    <form class="image-pull-form" @submit.prevent="submitPull">
      <label class="image-field image-reference">{{ t('m.1357b2ae4279') }}<input v-model="reference" name="imageReference" autocomplete="off" :placeholder="t('m.15c4c6755fbe')" required></label>
      <label class="image-field">{{ t('m.4867bdab7b06') }}<input v-model="username" name="registryUsername" autocomplete="off" :placeholder="t('m.293b74110f45')"></label>
      <label class="image-field">{{ t('m.8b7addffb5c2') }}<input v-model="password" name="registryPassword" type="password" autocomplete="new-password" :placeholder="t('m.51dccb462e09')"></label>
      <button class="image-button primary" type="submit" :disabled="submitting || !reference.trim()">{{ submitting ? t('m.26ef00b7d061') : t('m.2b0005604564') }}</button>
    </form>
    <p class="image-hint">{{ t('m.87bb077c4318') }}</p>

    <div class="image-filter-row">
      <label class="image-field">{{ t('m.8a6ab3b06580') }}<input v-model="filterInput" type="search" :placeholder="t('m.f7a89a15336a')" @keydown.enter.prevent="applyFilter"></label>
      <button class="image-button" type="button" @click="applyFilter">{{ t('m.b5f15473fdc0') }}</button>
      <span class="image-total">{{ t('m.76e547a8fa54') }}{{ total.toLocaleString(locale) }}{{ t('m.51a215db46d9') }}</span>
    </div>

    <p v-if="error" class="image-message error" role="alert">{{ display(error) }}</p>
    <p v-if="notice" class="image-message" role="status">{{ display(notice) }}</p>
    <p v-if="taskError" class="image-message error" role="alert">{{ display(taskError) }}</p>

    <article v-if="activeTask" class="image-task" :data-status="activeTask.status" aria-live="polite">
      <div><strong>{{ activeTask.action === 'image_pull' ? t('m.8d6e8b5c37c8') : t('m.a1d1a5cd5594') }} · {{ activeTask.status }}</strong><span>{{ taskStateLabel(activeTask) }}</span></div>
      <button v-if="activeTask.action === 'image_pull' && activeTask.status === 'running'" class="image-button" type="button" :disabled="cancelPending" @click="cancelPull">{{ cancelPending ? t('m.e8e08b0f61dd') : t('m.a7d9cc0d2cb9') }}</button>
      <button class="image-button" type="button" @click="toggleAudit">{{ auditVisible ? t('m.54e74c1e7504') : t('m.97f395dec8ce') }}</button>
    </article>
    <div v-if="activeTask && auditVisible" class="image-audit" :aria-label="t('m.30bf21ac5f41')">
      <p v-if="auditLoading">{{ t('m.cf62890bdb61') }}</p>
      <p v-else-if="auditError" class="error" role="alert">{{ display(auditError) }}</p>
      <ol v-else>
        <li v-for="event in auditEvents" :key="event.id">
          <span>{{ event.event }}<template v-if="event.toStatus"> · {{ event.fromStatus || t('m.d2bb025a2e51') }} → {{ event.toStatus }}</template></span>
          <time :datetime="event.occurredAt">{{ new Date(event.occurredAt).toLocaleString(locale) }}</time>
        </li>
      </ol>
    </div>

    <div v-if="images.length" class="image-list">
      <article v-for="image in images" :key="image.id" class="image-row">
        <div class="image-row-head"><strong>{{ image.tags.length ? image.tags.join(', ') : t('m.247781b10fdf') }}</strong><span>{{ formatSize(image.size) }}</span></div>
        <div class="image-row-meta">
          <span class="image-id">{{ image.id }}</span>
          <span>{{ image.containers }}{{ t('m.7a5eafabc37f') }}</span>
          <span v-if="image.createdAt">{{ t('m.dc4d662d071e') }}{{ new Date(image.createdAt * 1000).toLocaleString(locale) }}</span>
        </div>
        <p v-if="image.digests.length" class="image-digests">{{ image.digests.join(' · ') }}</p>
        <button class="image-button danger" type="button" :disabled="image.containers > 0 || deleting[image.id]" :title="image.containers > 0 ? t('m.1e695283a66a') : t('m.62f417a3db56')" @click="submitDelete(image)">
          {{ deleting[image.id] ? t('m.26ef00b7d061') : image.containers > 0 ? t('m.fa48e8938940') : t('m.dc61b6b9b500') }}
        </button>
      </article>
    </div>
    <p v-else-if="!loading && !error" class="images-empty">{{ filter ? t('m.bf11e3fed6b2') : t('m.715c063e56ec') }}</p>

    <nav class="image-pagination" :aria-label="t('m.1fa7b5f8794e')">
      <button class="image-button" type="button" :disabled="page === 0 || loading" @click="movePage(-1)">{{ t('m.c9b9ae7a6144') }}</button>
      <span>{{ t('m.af50a0fe2a50') }}{{ page + 1 }} / {{ pageCount }}{{ t('m.d24d3c99460c') }}</span>
      <button class="image-button" type="button" :disabled="page + 1 >= pageCount || loading" @click="movePage(1)">{{ t('m.8a8542f69648') }}</button>
    </nav>
  </section>
</template>

<style scoped>
.images-panel {margin-top: 18px;     color: var(--text); border:0;border-radius:0;background:transparent;box-shadow:none;padding:16px 0;}
.images-heading, .image-row-head, .image-pagination, .image-task { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.images-heading { margin-bottom: 14px; }
.images-heading h2 { margin: 4px 0 0; font-size: 17px; }
.image-pull-form { display: grid; grid-template-columns: minmax(180px,420px) repeat(2,minmax(140px,220px)) max-content; align-items: end; gap: 9px; }
.image-field { display: grid; min-width: 0; gap: 5px; color: var(--muted-strong); font-size: 12px; }
.image-field input { min-width: 0; min-height:40px; border: 1px solid var(--line); border-radius:var(--radius-control); padding: 7px 9px; color: var(--text); background: var(--surface-soft); font: inherit; font-size: 12px; }
.image-button { min-height:40px; border: 1px solid var(--line); border-radius:var(--radius-control); padding: 6px 10px; color: var(--accent); background: var(--line); font: inherit; font-size: 12px; cursor: pointer; white-space: nowrap; }
.image-button.primary { color: var(--text); background: var(--line); }
.image-button.danger { color: var(--danger); border-color: var(--danger-line); background: var(--danger-soft); }
.image-button:disabled { opacity: .45; cursor: not-allowed; }
.image-hint, .images-empty { color: var(--muted); font-size: 12px; line-height: 1.6; }
.image-filter-row { display: flex; align-items: end; gap: 9px; margin: 15px 0 12px; }
.image-filter-row .image-field { flex: 0 1 360px; }
.image-total { padding: 8px 0; color: var(--muted); font-size: 12px; white-space: nowrap; }
.image-message { border: 1px solid var(--success-line); border-radius:var(--radius-card); padding: 8px 10px; color: var(--success); background: var(--success-soft); font-size: 12px; overflow-wrap: anywhere; }
.image-message.error { color: var(--danger); border-color: var(--danger-line); background: var(--danger-soft); }
.image-task { border: 1px solid var(--line); border-radius:var(--radius-card); padding: 10px; color: var(--accent); background: var(--line); font-size: 12px; }
.image-task > div { display: grid; gap: 4px; min-width: 0; }
.image-task span { color: var(--muted-strong); overflow-wrap: anywhere; }
.image-audit { border: 1px solid var(--line); border-radius:var(--radius-card); padding: 8px 10px; color: var(--muted-strong); font-size: 12px; }
.image-audit ol { display: grid; gap: 6px; margin: 0; padding-left: 18px; }
.image-audit li { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 5px 12px; overflow-wrap: anywhere; }
.image-audit time { color: var(--muted); }
.image-audit .error { color: var(--danger); }
.image-list {display: grid;  gap:0;}
.image-row {display: grid; gap: 7px; min-width: 0;     border:0;border-radius:0;background:transparent;box-shadow:none;padding:16px 0;border-top:1px solid var(--line);}
.image-row-head { align-items: start; color: var(--text); font-size: 12px; }
.image-row-head strong { min-width: 0; overflow-wrap: anywhere; }
.image-row-head > span { color: var(--muted-strong); white-space: nowrap; }
.image-row-meta { display: flex; flex-wrap: wrap; gap: 5px 12px; color: var(--muted-strong); font-size: 12px; }
.image-id, .image-digests { overflow-wrap: anywhere; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.image-digests { margin: 0; color: var(--accent); font-size: 12px; }
.image-row > .image-button { justify-self: start; }
.image-pagination { justify-content: center; margin-top: 13px; color: var(--muted); font-size: 12px; }
@media (max-width: 900px) { .image-pull-form { grid-template-columns: repeat(2,minmax(0,1fr)); } .image-reference { grid-column: 1 / -1; } }
@media (max-width: 520px) { .image-pull-form { grid-template-columns: 1fr; } .image-reference { grid-column: auto; } .image-filter-row { flex-wrap: wrap; } .image-filter-row .image-field { flex-basis: 100%; } .image-total { margin-left: auto; } .image-task { align-items: start; flex-direction: column; } }
</style>
