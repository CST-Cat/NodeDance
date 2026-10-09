import fs from 'node:fs'
import { chromium } from '@playwright/test'

const config = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'))
const browser = await chromium.launch({ headless: true })
try {
  const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 390, height: 844 } })
  await context.addCookies([{ name: 'nodedance_session', value: config.session, url: config.url,
    secure: true, httpOnly: true, sameSite: 'Strict' }])
  const page = await context.newPage()
  await page.goto(config.url, { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: '节点监控' }).waitFor({ state: 'visible', timeout: 15_000 })
  await page.getByRole('button', { name: '节点监控' }).click()
  const row = page.locator(`.docker-row[data-container-id="${config.containerId}"]`)
  await row.waitFor({ state: 'visible', timeout: 30_000 })
  await row.getByRole('button', { name: '实时统计' }).click()
  await page.waitForFunction((containerId) => {
    const row = document.querySelector(`.docker-row[data-container-id="${containerId}"]`)
    return row?.querySelector('[data-testid="stream-stats-state"]')?.getAttribute('data-state') === 'connected' &&
      Number(row.querySelector('[data-testid="stream-stats-snapshot"]')?.getAttribute('data-samples') ?? 0) > 0
  }, config.containerId, { timeout: 20_000 })

  process.stdout.write('READY\n')
  await page.waitForFunction((containerId) => {
    const row = document.querySelector(`.docker-row[data-container-id="${containerId}"]`)
    return row?.querySelector('[data-testid="stream-stats-state"]')?.getAttribute('data-state') === 'error' &&
      Boolean(row.querySelector('[data-testid="stream-stats-error"]'))
  }, config.containerId, { timeout: 20_000 })
  const result = await row.evaluate((element) => ({
    closed: element.querySelector('[data-testid="stream-stats-state"]')?.getAttribute('data-state') === 'error',
    state: element.querySelector('[data-testid="stream-stats-state"]')?.getAttribute('data-state'),
    reason: element.querySelector('[data-testid="stream-stats-error"]')?.textContent?.trim() ?? '',
  }))
  if (!result.closed) throw new Error(`product UI did not close the stats stream after Agent disconnect: ${JSON.stringify(result)}`)
  process.stdout.write(`${JSON.stringify(result)}\n`)
  await context.close()
} finally {
  await browser.close()
}
