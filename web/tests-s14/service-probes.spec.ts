import { expect, test, type Page, type Route } from '@playwright/test'

const nodeID = '12345678-1234-4234-8234-123456789abc'
const probeID = '87654321-4321-4321-8321-cba987654321'
const sampleProbe = {
  id: probeID, nodeId: nodeID, name: 'Local health', kind: 'http', target: 'http://127.0.0.1:18080/health',
  expectedHttpStatus: 200, intervalSeconds: 30, timeoutSeconds: 3, enabled: true, revision: 1, status: 'unknown',
  consecutiveFailures: 0, consecutiveSuccesses: 0, nextDueAt: '2026-10-08T12:00:00Z', createdAt: '2026-10-08T12:00:00Z', updatedAt: '2026-10-08T12:00:00Z',
}

async function stubApplicationAPI(page: Page) {
  let probes: Record<string, unknown>[] = []
  const writes: Record<string, unknown>[] = []
  await page.route('**/api/v1/**', async (route: Route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/setup/status') return json(route, { initialized: true })
    if (path === '/api/v1/public/appearance') return json(route, { displayName: 'Test Admin', theme: 'dark', backgroundColor: '#101827', avatarUrl: '', backgroundUrl: '' })
    if (path === '/api/v1/auth/me') return json(route, { user: { displayName: 'Test Admin' } })
    if (path === '/api/v1/auth/sessions') return json(route, { sessions: [] })
    if (path === '/api/v1/auth/csrf') return json(route, { token: 'test-csrf-token' })
    if (path === '/api/v1/nodes') return json(route, { nodes: [{ nodeId: nodeID, agentId: 'agent-id', displayName: 'Probe node', status: 'online', generation: 1, capabilities: ['agent.probes.v1'] }], serverTime: '2026-10-08T12:00:00Z' })
    if (path === `/api/v1/nodes/${nodeID}/metrics`) return json(route, { type: 'node_status', nodeId: nodeID, state: { nodeId: nodeID, status: 'online', generation: 1, serverTime: '2026-10-08T12:00:00Z' } })
    if (path === `/api/v1/nodes/${nodeID}/containers`) return json(route, { type: 'node_containers', nodeId: nodeID, state: { nodeId: nodeID, status: 'online', generation: 1, serverTime: '2026-10-08T12:00:00Z' }, inventory: { nodeId: nodeID, agentId: 'agent-id', agentOnline: true, activeGeneration: 1, dockerAvailability: 'available', dockerEventsConnected: true, dockerSnapshotFresh: true, dataStale: false, containers: [], serverTime: '2026-10-08T12:00:00Z' } })
    if (path === `/api/v1/nodes/${nodeID}/tasks`) return json(route, { tasks: [], nextCursor: '' })
    if (path === '/api/v1/probes' && request.method() === 'GET') return json(route, { probes })
    if (path === '/api/v1/probes' && request.method() === 'POST') {
      const input = request.postDataJSON() as Record<string, unknown>
      writes.push(input)
      const created = { ...sampleProbe, ...input, id: probeID }
      probes = [created]
      return json(route, created, 201)
    }
    return json(route, { message: `Unexpected request: ${request.method()} ${path}` }, 404)
  })
  return { writes }
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
}

test('opens probes from the selected node and remains usable at responsive widths', async ({ page }) => {
  const { writes } = await stubApplicationAPI(page)
  await page.goto('/')
  await page.getByRole('button', { name: '节点监控' }).click()
  const panel = page.getByTestId('service-probes')
  await expect(panel).toBeVisible()
  await expect(panel).toContainText('Probe node')
  await page.getByRole('button', { name: '添加探测' }).click()
  await page.getByLabel('名称').fill('Homepage')
  await page.getByLabel('目标地址').fill('http://127.0.0.1:18080/health')
  await page.getByRole('button', { name: '保存配置' }).click()
  await expect(panel.locator('.probe-card')).toContainText('Homepage')
  expect(writes).toHaveLength(1)
  expect(writes[0]).toMatchObject({ nodeId: nodeID, name: 'Homepage', kind: 'http', expectedHttpStatus: 200, intervalSeconds: 30, timeoutSeconds: 3 })

  for (const width of [375, 768, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    await expect(panel).toBeVisible()
    const widths = await page.evaluate(() => ({ viewport: window.innerWidth, document: document.documentElement.scrollWidth }))
    expect(widths.document, `horizontal overflow at ${width}px`).toBeLessThanOrEqual(widths.viewport + 1)
  }
})
