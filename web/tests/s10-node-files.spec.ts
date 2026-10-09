import { expect, test } from '@playwright/test'

const nodeId = '01234567-89ab-4cde-8fab-0123456789ab'
const textFile = {
  name: '报告 [final].txt',
  path: '/报告 [final].txt',
  kind: 'file',
  size: 12,
  mode: 420,
  ownerUid: 1000,
  ownerGid: 1000,
  modifiedAt: 1_800_000_000_000,
  version: 'v1',
}

async function installFileAPIMock(page: import('@playwright/test').Page, onRequest?: (request: import('@playwright/test').Request) => void) {
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    onRequest?.(request)
    const url = new URL(request.url())
    if (url.pathname === '/api/v1/auth/csrf') {
      await route.fulfill({ json: { token: 'fixture-csrf' } })
      return
    }
    const base = `/api/v1/nodes/${nodeId}/files`
    if (url.pathname === base && request.method() === 'GET') {
      await route.fulfill({ json: { path: url.searchParams.get('path') ?? '/', entries: [
        { ...textFile },
        { name: '资料 目录', path: '/资料 目录', kind: 'directory', size: 0, mode: 493, ownerUid: 1000, ownerGid: 1000, modifiedAt: 1_800_000_000_000 },
      ] } })
      return
    }
    if (url.pathname === `${base}/text` && request.method() === 'GET') {
      await route.fulfill({ json: { path: textFile.path, text: 'before\n', version: 'v1', size: 7 } })
      return
    }
    if (url.pathname === `${base}/text` && request.method() === 'PUT') {
      await route.fulfill({ status: 409, json: { message: 'version conflict' } })
      return
    }
    if (url.pathname === `${base}/stat` && request.method() === 'GET') {
      await route.fulfill({ status: 404, json: { message: 'not found' } })
      return
    }
    if (url.pathname === `${base}/upload` && request.method() === 'POST') {
      await route.fulfill({ json: { transferId: 'fixture-transfer', status: 'succeeded', sha256: 'a'.repeat(64) } })
      return
    }
    if (url.pathname === `${base}/delete` && request.method() === 'DELETE') {
      await route.fulfill({ json: { transferId: 'fixture-transfer', status: 'succeeded' } })
      return
    }
    if (url.pathname === `${base}/rename` && request.method() === 'POST') {
      await route.fulfill({ json: { transferId: 'fixture-transfer', status: 'succeeded' } })
      return
    }
    if (url.pathname === `${base}/directories` && request.method() === 'POST') {
      await route.fulfill({ json: { transferId: 'fixture-transfer', status: 'succeeded' } })
      return
    }
    await route.fulfill({ status: 404, json: { message: `unmocked ${request.method()} ${url.pathname}` } })
  })
}

async function installDashboardMock(page: import('@playwright/test').Page, filesCapability = true) {
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
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    if (url.pathname === '/api/v1/nodes') {
      await route.fulfill({ json: { serverTime: '2030-01-01T00:00:00.000Z', nodes: [{
        nodeId, agentId: 'agent-s10-ui', displayName: 'S10 UI node', status: 'online', generation: 1,
        lastSeen: '2030-01-01T00:00:00.000Z', leaseValidUntil: '2030-01-01T00:01:00.000Z',
        capabilities: ['agent.metrics.v1', ...(filesCapability ? ['agent.files.v1'] : [])],
      }] } })
      return
    }
    if (url.pathname === `/api/v1/nodes/${nodeId}/metrics`) {
      await route.fulfill({ json: { type: 'node_status', nodeId, state: {
        nodeId, agentId: 'agent-s10-ui', status: 'online', generation: 1,
        serverTime: '2030-01-01T00:00:00.000Z', leaseValidUntil: '2030-01-01T00:01:00.000Z',
      } } })
      return
    }
    if (url.pathname === `/api/v1/nodes/${nodeId}/containers`) {
      await route.fulfill({ json: { type: 'node_containers', nodeId, state: {
        nodeId, agentId: 'agent-s10-ui', status: 'online', generation: 1,
        serverTime: '2030-01-01T00:00:00.000Z', leaseValidUntil: '2030-01-01T00:01:00.000Z',
      }, inventory: {
        agentId: 'agent-s10-ui', nodeId, agentOnline: true, activeGeneration: 1,
        leaseValidUntil: '2030-01-01T00:01:00.000Z', dockerAvailability: 'available', dockerEventsConnected: true,
        dockerSnapshotFresh: true, dataStale: false, containers: [], serverTime: '2030-01-01T00:00:00.000Z',
      } } })
      return
    }
    if (url.pathname === `/api/v1/nodes/${nodeId}/tasks`) {
      await route.fulfill({ json: { tasks: [], nextCursor: '' } })
      return
    }
    if (url.pathname === `/api/v1/nodes/${nodeId}/files`) {
      await route.fulfill({ json: { path: '/', entries: [{ ...textFile, path: '/report.txt', name: 'report.txt' }] } })
      return
    }
    await route.fulfill({ status: 404, json: { message: `unmocked ${request.method()} ${url.pathname}` } })
  })
}

test('NodeFiles stays usable without horizontal page overflow at common responsive widths', async ({ page }) => {
  await installFileAPIMock(page)
  await page.goto('/tests/fixtures/s10-node-files.html')
  await expect(page.getByRole('heading', { name: '节点文件' })).toBeVisible()
  await expect(page.getByText('报告 [final].txt')).toBeVisible()
  for (const width of [375, 768, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    const dimensions = await page.evaluate(() => ({ viewport: document.documentElement.clientWidth, page: document.documentElement.scrollWidth }))
    expect(dimensions.page).toBeLessThanOrEqual(dimensions.viewport)
    await expect(page.getByRole('button', { name: '上传文件' })).toBeVisible()
  }
})

test('external edit conflict is shown and does not claim the stale save succeeded', async ({ page }) => {
  await installFileAPIMock(page)
  await page.goto('/tests/fixtures/s10-node-files.html')
  await page.getByRole('button', { name: '编辑' }).click()
  await expect(page.getByRole('dialog')).toBeVisible()
  await page.getByRole('textbox', { name: '文件文本内容' }).fill('web version')
  await page.getByRole('button', { name: '保存并备份' }).click()
  await expect(page.getByRole('alert')).toContainText('文件已被其他进程修改')
  await expect(page.getByRole('dialog')).toBeVisible()
})

test('upload streams a file body and delete sends the exact path confirmation', async ({ page }) => {
  let uploadContentType = ''
  let deletePayload: unknown
  await installFileAPIMock(page, (request) => {
    const url = new URL(request.url())
    if (url.pathname.endsWith('/files/upload') && request.method() === 'POST') {
      uploadContentType = request.headers()['content-type'] ?? ''
    }
    if (url.pathname.endsWith('/files/delete') && request.method() === 'DELETE') {
      deletePayload = request.postDataJSON()
    }
  })
  await page.addInitScript(() => {
    const originalFetch = window.fetch.bind(window)
    Object.defineProperty(window, '__s10UploadBody', { value: null, writable: true })
    window.fetch = (input, init) => {
      const requestURL = input instanceof Request ? input.url : String(input)
      if (new URL(requestURL, window.location.href).pathname.endsWith('/files/upload')) {
        Object.defineProperty(window, '__s10UploadBody', { value: init?.body ?? null, writable: true })
      }
      return originalFetch(input, init)
    }
  })
  await page.addInitScript((path) => { window.prompt = () => path }, textFile.path)
  await page.goto('/tests/fixtures/s10-node-files.html')
  await page.locator('input[type=file]').setInputFiles({ name: 'new 文件.txt', mimeType: 'text/plain', buffer: Buffer.from('upload bytes') })
  await expect(page.getByRole('status').filter({ hasText: '文件上传并校验完成' })).toBeVisible()
  expect(uploadContentType).toContain('application/octet-stream')
  const uploadedFile = await page.evaluate(async () => {
    const body = (window as Window & { __s10UploadBody?: BodyInit | null }).__s10UploadBody
    if (!(body instanceof File)) return null
    return { name: body.name, size: body.size, text: await body.text() }
  })
  expect(uploadedFile).toEqual({ name: 'new 文件.txt', size: 12, text: 'upload bytes' })
  await page.getByRole('button', { name: '删除' }).first().click()
  expect(deletePayload).toEqual({ path: textFile.path, confirmPath: textFile.path })
})

test('a selected node opens and leaves the file manager from its dashboard detail', async ({ page }) => {
  await installDashboardMock(page)
  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  const openFiles = page.getByRole('button', { name: '管理节点文件' })
  await expect(openFiles).toBeEnabled()
  await openFiles.click()
  await expect(page.locator('.node-files')).toBeVisible()
  await expect(page.getByText('report.txt')).toBeVisible()
  await expect(page.getByTestId('docker-inventory')).toHaveCount(0)

  await page.getByRole('button', { name: '返回节点详情' }).click()
  await expect(page.getByTestId('docker-inventory')).toBeVisible()
  await expect(page.getByRole('button', { name: '管理节点文件' })).toBeEnabled()
})

test('an online Agent without file capability shows why host file management is disabled', async ({ page }) => {
  await installDashboardMock(page, false)
  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  await expect(page.getByRole('button', { name: '管理节点文件' })).toBeDisabled()
  await expect(page.getByTestId('file-capability-missing')).toContainText('文件管理已禁用')
  await expect(page.getByRole('region', { name: '节点文件管理' })).toHaveCount(0)
})
