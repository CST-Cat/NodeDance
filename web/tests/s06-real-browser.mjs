import fs from 'node:fs'
import path from 'node:path'
import { chromium } from '@playwright/test'

const config = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'))
const expected = config.expectedContainers
const caseResults = {}
const screenshots = []
let browser
let context
let page
let preferenceWrites = []

function fail(message) {
  throw new Error(message)
}

async function captureCase(id, run) {
  try {
    caseResults[id] = { status: 'PASS', ...(await run()) }
  } catch (error) {
    caseResults[id] = { status: 'FAIL', error: String(error).slice(0, 500) }
  }
}

async function openDashboard() {
  browser = await chromium.launch({ headless: true })
  const anonymous = await browser.newPage({
    ignoreHTTPSErrors: true,
    viewport: { width: 1440, height: 900 },
  })
  await anonymous.goto(config.url, { waitUntil: 'domcontentloaded' })
  await anonymous.locator('[data-screen="login"]').waitFor({ timeout: 15_000 })
  const unauthorizedStatus = await anonymous.evaluate(async () => (await fetch('/api/v1/nodes')).status)
  if (unauthorizedStatus !== 401) fail(`anonymous Core API status was ${unauthorizedStatus}, expected 401`)
  await anonymous.close()

  context = await browser.newContext({
    ignoreHTTPSErrors: true,
    viewport: { width: 375, height: 900 },
    isMobile: true,
    hasTouch: true,
    deviceScaleFactor: 1,
  })
  await context.addCookies([{
    name: 'nodedance_session',
    value: config.session,
    url: config.url,
    secure: true,
    httpOnly: true,
    sameSite: 'Strict',
  }])
  await context.addInitScript(() => {
    Object.defineProperty(window, '__s06PointerEvents', { value: [] })
    for (const type of ['pointerdown', 'pointermove', 'pointerup', 'pointercancel']) {
      window.addEventListener(type, (event) => {
        if (event instanceof PointerEvent) {
          window.__s06PointerEvents.push({ type, pointerType: event.pointerType })
        }
      }, true)
    }
  })
  page = await context.newPage()
  page.on('response', (response) => {
    const request = response.request()
    if (request.method() === 'PUT' && new URL(response.url()).pathname === `/api/v1/nodes/${config.nodeId}/preferences`) {
      preferenceWrites.push(response.status())
    }
  })
  await page.goto(config.url, { waitUntil: 'domcontentloaded' })
  await showDashboard()
}

async function showDashboard() {
  const monitor = page.getByRole('button', { name: '节点监控', exact: true })
  await monitor.waitFor({ timeout: 15_000 })
  await monitor.click()
  const nodeOption = page.locator('.node-option').first()
  await nodeOption.waitFor({ timeout: 15_000 })
  if (!(await nodeOption.getAttribute('class'))?.split(/\s+/).includes('selected')) await nodeOption.click()
  await page.getByTestId('fused-overview').waitFor({ state: 'visible', timeout: 30_000 })
  await page.waitForFunction(() => {
    const state = document.querySelector('.node-option.selected .node-option-dot')
    return state?.getAttribute('data-status') === 'online' && document.querySelectorAll('.featured-container').length === 4
  }, undefined, { timeout: 30_000 })
}

async function chooseSection(label) {
  const button = page.getByRole('button', { name: label, exact: true })
  await button.waitFor({ timeout: 15_000 })
  await button.click()
  if (await button.getAttribute('aria-current') !== 'page') fail(`${label} tab did not become active`)
}

async function readLiveInventory() {
  return page.evaluate(async (nodeId) => {
    const response = await fetch(`/api/v1/nodes/${encodeURIComponent(nodeId)}/containers`, {
      credentials: 'include', cache: 'no-store',
    })
    const payload = response.ok ? await response.json() : null
    return {
      status: response.status,
      ids: Array.isArray(payload?.inventory?.containers)
        ? payload.inventory.containers.map((record) => record.container.id)
        : [],
      stale: payload?.inventory?.dataStale ?? null,
      available: payload?.inventory?.dockerAvailability ?? null,
    }
  }, config.nodeId)
}

async function verifyOverviewAndDetails() {
  await page.getByTestId('fused-overview').waitFor({ state: 'visible', timeout: 20_000 })
  await page.waitForFunction(() => document.querySelectorAll('.featured-container').length === 4, undefined, { timeout: 20_000 })
  const featuredCount = await page.locator('.featured-container').count()
  await chooseSection('Docker 详情')
  await page.locator('.docker-panel').waitFor({ state: 'visible', timeout: 20_000 })
  await page.waitForFunction(() => document.querySelectorAll('.docker-row').length === 40, undefined, { timeout: 30_000 })
  const rows = await page.locator('.docker-row').evaluateAll((elements) => elements.map((row) => row.getAttribute('data-container-id')))
  const inventory = await readLiveInventory()
  const wantIDs = expected.map((item) => item.id).sort()
  const rowIDs = rows.filter((id) => typeof id === 'string').sort()
  const apiIDs = [...inventory.ids].sort()
  const exactIDsMatch = inventory.status === 200 && inventory.available === 'available' && inventory.stale === false &&
    wantIDs.length === rowIDs.length && wantIDs.every((id, index) => id === rowIDs[index]) &&
    wantIDs.length === apiIDs.length && wantIDs.every((id, index) => id === apiIDs[index])
  if (!exactIDsMatch) fail(`real Engine/Core/browser inventory did not match the 40 exact run-owned IDs (API=${inventory.status}, rows=${rowIDs.length}, API rows=${apiIDs.length})`)
  if (featuredCount !== 4 || rows.length !== 40) fail(`featured/detail counts were ${featuredCount}/${rows.length}, expected 4/40`)
  return {
    featuredCount,
    renderedCount: rows.length,
    apiInventoryCount: inventory.ids.length,
    exactIDsMatch,
  }
}

async function measureResponsivePage(width, section) {
  await page.setViewportSize({ width, height: 900 })
  await page.waitForTimeout(100)
  if (section === 'overview') {
    await page.getByTestId('fused-overview').waitFor({ state: 'visible', timeout: 15_000 })
  } else {
    await page.locator('.docker-panel').waitFor({ state: 'visible', timeout: 15_000 })
    await page.locator('.docker-row').first().waitFor({ state: 'visible', timeout: 15_000 })
  }
  const measurements = await page.evaluate((selectedSection) => {
    const visible = (element) => {
      if (!element) return false
      const style = getComputedStyle(element)
      return style.display !== 'none' && style.visibility !== 'hidden'
    }
    const selectors = selectedSection === 'overview'
      ? ['.nodes-dashboard', '.nodes-layout', '.node-list', '.node-detail', '.fused-overview', '.host-summary-card', '.featured-containers-card']
      : ['.nodes-dashboard', '.nodes-layout', '.node-list', '.node-detail', '.docker-panel', '.docker-row', '.drag-handle']
    const elements = selectors.map((selector) => {
      const element = document.querySelector(selector)
      if (!visible(element)) return { selector, visible: false }
      const rect = element.getBoundingClientRect()
      return { selector, visible: true, x: rect.left, right: rect.right, width: rect.width }
    })
    return {
      viewport: window.innerWidth,
      documentWidth: document.documentElement.scrollWidth,
      bodyWidth: document.body.scrollWidth,
      elements,
    }
  }, section)
  const overflow = measurements.documentWidth > width || measurements.bodyWidth > width || measurements.elements.some((item) =>
    item.visible && (item.x < -1 || item.right > width + 1 || item.width <= 0))
  if (overflow) fail(`${section} page overflowed ${width}px viewport: ${JSON.stringify(measurements)}`)
  if (measurements.elements.some((item) => !item.visible)) fail(`${section} core element was not visible at ${width}px`)
  return measurements
}

async function verifyResponsiveWidths() {
  const viewports = []
  let overflowCount = 0
  let controlCount = 0
  for (const width of [375, 768, 1440]) {
    await chooseSection('总览')
    await measureResponsivePage(width, 'overview')
    const overviewScreenshot = path.join(config.evidenceDir, `s06-overview-${width}.png`)
    await page.screenshot({ path: overviewScreenshot, fullPage: false })
    screenshots.push(overviewScreenshot)

    await chooseSection('Docker 详情')
    await measureResponsivePage(width, 'docker')
    const handle = page.getByRole('button', { name: '拖动调整容器顺序' }).first()
    if (!(await handle.isVisible()) || !(await handle.isEnabled()) || !(await handle.getAttribute('aria-label'))) {
      fail(`Docker reorder control is not visible and operable at ${width}px`)
    }
    controlCount += 1
    const dockerScreenshot = path.join(config.evidenceDir, `s06-docker-${width}.png`)
    await page.screenshot({ path: dockerScreenshot, fullPage: false })
    screenshots.push(dockerScreenshot)
    viewports.push(String(width))
  }
  return { viewports, overflowCount, controlCount, screenshots: screenshots.slice(-6) }
}

async function currentPreferenceList() {
  const response = await page.evaluate(async (nodeId) => {
    const result = await fetch(`/api/v1/nodes/${encodeURIComponent(nodeId)}/preferences`, {
      credentials: 'include', cache: 'no-store',
    })
    return { status: result.status, body: result.ok ? await result.json() : null }
  }, config.nodeId)
  if (response.status !== 200 || !Array.isArray(response.body?.preferences)) {
    fail(`authenticated Core preferences GET returned HTTP ${response.status}`)
  }
  return response.body.preferences
}

async function reorderWithRealTouch() {
  await page.setViewportSize({ width: 375, height: 900 })
  await chooseSection('Docker 详情')
  await page.locator('.docker-row').first().waitFor({ state: 'visible', timeout: 20_000 })
  const sourceID = expected[0].id
  const targetID = expected[1].id
  const source = page.locator(`.docker-row[data-container-id="${sourceID}"]`)
  const handle = source.getByRole('button', { name: '拖动调整容器顺序' })
  const target = page.locator(`.docker-row[data-container-id="${targetID}"]`)
  await source.scrollIntoViewIfNeeded()
  await target.scrollIntoViewIfNeeded()
  const sourceBox = await handle.boundingBox()
  const targetBox = await target.boundingBox()
  if (!sourceBox || !targetBox) fail('touch reorder source/target does not have a visible box')

  preferenceWrites = []
  const cdp = await context.newCDPSession(page)
  await cdp.send('Emulation.setTouchEmulationEnabled', { enabled: true, maxTouchPoints: 1 })
  const start = { x: sourceBox.x + sourceBox.width / 2, y: sourceBox.y + sourceBox.height / 2, id: 71, radiusX: 4, radiusY: 4, force: 1 }
  const end = { x: targetBox.x + targetBox.width / 2, y: targetBox.y + targetBox.height / 2, id: 71, radiusX: 4, radiusY: 4, force: 1 }
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [start] })
  await page.waitForTimeout(80)
  const middle = { ...end, x: Math.round((start.x + end.x) / 2), y: Math.round((start.y + end.y) / 2) }
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [middle] })
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [end] })
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })

  await page.waitForFunction(async ({ nodeId, sourceID, targetID }) => {
    const response = await fetch(`/api/v1/nodes/${encodeURIComponent(nodeId)}/preferences`, { credentials: 'include', cache: 'no-store' })
    if (!response.ok) return false
    const body = await response.json()
    const source = body.preferences?.find((item) => item.identity === `container:${sourceID}`)
    const target = body.preferences?.find((item) => item.identity === `container:${targetID}`)
    return body.preferences?.length === 40 && source?.sortOrder === 1 && target?.sortOrder === 0
  }, { nodeId: config.nodeId, sourceID, targetID }, { timeout: 30_000 })
  await page.waitForFunction((targetID) => document.querySelector('.docker-row')?.getAttribute('data-container-id') === targetID,
    targetID, { timeout: 10_000 })
  await page.waitForTimeout(200)

  const preferences = await currentPreferenceList()
  const sourcePreference = preferences.find((item) => item.identity === `container:${sourceID}`)
  const targetPreference = preferences.find((item) => item.identity === `container:${targetID}`)
  const pointerEvents = await page.evaluate(() => window.__s06PointerEvents.filter((item) => item.pointerType === 'touch'))
  if (pointerEvents.length < 3 || !pointerEvents.some((item) => item.type === 'pointerdown') ||
    !pointerEvents.some((item) => item.type === 'pointermove') || !pointerEvents.some((item) => item.type === 'pointerup')) {
    fail(`touch input did not produce a complete real pointer sequence (events=${pointerEvents.length})`)
  }
  const failures = preferenceWrites.filter((status) => status !== 204)
  if (preferenceWrites.length !== 40 || failures.length !== 0 || preferences.length !== 40 ||
    sourcePreference?.sortOrder !== 1 || targetPreference?.sortOrder !== 0) {
    fail(`Core did not persist all 40 reorder updates (writes=${preferenceWrites.length}, failed=${failures.length}, prefs=${preferences.length})`)
  }

  await page.reload({ waitUntil: 'domcontentloaded' })
  await showDashboard()
  await chooseSection('Docker 详情')
  await page.waitForFunction((targetID) => document.querySelector('.docker-row')?.getAttribute('data-container-id') === targetID, targetID, { timeout: 20_000 })
  const reloadedFirstID = await page.locator('.docker-row').first().getAttribute('data-container-id')
  const screenshot = path.join(config.evidenceDir, 's06-touch-reorder-375.png')
  await page.screenshot({ path: screenshot, fullPage: false })
  screenshots.push(screenshot)
  await cdp.detach()
  return {
    touchPointerEvents: pointerEvents.length,
    preferenceWrites: preferenceWrites.length,
    preferenceWriteFailures: failures.length,
    reloadedFirstID,
    screenshots: [screenshot],
  }
}

async function main() {
  let setupError = ''
  try {
    await openDashboard()
  } catch (error) {
    setupError = String(error).slice(0, 500)
  }
  if (setupError) {
    for (const id of ['S06-01', 'S06-11', 'S06-12']) caseResults[id] = { status: 'FAIL', error: `real browser/Core setup failed: ${setupError}` }
  } else {
    await captureCase('S06-01', verifyOverviewAndDetails)
    await captureCase('S06-11', verifyResponsiveWidths)
    await captureCase('S06-12', reorderWithRealTouch)
  }
  const result = {
    browser: page ? await page.evaluate(() => navigator.userAgent).catch(() => 'Chromium') : 'Chromium',
    cases: caseResults,
    screenshots,
  }
  process.stdout.write(JSON.stringify(result))
}

try {
  await main()
} finally {
  if (browser) await browser.close().catch(() => {})
}
