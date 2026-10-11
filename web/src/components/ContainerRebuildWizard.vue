<script setup lang="ts">
import { requestPrompt } from '../dialogs'
import { t, display, locale } from "../i18n"
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
  if (task.value.status === 'queued') return t('m.ab502a2e4167')
  if (task.value.status === 'running') return t('m.dc8605450aaa', {"0": (task.value.progress.phase || t('m.0a68e6a99abe'))})
  if (task.value.status === 'succeeded') return task.value.result.observedState === 'rollback_container_removed_snapshot_retained'
    ? t('m.efcd16dd368b')
    : t('m.3c2ee110bf07')
  if (task.value.status === 'unknown') return t('m.5013ec7ae306')
  if (task.value.status === 'timed_out') return t('m.3e78f8c0e01b')
  if (task.value.status === 'canceled') return t('m.d7df27ca8d31')
  if ((task.value.result.observedState ?? '').startsWith('restored:')) {
    return t('m.c4433033c3ce', {"0": ((task.value.result.observedState ?? '').slice('restored:'.length))})
  }
  return t('m.bab93f6cf8a1', {"0": (task.value.result.resourceRevision || task.value.result.code || t('m.fb2425b0c8e1'))})
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
    error.value = reason instanceof Error ? reason.message : t('m.8dc5598b2ee8')
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
    if (requestGeneration === generation) error.value = reason instanceof Error ? reason.message : t('m.b22473f5734c')
  }
}

function schedulePoll(taskId: string, requestGeneration: number) {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = setTimeout(() => { void waitForTask(taskId, requestGeneration) }, 1200)
}

async function submitRebuild() {
  if (!plan.value || !acknowledged.value || submitting.value || props.stale || props.container.stale) return
  const confirmation = await requestPrompt(t('m.751a2318fd6e', {"0": (props.container.id)}))
  if (confirmation === null) return
  if (confirmation.trim() !== props.container.id) {
    error.value = t('m.25b38b35f7a5')
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
    error.value = reason instanceof Error ? reason.message : t('m.8beb6052b0fc')
  } finally {
    submitting.value = false
  }
}

async function cleanupRollbackResources() {
  const rollback = cleanupTask.value
  if (!rollback || cleanupDone.value || cleaning.value || props.stale || props.container.stale) return
  const cleanupDescription = cleanupRestoredFailure.value
    ? t('m.91f8944e8aa7')
    : t('m.fd78403a0172')
  const confirmation = await requestPrompt(t('m.c9dab8fa99dd', {"0": (cleanupDescription), "1": (props.container.id)}))
  if (confirmation === null) return
  if (confirmation.trim() !== props.container.id) {
    error.value = t('m.af34e98d4a24')
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
    error.value = reason instanceof Error ? reason.message : t('m.ffeb5f93c877')
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
    <button class="container-action" type="button" :disabled="stale || container.stale || Boolean(container.compose)" :title="container.compose ? t('m.0c6674d92b52') : t('m.f754d1c47f3e')" @click="openWizard">{{ t('m.e6953475e88d') }}</button>
    <span v-if="task" class="container-task-status" data-testid="rebuild-task-status" :data-status="task.status" role="status">{{ display(taskMessage) }}</span>
    <button v-if="cleanupTask && !cleanupDone" class="container-action" type="button" :disabled="stale || container.stale || cleaning" @click="cleanupRollbackResources">
      {{ cleaning ? t('m.248e8cfc2116') : cleanupRestoredFailure ? t('m.606302a043c7') : t('m.4e188c5690ca') }}
    </button>
    <span v-if="cleanupDone" class="container-task-status" role="status">{{ t('m.d36c4aa4610f') }}</span>
    <section v-if="opened" class="rebuild-dialog" role="dialog" aria-modal="false" aria-labelledby="rebuild-title" data-testid="container-rebuild-wizard">
      <header class="rebuild-heading">
        <div><span class="eyebrow">{{ t('label.08671c7919d1') }}</span><h3 id="rebuild-title">{{ t('m.4b8d9d4f3850') }}{{ container.name || container.id.slice(0, 12) }}</h3></div>
        <button class="container-action" type="button" :disabled="submitting" @click="opened = false">{{ t('m.3fd47edce45b') }}</button>
      </header>
      <p class="rebuild-intro">{{ t('m.11edce34ba7c') }}</p>
      <label class="rebuild-mode">{{ t('m.54644705e9c6') }}<select v-model="mode" :disabled="submitting" @change="plan = undefined">
          <option value="keep">{{ t('m.841bbfd5aeae') }}</option>
          <option value="replace">{{ t('m.1f329f154394') }}</option>
          <option value="clear">{{ t('m.7c32fb37345f') }}</option>
        </select>
      </label>
      <div v-if="mode === 'replace'" class="rebuild-bindings">
        <div v-for="(binding, index) in bindings" :key="index" class="rebuild-binding" :data-testid="`rebuild-binding-${index}`">
          <label>{{ t('m.9c9980ac48fe') }}<input v-model="binding.containerPort" :aria-label="t('m.99af036931c4', {'0': (index + 1)})" placeholder="80/tcp" @input="plan = undefined"></label>
          <label>{{ t('m.7782666b6ce5') }}<input v-model="binding.hostIp" :aria-label="t('m.c8c0858dd7d7', {'0': (index + 1)})" :placeholder="t('m.50f9c5e2f0a1')" @input="plan = undefined"></label>
          <label>{{ t('m.5c474b41c45e') }}<input v-model="binding.hostPort" :aria-label="t('m.c6e031cb1c5f', {'0': (index + 1)})" inputmode="numeric" placeholder="18080" @input="plan = undefined"></label>
          <button class="container-action" type="button" :aria-label="t('m.6ccdc5171a37', {'0': (index + 1)})" @click="removeBinding(index)">{{ t('m.6135d4159e89') }}</button>
        </div>
        <button class="container-action" type="button" @click="addBinding">{{ t('m.3681abca77fd') }}</button>
      </div>
      <button class="container-action" type="button" :disabled="loadingPlan || submitting" @click="preview">{{ loadingPlan ? t('m.6b72c3d6855c') : t('m.fda000ea51ab') }}</button>
      <p v-if="error" class="rebuild-error" role="alert">{{ display(error) }}</p>
      <p v-if="loadingPlan" class="rebuild-wait" role="status">{{ t('m.c6e3dd69d100') }}</p>
      <div v-if="plan" class="rebuild-plan" data-testid="rebuild-plan">
        <dl class="rebuild-facts">
          <div><dt>{{ t('m.eed0f73e6ed4') }}</dt><dd>{{ plan.imageId }}</dd></div>
          <div><dt>{{ t('m.82b7cf40b5e6') }}</dt><dd>{{ plan.wasRunning ? t('m.ad5a3b7023cc') : t('m.7942087a70a9') }}</dd></div>
          <div><dt>{{ t('m.07085db02b1f') }}</dt><dd>{{ plan.writableLayerBytes }} bytes{{ plan.snapshotRequired ? t('m.0c661a382e4c') : t('m.d9dae035a677') }}</dd></div>
        </dl>
        <p><strong>{{ t('m.9ffd3498126c') }}</strong>{{ plan.downtime }}</p>
        <div class="rebuild-columns">
          <div><h4>{{ t('m.670ec25af841') }}</h4><ul><li v-for="item in plan.preserved" :key="item">{{ item }}</li></ul></div>
          <div><h4>{{ t('m.e329c44530ee') }}</h4><ul><li v-for="item in plan.changed" :key="item">{{ item }}</li></ul></div>
        </div>
        <div class="rebuild-columns">
          <div><h4>{{ t('m.6d355bd867cf') }}</h4><ul><li v-for="item in plan.portsBefore" :key="item">{{ item }}</li><li v-if="!plan.portsBefore.length">{{ t('m.cf159c2050f5') }}</li></ul></div>
          <div><h4>{{ t('m.f0754c848ad6') }}</h4><ul><li v-for="item in plan.portsAfter" :key="item">{{ item }}</li><li v-if="!plan.portsAfter.length">{{ t('m.cf159c2050f5') }}</li></ul></div>
        </div>
        <h4>{{ t('m.a68527cc9644') }}</h4>
        <ul><li v-for="mount in plan.mounts" :key="`${mount.type}-${mount.destination}`">{{ mount.type }} → {{ mount.destination }}（{{ mount.readWrite ? t('m.a3bc3e79fc0d') : t('m.3b5ec3533b0e') }}）</li><li v-if="!plan.mounts.length">{{ t('m.a687446f9cb9') }}</li></ul>
        <h4>{{ t('m.c006769c23af') }}</h4>
        <ul class="rebuild-risks"><li v-for="item in plan.risks" :key="item">{{ item }}</li></ul>
        <label class="rebuild-ack"><input v-model="acknowledged" type="checkbox" :disabled="submitting">{{ t('m.2af7ec0d2b15') }}</label>
        <button class="container-action container-action-danger" type="button" data-testid="submit-container-rebuild" :disabled="!acknowledged || submitting || stale || container.stale" @click="submitRebuild">
          {{ submitting ? t('m.26ef00b7d061') : t('m.be23bb9aeb97') }}
        </button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.rebuild-entry { display: contents; }
.rebuild-dialog { flex: 1 0 100%; min-width: 0; margin-top: 8px; border: 1px solid var(--line); border-radius:var(--radius-card); padding: clamp(12px, 2.4vw, 18px); background: var(--surface-soft); color: var(--text); }
.rebuild-heading { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.rebuild-heading h3 { margin: 5px 0; font-size: 16px; }
.rebuild-intro, .rebuild-wait { color: var(--muted-strong); font-size: 12px; line-height: 1.6; }
.rebuild-mode { display: grid; gap: 6px; max-width: 340px; margin: 12px 0; color: var(--text); font-size: 12px; }
.rebuild-mode select, .rebuild-binding input { width: 100%; min-width: 0; border: 1px solid var(--line); border-radius:var(--radius-control); padding: 8px 9px; color: var(--text); background: var(--surface-soft); font: inherit; }
.rebuild-bindings { display: grid; gap: 8px; margin-bottom: 10px; }
.rebuild-binding { display: grid; grid-template-columns: 1fr 1.2fr 1fr auto; align-items: end; gap: 8px; }
.rebuild-binding label { display: grid; gap: 5px; color: var(--muted-strong); font-size: 12px; }
.rebuild-plan { margin-top: 12px; border-top: 1px solid var(--line); padding-top: 12px; font-size: 12px; line-height: 1.6; overflow-wrap: anywhere; }
.rebuild-plan h4 { margin: 12px 0 5px; color: var(--text); font-size: 12px; }
.rebuild-facts { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; margin: 0; }
.rebuild-facts div { min-width: 0; border: 1px solid var(--line); border-radius:var(--radius-card); padding: 8px; }
.rebuild-facts dt { color: var(--muted-strong); font-size: 12px; }
.rebuild-facts dd { margin: 4px 0 0; overflow-wrap: anywhere; }
.rebuild-columns { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.rebuild-plan ul { margin: 4px 0; padding-left: 18px; color: var(--muted-strong); }
.rebuild-risks { color: var(--warning) !important; }
.rebuild-ack { display: flex; gap: 8px; align-items: flex-start; margin: 14px 0; color: var(--warning); }
.rebuild-ack input { flex: 0 0 auto; margin-top: 3px; }
.rebuild-error { color: var(--danger); }
@media (max-width: 600px) { .rebuild-binding { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); } .rebuild-facts { grid-template-columns: 1fr; } .rebuild-columns { grid-template-columns: 1fr; } }
</style>
