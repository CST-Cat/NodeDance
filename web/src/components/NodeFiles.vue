<script setup lang="ts">
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
    if (!disposed) error.value = reason instanceof Error ? reason.message : '无法读取目录。'
  } finally {
    if (!disposed) loading.value = false
  }
}

async function waitForTask(taskId: string): Promise<ContainerTask> {
  for (let attempt = 0; attempt < 240; attempt += 1) {
    const task = await api.nodeTask(props.nodeId, taskId)
    if (task.status !== 'queued' && task.status !== 'running') return task
    await new Promise((resolve) => window.setTimeout(resolve, 500))
    if (disposed) throw new Error('文件页面已关闭。')
  }
  throw new Error('文件操作仍在运行，请到操作记录查看结果。')
}

async function runFileTask(payload: CreateFileTaskPayload, successMessage: string) {
  pending.value = true
  error.value = ''
  message.value = ''
  try {
    const accepted = await api.createFileTask(props.nodeId, payload, crypto.randomUUID())
    const task = await waitForTask(accepted.taskId)
    if (task.status === 'succeeded') {
      message.value = `${successMessage}，Agent 已核实结果。`
      await load()
    } else if (task.status === 'unknown') {
      message.value = '操作结果待核实；系统不会自动重试。请检查文件后再决定是否重做。'
    } else {
      throw new Error(`文件操作失败：${task.result.code || task.status}`)
    }
    return task
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '文件操作失败。'
    return null
  } finally {
    pending.value = false
  }
}

async function openText(entry?: NodeFileEntry) {
  error.value = ''
  message.value = ''
  if (!entry) {
    const name = window.prompt('新文本文件名称')?.trim()
    if (!name || name.includes('/') || name === '.' || name === '..') return
    editorPath.value = joinNodePath(currentPath.value, name)
    editorText.value = ''
    editorVersion.value = ''
    return
  }
  if (entry.kind !== 'file' || entry.size > 32 * 1024) {
    error.value = '只支持编辑 32 KiB 以内的普通文本文件。'
    return
  }
  editorLoading.value = true
  try {
    const result = await api.nodeFileText(props.nodeId, entry.path)
    editorPath.value = result.path
    editorText.value = result.text
    editorVersion.value = result.version
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法读取文本文件。'
  } finally {
    editorLoading.value = false
  }
}

async function saveText() {
  if (!editorPath.value) return
  const task = await runFileTask({ action: 'save_text', path: editorPath.value, expectedVersion: editorVersion.value, content: editorText.value }, '文本文件已保存')
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
  const name = window.prompt('新文件夹名称')?.trim()
  if (!name || name.includes('/') || name === '.' || name === '..') return
  await runFileTask({ action: 'mkdir', path: joinNodePath(currentPath.value, name) }, '文件夹已创建')
}

async function renameEntry(entry: NodeFileEntry) {
  const name = window.prompt('新名称', entry.name)?.trim()
  if (!name || name === entry.name || name.includes('/') || name === '.' || name === '..') return
  await runFileTask({ action: 'rename', path: entry.path, newPath: joinNodePath(currentPath.value, name) }, '文件已重命名')
}

async function deleteEntry(entry: NodeFileEntry) {
  if (!window.confirm(`永久删除「${entry.path}」？\n\n目录会连同其中内容一起删除。此操作不会进入回收站。`)) return
  await runFileTask({ action: 'delete', path: entry.path, deleteConfirmed: true, confirmationPath: entry.path }, '文件已删除')
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
    if (existing.kind !== 'file' || !window.confirm(`替换现有文件「${path}」？原文件只会在上传完整且校验通过后替换。`)) return
    try {
      const latest = await api.nodeFileStat(props.nodeId, path)
      expectedVersion = latest.version || ''
    } catch (reason) {
      error.value = reason instanceof Error ? reason.message : '无法确认现有文件版本。'
      return
    }
  }
  pending.value = true
  error.value = ''
  message.value = ''
  try {
    const task = await api.uploadNodeFile(props.nodeId, path, file, expectedVersion)
    if (task.status === 'succeeded') {
      message.value = '上传完成，Agent 已校验文件内容。'
      await load()
    } else if (task.status === 'unknown') {
      message.value = '上传结果待核实；系统不会自动重试。请检查目标文件。'
    } else {
      error.value = `上传失败：${task.result.code || task.status}`
    }
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '上传失败。'
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
      <div><span class="eyebrow">REMOTE FILES</span><h2 id="files-title">文件管理 · {{ nodeName }}</h2></div>
      <div class="files-actions">
        <button class="container-action" type="button" :disabled="pending" @click="void openText()">新建文本</button>
        <button class="container-action" type="button" :disabled="pending" @click="createDirectory">新建文件夹</button>
        <button class="container-action" type="button" :disabled="pending" @click="uploadFile">上传文件</button>
        <button class="container-action" type="button" :disabled="loading || pending" @click="void load()">刷新</button>
        <input ref="filePicker" class="file-picker" type="file" @change="acceptUpload">
      </div>
    </header>
    <p v-if="error" class="files-message files-error" role="alert">{{ error }}</p>
    <p v-if="message" class="files-message" role="status">{{ message }}</p>
    <p v-if="pending" class="files-message" role="status">正在等待 Agent 执行并确认文件操作…</p>
    <nav class="files-breadcrumbs" aria-label="当前目录">
      <button v-for="crumb in breadcrumbs" :key="crumb.path" type="button" :aria-current="crumb.path === currentPath ? 'page' : undefined" @click="void load(crumb.path)">{{ crumb.label }}</button>
    </nav>
    <button v-if="parentPath" class="file-parent" type="button" @click="void load(parentPath)">↑ 上级目录</button>
    <p v-if="loading" class="files-empty">正在读取目录…</p>
    <p v-else-if="entries.length === 0" class="files-empty">此目录为空。</p>
    <div v-else class="file-list" aria-label="目录内容">
      <article v-for="entry in entries" :key="entry.path" class="file-row" :data-kind="entry.kind">
        <button v-if="entry.kind === 'directory'" class="file-name" type="button" @click="void load(entry.path)"><span aria-hidden="true">📁</span>{{ entry.name }}/</button>
        <button v-else-if="isEditableTextFile(entry)" class="file-name" type="button" @click="void openText(entry)"><span aria-hidden="true">📄</span>{{ entry.name }}</button>
        <a v-else class="file-name" :href="downloadURL(entry)" :download="entry.name"><span aria-hidden="true">{{ entry.kind === 'file' ? '📄' : '↗' }}</span>{{ entry.name }}</a>
        <span class="file-kind">{{ entry.kind === 'directory' ? '目录' : entry.kind === 'symlink' ? '链接' : entry.kind === 'file' ? '文件' : '其他' }}</span>
        <span class="file-size">{{ entry.kind === 'file' ? formatFileSize(entry.size) : '—' }}</span>
        <time v-if="fileModifiedDate(entry)" class="file-date" :datetime="fileModifiedDate(entry)!.toISOString()">{{ fileModifiedDate(entry)!.toLocaleString() }}</time><span v-else class="file-date">—</span>
        <div class="file-row-actions">
          <button type="button" :disabled="pending || entry.path === '/'" @click="void renameEntry(entry)">重命名</button>
          <button type="button" class="file-delete" :disabled="pending || entry.path === '/'" @click="void deleteEntry(entry)">删除</button>
        </div>
      </article>
    </div>
    <section v-if="editorPath" class="file-editor" aria-label="文本编辑器">
      <header><div><span class="eyebrow">{{ editorVersion ? 'EDIT FILE' : 'NEW FILE' }}</span><h3>{{ editorPath }}</h3></div><button type="button" class="container-action" :disabled="pending" @click="editorPath = ''">关闭</button></header>
      <p v-if="editorLoading" class="files-empty">正在读取文件…</p>
      <textarea v-else v-model="editorText" spellcheck="false" aria-label="文本内容"></textarea>
      <footer><span>{{ editorBytes }} / 32 KiB</span><button type="button" :disabled="pending || editorLoading || editorBytes > 32 * 1024" @click="void saveText()">{{ pending ? '保存中…' : '保存文本' }}</button></footer>
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
