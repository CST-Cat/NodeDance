// The connected Core owns this contract. All Web transports resolve names here.
export interface BodySchema {
  description?: string
  anyOf?: BodySchema[]
  format?: string
  type?: string
  properties?: Record<string, BodySchema>
  required?: string[]
  enum?: Array<string | number | boolean>
  items?: BodySchema
  writeOnly?: boolean
  default?: unknown
  nullable?: boolean
  minimum?: number
  maximum?: number
  maximumInteger?: string
  minimumInteger?: string
  minLength?: number
  maxLength?: number
  maxBytes?: number
  minBytes?: number
  minItems?: number
  maxItems?: number
  additionalProperties?: boolean | BodySchema
}
export interface CoreOperation {
  requiredRole: "public" | "viewer" | "administrator"
  name: string
  description: string
  method: string
  path: string
  parameters: string[]
  query: string[]
  bodySchema?: BodySchema
  transport: 'json' | 'download' | 'upload' | 'multipart' | 'websocket' | 'terminal'
  requiresIdempotencyKey?: boolean
  headers?: Record<string, BodySchema & { description?: string }>
  upload?: { field?: string; contentType: string; maxBytes?: number }
  maxBodyBytes?: number
  maxResponseBytes?: number
  responseSchema?: BodySchema
  completion?: { operation: string; params: Record<string,string>; query?: Record<string,string>; statusPath: string; terminalStatuses: string[]; successStatuses: string[] }
  duplex?: boolean
}

export function schemaHasSecrets(schema?: BodySchema): boolean {
  return !!schema && (!!schema.writeOnly || Object.values(schema.properties ?? {}).some(schemaHasSecrets) || schemaHasSecrets(schema.items))
}

// Presence is different from a nonempty string. Untouched optional fields are
// omitted; explicitly cleared fields and required strings retain "".
// Decode numeric tokens before JS can round them. Big integers stay bigint in
// typed contract fields and are written as JSON numbers, never quoted strings.
export function parseExactJSON(raw: string): unknown {
  const decoded = JSON.parse(raw)
  if (!/\d{16}/.test(raw)) return decoded
  // Scan strings iteratively: a repeated regex over multi-MiB escaped text
  // can overflow the JS regex engine's stack even though JSON.parse accepts it.
  const tokens:string[]=[]
  for(let offset=0;offset<raw.length;){
    const character=raw[offset]!
    if(character.charCodeAt(0)<=32){offset++;continue}
    const start=offset
    if(character==='"'){offset++;while(offset<raw.length){const next=raw[offset++]!;if(next==='\\')offset++;else if(next==='"')break}}
    else if(character==='-'||character>='0'&&character<='9'){offset++;while(offset<raw.length&&/[0-9eE+.\-]/.test(raw[offset]!))offset++}
    else if(character==='t')offset+=4
    else if(character==='f')offset+=5
    else if(character==='n')offset+=4
    else offset++
    tokens.push(raw.slice(start,offset))
  }
  let i=0
  const read=(): unknown=>{
    const token=tokens[i++]!
    if(token==='{'){const value:Record<string,unknown>={};while(tokens[i]!=='}'){const key=JSON.parse(tokens[i++]!);i++;Object.defineProperty(value,key,{value:read(),enumerable:true,writable:true,configurable:true});if(tokens[i]===',')i++}i++;return value}
    if(token==='['){const value:unknown[]=[];while(tokens[i]!==']'){value.push(read());if(tokens[i]===',')i++}i++;return value}
    if(/^-?\d+$/.test(token)){const n=BigInt(token);if(n>BigInt(Number.MAX_SAFE_INTEGER)||n<BigInt(Number.MIN_SAFE_INTEGER))return n}
    return JSON.parse(token)
  }
  return read()
}
export function exactJSON(value: unknown): string {
  if(typeof value==='bigint')return String(value)
  if(Array.isArray(value))return '['+value.map(v=>exactJSON(v ?? null)).join(',')+']'
  if(value && typeof value==='object')return '{'+Object.entries(value).filter(([,v])=>v!==undefined).map(([k,v])=>JSON.stringify(k)+':'+exactJSON(v)).join(',')+'}'
  return JSON.stringify(value)
}
export function decodeContractJSON(raw:string,schema?:BodySchema):unknown {
  if (!/\d{16}/.test(raw)) return JSON.parse(raw)
  const select = (s:BodySchema|undefined,pick:(s:BodySchema)=>BodySchema|undefined):BodySchema|undefined => {
    if (!s?.anyOf) return s && pick(s)
    const choices = s.anyOf.map(variant=>select(variant,pick)).filter((choice):choice is BodySchema=>!!choice)
    return choices.length===1 ? choices[0] : {anyOf:choices}
  }
  const integer = (s?:BodySchema):boolean => s?.type==='integer' || !!s?.anyOf?.some(integer)
  const visit=(value:unknown,s?:BodySchema):unknown=>{
    if(typeof value==='bigint')return integer(s)?value:Number(value)
    if(Array.isArray(value)){const item=select(s,definition=>definition.items);return value.map(v=>visit(v,item))}
    if(value && typeof value==='object')return Object.fromEntries(Object.entries(value).map(([k,v])=>[k,visit(v,select(s,definition=>definition.properties?.[k] ?? (typeof definition.additionalProperties==='object'?definition.additionalProperties:undefined)))]))
    return value
  }
  return visit(parseExactJSON(raw),schema)
}

export function operationBody(operation: CoreOperation, values: Record<string,string>, raw = '', rawMode=false): string | undefined {
  const schema = operation.bodySchema
  if (!schema) return undefined
  if (rawMode || schema.type !== 'object') {
    return raw.trim()
  }
  const encoded: string[] = []
  for (const [key, field] of Object.entries(schema.properties ?? {})) {
    if (!Object.hasOwn(values,key) && !schema.required?.includes(key)) continue
    const value = String(values[key] ?? '')
    if (field.type === 'string') encoded.push(`${JSON.stringify(key)}:${JSON.stringify(value)}`)
    else if (value === '' && !schema.required?.includes(key)) continue
    else encoded.push(`${JSON.stringify(key)}:${exactJSON(parseExactJSON(value))}`)
  }
  return `{${encoded.join(',')}}`
}

export function operationUpload(operation: CoreOperation, file: File): BodyInit {
  if (!operation.upload) throw new Error('Core omitted upload contract')
  if (operation.transport === 'upload') return file
  if (!operation.upload.field) throw new Error('Core omitted multipart field')
  const form = new FormData(); form.append(operation.upload.field,file); return form
}

export function completionInput(operation: CoreOperation, input: OperationInput, body: string | undefined, data: unknown) {
  if (!operation.completion) throw new Error('Core omitted completion contract')
  const root = { params: input.params ?? {}, query: input.query ?? {}, body: body ? JSON.parse(body) : {}, data }
  const at = (reference: string): unknown => reference.split('.').reduce<unknown>((value,key) => value && typeof value==='object' ? (value as Record<string,unknown>)[key] : undefined,root)
  const params: Record<string,string> = {}, query: Record<string,string> = {}
  for (const [key,reference] of Object.entries(operation.completion.params)) { const value=at(reference); if(typeof value!=='string'||!value)throw new Error(`Missing completion reference: ${reference}`); params[key]=value }
  for (const [key,reference] of Object.entries(operation.completion.query ?? {})) { const value=at(reference); if(typeof value==='string'&&value) query[key]=value }
  return { name:operation.completion.operation, input:{params,query} }
}

export function completionStatus(operation: CoreOperation, data: unknown): string {
  const value=operation.completion?.statusPath.split('.').reduce<unknown>((value,key)=>value && typeof value==='object' ? (value as Record<string,unknown>)[key] : undefined,data)
  if(typeof value!=='string'||!value)throw new Error('Core completion response has no status')
  return value
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
