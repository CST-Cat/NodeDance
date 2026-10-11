import { operationURL } from './operations'
export type ContainerStreamKind = 'logs' | 'stats'

export interface ContainerStreamEnvelope<T = unknown> {
  version: number
  type: string
  generation: number
  sequence: number
  requestId: string
  payload: T
}

export interface ContainerLogFrame {
  containerId: string
  channel: 'stdout' | 'stderr'
  data: string
}

export interface ContainerStreamMetric<T> {
  state: 'ok' | 'unknown'
  value?: T
  reason?: string
}

export interface ContainerStreamSnapshot {
  container_id: string
  observed_at?: string
  cpu_percent: ContainerStreamMetric<number>
  memory: ContainerStreamMetric<{ used_bytes: number; cache_bytes: number; limit_bytes: number; used_percent: number }>
  network: ContainerStreamMetric<{ received_bytes_per_second: number; sent_bytes_per_second: number }>
  block_io: ContainerStreamMetric<{ read_bytes_per_second: number; write_bytes_per_second: number }>
}

export interface ContainerStatsFrame {
  snapshot: ContainerStreamSnapshot
}

export interface ContainerStreamReady {
  kind: ContainerStreamKind
  containerId: string
}

export interface ContainerStreamError {
  code: string
}

export async function createContainerStreamSocket(kind: ContainerStreamKind, nodeId: string, containerId: string, tail = '200'): Promise<WebSocket> {
  const url = await operationURL(`containers.${kind}`, { query: { nodeId, containerId, ...(kind === 'logs' ? { tail } : {}) } })
  return new WebSocket(url)
}

export function decodeContainerLogData(encoded: string, decoder?: TextDecoder): string {
  const binary = window.atob(encoded)
  const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0))
  return decoder ? decoder.decode(bytes, { stream: true }) : new TextDecoder().decode(bytes)
}
