import { expect, test } from '@playwright/test'

const nodeID = '12345678-1234-4234-8234-123456789abc'
const t1 = '2030-01-02T03:04:05.000Z'
const t2 = '2030-01-02T03:04:10.000Z'
const t3 = '2030-01-02T03:04:12.000Z'

function nodeList(serverTime: string, status: string, generation: number, leaseValidUntil: string) {
  return {
    serverTime,
    nodes: [{
      nodeId: nodeID,
      agentId: 'agent-race-test',
      displayName: 'race-test-node',
      status,
      generation,
      lastSeen: serverTime,
      leaseValidUntil,
      agentVersion: 'test-agent',
      capabilities: ['agent.metrics.v1'],
    }],
  }
}

function metricView(options: {
  activeGeneration: number
  generation: number
  sequence: number
  cpu: number
  nodeStatus: 'online' | 'offline'
  serverTime: string
  leaseValidUntil: string
}) {
  const known = <T>(value: T) => ({ status: 'known', value, reason: '', sampledAt: options.serverTime, sampleAgeMillis: 0 })
  const memory = { totalBytes: 8_000, availableBytes: 5_000, usedBytes: 3_000, usedPercent: 37.5 }
  const disk = { totalBytes: 100_000, availableBytes: 40_000, usedBytes: 60_000, usedPercent: 60 }
  return {
    agentId: 'agent-race-test',
    nodeId: nodeID,
    generation: options.generation,
    sequence: options.sequence,
    bootId: 'boot-race-test',
    collectedAt: options.serverTime,
    receivedAt: options.serverTime,
    clockOffsetMs: 0,
    nodeStatus: options.nodeStatus,
    activeGeneration: options.activeGeneration,
    serverTime: options.serverTime,
    leaseValidUntil: options.leaseValidUntil,
    metrics: {
      system: {
        hostname: known('race-test-node'), os: known('linux'), architecture: known('amd64'),
        platform: known('test'), platformFamily: known('test'), platformVersion: known('1'), kernelVersion: known('1'),
      },
      cpu: { usagePercent: known(options.cpu), logicalCores: known(4) },
      memory: known(memory),
      network: { summary: known({ receivedBytesPerSecond: 10, sentBytesPerSecond: 20 }), interfaces: [] },
      disk: { status: 'known', sampledAt: options.serverTime, sampleAgeMillis: 0, mounts: [{ device: '/dev/test', mountpoint: '/', filesystem: 'test', usage: known(disk) }] },
      uptime: known({ seconds: 1000, bootId: 'boot-race-test' }),
    },
  }
}

test('nodes dashboard fits phone, tablet, and desktop widths', async ({ page }) => {
  await page.addInitScript(() => {
    class DashboardSocketStub {
      onopen: ((event: Event) => void) | null = null
      onmessage: ((event: MessageEvent) => void) | null = null
      onclose: ((event: Event) => void) | null = null
      onerror: ((event: Event) => void) | null = null
      constructor() { queueMicrotask(() => this.onopen?.(new Event('open'))) }
      send() {}
      close() { this.onclose?.(new Event('close')) }
    }
    const NativeWebSocket = window.WebSocket
    Object.defineProperty(window, 'WebSocket', { value: new Proxy(NativeWebSocket, {
      construct(target, args) {
        if (String(args[0]).includes('/ws/v1/dashboard')) {
          const stub = new DashboardSocketStub()
          queueMicrotask(() => stub.onopen?.(new Event('open')))
          return stub as unknown as WebSocket
        }
        return Reflect.construct(target, args)
      },
    }) })
  })
  await page.route('**/api/v1/nodes', (route) => route.fulfill({ json: { nodes: [], serverTime: t2 } }))
  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  await expect(page.getByRole('heading', { name: '节点指标.' })).toBeVisible()
  for (const width of [375, 768, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    const dimensions = await page.evaluate(() => ({
      viewport: document.documentElement.clientWidth,
      content: document.documentElement.scrollWidth,
    }))
    expect(dimensions.content).toBeLessThanOrEqual(dimensions.viewport)
    await expect(page.getByRole('complementary', { name: '节点列表' })).toBeVisible()
  }
})

test('pending node selection shows registration wait state without a metrics error', async ({ page }) => {
  await page.addInitScript(() => {
    class DashboardSocketStub {
      onopen: ((event: Event) => void) | null = null
      onmessage: ((event: MessageEvent) => void) | null = null
      onclose: ((event: Event) => void) | null = null
      onerror: ((event: Event) => void) | null = null
      send() {}
      close() { this.onclose?.(new Event('close')) }
    }
    const NativeWebSocket = window.WebSocket
    Object.defineProperty(window, 'WebSocket', { value: new Proxy(NativeWebSocket, {
      construct(target, args) {
        if (!String(args[0]).includes('/ws/v1/dashboard')) return Reflect.construct(target, args)
        const stub = new DashboardSocketStub()
        queueMicrotask(() => stub.onopen?.(new Event('open')))
        return stub as unknown as WebSocket
      },
    }) })
  })
  await page.route('**/api/v1/nodes', (route) => route.fulfill({ json: {
    serverTime: t2,
    nodes: [{ nodeId: nodeID, displayName: 'pending-node', status: 'pending', generation: 0, capabilities: [] }],
  } }))
  let metricsRequests = 0
  await page.route(`**/api/v1/nodes/${nodeID}/metrics`, (route) => {
    metricsRequests += 1
    return route.fulfill({ status: 500, body: 'pending node should not request metrics' })
  })
  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  await expect(page.locator('.node-option-copy small')).toContainText('等待 Agent 注册')
  await page.locator('.node-option').click()
  await expect(page.getByRole('heading', { name: '等待 Agent 注册' })).toBeVisible()
  await expect(page.locator('.metrics-waiting p')).toContainText('Agent 完成注册后')
  await expect(page.getByRole('alert')).toHaveCount(0)
  expect(metricsRequests).toBe(0)
})

test('late node and metric HTTP responses cannot roll back newer dashboard state', async ({ page }) => {
  const oldList = nodeList(t1, 'offline', 2, '2030-01-02T03:04:06.000Z')
  const currentList = nodeList(t2, 'online', 2, '2030-01-02T03:05:00.000Z')
  const currentMetrics = metricView({
    activeGeneration: 2, generation: 2, sequence: 20, cpu: 21, nodeStatus: 'online',
    serverTime: t2, leaseValidUntil: '2030-01-02T03:05:00.000Z',
  })
  const lateOldMetrics = metricView({
    activeGeneration: 1, generation: 1, sequence: 99, cpu: 2, nodeStatus: 'offline',
    serverTime: t1, leaseValidUntil: '2030-01-02T03:04:06.000Z',
  })

  await page.addInitScript(() => {
    type SocketHandler = ((event: Event | MessageEvent) => void) | null
    type TestSocket = {
      onopen: SocketHandler
      onmessage: SocketHandler
      onclose: SocketHandler
      onerror: SocketHandler
      close: () => void
    }
    const sockets: TestSocket[] = []
    class DashboardSocket implements TestSocket {
      onopen: SocketHandler = null
      onmessage: SocketHandler = null
      onclose: SocketHandler = null
      onerror: SocketHandler = null
      constructor() {}
      send() {}
      close() { this.onclose?.(new Event('close')) }
    }
    const NativeWebSocket = window.WebSocket
    Object.defineProperty(window, 'WebSocket', { value: new Proxy(NativeWebSocket, {
      construct(target, args) {
        if (!String(args[0]).includes('/ws/v1/dashboard')) return Reflect.construct(target, args)
        const socket = new DashboardSocket()
        sockets.push(socket)
        queueMicrotask(() => socket.onopen?.(new Event('open')))
        return socket as unknown as WebSocket
      },
    }) })
    Object.defineProperty(window, '__emitDashboardMessage', {
      value: (message: unknown) => sockets.at(-1)?.onmessage?.(new MessageEvent('message', { data: JSON.stringify(message) })),
    })
    Object.defineProperty(window, '__emitDashboardMessageFrom', {
      value: (index: number, message: unknown) => sockets[index]?.onmessage?.(new MessageEvent('message', { data: JSON.stringify(message) })),
    })
    Object.defineProperty(window, '__disconnectDashboardSocket', {
      value: () => sockets.at(-1)?.onclose?.(new Event('close')),
    })
    Object.defineProperty(window, '__fireDashboardSocketClose', {
      value: (index: number) => sockets[index]?.onclose?.(new Event('close')),
    })
    Object.defineProperty(window, '__dashboardSocketCount', { get: () => sockets.length })
  })

  let releaseOldNodes!: () => void
  let markOldNodesRequested!: () => void
  const oldNodesStarted = new Promise<void>((resolve) => { markOldNodesRequested = resolve })
  const oldNodesResponse = new Promise<void>((resolve) => { releaseOldNodes = resolve })
  let nodeCalls = 0
  await page.route(`**/api/v1/nodes`, async (route) => {
    nodeCalls += 1
    if (nodeCalls === 1) {
      markOldNodesRequested()
      await oldNodesResponse
      await route.fulfill({ json: oldList })
      return
    }
    await route.fulfill({ json: currentList })
  })

  let releaseOldMetrics!: () => void
  let markOldMetricsRequested!: () => void
  const oldMetricsStarted = new Promise<void>((resolve) => { markOldMetricsRequested = resolve })
  const oldMetricsResponse = new Promise<void>((resolve) => { releaseOldMetrics = resolve })
  let metricCalls = 0
  await page.route(`**/api/v1/nodes/${nodeID}/metrics`, async (route) => {
    metricCalls += 1
    if (metricCalls === 2) {
      markOldMetricsRequested()
      await oldMetricsResponse
      await route.fulfill({ json: lateOldMetrics })
      return
    }
    await route.fulfill({ json: currentMetrics })
  })

  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  await oldNodesStarted
  await expect(page.getByTestId('cpu-value')).toHaveText('21.0%')
  await page.locator('.node-option').click()
  await oldMetricsStarted
  await page.evaluate((metrics) => {
    ;(window as Window & { __emitDashboardMessage: (message: unknown) => void }).__emitDashboardMessage({
      type: 'node_metrics', nodeId: metrics.nodeId, metrics,
    })
  }, metricView({
    activeGeneration: 2, generation: 2, sequence: 21, cpu: 34.5, nodeStatus: 'online',
    serverTime: t3, leaseValidUntil: '2030-01-02T03:05:02.000Z',
  }))
  await expect(page.getByTestId('cpu-value')).toHaveText('34.5%')

  releaseOldMetrics()
  releaseOldNodes()
  await expect(page.getByTestId('cpu-value')).toHaveText('34.5%')
  await expect(page.locator('.node-option-copy small')).toContainText('在线')
  await page.waitForTimeout(1_500)
  await expect(page.locator('.node-option-copy small')).toContainText('在线')
  await expect(page.getByTestId('node-status')).toHaveText('在线')

  await page.evaluate(() => (window as Window & { __disconnectDashboardSocket: () => void }).__disconnectDashboardSocket())
  await expect.poll(() => page.evaluate(() => (window as Window & { __dashboardSocketCount: number }).__dashboardSocketCount)).toBe(2)
  await expect(page.locator('.stream-state')).toHaveAttribute('data-state', 'connected')
  await page.evaluate((metrics) => {
    ;(window as Window & { __emitDashboardMessageFrom: (index: number, message: unknown) => void }).__emitDashboardMessageFrom(0, {
      type: 'node_metrics', nodeId: metrics.nodeId, metrics,
    })
    ;(window as Window & { __emitDashboardMessageFrom: (index: number, message: unknown) => void }).__emitDashboardMessageFrom(0, {
      type: 'node_status', nodeId: metrics.nodeId, state: {
        nodeId: metrics.nodeId, status: 'offline', generation: 1, serverTime: '2030-01-02T03:04:05.000Z',
        leaseValidUntil: '2030-01-02T03:04:06.000Z',
      },
    })
    ;(window as Window & { __fireDashboardSocketClose: (index: number) => void }).__fireDashboardSocketClose(0)
  }, lateOldMetrics)
  await expect(page.getByTestId('cpu-value')).toHaveText('34.5%')
  await expect(page.getByTestId('node-status')).toHaveText('在线')
  await expect(page.locator('.stream-state')).toHaveAttribute('data-state', 'connected')
  expect(metricCalls).toBe(3)
})
