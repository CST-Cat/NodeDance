import { expect, test, type Page, type Route } from '@playwright/test'

const nodeID = '12345678-1234-4234-8234-123456789abc'
const releaseID = '87654321-4321-4321-8321-cba987654321'

async function json(route: Route, value: unknown, status = 200) {
  await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(value) })
}

async function setupUpdatePage(page: Page) {
  const writes: Array<{ path: string; method: string; body: unknown }> = []
  const settings = { autoEnabled: false, windowStartMinute: 60, windowEndMinute: 120, batchSize: 1, releaseId: releaseID, campaignPaused: false }
  const release = { id: releaseID, version: '1.2.3', os: 'linux', architecture: 'amd64', createdAt: new Date().toISOString(), manifest: { formatVersion: 1, version: '1.2.3', os: 'linux', architecture: 'amd64', sha256: 'a'.repeat(64), size: 8, minProtocol: 1, maxProtocol: 1, signature: 'signed' } }
  let tasks: Array<Record<string, unknown>> = []
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/setup/status') return json(route, { initialized: true })
    if (path === '/api/v1/public/appearance') return json(route, { displayName: 'Admin', theme: 'dark', backgroundColor: '#101827', avatarUrl: '', backgroundUrl: '' })
    if (path === '/api/v1/auth/me') return json(route, { user: { displayName: 'Admin' } })
    if (path === '/api/v1/auth/sessions') return json(route, { sessions: [] })
    if (path === '/api/v1/auth/csrf') return json(route, { token: 'test-csrf' })
    if (path === '/api/v1/updates' || path === '/api/v1/updates/tasks') return json(route, { releases: [release], tasks, settings, nodes: [{ nodeId: nodeID, displayName: 'N1', status: 'online', agentVersion: '1.2.2', capabilities: ['agent.updates.v1'] }] })
    if (path === '/api/v1/updates/settings' && request.method() === 'PUT') {
      const body = request.postDataJSON() as typeof settings
      writes.push({ path, method: request.method(), body })
      Object.assign(settings, body)
      return json(route, { settings })
    }
    if (path === `/api/v1/updates/nodes/${nodeID}/update`) {
      const body = request.postDataJSON() as { releaseId: string }
      writes.push({ path, method: request.method(), body })
      const task = { id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', nodeId: nodeID, releaseId: body.releaseId, version: release.version, mode: 'manual', status: 'queued', createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() }
      tasks = [task]
      return json(route, task, 202)
    }
    if (path === '/api/v1/updates/releases' && request.method() === 'POST') {
      writes.push({ path, method: request.method(), body: 'multipart' })
      return json(route, { id: releaseID, version: release.version, architecture: release.architecture }, 201)
    }
    return json(route, { message: `Unexpected ${request.method()} ${path}` }, 404)
  })
  return writes
}

test('Agent update panel supports signed upload, manual rollout, schedules and responsive layout', async ({ page }) => {
  const writes = await setupUpdatePage(page)
  await page.goto('/')
  await page.getByRole('button', { name: 'Agent 更新' }).click()
  const panel = page.locator('.updates-page')
  await expect(panel).toContainText('1.2.3')
  await page.locator('input[type=file]').nth(0).setInputFiles({ name: 'manifest.json', mimeType: 'application/json', buffer: Buffer.from('{}') })
  await page.locator('input[type=file]').nth(1).setInputFiles({ name: 'nodedance-agent', mimeType: 'application/octet-stream', buffer: Buffer.from('binary') })
  await page.getByRole('button', { name: '验证并上传发布包' }).click()
  await expect(panel).toContainText('已验证并保存 Agent')
  await page.getByRole('button', { name: '手动更新' }).click()
  await expect(panel).toContainText('已排入队列')
  await page.getByLabel('启用自动更新').check()
  await page.getByLabel('窗口开始（UTC）').fill('02:30')
  await page.getByLabel('窗口结束（UTC）').fill('03:30')
  await page.getByLabel('每批节点数').fill('3')
  await page.getByRole('button', { name: '保存更新策略' }).click()
  await expect(panel).toContainText('自动更新窗口和批次设置已保存')
  expect(writes.map(({ path, method }) => `${method} ${path}`)).toEqual([
    'POST /api/v1/updates/releases',
    `POST /api/v1/updates/nodes/${nodeID}/update`,
    'PUT /api/v1/updates/settings',
  ])
  expect(writes[2]?.body).toMatchObject({ autoEnabled: true, windowStartMinute: 150, windowEndMinute: 210, batchSize: 3, releaseId: releaseID })
  for (const width of [375, 768, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    const sizes = await page.evaluate(() => ({ viewport: window.innerWidth, document: document.documentElement.scrollWidth }))
    expect(sizes.document, `horizontal overflow at ${width}px`).toBeLessThanOrEqual(sizes.viewport + 1)
  }
})
