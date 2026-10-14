<script setup lang="ts">
import { requestConfirm } from '../dialogs'
import { t, display, locale } from "../i18n"
import { defineAsyncComponent, nextTick, onBeforeUnmount, ref } from 'vue'
import { composeApi } from '../compose-api'
import type { ContainerTask } from '../api'
const NodeFiles=defineAsyncComponent(()=>import('./NodeFiles.vue'))

const props = defineProps<{ nodeId: string; disabled: boolean }>()
const emit = defineEmits<{ created: [task: ContainerTask] }>()

const name = ref('')
const workingDirectory = ref('')
const content = ref('')
const directoryPickerOpen=ref(false)
const selectedDirectory=ref('')
const directoryButton=ref<HTMLButtonElement>()
async function closeDirectoryPicker(useDirectory=false){
 if(useDirectory&&selectedDirectory.value)workingDirectory.value=selectedDirectory.value
 directoryPickerOpen.value=false;selectedDirectory.value=''
 await nextTick();directoryButton.value?.focus()
}

const submitting = ref(false)
const error = ref('')
const message = ref('')
const task = ref<ContainerTask>()
let pollTimer: ReturnType<typeof setTimeout> | undefined
let disposed = false

function validInput() {
  return Boolean(name.value.trim() && workingDirectory.value.trim() && content.value.trim())
}

function taskMessage(value: ContainerTask) {
  if (value.status === 'succeeded') return t('m.cffa1e3e2c3b')
  if (value.status === 'failed') return t('m.8fd2915e75c4', {"0": (value.result.code || t('m.f50ce21ae910'))})
  if (value.status === 'unknown' || value.status === 'timed_out') return t('m.0c4dcb0a3484')
  if (value.status === 'canceled') return t('m.12632265c570')
  return t('m.282f0fac39e8', {"0": (value.status)})
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
      error.value = reason instanceof Error ? reason.message : t('m.bbf9a9b3593c')
    }
  }, 750)
}

async function createProject() {
  if (props.disabled || submitting.value || !validInput()) return
  if (!await requestConfirm(t('m.0934bc7652a3', {"0": (props.nodeId), "1": (name.value)}))) return
  submitting.value = true
  error.value = ''
  message.value = ''
  task.value = undefined
  try {
    const accepted = await composeApi.createProject(props.nodeId, {
      name: name.value,
      workingDirectory: workingDirectory.value,
      content: content.value,
    })
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
    error.value = reason instanceof Error ? reason.message : t('m.748066b2afa5')
  }
}

onBeforeUnmount(() => {
  disposed = true
  if (pollTimer) clearTimeout(pollTimer)
})
</script>

<template>
  <section class="compose-create-form" :aria-label="t('m.2798c23115cf')" data-testid="compose-create-form">
    <header><div><span class="eyebrow">{{ t('label.7212dcc44ffd') }}</span><h3>{{ t('m.2798c23115cf') }}</h3></div><span>{{ t('m.0da9f2878ec4') }}</span></header>
    <p class="compose-create-note">{{ t('m.872a816958dc') }}</p>
    <div class="compose-create-fields">
      <label>{{ t('m.867698fb7b0c') }}<input v-model.trim="name" autocomplete="off" maxlength="63" :placeholder="t('m.d1bcd8876f63')" :disabled="submitting"></label>
      <div class="compose-directory-field"><label>{{ t('m.e9abe5532d97') }}<input v-model.trim="workingDirectory" data-testid="compose-workdir" autocomplete="off" placeholder="/srv/web-stack" :disabled="submitting"></label><button ref="directoryButton" class="container-action" type="button" :disabled="disabled || submitting" :aria-expanded="directoryPickerOpen" aria-controls="compose-directory-picker" data-testid="compose-directory-open" @click="selectedDirectory='';directoryPickerOpen=true">{{ t('compose.browseDirectory') }}</button></div>
    </div>
    <section v-if="directoryPickerOpen" id="compose-directory-picker" class="compose-directory-picker" :aria-label="t('compose.chooseDirectory')" data-testid="compose-directory-picker" @keydown.esc.stop="void closeDirectoryPicker()">
      <header><h3>{{ t('compose.chooseDirectory') }}</h3><button class="container-action" type="button" @click="void closeDirectoryPicker()">{{ t('dialog.cancel') }}</button></header>
      <p>{{ t('compose.directoryHelp') }}</p>
      <NodeFiles :node-id="nodeId" :node-name="t('compose.chooseDirectory')" directory-picker @directory-ready="selectedDirectory=$event" />
      <footer><span>{{ selectedDirectory || t('guest.loading') }}</span><button class="container-action" type="button" :disabled="!selectedDirectory" data-testid="compose-directory-use" @click="void closeDirectoryPicker(true)">{{ t('compose.useDirectory') }}</button></footer>
    </section>
    <label class="compose-create-yaml">Compose YAML<textarea v-model="content" spellcheck="false" :placeholder="'services:\n  app:\n    image: nginx:stable'" :disabled="submitting" /></label>
    <div class="compose-create-actions">
      <button class="container-action container-action-danger" type="button" :disabled="disabled || submitting || !validInput()" @click="void createProject()">{{ submitting ? t('m.92199e4ba37e') : t('m.6db055dc4ece') }}</button>
      <span v-if="message" role="status" :data-status="task?.status">{{ display(message) }}</span>
      <span v-if="error" role="alert">{{ display(error) }}</span>
    </div>
  </section>
</template>

<style scoped>
.compose-create-form { display:grid; gap:10px; min-width:0; padding:14px; border:1px solid var(--line); border-radius:var(--radius-card); background:var(--surface-soft); }
.compose-create-form header { display:flex; align-items:center; justify-content:space-between; gap:12px; }
.compose-create-form h3 { margin:4px 0 0; font-size: 14px; }
.compose-create-form header>span,.compose-create-note { color:var(--muted); font-size: 12px; line-height:1.55; }
.compose-create-note { margin:0; }
.compose-create-fields { display:grid; grid-template-columns:minmax(150px,.6fr) minmax(220px,1fr); gap:9px; }
.compose-create-form label { display:grid; gap:5px; min-width:0; color:var(--muted-strong); font-size: 12px; }
.compose-create-form input,.compose-create-form textarea { box-sizing:border-box; width:100%; border:1px solid var(--line); border-radius:var(--radius-control); padding:9px; color:var(--text); background:var(--surface-soft); font:inherit; }
.compose-create-form input { min-height:40px; }
.compose-create-form textarea { min-height:220px; resize:vertical; background:var(--surface-soft); font: 12px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace; }
.compose-directory-field { display:grid; grid-template-columns:minmax(0,1fr) auto; align-items:end; gap:8px; }
.compose-directory-picker { min-width:0; display:grid; gap:10px; padding:12px; border:1px solid var(--line); border-radius:var(--radius-card); }
.compose-directory-picker p { margin:0; color:var(--muted-strong); font-size:12px; }
.compose-directory-picker footer { display:flex; flex-wrap:wrap; align-items:center; justify-content:space-between; gap:8px; }
.compose-directory-picker footer span { min-width:0; overflow-wrap:anywhere; font:12px ui-monospace,monospace; }
.compose-create-actions { display:flex; flex-wrap:wrap; align-items:center; gap:10px; font-size: 12px; }
.compose-create-actions [role="status"] { color:var(--success); }
.compose-create-actions [data-status="failed"],.compose-create-actions [data-status="unknown"],.compose-create-actions [role="alert"] { color:var(--danger); }
@media (max-width:600px) { .compose-create-form header { align-items:flex-start; flex-direction:column; } .compose-create-fields { grid-template-columns:1fr; } .compose-create-form textarea { min-height:180px; } }
</style>
