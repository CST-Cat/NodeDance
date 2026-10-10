<script setup lang="ts">
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
const validationError = ref('')

function addPort() { ports.value = [...ports.value, { containerPort: 0, protocol: 'tcp', hostIp: '', hostPort: '' }] }
function removePort(index: number) { ports.value = ports.value.filter((_, current) => current !== index) }
function addMount() { mounts.value = [...mounts.value, { type: 'bind', source: '', target: '', readOnly: false }] }
function removeMount(index: number) { mounts.value = mounts.value.filter((_, current) => current !== index) }

function submit() {
  validationError.value = ''
  const command = commandText.value.split(/\r?\n/).filter((argument) => argument.length > 0)
  const environment = environmentText.value.split(/\r?\n/).filter((entry) => entry.trim().length > 0)
  const configuredPorts: ContainerCreatePort[] = []
  for (const port of ports.value) {
    if (port.containerPort === 0 && !port.hostIp && !port.hostPort) continue
    if (!Number.isInteger(port.containerPort) || port.containerPort < 1 || port.containerPort > 65535 || port.hostPort && !/^([1-9][0-9]{0,4})$/.test(port.hostPort) || port.hostPort && Number(port.hostPort) > 65535) {
      validationError.value = '请检查端口映射中的容器端口和宿主机端口。'
      return
    }
    configuredPorts.push({ ...port, hostIp: port.hostIp || undefined, hostPort: port.hostPort || undefined })
  }
  const configuredMounts: ContainerCreateMount[] = []
  for (const mount of mounts.value) {
    if (!mount.source && !mount.target) continue
    if (!mount.target.startsWith('/') || mount.type === 'bind' && !(mount.source || '').startsWith('/')) {
      validationError.value = '挂载目标必须是容器内绝对路径；bind 来源必须是宿主机绝对路径。'
      return
    }
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
  if (!spec.image || !spec.name) {
    validationError.value = '镜像和容器名称不能为空。'
    return
  }
  emit('submit', spec)
}
</script>

<template>
  <details class="container-create-form" data-testid="container-create-form">
    <summary>创建 Docker 容器</summary>
    <form @submit.prevent="submit">
      <div class="create-form-grid">
        <label>镜像 <input v-model="image" required maxlength="512" placeholder="例如 nginx:alpine"></label>
        <label>容器名称 <input v-model="name" required maxlength="128" placeholder="例如 web-frontend"></label>
        <label>网络 <input v-model="network" maxlength="128" placeholder="默认网络、host 或现有网络名称"></label>
        <label>重启策略 <select v-model="restartPolicy"><option value="no">不自动重启</option><option value="always">始终重启</option><option value="unless-stopped">除手动停止外重启</option><option value="on-failure">失败时重启</option></select></label>
        <label v-if="restartPolicy === 'on-failure'">失败重试次数 <input v-model.number="restartRetries" type="number" min="0" max="1000"></label>
        <label class="create-form-wide">命令参数 <textarea v-model="commandText" rows="3" placeholder="每行一个参数；第一行是命令"></textarea><small>参数会作为 argv 传给 Docker，不经过 shell。</small></label>
        <label class="create-form-wide">环境变量 <textarea v-model="environmentText" rows="3" placeholder="每行一个 KEY=VALUE"></textarea></label>
      </div>
      <fieldset class="create-form-list"><legend>端口映射</legend>
        <div v-for="(port, index) in ports" :key="index" class="create-form-row">
          <label>容器端口 <input v-model.number="port.containerPort" type="number" min="1" max="65535"></label>
          <label>协议 <select v-model="port.protocol"><option value="tcp">TCP</option><option value="udp">UDP</option></select></label>
          <label>宿主机 IP <input v-model="port.hostIp" placeholder="留空绑定所有地址"></label>
          <label>宿主机端口 <input v-model="port.hostPort" inputmode="numeric" placeholder="留空由 Docker 分配"></label>
          <button type="button" class="container-action" @click="removePort(index)">移除</button>
        </div>
        <button type="button" class="container-action" @click="addPort">添加端口</button>
      </fieldset>
      <fieldset class="create-form-list"><legend>挂载</legend>
        <div v-for="(mount, index) in mounts" :key="index" class="create-form-row create-mount-row">
          <label>类型 <select v-model="mount.type"><option value="bind">宿主机目录</option><option value="volume">Docker 卷</option></select></label>
          <label>{{ mount.type === 'bind' ? '宿主机路径' : '卷名称（留空创建匿名卷）' }} <input v-model="mount.source" :placeholder="mount.type === 'bind' ? '/srv/data' : 'app-data'"></label>
          <label>容器路径 <input v-model="mount.target" placeholder="/var/lib/app"></label>
          <label class="create-readonly"><input v-model="mount.readOnly" type="checkbox"> 只读</label>
          <button type="button" class="container-action" @click="removeMount(index)">移除</button>
        </div>
        <button type="button" class="container-action" @click="addMount">添加挂载</button>
      </fieldset>
      <p class="create-form-note">创建任务会先在 Docker 中创建停止状态的容器，再通过真实 Container ID 和 Docker Inspect 核实。创建后可在下方启动。</p>
      <p v-if="validationError" class="create-form-error" role="alert">{{ validationError }}</p>
      <p v-if="props.error" class="create-form-error" role="alert">{{ props.error }}</p>
      <button class="container-action create-submit" type="submit" :disabled="props.disabled || props.submitting">{{ props.submitting ? '提交中…' : '提交创建任务' }}</button>
    </form>
  </details>
</template>

<style scoped>
.container-create-form { margin:12px 0; border:1px solid rgba(141,201,255,.18); border-radius:9px; background:rgba(13,23,37,.72); }
.container-create-form summary { cursor:pointer; padding:11px 13px; color:#d4e9ff; font-size:11px; font-weight:650; }
.container-create-form form { display:grid; gap:11px; padding:0 12px 12px; }
.create-form-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:9px; }
.container-create-form label { display:grid; min-width:0; gap:5px; color:#a8b7cb; font-size:9px; }
.container-create-form input,.container-create-form select,.container-create-form textarea { min-width:0; min-height:34px; border:1px solid rgba(171,196,232,.16); border-radius:6px; padding:7px 8px; color:#eaf0fa; background:#111b29; font:inherit; font-size:10px; }
.container-create-form textarea { resize:vertical; line-height:1.5; }.container-create-form small,.create-form-note { color:#8193aa; font-size:9px; line-height:1.5; }.create-form-wide { grid-column:1/-1; }
.create-form-list { min-width:0; margin:0; border:1px solid rgba(171,196,232,.12); border-radius:7px; padding:9px; }.create-form-list legend { padding:0 5px; color:#b9cbe2; font-size:9px; }.create-form-row { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)) auto; align-items:end; gap:7px; margin:4px 0 9px; }.create-form-row .container-action { min-height:34px; }.create-readonly { display:flex!important; align-items:center; min-height:34px; }.create-readonly input { min-height:0; }
.create-form-note,.create-form-error { margin:0; }.create-form-error { color:#ffc1b8; font-size:10px; }.create-submit { justify-self:start; }
@media(max-width:680px) { .create-form-grid { grid-template-columns:minmax(0,1fr); }.create-form-wide { grid-column:auto; }.create-form-row { grid-template-columns:repeat(2,minmax(0,1fr)); }.create-form-row .container-action { justify-self:start; } }
</style>
