export type MetricStatus = 'known' | 'unknown' | 'error' | 'stale'

export interface Metric<T> {
  status: MetricStatus
  value?: T | null
  reason?: string
  sampledAt: string
  sampleAgeMillis: number
}

export interface MetricsView {
  agentId: string
  nodeId: string
  generation: number
  sequence: number
  bootId?: string
  previousBootId?: string
  bootIdChangedAt?: string
  collectedAt: string
  receivedAt: string
  clockOffsetMs: number
  nodeStatus: 'online' | 'offline'
  activeGeneration: number
  serverTime: string
  leaseValidUntil: string
  metrics: {
    system: {
      hostname: Metric<string>
      os: Metric<string>
      architecture: Metric<string>
      platform: Metric<string>
      platformFamily: Metric<string>
      platformVersion: Metric<string>
      kernelVersion: Metric<string>
    }
    cpu: {
      usagePercent: Metric<number>
      logicalCores: Metric<number>
    }
    memory: Metric<{
      totalBytes: number
      availableBytes: number
      usedBytes: number
      usedPercent: number
    }>
    network: {
      summary: Metric<{ receivedBytesPerSecond: number; sentBytesPerSecond: number }>
      interfaces: Array<{
        name: string
        up?: boolean
        includedInSummary: boolean
        summaryReason?: string
        rate: Metric<{ receivedBytesPerSecond: number; sentBytesPerSecond: number }>
      }>
    }
    disk: {
      status: MetricStatus
      reason?: string
      sampledAt: string
      sampleAgeMillis: number
      mounts: Array<{
        device: string
        mountpoint: string
        filesystem: string
        usage: Metric<{
          totalBytes: number
          availableBytes: number
          usedBytes: number
          usedPercent: number
        }>
      }>
    }
    uptime: Metric<{
      seconds: number
      bootId: string
      bootTimeUnix?: number
    }>
  }
}
