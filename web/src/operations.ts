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
  JSON.parse(raw) // Validate the complete grammar first.
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
  const visit=(value:unknown,s?:BodySchema):unknown=>{
    if(s?.anyOf)s=s.anyOf.find(variant=>{try{validateSchema(variant,value);return true}catch{return false}})
    if(typeof value==='bigint')return s?.type==='integer'?value:Number(value)
    if(Array.isArray(value))return value.map(v=>visit(v,s?.items))
    if(value && typeof value==='object')return Object.fromEntries(Object.entries(value).map(([k,v])=>[k,visit(v,s?.properties?.[k] ?? (typeof s?.additionalProperties==='object'?s.additionalProperties:undefined))]))
    return value
  }
  return visit(parseExactJSON(raw),schema)
}

export function operationBody(operation: CoreOperation, values: Record<string,string>, raw = '', rawMode=false): string | undefined {
  const schema = operation.bodySchema
  if (!schema) return undefined
  if (rawMode || schema.type !== 'object') {
    const value=parseExactJSON(raw); validateSchema(schema,value); const result=raw.trim()
    if(operation.maxBodyBytes && new TextEncoder().encode(result).length>operation.maxBodyBytes)throw new Error('Body exceeds Core request limit')
    return result
  }
  const body: Record<string,unknown> = {}
  const encoded: string[] = []
  for (const [key, field] of Object.entries(schema.properties ?? {})) {
    if (!Object.hasOwn(values,key) && !schema.required?.includes(key)) continue
    const value = String(values[key] ?? '')
    if (field.type === 'string') {body[key] = value;encoded.push(`${JSON.stringify(key)}:${JSON.stringify(value)}`)}
    else if (value === '' && !schema.required?.includes(key)) continue
    else {body[key] = parseExactJSON(value);encoded.push(`${JSON.stringify(key)}:${exactJSON(body[key])}`)}
  }
  validateSchema(schema,body)
  const result=`{${encoded.join(',')}}`
  if(operation.maxBodyBytes && new TextEncoder().encode(result).length>operation.maxBodyBytes)throw new Error('Body exceeds Core request limit')
  return result
}

export function validateSchema(schema: BodySchema | undefined,value: unknown,path='body'): void {
  if(!schema || (value===null && schema.nullable))return
  if(schema.anyOf){if(schema.anyOf.some(variant=>{try{validateSchema(variant,value,path);return true}catch{return false}}))return;throw new Error(path+': does not match any published response shape')}
  const fail=(rule:string):never=>{throw new Error(`${path}: ${rule}`)}
  if(schema.enum && !schema.enum.includes(value as string))fail('value is not in the allowed enum')
  if(schema.type==='object'){
    if(!value || typeof value!=='object' || Array.isArray(value))fail('expected object')
    const object=value as Record<string,unknown>
    for(const key of schema.required ?? [])if(!Object.hasOwn(object,key))fail(`missing property ${key}`)
    for(const [key,v] of Object.entries(object)){
      const child=schema.properties?.[key]
      if(child)validateSchema(child,v,`${path}.${key}`)
      else if(schema.additionalProperties===false)fail(`unknown property ${key}`)
      else if(typeof schema.additionalProperties==='object')validateSchema(schema.additionalProperties,v,`${path}.${key}`)
    }
  }else if(schema.type==='array'){
    if(!Array.isArray(value))fail('expected array')
    if(schema.minItems!==undefined && (value as unknown[]).length<schema.minItems)fail('too few items')
    if(schema.maxItems!==undefined && (value as unknown[]).length>schema.maxItems)fail('too many items')
    ;(value as unknown[]).forEach((v,i)=>validateSchema(schema.items,v,`${path}[${i}]`))
  }else if(schema.type==='string'){
    if(typeof value!=='string')fail('expected string')
    const text=value as string
    if(schema.minLength!==undefined && [...text].length<schema.minLength)fail('string is too short')
    if(schema.maxLength!==undefined && [...text].length>schema.maxLength)fail('string is too long')
    if(schema.minBytes!==undefined && new TextEncoder().encode(text).length<schema.minBytes)fail('string below byte minimum')
    if(schema.maxBytes!==undefined && new TextEncoder().encode(text).length>schema.maxBytes)fail('string exceeds byte limit')
  }else if(schema.type==='integer'||schema.type==='number'){
    if(!(schema.type==='integer'&&typeof value==='bigint') && (typeof value!=='number'||!Number.isFinite(value)||(schema.type==='integer'&&!Number.isInteger(value))))fail(`expected ${schema.type}`)
    if(schema.minimumInteger!==undefined && BigInt(value as bigint|number)<BigInt(schema.minimumInteger))fail('integer below exact minimum')
    if(schema.maximumInteger!==undefined && BigInt(value as bigint|number)>BigInt(schema.maximumInteger))fail('integer exceeds exact maximum')
    if(schema.minimum!==undefined && (value as number)<schema.minimum)fail('value below minimum')
    if(schema.maximum!==undefined && (value as number)>schema.maximum)fail('value above maximum')
  }else if(schema.type==='boolean' && typeof value!=='boolean')fail('expected boolean')
}

export function operationUpload(operation: CoreOperation, file: File): BodyInit {
  if (!operation.upload) throw new Error('Core omitted upload contract')
  if (operation.upload.maxBytes && file.size > operation.upload.maxBytes) throw new Error('Upload exceeds Core size limit')
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
