<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ApiError, api, type NodeFileEntry } from '../api'

const props = defineProps<{ nodeId: string; disabled?: boolean }>()

const currentPath = ref('/')
const entries = ref<NodeFileEntry[]>([])
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const editorOpen = ref(false)
const editorPath = ref('')
const editorText = ref('')
const editorVersion = ref('')
const editorBusy = ref(false)
const editorError = ref('')
const uploadInput = ref<HTMLInputElement>()
const uploadController = ref<AbortController>()
const pathParts = computed(() => currentPath.value === '/' ? [] : currentPath.value.slice(1).split('/'))
const parentPath = computed(() => {
  if (currentPath.value === '/') return '/'
  const parent = currentPath.value.slice(0, currentPath.value.lastIndexOf('/'))
  return parent || '/'
})

function joinPath(base: string, name: string) {
  const normalizedBase = base.endsWith('/') ? base : `${base}/`
  return `${normalizedBase}${name}`
}

async function loadFiles(path = currentPath.value) {
  if (!props.nodeId) return
  loading.value = true
  error.value = ''
  try {
    const result = await api.listNodeFiles(props.nodeId, path)
    currentPath.value = result.path
    entries.value = result.entries
  } catch (caught) {
    error.value = messageFor(caught)
  } finally {
    loading.value = false
  }
}

function messageFor(caught: unknown) {
  if (caught instanceof ApiError && caught.status === 409) return '文件已被其他进程修改，请重新打开后再保存。'
  if (caught instanceof ApiError && caught.status === 404) return '目标文件或目录不存在。'
  if (caught instanceof ApiError && caught.status === 413) return '文件超过当前节点允许的传输上限。'
  if (caught instanceof ApiError && caught.status === 403) return 'Agent 没有访问该文件的权限。'
  return caught instanceof Error ? caught.message : '文件操作暂时不可用。'
}

async function openEntry(entry: NodeFileEntry) {
  if (entry.kind === 'directory') {
    await loadFiles(entry.path)
    return
  }
  if (entry.kind !== 'file') return
  if (entry.size > 32 * 1024) {
    notice.value = '该文件超过在线文本编辑上限，可下载后在目标节点外部编辑。'
    return
  }
  editorError.value = ''
  try {
    const result = await api.readNodeText(props.nodeId, entry.path)
    editorPath.value = entry.path
    editorText.value = result.text
    editorVersion.value = result.version
    editorOpen.value = true
  } catch (caught) {
    notice.value = messageFor(caught)
  }
}

async function saveText() {
  editorBusy.value = true
  editorError.value = ''
  try {
    const result = await api.saveNodeText(props.nodeId, {
      path: editorPath.value,
      version: editorVersion.value,
      text: editorText.value,
    })
    editorVersion.value = result.entry?.version ?? editorVersion.value
    editorOpen.value = false
    notice.value = result.backupPath ? `已保存，备份位于 ${result.backupPath}` : '文件已保存。'
    await loadFiles()
  } catch (caught) {
    editorError.value = messageFor(caught)
  } finally {
    editorBusy.value = false
  }
}

async function makeDirectory() {
  const name = window.prompt('新目录名称')
  if (!name) return
  busy.value = true
  error.value = ''
  try {
    await api.createNodeDirectory(props.nodeId, joinPath(currentPath.value, name))
    notice.value = '目录已创建。'
    await loadFiles()
  } catch (caught) {
    error.value = messageFor(caught)
  } finally {
    busy.value = false
  }
}

async function renameEntry(entry: NodeFileEntry) {
  const destination = window.prompt('新路径', joinPath(parentPath.value, entry.name))
  if (!destination || destination === entry.path) return
  busy.value = true
  error.value = ''
  try {
    await api.renameNodeFile(props.nodeId, entry.path, destination)
    notice.value = '已重命名。'
    await loadFiles()
  } catch (caught) {
    error.value = messageFor(caught)
  } finally {
    busy.value = false
  }
}

async function deleteEntry(entry: NodeFileEntry) {
  const confirmation = window.prompt(`请输入完整路径以确认删除：${entry.path}`)
  if (confirmation !== entry.path) return
  busy.value = true
  error.value = ''
  try {
    await api.deleteNodeFile(props.nodeId, entry.path)
    notice.value = '已删除。'
    await loadFiles()
  } catch (caught) {
    error.value = messageFor(caught)
  } finally {
    busy.value = false
  }
}

function chooseUpload() {
  uploadInput.value?.click()
}

async function uploadChanged(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  const destination = joinPath(currentPath.value, file.name)
  let version = ''
  try {
    const existing = await api.statNodeFile(props.nodeId, destination)
    if (!window.confirm(`目标已存在：${destination}\n确认以此版本为基础原子替换吗？`)) return
    version = existing.version ?? ''
  } catch (caught) {
    if (!(caught instanceof ApiError) || caught.status !== 404) {
      error.value = messageFor(caught)
      return
    }
  }
  uploadController.value = new AbortController()
  busy.value = true
  error.value = ''
  notice.value = `正在上传 ${file.name}…`
  try {
    await api.uploadNodeFile(props.nodeId, destination, file, version, uploadController.value.signal)
    notice.value = '文件上传并校验完成。'
    await loadFiles()
  } catch (caught) {
    error.value = caught instanceof DOMException && caught.name === 'AbortError' ? '上传已取消。' : messageFor(caught)
  } finally {
    uploadController.value = undefined
    busy.value = false
  }
}

function cancelUpload() {
  uploadController.value?.abort()
}

function formatSize(size: number) {
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KiB`
  if (size < 1024 * 1024 * 1024) return `${(size / 1024 / 1024).toFixed(1)} MiB`
  return `${(size / 1024 / 1024 / 1024).toFixed(2)} GiB`
}

function downloadURL(entry: NodeFileEntry) {
  return api.nodeFileDownloadURL(props.nodeId, entry.path)
}

watch(() => props.nodeId, () => {
  currentPath.value = '/'
  void loadFiles('/')
})
onMounted(() => void loadFiles())
onBeforeUnmount(() => uploadController.value?.abort())
</script>

<template>
  <section class="node-files" aria-label="节点文件管理">
    <header class="files-header">
      <div>
        <h2>节点文件</h2>
        <p>文件操作直接在被控节点执行，在线文本编辑上限为 32 KiB。</p>
      </div>
      <div class="toolbar">
        <button type="button" :disabled="disabled || busy" @click="makeDirectory">新建目录</button>
        <button type="button" :disabled="disabled || busy" @click="chooseUpload">上传文件</button>
        <button type="button" :disabled="disabled || loading || busy" @click="loadFiles()">刷新</button>
        <button v-if="uploadController" type="button" @click="cancelUpload">取消上传</button>
        <input ref="uploadInput" class="visually-hidden" type="file" @change="uploadChanged">
      </div>
    </header>

    <nav class="breadcrumbs" aria-label="文件路径">
      <button type="button" @click="loadFiles('/')">/</button>
      <template v-for="(part, index) in pathParts" :key="`${index}-${part}`">
        <span aria-hidden="true">/</span>
        <button type="button" @click="loadFiles(`/${pathParts.slice(0, index + 1).join('/')}`)">{{ part }}</button>
      </template>
    </nav>

    <p v-if="error" class="feedback error" role="alert">{{ error }}</p>
    <p v-if="notice" class="feedback" role="status">{{ notice }}</p>
    <p v-if="busy" class="busy-status" role="status">文件操作进行中…</p>

    <div class="files-location">
      <button type="button" :disabled="currentPath === '/' || busy" @click="loadFiles(parentPath)">上级目录</button>
      <code>{{ currentPath }}</code>
    </div>

    <div v-if="loading" class="loading" role="status">正在读取目录…</div>
    <div v-else-if="entries.length === 0" class="empty">此目录为空。</div>
    <div v-else class="table-scroll">
      <table>
        <thead>
          <tr><th scope="col">名称</th><th scope="col">类型</th><th scope="col">大小</th><th scope="col">权限</th><th scope="col">所有者</th><th scope="col">操作</th></tr>
        </thead>
        <tbody>
          <tr v-for="entry in entries" :key="entry.path">
            <td>
              <button v-if="entry.kind === 'directory'" class="name-button" type="button" @click="openEntry(entry)">{{ entry.name }}/</button>
              <span v-else>{{ entry.name }}</span>
            </td>
            <td>{{ entry.kind }}</td>
            <td>{{ formatSize(entry.size) }}</td>
            <td><code>{{ entry.mode.toString(8).padStart(3, '0') }}</code></td>
            <td>{{ entry.ownerUid }}:{{ entry.ownerGid }}</td>
            <td class="row-actions">
              <a v-if="entry.kind === 'file'" :href="downloadURL(entry)" :download="entry.name">下载</a>
              <button v-if="entry.kind === 'file'" type="button" :disabled="busy" @click="openEntry(entry)">编辑</button>
              <button type="button" :disabled="busy || entry.path === '/'" @click="renameEntry(entry)">重命名</button>
              <button type="button" class="danger" :disabled="busy || entry.path === '/'" @click="deleteEntry(entry)">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="editorOpen" class="editor-backdrop" role="presentation">
      <section class="editor-dialog" role="dialog" aria-modal="true" aria-labelledby="editor-title">
        <header><h3 id="editor-title">编辑 {{ editorPath }}</h3><button type="button" :disabled="editorBusy" @click="editorOpen = false">关闭</button></header>
        <p>保存前会重新核对版本；外部进程修改文件时，保存会返回冲突。</p>
        <textarea v-model="editorText" aria-label="文件文本内容" spellcheck="false"></textarea>
        <p v-if="editorError" class="feedback error" role="alert">{{ editorError }}</p>
        <footer><button type="button" :disabled="editorBusy" @click="editorOpen = false">取消</button><button type="button" :disabled="editorBusy" @click="saveText">{{ editorBusy ? '保存中…' : '保存并备份' }}</button></footer>
      </section>
    </div>
  </section>
</template>

<style scoped>
.node-files{display:grid;gap:1rem;min-width:0;color:#e6edf7}.files-header{display:flex;align-items:flex-start;justify-content:space-between;gap:1rem}.files-header h2{margin:0;font-size:1.2rem}.files-header p,.editor-dialog>p{margin:.35rem 0 0;color:#9cabc0;font-size:.88rem}.toolbar,.row-actions{display:flex;flex-wrap:wrap;align-items:center;gap:.45rem}.node-files button,.node-files a{border:1px solid #42546b;border-radius:.45rem;background:#1a2636;color:#e6edf7;padding:.45rem .65rem;font:inherit;text-decoration:none;cursor:pointer}.node-files button:hover,.node-files a:hover{background:#26374c}.node-files button:disabled{opacity:.5;cursor:not-allowed}.node-files .danger{color:#ffb1ae}.breadcrumbs,.files-location{display:flex;align-items:center;gap:.4rem;min-width:0;overflow-wrap:anywhere}.breadcrumbs button{background:transparent;border:0;padding:.15rem}.files-location code{overflow-wrap:anywhere;color:#b9c9dc}.feedback{margin:0;padding:.65rem .8rem;border-radius:.45rem;background:#17344a;color:#d6e9ff;overflow-wrap:anywhere}.feedback.error{background:#482a32;color:#ffd7d9}.busy-status,.loading,.empty{color:#a8b6c8}.table-scroll{width:100%;overflow:auto;border:1px solid #334359;border-radius:.6rem}table{width:100%;border-collapse:collapse;min-width:760px}th,td{padding:.7rem .6rem;text-align:left;border-bottom:1px solid #334359}th{font-size:.83rem;color:#aebbd0;background:#182536}tbody tr:last-child td{border-bottom:0}.name-button{border:0!important;background:transparent!important;padding:0!important;color:#8bc7ff!important}.row-actions{min-width:220px}.visually-hidden{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}.editor-backdrop{position:fixed;z-index:1000;inset:0;display:grid;place-items:center;padding:1rem;background:#0009}.editor-dialog{display:grid;gap:.8rem;width:min(900px,100%);max-height:min(90vh,900px);overflow:auto;padding:1rem;border:1px solid #475a72;border-radius:.8rem;background:#111b29;box-shadow:0 20px 70px #0008}.editor-dialog header,.editor-dialog footer{display:flex;align-items:center;justify-content:space-between;gap:.5rem}.editor-dialog h3{margin:0;overflow-wrap:anywhere}.editor-dialog textarea{width:100%;min-height:45vh;resize:vertical;padding:.8rem;border:1px solid #42546b;border-radius:.5rem;background:#0b111b;color:#e6edf7;font: .9rem/1.5 ui-monospace,monospace}.editor-dialog footer{justify-content:flex-end}@media(max-width:640px){.files-header{display:grid}.toolbar{display:grid;grid-template-columns:repeat(2,minmax(0,1fr))}.toolbar button{min-height:2.7rem}.files-location{align-items:flex-start;flex-direction:column}.table-scroll{border:0;overflow:visible}table{min-width:0}thead{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0)}tbody{display:grid;gap:.7rem}tbody tr{display:grid;gap:.4rem;padding:.75rem;border:1px solid #334359;border-radius:.6rem;background:#152131}tbody td{display:flex;justify-content:space-between;gap:.8rem;padding:.2rem;border:0}tbody td:nth-child(1)::before{content:'名称';color:#9cabc0}tbody td:nth-child(2)::before{content:'类型';color:#9cabc0}tbody td:nth-child(3)::before{content:'大小';color:#9cabc0}tbody td:nth-child(4)::before{content:'权限';color:#9cabc0}tbody td:nth-child(5)::before{content:'所有者';color:#9cabc0}tbody td:nth-child(6)::before{content:'操作';color:#9cabc0}.row-actions{justify-content:flex-end;min-width:0}.row-actions button,.row-actions a{min-height:2.5rem}.editor-dialog textarea{min-height:55vh}}
</style>
