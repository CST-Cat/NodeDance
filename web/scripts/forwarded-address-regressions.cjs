const assert=require('node:assert/strict'),fs=require('node:fs')
const {request}=require('playwright')
const {startCore,sqlite}=require('./core-fixture.cjs')
async function observed(core,credentials,forwarded){
 const api=await request.newContext({extraHTTPHeaders:forwarded===undefined?{}:{'X-Forwarded-For':forwarded}})
 try{
  const csrf=await(await api.get(core.base+'/api/v1/auth/csrf')).json()
  const login=await api.post(core.base+'/api/v1/auth/login',{headers:{Origin:core.base,'X-CSRF-Token':csrf.token},data:credentials});assert.equal(login.status(),200,await login.text())
  const sessions=(await(await api.get(core.base+'/api/v1/auth/sessions')).json()).sessions,current=sessions.find(item=>item.current);assert.ok(current)
  const audited=sqlite(core.database,"SELECT remote_addr FROM audit_entries WHERE action='login' AND outcome='succeeded' ORDER BY id DESC LIMIT 1")[0][0]
  assert.equal(audited,current.remoteAddr,'Audit trail and session record must agree on the client address')
  const logout=await api.post(core.base+'/api/v1/auth/logout',{headers:{Origin:core.base,'X-CSRF-Token':(await login.json()).csrfToken},data:{}});assert.ok(logout.ok(),await logout.text())
  return current.remoteAddr
 }finally{await api.dispose()}
}
module.exports=async function forwardedAddress(){
 let core=await startCore()
 try{
  const credentials=core.credentials()
  assert.equal(await observed(core,credentials),'127.0.0.1')
  assert.equal(await observed(core,credentials,'203.0.113.7'),'203.0.113.7','A local reverse proxy reports its client')
  assert.equal(await observed(core,credentials,'198.51.100.9, 203.0.113.7'),'203.0.113.7','Client-supplied leading entries cannot replace the nearest untrusted hop')
  assert.equal(await observed(core,credentials,'127.0.0.1, 198.51.100.9, 203.0.113.7'),'203.0.113.7')
  assert.equal(await observed(core,credentials,'203.0.113.7, 127.0.0.1'),'203.0.113.7','Self addresses are skipped as proxy hops')
  assert.equal(await observed(core,credentials,'forged-admin, 203.0.113.7'),'127.0.0.1','A malformed chain falls back to the socket address')
  assert.equal(await observed(core,credentials,'203.0.113.7, '),'127.0.0.1')
  assert.equal(await observed(core,credentials,'<script>alert(1)</script>'),'127.0.0.1')
  assert.equal(await observed(core,credentials,'203.0.113.8, 203.0.113.7'),'203.0.113.7','An unconfigured proxy range is never trusted')
  await core.stop()
  core=await startCore({directory:core.directory,args:['--trusted-proxies','203.0.113.0/24']})
  assert.equal(await observed(core,credentials,'198.51.100.9, 203.0.113.7'),'198.51.100.9','A configured proxy hop is skipped')
  assert.equal(await observed(core,credentials,'192.0.2.44, 198.51.100.9, 203.0.113.7'),'198.51.100.9','Entries left of the first untrusted hop stay ignored')
  assert.equal(await observed(core,credentials,'203.0.113.8, 203.0.113.7'),'203.0.113.8','A fully trusted chain resolves to its first entry')
  assert.equal(await observed(core,credentials,'forged-admin, 203.0.113.7'),'127.0.0.1')
  const recorded=sqlite(core.database,'SELECT DISTINCT remote_addr FROM audit_entries').map(row=>row[0])
  for(const value of recorded)assert.ok(/^[0-9a-f:.]+$/i.test(value)||value==='unknown',`Unsanitized audit address ${value}`)
  console.log('forwarded-address-regressions passed.')
 }finally{await core.stop();fs.rmSync(core.directory,{recursive:true,force:true})}
}
