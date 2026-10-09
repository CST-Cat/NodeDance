<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { api, type ContainerRebuildPlan, type ContainerTask, type DockerContainer, type RebuildPortBinding, type RebuildSpec } from '../api'

const props = defineProps<{
  nodeId: string
  container: DockerContainer
  stale: boolean
  tasks: ContainerTask[]
}>()

const opened = ref(false)
const mode = ref<'keep' | 'replace' | 'clear'>('keep')
const bindings = ref<RebuildPortBinding[]>([])
const plan = ref<ContainerRebuildPlan>()
const task = ref<ContainerTask>()
const acknowledged = ref(false)
const loadingPlan = ref(false)
const submitting = ref(false)
const cleaning = ref(false)
const error = ref('')
let pollTimer: ReturnType<typeof setTimeout> | undefined
let generation = 0

const taskMessage = computed(() => {
  if (!task.value) return ''
  if (task.value.status === 'queued') return '任务已保存，等待 Agent。'
  if (task.value.status === 'running') return `正在执行：${task.value.progress.phase || '重建中'}。`
  if (task.value.status === 'succeeded') return task.value.result.observedState === 'rollback_container_removed_snapshot_retained'
    ? '重建已验证；旧容器已清理，正在使用的可写层快照保留为新容器镜像。'
    : '重建已验证完成。'
  if (task.value.status === 'unknown') return '结果待确认；NodeDance 不会自动重放此操作。'
  if (task.value.status === 'timed_out') return '任务超时，实际结果待重新确认。'
  if (task.value.status === 'canceled') return '任务已取消。'
  if ((task.value.result.observedState ?? '').startsWith('restored:')) {
    return `重建失败，原容器已恢复：${(task.value.result.observedState ?? '').slice('restored:'.length)}。`
  }
  return `重建失败：${task.value.result.resourceRevision || task.value.result.code || '原因未分类'}。`
})

const previousTask = computed(() => props.tasks.find((item) =>
  item.action === 'rebuild' && item.result.resourceRevision === props.container.id &&
    (item.status === 'succeeded' || item.status === 'failed' && (item.result.observedState ?? '').startsWith('restored:')),
))
const cleanupDone = computed(() => props.tasks.some((item) =>
  item.action === 'rebuild_cleanup' && item.targetId === props.container.id && item.status === 'succeeded',
 ) || task.value?.action === 'rebuild_cleanup' && task.value.targetId === props.container.id && task.value.status === 'succeeded')
const cleanupTask = computed(() => task.value?.action === 'rebuild' && task.value.result.resourceRevision === props.container.id &&
  (task.value.status === 'succeeded' || task.value.status === 'failed' && (task.value.result.observedState ?? '').startsWith('restored:'))
  ? task.value : previousTask.value)
const cleanupRestoredFailure = computed(() => cleanupTask.value?.status === 'failed' && (cleanupTask.value.result.observedState ?? '').startsWith('restored:'))

watch(() => task.value?.taskId, () => {
  if (pollTimer) clearTimeout(pollTimer)
  if (task.value && ['queued', 'running', 'unknown'].includes(task.value.status)) schedulePoll(task.value.taskId, generation)
})

function taskTerminal(value: ContainerTask): boolean {
  return ['succeeded', 'failed', 'timed_out', 'canceled'].includes(value.status)
}

function specFromForm(): RebuildSpec {
  if (mode.value === 'clear') return { clearPortBindings: true }
  if (mode.value === 'keep') return {}
  return { portBindings: bindings.value.map((binding) => ({
    containerPort: binding.containerPort.trim(),
    hostIp: binding.hostIp?.trim() || undefined,
    hostPort: binding.hostPort?.trim() || undefined,
  })) }
}

function addBinding() {
  mode.value = 'replace'
  bindings.value = [...bindings.value, { containerPort: '', hostIp: '', hostPort: '' }]
  plan.value = undefined
}

function removeBinding(index: number) {
  mode.value = 'replace'
  bindings.value = bindings.value.filter((_, row) => row !== index)
  plan.value = undefined
}

async function openWizard() {
  if (props.stale || props.container.stale || props.container.compose || loadingPlan.value) return
  opened.value = true
  mode.value = 'keep'
  bindings.value = []
  plan.value = undefined
  task.value = undefined
  acknowledged.value = false
  error.value = ''
  await preview()
}

async function preview() {
  loadingPlan.value = true
  error.value = ''
  try {
    const result = await api.planContainerRebuild(props.nodeId, props.container.id, specFromForm())
    plan.value = result
    acknowledged.value = false
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法生成重建计划。'
    plan.value = undefined
  } finally {
    loadingPlan.value = false
  }
}

async function waitForTask(taskId: string, requestGeneration: number) {
  try {
    const updated = await api.nodeTask(props.nodeId, taskId)
    if (requestGeneration !== generation) return
    task.value = updated
    if (!taskTerminal(updated) && updated.status !== 'unknown') schedulePoll(taskId, requestGeneration)
  } catch (reason) {
    if (requestGeneration === generation) error.value = reason instanceof Error ? reason.message : '无法读取重建任务状态。'
  }
}

function schedulePoll(taskId: string, requestGeneration: number) {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = setTimeout(() => { void waitForTask(taskId, requestGeneration) }, 1200)
}

async function submitRebuild() {
  if (!plan.value || !acknowledged.value || submitting.value || props.stale || props.container.stale) return
  const confirmation = window.prompt(`NodeDance 将短暂停止并重建此独立容器。请输入完整容器 ID 确认：\n${props.container.id}`)
  if (confirmation === null) return
  if (confirmation.trim() !== props.container.id) {
    error.value = '未提交：确认内容必须与当前完整容器 ID 完全一致。'
    return
  }
  submitting.value = true
  error.value = ''
  const requestGeneration = ++generation
  try {
    const accepted = await api.createContainerTask(props.nodeId, props.container.id,
      { action: 'rebuild', rebuild: specFromForm() }, crypto.randomUUID())
    task.value = await api.nodeTask(props.nodeId, accepted.taskId)
    if (!taskTerminal(task.value) && task.value.status !== 'unknown') schedulePoll(accepted.taskId, requestGeneration)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法创建受控重建任务。'
  } finally {
    submitting.value = false
  }
}

async function cleanupRollbackResources() {
  const rollback = cleanupTask.value
  if (!rollback || cleanupDone.value || cleaning.value || props.stale || props.container.stale) return
  const cleanupDescription = cleanupRestoredFailure.value
    ? '此操作只清理失败重建遗留且已确认归本任务所有的快照。已恢复的原容器保持不变，数据卷不会删除。'
    : '此操作只清理已停止的回滚容器。数据卷不会删除；正在使用的可写层快照会保留。'
  const confirmation = window.prompt(`${cleanupDescription}\n请输入当前完整容器 ID：\n${props.container.id}`)
  if (confirmation === null) return
  if (confirmation.trim() !== props.container.id) {
    error.value = '未清理：确认内容必须与当前完整容器 ID 完全一致。'
    return
  }
  cleaning.value = true
  error.value = ''
  try {
    const accepted = await api.createContainerTask(props.nodeId, props.container.id, {
      action: 'rebuild_cleanup',
      rebuild: { cleanupTaskId: rollback.taskId },
      confirmationId: props.container.id,
    }, crypto.randomUUID())
    const updated = await api.nodeTask(props.nodeId, accepted.taskId)
    task.value = updated
    if (updated.status === 'queued' || updated.status === 'running') schedulePoll(accepted.taskId, ++generation)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法清理回滚资源。'
  } finally {
    cleaning.value = false
  }
}

onBeforeUnmount(() => {
  generation++
  if (pollTimer) clearTimeout(pollTimer)
})
</script>

<template>
  <div class="rebuild-entry">
    <button class="container-action" type="button" :disabled="stale || container.stale || Boolean(container.compose)" :title="container.compose ? 'Compose 容器请使用 Compose 项目操作' : '先检查配置与风险，再提交受控重建'" @click="openWizard">
      受控重建
    </button>
    <span v-if="task" class="container-task-status" data-testid="rebuild-task-status" :data-status="task.status" role="status">{{ taskMessage }}</span>
    <button v-if="cleanupTask && !cleanupDone" class="container-action" type="button" :disabled="stale || container.stale || cleaning" @click="cleanupRollbackResources">
      {{ cleaning ? '清理中…' : cleanupRestoredFailure ? '清理重建遗留资源' : '清理回滚容器' }}
    </button>
    <span v-if="cleanupDone" class="container-task-status" role="status">重建遗留回滚资源已清理，数据卷保留。</span>
    <section v-if="opened" class="rebuild-dialog" role="dialog" aria-modal="false" aria-labelledby="rebuild-title" data-testid="container-rebuild-wizard">
      <header class="rebuild-heading">
        <div><span class="eyebrow">CONTROLLED REBUILD</span><h3 id="rebuild-title">重建 {{ container.name || container.id.slice(0, 12) }}</h3></div>
        <button class="container-action" type="button" :disabled="submitting" @click="opened = false">关闭</button>
      </header>
      <p class="rebuild-intro">计划由 Agent 对当前 Docker Inspect 生成。确认停机和数据风险后才会创建持久任务。</p>
      <label class="rebuild-mode">端口设置
        <select v-model="mode" :disabled="submitting" @change="plan = undefined">
          <option value="keep">保留当前端口</option>
          <option value="replace">替换端口映射</option>
          <option value="clear">清除所有发布端口</option>
        </select>
      </label>
      <div v-if="mode === 'replace'" class="rebuild-bindings">
        <div v-for="(binding, index) in bindings" :key="index" class="rebuild-binding" :data-testid="`rebuild-binding-${index}`">
          <label>容器端口 <input v-model="binding.containerPort" :aria-label="`容器端口 ${index + 1}`" placeholder="80/tcp" @input="plan = undefined"></label>
          <label>主机 IP <input v-model="binding.hostIp" :aria-label="`主机 IP ${index + 1}`" placeholder="127.0.0.1 或留空" @input="plan = undefined"></label>
          <label>主机端口 <input v-model="binding.hostPort" :aria-label="`主机端口 ${index + 1}`" inputmode="numeric" placeholder="18080" @input="plan = undefined"></label>
          <button class="container-action" type="button" :aria-label="`移除端口映射 ${index + 1}`" @click="removeBinding(index)">移除</button>
        </div>
        <button class="container-action" type="button" @click="addBinding">添加端口映射</button>
      </div>
      <button class="container-action" type="button" :disabled="loadingPlan || submitting" @click="preview">{{ loadingPlan ? '正在检查…' : '重新生成计划' }}</button>
      <p v-if="error" class="rebuild-error" role="alert">{{ error }}</p>
      <p v-if="loadingPlan" class="rebuild-wait" role="status">正在向 Agent 请求最新配置并检查可保留项目。</p>
      <div v-if="plan" class="rebuild-plan" data-testid="rebuild-plan">
        <dl class="rebuild-facts">
          <div><dt>镜像 ID</dt><dd>{{ plan.imageId }}</dd></div>
          <div><dt>当前状态</dt><dd>{{ plan.wasRunning ? '运行中，重建期间会停止' : '已停止，完成后保持停止' }}</dd></div>
          <div><dt>可写层</dt><dd>{{ plan.writableLayerBytes }} bytes{{ plan.snapshotRequired ? '，重建前创建本地快照' : '，无需快照' }}</dd></div>
        </dl>
        <p><strong>停机影响：</strong>{{ plan.downtime }}</p>
        <div class="rebuild-columns">
          <div><h4>保留</h4><ul><li v-for="item in plan.preserved" :key="item">{{ item }}</li></ul></div>
          <div><h4>变化</h4><ul><li v-for="item in plan.changed" :key="item">{{ item }}</li></ul></div>
        </div>
        <div class="rebuild-columns">
          <div><h4>端口变更前</h4><ul><li v-for="item in plan.portsBefore" :key="item">{{ item }}</li><li v-if="!plan.portsBefore.length">无已发布端口</li></ul></div>
          <div><h4>端口变更后</h4><ul><li v-for="item in plan.portsAfter" :key="item">{{ item }}</li><li v-if="!plan.portsAfter.length">无已发布端口</li></ul></div>
        </div>
        <h4>挂载（不作为快照备份）</h4>
        <ul><li v-for="mount in plan.mounts" :key="`${mount.type}-${mount.destination}`">{{ mount.type }} → {{ mount.destination }}（{{ mount.readWrite ? '可写' : '只读' }}）</li><li v-if="!plan.mounts.length">无挂载</li></ul>
        <h4>风险和限制</h4>
        <ul class="rebuild-risks"><li v-for="item in plan.risks" :key="item">{{ item }}</li></ul>
        <label class="rebuild-ack"><input v-model="acknowledged" type="checkbox" :disabled="submitting">我已检查停机影响，理解快照不包含挂载卷和 bind mount 数据。</label>
        <button class="container-action container-action-danger" type="button" data-testid="submit-container-rebuild" :disabled="!acknowledged || submitting || stale || container.stale" @click="submitRebuild">
          {{ submitting ? '正在提交…' : '提交受控重建' }}
        </button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.rebuild-entry { display: contents; }
.rebuild-dialog { flex: 1 0 100%; min-width: 0; margin-top: 8px; border: 1px solid rgba(125, 165, 218, .28); border-radius: 10px; padding: clamp(12px, 2.4vw, 18px); background: rgba(6, 13, 23, .94); color: #eaf0fa; }
.rebuild-heading { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.rebuild-heading h3 { margin: 5px 0; font-size: 16px; }
.rebuild-intro, .rebuild-wait { color: #a8b7cc; font-size: 12px; line-height: 1.6; }
.rebuild-mode { display: grid; gap: 6px; max-width: 340px; margin: 12px 0; color: #cad5e5; font-size: 12px; }
.rebuild-mode select, .rebuild-binding input { width: 100%; min-width: 0; border: 1px solid rgba(171, 196, 232, .2); border-radius: 7px; padding: 8px 9px; color: #edf3fc; background: #121d2d; font: inherit; }
.rebuild-bindings { display: grid; gap: 8px; margin-bottom: 10px; }
.rebuild-binding { display: grid; grid-template-columns: 1fr 1.2fr 1fr auto; align-items: end; gap: 8px; }
.rebuild-binding label { display: grid; gap: 5px; color: #aebbd0; font-size: 10px; }
.rebuild-plan { margin-top: 12px; border-top: 1px solid rgba(171, 196, 232, .12); padding-top: 12px; font-size: 11px; line-height: 1.6; overflow-wrap: anywhere; }
.rebuild-plan h4 { margin: 12px 0 5px; color: #dce6f5; font-size: 11px; }
.rebuild-facts { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; margin: 0; }
.rebuild-facts div { min-width: 0; border: 1px solid rgba(171, 196, 232, .1); border-radius: 7px; padding: 8px; }
.rebuild-facts dt { color: #9aabc1; font-size: 9px; }
.rebuild-facts dd { margin: 4px 0 0; overflow-wrap: anywhere; }
.rebuild-columns { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.rebuild-plan ul { margin: 4px 0; padding-left: 18px; color: #c1ccdc; }
.rebuild-risks { color: #ffd092 !important; }
.rebuild-ack { display: flex; gap: 8px; align-items: flex-start; margin: 14px 0; color: #ffd092; }
.rebuild-ack input { flex: 0 0 auto; margin-top: 3px; }
.rebuild-error { color: #ffc1b8; }
@media (max-width: 600px) { .rebuild-binding { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); } .rebuild-facts { grid-template-columns: 1fr; } .rebuild-columns { grid-template-columns: 1fr; } }
</style>
