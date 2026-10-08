import { createApp, h, ref } from 'vue'
import MetricsPanel from '../../src/components/MetricsPanel.vue'
import type { MetricsView } from '../../src/metrics-contract'

const fixtureServerTime = '2030-01-02T03:04:05.000Z'

function ageFromQuery(name: string, fallback: number): number {
  const value = Number(new URLSearchParams(window.location.search).get(name))
  return Number.isFinite(value) && value >= 0 ? value : fallback
}

function makeView(fastAgeMillis: number, diskAgeMillis: number, leaseMillis: number): MetricsView {
  const sampledAt = fixtureServerTime
  const known = <T>(value: T, age = fastAgeMillis) => ({ status: 'known' as const, value, reason: '', sampledAt, sampleAgeMillis: age })
  const system = {
    hostname: known('fixture-host'),
    os: known('linux'),
    architecture: known('amd64'),
    platform: known('ubuntu'),
    platformFamily: known('debian'),
    platformVersion: known('24.04'),
    kernelVersion: known('6.8.0'),
  }
  const rate = { receivedBytesPerSecond: 1024, sentBytesPerSecond: 2048 }
  const memory = { totalBytes: 8 * 1024 ** 3, availableBytes: 3 * 1024 ** 3, usedBytes: 5 * 1024 ** 3, usedPercent: 62.5 }
  const memoryState = new URLSearchParams(window.location.search).get('memoryState')
  const memoryMetric = memoryState === 'unknown' || memoryState === 'error'
    ? { status: memoryState, reason: 'permission_denied', sampledAt, sampleAgeMillis: fastAgeMillis }
    : known(memory)
  const diskUsage = { totalBytes: 100 * 1024 ** 3, availableBytes: 40 * 1024 ** 3, usedBytes: 60 * 1024 ** 3, usedPercent: 60 }
  const diskMetric = { status: 'known' as const, value: diskUsage, reason: '', sampledAt, sampleAgeMillis: diskAgeMillis }
  const uptime = { seconds: 100_000, bootId: 'fixture-boot-id' }
  const serverDate = new Date(fixtureServerTime)
  return {
    agentId: 'fixture-agent',
    nodeId: 'fixture-node',
    generation: 3,
    sequence: 17,
    bootId: uptime.bootId,
    collectedAt: sampledAt,
    receivedAt: sampledAt,
    clockOffsetMs: 120,
    nodeStatus: 'online',
    activeGeneration: 3,
    serverTime: sampledAt,
    leaseValidUntil: new Date(serverDate.getTime() + leaseMillis).toISOString(),
    metrics: {
      system,
      cpu: {
        usagePercent: known(12.5),
        logicalCores: known(4),
      },
      memory: memoryMetric,
      network: {
        summary: known(rate),
        interfaces: [{ name: 'eth0', up: true, includedInSummary: true, rate: known(rate) }],
      },
      disk: {
        status: 'known',
        reason: '',
        sampledAt,
        sampleAgeMillis: diskAgeMillis,
        mounts: [{ device: '/dev/vda1', mountpoint: '/', filesystem: 'ext4', usage: diskMetric }],
      },
      uptime: known(uptime),
    },
  }
}

const view = ref(makeView(ageFromQuery('fastAge', 0), ageFromQuery('diskAge', 0), ageFromQuery('leaseMs', 30_000)))

declare global {
  interface Window {
    updateMetricsView: () => void
  }
}

window.updateMetricsView = () => {
  view.value = makeView(0, 0, 30_000)
}

createApp({
  setup: () => () => h(MetricsPanel, { view: view.value }),
}).mount('#app')
