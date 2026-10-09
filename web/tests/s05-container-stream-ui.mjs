import fs from 'node:fs'
import { chromium } from '@playwright/test'

const config = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'))
let browser

function streamHTTPURL(kind, nodeId, containerId) {
  const url = new URL(`/ws/v1/streams/${kind}`, config.url)
  url.searchParams.set('nodeId', nodeId)
  url.searchParams.set('containerId', containerId)
  return url.toString()
}

async function requestStatus(context, kind, nodeId, containerId, origin) {
  const response = await context.request.get(streamHTTPURL(kind, nodeId, containerId), { headers: { Origin: origin } })
  await response.body()
  return response.status()
}

async function readStatsCounter(context) {
  const response = await context.request.get(config.statsCounterUrl)
  if (!response.ok()) throw new Error(`Docker stats counter endpoint status=${response.status()}`)
  return response.json()
}

async function waitStatsCounter(context, predicate, message) {
  const deadline = Date.now() + 10_000
  let latest
  while (Date.now() < deadline) {
    latest = await readStatsCounter(context)
    if (predicate(latest)) return latest
    await new Promise((resolve) => setTimeout(resolve, 100))
  }
  throw new Error(`${message}; latest=${JSON.stringify(latest)}`)
}

async function setupDashboard(context, containerId) {
  const page = await context.newPage()
  await page.goto(config.url, { waitUntil: 'domcontentloaded' })
  const monitor = page.getByRole('button', { name: '节点监控' })
  await monitor.waitFor({ state: 'visible', timeout: 15_000 })
  await monitor.click()
  const row = page.locator(`.docker-row[data-container-id="${containerId}"]`)
  await row.waitFor({ state: 'visible', timeout: 30_000 })
  await row.locator('[data-testid="container-streams"]').waitFor({ state: 'visible' })
  return { page, row }
}

async function expectLogFlushIntervals(page, expected, phase) {
  await page.waitForFunction((count) => window.__nodedanceLogFlushIntervalCount?.() === count, expected, { timeout: 5_000 })
  const actual = await page.evaluate(() => window.__nodedanceLogFlushIntervalCount?.() ?? -1)
  if (actual !== expected) throw new Error(`${phase}: active log flush intervals=${actual}, want ${expected}`)
  return actual
}

async function openUIStream(page, row, kind) {
  const buttonName = kind === 'logs' ? '查看日志' : '实时统计'
  const stateTestId = kind === 'logs' ? 'stream-log-state' : 'stream-stats-state'
  await row.getByRole('button', { name: buttonName }).click()
  await page.waitForFunction(({ containerId, stateTestId }) => {
    const row = document.querySelector(`.docker-row[data-container-id="${containerId}"]`)
    const state = row?.querySelector(`[data-testid="${stateTestId}"]`)
    return state?.getAttribute('data-state') === 'connected' || state?.getAttribute('data-state') === 'error'
  }, { containerId: await row.getAttribute('data-container-id'), stateTestId }, { timeout: 20_000 })
  const state = await row.getByTestId(stateTestId).getAttribute('data-state')
  if (state !== 'connected') {
    const errorTestId = kind === 'logs' ? 'stream-log-error' : 'stream-stats-error'
    const error = await row.getByTestId(errorTestId).textContent().catch(() => '')
    throw new Error(`${kind} stream did not connect through product UI: ${error}`)
  }
  return { state }
}

async function waitForLogs(page, row, minimumBytes, marker) {
  const containerId = await row.getAttribute('data-container-id')
  try {
    await page.waitForFunction(({ containerId, minimumBytes, marker }) => {
      const output = document.querySelector(`.docker-row[data-container-id="${containerId}"] [data-testid="stream-log-output"]`)
      if (!output) return false
      const bytes = Number(output.getAttribute('data-bytes') ?? 0)
      const channels = (output.getAttribute('data-channels') ?? '').split(',')
      return bytes >= minimumBytes && channels.includes('stdout') && channels.includes('stderr') && output.textContent?.includes(marker)
    }, { containerId, minimumBytes, marker }, { timeout: 45_000 })
  } catch (error) {
    const details = await row.evaluate((element) => ({
      state: element.querySelector('[data-testid="stream-log-state"]')?.getAttribute('data-state'),
      bytes: element.querySelector('[data-testid="stream-log-output"]')?.getAttribute('data-bytes'),
      channels: element.querySelector('[data-testid="stream-log-output"]')?.getAttribute('data-channels'),
      error: element.querySelector('[data-testid="stream-log-error"]')?.textContent,
      sample: element.querySelector('[data-testid="stream-log-output"]')?.textContent?.slice(0, 160),
    }))
    throw new Error(`${String(error)}; final log stream state=${JSON.stringify(details)}`)
  }
  return row.getByTestId('stream-log-output').evaluate((output) => ({
    bytes: Number(output.getAttribute('data-bytes') ?? 0),
    channels: (output.getAttribute('data-channels') ?? '').split(',').filter(Boolean),
    sample: output.textContent?.slice(0, 256) ?? '',
  }))
}

async function waitForStatsSamples(page, row, minimum) {
  const containerId = await row.getAttribute('data-container-id')
  await page.waitForFunction(({ containerId, minimum }) => {
    const card = document.querySelector(`.docker-row[data-container-id="${containerId}"] [data-testid="stream-stats-snapshot"]`)
    return Number(card?.getAttribute('data-samples') ?? 0) >= minimum
  }, { containerId, minimum }, { timeout: 20_000 })
  return Number(await row.getByTestId('stream-stats-snapshot').getAttribute('data-samples'))
}

async function closeUIStream(page, row, kind) {
  const buttonName = kind === 'logs' ? '关闭日志流' : '关闭统计'
  const stateTestId = kind === 'logs' ? 'stream-log-state' : 'stream-stats-state'
  await row.getByRole('button', { name: buttonName }).click()
  await page.waitForFunction(({ containerId, stateTestId }) => {
    const state = document.querySelector(`.docker-row[data-container-id="${containerId}"] [data-testid="${stateTestId}"]`)
    return state?.getAttribute('data-state') === 'idle'
  }, { containerId: await row.getAttribute('data-container-id'), stateTestId }, { timeout: 10_000 })
}

async function main() {
  browser = await chromium.launch({ headless: true })
  const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 390, height: 844 } })
  await context.addInitScript(() => {
    const activeFlushIntervals = new Set()
    const originalSetInterval = window.setInterval.bind(window)
    const originalClearInterval = window.clearInterval.bind(window)
    window.setInterval = (handler, timeout, ...args) => {
      const id = originalSetInterval(handler, timeout, ...args)
      if (timeout === 50) activeFlushIntervals.add(id)
      return id
    }
    window.clearInterval = (id) => {
      activeFlushIntervals.delete(id)
      originalClearInterval(id)
    }
    Object.defineProperty(window, '__nodedanceLogFlushIntervalCount', {
      configurable: false,
      value: () => activeFlushIntervals.size,
    })
  })
  await context.addCookies([{ name: 'nodedance_session', value: config.session, url: config.url,
    secure: true, httpOnly: true, sameSite: 'Strict' }])

  const unauthorizedContext = await browser.newContext({ ignoreHTTPSErrors: true })
  const unauthorized = await requestStatus(unauthorizedContext, 'logs', config.nodeId, config.logContainerId, config.url)
  await unauthorizedContext.close()
  if (unauthorized !== 401) throw new Error(`unauthenticated stream status=${unauthorized}, want 401`)
  const crossOrigin = await requestStatus(context, 'logs', config.nodeId, config.logContainerId, 'https://evil.example')
  if (crossOrigin !== 403) throw new Error(`cross-origin stream status=${crossOrigin}, want 403`)
  const wrongNode = await requestStatus(context, 'logs', '00000000-0000-4000-8000-000000000099', config.logContainerId, config.url)
  if (wrongNode !== 404) throw new Error(`wrong-node stream status=${wrongNode}, want 404`)
  const wrongContainer = await requestStatus(context, 'logs', config.nodeId, 'f'.repeat(64), config.url)
  if (wrongContainer !== 404) throw new Error(`wrong-container stream status=${wrongContainer}, want 404`)

  const logsView = await setupDashboard(context, config.logContainerId)
  const idleContainerRows = await logsView.page.locator('.docker-row').count()
  const idleStreamRows = await logsView.page.locator('[data-testid="container-streams"]').count()
  if (idleContainerRows < 3 || idleStreamRows !== idleContainerRows) {
    throw new Error(`expected multiple idle container stream rows, containers=${idleContainerRows} streamRows=${idleStreamRows}`)
  }
  const idleLogFlushIntervals = await expectLogFlushIntervals(logsView.page, 0, 'idle container rows')
  const responsive = await logsView.row.locator('[data-testid="container-streams"]').evaluate((element) => ({
    width: element.clientWidth, scrollWidth: element.scrollWidth,
  }))
  if (responsive.width <= 0 || responsive.scrollWidth > responsive.width + 2) {
    throw new Error(`stream controls overflow at 390x844: ${JSON.stringify(responsive)}`)
  }
  const logsReady = await openUIStream(logsView.page, logsView.row, 'logs')
  const openedLogFlushIntervals = await expectLogFlushIntervals(logsView.page, 1, 'one active log stream')
  fs.writeFileSync(config.logStartPath, 'browser subscribed to live log stream\n', { mode: 0o600 })
  const logs = await waitForLogs(logsView.page, logsView.row, 512 * 1024, '你好')
  await closeUIStream(logsView.page, logsView.row, 'logs')
  const closedLogFlushIntervals = await expectLogFlushIntervals(logsView.page, 0, 'closed log stream')

  const ttyView = await setupDashboard(context, config.ttyContainerId)
  const ttyReady = await openUIStream(ttyView.page, ttyView.row, 'logs')
  const ttyLogFlushIntervals = await expectLogFlushIntervals(ttyView.page, 1, 'TTY log stream')
  await ttyView.page.waitForFunction((containerId) => {
    const output = document.querySelector(`.docker-row[data-container-id="${containerId}"] [data-testid="stream-log-output"]`)
    return output?.textContent?.includes('TTY-raw-你好') === true && output?.getAttribute('data-channels') === 'stdout'
  }, config.ttyContainerId, { timeout: 15_000 })
  const tty = await ttyView.row.getByTestId('stream-log-output').evaluate((output) => ({
    bytes: Number(output.getAttribute('data-bytes') ?? 0), channels: output.getAttribute('data-channels'), text: output.textContent,
  }))
  await closeUIStream(ttyView.page, ttyView.row, 'logs')
  const ttyClosedFlushIntervals = await expectLogFlushIntervals(ttyView.page, 0, 'closed TTY log stream')
  await openUIStream(ttyView.page, ttyView.row, 'logs')
  const beforeUnmountFlushIntervals = await expectLogFlushIntervals(ttyView.page, 1, 'log stream before unmount')
  await ttyView.page.getByRole('button', { name: '账户设置' }).click()
  await ttyView.page.getByRole('button', { name: '节点监控' }).waitFor({ state: 'visible', timeout: 10_000 })
  await ttyView.page.locator('.nodes-dashboard').waitFor({ state: 'detached', timeout: 10_000 })
  const unmountedFlushIntervals = await expectLogFlushIntervals(ttyView.page, 0, 'Dashboard unmount')

  const statsView1 = await setupDashboard(context, config.statsContainerId)
  const statsView2 = await setupDashboard(context, config.statsContainerId)
  const statsReady1 = await openUIStream(statsView1.page, statsView1.row, 'stats')
  const firstSamples = await waitForStatsSamples(statsView1.page, statsView1.row, 1)
  const statsReady2 = await openUIStream(statsView2.page, statsView2.row, 'stats')
  await waitForStatsSamples(statsView2.page, statsView2.row, 1)
  const sharedCounter = await waitStatsCounter(context, (count) => count.opens === 1 && count.active === 1,
    'two product pages did not share one Engine stats reader')
  const samplesBeforeHeartbeatWait = Number(await statsView2.row.getByTestId('stream-stats-snapshot').getAttribute('data-samples'))
  await new Promise((resolve) => setTimeout(resolve, 12_000))
  const samplesAfterHeartbeatWait = await waitForStatsSamples(statsView2.page, statsView2.row, samplesBeforeHeartbeatWait + 1)
  const statsFields = await statsView2.row.evaluate((row) => Object.fromEntries(
    ['stream-cpu', 'stream-memory', 'stream-network', 'stream-block-io'].map((name) => [name,
      row.querySelector(`[data-testid="${name}"]`)?.textContent?.trim() ?? '']),
  ))
  const invalidStatsFields = Object.entries(statsFields)
    .filter(([, value]) => !value || value === '未知' || value.includes('warming_up'))
  if (invalidStatsFields.length > 0) {
    throw new Error(`stats metrics remained warming_up or unknown without a reason after 12s and ${samplesAfterHeartbeatWait} samples: ${JSON.stringify(statsFields)}`)
  }
  await closeUIStream(statsView1.page, statsView1.row, 'stats')
  const samplesAfterFirstLeft = await waitForStatsSamples(statsView2.page, statsView2.row, samplesAfterHeartbeatWait + 1)
  const retainedCounter = await readStatsCounter(context)
  if (retainedCounter.opens !== 1 || retainedCounter.active !== 1) throw new Error(`first product page closing stopped a shared Engine stream: ${JSON.stringify(retainedCounter)}`)
  await closeUIStream(statsView2.page, statsView2.row, 'stats')
  const stoppedCounter = await waitStatsCounter(context, (count) => count.opens === 1 && count.active === 0,
    'last product page leaving did not close Engine stats')

  const reopened = await setupDashboard(context, config.statsContainerId)
  const reopenedReady = await openUIStream(reopened.page, reopened.row, 'stats')
  const reopenedSamples = await waitForStatsSamples(reopened.page, reopened.row, 1)
  const reopenedCounter = await waitStatsCounter(context, (count) => count.opens === 2 && count.active === 1,
    'product stats stream did not restart after the final subscriber left')
  await closeUIStream(reopened.page, reopened.row, 'stats')
  const finalCounter = await waitStatsCounter(context, (count) => count.opens === 2 && count.active === 0,
    'reopened Engine stats stream remained active after the final product page closed')

  await context.close()
  await browser.close()
  browser = undefined
  process.stdout.write(`${JSON.stringify({ unauthorized, crossOrigin, wrongNode, wrongContainer, responsive,
    idleContainerRows, idleStreamRows, idleLogFlushIntervals, logsReady, openedLogFlushIntervals,
    logs, closedLogFlushIntervals, ttyReady, ttyLogFlushIntervals, tty, ttyClosedFlushIntervals,
    beforeUnmountFlushIntervals, unmountedFlushIntervals, statsReady1, firstSamples, statsReady2, sharedCounter,
    samplesBeforeHeartbeatWait, samplesAfterHeartbeatWait, samplesAfterFirstLeft, retainedCounter, stoppedCounter,
    statsFields, reopenedReady, reopenedSamples, reopenedCounter, finalCounter })}\n`)
}

main().catch(async (error) => {
  process.stderr.write(`${String(error?.stack ?? error)}\n`)
  await browser?.close()
  process.exitCode = 1
})
