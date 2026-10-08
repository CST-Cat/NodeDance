<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api, type AgentNode, type AgentNodesResponse, type NodeStatusResponse } from '../api'
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
const clocks = ref<Record<string, NodeClock>>({})
const nodeReasons = ref<Record<string, string>>({})
const busy = ref(true)
const error = ref('')
const elapsed = ref(0)
const socketState = ref<'connecting' | 'connected' | 'retrying'>('connecting')
let socket: WebSocket | undefined
let reconnectTimer: ReturnType<typeof setTimeout> | undefined
let refreshTimer: ReturnType<typeof setInterval> | undefined
let tickTimer: ReturnType<typeof setInterval> | undefined
let reconnectDelay = 1000
let disposed = false
let latestNodeListServerTime = Number.NEGATIVE_INFINITY

const selectedNode = computed(() => nodes.value.find((node) => node.nodeId === selectedNodeID.value) ?? null)
const selectedView = computed(() => views.value[selectedNodeID.value] ?? null)

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
  if (node?.agentId) await loadMetrics(node)
}

async function loadMetrics(node: AgentNode) {
	try {
		const result = await api.nodeMetrics(node.nodeId)
    acceptMetricsResponse(result)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法加载主机指标。'
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
  views.value = { ...views.value, [result.nodeId]: result }
  setNodeReason(result.nodeId)
  setClock(result.nodeId, result.nodeStatus, result.serverTime, result.leaseValidUntil, result.activeGeneration)
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
    void loadMetrics(node)
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
})

onBeforeUnmount(() => {
  disposed = true
  if (reconnectTimer !== undefined) clearTimeout(reconnectTimer)
  if (refreshTimer !== undefined) clearInterval(refreshTimer)
  if (tickTimer !== undefined) clearInterval(tickTimer)
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
@media (max-width: 760px) { .nodes-layout { grid-template-columns: minmax(0, 1fr); } .node-list { max-height: 270px; overflow: auto; } }
@media (max-width: 480px) { .nodes-dashboard { padding: 22px 14px; } .nodes-heading { flex-direction: column; align-items: flex-start; gap: 14px; } .nodes-heading p { max-width: 250px; } .stream-state { padding: 7px 9px; font-size: 9px; } .node-detail { padding: 12px; } }
</style>
