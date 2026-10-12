// Only hashes and random keys are retained. Request bodies and credentials are
// never persisted. A failed/uncertain submission retains the same intent key.
const pending = new Map<string,string>()
const storageKey='nodedance.pending-operation-keys'
try { const records=JSON.parse(sessionStorage.getItem(storageKey) ?? '[]');if(Array.isArray(records))for(const record of records.slice(-100))if(Array.isArray(record)&&/^[a-f0-9]{64}$/.test(record[0])&&typeof record[1]==='string'&&record[1].length<=128)pending.set(record[0],record[1]) }catch{/* Storage can be unavailable. */}
function persist(){try{while(pending.size>100)pending.delete(pending.keys().next().value!);sessionStorage.setItem(storageKey,JSON.stringify([...pending]))}catch{/* In-memory retries still work. */}}
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
  if(!key){key=crypto.randomUUID();pending.set(intent,key);persist()}
  return key
}
export function finishIntent(intent: string): void {pending.delete(intent);persist()}
