<script setup lang="ts">
import { t, display, locale } from "../i18n"
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
const currentFile = computed(() => props.project.ref.configFiles[fileIndex.value] ?? t('m.8b98267611a6', {"0": (fileIndex.value + 1)}))

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
    message.value = t('m.8d2e18f84c7e')
  } catch (reason) {
    if (generation === requestGeneration) error.value = reason instanceof Error ? reason.message : t('m.3957ffb3e358')
  } finally {
    if (generation === requestGeneration) loading.value = false
  }
}

async function validate() {
  const requestGeneration = generation
  const submittedContent = content.value
  validating.value = true
  error.value = ''
  message.value = ''
  valid.value = false
  try {
    await composeApi.validateConfig(props.nodeId, props.project.ref.key, fileIndex.value, submittedContent)
    if (generation !== requestGeneration || content.value !== submittedContent) return
    valid.value = true
    message.value = t('m.351d70486605')
  } catch (reason) {
    if (generation !== requestGeneration || content.value !== submittedContent) return
    error.value = reason instanceof Error ? reason.message : t('m.29e08c6652a0')
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
        message.value = t('m.334ef965226e')
        saving.value = false
        return
      }
      if (current.status === 'failed' || current.status === 'unknown' || current.status === 'timed_out' || current.status === 'canceled') {
        error.value = current.status === 'unknown' ? t('m.e90270efd29a') : t('m.2a044add4de5')
        saving.value = false
        return
      }
      schedulePoll(taskId, requestGeneration)
    } catch (reason) {
      if (requestGeneration !== generation) return
      error.value = reason instanceof Error ? reason.message : t('m.f5ab77abe914')
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
    })
    task.value = await composeApi.task(props.nodeId, accepted.taskId)
    if (task.value.status === 'succeeded') {
      await loadConfig()
      message.value = t('m.334ef965226e')
      saving.value = false
      return
    }
    if (task.value.status === 'failed' || task.value.status === 'unknown') {
      error.value = task.value.status === 'unknown' ? t('m.e90270efd29a') : t('m.2a044add4de5')
      saving.value = false
      return
    }
    schedulePoll(accepted.taskId, requestGeneration)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : t('m.3d63ac5acf31')
    saving.value = false
  }
}

function restore() {
  content.value = savedContent.value
  valid.value = false
  error.value = ''
  message.value = t('m.5440ce29fbc9')
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
  <section class="compose-editor" :aria-label="t('m.68caef829d91')" data-testid="compose-editor">
    <header class="compose-editor-heading">
      <div><span class="eyebrow">{{ t('label.d5cde76290bf') }}</span><h3>{{ t('m.a5de4df1d563') }}</h3></div>
      <button class="container-action" type="button" :disabled="loading || saving" @click="void loadConfig()">{{ t('m.df7392cc96be') }}</button>
    </header>
    <label class="compose-file-picker">{{ t('m.1d27f02ed278') }}<UiSelect v-model.number="fileIndex" :disabled="loading || saving" @change="onFileChange">
        <option v-for="(path, index) in project.ref.configFiles" :key="path" :value="index">{{ path.split('/').at(-1) || path }}</option>
      </UiSelect>
      <small>{{ currentFile }}</small>
    </label>
    <p class="compose-editor-risk">{{ t('m.cd673d386eed') }}</p>
    <textarea v-model="content" :aria-label="t('m.47a8168380df')" spellcheck="false" :disabled="loading || saving" @input="onContentChange" />
    <div class="compose-editor-actions">
      <button class="container-action" type="button" :disabled="loading || validating || saving" @click="void validate()">{{ validating ? t('m.fee0b2ceff3f') : t('m.b26286234be3') }}</button>
      <button class="container-action container-action-danger" type="button" :disabled="!valid || !dirty || saving" @click="void save()">{{ saving ? t('m.ff509c9ba052') : t('m.6e584e3d5ce6') }}</button>
      <button class="container-action" type="button" :disabled="!dirty || saving" @click="restore">{{ t('m.131be45b460c') }}</button>
    </div>
    <p v-if="loading" class="compose-editor-message" role="status">{{ t('m.49d3303a0315') }}</p>
    <p v-if="message" class="compose-editor-message" role="status">{{ display(message) }}</p>
    <p v-if="error" class="compose-editor-error" role="alert">{{ display(error) }}</p>
  </section>
</template>

<style scoped>
.compose-editor { display: grid; gap: 10px; min-width: 0; margin-top: 16px; padding: 14px; border: 1px solid var(--line); border-radius:var(--radius-card); background: var(--surface-soft); }
.compose-editor-heading { display:flex; justify-content:space-between; align-items:center; gap:12px; }
.compose-editor-heading h3 { margin:4px 0 0; font-size: 14px; }
.compose-file-picker { display:grid; gap:5px; color:var(--muted-strong); font-size: 12px; }
.compose-file-picker select { min-height:40px; border:1px solid var(--line); border-radius:var(--radius-control); padding:7px 9px; color:var(--text); background:var(--surface-soft); font:inherit; }
.compose-file-picker small { overflow-wrap:anywhere; color:var(--muted); }
.compose-editor-risk { margin:0; color:var(--warning); font-size: 12px; line-height:1.6; }
.compose-editor textarea { width:100%; min-height:360px; resize:vertical; box-sizing:border-box; border:1px solid var(--line); border-radius:var(--radius-control); padding:12px; color:var(--text); background:var(--surface-soft); font: 12px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace; tab-size:2; }
.compose-editor-actions { display:flex; flex-wrap:wrap; gap:8px; }
.compose-editor-message { margin:0; color:var(--success); font-size: 12px; }
.compose-editor-error { margin:0; color:var(--danger); font-size: 12px; }
@media (max-width:600px) { .compose-editor textarea { min-height:260px; } }
</style>
