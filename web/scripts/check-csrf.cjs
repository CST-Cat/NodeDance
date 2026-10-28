const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')
const ts = require('typescript')
const source = fs.readFileSync(path.resolve(__dirname, '../src/api.ts'), 'utf8')
const code = ts.transpileModule(source, {compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
function client(fetch) {
  const moduleObject = {exports:{}}
  const imports = {
    './operations': {resolveOperation:async name=>({operation:{name,method:name==='auth.csrf'?'GET':'POST',transport:'json'},path:'/'+name}),decodeContractJSON:JSON.parse,exactJSON:JSON.stringify},
    './operation-intent': {},
    './i18n': {t:value=>value,diagnostic:value=>value},
  }
  vm.runInNewContext(code,{module:moduleObject,exports:moduleObject.exports,require:name=>imports[name],fetch,AbortController,AbortSignal,DOMException,Headers,FormData,Blob,Event,window:{setTimeout,clearTimeout,dispatchEvent(){}}})
  return moduleObject.exports
}
const reply = value => new Response(JSON.stringify(value),{status:200,headers:{'Content-Type':'application/json'}})
;(async()=>{
  let release,started,reads=0
  const waiting = new Promise(resolve=>started=resolve),mutations=[]
  const api=client(async (url,options)=>{
    if(url==='/auth.csrf'){reads++;started();return await new Promise(resolve=>release=()=>resolve(reply({token:'shared'})))}
    mutations.push([url,options.headers.get('X-CSRF-Token')]);return reply({ok:true})
  })
  const controller=new AbortController()
  const canceled=api.request('/canceled',{method:'POST',signal:controller.signal},true,5000)
  const timedOut=api.request('/timed-out',{method:'POST'},true,20)
  const survivor=api.request('/survivor',{method:'POST'},true,5000)
  const canceledCheck=assert.rejects(canceled,error=>error.name==='AbortError')
  const timeoutCheck=assert.rejects(timedOut,error=>error.name==='TimeoutError')
  await waiting;controller.abort(new DOMException('Canceled','AbortError'));await Promise.all([canceledCheck,timeoutCheck])
  assert.equal(reads,1);release();await survivor;assert.deepEqual(mutations,[['/survivor','shared']])

  let releaseOld,oldStarted,oldSignal,fetches=0
  const oldWaiting=new Promise(resolve=>oldStarted=resolve),sent=[]
  const next=client(async (url,options)=>{
    if(url==='/auth.csrf'){
      fetches++
      if(fetches===1){oldSignal=options.signal;oldStarted();return await new Promise(resolve=>releaseOld=()=>resolve(reply({token:'old'})))}
      return reply({token:'prelogin'})
    }
    sent.push([url,options.headers.get('X-CSRF-Token')])
    return reply(url==='/auth.login'?{csrfToken:'new-session'}:url==='/auth.switch'?{csrfToken:'switched-session'}:{ok:true})
  })
  const old=next.request('/old-request',{method:'POST'},true,5000)
  const oldCheck=assert.rejects(old,error=>error.name==='AbortError')
  await oldWaiting;await next.api.login('user','password');await oldCheck;assert.ok(oldSignal.aborted,'A stale token request cannot update cookies after session change');releaseOld()
  await new Promise(resolve=>setImmediate(resolve))
  await next.request('/after-login',{method:'POST'},true,5000)
  assert.equal(fetches,2);assert.deepEqual(sent,[['/auth.login','prelogin'],['/after-login','new-session']])
  await next.operationRequest('auth.switch');await next.request('/after-switch',{method:'POST'},true,5000)
  assert.deepEqual(sent.slice(-2),[['/auth.switch','new-session'],['/after-switch','switched-session']])
  console.log('Shared CSRF requests preserve cancellation, timeouts and session changes.')
})().catch(error=>{console.error(error);process.exit(1)})
