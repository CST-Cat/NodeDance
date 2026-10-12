// Only hashes and random keys are retained. Request bodies and credentials are
// never persisted. A failed/uncertain submission retains the same intent key.
const pending = new Map<string,string>()
const files = new WeakMap<Blob,string>()
export async function requestIntent(name: string, input: unknown, body?: BodyInit | null, headers?: Headers): Promise<string> {
  let content: string | undefined
  if (body instanceof Blob) {
    let identity=files.get(body); if(!identity){identity=crypto.randomUUID();files.set(body,identity)}
    content=identity
  } else if (body instanceof FormData) {
    const entries: string[]=[]
    for(const [key,value] of body.entries()) entries.push(`${key}:${typeof value==='string'?value:await requestIntent('file',{},value)}`)
    content=JSON.stringify(entries)
  } else content=body?.toString()
  const intentHeaders=[...(headers?.entries() ?? [])].filter(([key])=>key!=='idempotency-key').sort()
  const canonical=(value: unknown): unknown => Array.isArray(value)?value.map(canonical):value && typeof value==='object'?Object.fromEntries(Object.entries(value).sort(([a],[b])=>a.localeCompare(b)).map(([key,v])=>[key,canonical(v)])):value
  // JSON formatting and property order do not define a different intent.
  if(typeof body==='string'){try{
    const parsed=JSON.parse(body)
    const unsafe=(value:unknown):boolean=>typeof value==='number'?Number.isInteger(value)&&!Number.isSafeInteger(value):Array.isArray(value)?value.some(unsafe):value && typeof value==='object'?Object.values(value).some(unsafe):false
    // Preserve numeric tokens beyond JS's exact integer range. A formatting
    // change in this uncommon case requires the user to supply the retry key.
    content=unsafe(parsed)?body:JSON.stringify(canonical(parsed))
  }catch{/* Core reports invalid JSON. */}}
  const bytes=new TextEncoder().encode(JSON.stringify([name,canonical(input),content,intentHeaders]))
  return Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',bytes)),byte=>byte.toString(16).padStart(2,'0')).join('')
}
export function intentKey(intent: string, fresh=false): string {
  if(fresh) pending.delete(intent)
  let key=pending.get(intent)
  if(!key){key=crypto.randomUUID();pending.set(intent,key)}
  return key
}
export function finishIntent(intent: string): void {pending.delete(intent)}
