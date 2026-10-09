<script setup lang="ts">
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
  if (task.status === 'queued') return '等待 Agent 接收'
  if (task.status === 'running') return task.progress.total > 0
    ? `正在执行 ${task.progress.completed.toLocaleString()} / ${task.progress.total.toLocaleString()} 字节`
    : `正在执行：${task.progress.phase || '处理中'}`
  if (task.status === 'unknown') return '结果待确认；系统不会自动重试'
  if (task.status === 'succeeded') return `已验证完成：${task.result.observedState || 'Engine 状态已确认'}`
  if (task.status === 'canceled') return '已确认取消'
  if (task.status === 'timed_out') return '超时，等待实际状态确认'
  return `操作失败：${task.result.observedState || task.result.code || 'Engine 拒绝请求'}`
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
    if (result.errorCode) throw new Error('Docker Engine 当前无法读取镜像列表。')
    images.value = Array.isArray(result.images) ? result.images : []
    total.value = result.total
  } catch (reason) {
    if (!disposed) error.value = reason instanceof Error ? reason.message : '无法读取 Docker 镜像。'
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
    if (!disposed) taskError.value = reason instanceof Error ? reason.message : '无法读取镜像操作进度。'
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
    if (!disposed) taskError.value = reason instanceof Error ? reason.message : '无法更新镜像操作状态。'
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
    }, crypto.randomUUID())
    reference.value = ''
    setActiveTask(await api.nodeTask(props.nodeId, accepted.taskId))
    notice.value = '镜像拉取任务已保存。认证信息仅用于本次请求。'
    await loadImages()
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法创建镜像拉取任务。'
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
  const confirmation = window.prompt(`删除不会删除容器数据卷。请输入完整镜像 ID 以确认：\n${image.id}`)
  if (confirmation === null) return
  if (confirmation.trim() !== image.id) {
    error.value = '删除未提交：确认内容必须与当前完整镜像 ID 完全一致。'
    return
  }
  deleting.value = { ...deleting.value, [image.id]: true }
  error.value = ''
  try {
    const accepted = await api.createImageDeleteTask(props.nodeId, image.id, crypto.randomUUID())
    setActiveTask(await api.nodeTask(props.nodeId, accepted.taskId))
    notice.value = '镜像删除任务已保存；Engine 会再次检查容器引用。'
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法创建镜像删除任务。'
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
    notice.value = '已发送取消请求，等待 Agent 停止拉取并核实镜像状态。'
  } catch (reason) {
    taskError.value = reason instanceof Error ? reason.message : '无法请求取消拉取。'
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
    if (!disposed) auditError.value = reason instanceof Error ? reason.message : '无法读取镜像任务审计记录。'
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
      <div><span class="eyebrow">DOCKER ENGINE</span><h2 id="images-title">镜像</h2></div>
      <button class="image-button" type="button" :disabled="loading" @click="loadImages">{{ loading ? '正在读取…' : '刷新' }}</button>
    </header>

    <form class="image-pull-form" @submit.prevent="submitPull">
      <label class="image-field image-reference">镜像引用<input v-model="reference" name="imageReference" autocomplete="off" placeholder="例如 registry.example/app:1.2.3" required></label>
      <label class="image-field">Registry 用户名<input v-model="username" name="registryUsername" autocomplete="off" placeholder="可留空"></label>
      <label class="image-field">Registry 密码<input v-model="password" name="registryPassword" type="password" autocomplete="new-password" placeholder="仅用于本次拉取"></label>
      <button class="image-button primary" type="submit" :disabled="submitting || !reference.trim()">{{ submitting ? '正在提交…' : '拉取镜像' }}</button>
    </form>
    <p class="image-hint">认证信息不会显示在任务、审计或错误消息中；提交后会立即清空表单。</p>

    <div class="image-filter-row">
      <label class="image-field">筛选镜像<input v-model="filterInput" type="search" placeholder="镜像 ID、标签或 digest" @keydown.enter.prevent="applyFilter"></label>
      <button class="image-button" type="button" @click="applyFilter">筛选</button>
      <span class="image-total">共 {{ total.toLocaleString() }} 个镜像</span>
    </div>

    <p v-if="error" class="image-message error" role="alert">{{ error }}</p>
    <p v-if="notice" class="image-message" role="status">{{ notice }}</p>
    <p v-if="taskError" class="image-message error" role="alert">{{ taskError }}</p>

    <article v-if="activeTask" class="image-task" :data-status="activeTask.status" aria-live="polite">
      <div><strong>{{ activeTask.action === 'image_pull' ? '镜像拉取' : '镜像删除' }} · {{ activeTask.status }}</strong><span>{{ taskStateLabel(activeTask) }}</span></div>
      <button v-if="activeTask.action === 'image_pull' && activeTask.status === 'running'" class="image-button" type="button" :disabled="cancelPending" @click="cancelPull">{{ cancelPending ? '正在取消…' : '取消拉取' }}</button>
      <button class="image-button" type="button" @click="toggleAudit">{{ auditVisible ? '隐藏审计' : '查看审计' }}</button>
    </article>
    <div v-if="activeTask && auditVisible" class="image-audit" aria-label="镜像任务审计">
      <p v-if="auditLoading">正在读取审计记录…</p>
      <p v-else-if="auditError" class="error" role="alert">{{ auditError }}</p>
      <ol v-else>
        <li v-for="event in auditEvents" :key="event.id">
          <span>{{ event.event }}<template v-if="event.toStatus"> · {{ event.fromStatus || '开始' }} → {{ event.toStatus }}</template></span>
          <time :datetime="event.occurredAt">{{ new Date(event.occurredAt).toLocaleString() }}</time>
        </li>
      </ol>
    </div>

    <div v-if="images.length" class="image-list">
      <article v-for="image in images" :key="image.id" class="image-row">
        <div class="image-row-head"><strong>{{ image.tags.length ? image.tags.join(', ') : '<无标签>' }}</strong><span>{{ formatSize(image.size) }}</span></div>
        <div class="image-row-meta">
          <span class="image-id">{{ image.id }}</span>
          <span>{{ image.containers }} 个容器引用</span>
          <span v-if="image.createdAt">创建于 {{ new Date(image.createdAt * 1000).toLocaleString() }}</span>
        </div>
        <p v-if="image.digests.length" class="image-digests">{{ image.digests.join(' · ') }}</p>
        <button class="image-button danger" type="button" :disabled="image.containers > 0 || deleting[image.id]" :title="image.containers > 0 ? '仍有容器引用此镜像' : '删除前会再次检查容器引用'" @click="submitDelete(image)">
          {{ deleting[image.id] ? '正在提交…' : image.containers > 0 ? '使用中' : '删除镜像' }}
        </button>
      </article>
    </div>
    <p v-else-if="!loading && !error" class="images-empty">{{ filter ? '没有匹配的镜像。' : '此节点当前没有镜像。' }}</p>

    <nav class="image-pagination" aria-label="镜像分页">
      <button class="image-button" type="button" :disabled="page === 0 || loading" @click="movePage(-1)">上一页</button>
      <span>第 {{ page + 1 }} / {{ pageCount }} 页</span>
      <button class="image-button" type="button" :disabled="page + 1 >= pageCount || loading" @click="movePage(1)">下一页</button>
    </nav>
  </section>
</template>

<style scoped>
.images-panel { margin-top: 18px; border: 1px solid rgba(171,196,232,.13); border-radius: 12px; padding: clamp(14px,2vw,20px); background: rgba(9,17,29,.46); color: #eaf0fa; }
.images-heading, .image-row-head, .image-pagination, .image-task { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.images-heading { margin-bottom: 14px; }
.images-heading h2 { margin: 4px 0 0; font-size: 17px; }
.image-pull-form { display: grid; grid-template-columns: minmax(180px,2fr) repeat(2,minmax(140px,1fr)) auto; align-items: end; gap: 9px; }
.image-field { display: grid; min-width: 0; gap: 5px; color: #aab9ce; font-size: 10px; }
.image-field input { min-width: 0; min-height: 36px; border: 1px solid rgba(171,196,232,.17); border-radius: 7px; padding: 7px 9px; color: #eaf0fa; background: rgba(18,29,45,.85); font: inherit; font-size: 11px; }
.image-button { min-height: 34px; border: 1px solid rgba(141,201,255,.25); border-radius: 7px; padding: 6px 10px; color: #cce6ff; background: rgba(62,119,170,.16); font: inherit; font-size: 10px; cursor: pointer; white-space: nowrap; }
.image-button.primary { color: #e5f4ff; background: rgba(55,131,196,.32); }
.image-button.danger { color: #ffc1b8; border-color: rgba(255,129,116,.25); background: rgba(184,77,72,.1); }
.image-button:disabled { opacity: .45; cursor: not-allowed; }
.image-hint, .images-empty { color: #93a4bb; font-size: 10px; line-height: 1.6; }
.image-filter-row { display: flex; align-items: end; gap: 9px; margin: 15px 0 12px; }
.image-filter-row .image-field { flex: 1; }
.image-total { padding: 8px 0; color: #93a4bb; font-size: 10px; white-space: nowrap; }
.image-message { border: 1px solid rgba(121,214,156,.2); border-radius: 7px; padding: 8px 10px; color: #a8e4bb; background: rgba(80,186,119,.08); font-size: 10px; overflow-wrap: anywhere; }
.image-message.error { color: #ffc1b8; border-color: rgba(255,129,116,.24); background: rgba(184,77,72,.1); }
.image-task { border: 1px solid rgba(141,201,255,.2); border-radius: 8px; padding: 10px; color: #cce6ff; background: rgba(62,119,170,.1); font-size: 10px; }
.image-task > div { display: grid; gap: 4px; min-width: 0; }
.image-task span { color: #aab9ce; overflow-wrap: anywhere; }
.image-audit { border: 1px solid rgba(171,196,232,.1); border-radius: 8px; padding: 8px 10px; color: #aab9ce; font-size: 9px; }
.image-audit ol { display: grid; gap: 6px; margin: 0; padding-left: 18px; }
.image-audit li { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 5px 12px; overflow-wrap: anywhere; }
.image-audit time { color: #8497b0; }
.image-audit .error { color: #ffc1b8; }
.image-list { display: grid; gap: 8px; }
.image-row { display: grid; gap: 7px; min-width: 0; border: 1px solid rgba(171,196,232,.1); border-radius: 8px; padding: 11px; background: rgba(18,29,45,.68); }
.image-row-head { align-items: start; color: #e4edf9; font-size: 11px; }
.image-row-head strong { min-width: 0; overflow-wrap: anywhere; }
.image-row-head > span { color: #aab9ce; white-space: nowrap; }
.image-row-meta { display: flex; flex-wrap: wrap; gap: 5px 12px; color: #aab9ce; font-size: 9px; }
.image-id, .image-digests { overflow-wrap: anywhere; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.image-digests { margin: 0; color: #89bbec; font-size: 9px; }
.image-row > .image-button { justify-self: start; }
.image-pagination { justify-content: center; margin-top: 13px; color: #93a4bb; font-size: 10px; }
@media (max-width: 900px) { .image-pull-form { grid-template-columns: repeat(2,minmax(0,1fr)); } .image-reference { grid-column: 1 / -1; } }
@media (max-width: 520px) { .image-pull-form { grid-template-columns: 1fr; } .image-reference { grid-column: auto; } .image-filter-row { flex-wrap: wrap; } .image-filter-row .image-field { flex-basis: 100%; } .image-total { margin-left: auto; } .image-task { align-items: start; flex-direction: column; } }
</style>
