import { expect, test } from '@playwright/test'

test('metrics panel uses Core time, then expires metrics and lease without another update', async ({ page }) => {
  await page.addInitScript(() => {
    // The dashboard must not compare freshness or lease deadlines with this
    // deliberately wrong device wall clock.
    Date.now = () => Date.UTC(2100, 0, 1)
  })
  await page.clock.install({ time: new Date('2100-01-01T00:00:00.000Z') })
  await page.goto('/tests/fixtures/s03-metrics-panel.html?fastAge=14000&diskAge=89000&leaseMs=5000')

  await expect(page.getByTestId('node-status')).toHaveText('在线')
  await expect(page.getByTestId('cpu-value')).toHaveText('12.5%')
  await expect(page.locator('[data-testid="cpu-card"] .status-pill')).toHaveAttribute('data-status', 'known')

  await page.clock.fastForward(2_000)
  await expect(page.locator('[data-testid="cpu-card"] .status-pill')).toHaveAttribute('data-status', 'stale')
  await expect(page.locator('[data-testid="disk-card"] .card-heading .status-pill')).toHaveAttribute('data-status', 'stale')
  await expect(page.getByTestId('cpu-value')).toHaveText('12.5%')
  await expect(page.getByTestId('cpu-reason')).toContainText('sample_expired')
  await expect(page.getByTestId('node-status')).toHaveText('在线')

  await page.clock.fastForward(4_000)
  await expect(page.getByTestId('node-status')).toHaveText('离线')
  await expect(page.getByTestId('cpu-value')).toHaveText('12.5%')
  await expect(page.getByTestId('cpu-reason')).toContainText('lease_expired')

  await page.evaluate(() => window.updateMetricsView())
  await expect(page.getByTestId('node-status')).toHaveText('在线')
  await expect(page.locator('[data-testid="cpu-card"] .status-pill')).toHaveAttribute('data-status', 'known')
})

test('metrics panel fits a narrow viewport', async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 })
  await page.goto('/tests/fixtures/s03-metrics-panel.html')
  await expect(page.getByRole('heading', { name: '实时指标' })).toBeVisible()
  const widths = await page.evaluate(() => ({
    viewport: document.documentElement.clientWidth,
    content: document.documentElement.scrollWidth,
  }))
  expect(widths.content).toBeLessThanOrEqual(widths.viewport)
  await expect(page.getByTestId('disk-card')).toBeVisible()
  await expect(page.getByTestId('network-card')).toBeVisible()
})

test('unknown and failed measurements stay explicit without showing a zero value', async ({ page }) => {
  await page.goto('/tests/fixtures/s03-metrics-panel.html?memoryState=unknown')
  await expect(page.locator('[data-testid="memory-card"] .status-pill')).toHaveAttribute('data-status', 'unknown')
  await expect(page.getByTestId('memory-value')).toHaveText('—')
  await expect(page.getByTestId('memory-reason')).toHaveText('permission_denied')

  await page.goto('/tests/fixtures/s03-metrics-panel.html?memoryState=error')
  await expect(page.locator('[data-testid="memory-card"] .status-pill')).toHaveAttribute('data-status', 'error')
  await expect(page.getByTestId('memory-value')).toHaveText('—')
  await expect(page.getByTestId('memory-reason')).toHaveText('permission_denied')
})
