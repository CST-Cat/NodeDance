<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import type { ComposeProject } from '../compose-api'
import {
  composeEditorApi, type ComposeEditorFile, type ComposeEditorInput,
  type ComposeEditorOperation, type ComposeEditorPreview, type ComposePortEdit,
} from '../compose-editor-api'

const props = defineProps<{ nodeId: string; project: ComposeProject }>()
const envText = ref('')
const profileText = ref('')
const files = ref<ComposeEditorFile[]>([])
const selectedPath = ref('')
const sourceLoaded = ref(false)
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')
const preview = ref<ComposeEditorPreview | null>(null)
const latestOperation = ref<ComposeEditorOperation | null>(null)
const port = reactive({ file: '', service: '', target: '', protocol: 'tcp' as 'tcp' | 'udp', oldHostIP: '', oldPublished: '', newHostIP: '', newPublished: '' })

const selectedFile = computed(() => files.value.find((file) => file.path === selectedPath.value) ?? null)
const envFiles = computed(() => envText.value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean))
const profiles = computed(() => profileText.value.split(/[\r\n,]+/).map((item) => item.trim()).filter(Boolean))
const expectedVersions = computed(() => Object.fromEntries(files.value.map((file) => [file.path, file.version])))
const hasChanges = computed(() => !!preview.value?.diff)

function parsePort(value: string, optional = false): number | undefined {
  if (optional && !value.trim()) return undefined
  if (!/^\d+$/.test(value.trim())) throw new Error('端口必须是 1 到 65535 的整数。')
  const portValue = Number(value)
  if (!Number.isInteger(portValue) || portValue < 1 || portValue > 65535) throw new Error('端口必须是 1 到 65535 的整数。')
  return portValue
}

function inputSnapshot(): ComposeEditorInput {
  const portEdits: ComposePortEdit[] = []
  if (port.file || port.service || port.target || port.oldPublished || port.newPublished) {
    if (!port.file || !port.service.trim()) throw new Error('结构化端口修改需要选择配置文件并填写服务名。')
    const target = parsePort(port.target)
    const newPublished = parsePort(port.newPublished)
    portEdits.push({
      file: port.file, service: port.service.trim(), target: target!, protocol: port.protocol,
      oldHostIP: port.oldHostIP.trim(), oldPublished: parsePort(port.oldPublished, true),
      newHostIP: port.newHostIP.trim(), newPublished: newPublished!,
    })
  }
  return {
    files: files.value.map((file) => ({ ...file })),
    expectedVersions: { ...expectedVersions.value },
    portEdits,
  }
}

async function loadSource() {
  loading.value = true
  error.value = ''
  notice.value = ''
  preview.value = null
  try {
    const result = await composeEditorApi.source(props.nodeId, props.project.ref.key, envFiles.value, profiles.value)
    files.value = result.files
    selectedPath.value = result.files[0]?.path ?? ''
    sourceLoaded.value = true
    notice.value = `已从节点读取 ${result.files.length} 个配置/环境文件。`
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法读取 Compose 源文件。'
  } finally {
    loading.value = false
  }
}

function updateSelectedContent(value: string) {
  if (!selectedFile.value) return
  const target = selectedFile.value
  target.content = value
  preview.value = null
}

async function previewChanges() {
  error.value = ''
  notice.value = ''
  try {
    const result = await composeEditorApi.preview(props.nodeId, props.project.ref.key, inputSnapshot(), envFiles.value, profiles.value)
    preview.value = result
    notice.value = result.diff ? '配置已在节点原项目目录中解析，可检查影响后再应用。' : '解析成功，没有检测到实际服务模型变化。'
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : 'Compose 预览失败。'
    preview.value = null
  }
}

async function applyChanges() {
  if (!preview.value || !hasChanges.value) return
  const services = preview.value.affectedServices.length ? preview.value.affectedServices.join(', ') : '无容器服务重建'
  if (!window.confirm(`将应用此变更。受影响服务：${services}。配置回滚不包含应用数据卷备份。继续？`)) return
  saving.value = true
  error.value = ''
  notice.value = ''
  try {
    let operation = await composeEditorApi.apply(props.nodeId, props.project.ref.key, inputSnapshot(), envFiles.value, profiles.value)
    latestOperation.value = operation
    for (let attempt = 0; attempt < 900 && (operation.status === 'queued' || operation.status === 'running'); attempt += 1) {
      await new Promise((resolve) => window.setTimeout(resolve, 1000))
      operation = await composeEditorApi.operation(props.nodeId, operation.operationId)
      latestOperation.value = operation
    }
    if (operation.status === 'succeeded' && operation.verified) {
      notice.value = 'Compose 文件、受影响服务及端口已完成实际状态验证。'
      files.value = files.value.map((file) => {
        const saved = preview.value?.files.find((item) => item.path === file.path)
        return saved ? { ...saved } : file
      })
      preview.value = null
    } else if (operation.status === 'unknown') {
      error.value = '操作结果待确认。节点重新连接后，系统会核对实际服务状态；请勿重复提交。'
    } else {
      error.value = `Compose 变更未通过验证（${operation.errorCode || operation.status}）。${operation.rollbackConfirmed ? '配置及服务回滚已确认。' : '回滚状态尚未确认。'}`
    }
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '应用 Compose 变更失败。'
  } finally {
    saving.value = false
  }
}

function resetPortForm() {
  Object.assign(port, { file: '', service: '', target: '', protocol: 'tcp', oldHostIP: '', oldPublished: '', newHostIP: '', newPublished: '' })
}
</script>

<template>
  <section class="editor" aria-labelledby="editor-title">
    <header class="editor-heading">
      <div>
        <p class="eyebrow">Compose 配置变更</p>
        <h2 id="editor-title">安全编辑与端口修改</h2>
        <p class="muted">项目：{{ project.ref.name }} · {{ project.ref.workingDirectory }}</p>
      </div>
      <button type="button" :disabled="loading || !project.configAvailable" @click="loadSource">{{ loading ? '读取中…' : sourceLoaded ? '重新读取源文件' : '读取源文件' }}</button>
    </header>

    <p class="callout">预览和校验使用节点上的原项目目录、多文件顺序、环境文件及 profile。配置回滚不等同于应用数据备份。</p>
    <label class="context-grid">
      <span>环境文件（绝对路径，每行一个）<textarea v-model="envText" rows="2" placeholder="/srv/app/production.env" :disabled="sourceLoaded" /></span>
      <span>Profiles（逗号或换行分隔）<input v-model="profileText" placeholder="例如 monitoring" :disabled="sourceLoaded" /></span>
    </label>

    <p v-if="error" class="message error" role="alert">{{ error }}</p>
    <p v-if="notice" class="message success" role="status">{{ notice }}</p>

    <template v-if="sourceLoaded">
      <div class="file-editor">
        <nav class="file-tabs" aria-label="Compose 源文件">
          <button v-for="file in files" :key="file.path" type="button" :aria-pressed="selectedPath === file.path" @click="selectedPath = file.path">
            {{ file.path }}
          </button>
        </nav>
        <label class="source-label">源文件内容（保存前会做合并配置校验）
          <textarea class="source" :value="selectedFile?.content ?? ''" spellcheck="false" :aria-label="selectedFile?.path ?? 'Compose source'" @input="updateSelectedContent(($event.target as HTMLTextAreaElement).value)" />
        </label>
      </div>

      <details class="port-editor">
        <summary>结构化修改一个端口映射</summary>
        <div class="port-grid">
          <label>配置文件<select v-model="port.file"><option value="">选择文件</option><option v-for="file in project.ref.configFiles" :key="file" :value="file">{{ file }}</option></select></label>
          <label>服务名<input v-model="port.service" placeholder="web" /></label>
          <label>容器目标端口<input v-model="port.target" inputmode="numeric" placeholder="80" /></label>
          <label>协议<select v-model="port.protocol"><option value="tcp">TCP</option><option value="udp">UDP</option></select></label>
          <label>原宿主 IP（可空）<input v-model="port.oldHostIP" placeholder="127.0.0.1" /></label>
          <label>原宿主端口（可空）<input v-model="port.oldPublished" inputmode="numeric" placeholder="18080" /></label>
          <label>新宿主 IP（可空）<input v-model="port.newHostIP" placeholder="127.0.0.1 或 ::1" /></label>
          <label>新宿主端口<input v-model="port.newPublished" inputmode="numeric" placeholder="18081" /></label>
        </div>
        <button type="button" class="secondary" @click="resetPortForm">清空结构化修改</button>
      </details>

      <div class="actions">
        <button type="button" :disabled="saving" @click="previewChanges">{{ saving ? '处理中…' : '预览并校验' }}</button>
        <button type="button" class="primary" :disabled="saving || !hasChanges" @click="applyChanges">{{ saving ? '应用中…' : '应用已预览的变更' }}</button>
      </div>

      <section v-if="preview" class="preview" aria-label="变更预览">
        <h3>预览结果</h3>
        <div class="impact-grid">
          <div><strong>受影响服务</strong><span>{{ preview.affectedServices.length ? preview.affectedServices.join(', ') : '无容器服务重建' }}</span></div>
          <div><strong>应用数据备份</strong><span>{{ preview.dataBackup ? '已备份' : '未备份；请使用应用自身备份' }}</span></div>
        </div>
        <ul><li v-for="item in preview.impact" :key="item">{{ item }}</li></ul>
        <details open><summary>文件差异</summary><pre>{{ preview.diff || '没有文本差异' }}</pre></details>
        <details><summary>解析后的配置（环境变量值已脱敏）</summary><pre>{{ preview.resolvedConfig }}</pre></details>
      </section>
      <p v-if="latestOperation" class="operation-state" role="status">最近任务：{{ latestOperation.operationId }} · {{ latestOperation.status }}{{ latestOperation.errorCode ? ` · ${latestOperation.errorCode}` : '' }}</p>
    </template>
  </section>
</template>

<style scoped>
.editor { --ink: #edf2fa; --muted: #a2b0c4; --line: rgba(160,178,205,.2); --panel: rgba(19,29,46,.9); color: var(--ink); display: grid; gap: .9rem; min-width: 0; }
.editor-heading { align-items: flex-start; display: flex; gap: 1rem; justify-content: space-between; }
.eyebrow { color: #8ab8f3; font-size: .76rem; letter-spacing: .08em; margin: 0; text-transform: uppercase; }
h2 { font-size: clamp(1.15rem, 3vw, 1.5rem); margin: .25rem 0; overflow-wrap: anywhere; }
h3 { margin: 0; }
.muted, .callout { color: var(--muted); margin: 0; overflow-wrap: anywhere; }
.callout { background: rgba(229,148,64,.09); border: 1px solid rgba(229,148,64,.25); border-radius: .7rem; padding: .7rem .85rem; }
button, input, textarea, select { color: inherit; font: inherit; }
button { background: #25364f; border: 1px solid var(--line); border-radius: .65rem; cursor: pointer; min-height: 2.5rem; padding: .5rem .8rem; }
button:disabled { cursor: not-allowed; opacity: .55; }
button:hover:not(:disabled) { background: #314a6d; }
.context-grid, .port-grid, .impact-grid { display: grid; gap: .7rem; grid-template-columns: repeat(2, minmax(0, 1fr)); }
.context-grid > span, .port-grid label, .source-label { color: var(--muted); display: grid; font-size: .83rem; gap: .35rem; min-width: 0; }
input, textarea, select { background: #101b2a; border: 1px solid var(--line); border-radius: .55rem; min-height: 2.5rem; padding: .55rem .7rem; width: 100%; }
input:disabled { opacity: .6; }
.file-editor, .port-editor, .preview { background: var(--panel); border: 1px solid var(--line); border-radius: .8rem; min-width: 0; padding: .8rem; }
.file-tabs { display: flex; gap: .4rem; margin: -.1rem -.1rem .8rem; overflow-x: auto; }
.file-tabs button { flex: 0 0 auto; font-size: .77rem; max-width: min(75vw, 28rem); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.file-tabs button[aria-pressed="true"] { border-color: #6faeff; }
.source { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; min-height: 18rem; resize: vertical; tab-size: 2; white-space: pre; }
.port-editor summary, .preview summary { cursor: pointer; font-weight: 650; padding: .3rem 0 .7rem; }
.port-grid { margin-bottom: .8rem; }
.secondary { font-size: .82rem; }
.actions { display: flex; flex-wrap: wrap; gap: .6rem; }
.primary { background: #176a4a; border-color: rgba(77,211,147,.38); }
.primary:hover:not(:disabled) { background: #20835d; }
.preview { display: grid; gap: .7rem; }
.impact-grid > div { background: rgba(255,255,255,.035); border-radius: .6rem; display: grid; gap: .3rem; min-width: 0; padding: .7rem; }
.impact-grid span { color: var(--muted); overflow-wrap: anywhere; }
.preview ul { color: var(--muted); margin: 0; padding-left: 1.2rem; }
.preview pre { background: #0b1320; border: 1px solid var(--line); border-radius: .55rem; max-height: 24rem; overflow: auto; padding: .7rem; white-space: pre-wrap; overflow-wrap: anywhere; }
.message, .operation-state { border-radius: .65rem; margin: 0; padding: .7rem .85rem; overflow-wrap: anywhere; }
.message.error { background: rgba(190,42,52,.12); color: #ffb8bc; }
.message.success { background: rgba(40,153,95,.12); color: #a2e7be; }
.operation-state { color: var(--muted); font-size: .8rem; }
@media (max-width: 640px) {
  .editor-heading { flex-direction: column; }
  .editor-heading > button, .actions button { width: 100%; }
  .context-grid, .port-grid, .impact-grid { grid-template-columns: 1fr; }
  .source { min-height: 14rem; }
}
</style>
