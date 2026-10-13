import { api, operationRequest, type ContainerTask } from './api'

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

export interface ComposeCreateRequest {
  name: string
  workingDirectory: string
  content: string
}

export const composeApi = {
  projects: (nodeId: string) => operationRequest<ComposeProjectsResponse>("compose.list", { params: { nodeId: String(nodeId) } }),
  createProject: (nodeId: string, payload: ComposeCreateRequest) => operationRequest<{ taskId: string; status: ContainerTask['status'] }>("compose.create", { params: { nodeId: String(nodeId) } }, { body: JSON.stringify(payload) }),
  config: (nodeId: string, projectKey: string, fileIndex: number) => operationRequest<ComposeConfigDocument>("compose.config", { params: { nodeId: String(nodeId), projectKey: String(projectKey), fileIndex: String(fileIndex) } }),
  validateConfig: (nodeId: string, projectKey: string, fileIndex: number, content: string) => operationRequest<{ valid: boolean }>("compose.validate", { params: { nodeId: String(nodeId), projectKey: String(projectKey), fileIndex: String(fileIndex) } }, { body: JSON.stringify({ content }) }),
  createTask: (nodeId: string, projectKey: string, payload: {
    action: ComposeOperation
    fileIndex?: number
    content?: string
    baseSha256?: string
  }) => operationRequest<{ taskId: string; status: ContainerTask['status'] }>("compose.action", { params: { nodeId: String(nodeId), projectKey: String(projectKey) } }, { body: JSON.stringify(payload) }),
  task: (nodeId: string, taskId: string) => api.nodeTask(nodeId, taskId),
}
