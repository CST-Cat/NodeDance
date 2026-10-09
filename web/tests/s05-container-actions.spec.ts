import { expect, test } from '@playwright/test'

const nodeID = '12345678-1234-4234-8234-123456789abc'
const pausedID = 'a'.repeat(64)
const restartingID = 'b'.repeat(64)
const serverTime = '2026-10-08T18:00:00.000Z'
const leaseValidUntil = '2026-10-08T18:00:30.000Z'

test('paused containers offer resume without start, and restarting containers cannot be deleted', async ({ page }) => {
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
        if (String(args[0]).includes('/ws/v1/dashboard')) {
          const socket = new DashboardSocketStub()
          queueMicrotask(() => socket.onopen?.(new Event('open')))
          return socket as unknown as WebSocket
        }
        return Reflect.construct(target, args)
      },
    }) })
  })

  const state = { nodeId: nodeID, agentId: 's05-ui-agent', status: 'online', generation: 1, serverTime, leaseValidUntil }
  const records = [
    { id: pausedID, name: 'nd-paused', state: 'paused', running: true, paused: true, restarting: false },
    { id: restartingID, name: 'nd-restarting', state: 'restarting', running: false, paused: false, restarting: true },
  ].map((fixture) => ({
    container: {
      ...fixture, image: 'busybox:1.37', imageId: 'sha256:busybox', health: 'none',
      healthcheckConfigured: false, stale: false, restartCount: 0, ports: [], observedAt: serverTime,
    },
    generation: 1,
    sequence: 1,
    receivedAt: serverTime,
  }))
  const inventory = {
    agentId: state.agentId,
    nodeId: nodeID,
    agentOnline: true,
    activeGeneration: 1,
    leaseValidUntil,
    dockerAvailability: 'available',
    dockerEventsConnected: true,
    dockerSnapshotFresh: true,
    dataStale: false,
    containers: records,
    serverTime,
  }
  const acceptedTask = {
    taskId: 'ndt_s05_resume_ui',
    nodeId: nodeID,
    targetId: pausedID,
    action: 'resume',
    status: 'succeeded',
    deliveryState: 'finished',
    reconciliationRequired: false,
    progress: { phase: 'verified', completed: 1, total: 1 },
    result: { observedState: 'running' },
    createdAt: serverTime,
    updatedAt: serverTime,
    startedAt: serverTime,
    finishedAt: serverTime,
  }
  const submittedActions: unknown[] = []
  await page.route('**/api/v1/auth/csrf', (route) => route.fulfill({ json: { token: 's05-test-csrf' } }))
  await page.route('**/api/v1/nodes', (route) => route.fulfill({ json: {
    serverTime,
    nodes: [{ ...state, displayName: 'S05 UI test node', lastSeen: serverTime,
      capabilities: ['agent.metrics.v1', 'agent.docker.v1'] }],
  } }))
  await page.route(`**/api/v1/nodes/${nodeID}/metrics`, (route) => route.fulfill({ json: {
    type: 'node_status', nodeId: nodeID, state,
  } }))
  await page.route(`**/api/v1/nodes/${nodeID}/containers`, (route) => route.fulfill({ json: {
    type: 'node_containers', nodeId: nodeID, state, inventory,
  } }))
  await page.route(`**/api/v1/nodes/${nodeID}/tasks?limit=50`, (route) => route.fulfill({ json: { tasks: [], nextCursor: '' } }))
  await page.route(`**/api/v1/nodes/${nodeID}/containers/${pausedID}/actions`, async (route) => {
    submittedActions.push(route.request().postDataJSON())
    await route.fulfill({ json: { taskId: acceptedTask.taskId, status: 'queued' } })
  })
  await page.route(`**/api/v1/nodes/${nodeID}/tasks/${acceptedTask.taskId}`, (route) => route.fulfill({ json: acceptedTask }))

  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  const pausedRow = page.locator(`article[data-container-id="${pausedID}"]`)
  await expect(pausedRow).toBeVisible()
  await expect(pausedRow.getByRole('button', { name: '恢复' })).toBeVisible()
  await expect(pausedRow.getByRole('button', { name: '启动' })).toHaveCount(0)
  await expect(pausedRow.getByRole('button', { name: '停止' })).toHaveCount(0)
  await expect(pausedRow.getByRole('button', { name: '重启' })).toHaveCount(0)
  await expect(pausedRow.getByRole('button', { name: '暂停' })).toHaveCount(0)

  await pausedRow.getByRole('button', { name: '恢复' }).click()
  await expect.poll(() => submittedActions).toEqual([{ action: 'resume' }])

  const restartingRow = page.locator(`article[data-container-id="${restartingID}"]`)
  await expect(restartingRow.getByRole('button', { name: '删除' })).toBeDisabled()
})
