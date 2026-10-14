<script setup lang="ts">
import { t, display, locale } from "../i18n"
import { ref } from 'vue'
import type { ContainerCreateMount, ContainerCreatePort, ContainerCreateSpec } from '../api'

const props = defineProps<{ disabled?: boolean; submitting?: boolean; error?: string }>()
const emit = defineEmits<{ submit: [spec: ContainerCreateSpec] }>()

const image = ref('')
const name = ref('')
const commandText = ref('')
const environmentText = ref('')
const network = ref('')
const restartPolicy = ref<ContainerCreateSpec['restartPolicy']>('no')
const restartRetries = ref(0)
const ports = ref<ContainerCreatePort[]>([])
const mounts = ref<ContainerCreateMount[]>([])

function addPort() { ports.value = [...ports.value, { containerPort: 0, protocol: 'tcp', hostIp: '', hostPort: '' }] }
function removePort(index: number) { ports.value = ports.value.filter((_, current) => current !== index) }
function addMount() { mounts.value = [...mounts.value, { type: 'bind', source: '', target: '', readOnly: false }] }
function removeMount(index: number) { mounts.value = mounts.value.filter((_, current) => current !== index) }

function submit() {
  const command = commandText.value.split(/\r?\n/).filter((argument) => argument.length > 0)
  const environment = environmentText.value.split(/\r?\n/).filter((entry) => entry.trim().length > 0)
  const configuredPorts: ContainerCreatePort[] = []
  for (const port of ports.value) {
    if (port.containerPort === 0 && !port.hostIp && !port.hostPort) continue
    configuredPorts.push({ ...port, hostIp: port.hostIp || undefined, hostPort: port.hostPort || undefined })
  }
  const configuredMounts: ContainerCreateMount[] = []
  for (const mount of mounts.value) {
    if (!mount.source && !mount.target) continue
    configuredMounts.push({ ...mount, source: mount.source || undefined })
  }
  const spec: ContainerCreateSpec = {
    image: image.value.trim(), name: name.value.trim(),
    command: command.length ? command : undefined,
    environment: environment.length ? environment : undefined,
    ports: configuredPorts.length ? configuredPorts : undefined,
    mounts: configuredMounts.length ? configuredMounts : undefined,
    network: network.value.trim() || undefined,
    restartPolicy: restartPolicy.value,
    restartRetries: restartPolicy.value === 'on-failure' ? restartRetries.value : undefined,
  }
  emit('submit', spec)
}
</script>

<template>
  <details class="container-create-form" data-testid="container-create-form">
    <summary>{{ t('m.d1f7023b4f8d') }}</summary>
    <form @submit.prevent="submit">
      <div class="create-form-grid">
        <label>{{ t('m.176c09844e0f') }}<input v-model="image" required maxlength="512" :placeholder="t('m.4204fc5cf592')"></label>
        <label>{{ t('m.62621c43654a') }}<input v-model="name" required maxlength="128" :placeholder="t('m.6ae23d2114e1')"></label>
        <label>{{ t('m.97b31b5d63f5') }}<input v-model="network" maxlength="128" :placeholder="t('m.7f630368cc55')"></label>
        <label>{{ t('m.c6e4aade79e8') }}<UiSelect v-model="restartPolicy"><option value="no">{{ t('m.4858fc1fce95') }}</option><option value="always">{{ t('m.938f259b93a3') }}</option><option value="unless-stopped">{{ t('m.4ac3e0ecef3c') }}</option><option value="on-failure">{{ t('m.787e9ce9e178') }}</option></UiSelect></label>
        <label v-if="restartPolicy === 'on-failure'">{{ t('m.696303d9c413') }}<input v-model.number="restartRetries" type="number" min="0" max="1000"></label>
        <label class="create-form-wide">{{ t('m.570674fea082') }}<textarea v-model="commandText" rows="3" :placeholder="t('m.42dca5a99d0f')"></textarea><small>{{ t('m.47c94fbef3fe') }}</small></label>
        <label class="create-form-wide">{{ t('m.ae27b474ea4d') }}<textarea v-model="environmentText" rows="3" :placeholder="t('m.555aa8a036b4')"></textarea></label>
      </div>
      <fieldset class="create-form-list"><legend>{{ t('m.c263674112e4') }}</legend>
        <div v-for="(port, index) in ports" :key="index" class="create-form-row">
          <label>{{ t('m.9c9980ac48fe') }}<input v-model.number="port.containerPort" type="number" min="1" max="65535"></label>
          <label>{{ t('m.ab2f31f30acf') }}<UiSelect v-model="port.protocol"><option value="tcp">TCP</option><option value="udp">UDP</option></UiSelect></label>
          <label>{{ t('m.2ffee39fb19a') }}<input v-model="port.hostIp" :placeholder="t('m.19caa396b494')"></label>
          <label>{{ t('m.f4a4267edc03') }}<input v-model="port.hostPort" inputmode="numeric" :placeholder="t('m.fdae3b9db051')"></label>
          <button type="button" class="container-action" @click="removePort(index)">{{ t('m.6135d4159e89') }}</button>
        </div>
        <button type="button" class="container-action" @click="addPort">{{ t('m.173c6155fb1a') }}</button>
      </fieldset>
      <fieldset class="create-form-list"><legend>{{ t('m.6fc9c71cb14f') }}</legend>
        <div v-for="(mount, index) in mounts" :key="index" class="create-form-row create-mount-row">
          <label>{{ t('m.ba40014ff496') }}<UiSelect v-model="mount.type"><option value="bind">{{ t('m.8b61a84a6fc9') }}</option><option value="volume">{{ t('m.22256deee0ac') }}</option></UiSelect></label>
          <label>{{ mount.type === 'bind' ? t('m.bb2ab0977e56') : t('m.a8833f0d0a67') }} <input v-model="mount.source" :placeholder="mount.type === 'bind' ? '/srv/data' : 'app-data'"></label>
          <label>{{ t('m.52e49d585de1') }}<input v-model="mount.target" placeholder="/var/lib/app"></label>
          <label class="create-readonly"><input v-model="mount.readOnly" type="checkbox">{{ t('m.3b5ec3533b0e') }}</label>
          <button type="button" class="container-action" @click="removeMount(index)">{{ t('m.6135d4159e89') }}</button>
        </div>
        <button type="button" class="container-action" @click="addMount">{{ t('m.78c8f87622ca') }}</button>
      </fieldset>
      <p class="create-form-note">{{ t('m.4576641fd0a6') }}</p>
      <p v-if="props.error" class="create-form-error" role="alert">{{ display(props.error) }}</p>
      <button class="container-action create-submit" type="submit" :disabled="props.disabled || props.submitting">{{ props.submitting ? t('m.18931020ad52') : t('m.cb34cd41fbba') }}</button>
    </form>
  </details>
</template>

<style scoped>
.container-create-form { margin:12px 0; border:1px solid var(--line); border-radius:var(--radius-card); background:var(--surface-soft); }
.container-create-form summary { cursor:pointer; padding:11px 13px; color:var(--text); font-size: 12px; font-weight:500; }
.container-create-form form { display:grid; gap:11px; padding:0 12px 12px; }
.create-form-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:9px; }
.container-create-form label { display:grid; min-width:0; gap:5px; color:var(--muted-strong); font-size: 12px; }
.container-create-form input,.container-create-form select,.container-create-form textarea { min-width:0; min-height:40px; border:1px solid var(--line); border-radius:var(--radius-control); padding:7px 8px; color:var(--text); background:var(--surface-soft); font:inherit; font-size: 12px; }
.container-create-form textarea { resize:vertical; line-height:1.5; }.container-create-form small,.create-form-note { color:var(--muted); font-size: 12px; line-height:1.5; }.create-form-wide { grid-column:1/-1; }
.create-form-list { min-width:0; margin:0; border:1px solid var(--line); border-radius:var(--radius-card); padding:9px; }.create-form-list legend { padding:0 5px; color:var(--muted-strong); font-size: 12px; }.create-form-row { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)) auto; align-items:end; gap:7px; margin:4px 0 9px; }.create-form-row .container-action { min-height:40px; }.create-readonly { display:flex!important; align-items:center; min-height:40px; }.create-readonly input { min-height:0; }
.create-form-note,.create-form-error { margin:0; }.create-form-error { color:var(--danger); font-size: 12px; }.create-submit { justify-self:start; }
@media(max-width:680px) { .create-form-grid { grid-template-columns:minmax(0,1fr); }.create-form-wide { grid-column:auto; }.create-form-row { grid-template-columns:repeat(2,minmax(0,1fr)); }.create-form-row .container-action { justify-self:start; } }
</style>
