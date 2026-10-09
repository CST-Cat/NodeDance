<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api, type AgentNode, type AgentNodesResponse, type ContainerTask, type DockerInventory, type DockerInventoryMessage, type NodeStatusResponse } from '../api'
import type { MetricsView } from '../metrics-contract'
import MetricsPanel from './MetricsPanel.vue'

interface NodeClock {
  status: string
  generation: number
  serverTime: string
  leaseValidUntil?: string
  startedAt: number
}

const nodes = ref<AgentNode[]>([])
const selectedNodeID = ref('')
const views = ref<Record<string, MetricsView>>({})
const dockerViews = ref<Record<string, DockerInventory>>({})
const clocks = ref<Record<string, NodeClock>>({})
const nodeReasons = ref<Record<string, string>>({})
const taskViews = ref<Record<string, ContainerTask>>({})
const taskSubmitting = ref<Record<string, boolean>>({})
const taskErrors = ref<Record<string, string>>({})
const busy = ref(true)
const error = ref('')
const elapsed = ref(0)
const socketState = ref<'connecting' | 'connected' | 'retrying'>('connecting')
let socket: WebSocket | undefined
let reconnectTimer: ReturnType<typeof setTimeout> | undefined
let refreshTimer: ReturnType<typeof setInterval> | undefined
let tickTimer: ReturnType<typeof setInterval> | undefined
let taskRefreshTimer: ReturnType<typeof setInterval> | undefined
let reconnectDelay = 1000
let disposed = false
let latestNodeListServerTime = Number.NEGATIVE_INFINITY

const selectedNode = computed(() => nodes.value.find((node) => node.nodeId === selectedNodeID.value) ?? null)
const selectedView = computed(() => views.value[selectedNodeID.value] ?? null)
const selectedDocker = computed(() => dockerViews.value[selectedNodeID.value] ?? null)

function dockerAvailabilityText(inventory: DockerInventory): string {
  if (inventory.dockerAvailability === 'available') {
    if (inventory.health?.errorKind === 'api_incompatible') return 'Docker API 不兼容'
    if (dockerIsStale(inventory)) return '数据过期'
    if (inventory.health?.reason) return 'Engine 状态异常'
    return 'Engine 正常'
  }
  if (inventory.dockerAvailability === 'unavailable') return 'Docker 不可用'
  return '等待 Docker 状态'
}

function dockerStatusReason(inventory: DockerInventory): string {
  return inventory.health?.reason || inventory.staleReason || ''
}

function dockerIsStale(inventory: DockerInventory): boolean {
  if (inventory.dataStale || !inventory.agentOnline) return true
  const clock = clocks.value[inventory.nodeId]
  const node = nodes.value.find((item) => item.nodeId === inventory.nodeId)
  return !clock || clock.status !== 'online' ||
    clock.generation !== inventory.activeGeneration ||
    Boolean(node?.agentId && inventory.agentId !== node.agentId) ||
    elapsed.value - clock.startedAt >= remainingLease(clock)
}

function containerHealthText(container: DockerInventory['containers'][number]['container']): string {
  if (!container.healthcheckConfigured || container.health === 'none') return '未配置健康检查'
  const labels: Record<string, string> = {
    healthy: '健康', unhealthy: '异常', starting: '启动中', unknown: '未知',
  }
  return labels[container.health] ?? container.health
}

function portText(port: DockerInventory['containers'][number]['container']['ports'][number]): string {
  const published = Array.isArray(port.published) ? port.published : []
  const configured = Array.isArray(port.configured) ? port.configured : []
  if (published.length > 0) {
    return published.map((binding) => {
      const host = binding.ip || '*'
      const formattedHost = host.includes(':') && !host.startsWith('[') ? `[${host}]` : host
      return `${formattedHost}:${binding.port} → ${port.containerPort}/${port.protocol}`
    }).join('，')
  }
  if (configured.length > 0) return `${port.containerPort}/${port.protocol}（配置映射，当前未发布）`
  if (port.exposed) return `${port.containerPort}/${port.protocol}（仅声明）`
  return `${port.containerPort}/${port.protocol}`
}

function remainingLease(clock: NodeClock): number {
  if (!clock.leaseValidUntil) return 0
  const from = Date.parse(clock.serverTime)
  const until = Date.parse(clock.leaseValidUntil)
  if (!Number.isFinite(from) || !Number.isFinite(until)) return 0
  return Math.max(0, until - from)
}

function nodeIsOnline(node: AgentNode): boolean {
  const clock = clocks.value[node.nodeId]
  if (!clock) return node.status === 'online'
  return clock.status === 'online' && elapsed.value - clock.startedAt < remainingLease(clock)
}

function setNodeReason(nodeID: string, reason?: string) {
  if (reason) {
    nodeReasons.value = { ...nodeReasons.value, [nodeID]: reason }
    return
  }
  if (!(nodeID in nodeReasons.value)) return
  const next = { ...nodeReasons.value }
  delete next[nodeID]
  nodeReasons.value = next
}

function setClock(nodeID: string, status: string, serverTime: string, leaseValidUntil?: string, generation = 0) {
  const incomingTime = Date.parse(serverTime)
  if (!Number.isFinite(incomingTime)) return false
  const previous = clocks.value[nodeID]
  if (previous) {
    if (generation < previous.generation) return false
    const previousTime = Date.parse(previous.serverTime)
    // Reapplying an identical Core snapshot must not restart its client-side
    // lease countdown, especially when an HTTP response arrives late.
    if (generation === previous.generation && Number.isFinite(previousTime) && incomingTime <= previousTime) return false
  }
	clocks.value = {
		...clocks.value,
		[nodeID]: { status, serverTime, leaseValidUntil, generation, startedAt: performance.now() },
	}
	return true
}

function mergeNodeList(payload: AgentNodesResponse): AgentNode[] | undefined {
  const listServerTime = Date.parse(payload.serverTime)
  if (!Number.isFinite(listServerTime) || listServerTime < latestNodeListServerTime) return undefined
  latestNodeListServerTime = listServerTime

  const previousNodes = new Map(nodes.value.map((node) => [node.nodeId, node]))
  const merged = payload.nodes.map((node) => {
    const clock = clocks.value[node.nodeId]
    const clockTime = clock ? Date.parse(clock.serverTime) : Number.NEGATIVE_INFINITY
    const stale = Boolean(clock && (
      node.generation < clock.generation ||
      node.generation === clock.generation && Number.isFinite(clockTime) && listServerTime <= clockTime
    ))
    if (!stale) {
      setClock(node.nodeId, node.status, payload.serverTime, node.leaseValidUntil, node.generation)
      return node
    }

    // Preserve current Core state while retaining node metadata returned by
    // the list endpoint. The per-node clock is newer than this list snapshot.
    return {
      ...node,
      status: clock!.status,
      generation: clock!.generation,
      leaseValidUntil: clock!.leaseValidUntil ?? node.leaseValidUntil,
      lastSeen: previousNodes.get(node.nodeId)?.lastSeen ?? node.lastSeen,
    }
  })
  const included = new Set(merged.map((node) => node.nodeId))
  for (const node of nodes.value) {
    if (included.has(node.nodeId)) continue
    const clock = clocks.value[node.nodeId]
    if (clock && Date.parse(clock.serverTime) > listServerTime) merged.push(node)
  }
  return merged
}

async function refreshNodes() {
  const result = await api.nodes()
  const merged = mergeNodeList(result)
  if (!merged) {
    busy.value = false
    return
  }
  nodes.value = merged
  if (!selectedNodeID.value || !merged.some((node) => node.nodeId === selectedNodeID.value)) {
    selectedNodeID.value = merged[0]?.nodeId ?? ''
  }
  busy.value = false
  if (!selectedNodeID.value) return
  const node = merged.find((item) => item.nodeId === selectedNodeID.value)
  if (node?.agentId) await Promise.all([loadMetrics(node), loadContainers(node), loadTasks(node)])
}

async function loadMetrics(node: AgentNode) {
	try {
		const result = await api.nodeMetrics(node.nodeId)
    acceptMetricsResponse(result)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法加载主机指标。'
  }
}

async function loadContainers(node: AgentNode) {
  try {
    const result = await api.nodeContainers(node.nodeId)
    acceptDockerResponse(result)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法加载 Docker 容器。'
  }
}

async function loadTasks(node: AgentNode) {
  try {
    const result = await api.nodeTasks(node.nodeId)
    const next = { ...taskViews.value }
    for (const [taskId, task] of Object.entries(next)) {
      if (task.nodeId === node.nodeId) delete next[taskId]
    }
    for (const task of result.tasks) next[task.taskId] = task
    taskViews.value = next
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法读取容器操作进度。'
  }
}

function taskForContainer(containerID: string): ContainerTask | undefined {
  return Object.values(taskViews.value)
    .filter((task) => task.nodeId === selectedNodeID.value && task.targetId === containerID)
    .sort((left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt))[0]
}

function taskStatusLabel(task: ContainerTask): string {
  if (task.status === 'queued') return '任务已保存，等待 Agent'
  if (task.status === 'running') return `正在执行：${task.progress.phase}`
  if (task.status === 'succeeded') return `已验证完成：${task.result.observedState || 'Docker 状态已确认'}`
  if (task.status === 'unknown') return '结果待确认，系统不会自动重试'
  if (task.status === 'timed_out') return '超时，等待实际状态确认'
  if (task.status === 'canceled') return '任务已取消'
  return `执行失败：${task.result.code || '原因未分类'}`
}

async function restartContainer(container: DockerInventory['containers'][number]['container']) {
  const node = selectedNode.value
  const inventory = selectedDocker.value
  if (!node || !inventory || !node.agentId || dockerIsStale(inventory) || container.stale || taskSubmitting.value[container.id]) return
  taskSubmitting.value = { ...taskSubmitting.value, [container.id]: true }
  const errors = { ...taskErrors.value }
  delete errors[container.id]
  taskErrors.value = errors
  try {
    const accepted = await api.createContainerTask(node.nodeId, container.id, { action: 'restart' }, crypto.randomUUID())
    const current = await api.nodeTask(node.nodeId, accepted.taskId)
    taskViews.value = { ...taskViews.value, [current.taskId]: current }
  } catch (reason) {
    taskErrors.value = { ...taskErrors.value, [container.id]: reason instanceof Error ? reason.message : '无法创建容器重启任务。' }
  } finally {
    taskSubmitting.value = { ...taskSubmitting.value, [container.id]: false }
  }
}

function acceptDockerResponse(result: DockerInventoryMessage) {
  const containers = Array.isArray(result.inventory?.containers) ? result.inventory.containers : []
  result = {
    ...result,
    inventory: {
      ...result.inventory,
      containers: containers.map((record) => ({
        ...record,
        container: {
          ...record.container,
          ports: Array.isArray(record.container?.ports) ? record.container.ports.map((port) => ({
            ...port,
            configured: Array.isArray(port.configured) ? port.configured : [],
            published: Array.isArray(port.published) ? port.published : [],
          })) : [],
        },
      })),
    },
  }
  const previous = dockerViews.value[result.nodeId]
  if (previous) {
    if (result.inventory.activeGeneration < previous.activeGeneration) return
    if (result.inventory.activeGeneration === previous.activeGeneration) {
      const incomingTime = Date.parse(result.inventory.serverTime)
      const previousTime = Date.parse(previous.serverTime)
      if (Number.isFinite(incomingTime) && Number.isFinite(previousTime) && incomingTime < previousTime) return
      if (incomingTime === previousTime && incomingTime > 0) {
        const incomingSequence = Math.max(0, ...result.inventory.containers.map((item) => item.sequence))
        const previousSequence = Math.max(0, ...previous.containers.map((item) => item.sequence))
        if (incomingSequence < previousSequence) return
      }
    }
  }
  dockerViews.value = { ...dockerViews.value, [result.nodeId]: result.inventory }
  if (setClock(result.nodeId, result.state.status, result.state.serverTime, result.state.leaseValidUntil, result.state.generation)) {
    setNodeReason(result.nodeId, result.state.reason)
  }
}

function acceptMetricsResponse(result: MetricsView | NodeStatusResponse) {
  if (isNodeStatusResponse(result)) {
    if (setClock(result.nodeId, result.state.status, result.state.serverTime, result.state.leaseValidUntil, result.state.generation)) {
      setNodeReason(result.nodeId, result.state.reason)
    }
    return
  }
	const previous = views.value[result.nodeId]
	if (previous) {
		if (result.activeGeneration < previous.activeGeneration) return
		if (result.activeGeneration === previous.activeGeneration) {
			if (result.generation < previous.generation) return
			if (result.generation === previous.generation && result.sequence < previous.sequence) return
			const incomingTime = Date.parse(result.serverTime)
			const previousTime = Date.parse(previous.serverTime)
			if (result.generation === previous.generation && result.sequence === previous.sequence &&
				Number.isFinite(incomingTime) && Number.isFinite(previousTime) && incomingTime <= previousTime) return
		}
	}
	const previousClock = clocks.value[result.nodeId]
	if (previousClock && result.activeGeneration < previousClock.generation) return
	if (previousClock && result.activeGeneration === previousClock.generation) {
		const incomingTime = Date.parse(result.serverTime)
		const previousTime = Date.parse(previousClock.serverTime)
		if (Number.isFinite(incomingTime) && Number.isFinite(previousTime) && incomingTime < previousTime) return
	}
  const normalized: MetricsView = {
    ...result,
    metrics: {
      ...result.metrics,
      network: {
        ...result.metrics.network,
        interfaces: Array.isArray(result.metrics.network?.interfaces) ? result.metrics.network.interfaces : [],
      },
      disk: {
        ...result.metrics.disk,
        mounts: Array.isArray(result.metrics.disk?.mounts) ? result.metrics.disk.mounts : [],
      },
    },
  }
  views.value = { ...views.value, [normalized.nodeId]: normalized }
  setNodeReason(normalized.nodeId)
  setClock(normalized.nodeId, normalized.nodeStatus, normalized.serverTime, normalized.leaseValidUntil, normalized.activeGeneration)
}

function isNodeStatusResponse(result: MetricsView | NodeStatusResponse): result is NodeStatusResponse {
  return (result as NodeStatusResponse).type === 'node_status'
}

function acceptSocketMessage(raw: string) {
  let event: unknown
  try {
    event = JSON.parse(raw)
  } catch {
    return
  }
  if (typeof event !== 'object' || event === null || !('type' in event) || !('nodeId' in event)) return
  const value = event as { type?: string; nodeId?: string; metrics?: MetricsView; state?: NodeStatusResponse['state'] }
  if (!value.nodeId) return
  if (value.type === 'node_metrics' && value.metrics) {
    acceptMetricsResponse(value.metrics)
    return
  }
  if (value.type === 'node_status' && value.state) {
    if (setClock(value.nodeId, value.state.status, value.state.serverTime, value.state.leaseValidUntil, value.state.generation)) {
      setNodeReason(value.nodeId, value.state.reason)
    }
    return
  }
  if (value.type === 'node_containers') {
    acceptDockerResponse(event as DockerInventoryMessage)
  }
}

function connectDashboard() {
  if (disposed) return
  socketState.value = socketState.value === 'connected' ? 'retrying' : 'connecting'
  const url = new URL('/ws/v1/dashboard', window.location.href)
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  const current = new WebSocket(url)
  socket = current
  current.onopen = () => {
    if (disposed || socket !== current) return
    reconnectDelay = 1000
    socketState.value = 'connected'
    void refreshNodes().catch((reason: unknown) => {
      busy.value = false
      error.value = reason instanceof Error ? reason.message : '无法读取节点列表。'
    })
  }
  current.onmessage = (message) => {
    if (disposed || socket !== current) return
    acceptSocketMessage(String(message.data))
  }
  current.onclose = () => {
    if (socket === current) {
      socket = undefined
      scheduleReconnect()
    }
  }
  current.onerror = () => {
    if (socket === current) current.close()
  }
}

function scheduleReconnect() {
  if (disposed || reconnectTimer !== undefined) return
  socketState.value = 'retrying'
  reconnectTimer = setTimeout(() => {
    reconnectTimer = undefined
    connectDashboard()
  }, reconnectDelay)
  reconnectDelay = Math.min(30_000, reconnectDelay * 2)
}

function chooseNode(node: AgentNode) {
  selectedNodeID.value = node.nodeId
  error.value = ''
  if (node.agentId) {
    void Promise.all([loadMetrics(node), loadContainers(node), loadTasks(node)])
  } else {
    setNodeReason(node.nodeId, 'awaiting_agent_registration')
  }
}

function isPendingRegistration(node: AgentNode): boolean {
  return node.status === 'pending' || !node.agentId
}

onMounted(() => {
  void refreshNodes().catch((reason: unknown) => {
    busy.value = false
    error.value = reason instanceof Error ? reason.message : '无法读取节点列表。'
  })
  connectDashboard()
  refreshTimer = setInterval(() => {
    void refreshNodes().catch((reason: unknown) => {
      error.value = reason instanceof Error ? reason.message : '无法刷新节点列表。'
    })
  }, 10_000)
  tickTimer = setInterval(() => { elapsed.value = performance.now() }, 250)
  taskRefreshTimer = setInterval(() => {
    const node = selectedNode.value
    const hasActiveTask = Object.values(taskViews.value).some((task) => task.nodeId === selectedNodeID.value && (task.status === 'queued' || task.status === 'running'))
    if (node && hasActiveTask) void loadTasks(node)
  }, 1200)
})

onBeforeUnmount(() => {
  disposed = true
  if (reconnectTimer !== undefined) clearTimeout(reconnectTimer)
  if (refreshTimer !== undefined) clearInterval(refreshTimer)
  if (tickTimer !== undefined) clearInterval(tickTimer)
  if (taskRefreshTimer !== undefined) clearInterval(taskRefreshTimer)
  socket?.close()
})
</script>

<template>
  <section class="nodes-dashboard" aria-labelledby="nodes-title">
    <header class="nodes-heading">
      <div>
        <span class="eyebrow">HOST MONITORING</span>
        <h1 id="nodes-title">节点指标<span class="title-period">.</span></h1>
        <p>主机数据由 Agent 后台采集，无需保持此页面打开。</p>
      </div>
      <span class="stream-state" :data-state="socketState" role="status">
        <i aria-hidden="true"></i>{{ socketState === 'connected' ? '实时连接' : socketState === 'retrying' ? '正在重连' : '正在连接' }}
      </span>
    </header>

    <div v-if="error" class="nodes-error" role="alert">{{ error }}</div>
    <div class="nodes-layout">
      <aside class="node-list" aria-label="节点列表">
        <div class="node-list-heading"><strong>节点</strong><span>{{ nodes.length }}</span></div>
        <p v-if="busy && nodes.length === 0" class="node-empty">正在加载节点…</p>
        <p v-else-if="nodes.length === 0" class="node-empty">暂无已登记的节点。</p>
        <button v-for="node in nodes" :key="node.nodeId" class="node-option" :class="{ selected: node.nodeId === selectedNodeID }" type="button" @click="chooseNode(node)">
          <span class="node-option-dot" :data-status="nodeIsOnline(node) ? 'online' : 'offline'"></span>
          <span class="node-option-copy"><strong>{{ node.displayName }}</strong><small>{{ nodeIsOnline(node) ? '在线' : isPendingRegistration(node) ? '等待 Agent 注册' : node.status === 'revoked' ? '已撤销' : '离线' }} · {{ node.agentVersion || 'Agent 未连接' }}</small></span>
          <span class="node-option-arrow" aria-hidden="true">›</span>
        </button>
      </aside>

      <main class="node-detail" aria-live="polite">
        <MetricsPanel v-if="selectedView" :key="selectedView.nodeId" :view="selectedView" />
        <div v-else-if="selectedNode" class="metrics-waiting" data-testid="metrics-waiting">
          <span class="eyebrow">{{ selectedNode.displayName }}</span>
          <h2>{{ isPendingRegistration(selectedNode) ? '等待 Agent 注册' : nodeIsOnline(selectedNode) ? '等待首份主机样本' : '节点当前离线' }}</h2>
          <p v-if="isPendingRegistration(selectedNode)">节点已创建，Agent 完成注册后才会开始采集主机数据。</p>
          <p v-else-if="nodeIsOnline(selectedNode)">CPU、内存、网络与磁盘数据会在 Agent 首次采样后显示。</p>
          <p v-else>保留的最近数据会在重新连接时显示，并明确标记为过期。{{ nodeReasons[selectedNode.nodeId] ? `状态原因：${nodeReasons[selectedNode.nodeId]}` : '' }}</p>
          <span class="node-detail-status" :data-online="nodeIsOnline(selectedNode)">{{ isPendingRegistration(selectedNode) ? '等待注册' : nodeIsOnline(selectedNode) ? '在线' : '离线' }}</span>
        </div>
        <div v-else class="metrics-waiting"><h2>选择一个节点</h2><p>节点的实时与最近指标会显示在这里。</p></div>

        <section v-if="selectedNode" class="docker-panel" aria-labelledby="docker-title" data-testid="docker-inventory">
          <header class="docker-heading">
            <div><span class="eyebrow">CONTAINER INVENTORY</span><h2 id="docker-title">Docker 容器</h2></div>
            <span v-if="selectedDocker" class="docker-state" :data-available="selectedDocker.dockerAvailability" :data-stale="dockerIsStale(selectedDocker)">
              {{ dockerAvailabilityText(selectedDocker) }}
            </span>
          </header>
          <p v-if="!selectedDocker" class="docker-empty">正在读取此节点的 Docker 状态。</p>
          <p v-else-if="selectedDocker.dockerAvailability === 'unavailable'" class="docker-empty">
            Agent 仍可在线采集主机指标；Docker Engine 当前不可用。{{ dockerStatusReason(selectedDocker) ? `原因：${dockerStatusReason(selectedDocker)}` : '' }}
          </p>
          <p v-else-if="selectedDocker && dockerStatusReason(selectedDocker)" class="docker-empty" data-testid="docker-health-reason">
            Docker Engine 当前不能提供完整容器状态，下面保留最近已知容器并标记为过期。原因：{{ dockerStatusReason(selectedDocker) }}
          </p>
          <p v-if="selectedDocker && selectedDocker.containers.length === 0" class="docker-empty">
            {{ dockerIsStale(selectedDocker) ? '当前没有可确认的容器列表，等待完整扫描。' : '此节点没有容器。' }}
          </p>
          <div v-if="selectedDocker && selectedDocker.containers.length > 0" class="docker-list">
            <article v-for="record in selectedDocker.containers" :key="record.container.id" class="docker-row" :data-container-id="record.container.id" :data-stale="dockerIsStale(selectedDocker) || record.container.stale">
              <div class="docker-row-title">
                <div class="docker-container-copy"><strong>{{ record.container.name || record.container.id.slice(0, 12) }}</strong><small>{{ record.container.image || '未知镜像' }}<template v-if="record.container.compose"> · {{ record.container.compose.project }}/{{ record.container.compose.service }}</template></small></div>
                <span class="container-state" :data-running="record.container.running">{{ record.container.state || '未知' }}</span>
              </div>
              <div class="docker-row-meta">
                <span :data-health="record.container.health">健康：{{ containerHealthText(record.container) }}</span>
                <span v-if="dockerIsStale(selectedDocker) || record.container.stale" class="container-stale">
                  过期数据<template v-if="record.container.unavailableReason">：{{ record.container.unavailableReason }}</template>
                </span>
                <span v-else>更新于 {{ new Date(record.receivedAt).toLocaleTimeString() }}</span>
              </div>
              <div v-if="(record.container.ports ?? []).length" class="container-ports" aria-label="容器端口">
                <span v-for="(port, index) in (record.container.ports ?? [])" :key="`${port.containerPort}-${port.protocol}-${index}`">{{ portText(port) }}</span>
              </div>
              <p v-if="record.container.unavailableReason" class="container-reason">{{ record.container.unavailableReason }}</p>
              <div class="container-task-actions">
                <button class="container-action" type="button" :disabled="dockerIsStale(selectedDocker) || record.container.stale || taskSubmitting[record.container.id]" @click="restartContainer(record.container)">
                  {{ taskSubmitting[record.container.id] ? '正在提交…' : '重启容器' }}
                </button>
                <span v-if="taskForContainer(record.container.id)" class="container-task-status" :data-status="taskForContainer(record.container.id)?.status" role="status">
                  {{ taskStatusLabel(taskForContainer(record.container.id)!) }}
                </span>
                <span v-if="taskErrors[record.container.id]" class="container-task-error" role="alert">{{ taskErrors[record.container.id] }}</span>
              </div>
            </article>
          </div>
        </section>
      </main>
    </div>
  </section>
</template>

<style scoped>
.nodes-dashboard { width: min(1480px, 100%); margin: 0 auto; padding: clamp(24px, 5vw, 52px); color: #eaf0fa; }
.nodes-heading { display: flex; align-items: center; justify-content: space-between; gap: 20px; margin-bottom: 28px; }
.nodes-heading h1 { margin: 5px 0 0; font-size: clamp(27px, 4vw, 38px); letter-spacing: -.05em; }
.nodes-heading p { margin: 8px 0 0; color: #98a8be; font-size: 13px; }
.stream-state { display: inline-flex; align-items: center; gap: 8px; border: 1px solid rgba(171, 196, 232, .13); border-radius: 999px; padding: 8px 12px; color: #aebbd0; font-size: 11px; white-space: nowrap; }
.stream-state i, .node-option-dot { width: 7px; height: 7px; border-radius: 50%; background: #9aa9bc; }
.stream-state[data-state='connected'] i, .node-option-dot[data-status='online'] { background: #79d69c; box-shadow: 0 0 0 3px rgba(80, 186, 119, .13); }
.nodes-error { margin-bottom: 16px; border: 1px solid rgba(255, 129, 116, .24); border-radius: 10px; padding: 12px 14px; color: #ffc1b8; background: rgba(184, 77, 72, .1); font-size: 12px; overflow-wrap: anywhere; }
.nodes-layout { display: grid; grid-template-columns: minmax(220px, 280px) minmax(0, 1fr); gap: 20px; align-items: start; }
.node-list { overflow: hidden; border: 1px solid rgba(171, 196, 232, .13); border-radius: 14px; background: rgba(15, 25, 40, .72); }
.node-list-heading { display: flex; align-items: center; justify-content: space-between; border-bottom: 1px solid rgba(171, 196, 232, .1); padding: 14px 16px; color: #dce6f5; font-size: 12px; }
.node-list-heading span { color: #8fa1b9; font-size: 10px; }
.node-option { display: flex; width: 100%; align-items: center; gap: 10px; border: 0; border-bottom: 1px solid rgba(171, 196, 232, .08); padding: 13px 14px; color: #e3ebf7; background: transparent; text-align: left; cursor: pointer; }
.node-option:last-child { border-bottom: 0; }
.node-option:hover, .node-option.selected { background: rgba(87, 133, 195, .13); }
.node-option-copy { display: grid; min-width: 0; flex: 1; gap: 4px; }
.node-option-copy strong { overflow: hidden; font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.node-option-copy small { color: #93a4bb; font-size: 9px; }
.node-option-arrow { color: #8294ac; font-size: 20px; }
.node-empty { padding: 14px; color: #94a5bc; font-size: 11px; }
.node-detail { min-width: 0; border: 1px solid rgba(171, 196, 232, .13); border-radius: 14px; padding: clamp(15px, 2.5vw, 24px); background: rgba(15, 25, 40, .62); }
.metrics-waiting { position: relative; min-height: 260px; display: flex; flex-direction: column; align-items: flex-start; justify-content: center; padding: clamp(20px, 5vw, 52px); }
.metrics-waiting h2 { margin: 10px 0 0; font-size: 21px; }
.metrics-waiting p { max-width: 440px; color: #9aabc1; font-size: 12px; line-height: 1.7; }
.node-detail-status { margin-top: 12px; border: 1px solid rgba(171, 196, 232, .15); border-radius: 999px; padding: 6px 11px; color: #ffb4aa; font-size: 10px; }
.node-detail-status[data-online='true'] { color: #9ce0b7; }
.docker-panel { margin-top: 20px; border: 1px solid rgba(171, 196, 232, .13); border-radius: 12px; padding: clamp(14px, 2vw, 20px); background: rgba(9, 17, 29, .46); }
.docker-heading { display: flex; justify-content: space-between; align-items: center; gap: 12px; margin-bottom: 14px; }
.docker-heading h2 { margin: 4px 0 0; font-size: 17px; }
.docker-state, .container-state { border: 1px solid rgba(171, 196, 232, .16); border-radius: 999px; padding: 5px 9px; color: #aebbd0; font-size: 10px; white-space: nowrap; }
.docker-state[data-available='available'][data-stale='false'], .container-state[data-running='true'] { color: #9ce0b7; border-color: rgba(121, 214, 156, .24); }
.docker-state[data-available='unavailable'], .docker-state[data-stale='true'], .container-stale { color: #ffc1b8; border-color: rgba(255, 129, 116, .25); }
.docker-list { display: grid; gap: 9px; }
.docker-row { min-width: 0; border: 1px solid rgba(171, 196, 232, .1); border-radius: 9px; padding: 12px; background: rgba(18, 29, 45, .68); }
.docker-row[data-stale='true'] { border-color: rgba(255, 176, 129, .22); }
.docker-row-title { display: flex; justify-content: space-between; align-items: start; gap: 12px; }
.docker-container-copy { display: grid; min-width: 0; gap: 4px; }
.docker-container-copy strong { overflow-wrap: anywhere; font-size: 12px; }
.docker-container-copy small { overflow-wrap: anywhere; color: #93a4bb; font-size: 10px; }
.docker-row-meta { display: flex; flex-wrap: wrap; gap: 7px 14px; margin-top: 9px; color: #a5b3c8; font-size: 10px; }
.container-ports { display: grid; gap: 4px; margin-top: 8px; color: #8dc9ff; font-size: 10px; overflow-wrap: anywhere; }
.container-reason, .docker-empty { color: #9aabc1; font-size: 11px; line-height: 1.6; overflow-wrap: anywhere; }
.container-reason { margin: 8px 0 0; }
.container-task-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-top: 10px; }
.container-action { min-height: 34px; border: 1px solid rgba(141, 201, 255, .25); border-radius: 7px; padding: 6px 10px; color: #cce6ff; background: rgba(62, 119, 170, .16); font: inherit; font-size: 10px; cursor: pointer; }
.container-action:hover:not(:disabled) { background: rgba(62, 119, 170, .3); }
.container-action:disabled { opacity: .48; cursor: not-allowed; }
.container-task-status, .container-task-error { color: #a8bdd7; font-size: 10px; overflow-wrap: anywhere; }
.container-task-status[data-status='succeeded'] { color: #9ce0b7; }
.container-task-status[data-status='failed'], .container-task-status[data-status='unknown'], .container-task-status[data-status='timed_out'], .container-task-error { color: #ffc1b8; }
@media (max-width: 760px) { .nodes-layout { grid-template-columns: minmax(0, 1fr); } .node-list { max-height: 270px; overflow: auto; } }
@media (max-width: 480px) { .nodes-dashboard { padding: 22px 14px; } .nodes-heading { flex-direction: column; align-items: flex-start; gap: 14px; } .nodes-heading p { max-width: 250px; } .stream-state { padding: 7px 9px; font-size: 9px; } .node-detail { padding: 12px; } }
</style>
