<script setup lang="ts">
import { requestConfirm } from '../dialogs'
import { t, display, locale } from "../i18n"
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api, type ContainerTask } from '../api'
import { composeApi, type ComposeOperation, type ComposeProject } from '../compose-api'
import ComposeEditor from './ComposeEditor.vue'
import ComposeCreateForm from './ComposeCreateForm.vue'

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
    error.value = reason instanceof Error ? reason.message : t('m.777bbe3013b4')
  } finally {
    if (!disposed) loading.value = false
  }
}

function operationLabel(action: ComposeOperation) {
  return ({ start: t('m.56410fc65314'), stop: t('m.ca4d973c0b00'), restart: t('m.562822892865'), deploy: t('m.3ca7718593c0'), config_save: t('m.6e584e3d5ce6') } as const)[action]
}

function operationConfirmation(project: ComposeProject, action: ComposeOperation): string | null {
  const services = project.services.map((service) => t('m.6d88c1288a7f', {"0": (service.name), "1": (service.instances.length)})).join('、') || t('m.2681214710d2')
  if (action === 'stop') return t('m.4525ce3335ca', {"0": (project.ref.name), "1": (services)})
  if (action === 'restart') return t('m.3c8411939878', {"0": (project.ref.name), "1": (services)})
  if (action === 'deploy') return t('m.bbff0417eee6', {"0": (project.ref.name), "1": (services)})
  return null
}

function scheduleTaskPoll(projectKey: string, taskId: string) {
  const timer = setTimeout(async () => {
    timers.delete(timer)
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
      error.value = reason instanceof Error ? reason.message : t('m.54c881f3fc7c')
    }
  }, 800)
  timers.add(timer)
}

const timers = new Set<ReturnType<typeof setTimeout>>()

async function runAction(project: ComposeProject, action: ComposeOperation) {
  if (stale.value || project.dataStale || !project.configAvailable || pending.value[project.ref.key]) return
  const confirmation = operationConfirmation(project, action)
  if (confirmation && !await requestConfirm(confirmation)) return
  pending.value = { ...pending.value, [project.ref.key]: action }
  error.value = ''
  try {
    const accepted = await composeApi.createTask(props.nodeId, project.ref.key, { action })
    taskByProject.value = { ...taskByProject.value, [project.ref.key]: await api.nodeTask(props.nodeId, accepted.taskId) }
    const task = taskByProject.value[project.ref.key]
    if (task.status === 'queued' || task.status === 'running') scheduleTaskPoll(project.ref.key, accepted.taskId)
    else {
      pending.value = { ...pending.value, [project.ref.key]: '' }
      await refresh()
    }
  } catch (reason) {
    pending.value = { ...pending.value, [project.ref.key]: '' }
    error.value = reason instanceof Error ? reason.message : t('m.96556b2e2aac', {"0": (operationLabel(action))})
  }
}

function taskMessage(task?: ContainerTask) {
  if (!task) return ''
  if (task.status === 'succeeded') return t('m.10e0cfbae303', {"0": (operationLabel(task.action as ComposeOperation)), "1": (task.result.observedState || task.result.code)})
  if (task.status === 'failed') {
    if (task.action === 'compose_deploy' && ['recovered', 'recreated', 'restored_offline'].includes(task.result.observedState || '')) {
      const restored = task.result.observedState === 'restored_offline' ? t('m.74e19906ba04') : t('m.a40db8641914')
      return t('m.3c54e7a0bc54', {"0": (task.result.resourceRevision || task.result.code), "1": (restored)})
    }
    return t('m.5bc4c4816eeb', {"0": (task.result.code || t('m.f50ce21ae910'))})
  }
  if (task.status === 'unknown') {
    if (task.result.observedState === 'recovery_required') {
      return t('m.29dc41c6f776', {"0": (task.result.observedState), "1": (task.result.resourceRevision || t('m.fb2425b0c8e1'))})
    }
    return t('m.d004f1dc1829')
  }
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
      <div><span class="eyebrow">{{ t('label.135e86eb8ca8') }}</span><h2 id="compose-projects-title">{{ t('m.27852c17e6fc') }}{{ nodeId }}</h2></div>
      <button class="container-action" type="button" :disabled="loading" @click="void refresh()">{{ loading ? t('m.7f3c8312816f') : t('m.aee887434131') }}</button>
    </header>
    <p v-if="stale" class="compose-stale" role="status">{{ t('m.11a3711c480d') }}</p>
    <p v-if="error" class="compose-error" role="alert">{{ display(error) }}</p>
    <ComposeCreateForm :node-id="nodeId" :disabled="stale" @created="void refresh()" />
    <p v-if="loading && !projects.length" class="compose-empty">{{ t('m.6025e55b6f68') }}</p>
    <p v-else-if="!projects.length" class="compose-empty">{{ t('m.55f37d0877f7') }}</p>
    <article v-for="project in projects" :key="project.ref.key" class="compose-project-card" :data-stale="project.dataStale || stale">
      <header class="compose-project-heading">
        <div><span class="eyebrow">{{ project.configAvailable ? 'CONFIG AVAILABLE' : 'CONTAINERS DISCOVERED' }}</span><h3>{{ project.ref.name }}</h3><p>{{ project.ref.workingDirectory }}</p></div>
        <div class="compose-project-actions">
          <button class="container-action" type="button" :disabled="stale || project.dataStale || !project.configAvailable || !!pending[project.ref.key]" @click="void runAction(project, 'start')">{{ t('m.56410fc65314') }}</button>
          <button class="container-action" type="button" :disabled="stale || project.dataStale || !project.configAvailable || !!pending[project.ref.key]" @click="void runAction(project, 'stop')">{{ t('m.ca4d973c0b00') }}</button>
          <button class="container-action" type="button" :disabled="stale || project.dataStale || !project.configAvailable || !!pending[project.ref.key]" @click="void runAction(project, 'restart')">{{ t('m.562822892865') }}</button>
          <button class="container-action container-action-danger" type="button" :disabled="stale || project.dataStale || !project.configAvailable || !!pending[project.ref.key]" @click="void runAction(project, 'deploy')">{{ t('m.3ca7718593c0') }}</button>
          <button class="container-action" type="button" :disabled="stale || project.dataStale || !project.configAvailable" @click="editorProjectKey = project.ref.key">{{ t('m.b2b5b9fafa31') }}</button>
        </div>
      </header>
      <p v-if="!project.configAvailable" class="compose-warning">{{ t('m.083c34e37d43') }}</p>
      <p v-if="pending[project.ref.key]" class="compose-task-line" role="status">{{ t('m.e6b02a08e69a') }}{{ operationLabel(pending[project.ref.key] as ComposeOperation) }}…</p>
      <p v-if="taskByProject[project.ref.key]" class="compose-task-line" :data-status="taskByProject[project.ref.key].status" role="status">{{ display(taskMessage(taskByProject[project.ref.key])) }}</p>
      <div v-if="project.services.length" class="compose-services">
        <section v-for="service in project.services" :key="service.name" class="compose-service">
          <h4>{{ service.name }} <small>{{ service.instances.length }}{{ t('m.3cbb1fb681cc') }}</small></h4>
          <ul>
            <li v-for="instance in service.instances" :key="instance.containerId">
              <span>{{ instance.containerName }}</span><code>{{ instance.containerId.slice(0, 12) }}</code>
              <span :data-state="instance.state">{{ instance.state }}<template v-if="instance.health"> · {{ instance.health }}</template></span>
            </li>
          </ul>
        </section>
      </div>
      <p v-else class="compose-empty">{{ t('m.831759cffa59') }}</p>
    </article>
    <ComposeEditor v-if="editorProject" :key="nodeId + ':' + editorProject.ref.key" :node-id="nodeId" :project="editorProject" />
  </section>
</template>

<style scoped>
.compose-projects { display:grid; gap:12px; }
.compose-stale,.compose-warning { margin:0; border-left:2px solid var(--warning); padding:7px 10px; color:var(--warning); font-size: 12px; line-height:1.55; }
.compose-error { margin:0; color:var(--danger); font-size: 12px; }
.compose-empty { margin:0; color:var(--muted); font-size: 12px; line-height:1.6; }
.compose-project-card { min-width:0; border:1px solid var(--line); border-radius:var(--radius-card); padding:14px; background:var(--surface-soft); }
.compose-project-card[data-stale="true"] { opacity:.82; }
.compose-project-heading { display:flex; justify-content:space-between; align-items:flex-start; gap:14px; }
.compose-project-heading h3 { margin:4px 0; font-size: 15px; }
.compose-project-heading p { margin:0; color:var(--muted); font-size: 12px; overflow-wrap:anywhere; }
.compose-project-actions { display:flex; flex-wrap:wrap; justify-content:flex-end; gap:6px; }
.compose-task-line { margin:10px 0 0; color:var(--accent); font-size: 12px; }
.compose-task-line[data-status="failed"],.compose-task-line[data-status="unknown"] { color:var(--warning); }
.compose-services { display:grid; gap:8px; margin-top:12px; }
.compose-service { border-top:1px solid var(--line); padding-top:8px; }
.compose-service h4 { margin:0 0 6px; color:var(--text); font-size: 12px; }
.compose-service h4 small { color:var(--muted); font-weight:400; }
.compose-service ul { display:grid; gap:5px; margin:0; padding:0; list-style:none; }
.compose-service li { display:grid; grid-template-columns:minmax(90px,1fr) 90px minmax(70px,auto); gap:8px; color:var(--muted-strong); font-size: 12px; }
.compose-service li span { overflow-wrap:anywhere; }
.compose-service li code { color:var(--muted); }
.compose-service li [data-state="running"] { color:var(--success); }
@media (max-width:700px) { .compose-project-heading { display:grid; } .compose-project-actions { justify-content:flex-start; } .compose-service li { grid-template-columns:minmax(70px,1fr) 75px minmax(55px,auto); } }
</style>
