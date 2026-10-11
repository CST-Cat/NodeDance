<script setup lang="ts">
import { t, display, locale } from "../i18n"
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api, type ContainerTask, type CreateFileTaskPayload, type NodeFileEntry } from '../api'
import { fileModifiedDate, formatFileSize, isEditableTextFile, joinNodePath, parentNodePath } from './files'

const props = defineProps<{ nodeId: string; nodeName: string }>()
const currentPath = ref('/')
const entries = ref<NodeFileEntry[]>([])
const loading = ref(false)
const pending = ref(false)
const error = ref('')
const message = ref('')
const editorPath = ref('')
const editorText = ref('')
const editorVersion = ref('')
const editorLoading = ref(false)
const filePicker = ref<HTMLInputElement>()
let disposed = false

const breadcrumbs = computed(() => {
  const parts = currentPath.value.split('/').filter(Boolean)
  const result: Array<{ label: string; path: string }> = [{ label: '/', path: '/' }]
  let path = ''
  for (const part of parts) {
    path += `/${part}`
    result.push({ label: part, path })
  }
  return result
})
const editorBytes = computed(() => new TextEncoder().encode(editorText.value).length)
const parentPath = computed(() => parentNodePath(currentPath.value))
const downloadURL = (entry: NodeFileEntry) => `/api/v1/nodes/${encodeURIComponent(props.nodeId)}/files/download?path=${encodeURIComponent(entry.path)}`

async function load(path = currentPath.value) {
  loading.value = true
  error.value = ''
  try {
    const response = await api.nodeFiles(props.nodeId, path)
    if (disposed) return
    currentPath.value = response.path
    entries.value = response.entries
  } catch (reason) {
    if (!disposed) error.value = reason instanceof Error ? reason.message : t('m.e2e0acb8ab46')
  } finally {
    if (!disposed) loading.value = false
  }
}

async function waitForTask(taskId: string): Promise<ContainerTask> {
  for (let attempt = 0; attempt < 240; attempt += 1) {
    const task = await api.nodeTask(props.nodeId, taskId)
    if (task.status !== 'queued' && task.status !== 'running') return task
    await new Promise((resolve) => window.setTimeout(resolve, 500))
    if (disposed) throw new Error(t('m.d42b9624254f'))
  }
  throw new Error(t('m.1b76591a88e1'))
}

async function runFileTask(payload: CreateFileTaskPayload, successMessage: string) {
  pending.value = true
  error.value = ''
  message.value = ''
  try {
    const accepted = await api.createFileTask(props.nodeId, payload, crypto.randomUUID())
    const task = await waitForTask(accepted.taskId)
    if (task.status === 'succeeded') {
      message.value = t('m.9f6548a67616', {"0": (successMessage)})
      await load()
    } else if (task.status === 'unknown') {
      message.value = t('m.a07bf68b2e91')
    } else {
      throw new Error(t('m.0913aa5a4393', {"0": (task.result.code || task.status)}))
    }
    return task
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : t('m.91488a18c47d')
    return null
  } finally {
    pending.value = false
  }
}

async function openText(entry?: NodeFileEntry) {
  error.value = ''
  message.value = ''
  if (!entry) {
    const name = window.prompt(t('m.d72fdbdedfdb'))?.trim()
    if (!name || name.includes('/') || name === '.' || name === '..') return
    editorPath.value = joinNodePath(currentPath.value, name)
    editorText.value = ''
    editorVersion.value = ''
    return
  }
  if (entry.kind !== 'file' || entry.size > 32 * 1024) {
    error.value = t('m.55b50af2d703')
    return
  }
  editorLoading.value = true
  try {
    const result = await api.nodeFileText(props.nodeId, entry.path)
    editorPath.value = result.path
    editorText.value = result.text
    editorVersion.value = result.version
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : t('m.942d34bc82c7')
  } finally {
    editorLoading.value = false
  }
}

async function saveText() {
  if (!editorPath.value) return
  const task = await runFileTask({ action: 'save_text', path: editorPath.value, expectedVersion: editorVersion.value, content: editorText.value }, t('m.3d8ac004269e'))
  if (task?.status === 'succeeded') {
    try {
      const latest = await api.nodeFileText(props.nodeId, editorPath.value)
      editorPath.value = latest.path
      editorText.value = latest.text
      editorVersion.value = latest.version
    } catch {
      editorPath.value = ''
    }
  }
}

async function createDirectory() {
  const name = window.prompt(t('m.ae51764fbb38'))?.trim()
  if (!name || name.includes('/') || name === '.' || name === '..') return
  await runFileTask({ action: 'mkdir', path: joinNodePath(currentPath.value, name) }, t('m.f1edc97079ec'))
}

async function renameEntry(entry: NodeFileEntry) {
  const name = window.prompt(t('m.92ddb51db6c4'), entry.name)?.trim()
  if (!name || name === entry.name || name.includes('/') || name === '.' || name === '..') return
  await runFileTask({ action: 'rename', path: entry.path, newPath: joinNodePath(currentPath.value, name) }, t('m.54dbc5d14e6c'))
}

async function deleteEntry(entry: NodeFileEntry) {
  if (!window.confirm(t('m.051e9916c510', {"0": (entry.path)}))) return
  await runFileTask({ action: 'delete', path: entry.path, deleteConfirmed: true, confirmationPath: entry.path }, t('m.d3da497e999c'))
}

function uploadFile() {
  filePicker.value?.click()
}

async function acceptUpload(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  const path = joinNodePath(currentPath.value, file.name)
  let expectedVersion = ''
  const existing = entries.value.find((entry) => entry.path === path)
  if (existing) {
    if (existing.kind !== 'file' || !window.confirm(t('m.c81ec173997c', {"0": (path)}))) return
    try {
      const latest = await api.nodeFileStat(props.nodeId, path)
      expectedVersion = latest.version || ''
    } catch (reason) {
      error.value = reason instanceof Error ? reason.message : t('m.35c77a3cfed1')
      return
    }
  }
  pending.value = true
  error.value = ''
  message.value = ''
  try {
    const task = await api.uploadNodeFile(props.nodeId, path, file, expectedVersion)
    if (task.status === 'succeeded') {
      message.value = t('m.0be4d61c6f18')
      await load()
    } else if (task.status === 'unknown') {
      message.value = t('m.aef7e35ce1ec')
    } else {
      error.value = t('m.090a5d6ff2c9', {"0": (task.result.code || task.status)})
    }
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : t('m.3f42bdcea6f7')
  } finally {
    pending.value = false
  }
}

watch(() => props.nodeId, () => {
  currentPath.value = '/'
  editorPath.value = ''
  void load('/')
})
onMounted(() => { disposed = false; void load('/') })
onBeforeUnmount(() => { disposed = true })
</script>

<template>
  <section class="node-files section-panel" aria-labelledby="files-title" data-testid="node-files">
    <header class="files-heading">
      <div><span class="eyebrow">{{ t('label.144c3510385c') }}</span><h2 id="files-title">{{ t('m.55ec425eddc8') }}{{ nodeName }}</h2></div>
      <div class="files-actions">
        <button class="container-action" type="button" :disabled="pending" @click="void openText()">{{ t('m.017b0649496c') }}</button>
        <button class="container-action" type="button" :disabled="pending" @click="createDirectory">{{ t('m.84244abc71de') }}</button>
        <button class="container-action" type="button" :disabled="pending" @click="uploadFile">{{ t('m.bb5caa9c5976') }}</button>
        <button class="container-action" type="button" :disabled="loading || pending" @click="void load()">{{ t('m.aee887434131') }}</button>
        <input ref="filePicker" class="file-picker" type="file" @change="acceptUpload">
      </div>
    </header>
    <p v-if="error" class="files-message files-error" role="alert">{{ display(error) }}</p>
    <p v-if="message" class="files-message" role="status">{{ display(message) }}</p>
    <p v-if="pending" class="files-message" role="status">{{ t('m.f31c81b217a7') }}</p>
    <nav class="files-breadcrumbs" :aria-label="t('m.257f4442af73')">
      <button v-for="crumb in breadcrumbs" :key="crumb.path" type="button" :aria-current="crumb.path === currentPath ? 'page' : undefined" @click="void load(crumb.path)">{{ crumb.label }}</button>
    </nav>
    <button v-if="parentPath" class="file-parent" type="button" @click="void load(parentPath)">{{ t('m.bf0252306b57') }}</button>
    <p v-if="loading" class="files-empty">{{ t('m.36517d179e0f') }}</p>
    <p v-else-if="entries.length === 0" class="files-empty">{{ t('m.b372af75428d') }}</p>
    <div v-else class="file-list" :aria-label="t('m.6bbaf705e8c6')">
      <article v-for="entry in entries" :key="entry.path" class="file-row" :data-kind="entry.kind">
        <button v-if="entry.kind === 'directory'" class="file-name" type="button" @click="void load(entry.path)"><span aria-hidden="true">📁</span>{{ entry.name }}/</button>
        <button v-else-if="isEditableTextFile(entry)" class="file-name" type="button" @click="void openText(entry)"><span aria-hidden="true">📄</span>{{ entry.name }}</button>
        <a v-else class="file-name" :href="downloadURL(entry)" :download="entry.name"><span aria-hidden="true">{{ entry.kind === 'file' ? '📄' : '↗' }}</span>{{ entry.name }}</a>
        <span class="file-kind">{{ entry.kind === 'directory' ? t('m.52daa71ebc31') : entry.kind === 'symlink' ? t('m.305378ddd2b8') : entry.kind === 'file' ? t('m.39932f24fe11') : t('m.d2909f1647e7') }}</span>
        <span class="file-size">{{ entry.kind === 'file' ? formatFileSize(entry.size) : '—' }}</span>
        <time v-if="fileModifiedDate(entry)" class="file-date" :datetime="fileModifiedDate(entry)!.toISOString()">{{ fileModifiedDate(entry)!.toLocaleString(locale) }}</time><span v-else class="file-date">—</span>
        <div class="file-row-actions">
          <button type="button" :disabled="pending || entry.path === '/'" @click="void renameEntry(entry)">{{ t('m.0d0cbac2eee5') }}</button>
          <button type="button" class="file-delete" :disabled="pending || entry.path === '/'" @click="void deleteEntry(entry)">{{ t('m.2f9daa828907') }}</button>
        </div>
      </article>
    </div>
    <section v-if="editorPath" class="file-editor" :aria-label="t('m.c42f23fe05ae')">
      <header><div><span class="eyebrow">{{ editorVersion ? 'EDIT FILE' : 'NEW FILE' }}</span><h3>{{ editorPath }}</h3></div><button type="button" class="container-action" :disabled="pending" @click="editorPath = ''">{{ t('m.3fd47edce45b') }}</button></header>
      <p v-if="editorLoading" class="files-empty">{{ t('m.b75809afbe0c') }}</p>
      <textarea v-else v-model="editorText" spellcheck="false" :aria-label="t('m.479bac57975e')"></textarea>
      <footer><span>{{ editorBytes }} / 32 KiB</span><button type="button" :disabled="pending || editorLoading || editorBytes > 32 * 1024" @click="void saveText()">{{ pending ? t('m.ff509c9ba052') : t('m.5dd6704c1e9a') }}</button></footer>
    </section>
  </section>
</template>

<style scoped>
.files-heading,.files-heading>div,.file-editor header,.file-editor footer { display:flex; align-items:center; justify-content:space-between; gap:12px; }
.files-heading { flex-wrap:wrap; margin-bottom:12px; }
.files-heading h2,.file-editor h3 { margin:4px 0 0; overflow-wrap:anywhere; }
.files-actions,.file-row-actions { display:flex; align-items:center; flex-wrap:wrap; gap:7px; }
.file-picker { display:none; }
.files-breadcrumbs { display:flex; flex-wrap:wrap; gap:6px; margin:12px 0; }
.files-breadcrumbs button,.file-parent,.file-row-actions button { border:1px solid rgba(171,196,232,.18); border-radius:7px; padding:7px 9px; color:#d8e6f8; background:#142236; cursor:pointer; }
.files-breadcrumbs button[aria-current="page"] { color:#79d9bf; }
.file-parent { margin:0 0 8px; }
.files-message,.files-empty { color:#a9bdd7; font-size:13px; }
.files-error { color:#ffb4a6; }
.file-list { display:grid; gap:6px; }
.file-row { display:grid; grid-template-columns:minmax(180px,1fr) 70px 80px minmax(145px,auto) auto; align-items:center; gap:10px; padding:9px 10px; border:1px solid rgba(171,196,232,.12); border-radius:8px; background:rgba(15,27,43,.6); }
.file-name { display:flex; align-items:center; gap:8px; min-width:0; overflow-wrap:anywhere; color:#deebfc; text-align:left; text-decoration:none; background:none; border:0; cursor:pointer; }
.file-kind,.file-size,.file-date { color:#9db1ca; font-size:11px; }
.file-row-actions { justify-content:flex-end; }
.file-row-actions .file-delete { color:#ffb4a6; }
.file-editor { margin-top:16px; padding:14px; border:1px solid rgba(171,196,232,.16); border-radius:10px; background:rgba(8,16,28,.55); }
.file-editor textarea { display:block; width:100%; min-height:280px; margin:12px 0; padding:12px; resize:vertical; border:1px solid rgba(171,196,232,.2); border-radius:8px; color:#e3edf9; background:#08111f; font:13px/1.55 ui-monospace,SFMono-Regular,Menlo,monospace; }
.file-editor footer { color:#9db1ca; font-size:11px; }
.file-editor footer button { border:0; border-radius:7px; padding:9px 14px; color:#071c17; background:#79d9bf; font-weight:700; cursor:pointer; }
@media(max-width:760px){.file-row{grid-template-columns:minmax(0,1fr) auto;}.file-kind,.file-size,.file-date{display:none}.file-row-actions{grid-column:1/-1;justify-content:flex-start}.files-heading>div:first-child{width:100%}.files-actions{width:100%}.files-actions button{flex:1;min-height:40px}}
</style>
