import { expect, test, type Page, type Route } from '@playwright/test'

const nodeID = '12345678-1234-4234-8234-123456789abc'
const alertID = '87654321-4321-4321-8321-cba987654321'

async function json(route: Route, value: unknown, status = 200) {
  await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(value) })
}

async function stubCore(page: Page) {
  let acknowledged = false
  let channels: Array<Record<string, unknown>> = []
  const writes: Array<{ path: string; method: string; body: unknown }> = []
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/setup/status') return json(route, { initialized: true })
    if (path === '/api/v1/public/appearance') return json(route, { displayName: 'Test Admin', theme: 'dark', backgroundColor: '#101827', avatarUrl: '', backgroundUrl: '' })
    if (path === '/api/v1/auth/me') return json(route, { user: { displayName: 'Test Admin' } })
    if (path === '/api/v1/auth/sessions') return json(route, { sessions: [] })
    if (path === '/api/v1/auth/csrf') return json(route, { token: 'test-csrf' })
    if (path === '/api/v1/nodes') return json(route, { nodes: [{ nodeId: nodeID, displayName: 'N1', status: 'online', capabilities: [] }], serverTime: new Date().toISOString() })
    if (path === '/api/v1/alerts' || path === '/api/v1/alerts/history') return json(route, { alerts: path.endsWith('history') ? [] : [{ id: alertID, ruleId: '11111111-1111-4111-8111-111111111111', ruleName: 'CPU over 90%', nodeId: nodeID, nodeName: 'N1', severity: 'warning', status: 'active', message: 'CPU is 95%', firstSeenAt: new Date().toISOString(), lastSeenAt: new Date().toISOString(), ...(acknowledged ? { acknowledgedAt: new Date().toISOString() } : {}) }] })
    if (path === '/api/v1/alerts/events') return json(route, { events: [] })
    if (path === '/api/v1/alerts/rules') return json(route, { rules: [] })
    if (path === '/api/v1/alerts/channels' && request.method() === 'GET') return json(route, { channels })
    if (path === '/api/v1/alerts/windows') return json(route, { windows: [] })
    if (path === '/api/v1/alerts/deliveries') return json(route, { deliveries: [] })
    if (path === `/api/v1/alerts/${alertID}/acknowledge`) {
      writes.push({ path, method: request.method(), body: request.postDataJSON() })
      acknowledged = true
      return route.fulfill({ status: 204 })
    }
    if (path === '/api/v1/alerts/channels' && request.method() === 'POST') {
      const body = request.postDataJSON() as Record<string, unknown>
      writes.push({ path, method: request.method(), body })
      const created = { id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', name: body.name, kind: body.kind, enabled: true, hasSecret: false, revision: 1, config: body.config }
      channels = [created]
      return json(route, { channel: created })
    }
    return json(route, { message: `Unexpected ${request.method()} ${path}` }, 404)
  })
  return { writes }
}

test('alerts show active and acknowledged state and stay responsive', async ({ page }) => {
  const { writes } = await stubCore(page)
  await page.goto('/')
  await page.getByRole('button', { name: '告警中心' }).click()
  const center = page.locator('.alerts-page')
  await expect(center).toContainText('CPU over 90%')
  await page.getByRole('button', { name: '确认' }).click()
  await expect(center).toContainText('已确认')
  expect(writes[0]).toMatchObject({ method: 'POST', path: `/api/v1/alerts/${alertID}/acknowledge` })
  for (const width of [375, 768, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    await expect(center).toBeVisible()
    const sizes = await page.evaluate(() => ({ viewport: window.innerWidth, document: document.documentElement.scrollWidth }))
    expect(sizes.document, `horizontal overflow at ${width}px`).toBeLessThanOrEqual(sizes.viewport + 1)
  }
})

test('webhook channel form submits only configured secret fields', async ({ page }) => {
  const { writes } = await stubCore(page)
  await page.goto('/')
  await page.getByRole('button', { name: '告警中心' }).click()
  await page.getByRole('button', { name: '通知渠道' }).click()
  await page.getByLabel('名称').fill('Ops hook')
  await page.getByLabel('Webhook URL').fill('https://hooks.example.test')
  await page.getByRole('button', { name: '保存渠道' }).click()
  await expect(page.locator('.channel-list')).toContainText('Ops hook')
  expect(writes[0]?.body).toMatchObject({ name: 'Ops hook', kind: 'webhook', config: { webhookUrl: 'https://hooks.example.test' } })
})
