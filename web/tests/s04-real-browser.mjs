import fs from 'node:fs'
import readline from 'node:readline'
import { promisify } from 'node:util'
import { execFile as execFileCallback } from 'node:child_process'
import { chromium } from '@playwright/test'

const execFile = promisify(execFileCallback)
const config = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'))
let browser
let context
let page
let opened = false
let networkLossEnabled = false
let networkHTTPFailures = 0
let networkWebSocketFailures = 0

function fixtureRow() {
  return page.locator('.docker-row').filter({
    has: page.locator('.docker-container-copy strong', { hasText: config.containerName }),
  }).first()
}

async function waitFresh() {
  try {
    await page.waitForFunction(() => {
      const state = document.querySelector('.docker-state')
      const rows = [...document.querySelectorAll('.docker-row')]
      return state?.getAttribute('data-available') === 'available' && state?.getAttribute('data-stale') === 'false' &&
        rows.length > 0 && rows.every((row) => row.getAttribute('data-stale') === 'false')
    }, undefined, { timeout: 20_000 })
  } catch (error) {
    const diagnostics = await page.evaluate(async ({ nodeId, containerName }) => {
      const [response, metricsResponse, inventoryResponse] = await Promise.all([
        fetch('/api/v1/nodes').catch(() => null),
        fetch(`/api/v1/nodes/${encodeURIComponent(nodeId)}/metrics`).catch(() => null),
        fetch(`/api/v1/nodes/${encodeURIComponent(nodeId)}/containers`).catch(() => null),
      ])
      const payload = response?.ok ? await response.clone().json().catch(() => null) : null
      const metrics = metricsResponse?.ok ? await metricsResponse.clone().json().catch(() => null) : null
      const inventory = inventoryResponse?.ok ? await inventoryResponse.clone().json().catch(() => null) : null
      return {
        url: location.href,
        screen: document.querySelector('.app-shell')?.getAttribute('data-screen'),
        dashboard: Boolean(document.querySelector('.nodes-dashboard')),
        nodes: document.querySelectorAll('.node-option').length,
        selected: document.querySelector('.node-option.selected')?.innerText ?? null,
        dockerState: document.querySelector('.docker-state')?.outerHTML ?? null,
        dockerMessage: document.querySelector('.docker-empty')?.innerText ?? null,
        rows: document.querySelectorAll('.docker-row').length,
        error: document.querySelector('[role="alert"]')?.innerText ?? null,
        nodesStatus: response?.status ?? null,
        nodeCount: Array.isArray(payload?.nodes) ? payload.nodes.length : null,
        nodeSummaries: Array.isArray(payload?.nodes) ? payload.nodes.map(({ nodeId, agentId, status }) => ({ nodeId, hasAgent: Boolean(agentId), status })) : null,
        metricsStatus: metricsResponse?.status ?? null,
        metricsType: metrics?.type ?? 'metrics',
        diskMountsIsArray: Array.isArray(metrics?.metrics?.disk?.mounts),
        networkInterfacesIsArray: Array.isArray(metrics?.metrics?.network?.interfaces),
        inventoryStatus: inventoryResponse?.status ?? null,
        inventoryContainersIsArray: Array.isArray(inventory?.inventory?.containers),
        inventoryContainerCount: Array.isArray(inventory?.inventory?.containers) ? inventory.inventory.containers.length : null,
        fixturePortsIsArray: inventory?.inventory?.containers?.find((record) => record.container?.name === containerName)?.container?.ports instanceof Array,
        bodyText: document.body.innerText.slice(0, 500),
      }
    }, { nodeId: config.nodeId, containerName: config.containerName }).catch((diagnosticError) => ({ diagnosticError: String(diagnosticError) }))
    throw new Error(`dashboard did not become fresh: ${JSON.stringify(diagnostics)}; ${String(error)}`)
  }
  await fixtureRow().waitFor({ state: 'visible', timeout: 10_000 })
  return {
    dockerState: await page.locator('.docker-state').innerText(),
    initialRows: await page.locator('.docker-row').count(),
    containerIDs: await page.locator('.docker-row').evaluateAll((rows) => rows.map((row) => row.getAttribute('data-container-id')).sort()),
    staleRows: await page.locator('.docker-row[data-stale="true"]').count(),
  }
}

async function observeDockerRows() {
  await page.evaluate((name) => {
    window.__s04DockerObserver = { name, pending: null, observed: null }
    const matches = () => {
      const observer = window.__s04DockerObserver
      if (!observer?.pending) return
      for (const row of document.querySelectorAll('.docker-row')) {
        const label = row.querySelector('.docker-container-copy strong')?.textContent?.trim()
        const state = row.querySelector('.container-state')?.getAttribute('data-running')
        if (label === observer.name && state === observer.pending.running) {
          observer.observed = { running: state, at: Date.now(), performanceAt: performance.now() }
        }
      }
    }
    const observer = new MutationObserver(matches)
    observer.observe(document.querySelector('.docker-panel'), {
      attributes: true,
      characterData: true,
      childList: true,
      subtree: true,
    })
    matches()
  }, config.containerName)
}

async function open() {
  if (opened) throw new Error('browser page already opened')
  browser = await chromium.launch({ headless: true })

  const anonymous = await browser.newPage({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 } })
  await anonymous.goto(config.url, { waitUntil: 'domcontentloaded' })
  await anonymous.locator('[data-screen="login"]').waitFor({ timeout: 15_000 })
  const unauthorizedStatus = await anonymous.evaluate(async () => (await fetch('/api/v1/nodes')).status)
  const anonymousRows = await anonymous.locator('.docker-row').count()
  await anonymous.close()
  if (unauthorizedStatus !== 401 || anonymousRows !== 0) {
    throw new Error(`anonymous real-browser access was not denied: API=${unauthorizedStatus} rows=${anonymousRows}`)
  }

  context = await browser.newContext({
    ignoreHTTPSErrors: true,
    viewport: { width: 1440, height: 900 },
  })
  await context.route('**/api/v1/nodes**', async (route) => {
    if (networkLossEnabled) {
      networkHTTPFailures += 1
      await route.abort('failed')
      return
    }
    await route.continue()
  })
  await context.routeWebSocket('**/ws/v1/dashboard', (route) => {
    if (networkLossEnabled) {
      networkWebSocketFailures += 1
      route.close({ code: 1001, reason: 'S04 browser network outage' })
      return
    }
    route.connectToServer()
  })
  await context.addInitScript(() => {
    const leases = Object.create(null)
    const sockets = []
    const rememberLease = (value, receivedAt = performance.now()) => {
      if (typeof value !== 'object' || value === null) return
      const save = (nodeId, state, serverTime) => {
        if (!nodeId || !state?.leaseValidUntil || !state?.serverTime && !serverTime) return
        leases[nodeId] = {
          serverTime: state.serverTime || serverTime,
          leaseValidUntil: state.leaseValidUntil,
          receivedAt,
        }
      }
      if (Array.isArray(value.nodes) && typeof value.serverTime === 'string') {
        for (const node of value.nodes) save(node.nodeId, node, value.serverTime)
      }
      if (value.state) save(value.nodeId, value.state, value.serverTime)
      if (value.nodeId && value.leaseValidUntil && value.serverTime) save(value.nodeId, value, value.serverTime)
    }
    const NativeWebSocket = window.WebSocket
    const TrackedWebSocket = new Proxy(NativeWebSocket, {
      construct(target, args, newTarget) {
        const socket = Reflect.construct(target, args, newTarget)
        socket.addEventListener('message', (event) => {
          try { rememberLease(JSON.parse(String(event.data))) } catch { /* ignore unrelated frames */ }
        })
        sockets.push(socket)
        return socket
      },
    })
    Object.defineProperty(window, 'WebSocket', { value: TrackedWebSocket })
    Object.defineProperty(window, '__s04CoreLeases', { value: leases })
    Object.defineProperty(window, '__s04CloseDashboardSockets', { value: () => {
      for (const socket of sockets) {
        if (socket.url.includes('/ws/v1/dashboard') && socket.readyState < WebSocket.CLOSING) {
          socket.close(4001, 'S04 browser network outage')
        }
      }
    } })
    const nativeFetch = window.fetch.bind(window)
    window.fetch = async (input, init) => {
      const response = await nativeFetch(input, init)
      if (String(input).includes('/api/v1/nodes') && response.ok) {
        const receivedAt = performance.now()
        void response.clone().json().then((value) => rememberLease(value, receivedAt)).catch(() => {})
      }
      return response
    }
  })
  await context.addCookies([{
    name: 'nodedance_session',
    value: config.session,
    url: config.url,
    secure: true,
    httpOnly: true,
    sameSite: 'Strict',
  }])
  page = await context.newPage()
  page.on('pageerror', (error) => process.stderr.write(`S04_BROWSER_PAGEERROR ${String(error)}\n`))
  page.on('requestfailed', (request) => process.stderr.write(`S04_BROWSER_REQUESTFAILED ${request.url()} ${request.failure()?.errorText ?? ''}\n`))
  let consoleErrors = 0
  page.on('console', (message) => {
    if (message.type() === 'error' && consoleErrors++ < 4) process.stderr.write(`S04_BROWSER_CONSOLEERROR ${message.text().split('\n')[0]}\n`)
  })
  await page.goto(config.url, { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: '节点监控' }).waitFor({ timeout: 15_000 })
  await page.getByRole('button', { name: '节点监控' }).click()
  const initial = await waitFresh()
  const rowName = await fixtureRow().locator('.docker-container-copy strong').innerText()
  if (rowName !== config.containerName) throw new Error(`Core inventory rendered wrong fixture: ${rowName}`)
  const state = await fixtureRow().locator('.container-state').getAttribute('data-running')
  if (state !== 'false') throw new Error(`new fixture did not render its Created-only state: data-running=${state}`)

  await observeDockerRows()
  opened = true
  return {
    browser: `Chromium ${await page.evaluate(() => navigator.userAgent)}`,
    initialRows: initial.initialRows,
    containerIDs: initial.containerIDs,
    rows: await page.locator('.docker-row').evaluateAll((rows) => rows.map((row) => ({
      id: row.getAttribute('data-container-id'),
      name: row.querySelector('.docker-container-copy strong')?.textContent?.trim() ?? '',
      state: row.querySelector('.container-state')?.textContent?.trim() ?? '',
      health: row.querySelector('[data-health]')?.textContent?.trim() ?? '',
      ports: [...row.querySelectorAll('.container-ports span')].map((port) => port.textContent?.trim() ?? ''),
    }))),
    unauthorizedStatus,
  }
}

async function waitHealth(expected) {
  await page.waitForFunction(({ name, expected }) => {
    const row = [...document.querySelectorAll('.docker-row')].find((item) =>
      item.querySelector('.docker-container-copy strong')?.textContent?.trim() === name)
    return row?.querySelector('[data-health]')?.textContent?.trim() === expected
  }, { name: config.healthContainerName, expected }, { timeout: 15_000 })
  const row = page.locator('.docker-row').filter({ has: page.locator('.docker-container-copy strong', { hasText: config.healthContainerName }) })
  return { health: await row.locator('[data-health]').innerText() }
}

async function responsive() {
  const viewports = []
  for (const width of [375, 768, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    await page.waitForTimeout(75)
    const measurements = await page.evaluate(() => {
      const panel = document.querySelector('.docker-panel')
      const row = document.querySelector('.docker-row')
      const panelRect = panel?.getBoundingClientRect()
      const rowRect = row?.getBoundingClientRect()
      return {
        width: window.innerWidth,
        bodyWidth: document.documentElement.scrollWidth,
        panelX: panelRect?.x ?? -1,
        panelRight: panelRect?.right ?? -1,
        rowX: rowRect?.x ?? -1,
        rowRight: rowRect?.right ?? -1,
      }
    })
    if (measurements.bodyWidth > width || measurements.panelX < 0 || measurements.panelRight > width ||
        measurements.rowX < 0 || measurements.rowRight > width) {
      throw new Error(`responsive layout overflows ${width}px viewport: ${JSON.stringify(measurements)}`)
    }
    if (!(await fixtureRow().isVisible())) throw new Error(`fixture is not visible at ${width}px`)
    viewports.push(`${width}px`)
  }
  return { viewports }
}

async function waitUnavailable() {
  await page.waitForFunction(() => {
    const docker = document.querySelector('.docker-state')
    const node = document.querySelector('.node-option-dot')
    const rows = [...document.querySelectorAll('.docker-row')]
    return docker?.getAttribute('data-available') === 'unavailable' &&
      docker?.getAttribute('data-stale') === 'true' && node?.getAttribute('data-status') === 'online' &&
      rows.length > 0 && rows.every((row) => row.getAttribute('data-stale') === 'true')
  }, undefined, { timeout: 25_000 })
  return {
    dockerState: await page.locator('.docker-state').innerText(),
    initialRows: await page.locator('.docker-row').count(),
    containerIDs: await page.locator('.docker-row').evaluateAll((rows) => rows.map((row) => row.getAttribute('data-container-id')).sort()),
    staleRows: await page.locator('.docker-row[data-stale="true"]').count(),
  }
}

async function waitAgentOffline() {
  await page.waitForFunction(() => {
    const docker = document.querySelector('.docker-state')
    const node = document.querySelector('.node-option.selected .node-option-dot')
    const rows = [...document.querySelectorAll('.docker-row')]
    return docker?.getAttribute('data-stale') === 'true' && node?.getAttribute('data-status') === 'offline' &&
      rows.length > 0 && rows.every((row) => row.getAttribute('data-stale') === 'true')
  }, undefined, { timeout: 40_000 })
  return {
    dockerState: await page.locator('.docker-state').innerText(),
    initialRows: await page.locator('.docker-row').count(),
    containerIDs: await page.locator('.docker-row').evaluateAll((rows) => rows.map((row) => row.getAttribute('data-container-id')).sort()),
    staleRows: await page.locator('.docker-row[data-stale="true"]').count(),
  }
}

async function simulateNetworkLoss() {
  networkHTTPFailures = 0
  networkWebSocketFailures = 0
  networkLossEnabled = true
  return await page.evaluate((nodeId) => {
    const clock = window.__s04CoreLeases?.[nodeId]
    if (!clock) throw new Error(`no real Core lease message was observed for ${nodeId}`)
    const leaseDuration = Date.parse(clock.leaseValidUntil) - Date.parse(clock.serverTime)
    const predictedRemainingMs = Math.max(0, Math.round(leaseDuration - (performance.now() - clock.receivedAt)))
    window.__s04LeaseOutage = { startedAt: performance.now(), predictedRemainingMs }
    window.__s04CloseDashboardSockets()
    return { predictedRemainingMs }
  }, config.nodeId)
}

async function waitLeaseStale() {
  await page.waitForFunction(() => {
    const docker = document.querySelector('.docker-state')
    const node = document.querySelector('.node-option.selected .node-option-dot')
    const rows = [...document.querySelectorAll('.docker-row')]
    return docker?.getAttribute('data-stale') === 'true' && node?.getAttribute('data-status') === 'offline' &&
      rows.length > 0 && rows.every((row) => row.getAttribute('data-stale') === 'true')
  }, undefined, { timeout: 40_000 })
  return await page.evaluate(({ httpFailures, websocketFailures }) => ({
    dockerState: document.querySelector('.docker-state')?.innerText ?? null,
    initialRows: document.querySelectorAll('.docker-row').length,
    containerIDs: [...document.querySelectorAll('.docker-row')].map((row) => row.getAttribute('data-container-id')).sort(),
    staleRows: document.querySelectorAll('.docker-row[data-stale="true"]').length,
    elapsedMs: Math.round(performance.now() - window.__s04LeaseOutage.startedAt),
    predictedRemainingMs: window.__s04LeaseOutage.predictedRemainingMs,
    httpFailures,
    websocketFailures,
  }), { httpFailures: networkHTTPFailures, websocketFailures: networkWebSocketFailures })
}

async function restoreNetwork() {
  networkLossEnabled = false
  await page.reload({ waitUntil: 'domcontentloaded' })
  // Core intentionally opens authenticated sessions on the settings screen;
  // return to the live dashboard before asserting that its channels recovered.
  await page.getByRole('button', { name: '节点监控' }).waitFor({ timeout: 15_000 })
  await page.getByRole('button', { name: '节点监控' }).click()
  const fresh = await waitFresh()
  await observeDockerRows()
  return fresh
}

async function measure100() {
  const samples = []
  for (let index = 0; index < 100; index += 1) {
    const running = index % 2 === 0
    const action = running ? 'start' : 'stop'
    await page.evaluate(({ name, running }) => {
      window.__s04DockerObserver.pending = { name, running: String(running) }
      window.__s04DockerObserver.observed = null
    }, { name: config.containerName, running })
    const requestedAt = new Date().toISOString()
    const requestedEpoch = Date.now()
    await execFile('docker', [
      '--host', config.dockerEndpoint,
      'container', action,
      ...(action === 'stop' ? ['--time', '0'] : []),
      config.containerId,
    ], { timeout: 15_000, maxBuffer: 1024 * 1024 })
    await page.waitForFunction(() => Boolean(window.__s04DockerObserver?.observed), { timeout: 10_000 })
    const observed = await page.evaluate(() => window.__s04DockerObserver.observed)
    const latencyMS = observed.at - requestedEpoch
    if (observed.running !== String(running) || latencyMS < 0) {
      throw new Error(`DOM observer captured wrong state at sample ${index + 1}: ${JSON.stringify(observed)}`)
    }
    samples.push({
      index: index + 1,
      action,
      requestedAt,
      observedAt: new Date(observed.at).toISOString(),
      latencyMs: latencyMS,
    })
  }
  const sorted = samples.map((sample) => sample.latencyMs).sort((a, b) => a - b)
  const total = sorted.reduce((sum, latency) => sum + latency, 0)
  return {
    count: samples.length,
    missed: 0,
    minMs: sorted[0],
    meanMs: Math.round(total / sorted.length),
    p50Ms: sorted[Math.floor(sorted.length / 2)],
    p95Ms: sorted[Math.ceil(sorted.length * 0.95) - 1],
    maxMs: sorted.at(-1),
    samples,
  }
}

async function handle(action, argument) {
  switch (action) {
    case 'open': return await open()
    case 'responsive': return await responsive()
    case 'waitUnavailable': return await waitUnavailable()
    case 'waitAgentOffline': return await waitAgentOffline()
    case 'simulateNetworkLoss': return await simulateNetworkLoss()
    case 'waitLeaseStale': return await waitLeaseStale()
    case 'restoreNetwork': return await restoreNetwork()
    case 'waitFresh': return await waitFresh()
    case 'waitHealth': return await waitHealth(argument)
    case 'measure100': return await measure100()
    case 'close':
      await browser?.close()
      browser = undefined
      return { closed: true }
    default: throw new Error(`unknown browser harness action: ${action}`)
  }
}

const lines = readline.createInterface({ input: process.stdin, crlfDelay: Infinity })
for await (const line of lines) {
  if (!line) continue
  let request
  try {
    request = JSON.parse(line)
    const result = await handle(request.action, request.argument)
    process.stdout.write(`${JSON.stringify({ id: request.id, result })}\n`)
  } catch (error) {
    process.stdout.write(`${JSON.stringify({ id: request?.id ?? 0, error: String(error?.stack ?? error) })}\n`)
  }
}

await browser?.close()
