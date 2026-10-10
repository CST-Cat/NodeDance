<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { composeApi } from '../compose-api'
import type { ContainerTask } from '../api'

const props = defineProps<{ nodeId: string; disabled: boolean }>()
const emit = defineEmits<{ created: [task: ContainerTask] }>()

const name = ref('')
const workingDirectory = ref('')
const content = ref('')
const submitting = ref(false)
const error = ref('')
const message = ref('')
const task = ref<ContainerTask>()
let pollTimer: ReturnType<typeof setTimeout> | undefined
let disposed = false

function validInput() {
  return /^[a-z0-9][a-z0-9_-]{0,62}$/.test(name.value) && workingDirectory.value.startsWith('/') &&
    workingDirectory.value !== '/' && !workingDirectory.value.includes('\0') &&
    !workingDirectory.value.split('/').some((part) => part === '..' || part === '.') &&
    content.value.trim().length > 0 && new TextEncoder().encode(content.value).length <= 128 * 1024
}

function taskMessage(value: ContainerTask) {
  if (value.status === 'succeeded') return 'Compose up -d 已完成，并通过 Docker Engine 核实容器状态。'
  if (value.status === 'failed') return `创建失败：${value.result.code || '已确认失败'}`
  if (value.status === 'unknown' || value.status === 'timed_out') return '创建结果待核实；请先刷新项目清单和 Docker 容器，不要重复提交。'
  if (value.status === 'canceled') return '创建任务已取消，Agent 未确认部署结果。'
  return `创建任务：${value.status}`
}

function poll(taskId: string) {
  pollTimer = setTimeout(async () => {
    try {
      const latest = await composeApi.task(props.nodeId, taskId)
      if (disposed) return
      task.value = latest
      message.value = taskMessage(latest)
      if (latest.status === 'queued' || latest.status === 'running') {
        poll(taskId)
        return
      }
      submitting.value = false
      if (latest.status === 'succeeded') emit('created', latest)
    } catch (reason) {
      if (disposed) return
      submitting.value = false
      error.value = reason instanceof Error ? reason.message : '无法读取 Compose 创建任务状态。'
    }
  }, 750)
}

async function createProject() {
  if (props.disabled || submitting.value || !validInput()) return
  if (!window.confirm(`在节点 ${props.nodeId} 上为 Compose 项目“${name.value}”保存 compose.yaml 并执行 docker compose up -d？\n\nAgent 只会使用给定工作目录；已存在的 compose.yaml 不会被覆盖。`)) return
  submitting.value = true
  error.value = ''
  message.value = ''
  task.value = undefined
  try {
    const accepted = await composeApi.createProject(props.nodeId, {
      name: name.value,
      workingDirectory: workingDirectory.value,
      content: content.value,
    }, crypto.randomUUID())
    const current = await composeApi.task(props.nodeId, accepted.taskId)
    task.value = current
    message.value = taskMessage(current)
    if (current.status === 'queued' || current.status === 'running') poll(accepted.taskId)
    else {
      submitting.value = false
      if (current.status === 'succeeded') emit('created', current)
    }
  } catch (reason) {
    submitting.value = false
    error.value = reason instanceof Error ? reason.message : '无法提交 Compose 创建任务。'
  }
}

onBeforeUnmount(() => {
  disposed = true
  if (pollTimer) clearTimeout(pollTimer)
})
</script>

<template>
  <section class="compose-create-form" aria-label="创建 Compose 项目" data-testid="compose-create-form">
    <header><div><span class="eyebrow">NEW PROJECT</span><h3>创建 Compose 项目</h3></div><span>真实执行 docker compose config 与 up -d</span></header>
    <p class="compose-create-note">工作目录必须已存在于目标 Agent 主机。Agent 会拒绝符号链接目录和已有的 compose.yaml；配置校验通过后才会原子创建文件。</p>
    <div class="compose-create-fields">
      <label>项目名称<input v-model.trim="name" autocomplete="off" maxlength="63" placeholder="例如 web-stack" :disabled="submitting"></label>
      <label>Agent 工作目录<input v-model.trim="workingDirectory" autocomplete="off" placeholder="/srv/web-stack" :disabled="submitting"></label>
    </div>
    <label class="compose-create-yaml">Compose YAML<textarea v-model="content" spellcheck="false" placeholder="services:\n  app:\n    image: nginx:stable" :disabled="submitting" /></label>
    <div class="compose-create-actions">
      <button class="container-action container-action-danger" type="button" :disabled="disabled || submitting || !validInput()" @click="void createProject()">{{ submitting ? '等待 Agent 部署…' : '创建并部署' }}</button>
      <span v-if="message" role="status" :data-status="task?.status">{{ message }}</span>
      <span v-if="error" role="alert">{{ error }}</span>
    </div>
  </section>
</template>

<style scoped>
.compose-create-form { display:grid; gap:10px; min-width:0; padding:14px; border:1px solid rgba(125,165,218,.24); border-radius:10px; background:rgba(10,20,33,.55); }
.compose-create-form header { display:flex; align-items:center; justify-content:space-between; gap:12px; }
.compose-create-form h3 { margin:4px 0 0; font-size:14px; }
.compose-create-form header>span,.compose-create-note { color:#95a8c1; font-size:10px; line-height:1.55; }
.compose-create-note { margin:0; }
.compose-create-fields { display:grid; grid-template-columns:minmax(150px,.6fr) minmax(220px,1fr); gap:9px; }
.compose-create-form label { display:grid; gap:5px; min-width:0; color:#b6c4d7; font-size:11px; }
.compose-create-form input,.compose-create-form textarea { box-sizing:border-box; width:100%; border:1px solid rgba(171,196,232,.2); border-radius:7px; padding:9px; color:#edf3fc; background:#121d2d; font:inherit; }
.compose-create-form input { min-height:36px; }
.compose-create-form textarea { min-height:220px; resize:vertical; background:#08111d; font:12px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace; }
.compose-create-actions { display:flex; flex-wrap:wrap; align-items:center; gap:10px; font-size:11px; }
.compose-create-actions [role="status"] { color:#a8d9b5; }
.compose-create-actions [data-status="failed"],.compose-create-actions [data-status="unknown"],.compose-create-actions [role="alert"] { color:#ffc1b8; }
@media (max-width:600px) { .compose-create-form header { align-items:flex-start; flex-direction:column; } .compose-create-fields { grid-template-columns:1fr; } .compose-create-form textarea { min-height:180px; } }
</style>
