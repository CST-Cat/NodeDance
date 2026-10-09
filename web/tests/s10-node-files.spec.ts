import { expect, test } from '@playwright/test'
import { createHash } from 'node:crypto'

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

async function installFileAPIMock(page: import('@playwright/test').Page, onRequest?: (request: import('@playwright/test').Request) => void, textWriteStatus = 409) {
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
      if (textWriteStatus === 409) await route.fulfill({ status: 409, json: { message: 'version conflict' } })
      else await route.fulfill({ json: { taskId: 'fixture-task', transferId: 'fixture-task', status: 'succeeded' } })
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

async function installDashboardMock(page: import('@playwright/test').Page) {
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
        lastSeen: '2030-01-01T00:00:00.000Z', leaseValidUntil: '2030-01-01T00:01:00.000Z', capabilities: ['agent.metrics.v1'],
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

test('every file write sends a stable idempotency key and upload hashes the Blob stream', async ({ page }) => {
  const writes: Array<{ method: string; path: string; key: string; digest: string; body: string }> = []
  await installFileAPIMock(page, (request) => {
    const url = new URL(request.url())
    if (!url.pathname.includes('/files/')) return
    if (!['PUT', 'POST', 'DELETE'].includes(request.method())) return
    writes.push({
      method: request.method(),
      path: url.pathname,
      key: request.headers()['idempotency-key'] ?? '',
      digest: request.headers()['x-file-sha256'] ?? '',
      body: request.postData() ?? '',
    })
  }, 200)
  await page.goto('/tests/fixtures/s10-node-files.html')

  await page.evaluate(async (currentNodeId) => {
    const { api } = await import(/* @vite-ignore */ new URL('/src/api.ts', window.location.href).href)
    await api.saveNodeText(currentNodeId, { path: '/saved.txt', version: 'v1', text: 'saved' })
    await api.createNodeDirectory(currentNodeId, '/made')
    await api.renameNodeFile(currentNodeId, '/old', '/new')
    await api.deleteNodeFile(currentNodeId, '/removed')
    const file = new File(['upload bytes'], 'upload.txt', { type: 'text/plain' })
    Object.defineProperty(file, 'arrayBuffer', { value: () => { throw new Error('whole-file buffering is forbidden') } })
    await api.uploadNodeFile(currentNodeId, '/upload.txt', file)
    await api.createNodeDirectory(currentNodeId, '/retry', 'stable-retry-key')
    await api.createNodeDirectory(currentNodeId, '/retry', 'stable-retry-key')
  }, nodeId)

  const expectedPaths = [
    '/api/v1/nodes/' + nodeId + '/files/text',
    '/api/v1/nodes/' + nodeId + '/files/directories',
    '/api/v1/nodes/' + nodeId + '/files/rename',
    '/api/v1/nodes/' + nodeId + '/files/delete',
    '/api/v1/nodes/' + nodeId + '/files/upload',
  ]
  expect(writes.map(write => write.path)).toEqual([...expectedPaths, expectedPaths[1], expectedPaths[1]])
  for (const write of writes.slice(0, 5)) expect(write.key).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
  expect(new Set(writes.slice(0, 5).map(write => write.key)).size).toBe(5)
  expect(writes.slice(5).map(write => write.key)).toEqual(['stable-retry-key', 'stable-retry-key'])
  const upload = writes.find(write => write.path.endsWith('/upload'))!
  expect(upload.digest).toBe(createHash('sha256').update('upload bytes').digest('hex'))
  expect(writes.find(write => write.path.endsWith('/text'))?.body).toContain('"text":"saved"')
})

test('incremental SHA-256 covers empty, padding boundaries, and chunked streams', async ({ page }) => {
  await installFileAPIMock(page)
  await page.goto('/tests/fixtures/s10-node-files.html')
  const lengths = [0, 55, 56, 63, 64, 65, 131_101]
  const expected = Object.fromEntries(lengths.map(length => {
    const bytes = Buffer.alloc(length)
    for (let index = 0; index < length; index++) bytes[index] = index % 251
    return [String(length), createHash('sha256').update(bytes).digest('hex')]
  }))
  const splitBytes = Buffer.alloc(137)
  for (let index = 0; index < splitBytes.length; index++) splitBytes[index] = index % 251
  const splitExpected = createHash('sha256').update(splitBytes).digest('hex')
  const output = await page.evaluate(async ({ digests }) => {
    const { sha256Blob, sha256Stream } = await import(/* @vite-ignore */ new URL('/src/sha256.ts', window.location.href).href)
    const actual: Record<string, string> = { abc: await sha256Blob(new Blob(['abc'])) }
    for (const lengthText of Object.keys(digests)) {
      const length = Number(lengthText)
      const bytes = new Uint8Array(length)
      for (let index = 0; index < length; index++) bytes[index] = index % 251
      actual[lengthText] = await sha256Blob(new Blob([bytes]))
    }
    const bytes = new Uint8Array(137)
    for (let index = 0; index < bytes.length; index++) bytes[index] = index % 251
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(bytes.slice(0, 55))
        controller.enqueue(bytes.slice(55, 120))
        controller.enqueue(bytes.slice(120))
        controller.close()
      },
    })
    actual.split = await sha256Stream(stream)
    return actual
  }, { digests: expected })
  expect(output.abc).toBe(createHash('sha256').update('abc').digest('hex'))
  expect(Object.fromEntries(lengths.map(length => [String(length), output[String(length)]]))).toEqual(expected)
  expect(output.split).toBe(splitExpected)
})

test('a selected node opens and leaves the file manager from its dashboard detail', async ({ page }) => {
  await installDashboardMock(page)
  await page.goto('/tests/fixtures/s03-nodes-dashboard.html')
  const openFiles = page.getByRole('button', { name: '管理节点文件' })
  await expect(openFiles).toBeEnabled()
  await openFiles.click()
  await expect(page.getByRole('region', { name: '节点文件管理' })).toBeVisible()
  await expect(page.getByText('report.txt')).toBeVisible()
  await expect(page.getByTestId('docker-inventory')).toHaveCount(0)

  await page.getByRole('button', { name: '返回节点详情' }).click()
  await expect(page.getByTestId('docker-inventory')).toBeVisible()
  await expect(page.getByRole('button', { name: '管理节点文件' })).toBeEnabled()
})
