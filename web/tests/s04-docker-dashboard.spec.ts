import { expect, test } from '@playwright/test'

const nodeID = '12345678-1234-4234-8234-123456789abc'
const serverTime = '2026-10-08T18:00:00.000Z'
const leaseValidUntil = '2026-10-08T18:00:30.000Z'

const known = <T>(value: T) => ({
  status: 'known',
  value,
  reason: '',
  sampledAt: serverTime,
  sampleAgeMillis: 0,
})

const state = {
  nodeId: nodeID,
  agentId: 'agent-s04-dashboard-test',
  status: 'online',
  generation: 2,
  serverTime,
  leaseValidUntil,
}

const metricView = {
  agentId: state.agentId,
  nodeId: nodeID,
  generation: 2,
  sequence: 10,
  bootId: 'boot-s04-test',
  collectedAt: serverTime,
  receivedAt: serverTime,
  clockOffsetMs: 0,
  nodeStatus: 'online',
  activeGeneration: 2,
  serverTime,
  leaseValidUntil,
  metrics: {
    system: {
      hostname: known('s04-test-node'), os: known('linux'), architecture: known('amd64'),
      platform: known('linux'), platformFamily: known('debian'), platformVersion: known('1'), kernelVersion: known('test'),
    },
    cpu: { usagePercent: known(12.5), logicalCores: known(4) },
    memory: known({ totalBytes: 8192, availableBytes: 4096, usedBytes: 4096, usedPercent: 50 }),
    network: { summary: known({ receivedBytesPerSecond: 100, sentBytesPerSecond: 200 }), interfaces: [] },
    disk: { status: 'unknown', sampledAt: serverTime, sampleAgeMillis: 0, mounts: [] },
    uptime: known({ seconds: 1200, bootId: 'boot-s04-test' }),
  },
}

const inventory = {
  agentId: state.agentId,
  nodeId: nodeID,
  agentOnline: true,
  activeGeneration: 2,
  leaseValidUntil,
  dockerAvailability: 'available',
  dockerEventsConnected: true,
  dockerSnapshotFresh: true,
  dataStale: false,
  containers: [
    {
      container: {
        id: 'a'.repeat(64), name: 'nd-web', image: 'nginx:alpine', imageId: 'sha256:web', state: 'running',
        running: true, paused: false, restarting: false, health: 'healthy', healthcheckConfigured: true,
        stale: false, restartCount: 0, observedAt: serverTime,
        ports: [{ containerPort: 80, protocol: 'tcp', exposed: true,
          configured: [{ ip: '127.0.0.1', port: '18080' }], published: [{ ip: '127.0.0.1', port: '18080' }] },
        { containerPort: 443, protocol: 'tcp', exposed: true,
          configured: [{ ip: '::1', port: '18443' }], published: [{ ip: '::1', port: '18443' }] }],
        compose: { project: 'nd-demo', service: 'web' },
      },
      generation: 2, sequence: 11, receivedAt: serverTime,
    },
    {
      container: {
        id: 'b'.repeat(64), name: 'nd-configured', image: 'busybox:1.37', imageId: 'sha256:busybox', state: 'created',
        running: false, paused: false, restarting: false, health: 'none', healthcheckConfigured: false,
        stale: false, restartCount: 0, observedAt: serverTime,
        ports: [{ containerPort: 53, protocol: 'udp', exposed: false,
          configured: [{ ip: '0.0.0.0', port: '15353' }], published: [] }],
      },
      generation: 2, sequence: 11, receivedAt: serverTime,
    },
    {
      container: {
        id: 'c'.repeat(64), name: '<img src=x onerror=alert(1)>', image: 'test:latest', imageId: 'sha256:test', state: 'exited',
        running: false, paused: false, restarting: false, health: 'none', healthcheckConfigured: false,
        stale: true, unavailableReason: 'container_inspect_unavailable', restartCount: 0, observedAt: serverTime,
        ports: [{ containerPort: 8080, protocol: 'tcp', exposed: true, configured: [], published: [] }],
      },
      generation: 2, sequence: 11, receivedAt: serverTime,
    },
  ],
  serverTime,
}

test('Docker inventory is readable, truthful about ports, safe, and responsive', async ({ page }) => {
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
          const socket = new DashboardSocketStub()
          queueMicrotask(() => socket.onopen?.(new Event('open')))
          return socket as unknown as WebSocket
        }
        return Reflect.construct(target, args)
      },
    }) })
  })
  await page.route('**/api/v1/nodes', (route) => route.fulfill({ json: {
    serverTime,
    nodes: [{ nodeId: nodeID, agentId: state.agentId, displayName: 'S04 test node', status: 'online', generation: 2,
      lastSeen: serverTime, leaseValidUntil, capabilities: ['agent.metrics.v1', 'agent.docker.v1'] }],
  } }))
  await page.route(`**/api/v1/nodes/${nodeID}/metrics`, (route) => route.fulfill({ json: metricView }))
  await page.route(`**/api/v1/nodes/${nodeID}/containers`, (route) => route.fulfill({ json: {
    type: 'node_containers', nodeId: nodeID, state, inventory,
  } }))

  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  await expect(page.getByTestId('docker-inventory')).toBeVisible()
  await expect(page.getByText('Engine 正常')).toBeVisible()
  await expect(page.getByText('nd-web')).toBeVisible()
  await expect(page.getByText('127.0.0.1:18080 → 80/tcp')).toBeVisible()
  await expect(page.getByText('[::1]:18443 → 443/tcp')).toBeVisible()
  await expect(page.getByText('53/udp（配置映射，当前未发布）')).toBeVisible()
  await expect(page.getByText('8080/tcp（仅声明）')).toBeVisible()
  await expect(page.getByText('健康：健康')).toBeVisible()
  await expect(page.getByText('过期数据')).toBeVisible()
  await expect(page.getByText('<img src=x onerror=alert(1)>')).toBeVisible()
  await expect(page.locator('img[src="x"]')).toHaveCount(0)
  expect(await page.evaluate(() => (window as Window & { xssExecuted?: boolean }).xssExecuted)).toBeUndefined()

  for (const width of [375, 768, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    const dimensions = await page.evaluate(() => ({
      viewport: document.documentElement.clientWidth,
      content: document.documentElement.scrollWidth,
    }))
    expect(dimensions.content).toBeLessThanOrEqual(dimensions.viewport)
    await expect(page.getByTestId('docker-inventory')).toBeVisible()
  }
})

test('API incompatibility keeps the last container snapshot clearly stale', async ({ page }) => {
  await page.addInitScript(() => {
    const NativeWebSocket = window.WebSocket
    Object.defineProperty(window, 'WebSocket', { value: new Proxy(NativeWebSocket, {
      construct(target, args) {
        if (String(args[0]).includes('/ws/v1/dashboard')) {
          class DashboardSocketStub {
            onopen: ((event: Event) => void) | null = null
            onmessage: ((event: MessageEvent) => void) | null = null
            onclose: ((event: Event) => void) | null = null
            onerror: ((event: Event) => void) | null = null
            send() {}
            close() { this.onclose?.(new Event('close')) }
          }
          const socket = new DashboardSocketStub()
          queueMicrotask(() => socket.onopen?.(new Event('open')))
          return socket as unknown as WebSocket
        }
        return Reflect.construct(target, args)
      },
    }) })
  })
  const safeReason = 'Docker Engine API version is incompatible'
  const staleInventory = {
    ...inventory,
    dockerAvailability: 'available' as const,
    dockerSnapshotFresh: false,
    dataStale: true,
    health: { errorKind: 'api_incompatible', reason: safeReason },
    containers: inventory.containers.slice(0, 1).map((record) => ({
      ...record,
      container: { ...record.container, stale: true, unavailableReason: safeReason },
    })),
  }
  await page.route('**/api/v1/nodes', (route) => route.fulfill({ json: {
    serverTime,
    nodes: [{ nodeId: nodeID, agentId: state.agentId, displayName: 'S04 test node', status: 'online', generation: 2,
      lastSeen: serverTime, leaseValidUntil, capabilities: ['agent.metrics.v1', 'agent.docker.v1'] }],
  } }))
  await page.route(`**/api/v1/nodes/${nodeID}/metrics`, (route) => route.fulfill({ json: metricView }))
  await page.route(`**/api/v1/nodes/${nodeID}/containers`, (route) => route.fulfill({ json: {
    type: 'node_containers', nodeId: nodeID, state, inventory: staleInventory,
  } }))

  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  await expect(page.getByTestId('docker-inventory')).toBeVisible()
  await expect(page.getByText('Docker API 不兼容')).toBeVisible()
  await expect(page.getByTestId('docker-health-reason')).toContainText(safeReason)
  await expect(page.getByText('Engine 正常')).toHaveCount(0)
  await expect(page.locator('.docker-row')).toHaveCount(1)
  await expect(page.locator('.docker-row')).toHaveAttribute('data-stale', 'true')
  await expect(page.locator('.container-stale')).toContainText(safeReason)
})

test('dashboard applies and renders all rows from a complete streamed inventory', async ({ page }) => {
  await page.addInitScript(() => {
    type SocketHandler = ((event: Event | MessageEvent) => void) | null
    type TestSocket = { onopen: SocketHandler; onmessage: SocketHandler; onclose: SocketHandler; onerror: SocketHandler }
    const sockets: TestSocket[] = []
    class DashboardSocketStub implements TestSocket {
      onopen: SocketHandler = null
      onmessage: SocketHandler = null
      onclose: SocketHandler = null
      onerror: SocketHandler = null
      send() {}
      close() { this.onclose?.(new Event('close')) }
    }
    const NativeWebSocket = window.WebSocket
    Object.defineProperty(window, 'WebSocket', { value: new Proxy(NativeWebSocket, {
      construct(target, args) {
        if (!String(args[0]).includes('/ws/v1/dashboard')) return Reflect.construct(target, args)
        const socket = new DashboardSocketStub()
        sockets.push(socket)
        queueMicrotask(() => socket.onopen?.(new Event('open')))
        return socket as unknown as WebSocket
      },
    }) })
    Object.defineProperty(window, '__emitDockerInventory', {
      value: (message: unknown) => sockets.at(-1)?.onmessage?.(new MessageEvent('message', { data: JSON.stringify(message) })),
    })
  })
  const initialInventory = { ...inventory, containers: inventory.containers.slice(0, 1) }
  const fullInventory = {
    ...inventory,
    serverTime: '2026-10-08T18:00:10.000Z',
    containers: Array.from({ length: 200 }, (_, index) => {
      const source = inventory.containers[index % inventory.containers.length]
      const id = index.toString(16).padStart(64, '0')
      return {
        ...source,
        container: {
          ...source.container,
          id,
          name: `nd-stream-${String(index).padStart(3, '0')}`,
          state: index % 2 === 0 ? 'running' : 'exited',
          running: index % 2 === 0,
          observedAt: '2026-10-08T18:00:10.000Z',
        },
        sequence: index + 100,
        receivedAt: '2026-10-08T18:00:10.000Z',
      }
    }),
  }
  await page.route('**/api/v1/nodes', (route) => route.fulfill({ json: {
    serverTime,
    nodes: [{ nodeId: nodeID, agentId: state.agentId, displayName: 'S04 test node', status: 'online', generation: 2,
      lastSeen: serverTime, leaseValidUntil, capabilities: ['agent.metrics.v1', 'agent.docker.v1'] }],
  } }))
  await page.route(`**/api/v1/nodes/${nodeID}/metrics`, (route) => route.fulfill({ json: metricView }))
  await page.route(`**/api/v1/nodes/${nodeID}/containers`, (route) => route.fulfill({ json: {
    type: 'node_containers', nodeId: nodeID, state, inventory: initialInventory,
  } }))

  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  await expect(page.getByText('nd-web')).toBeVisible()
  await page.evaluate((message) => {
    ;(window as Window & { __emitDockerInventory: (value: unknown) => void }).__emitDockerInventory(message)
  }, { type: 'node_containers', nodeId: nodeID, state: { ...state, serverTime: fullInventory.serverTime }, inventory: fullInventory })
  await expect(page.locator('.docker-row')).toHaveCount(200)
  await expect(page.getByText('nd-stream-000')).toBeVisible()
  await expect(page.getByText('nd-stream-199')).toBeVisible()
})

test('dashboard marks prior-generation or wrong-agent Docker inventory stale until matching authority arrives', async ({ page }) => {
  await page.addInitScript(() => {
    type SocketHandler = ((event: Event | MessageEvent) => void) | null
    type TestSocket = { onopen: SocketHandler; onmessage: SocketHandler; onclose: SocketHandler; onerror: SocketHandler }
    const sockets: TestSocket[] = []
    class DashboardSocketStub implements TestSocket {
      onopen: SocketHandler = null
      onmessage: SocketHandler = null
      onclose: SocketHandler = null
      onerror: SocketHandler = null
      send() {}
      close() { this.onclose?.(new Event('close')) }
    }
    const NativeWebSocket = window.WebSocket
    Object.defineProperty(window, 'WebSocket', { value: new Proxy(NativeWebSocket, {
      construct(target, args) {
        if (!String(args[0]).includes('/ws/v1/dashboard')) return Reflect.construct(target, args)
        const socket = new DashboardSocketStub()
        sockets.push(socket)
        queueMicrotask(() => socket.onopen?.(new Event('open')))
        return socket as unknown as WebSocket
      },
    }) })
    Object.defineProperty(window, '__emitDockerDashboardEvent', {
      value: (message: unknown) => sockets.at(-1)?.onmessage?.(new MessageEvent('message', { data: JSON.stringify(message) })),
    })
  })
  await page.route('**/api/v1/nodes', (route) => route.fulfill({ json: {
    serverTime,
    nodes: [{ nodeId: nodeID, agentId: state.agentId, displayName: 'S04 test node', status: 'online', generation: 2,
      lastSeen: serverTime, leaseValidUntil, capabilities: ['agent.metrics.v1', 'agent.docker.v1'] }],
  } }))
  await page.route(`**/api/v1/nodes/${nodeID}/metrics`, (route) => route.fulfill({ json: metricView }))
  await page.route(`**/api/v1/nodes/${nodeID}/containers`, (route) => route.fulfill({ json: {
    type: 'node_containers', nodeId: nodeID, state, inventory,
  } }))

  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  const dockerPanel = page.getByTestId('docker-inventory')
  await expect(page.getByText('Engine 正常')).toBeVisible()
  await expect(page.getByText('nd-web')).toBeVisible()

  const generation3Time = '2026-10-08T18:00:10.000Z'
  const generation3Lease = '2026-10-08T18:00:40.000Z'
  const generation3Metrics = {
    ...metricView,
    generation: 3,
    activeGeneration: 3,
    sequence: 1,
    serverTime: generation3Time,
    leaseValidUntil: generation3Lease,
  }
  await page.evaluate((metrics) => {
    ;(window as Window & { __emitDockerDashboardEvent: (value: unknown) => void }).__emitDockerDashboardEvent({
      type: 'node_metrics', nodeId: metrics.nodeId, metrics,
    })
  }, generation3Metrics)

  // A new connection's metrics clock must not make the previously rendered
  // Docker snapshot look current while the containers endpoint is unavailable.
  await expect(dockerPanel.locator('.docker-state')).toHaveAttribute('data-stale', 'true')
  await expect(page.getByText('nd-web')).toBeVisible()
  await expect(page.getByText('过期数据').first()).toBeVisible()

  const generation3Inventory = {
    ...inventory,
    activeGeneration: 3,
    serverTime: generation3Time,
    leaseValidUntil: generation3Lease,
    containers: inventory.containers.map((record) => ({
      ...record,
      generation: 3,
      receivedAt: generation3Time,
    })),
  }
  const emitInventory = async (incomingInventory: typeof generation3Inventory, agentId = state.agentId, time = generation3Time) => {
    await page.evaluate(({ incomingInventory, agentId, time, nodeID, state, leaseValidUntil }) => {
      ;(window as Window & { __emitDockerDashboardEvent: (value: unknown) => void }).__emitDockerDashboardEvent({
        type: 'node_containers', nodeId: nodeID,
        state: { ...state, agentId, generation: 3, serverTime: time, leaseValidUntil },
        inventory: { ...incomingInventory, agentId, serverTime: time },
      })
    }, { incomingInventory, agentId, time, nodeID, state, leaseValidUntil: generation3Lease })
  }

  // Generation alone is insufficient when a node was re-enrolled under a
  // different device identity; reject that inventory for freshness display.
  await emitInventory(generation3Inventory, 'another-agent', '2026-10-08T18:00:11.000Z')
  await expect(dockerPanel.locator('.docker-state')).toHaveAttribute('data-stale', 'true')
  await expect(page.getByText('nd-web')).toBeVisible()

  await emitInventory(generation3Inventory, state.agentId, '2026-10-08T18:00:12.000Z')
  await expect(dockerPanel.locator('.docker-state')).toHaveAttribute('data-stale', 'false')
  await expect(page.getByText('Engine 正常')).toBeVisible()
})
