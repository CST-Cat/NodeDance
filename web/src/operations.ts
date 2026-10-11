// The connected Core owns this contract. All Web transports resolve names here.
export interface BodySchema {
  type?: string
  properties?: Record<string, BodySchema>
  required?: string[]
  enum?: string[]
  items?: BodySchema
  writeOnly?: boolean
}
export interface CoreOperation {
  name: string
  description: string
  method: string
  path: string
  parameters: string[]
  query: string[]
  bodySchema?: BodySchema
  transport: 'json' | 'download' | 'upload' | 'multipart' | 'websocket' | 'terminal'
  requiresIdempotencyKey?: boolean
}
export interface OperationInput {
  params?: Record<string, string>
  query?: Record<string, string>
}
export interface OperationCatalog { version: number; operations: CoreOperation[] }
let catalogPromise: Promise<OperationCatalog> | undefined

export function operationCatalog(): Promise<OperationCatalog> {
  if (!catalogPromise) {
    catalogPromise = fetch('/api/v1/operations', { credentials: 'include', cache: 'no-store' }).then(async response => {
      if (!response.ok) throw new Error(`Operation catalog: HTTP ${response.status}`)
      const catalog = await response.json() as OperationCatalog
      if (catalog.version !== 1 || !Array.isArray(catalog.operations) || !catalog.operations.length) throw new Error('Invalid Core operation catalog')
      const names = new Set<string>()
      for (const operation of catalog.operations) {
        if (!operation.name || names.has(operation.name) || !/^\/(api|ws)\//.test(operation.path) || !/^(GET|HEAD|POST|PUT|PATCH|DELETE)$/.test(operation.method) || !['json', 'download', 'upload', 'multipart', 'websocket', 'terminal'].includes(operation.transport)) throw new Error('Invalid Core operation definition')
        names.add(operation.name)
      }
      return catalog
    }).catch(error => { catalogPromise = undefined; throw error })
  }
  return catalogPromise
}

export function operationPath(operation: CoreOperation, input: OperationInput = {}): string {
  const params = input.params ?? {}
  for (const name of Object.keys(params)) if (!operation.parameters.includes(name)) throw new Error(`Unknown path parameter: ${name}`)
  let path = operation.path
  for (const name of operation.parameters) {
    const value = params[name]
    if (!value || value === '.' || value === '..' || /[\/\\\0\r\n]/.test(value)) throw new Error(`Missing or invalid path parameter: ${name}`)
    path = path.replace(`{${name}}`, encodeURIComponent(value))
  }
  const query = new URLSearchParams()
  for (const [name, value] of Object.entries(input.query ?? {})) {
    if (!operation.query.includes(name)) throw new Error(`Unknown query parameter: ${name}`)
    query.set(name, value)
  }
  return path + (query.size ? '?' + query.toString() : '')
}

export async function resolveOperation(name: string, input: OperationInput = {}) {
  const catalog = await operationCatalog()
  const operation = catalog.operations.find(item => item.name === name)
  if (!operation) throw new Error(`Core does not expose operation: ${name}`)
  return { operation, path: operationPath(operation, input) }
}

export async function operationURL(name: string, input: OperationInput = {}) {
  const { operation, path } = await resolveOperation(name, input)
  const url = new URL(path, window.location.origin)
  if (operation.transport === 'websocket') url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  return url
}
