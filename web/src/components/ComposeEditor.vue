<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { composeApi, type ComposeProject } from '../compose-api'
import type { ContainerTask } from '../api'

const props = defineProps<{ nodeId: string; project: ComposeProject }>()

const fileIndex = ref(0)
const content = ref('')
const savedContent = ref('')
const baseSHA256 = ref('')
const loading = ref(false)
const validating = ref(false)
const saving = ref(false)
const valid = ref(false)
const error = ref('')
const message = ref('')
const task = ref<ContainerTask>()
let pollTimer: ReturnType<typeof setTimeout> | undefined
let generation = 0

const dirty = computed(() => content.value !== savedContent.value)
const currentFile = computed(() => props.project.ref.configFiles[fileIndex.value] ?? `配置文件 ${fileIndex.value + 1}`)

async function loadConfig() {
  const requestGeneration = ++generation
  loading.value = true
  error.value = ''
  valid.value = false
  try {
    const result = await composeApi.config(props.nodeId, props.project.ref.key, fileIndex.value)
    if (generation !== requestGeneration) return
    content.value = result.content
    savedContent.value = result.content
    baseSHA256.value = result.sha256
    message.value = '已读取远端配置。'
  } catch (reason) {
    if (generation === requestGeneration) error.value = reason instanceof Error ? reason.message : '无法读取 Compose 配置。'
  } finally {
    if (generation === requestGeneration) loading.value = false
  }
}

async function validate() {
  validating.value = true
  error.value = ''
  message.value = ''
  valid.value = false
  try {
    await composeApi.validateConfig(props.nodeId, props.project.ref.key, fileIndex.value, content.value)
    valid.value = true
    message.value = 'Docker Compose 校验通过。'
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : 'Compose 配置校验失败。'
  } finally {
    validating.value = false
  }
}

function schedulePoll(taskId: string, requestGeneration: number) {
  pollTimer = setTimeout(async () => {
    try {
      const current = await composeApi.task(props.nodeId, taskId)
      if (requestGeneration !== generation) return
      task.value = current
      if (current.status === 'succeeded') {
        await loadConfig()
        message.value = '配置已原子保存并由 Agent 核对。'
        saving.value = false
        return
      }
      if (current.status === 'failed' || current.status === 'unknown' || current.status === 'timed_out' || current.status === 'canceled') {
        error.value = current.status === 'unknown' ? '保存结果待核实，请重新读取远端文件后再继续。' : '配置未保存；远端文件保持原样。'
        saving.value = false
        return
      }
      schedulePoll(taskId, requestGeneration)
    } catch (reason) {
      if (requestGeneration !== generation) return
      error.value = reason instanceof Error ? reason.message : '无法读取配置保存任务状态。'
      saving.value = false
    }
  }, 750)
}

async function save() {
  if (!valid.value || !dirty.value || saving.value) return
  saving.value = true
  error.value = ''
  message.value = ''
  const requestGeneration = ++generation
  try {
    const accepted = await composeApi.createTask(props.nodeId, props.project.ref.key, {
      action: 'config_save', fileIndex: fileIndex.value, content: content.value, baseSha256: baseSHA256.value,
    }, crypto.randomUUID())
    task.value = await composeApi.task(props.nodeId, accepted.taskId)
    if (task.value.status === 'succeeded') {
      await loadConfig()
      message.value = '配置已原子保存并由 Agent 核对。'
      saving.value = false
      return
    }
    if (task.value.status === 'failed' || task.value.status === 'unknown') {
      error.value = task.value.status === 'unknown' ? '保存结果待核实，请重新读取远端文件后再继续。' : '配置未保存；远端文件保持原样。'
      saving.value = false
      return
    }
    schedulePoll(accepted.taskId, requestGeneration)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法保存 Compose 配置。'
    saving.value = false
  }
}

function restore() {
  content.value = savedContent.value
  valid.value = false
  error.value = ''
  message.value = '已恢复到最近一次读取的远端版本。'
}

function onContentChange() {
  valid.value = false
  error.value = ''
  message.value = ''
}

function onFileChange() {
  void loadConfig()
}

onMounted(() => void loadConfig())
onBeforeUnmount(() => {
  generation++
  if (pollTimer) clearTimeout(pollTimer)
})
</script>

<template>
  <section class="compose-editor" aria-label="Compose 配置编辑器" data-testid="compose-editor">
    <header class="compose-editor-heading">
      <div><span class="eyebrow">CONFIGURATION</span><h3>编辑 Compose 配置</h3></div>
      <button class="container-action" type="button" :disabled="loading || saving" @click="void loadConfig()">重新读取</button>
    </header>
    <label class="compose-file-picker">配置文件
      <select v-model.number="fileIndex" :disabled="loading || saving" @change="onFileChange">
        <option v-for="(path, index) in project.ref.configFiles" :key="path" :value="index">{{ path.split('/').at(-1) || path }}</option>
      </select>
      <small>{{ currentFile }}</small>
    </label>
    <p class="compose-editor-risk">保存前会使用 Agent 上的 Docker Compose CLI 校验候选文件。保存采用同目录原子替换和版本摘要检查；部署会重建该项目服务，先检查挂载、端口及服务影响。</p>
    <textarea v-model="content" aria-label="Compose YAML 配置" spellcheck="false" :disabled="loading || saving" @input="onContentChange" />
    <div class="compose-editor-actions">
      <button class="container-action" type="button" :disabled="loading || validating || saving" @click="void validate()">{{ validating ? '校验中…' : '校验配置' }}</button>
      <button class="container-action container-action-danger" type="button" :disabled="!valid || !dirty || saving" @click="void save()">{{ saving ? '保存中…' : '保存配置' }}</button>
      <button class="container-action" type="button" :disabled="!dirty || saving" @click="restore">恢复到最近读取版本</button>
    </div>
    <p v-if="loading" class="compose-editor-message" role="status">正在读取远端配置…</p>
    <p v-if="message" class="compose-editor-message" role="status">{{ message }}</p>
    <p v-if="error" class="compose-editor-error" role="alert">{{ error }}</p>
  </section>
</template>

<style scoped>
.compose-editor { display: grid; gap: 10px; min-width: 0; margin-top: 16px; padding: 14px; border: 1px solid rgba(125,165,218,.25); border-radius: 10px; background: rgba(4,12,22,.72); }
.compose-editor-heading { display:flex; justify-content:space-between; align-items:center; gap:12px; }
.compose-editor-heading h3 { margin:4px 0 0; font-size:14px; }
.compose-file-picker { display:grid; gap:5px; color:#b6c4d7; font-size:11px; }
.compose-file-picker select { min-height:36px; border:1px solid rgba(171,196,232,.2); border-radius:7px; padding:7px 9px; color:#edf3fc; background:#121d2d; font:inherit; }
.compose-file-picker small { overflow-wrap:anywhere; color:#8295ae; }
.compose-editor-risk { margin:0; color:#efc58d; font-size:11px; line-height:1.6; }
.compose-editor textarea { width:100%; min-height:360px; resize:vertical; box-sizing:border-box; border:1px solid rgba(171,196,232,.2); border-radius:8px; padding:12px; color:#eaf0fa; background:#08111d; font:12px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace; tab-size:2; }
.compose-editor-actions { display:flex; flex-wrap:wrap; gap:8px; }
.compose-editor-message { margin:0; color:#a8d9b5; font-size:11px; }
.compose-editor-error { margin:0; color:#ffc1b8; font-size:11px; }
@media (max-width:600px) { .compose-editor textarea { min-height:260px; } }
</style>
