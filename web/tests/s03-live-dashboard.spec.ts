import { expect, test, type Page, type TestInfo } from '@playwright/test'
import { mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import type { MetricsView } from '../src/metrics-contract'

interface GuestPhase {
  phase?: string
  events?: string[]
  eventValues?: Record<string, string>
  updatedAtUTC?: string
  guestFinished?: boolean
  guestRunnerFinished?: boolean
  guestRunnerExitCode?: number
}

interface DashboardFrame {
  type?: string
  nodeId?: string
  metrics?: MetricsView
  state?: { status?: string; generation?: number; serverTime?: string; reason?: string }
  inventory?: {
    agentOnline?: boolean
    activeGeneration?: number
    dockerAvailability?: string
    dataStale?: boolean
    staleReason?: string
    dockerSnapshotFresh?: boolean
  }
}

interface CapturedFrame {
  capturedAtUTC: string
  capturedAtUnixMs: number
  phase: GuestPhase
  frame: DashboardFrame
}

interface DOMSample {
  capturedAtUTC: string
  capturedAtUnixMs: number
  phase: GuestPhase
  nodeStatus: string
  generation: number
  activeGeneration: number
  sequence: number
  bootID: string
  clockOffset: string
  cpuStatus: string
  cpuValue: string
  memoryStatus: string
  memoryValue: string
  memoryReason: string
  networkStatus: string
  networkReason: string
  interfaces: Array<{ name: string; status: string; reason: string; text: string }>
  mounts: string[]
  collectedAtText: string
  receivedAtText: string
  dockerAvailability: string
  dockerStale: string
  dockerReason: string
}

function readGuestPhase(file: string): GuestPhase {
  try {
    return JSON.parse(readFileSync(file, 'utf8')) as GuestPhase
  } catch {
    return { phase: 'before_agent', events: [], eventValues: {}, guestFinished: false }
  }
}

function writePrivateJSON(file: string, value: unknown) {
  mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 })
  const temporary = `${file}.${process.pid}.tmp`
  writeFileSync(temporary, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 })
  renameSync(temporary, file)
}

function rawEvent(phase: GuestPhase, name: string): string[] {
  const value = phase.eventValues?.[name]
  return typeof value === 'string' ? value.trim().split(/\s+/) : []
}

function percentile(values: number[], quantile: number): number {
  const sorted = [...values].sort((left, right) => left - right)
  return sorted[Math.max(0, Math.ceil(quantile * sorted.length) - 1)] ?? 0
}

async function installDOMRecorder(page: Page) {
  await page.evaluate(() => {
    const target = window as typeof window & { __s03DOMSamples?: unknown[]; __s03DOMLast?: string }
    target.__s03DOMSamples = []
    target.__s03DOMLast = ''
    const record = () => {
      const panel = document.querySelector<HTMLElement>('.metrics-panel')
      const identity = panel?.querySelector<HTMLElement>('[data-testid="sample-identity"]')
      if (!panel || !identity) return
      const generation = Number(identity.dataset.generation || 0)
      const activeGeneration = Number(identity.dataset.activeGeneration || 0)
      const sequence = Number(identity.dataset.sequence || 0)
      const stateSignature = [
        panel.dataset.nodeStatus ?? '',
        panel.querySelector<HTMLElement>('[data-testid="cpu-card"] .status-pill')?.dataset.status ?? '',
        panel.querySelector<HTMLElement>('[data-testid="memory-card"] .status-pill')?.dataset.status ?? '',
        panel.querySelector<HTMLElement>('[data-testid="network-card"] .status-pill')?.dataset.status ?? '',
        panel.querySelector<HTMLElement>('[data-testid="clock-offset"]')?.textContent?.trim() ?? '',
        document.querySelector<HTMLElement>('[data-testid="docker-inventory"] .docker-state')?.dataset.available ?? '',
        document.querySelector<HTMLElement>('[data-testid="docker-inventory"] .docker-state')?.dataset.stale ?? '',
        document.querySelector('[data-testid="docker-health-reason"]')?.textContent?.trim() ?? '',
      ].join(':')
      const key = `${generation}:${activeGeneration}:${sequence}:${stateSignature}`
      if (target.__s03DOMLast === key) return
      target.__s03DOMLast = key
      const text = (selector: string) => panel.querySelector(selector)?.textContent?.trim() ?? ''
      const status = (selector: string) => panel.querySelector<HTMLElement>(selector)?.dataset.status ?? ''
      const interfaces = Array.from(panel.querySelectorAll<HTMLElement>('[data-testid="network-interface"]')).map((row) => {
        const name = row.dataset.interfaceName ?? ''
        return {
          name,
          status: row.querySelector<HTMLElement>('[data-testid^="interface-rate-status-"]')?.dataset.status ?? '',
          reason: row.querySelector('[data-testid^="interface-rate-reason-"]')?.textContent?.trim() ?? '',
          text: row.textContent?.trim() ?? '',
        }
      })
      const mounts = Array.from(panel.querySelectorAll<HTMLElement>('.mount-row .mount-title strong')).map((item) => item.textContent?.trim() ?? '')
      const footer = Array.from(panel.querySelectorAll<HTMLElement>('.metrics-footer > div'))
      const footerText = (label: string) => footer.find((item) => item.querySelector('span')?.textContent?.trim() === label)?.querySelector('strong')?.textContent?.trim() ?? ''
      const dockerState = document.querySelector<HTMLElement>('[data-testid="docker-inventory"] .docker-state')
      ;(target.__s03DOMSamples ?? []).push({
        capturedAtUTC: new Date().toISOString(),
        capturedAtUnixMs: Date.now(),
        nodeStatus: panel.dataset.nodeStatus ?? '',
        generation,
        activeGeneration,
        sequence,
        bootID: text('[data-testid="boot-id"]'),
        clockOffset: text('[data-testid="clock-offset"]'),
        cpuStatus: status('[data-testid="cpu-card"] .status-pill'),
        cpuValue: text('[data-testid="cpu-value"]'),
        memoryStatus: status('[data-testid="memory-card"] .status-pill'),
        memoryValue: text('[data-testid="memory-value"]'),
        memoryReason: text('[data-testid="memory-reason"]'),
        networkStatus: status('[data-testid="network-card"] .status-pill'),
        networkReason: text('[data-testid="network-reason"]'),
        interfaces,
        mounts,
        collectedAtText: footerText('Agent 采集'),
        receivedAtText: footerText('Core 接收'),
        dockerAvailability: dockerState?.dataset.available ?? '',
        dockerStale: dockerState?.dataset.stale ?? '',
        dockerReason: document.querySelector('[data-testid="docker-health-reason"]')?.textContent?.trim() ?? '',
      })
    }
    const observer = new MutationObserver(record)
    observer.observe(document.body, { subtree: true, childList: true, characterData: true, attributes: true })
    record()
  })
}

async function sampleDOM(page: Page): Promise<Array<Omit<DOMSample, 'phase'>>> {
  return page.evaluate((guestPhase) => {
    const target = window as typeof window & { __s03DOMSamples?: Array<Omit<DOMSample, 'phase'>> }
    return target.__s03DOMSamples ?? []
  })
}

test('real guest Agent metrics update the authenticated dashboard through each S03 fault phase', async ({ page }, testInfo: TestInfo) => {
  test.setTimeout(620_000)
  const phaseFile = process.env.NODEDANCE_S03_PHASE_FILE
  const evidencePrefix = process.env.NODEDANCE_S03_BROWSER_EVIDENCE
  const readyPrefix = process.env.NODEDANCE_S03_BROWSER_READY_PREFIX
  if (!phaseFile || !evidencePrefix || !readyPrefix) throw new Error('live S03 browser paths are required')

  const engine = testInfo.project.name
  const evidencePath = `${evidencePrefix}-${engine}.json`
  const readyPath = `${readyPrefix}-${engine}.json`
  const frames: CapturedFrame[] = []
  const observedDOMSamples: DOMSample[] = []
  let observedDOMCount = 0
  let guestPhase = readGuestPhase(phaseFile)
  let artifactStatus: 'PASS' | 'FAIL' | 'NOT_READY' = 'FAIL'
  let artifactReason = ''

  page.on('websocket', (socket) => {
    if (!socket.url().includes('/ws/v1/dashboard')) return
    socket.on('framereceived', (event) => {
      try {
        const frame = JSON.parse(String(event.payload)) as DashboardFrame
        if (frame.type === 'node_metrics' || frame.type === 'node_status' || frame.type === 'node_containers') {
          frames.push({
            capturedAtUTC: new Date().toISOString(),
            capturedAtUnixMs: Date.now(),
            phase: readGuestPhase(phaseFile),
            frame,
          })
        }
      } catch {
        // Non-JSON websocket frames are not dashboard measurements.
      }
    })
  })

  try {
    await page.goto('/')
    await expect(page.getByRole('heading', { name: '管理员登录' })).toBeVisible()
    await page.getByLabel('管理员密码').fill('S03-guest-test-password')
    await page.getByRole('button', { name: '登录控制台' }).click()
    await expect(page.getByRole('button', { name: '节点监控' })).toBeVisible()
    await page.getByRole('button', { name: '节点监控' }).click()
    await expect(page.getByRole('heading', { name: '节点指标.' })).toBeVisible()
    expect(new URL(page.url()).origin, 'browser is on the exact same-origin loopback configured on Core').toBe('http://127.0.0.1:4187')
    await expect(page.locator('.node-option')).toHaveCount(1)
    await expect(page.getByTestId('metrics-waiting')).toContainText('等待 Agent')
    await installDOMRecorder(page)

    mkdirSync(path.dirname(readyPath), { recursive: true, mode: 0o700 })
    writeFileSync(readyPath, `${JSON.stringify({ engine, readyAtUTC: new Date().toISOString(), origin: new URL(page.url()).origin })}\n`, { mode: 0o600 })

    const startedAt = Date.now()
    let finishedAt = 0
    while (Date.now() - startedAt < 570_000) {
      guestPhase = readGuestPhase(phaseFile)
      const currentDOMSamples = await sampleDOM(page)
      for (const sample of currentDOMSamples.slice(observedDOMCount)) {
        observedDOMSamples.push({ ...sample, phase: guestPhase })
      }
      observedDOMCount = currentDOMSamples.length
      if (guestPhase.guestRunnerFinished) {
        if (!finishedAt) finishedAt = Date.now()
        if (Date.now() - finishedAt >= 3_000) break
      }
      await page.waitForTimeout(250)
    }

    guestPhase = readGuestPhase(phaseFile)
    const rawDOMSamples = await sampleDOM(page)
    for (const sample of rawDOMSamples.slice(observedDOMCount)) {
      observedDOMSamples.push({ ...sample, phase: guestPhase })
    }
    const domSamples = observedDOMSamples
    if (!guestPhase.guestRunnerFinished) {
      artifactStatus = 'NOT_READY'
      artifactReason = 'the live guest runner did not finish within the browser observation bound'
      return
    }
    if (!(guestPhase.events ?? []).includes('DONE') || !(guestPhase.events ?? []).includes('REBOOT_POST')) {
      artifactStatus = 'NOT_READY'
      artifactReason = `the guest did not emit a complete load/fault/clock/reboot event trace (runner_exit=${guestPhase.guestRunnerExitCode ?? 'unknown'})`
      return
    }

    const metricFramesCaptured = frames.filter((item) => item.frame.type === 'node_metrics' && item.frame.metrics)
    const uniqueBySample = new Map<string, CapturedFrame>()
    for (const item of metricFramesCaptured) {
      const view = item.frame.metrics!
      const key = `${item.frame.nodeId}:${view.generation}:${view.activeGeneration}:${view.sequence}`
      if (!uniqueBySample.has(key)) uniqueBySample.set(key, item)
    }
    const metricFrames = [...uniqueBySample.values()].sort((left, right) => left.capturedAtUnixMs - right.capturedAtUnixMs)
    expect(metricFrames.length, 'dashboard WSS frames from the enrolled real guest').toBeGreaterThanOrEqual(20)
    expect(new Set(metricFrames.map((item) => item.frame.nodeId)).size, 'single enrolled guest node').toBe(1)

    const firstSample = metricFrames.find((item) => item.frame.metrics?.generation === 1 && item.frame.metrics.sequence === 1)?.frame.metrics
    expect(firstSample?.metrics.cpu.usagePercent.status, 'initial CPU sample is explicitly unknown').toBe('unknown')
    expect(firstSample?.metrics.cpu.usagePercent.value ?? null, 'unknown CPU is not reported as zero').toBeNull()
    expect(firstSample?.metrics.network.summary.status, 'initial network rate is explicitly unknown').toBe('unknown')
    expect(firstSample?.metrics.network.summary.value ?? null, 'unknown network rate is not reported as zero').toBeNull()

    const knownFrames = metricFrames.filter((item) => item.frame.metrics?.metrics.cpu.usagePercent.status === 'known' && item.frame.metrics.metrics.memory.status === 'known')
    expect(knownFrames.length, 'real CPU and memory samples reach WSS').toBeGreaterThan(4)
    expect(domSamples.some((item) => item.cpuStatus === 'known' && item.memoryStatus === 'known'), 'known sample is rendered into the DOM').toBe(true)

    const dockerFrames = frames.filter((item) => item.frame.type === 'node_containers' && item.frame.inventory)
    const stalledDockerFrames = dockerFrames.filter((item) =>
      ['docker_query_stall_active', 'docker_query_stalled'].includes(item.phase.phase ?? '') &&
      item.frame.inventory?.agentOnline === true && item.frame.inventory.dataStale === true &&
      item.frame.inventory.staleReason?.includes('Docker Engine request failed'))
    expect(stalledDockerFrames.some((item) => item.frame.inventory?.agentOnline === true && item.frame.inventory.dataStale === true),
      'Core pushes a stale Docker inventory while the authenticated Agent remains online').toBe(true)
    expect(domSamples.some((item) => item.nodeStatus === 'online' && item.dockerStale === 'true' &&
      item.dockerReason.includes('Docker Engine request failed') && stalledDockerFrames.some((frame) =>
        Math.abs(frame.capturedAtUnixMs - item.capturedAtUnixMs) <= 5_000)),
    'the selected node dashboard renders the matching stale Docker failure while host metrics remain online').toBe(true)
    const activeStallMetrics = metricFrames.filter((item) => item.phase.phase === 'docker_query_stall_active' &&
      item.frame.metrics?.nodeStatus === 'online' && item.frame.metrics.generation === item.frame.metrics.activeGeneration &&
      item.frame.metrics.metrics.cpu.usagePercent.status === 'known')
    const observedStallMetrics = metricFrames.filter((item) => item.phase.phase === 'docker_query_stalled' &&
      item.frame.metrics?.nodeStatus === 'online' && item.frame.metrics.generation === item.frame.metrics.activeGeneration &&
      item.frame.metrics.metrics.cpu.usagePercent.status === 'known')
    expect(activeStallMetrics.length, 'known host samples continue while the SDK list request is blocked').toBeGreaterThan(0)
    expect(new Set(observedStallMetrics.map((item) => item.frame.metrics!.sequence)).size,
      'host sample sequences continue after the Docker query cancellation').toBeGreaterThanOrEqual(2)
    expect(Math.max(...observedStallMetrics.map((item) => item.frame.metrics!.sequence)),
      'post-cancel host samples advance beyond the blocked interval').toBeGreaterThan(
        Math.max(...activeStallMetrics.map((item) => item.frame.metrics!.sequence)))
    const stallLeaseDeadlines = frames.filter((item) => item.frame.type === 'node_metrics' &&
      ['docker_query_stall_active', 'docker_query_stalled'].includes(item.phase.phase) &&
      item.frame.metrics?.nodeStatus === 'online' && item.frame.metrics.generation === item.frame.metrics.activeGeneration)
      .map((item) => Date.parse(item.frame.metrics?.leaseValidUntil ?? ''))
      .filter((value) => Number.isFinite(value))
    expect(stallLeaseDeadlines.length).toBeGreaterThan(1)
    expect(Math.max(...stallLeaseDeadlines), 'Core heartbeat lease continues to renew during the Docker stall')
      .toBeGreaterThan(Math.min(...stallLeaseDeadlines))
    const interfaceNames = rawEvent(guestPhase, 'AGENT_NICS')
    expect(interfaceNames).toHaveLength(2)
    for (const name of interfaceNames) {
      expect(domSamples.some((item) => item.interfaces.some((iface) => iface.name === name)), `the live per-interface row ${name} is rendered`).toBe(true)
    }
    expect(domSamples.some((item) => item.mounts.includes('/mnt/second')), 'the guest tmpfs mount is rendered').toBe(true)

    const loadFrames = metricFrames.filter((item) => item.phase.phase === 'controlled_load').map((item) => item.frame.metrics!)
    const baselineFrames = metricFrames.filter((item) => ['agent_initial', 'docker_query_stalled'].includes(item.phase.phase ?? ''))
      .map((item) => item.frame.metrics!)
    const recoveryFrames = metricFrames.filter((item) => item.phase.phase === 'load_recovery').map((item) => item.frame.metrics!)
    const median = (values: number[]) => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)]
    const cpuValues = (values: MetricsView[]) => values.flatMap((item) => item.metrics.cpu.usagePercent.value == null ? [] : [item.metrics.cpu.usagePercent.value])
    const memoryValues = (values: MetricsView[]) => values.flatMap((item) => item.metrics.memory.value == null ? [] : [item.metrics.memory.value.usedBytes])
    const baselineCPU = cpuValues(baselineFrames)
    const duringCPU = cpuValues(loadFrames)
    const recoveredCPU = cpuValues(recoveryFrames)
    const baselineMemory = memoryValues(baselineFrames)
    const duringMemory = memoryValues(loadFrames)
    const recoveredMemory = memoryValues(recoveryFrames)
    expect(baselineCPU.length).toBeGreaterThan(0)
    expect(duringCPU.length).toBeGreaterThan(0)
    expect(recoveredCPU.length).toBeGreaterThan(0)
    expect(baselineMemory.length).toBeGreaterThan(0)
    expect(duringMemory.length).toBeGreaterThan(0)
    expect(recoveredMemory.length).toBeGreaterThan(0)
    const loadStats = {
      baselineCPUMedianPercent: median(baselineCPU),
      duringCPUPeakPercent: Math.max(...duringCPU),
      afterReleaseCPUMedianPercent: median(recoveredCPU),
      baselineUsedBytesMedian: median(baselineMemory),
      duringUsedBytesPeak: Math.max(...duringMemory),
      afterReleaseUsedBytesMedian: median(recoveredMemory),
    }
    expect(loadStats.duringCPUPeakPercent, 'guest load CPU rise is visible in WSS').toBeGreaterThanOrEqual(loadStats.baselineCPUMedianPercent + 20)
    expect(loadStats.duringUsedBytesPeak, 'guest resident memory rise is visible in WSS').toBeGreaterThanOrEqual(loadStats.baselineUsedBytesMedian + 128 * 1024 * 1024)
    expect(loadStats.afterReleaseCPUMedianPercent, 'guest CPU returns after load release').toBeLessThanOrEqual(loadStats.baselineCPUMedianPercent + 15)
    expect(loadStats.afterReleaseUsedBytesMedian, 'guest RSS returns after load release').toBeLessThanOrEqual(loadStats.baselineUsedBytesMedian + 96 * 1024 * 1024)
    expect(domSamples.some((item) => item.phase.phase === 'controlled_load' && Number.parseFloat(item.cpuValue) >= loadStats.baselineCPUMedianPercent + 20), 'the dashboard visibly renders the actual CPU load').toBe(true)

    const replacement = rawEvent(guestPhase, 'AGENT_INTERFACE_REPLACED')
    expect(replacement.length).toBe(5)
    const testInterface = replacement[0]
    const replacementFrames = metricFrames.filter((item) => item.phase.phase === 'interface_replacement')
    const interfaceChanged = replacementFrames.some((item) => {
      const iface = item.frame.metrics?.metrics.network.interfaces.find((value) => value.name === testInterface)
      return iface && ['unknown', 'stale'].includes(iface.rate.status) && iface.rate.reason?.includes('interface_changed')
    })
    expect(interfaceChanged, 'same-ifindex MAC replacement is unknown with a reason before rates resume').toBe(true)
    expect(domSamples.some((item) => item.phase.phase === 'interface_replacement' && item.interfaces.some((iface) => iface.name === testInterface && ['unknown', 'stale'].includes(iface.status) && iface.reason.includes('interface_changed'))), 'interface replacement reason is visible in the dashboard').toBe(true)
    expect(metricFrames.some((item) => ['interface_replacement_recovered', 'permission_fault'].includes(item.phase.phase) &&
      item.frame.metrics?.metrics.network.interfaces.some((iface) => iface.name === testInterface && iface.rate.status === 'known')),
    'interface rate returns to known after identity warm-up').toBe(true)

    const permissionFrameIndex = metricFrames.findIndex((item) => item.phase.phase === 'permission_fault' &&
      item.frame.metrics?.metrics.memory.status === 'stale' && item.frame.metrics.metrics.memory.reason?.includes('permission_denied') &&
      item.frame.metrics.metrics.cpu.usagePercent.status === 'known' && item.frame.metrics.metrics.network.summary.status === 'known')
    const permissionMemory = permissionFrameIndex >= 0 ? metricFrames[permissionFrameIndex].frame.metrics : undefined
    expect(permissionMemory, 'permission failure remains explicit and does not become zero').toBeTruthy()
    const priorKnown = metricFrames.slice(0, permissionFrameIndex).reverse().find((item) => item.frame.metrics?.metrics.memory.status === 'known')?.frame.metrics
    expect(permissionMemory!.metrics.memory.value?.usedBytes).toBe(priorKnown?.metrics.memory.value?.usedBytes)
    expect(permissionMemory!.metrics.memory.sampleAgeMillis).toBeGreaterThan(priorKnown?.metrics.memory.sampleAgeMillis ?? -1)
    expect(permissionMemory!.metrics.cpu.usagePercent.status).toBe('known')
    expect(permissionMemory!.metrics.network.summary.status).toBe('known')
    expect(domSamples.some((item) => item.phase.phase === 'permission_fault' && item.memoryStatus === 'stale' && item.memoryReason.includes('permission_denied')), 'permission error reason is visible in the dashboard').toBe(true)
    expect(metricFrames.some((item) => item.phase.phase === 'permission_recovery' && item.frame.metrics?.metrics.memory.status === 'known'), 'memory sampling recovers in WSS').toBe(true)

    const clockPost = rawEvent(guestPhase, 'CLOCK_POST')
    expect(clockPost).toHaveLength(3)
    const clockBootID = clockPost[0]
    const clockPostUnixMs = Number(clockPost[2]) * 1000
    const clockFrames = metricFrames.filter((item) => {
      const view = item.frame.metrics!
      return view.bootId === clockBootID && Date.parse(view.collectedAt) >= clockPostUnixMs && view.clockOffsetMs <= -3_540_000
    })
    expect(clockFrames.length, 'a post-adjustment sample from the unchanged guest boot shows the real clock offset').toBeGreaterThan(0)
    expect(domSamples.some((item) => item.bootID === clockBootID && Number(item.clockOffset.match(/-?[\d,]+/)?.[0]?.replaceAll(',', '')) <= -3_540_000), 'adjusted guest clock offset is visible in the dashboard').toBe(true)

    const offlineDOM = domSamples.find((item) => item.phase.phase === 'network_isolated' && item.nodeStatus === 'offline')
    expect(offlineDOM, 'dashboard marks the guest offline while its primary NIC is isolated').toBeTruthy()
    expect(offlineDOM?.cpuStatus).toBe('stale')
    const beforeRecovery = metricFrames.filter((item) => item.phase.phase === 'post_clock' || item.phase.phase === 'clock_change').map((item) => item.frame.metrics!.activeGeneration)
    const recovered = metricFrames.filter((item) => ['network_recovery', 'network_recovered'].includes(item.phase.phase) &&
      item.frame.metrics?.nodeStatus === 'online' && item.frame.metrics.metrics.cpu.usagePercent.status === 'known')
    expect(recovered.length).toBeGreaterThan(0)
    expect(Math.min(...recovered.map((item) => item.frame.metrics!.activeGeneration))).toBeGreaterThan(Math.max(...beforeRecovery))
    expect(domSamples.some((item) => ['network_recovery', 'network_recovered'].includes(item.phase.phase) && item.nodeStatus === 'online' && item.cpuStatus === 'known'), 'dashboard returns online with new-generation metrics').toBe(true)

    const reboot = rawEvent(guestPhase, 'REBOOT_POST')
    expect(reboot).toHaveLength(6)
    expect(reboot[0]).not.toBe(reboot[3])
    const previousGeneration = Math.max(...metricFrames.filter((item) => ['network_recovery', 'network_recovered', 'pre_reboot'].includes(item.phase.phase) &&
      item.frame.metrics?.nodeStatus === 'online' && item.frame.metrics.generation === item.frame.metrics.activeGeneration)
      .map((item) => item.frame.metrics?.activeGeneration ?? 0))
    const postReboot = metricFrames.filter((item) => item.phase.phase === 'post_reboot' && item.frame.metrics?.bootId === reboot[3] &&
      item.frame.metrics.activeGeneration > previousGeneration)
    expect(postReboot.some((item) => item.frame.metrics?.sequence === 1 && item.frame.metrics.metrics.cpu.usagePercent.status === 'stale' &&
      item.frame.metrics.metrics.cpu.usagePercent.reason?.includes('warming_up')), 'Core retains the last CPU value as stale while the rebooted Agent warms up').toBe(true)
    expect(postReboot.some((item) => item.frame.metrics?.sequence && item.frame.metrics.sequence > 1 && item.frame.metrics.metrics.cpu.usagePercent.status === 'known'), 'post-reboot sampling becomes known').toBe(true)
    expect(domSamples.some((item) => item.phase.phase === 'post_reboot' && item.bootID === reboot[3] && item.activeGeneration > previousGeneration), 'new boot ID and generation reach the dashboard').toBe(true)

    const domByIdentity = new Map<string, DOMSample>()
    for (const item of domSamples) {
      const key = `${item.generation}:${item.activeGeneration}:${item.sequence}`
      if (!domByIdentity.has(key)) domByIdentity.set(key, item)
    }
    const latencyPairs = metricFrames.flatMap((capture) => {
      const view = capture.frame.metrics!
      if (view.nodeStatus !== 'online' || view.generation !== view.activeGeneration) return []
      const dom = domByIdentity.get(`${view.generation}:${view.activeGeneration}:${view.sequence}`)
      const age = view.metrics.cpu.usagePercent.sampleAgeMillis
      if (!dom || !Number.isFinite(age) || age < 0) return []
      const sampleAtCoreMs = Date.parse(view.receivedAt) - age
      const elapsedMs = dom.capturedAtUnixMs - sampleAtCoreMs
      if (!Number.isFinite(elapsedMs) || elapsedMs < 0 || elapsedMs > 120_000) return []
      return [{
        nodeId: view.nodeId, generation: view.generation, activeGeneration: view.activeGeneration,
        sequence: view.sequence, collectedAt: view.collectedAt, receivedAt: view.receivedAt,
        sampleAgeMillis: age, estimatedSampleAtCoreMs: sampleAtCoreMs,
        domAtUTC: dom.capturedAtUTC, domAtUnixMs: dom.capturedAtUnixMs, latencyMillis: elapsedMs,
      }]
    })
    expect(latencyPairs.length, 'sample and rendered DOM are joined by exact generation and sequence').toBeGreaterThanOrEqual(20)
    const latencies = latencyPairs.map((item) => item.latencyMillis)
    const latencyStats = {
      count: latencies.length,
      p50Millis: percentile(latencies, 0.50),
      p95Millis: percentile(latencies, 0.95),
      p99Millis: percentile(latencies, 0.99),
      maxMillis: Math.max(...latencies),
    }
    expect(latencyStats.p95Millis, 'real sampling-to-DOM P95 is at most eight seconds').toBeLessThanOrEqual(8_000)

    const viewportEvidence: Array<{ width: number; scrollWidth: number; clientWidth: number }> = []
    for (const width of [375, 768, 1440]) {
      await page.setViewportSize({ width, height: 900 })
      await page.waitForTimeout(150)
      const measured = await page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth }))
      viewportEvidence.push({ width, ...measured })
      expect(measured.scrollWidth, `dashboard has no horizontal overflow at ${width}px`).toBeLessThanOrEqual(measured.clientWidth)
    }
    artifactStatus = 'PASS'
    artifactReason = ''

    const artifact = {
      schema: 1, stage: 'S03', check: 'real_browser_guest_dashboard', status: artifactStatus,
      engine, origin: new URL(page.url()).origin, phaseFile, guestFinalPhase: guestPhase,
      guestRunnerExitCode: guestPhase.guestRunnerExitCode,
      websocketFrames: frames, distinctSamples: metricFrames.length,
      duplicateSnapshotDeliveries: metricFramesCaptured.length - metricFrames.length,
      domSamples, loadStats, latencyStats, sampleToDOM: latencyPairs, viewportEvidence,
      assertions: {
        realAgentCPUAndMemory: true, initialUnknownNotZero: true, interfacesAndMounts: true,
        controlledLoadAndRecovery: true, interfaceIdentityReset: true, permissionFailureAndRecovery: true,
        dockerStallDoesNotBlockAgent: true, guestClockCorrection: true,
        networkOfflineAndRecovery: true, realReboot: true,
      },
    }
    writePrivateJSON(evidencePath, artifact)
  } catch (error) {
    artifactStatus = 'FAIL'
    artifactReason = error instanceof Error ? error.message : String(error)
    throw error
  } finally {
    if (artifactStatus !== 'PASS') {
      const rawDOMSamples = await sampleDOM(page).catch(() => [])
      const domSamples = rawDOMSamples.map((item) => ({ ...item, phase: readGuestPhase(phaseFile) }))
      writePrivateJSON(evidencePath, {
        schema: 1, stage: 'S03', check: 'real_browser_guest_dashboard', status: artifactStatus,
        reason: artifactReason, engine, phaseFile, guestFinalPhase: readGuestPhase(phaseFile),
        websocketFrames: frames, domSamples,
      })
    }
  }
})
