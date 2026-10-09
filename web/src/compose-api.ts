import { request } from './api'

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

export const composeApi = {
  projects: (nodeId: string) => request<ComposeProjectsResponse>(
    `/api/v1/nodes/${encodeURIComponent(nodeId)}/compose/projects`,
  ),
}
