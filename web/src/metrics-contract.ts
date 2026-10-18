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
    host?: HostMetrics
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
      detailsTruncated?: boolean
      truncationReason?: string
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
      detailsTruncated?: boolean
      truncationReason?: string
    }
    uptime: Metric<{
      seconds: number
      bootId: string
      bootTimeUnix?: number
    }>
  }
}

export interface Observability {values:Record<string,Metric<number>>;entities:Array<{kind:string;id:string;name:string;values:Record<string,Metric<number>>}>;truncated:boolean;slowIntervalSeconds:number}
export interface HostMetrics {
 observability?:Observability

 diskIO?:Metric<{readBytesPerSecond:number;writeBytesPerSecond:number}>
 memoryMode?:string;fastIntervalSeconds:number; diskIntervalSeconds:number
 swap:Metric<{totalBytes:number;usedBytes:number;usedPercent:number}>
 load:Metric<{one:number;five:number;fifteen:number}>
 processes:Metric<number>;sockets:Metric<{tcp:number;udp:number}>
 cpu:Metric<{model:string;physicalCores:number}>;virtualization:Metric<string>
 memory:Metric<{rawUsedBytes?:number;availableBytes?:number;freeBytes:number;cachedBytes:number;bufferedBytes:number;sharedBytes:number}>
 addresses:Metric<Array<{interface:string;address:string;family:number}>>
 publicIpv4:Metric<string>;publicIpv6:Metric<string>
 gpus:Metric<Array<{id:string;name:string;utilization?:number;memoryTotalBytes?:number;memoryUsedBytes?:number;temperatureCelsius?:number}>>
 traffic:Metric<{receivedBytes:number;sentBytes:number;periodReceivedBytes:number;periodSentBytes:number;periodStart:string;coverageStart:string;lastSample:string;gaps:number}>
}
