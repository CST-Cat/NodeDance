import { api, request, type ContainerTask } from './api'

export interface ComposeProjectRef {
  key: string
  name: string
  workingDirectory: string
  configFiles: string[]
}

export interface ComposeServiceInstance {
  containerId: string
  containerName: string
  state: string
  health: string
}

export interface ComposeService {
  name: string
  instances: ComposeServiceInstance[]
}

export interface ComposeProject {
  ref: ComposeProjectRef
  configAvailable: boolean
  configReason?: string
  dataStale?: boolean
  services: ComposeService[]
}

export interface ComposeProjectsResponse {
  projects: ComposeProject[]
  serverTime: string
  dataStale?: boolean
}

export interface ComposeConfigDocument {
  fileIndex: number
  content: string
  sha256: string
}

export type ComposeOperation = 'start' | 'stop' | 'restart' | 'deploy' | 'config_save'

export const composeApi = {
  projects: (nodeId: string) => request<ComposeProjectsResponse>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/compose/projects`,
  ),
  config: (nodeId: string, projectKey: string, fileIndex: number) => request<ComposeConfigDocument>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/compose/projects/${encodeURIComponent(projectKey)}/config/${fileIndex}`,
  ),
  validateConfig: (nodeId: string, projectKey: string, fileIndex: number, content: string) => request<{ valid: boolean }>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/compose/projects/${encodeURIComponent(projectKey)}/config/${fileIndex}/validate`,
    { method: 'POST', body: JSON.stringify({ content }) }, true,
  ),
  createTask: (nodeId: string, projectKey: string, payload: {
    action: ComposeOperation
    fileIndex?: number
    content?: string
    baseSha256?: string
  }, idempotencyKey: string) => request<{ taskId: string; status: ContainerTask['status'] }>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/compose/projects/${encodeURIComponent(projectKey)}/tasks`,
    { method: 'POST', headers: { 'Idempotency-Key': idempotencyKey }, body: JSON.stringify(payload) }, true,
  ),
  task: (nodeId: string, taskId: string) => api.nodeTask(nodeId, taskId),
}
