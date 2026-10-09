<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { composeApi, type ComposeAction, type ComposeOperationResponse, type ComposeProject } from '../compose-api'
import ComposeEditor from './ComposeEditor.vue'

const props = defineProps<{ nodeId: string; nodeName?: string }>()
const actions: ComposeAction[] = ['validate', 'up', 'start', 'stop', 'restart', 'down']
const projects = ref<ComposeProject[]>([])
const loading = ref(false)
const error = ref('')
const notice = ref('')
const activeOperation = ref('')
const activeProjectKey = ref('')
const query = ref('')
const editorProjectKey = ref('')
const projectInputs = reactive<Record<string, { profiles: string; envFiles: string }>>({})

const filteredProjects = computed(() => {
  const needle = query.value.trim().toLocaleLowerCase()
  if (!needle) return projects.value
  return projects.value.filter((project) => [project.ref.name, project.ref.workingDirectory,
    ...project.services.map((service) => service.name), ...project.ref.configFiles]
    .some((value) => value.toLocaleLowerCase().includes(needle)))
})

async function refresh() {
  loading.value = true
  error.value = ''
  try {
    const result = await composeApi.projects(props.nodeId)
    projects.value = result.projects
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : 'Compose 项目暂时无法加载。'
  } finally {
    loading.value = false
  }
}

function isBusy(project: ComposeProject) {
  return activeOperation.value !== '' && activeProjectKey.value === project.ref.key
}

function toggleEditor(project: ComposeProject) {
  editorProjectKey.value = editorProjectKey.value === project.ref.key ? '' : project.ref.key
}

function inputsFor(project: ComposeProject) {
  if (!projectInputs[project.ref.key]) projectInputs[project.ref.key] = { profiles: '', envFiles: '' }
  return projectInputs[project.ref.key]!
}

async function runAction(project: ComposeProject, action: ComposeAction) {
  if (!project.configAvailable) return
  if (action === 'down' && !window.confirm(`确认停止并移除 Compose 项目“${project.ref.name}”的容器和网络？命名卷会保留。`)) return
  activeOperation.value = action
  activeProjectKey.value = project.ref.key
  error.value = ''
  notice.value = ''
  try {
    const input = inputsFor(project)
    const result = await composeApi.action(props.nodeId, project.ref, action, {
      profiles: input.profiles.split(/[\r\n,]+/).map((value) => value.trim()).filter(Boolean),
      envFiles: input.envFiles.split(/\r?\n/).map((value) => value.trim()).filter(Boolean),
    })
    const operation = await waitForOperation(result)
    if (operation.status === 'succeeded' && operation.verified) {
      notice.value = `项目 ${project.ref.name} 的 ${actionLabel(action)} 操作已验证。`
      await refresh()
    } else if (operation.status === 'queued' || operation.status === 'running') {
      notice.value = `项目 ${project.ref.name} 的操作仍在执行；刷新后可查看持久任务状态。`
    } else {
      error.value = operation.status === 'unknown'
        ? '操作结果待确认；NodeDance 将在重新连接后核对实际资源状态。'
        : `Compose 操作未通过实际状态验证（${operation.errorCode || operation.status}）。`
    }
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : 'Compose 操作失败。'
  } finally {
    activeOperation.value = ''
    activeProjectKey.value = ''
  }
}

async function waitForOperation(initial: ComposeOperationResponse): Promise<ComposeOperationResponse> {
  let current = initial
  if (['succeeded', 'failed', 'timed_out', 'unknown'].includes(current.status)) return current
  for (let attempt = 0; attempt < 720; attempt += 1) {
    await new Promise((resolve) => window.setTimeout(resolve, 1000))
    current = await composeApi.operation(props.nodeId, current.operationId)
    if (['succeeded', 'failed', 'timed_out', 'unknown'].includes(current.status)) return current
  }
  return current
}

function actionLabel(action: ComposeAction) {
  return ({ validate: '校验配置', up: '应用并启动', start: '启动', stop: '停止', restart: '重启', down: '停止并移除' } as const)[action]
}

function actionDescription(action: ComposeAction) {
  if (action === 'down') return '移除项目容器和网络，保留命名卷及镜像。'
  if (action === 'up') return '按当前配置应用服务，并核验真实实例状态。'
  if (action === 'validate') return '按目录、文件顺序、环境文件和 profile 校验配置。'
  return '只作用于这个 Compose 项目，不会删除数据卷。'
}

onMounted(() => { void refresh() })
</script>

<template>
  <section class="compose-page" aria-labelledby="compose-title">
    <header class="compose-header">
      <div>
        <p class="eyebrow">{{ nodeName || '节点' }} · Docker Compose</p>
        <h1 id="compose-title">Compose 项目</h1>
        <p class="lede">按真实项目目录、配置文件顺序和服务实例管理现有项目。</p>
      </div>
      <button class="refresh-button" type="button" :disabled="loading" @click="refresh">
        {{ loading ? '刷新中…' : '刷新项目' }}
      </button>
    </header>

    <p v-if="error" class="notice error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice success" role="status">{{ notice }}</p>

    <label class="search-field">
      <span>搜索项目、目录或服务</span>
      <input v-model="query" type="search" autocomplete="off" placeholder="例如 nd-demo 或 web" />
    </label>

    <div v-if="!loading && !filteredProjects.length" class="empty-state">
      <strong>{{ query ? '没有匹配的 Compose 项目' : '尚未发现 Compose 项目' }}</strong>
      <span>NodeDance 从 Docker Engine 的 Compose 标签发现项目，不要求项目由 NodeDance 创建。</span>
    </div>

    <article v-for="project in filteredProjects" :key="project.ref.key" class="project-card">
      <div class="project-heading">
        <div class="project-title">
          <span class="project-icon" aria-hidden="true">▦</span>
          <div>
            <h2>{{ project.ref.name }}</h2>
            <p class="project-path" :title="project.ref.workingDirectory">{{ project.ref.workingDirectory }}</p>
          </div>
        </div>
        <span class="source-state" :class="project.configAvailable ? 'available' : 'missing'">
          {{ project.configAvailable ? '配置可用' : '配置文件缺失' }}
        </span>
        <span v-if="project.dataStale" class="source-state missing">数据已过期</span>
      </div>

      <div v-if="!project.configAvailable" class="missing-source" role="status">
        <strong>项目编排功能已停用</strong>
        <span>源配置不可读取；容器和服务实例仍可从 Docker 页面单独查看与管理。</span>
      </div>

      <div class="config-files" aria-label="Compose 配置文件顺序">
        <span class="subheading">配置文件（按合并顺序）</span>
        <ol>
          <li v-for="file in project.ref.configFiles" :key="file" :title="file">{{ file }}</li>
        </ol>
      </div>

      <div class="compose-options">
        <label>
          <span>启用 profile（逗号或换行分隔）</span>
          <input v-model="inputsFor(project).profiles" type="text" autocomplete="off" placeholder="例如 monitoring, worker" />
        </label>
        <label>
          <span>环境文件（每行一个绝对路径，按顺序合并）</span>
          <textarea v-model="inputsFor(project).envFiles" rows="2" autocomplete="off" placeholder="/srv/app/production.env" />
        </label>
      </div>

      <div class="services-list">
        <h3>服务与实例 <span>{{ project.services.reduce((count, service) => count + service.instances.length, 0) }}</span></h3>
        <div v-if="!project.services.length" class="service-empty">当前没有容器实例；项目仍可通过其有效配置启动。</div>
        <section v-for="service in project.services" :key="service.name" class="service-row">
          <div class="service-name"><strong>{{ service.name }}</strong><span>{{ service.instances.length }} 个实例</span></div>
          <ul class="instance-list">
            <li v-for="instance in service.instances" :key="instance.containerId">
              <span class="state-mark" :class="instance.state === 'running' ? 'running' : 'stopped'" aria-hidden="true"></span>
              <span class="instance-name">{{ instance.containerName }}</span>
              <span class="instance-state">{{ instance.state }}<template v-if="instance.health && instance.health !== 'none'"> · {{ instance.health }}</template></span>
              <code>{{ instance.containerId.slice(0, 12) }}</code>
            </li>
          </ul>
        </section>
      </div>

      <section v-if="editorProjectKey === project.ref.key" class="editor-panel">
        <ComposeEditor :node-id="nodeId" :project="project" />
      </section>

      <footer class="project-actions" :aria-label="`${project.ref.name} 操作`">
        <div v-for="action in actions" :key="action" class="action-control">
          <button type="button" :class="{ danger: action === 'down' }"
            :disabled="!project.configAvailable || loading || (activeOperation !== '' && !isBusy(project)) || isBusy(project)"
            @click="runAction(project, action)">
            {{ isBusy(project) && activeOperation === action ? '执行中…' : actionLabel(action) }}
          </button>
          <small>{{ actionDescription(action) }}</small>
        </div>
        <div class="action-control">
          <button type="button" :disabled="!project.configAvailable || loading || (activeOperation !== '' && !isBusy(project))"
            :aria-expanded="editorProjectKey === project.ref.key" @click="toggleEditor(project)">
            {{ editorProjectKey === project.ref.key ? '关闭配置编辑器' : '编辑配置与端口' }}
          </button>
          <small>预览合并配置、受影响服务和回滚范围后再提交。</small>
        </div>
      </footer>
    </article>
  </section>
</template>

<style scoped>
.compose-page { --ink: #edf2fa; --muted: #9aa8bd; --line: rgba(160,178,205,.16); --panel: rgba(19,29,46,.88); color: var(--ink); display: grid; gap: 1.1rem; margin: 0 auto; max-width: 1120px; padding: clamp(1rem, 3vw, 2rem); }
.compose-header { align-items: center; display: flex; gap: 1rem; justify-content: space-between; }
.eyebrow, .lede, .project-path { color: var(--muted); margin: 0; }
.eyebrow { font-size: .76rem; letter-spacing: .08em; text-transform: uppercase; }
h1 { font-size: clamp(1.5rem, 4vw, 2rem); margin: .35rem 0; }
.lede { font-size: .94rem; }
button, input { font: inherit; }
button { background: #21314a; border: 1px solid var(--line); border-radius: .7rem; color: var(--ink); cursor: pointer; min-height: 2.65rem; padding: .55rem .9rem; }
button:hover:not(:disabled) { background: #2d4362; }
button:disabled { cursor: not-allowed; opacity: .55; }
.refresh-button { white-space: nowrap; }
.search-field { display: grid; gap: .45rem; max-width: 30rem; }
.search-field span, .subheading { color: var(--muted); font-size: .84rem; }
.search-field input { background: var(--panel); border: 1px solid var(--line); border-radius: .65rem; color: var(--ink); min-height: 2.8rem; padding: .6rem .8rem; width: 100%; }
.compose-options { display: grid; gap: .8rem; grid-template-columns: repeat(2, minmax(0, 1fr)); }
.compose-options label { color: var(--muted); display: grid; font-size: .84rem; gap: .4rem; min-width: 0; }
.compose-options input, .compose-options textarea { background: var(--panel); border: 1px solid var(--line); border-radius: .65rem; color: var(--ink); font: inherit; min-height: 2.7rem; padding: .6rem .75rem; width: 100%; }
.compose-options textarea { resize: vertical; }
.notice, .empty-state, .project-card { background: var(--panel); border: 1px solid var(--line); border-radius: 1rem; }
.notice { margin: 0; padding: .8rem 1rem; }
.notice.error { border-color: rgba(248,113,113,.45); color: #ffb9b9; }
.notice.success { border-color: rgba(72,187,120,.4); color: #a8e7c0; }
.empty-state { color: var(--muted); display: grid; gap: .45rem; padding: 1.5rem; }
.empty-state strong { color: var(--ink); }
.project-card { display: grid; gap: 1rem; padding: clamp(.9rem, 2.3vw, 1.4rem); }
.project-heading, .project-title, .service-row, .instance-list li, .project-actions { align-items: center; display: flex; gap: .8rem; }
.project-heading { justify-content: space-between; }
.project-title { min-width: 0; }
.project-icon { align-items: center; background: #1f3653; border-radius: .7rem; color: #86b9ff; display: inline-flex; flex: 0 0 2.5rem; height: 2.5rem; justify-content: center; }
.project-title h2 { font-size: 1.08rem; margin: 0 0 .25rem; }
.project-path { font-size: .82rem; max-width: min(70vw, 50rem); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.source-state { border-radius: 999px; flex: 0 0 auto; font-size: .76rem; padding: .35rem .65rem; }
.source-state.available { background: rgba(42,155,105,.14); color: #8ee0b2; }
.source-state.missing { background: rgba(229,148,64,.14); color: #ffc881; }
.missing-source { background: rgba(229,148,64,.08); border: 1px solid rgba(229,148,64,.22); border-radius: .7rem; color: #f0cf9f; display: grid; gap: .3rem; padding: .75rem .9rem; }
.missing-source span { color: var(--muted); font-size: .86rem; }
.config-files { display: grid; gap: .45rem; }
.config-files ol { color: #c5d1e2; display: grid; gap: .25rem; margin: 0; padding-left: 1.35rem; }
.config-files li { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .79rem; overflow-wrap: anywhere; }
.services-list { border-top: 1px solid var(--line); display: grid; gap: .6rem; padding-top: .9rem; }
.editor-panel { border-top: 1px solid var(--line); min-width: 0; padding-top: .9rem; }
.services-list h3 { align-items: center; display: flex; font-size: .96rem; gap: .5rem; margin: 0; }
.services-list h3 span, .service-name span { color: var(--muted); font-size: .78rem; font-weight: 400; }
.service-empty { color: var(--muted); font-size: .86rem; }
.service-row { align-items: flex-start; border-top: 1px solid rgba(160,178,205,.09); flex-wrap: wrap; padding: .6rem 0 .2rem; }
.service-name { align-items: baseline; display: flex; flex: 0 0 11rem; gap: .5rem; padding-top: .22rem; }
.instance-list { display: grid; flex: 1; gap: .38rem; list-style: none; margin: 0; min-width: min(100%, 25rem); padding: 0; }
.instance-list li { color: #c5d1e2; font-size: .79rem; min-width: 0; }
.state-mark { background: #718096; border-radius: 50%; flex: 0 0 .5rem; height: .5rem; }
.state-mark.running { background: #54d39a; box-shadow: 0 0 0 3px rgba(84,211,154,.12); }
.instance-name { flex: 1; min-width: 4rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.instance-state { color: var(--muted); }
code { color: #8ca0bd; font-size: .72rem; }
.project-actions { border-top: 1px solid var(--line); display: grid; gap: .7rem; grid-template-columns: repeat(auto-fit, minmax(9rem, 1fr)); padding-top: .9rem; }
.action-control { display: grid; gap: .3rem; min-width: 0; }
.action-control small { color: var(--muted); font-size: .72rem; line-height: 1.35; }
.project-actions .danger { border-color: rgba(238,105,105,.35); color: #ffb1b1; }
@media (max-width: 680px) {
  .compose-header { align-items: flex-start; flex-direction: column; }
  .refresh-button { width: 100%; }
  .project-heading { align-items: flex-start; flex-direction: column; }
  .project-path { max-width: calc(100vw - 5rem); }
  .service-name { flex-basis: 100%; }
  .instance-list { min-width: 100%; }
  .instance-state { max-width: 7rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .compose-options { grid-template-columns: minmax(0, 1fr); }
  .project-actions { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
</style>
