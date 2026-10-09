import { expect, test } from '@playwright/test'

const nodeID = '12345678-1234-4234-8234-123456789abc'
const containerID = 'a'.repeat(64)
const replacementID = 'b'.repeat(64)
const serverTime = '2026-10-08T18:00:00.000Z'
const leaseValidUntil = '2026-10-08T18:00:30.000Z'

async function openDockerDetails(page: import('@playwright/test').Page) {
  await page.getByRole('button', { name: 'Docker 详情' }).click()
  await expect(page.getByTestId('docker-inventory')).toBeVisible()
}

test('responsive rebuild wizard previews risks and submits the reviewed port plan', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
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

  const state = { nodeId: nodeID, agentId: 's12-ui-agent', status: 'online', generation: 1, serverTime, leaseValidUntil }
  const container = {
    id: containerID, name: 'nd-web', image: 'nginx:locked', imageId: 'sha256:fixture', state: 'running', running: true,
    paused: false, restarting: false, health: 'healthy', healthcheckConfigured: false, stale: false, restartCount: 0,
    ports: [{ containerPort: 80, protocol: 'tcp', exposed: true,
      configured: [{ ip: '127.0.0.1', port: '18080' }], published: [{ ip: '127.0.0.1', port: '18080' }] }], observedAt: serverTime,
  }
  const inventory = {
    agentId: state.agentId, nodeId: nodeID, agentOnline: true, activeGeneration: 1, leaseValidUntil,
    dockerAvailability: 'available', dockerEventsConnected: true, dockerSnapshotFresh: true, dataStale: false,
    containers: [{ container, generation: 1, sequence: 1, receivedAt: serverTime }], serverTime,
  }
  const submittedActions: unknown[] = []
  const plannedSpecs: unknown[] = []
  const listedTasks: unknown[] = []
  const task = {
    taskId: 'ndt_s12_ui_rebuild', nodeId: nodeID, targetId: containerID, action: 'rebuild', status: 'succeeded',
    deliveryState: 'finished', reconciliationRequired: false,
    progress: { phase: 'verified', completed: 6, total: 6 },
    result: { code: 'verified', observedState: 'running', resourceRevision: replacementID },
    createdAt: serverTime, updatedAt: serverTime, startedAt: serverTime, finishedAt: serverTime,
  }
  const plan = {
    containerId: containerID, name: 'nd-web', imageId: 'sha256:fixture', wasRunning: true,
    writableLayerBytes: 4096, snapshotRequired: true,
    portsBefore: ['80/tcp <- 127.0.0.1:18080'], portsAfter: ['80/tcp <- 127.0.0.1:18081'],
    preserved: ['restart policy', 'named volumes', 'bind mounts', 'network aliases'],
    changed: ['published ports will change'], downtime: 'The current container stops during the name and network move.',
    risks: ['The snapshot covers the writable layer only; mounted volume and bind-mount data are not backed up.'],
    mounts: [{ type: 'bind', destination: '/srv/app/data', readWrite: true }],
  }

  await page.route('**/api/v1/auth/csrf', (route) => route.fulfill({ json: { token: 's12-test-csrf' } }))
  await page.route('**/api/v1/nodes', (route) => route.fulfill({ json: {
    serverTime, nodes: [{ ...state, displayName: 'S12 UI test node', lastSeen: serverTime,
      capabilities: ['agent.metrics.v1', 'agent.docker.v1'] }],
  } }))
  await page.route(`**/api/v1/nodes/${nodeID}/metrics`, (route) => route.fulfill({ json: { type: 'node_status', nodeId: nodeID, state } }))
  await page.route(`**/api/v1/nodes/${nodeID}/containers`, (route) => route.fulfill({ json: { type: 'node_containers', nodeId: nodeID, state, inventory } }))
  await page.route(`**/api/v1/nodes/${nodeID}/tasks?limit=50`, (route) => route.fulfill({ json: { tasks: listedTasks, nextCursor: '' } }))
  await page.route(`**/api/v1/nodes/${nodeID}/containers/${containerID}/rebuild/plan`, async (route) => {
    plannedSpecs.push(route.request().postDataJSON())
    await route.fulfill({ json: plan })
  })
  await page.route(`**/api/v1/nodes/${nodeID}/containers/${containerID}/actions`, async (route) => {
    submittedActions.push(route.request().postDataJSON())
    listedTasks.push(task)
    await route.fulfill({ json: { taskId: task.taskId, status: 'queued' } })
  })
  await page.route(`**/api/v1/nodes/${nodeID}/tasks/${task.taskId}`, (route) => route.fulfill({ json: task }))

  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  await openDockerDetails(page)
  const row = page.locator(`article[data-container-id="${containerID}"]`)
  await expect(row.getByRole('button', { name: '受控重建' })).toBeVisible()
  await row.getByRole('button', { name: '受控重建' }).click()
  const wizard = page.getByTestId('container-rebuild-wizard')
  await expect(wizard.getByTestId('rebuild-plan')).toBeVisible()
  await expect(wizard.getByText(/挂载卷和 bind mount 数据/)).toBeVisible()

  const box = await wizard.boundingBox()
  expect(box).not.toBeNull()
  expect(box!.width).toBeLessThanOrEqual(390)
  expect(await wizard.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)

  await wizard.getByLabel('端口设置').selectOption('replace')
  await wizard.getByRole('button', { name: '添加端口映射' }).click()
  await wizard.getByLabel('容器端口 1').fill('80/tcp')
  await wizard.getByLabel('主机 IP 1').fill('127.0.0.1')
  await wizard.getByLabel('主机端口 1').fill('18081')
  await wizard.getByRole('button', { name: '重新生成计划' }).click()
  await expect.poll(() => plannedSpecs.length).toBe(2)
  await expect(wizard.getByText('80/tcp <- 127.0.0.1:18081')).toBeVisible()
  await wizard.getByLabel(/我已检查停机影响/).check()
  page.on('dialog', async (dialog) => dialog.accept(containerID))
  await wizard.getByTestId('submit-container-rebuild').click()
  await expect.poll(() => submittedActions).toEqual([{
    action: 'rebuild',
    rebuild: { portBindings: [{ containerPort: '80/tcp', hostIp: '127.0.0.1', hostPort: '18081' }] },
  }])
  await expect(row.getByTestId('rebuild-task-status')).toContainText('重建已验证完成')

  container.id = replacementID
  container.ports = [{ containerPort: 80, protocol: 'tcp', exposed: true,
    configured: [{ ip: '127.0.0.1', port: '18081' }], published: [{ ip: '127.0.0.1', port: '18081' }] }]
  inventory.containers.push({ container: { ...container, id: containerID, name: 'nodedance-rb-test', running: false, state: 'exited' },
    generation: 1, sequence: 2, receivedAt: serverTime })
  await page.reload()
  await openDockerDetails(page)
  const replacementRow = page.locator(`article[data-container-id="${replacementID}"]`)
  const rollbackRow = page.locator(`article[data-container-id="${containerID}"]`)
  await expect(replacementRow.getByText('已验证完成：running')).toBeVisible()
  await expect(rollbackRow.locator('[data-role="rollback"]')).toHaveText('回滚副本')
})

test('failed restored rebuild offers explicit cleanup without touching the original container', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
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
  const failedTask = {
    taskId: 'ndt_s12_ui_failed', nodeId: nodeID, targetId: containerID, action: 'rebuild', status: 'failed',
    deliveryState: 'finished', reconciliationRequired: false, progress: { phase: 'rolled_back', completed: 6, total: 6 },
    result: { code: 'failed', observedState: 'restored:rebuild_operation_failed', resourceRevision: containerID },
    createdAt: serverTime, updatedAt: serverTime, startedAt: serverTime, finishedAt: serverTime,
  }
  const cleanupTask = {
    ...failedTask, taskId: 'ndt_s12_ui_cleanup', targetId: containerID, action: 'rebuild_cleanup', status: 'succeeded',
    result: { code: 'verified', observedState: 'rollback_resources_cleaned', resourceRevision: 'rollback_resources_removed' },
  }
  const state = { nodeId: nodeID, agentId: 's12-ui-agent', status: 'online', generation: 1, serverTime, leaseValidUntil }
  const container = {
    id: containerID, name: 'nd-web', image: 'nginx:locked', imageId: 'sha256:fixture', state: 'running', running: true,
    paused: false, restarting: false, health: 'healthy', healthcheckConfigured: false, stale: false, restartCount: 0,
    ports: [], observedAt: serverTime,
  }
  const inventory = {
    agentId: state.agentId, nodeId: nodeID, agentOnline: true, activeGeneration: 1, leaseValidUntil,
    dockerAvailability: 'available', dockerEventsConnected: true, dockerSnapshotFresh: true, dataStale: false,
    containers: [{ container, generation: 1, sequence: 1, receivedAt: serverTime }], serverTime,
  }
  const submittedActions: unknown[] = []
  await page.route('**/api/v1/auth/csrf', (route) => route.fulfill({ json: { token: 's12-test-csrf' } }))
  await page.route('**/api/v1/nodes', (route) => route.fulfill({ json: {
    serverTime, nodes: [{ ...state, displayName: 'S12 failed cleanup test node', lastSeen: serverTime,
      capabilities: ['agent.metrics.v1', 'agent.docker.v1'] }],
  } }))
  await page.route(`**/api/v1/nodes/${nodeID}/metrics`, (route) => route.fulfill({ json: { type: 'node_status', nodeId: nodeID, state } }))
  await page.route(`**/api/v1/nodes/${nodeID}/containers`, (route) => route.fulfill({ json: { type: 'node_containers', nodeId: nodeID, state, inventory } }))
  await page.route(`**/api/v1/nodes/${nodeID}/tasks?limit=50`, (route) => route.fulfill({ json: { tasks: [failedTask], nextCursor: '' } }))
  await page.route(`**/api/v1/nodes/${nodeID}/containers/${containerID}/actions`, async (route) => {
    submittedActions.push(route.request().postDataJSON())
    await route.fulfill({ json: { taskId: cleanupTask.taskId, status: 'queued' } })
  })
  await page.route(`**/api/v1/nodes/${nodeID}/tasks/${cleanupTask.taskId}`, (route) => route.fulfill({ json: cleanupTask }))
  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  await openDockerDetails(page)
  const row = page.locator(`article[data-container-id="${containerID}"]`)
  const cleanup = row.getByRole('button', { name: '清理重建遗留资源' })
  await expect(cleanup).toBeVisible()
  page.on('dialog', async (dialog) => {
    expect(dialog.message()).toContain('已恢复的原容器保持不变')
    await dialog.accept(containerID)
  })
  await cleanup.click()
  await expect.poll(() => submittedActions).toEqual([{
    action: 'rebuild_cleanup',
    rebuild: { cleanupTaskId: failedTask.taskId },
    confirmationId: containerID,
  }])
  await expect(row.getByText('重建遗留回滚资源已清理，数据卷保留。')).toBeVisible()
})
