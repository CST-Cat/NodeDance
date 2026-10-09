<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api, type ContainerTask } from '../api'
import { composeApi, type ComposeOperation, type ComposeProject } from '../compose-api'
import ComposeEditor from './ComposeEditor.vue'

const props = defineProps<{ nodeId: string }>()

const projects = ref<ComposeProject[]>([])
const loading = ref(true)
const error = ref('')
const stale = ref(true)
const editorProjectKey = ref('')
const pending = ref<Record<string, string>>({})
const taskByProject = ref<Record<string, ContainerTask>>({})
let refreshTimer: ReturnType<typeof setInterval> | undefined
let disposed = false

const editorProject = computed(() => projects.value.find((project) => project.ref.key === editorProjectKey.value) ?? null)

async function refresh() {
  loading.value = projects.value.length === 0
  error.value = ''
  try {
    const result = await composeApi.projects(props.nodeId)
    if (disposed) return
    projects.value = result.projects
    stale.value = Boolean(result.dataStale)
  } catch (reason) {
    if (disposed) return
    stale.value = true
    error.value = reason instanceof Error ? reason.message : '无法读取 Compose 项目。'
  } finally {
    if (!disposed) loading.value = false
  }
}

function operationLabel(action: ComposeOperation) {
  return ({ start: '启动', stop: '停止', restart: '重启', deploy: '部署', config_save: '保存配置' } as const)[action]
}

function operationConfirmation(project: ComposeProject, action: ComposeOperation): string | null {
  const services = project.services.map((service) => `${service.name}（${service.instances.length} 个容器）`).join('、') || '当前无已发现服务容器'
  if (action === 'stop') return `停止 Compose 项目「${project.ref.name}」的服务？\n\n受影响服务：${services}\n运行中的服务会中断；挂载卷和数据目录不会由 stop 删除。`
  if (action === 'restart') return `重启 Compose 项目「${project.ref.name}」的所有服务？\n\n受影响服务：${services}\n服务会短暂中断，挂载卷会保留。`
  if (action === 'deploy') return `部署并强制重新创建 Compose 项目「${project.ref.name}」的服务？\n\n受影响服务：${services}\n\n请先检查配置变更、端口映射和挂载。此操作可能造成停机；Docker 卷不会被删除。`
  return null
}

function scheduleTaskPoll(projectKey: string, taskId: string) {
  const timer = setTimeout(async () => {
    try {
      const current = await api.nodeTask(props.nodeId, taskId)
      if (disposed) return
      taskByProject.value = { ...taskByProject.value, [projectKey]: current }
      if (current.status === 'queued' || current.status === 'running') {
        scheduleTaskPoll(projectKey, taskId)
      } else {
        pending.value = { ...pending.value, [projectKey]: '' }
        await refresh()
      }
    } catch (reason) {
      if (disposed) return
      pending.value = { ...pending.value, [projectKey]: '' }
      error.value = reason instanceof Error ? reason.message : '无法读取 Compose 操作结果。'
    }
  }, 800)
  timers.add(timer)
}

const timers = new Set<ReturnType<typeof setTimeout>>()

async function runAction(project: ComposeProject, action: ComposeOperation) {
  if (stale.value || project.dataStale || !project.configAvailable || pending.value[project.ref.key]) return
  const confirmation = operationConfirmation(project, action)
  if (confirmation && !window.confirm(confirmation)) return
  pending.value = { ...pending.value, [project.ref.key]: action }
  error.value = ''
  try {
    const accepted = await composeApi.createTask(props.nodeId, project.ref.key, { action }, crypto.randomUUID())
    taskByProject.value = { ...taskByProject.value, [project.ref.key]: await api.nodeTask(props.nodeId, accepted.taskId) }
    const task = taskByProject.value[project.ref.key]
    if (task.status === 'queued' || task.status === 'running') scheduleTaskPoll(project.ref.key, accepted.taskId)
    else {
      pending.value = { ...pending.value, [project.ref.key]: '' }
      await refresh()
    }
  } catch (reason) {
    pending.value = { ...pending.value, [project.ref.key]: '' }
    error.value = reason instanceof Error ? reason.message : `无法${operationLabel(action)} Compose 项目。`
  }
}

function taskMessage(task?: ContainerTask) {
  if (!task) return ''
  if (task.status === 'succeeded') return `${operationLabel(task.action as ComposeOperation)}已核实：${task.result.observedState || task.result.code}`
  if (task.status === 'failed') return `操作失败：${task.result.code || '已确认失败'}`
  if (task.status === 'unknown') return '结果待核实；不会自动重试高风险 Compose 操作。'
  return `${operationLabel(task.action as ComposeOperation)}：${task.status}`
}

onMounted(() => {
  void refresh()
  refreshTimer = setInterval(() => void refresh(), 15_000)
})

onBeforeUnmount(() => {
  disposed = true
  if (refreshTimer) clearInterval(refreshTimer)
  for (const timer of timers) clearTimeout(timer)
  timers.clear()
})
</script>

<template>
  <section class="compose-projects section-panel" aria-labelledby="compose-projects-title" data-testid="compose-projects">
    <header class="section-toolbar">
      <div><span class="eyebrow">DOCKER COMPOSE</span><h2 id="compose-projects-title">Compose 项目 · {{ nodeId }}</h2></div>
      <button class="container-action" type="button" :disabled="loading" @click="void refresh()">{{ loading ? '刷新中…' : '刷新' }}</button>
    </header>
    <p v-if="stale" class="compose-stale" role="status">Agent 离线、Docker 不可用或项目清单过期时，操作与编辑会被禁用；历史项目仍可查看。</p>
    <p v-if="error" class="compose-error" role="alert">{{ error }}</p>
    <p v-if="loading && !projects.length" class="compose-empty">正在读取真实 Docker Compose 标签和容器库存…</p>
    <p v-else-if="!projects.length" class="compose-empty">没有发现 Compose 项目。没有 Compose 文件时，关联容器仍保留在 Docker 详情中。</p>
    <article v-for="project in projects" :key="project.ref.key" class="compose-project-card" :data-stale="project.dataStale || stale">
      <header class="compose-project-heading">
        <div><span class="eyebrow">{{ project.configAvailable ? 'CONFIG AVAILABLE' : 'CONTAINERS DISCOVERED' }}</span><h3>{{ project.ref.name }}</h3><p>{{ project.ref.workingDirectory }}</p></div>
        <div class="compose-project-actions">
          <button class="container-action" type="button" :disabled="stale || project.dataStale || !project.configAvailable || !!pending[project.ref.key]" @click="void runAction(project, 'start')">启动</button>
          <button class="container-action" type="button" :disabled="stale || project.dataStale || !project.configAvailable || !!pending[project.ref.key]" @click="void runAction(project, 'stop')">停止</button>
          <button class="container-action" type="button" :disabled="stale || project.dataStale || !project.configAvailable || !!pending[project.ref.key]" @click="void runAction(project, 'restart')">重启</button>
          <button class="container-action container-action-danger" type="button" :disabled="stale || project.dataStale || !project.configAvailable || !!pending[project.ref.key]" @click="void runAction(project, 'deploy')">部署</button>
          <button class="container-action" type="button" :disabled="stale || project.dataStale || !project.configAvailable" @click="editorProjectKey = project.ref.key">编辑配置</button>
        </div>
      </header>
      <p v-if="!project.configAvailable" class="compose-warning">配置文件当前不可用。对应容器仍可在 Docker 详情中单独查看和管理。</p>
      <p v-if="pending[project.ref.key]" class="compose-task-line" role="status">正在{{ operationLabel(pending[project.ref.key] as ComposeOperation) }}…</p>
      <p v-if="taskByProject[project.ref.key]" class="compose-task-line" :data-status="taskByProject[project.ref.key].status" role="status">{{ taskMessage(taskByProject[project.ref.key]) }}</p>
      <div v-if="project.services.length" class="compose-services">
        <section v-for="service in project.services" :key="service.name" class="compose-service">
          <h4>{{ service.name }} <small>{{ service.instances.length }} 个实例</small></h4>
          <ul>
            <li v-for="instance in service.instances" :key="instance.containerId">
              <span>{{ instance.containerName }}</span><code>{{ instance.containerId.slice(0, 12) }}</code>
              <span :data-state="instance.state">{{ instance.state }}<template v-if="instance.health"> · {{ instance.health }}</template></span>
            </li>
          </ul>
        </section>
      </div>
      <p v-else class="compose-empty">当前未发现容器实例；项目源信息保留，操作仍会由 Agent 验证。</p>
    </article>
    <ComposeEditor v-if="editorProject" :node-id="nodeId" :project="editorProject" />
  </section>
</template>

<style scoped>
.compose-projects { display:grid; gap:12px; }
.compose-stale,.compose-warning { margin:0; border-left:2px solid #efb77b; padding:7px 10px; color:#efc58d; font-size:11px; line-height:1.55; }
.compose-error { margin:0; color:#ffc1b8; font-size:11px; }
.compose-empty { margin:0; color:#9aaac0; font-size:11px; line-height:1.6; }
.compose-project-card { min-width:0; border:1px solid rgba(125,165,218,.2); border-radius:10px; padding:14px; background:rgba(10,20,33,.68); }
.compose-project-card[data-stale="true"] { opacity:.82; }
.compose-project-heading { display:flex; justify-content:space-between; align-items:flex-start; gap:14px; }
.compose-project-heading h3 { margin:4px 0; font-size:15px; }
.compose-project-heading p { margin:0; color:#8194ad; font-size:10px; overflow-wrap:anywhere; }
.compose-project-actions { display:flex; flex-wrap:wrap; justify-content:flex-end; gap:6px; }
.compose-task-line { margin:10px 0 0; color:#b4cae8; font-size:11px; }
.compose-task-line[data-status="failed"],.compose-task-line[data-status="unknown"] { color:#efc58d; }
.compose-services { display:grid; gap:8px; margin-top:12px; }
.compose-service { border-top:1px solid rgba(171,196,232,.1); padding-top:8px; }
.compose-service h4 { margin:0 0 6px; color:#d7e3f4; font-size:11px; }
.compose-service h4 small { color:#8497b0; font-weight:400; }
.compose-service ul { display:grid; gap:5px; margin:0; padding:0; list-style:none; }
.compose-service li { display:grid; grid-template-columns:minmax(90px,1fr) 90px minmax(70px,auto); gap:8px; color:#bdc9da; font-size:10px; }
.compose-service li span { overflow-wrap:anywhere; }
.compose-service li code { color:#8295ae; }
.compose-service li [data-state="running"] { color:#a8d9b5; }
@media (max-width:700px) { .compose-project-heading { display:grid; } .compose-project-actions { justify-content:flex-start; } .compose-service li { grid-template-columns:minmax(70px,1fr) 75px minmax(55px,auto); } }
</style>
