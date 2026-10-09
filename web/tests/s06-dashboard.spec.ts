import { expect, test } from '@playwright/test'

const nodeID = '12345678-1234-4234-8234-123456789abc'
const agentID = 'agent-s06-browser'
const serverTime = new Date().toISOString()
const leaseValidUntil = new Date(Date.now() + 30_000).toISOString()

const known = <T>(value: T) => ({ status: 'known', value, reason: '', sampledAt: serverTime, sampleAgeMillis: 0 })

const identity = (index: number) => index.toString(16).padStart(64, '0')

const records = Array.from({ length: 40 }, (_, index) => {
  const id = identity(index + 1)
  return {
    container: {
      id,
      name: `service-${String(index).padStart(2, '0')}`,
      image: 'busybox:1.37',
      imageId: `sha256:${index}`,
      state: index % 2 === 0 ? 'running' : 'exited',
      running: index % 2 === 0,
      paused: false,
      restarting: false,
      health: 'none',
      healthcheckConfigured: false,
      stale: false,
      restartCount: 0,
      observedAt: serverTime,
      ports: [],
    },
    generation: 1,
    sequence: index + 1,
    receivedAt: serverTime,
  }
})

const preferences = records.map(({ container }, index) => ({
  nodeId: nodeID,
  targetKind: 'container' as const,
  identity: `container:${container.id}`,
  alias: index === 20 ? 'Pinned Service' : '',
  icon: index === 20 ? 'server' : '',
  notes: index === 20 ? 'Homepage favorite' : '',
  serviceUrl: index === 20 ? 'https://service.example.test' : '',
  sortOrder: index,
  visible: index !== 1,
  pinned: index === 20,
}))

test.use({ viewport: { width: 375, height: 900 }, hasTouch: true })

test('S06 homepage caps featured containers and keeps all rows, preferences, and responsive controls available', async ({ page }) => {
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
        const socket = new DashboardSocketStub()
        queueMicrotask(() => socket.onopen?.(new Event('open')))
        return socket as unknown as WebSocket
      },
    }) })
  })

  await page.route('**/api/v1/nodes', (route) => route.fulfill({ json: {
    serverTime,
    nodes: [{ nodeId: nodeID, agentId: agentID, displayName: 'S06 test node', status: 'online', generation: 1,
      lastSeen: serverTime, leaseValidUntil, capabilities: ['agent.metrics.v1', 'agent.docker.v1'] }],
  } }))
  await page.route(`**/api/v1/nodes/${nodeID}/metrics`, (route) => route.fulfill({ json: {
    agentId: agentID, nodeId: nodeID, generation: 1, sequence: 1, bootId: 's06-boot',
    collectedAt: serverTime, receivedAt: serverTime, clockOffsetMs: 0, nodeStatus: 'online', activeGeneration: 1,
    serverTime, leaseValidUntil,
    metrics: {
      system: { hostname: known('s06-test-node'), os: known('linux'), architecture: known('amd64'),
        platform: known('linux'), platformFamily: known('debian'), platformVersion: known('1'), kernelVersion: known('test') },
      cpu: { usagePercent: known(12.5), logicalCores: known(4) },
      memory: known({ totalBytes: 8192, availableBytes: 4096, usedBytes: 4096, usedPercent: 50 }),
      network: { summary: known({ receivedBytesPerSecond: 100, sentBytesPerSecond: 200 }), interfaces: [] },
      disk: { status: 'unknown', sampledAt: serverTime, sampleAgeMillis: 0, mounts: [] },
      uptime: known({ seconds: 1200, bootId: 's06-boot' }),
    },
  } }))
  await page.route(`**/api/v1/nodes/${nodeID}/containers`, (route) => route.fulfill({ json: {
    type: 'node_containers', nodeId: nodeID,
    state: { nodeId: nodeID, status: 'online', generation: 1, serverTime, leaseValidUntil },
    inventory: { agentId: agentID, nodeId: nodeID, agentOnline: true, activeGeneration: 1, leaseValidUntil,
      dockerAvailability: 'available', dockerEventsConnected: true, dockerSnapshotFresh: true,
      dataStale: false, containers: records, serverTime },
    preferenceIdentities: Object.fromEntries(records.map(({ container }) => [container.id, `container:${container.id}`])),
  } }))
  let savedPreferences = [...preferences]
  await page.route(`**/api/v1/nodes/${nodeID}/preferences`, (route) => route.fulfill({ json: { preferences: savedPreferences } }))
  await page.route(`**/api/v1/nodes/${nodeID}/preferences`, async (route) => {
    if (route.request().method() !== 'PUT') return route.fallback()
    const preference = route.request().postDataJSON() as typeof preferences[number]
    savedPreferences = savedPreferences.filter((item) => item.targetKind !== preference.targetKind || item.identity !== preference.identity)
    savedPreferences.push(preference)
    await route.fulfill({ status: 204 })
  })
  await page.route('**/api/v1/auth/csrf', (route) => route.fulfill({ json: { token: 's06-browser-csrf' } }))
  await page.route(`**/api/v1/nodes/${nodeID}/tasks`, (route) => route.fulfill({ json: { tasks: [] } }))
  await page.route('**/api/v1/dashboard/settings', (route) => route.fulfill({ json: {
    viewMode: 'monitor', groupBy: 'node', sortBy: 'custom', featuredLimit: 4, customFields: ['state', 'ports', 'health'],
  } }))

  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  await expect(page.getByTestId('fused-overview')).toBeVisible()
  await expect(page.locator('.featured-container')).toHaveCount(4)
  await expect(page.locator('.featured-container').first()).toContainText('Pinned Service')
  await expect(page.locator('.featured-container').first()).toContainText('Homepage favorite')
  await expect(page.locator(`.featured-container[data-container-id="${identity(2)}"]`)).toHaveCount(0)

  await page.getByRole('button', { name: 'Docker 详情' }).click()
  await expect(page.locator('.docker-row')).toHaveCount(40)
  await expect(page.locator('.docker-row[data-container-id="0000000000000000000000000000000000000000000000000000000000000002"]')).toHaveCount(1)
  await page.getByRole('searchbox', { name: '搜索容器' }).fill('service-39')
  await expect(page.locator('.docker-row')).toHaveCount(1)
  await expect(page.locator('.docker-row')).toContainText('service-39')

  await page.getByRole('searchbox', { name: '搜索容器' }).fill('')
  await page.setViewportSize({ width: 375, height: 900 })
  const sourceHandle = page.locator(`.docker-row[data-container-id="${identity(1)}"] .drag-handle`)
  const targetRow = page.locator(`.docker-row[data-container-id="${identity(40)}"]`)
  await targetRow.scrollIntoViewIfNeeded()
  const sourcePoint = await sourceHandle.evaluate((element) => {
    const rect = element.getBoundingClientRect()
    return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 }
  })
  await sourceHandle.evaluate((element, point) => element.dispatchEvent(new PointerEvent('pointerdown', {
    bubbles: true, pointerId: 17, pointerType: 'touch', clientX: point.x, clientY: point.y,
  })), sourcePoint)
  const targetPoint = await targetRow.evaluate((element) => {
    const rect = element.getBoundingClientRect()
    return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 }
  })
  await page.evaluate(({ x, y }) => document.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, pointerId: 17, pointerType: 'touch', clientX: x, clientY: y })), targetPoint)
  await page.evaluate(() => document.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, pointerId: 17, pointerType: 'touch' })))
  await expect.poll(() => savedPreferences.find((item) => item.identity === `container:${identity(1)}`)?.sortOrder).toBe(39)
  await page.reload()
  await expect(page.getByTestId('fused-overview')).toBeVisible()
  await page.getByRole('button', { name: 'Docker 详情' }).click()
  await expect(page.locator('.docker-row').last()).toContainText('service-00')

  await page.getByRole('button', { name: '节点设置' }).click()
  await expect(page.getByRole('checkbox', { name: '镜像' })).toBeVisible()
  const sortOptions = await page.locator('.dashboard-settings-grid select').nth(2).locator('option').allTextContents()
  expect(sortOptions).not.toContain('CPU 使用率')
  expect(sortOptions).not.toContain('内存使用率')

  for (const width of [375, 768, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    const dimensions = await page.evaluate(() => ({ viewport: document.documentElement.clientWidth, content: document.documentElement.scrollWidth }))
    expect(dimensions.content).toBeLessThanOrEqual(dimensions.viewport)
    await expect(page.getByTestId('node-settings')).toBeVisible()
  }
})
