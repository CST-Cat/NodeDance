import { expect, test, type Page } from '@playwright/test'

const nodeID = '12345678-1234-4234-8234-123456789abc'
const containerID = 'a'.repeat(64)
const serverTime = '2026-10-08T18:00:00.000Z'
const leaseValidUntil = '2026-10-08T18:01:00.000Z'
const streamID = 'terminal-stream-s09-responsive'

async function mockDashboard(page: Page) {
  await page.addInitScript((id: string) => {
    type Handler = ((event: Event | MessageEvent) => void) | null
    type SentFrame = { action?: string; data?: string; rows?: number; columns?: number }
    const sent: SentFrame[] = []
    class SocketStub {
      static readonly CONNECTING = 0
      static readonly OPEN = 1
      static readonly CLOSING = 2
      static readonly CLOSED = 3
      readyState = SocketStub.CONNECTING
      onopen: Handler = null
      onmessage: Handler = null
      onclose: Handler = null
      onerror: Handler = null
      private readonly terminal: boolean

      constructor(url: string | URL) {
        this.terminal = String(url).includes('/ws/v1/streams/terminal')
        queueMicrotask(() => {
          this.readyState = SocketStub.OPEN
          this.onopen?.(new Event('open'))
          if (this.terminal) {
            queueMicrotask(() => this.onmessage?.(new MessageEvent('message', {
              data: JSON.stringify({ type: 'terminal', frame: { streamId: id, action: 'ready' } }),
            })))
          }
        })
      }

      send(raw: string) {
        try { sent.push(JSON.parse(raw) as SentFrame) } catch { /* ignore non-terminal frames */ }
      }

      close() {
        this.readyState = SocketStub.CLOSED
        this.onclose?.(new Event('close'))
      }
    }
    Object.defineProperty(window, 'WebSocket', { value: SocketStub })
    Object.defineProperty(window, '__s09TerminalFrames', { value: sent })
  }, streamID)

  await page.route('**/api/v1/auth/csrf', (route) => route.fulfill({ json: { token: 's09-csrf-token' } }))
  await page.route('**/api/v1/nodes', (route) => route.fulfill({ json: {
    serverTime,
    nodes: [{ nodeId: nodeID, agentId: 'agent-s09-browser', displayName: 'S09 test node', status: 'online', generation: 2,
      lastSeen: serverTime, leaseValidUntil, capabilities: ['agent.metrics.v1', 'agent.docker.v1', 'agent.terminal.v1'] }],
  } }))
  await page.route('**/api/v1/dashboard/settings', (route) => route.fulfill({ json: {
    viewMode: 'monitor', groupBy: 'node', sortBy: 'custom', featuredLimit: 4, customFields: ['state', 'ports', 'health'],
  } }))
  await page.route(`**/api/v1/nodes/${nodeID}/preferences`, (route) => route.fulfill({ json: { preferences: [] } }))
  await page.route(`**/api/v1/nodes/${nodeID}/tasks?*`, (route) => route.fulfill({ json: { tasks: [], nextCursor: '' } }))
  await page.route(`**/api/v1/nodes/${nodeID}/metrics`, (route) => route.fulfill({ json: {
    type: 'node_status', nodeId: nodeID,
    state: { status: 'online', generation: 2, serverTime, leaseValidUntil },
  } }))
  await page.route(`**/api/v1/nodes/${nodeID}/containers`, (route) => route.fulfill({ json: {
    type: 'node_containers', nodeId: nodeID,
    state: { status: 'online', generation: 2, serverTime, leaseValidUntil },
    inventory: {
      nodeId: nodeID, agentId: 'agent-s09-browser', agentOnline: true, activeGeneration: 2, leaseValidUntil,
      dockerAvailability: 'available', dockerEventsConnected: true, dockerSnapshotFresh: true, dataStale: false,
      containers: [{ container: {
        id: containerID, name: 'nd-terminal', image: 'busybox', imageId: 'sha256:fixture', state: 'running',
        running: true, paused: false, restarting: false, health: 'none', healthcheckConfigured: false,
        stale: false, restartCount: 0, observedAt: serverTime, ports: [],
      }, generation: 2, sequence: 1, receivedAt: serverTime }],
      serverTime,
    },
  } }))
}

test('terminal streams keyboard and mobile auxiliary keys without horizontal overflow', async ({ page }) => {
  await mockDashboard(page)
  let requestBody: unknown
  let requestHasCSRF = false
  await page.route(`**/api/v1/nodes/${nodeID}/terminals`, async (route) => {
    requestBody = route.request().postDataJSON()
    requestHasCSRF = route.request().headers()['x-csrf-token'] === 's09-csrf-token'
    await route.fulfill({ json: { streamId: streamID, ticket: 's09-one-use-terminal-ticket', expiresAt: leaseValidUntil } })
  })

  await page.setViewportSize({ width: 375, height: 812 })
  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  await page.getByRole('button', { name: 'Docker 详情' }).click()
  await page.getByRole('button', { name: '打开终端' }).click()
  await expect(page.getByRole('dialog')).toBeVisible()
  await expect(page.getByRole('dialog').getByText('已连接', { exact: true })).toBeVisible()
  expect(requestBody).toEqual({ targetKind: 'host' })
  expect(requestHasCSRF).toBe(true)

  const terminalScreen = page.getByTestId('terminal-screen')
  await expect(terminalScreen).toBeVisible()
  const terminalTextarea = terminalScreen.locator('textarea').first()
  await terminalTextarea.focus()
  await page.keyboard.type('echo NodeDance 你好')
  await expect.poll(() => page.evaluate(() => (window as Window & { __s09TerminalFrames: Array<{ action?: string }> }).__s09TerminalFrames.some((frame) => frame.action === 'input'))).toBe(true)
  await page.getByRole('button', { name: 'Tab', exact: true }).click()

  const sentInputs = await page.evaluate(() => (window as Window & { __s09TerminalFrames: Array<{ action?: string; data?: string }> }).__s09TerminalFrames.filter((frame) => frame.action === 'input').map((frame) => {
    const binary = atob(frame.data ?? '')
    return new TextDecoder().decode(Uint8Array.from(binary, (character) => character.charCodeAt(0)))
  }))
  expect(sentInputs.join('')).toContain('echo NodeDance 你好')
  expect(sentInputs.join('')).toContain('\t')
  const dimensions = await page.evaluate(() => ({ viewport: document.documentElement.clientWidth, content: document.documentElement.scrollWidth }))
  expect(dimensions.content).toBeLessThanOrEqual(dimensions.viewport)
  await expect(page.getByRole('navigation', { name: '终端辅助按键' }).getByRole('button')).toHaveCount(8)
})

test('container console authorization names the exact running container', async ({ page }) => {
  await mockDashboard(page)
  let requestBody: unknown
  await page.route(`**/api/v1/nodes/${nodeID}/terminals`, async (route) => {
    requestBody = route.request().postDataJSON()
    await route.fulfill({ json: { streamId: streamID, ticket: 's09-one-use-terminal-ticket', expiresAt: leaseValidUntil } })
  })
  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  await page.getByRole('button', { name: 'Docker 详情' }).click()
  await page.getByRole('button', { name: '控制台' }).click()
  await expect(page.getByRole('dialog')).toBeVisible()
  expect(requestBody).toEqual({ targetKind: 'container', containerId: containerID })
  await expect(page.getByRole('heading', { name: 'S09 test node · nd-terminal' })).toBeVisible()
})
