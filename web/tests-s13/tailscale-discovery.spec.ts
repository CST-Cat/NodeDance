import { expect, test, type Page, type Route } from '@playwright/test'

const linuxPeer = {
  identity: 'nodekey:stable-linux-id',
  name: 'edge-linux',
  dnsName: 'edge-linux.example.ts.net',
  os: 'linux',
  class: 'linux',
  online: true,
  ips: ['100.64.0.10'],
  managed: false,
}

const macPeer = {
  identity: 'nodekey:mac-id',
  name: 'admin-mac',
  os: 'macOS',
  class: 'unsupported',
  online: true,
  ips: ['100.64.0.11'],
  managed: false,
}

type MockOptions = { outcome?: 'succeeded' | 'failed'; message?: string }

async function stubApplicationAPI(page: Page, options: MockOptions = {}) {
  const deployRequests: Record<string, unknown>[] = []
  let deploymentTaskID = ''
  await page.route('**/api/v1/**', async (route: Route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/setup/status') {
      return json(route, { initialized: true })
    }
    if (path === '/api/v1/public/appearance') {
      return json(route, { displayName: 'Test Admin', theme: 'dark', backgroundColor: '#101827', avatarUrl: '', backgroundUrl: '' })
    }
    if (path === '/api/v1/auth/me') return json(route, { user: { displayName: 'Test Admin' } })
    if (path === '/api/v1/auth/sessions') return json(route, { sessions: [] })
    if (path === '/api/v1/auth/csrf') return json(route, { token: 'test-csrf-token' })
    if (path === '/api/v1/discovery/tailscale') {
      return json(route, { peers: [linuxPeer, macPeer], discoveredAt: '2026-10-08T12:00:00Z' })
    }
    if (path === '/api/v1/discovery/tailscale/host-key' && request.method() === 'POST') {
      return json(route, { fingerprint: 'SHA256:trustedIndependentFingerprint0123456789' })
    }
    if (path === '/api/v1/discovery/deployments' && request.method() === 'POST') {
      deployRequests.push(request.postDataJSON() as Record<string, unknown>)
      deploymentTaskID = 'deployment-task-1'
      return json(route, {
        taskId: deploymentTaskID,
        peerIdentity: linuxPeer.identity,
        peerName: linuxPeer.name,
        status: 'queued',
        phase: 'queued',
        createdAt: '2026-10-08T12:00:00Z',
        updatedAt: '2026-10-08T12:00:00Z',
      }, 202)
    }
    if (path === `/api/v1/discovery/deployments/${deploymentTaskID}`) {
      const status = options.outcome ?? 'succeeded'
      return json(route, {
        taskId: deploymentTaskID,
        peerIdentity: linuxPeer.identity,
        peerName: linuxPeer.name,
        status,
        phase: status === 'succeeded' ? 'complete' : 'failed',
        message: options.message ?? (status === 'succeeded' ? 'Agent online; Docker Engine is confirmed.' : 'SSH host key was rejected.'),
        nodeId: status === 'succeeded' ? '12345678-1234-4234-8234-123456789abc' : undefined,
        createdAt: '2026-10-08T12:00:00Z',
        updatedAt: '2026-10-08T12:00:02Z',
      })
    }
    return json(route, { message: `Unexpected mocked API request: ${request.method()} ${path}` }, 404)
  })
  return { deployRequests }
}

async function openDiscovery(page: Page) {
  await page.goto('/')
  await expect(page.getByRole('button', { name: '发现节点' })).toBeVisible()
  await page.getByRole('button', { name: '发现节点' }).click()
  await expect(page.getByTestId('tailscale-discovery')).toBeVisible()
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
}

test('classifies discovered peers without auto-install and remains usable at mobile widths', async ({ page }) => {
  const { deployRequests } = await stubApplicationAPI(page)
  await openDiscovery(page)

  await expect(page.getByRole('heading', { name: '可部署的 Linux 节点' })).toBeVisible()
  await expect(page.locator('[data-identity="nodekey:stable-linux-id"]')).toContainText('edge-linux')
  await expect(page.getByRole('heading', { name: '暂不支持的节点' })).toBeVisible()
  await expect(page.locator('.unsupported-list')).toContainText('admin-mac')
  expect(deployRequests).toHaveLength(0)

  for (const width of [375, 768, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    await expect(page.getByTestId('tailscale-discovery')).toBeVisible()
    const widths = await page.evaluate(() => ({ viewport: window.innerWidth, document: document.documentElement.scrollWidth }))
    expect(widths.document, `horizontal overflow at ${width}px`).toBeLessThanOrEqual(widths.viewport + 1)
  }
})

test('requires confirmed SSH fingerprint and explicit fallback, clears credentials, reports success', async ({ page }) => {
  const { deployRequests } = await stubApplicationAPI(page)
  await openDiscovery(page)
  await page.getByRole('button', { name: '查看部署选项' }).click()
  await expect(page.locator('.ssh-rescue')).toContainText('100.64.0.10')
  await expect(page.locator('.ssh-rescue pre')).toContainText('ssh --')

  await page.locator('.deploy-form .field input').nth(1).fill('https://core.example.ts.net')
  await page.locator('.deploy-form .field input').nth(2).fill('https://backup.example.net')
  await page.getByLabel('SSH 用户').fill('deploy')
  const password = page.locator('input[type="password"][autocomplete="new-password"]')
  await password.fill('one-time-ssh-password')
  const start = page.getByRole('button', { name: '启动 SSH 部署任务' })
  await expect(start).toBeDisabled()

  await page.getByRole('button', { name: '读取 SSH host key 指纹' }).click()
  await expect(page.getByTestId('ssh-fingerprint')).toContainText('SHA256:trustedIndependentFingerprint0123456789')
  await page.getByLabel('我已通过独立可信渠道核对该指纹').check()
  await expect(start).toBeEnabled()
  await start.click()

  await expect(page.getByTestId('deployment-task')).toHaveAttribute('data-status', 'succeeded')
  await expect(page.locator('.alert-success')).toContainText('Agent online; Docker Engine is confirmed.')
  await expect(password).toHaveValue('')
  expect(deployRequests).toHaveLength(1)
  expect(deployRequests[0]).toMatchObject({
    peerIdentity: linuxPeer.identity,
    coreUrl: 'https://core.example.ts.net',
    fallbackUrl: 'https://backup.example.net',
    allowFallback: false,
    confirmHostKey: true,
  })
  expect(JSON.stringify(deployRequests[0])).toContain('one-time-ssh-password')
})

test('shows failed deployment status with the remote failure reason', async ({ page }) => {
  await stubApplicationAPI(page, { outcome: 'failed', message: 'SSH host key did not match the confirmed fingerprint.' })
  await openDiscovery(page)
  await page.getByRole('button', { name: '查看部署选项' }).click()
  await page.getByLabel('SSH 用户').fill('deploy')
  await page.getByRole('button', { name: '读取 SSH host key 指纹' }).click()
  await page.getByLabel('我已通过独立可信渠道核对该指纹').check()
  await page.getByRole('button', { name: '启动 SSH 部署任务' }).click()
  await expect(page.getByTestId('deployment-task')).toHaveAttribute('data-status', 'failed')
  await expect(page.getByRole('alert')).toContainText('SSH host key did not match the confirmed fingerprint.')
})
