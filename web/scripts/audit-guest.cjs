// Isolated real Core, simulated protocol-only Agent, and anonymous browser.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),net=require('node:net'),crypto=require('node:crypto')
const {spawn,execFileSync}=require('node:child_process'),{chromium}=require('playwright')
const root=path.resolve(__dirname,'../..'),binary=path.join(root,'.build/guest-audit-core'),artifacts=path.join(root,'.build/guest-audit')
fs.mkdirSync(artifacts,{recursive:true});execFileSync('go',['build','-o',binary,'./cmd/nodedance'],{cwd:root,stdio:'inherit'})
async function freePort(){const s=net.createServer();await new Promise(r=>s.listen(0,'127.0.0.1',r));const p=s.address().port;await new Promise(r=>s.close(r));return p}
;(async()=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-guest-audit-')),base=`http://127.0.0.1:${await freePort()}`
 const core=spawn(binary,['serve','--dev','--listen',new URL(base).host,'--data-dir',directory],{cwd:root,stdio:['ignore','pipe','pipe']});let logs='',browser,keepalive
 core.stdout.on('data',x=>logs+=x);core.stderr.on('data',x=>logs+=x)
 try{
  let ready=false;for(let i=0;i<100;i++){try{ready=(await fetch(base+'/api/v1/health')).ok}catch{}if(ready)break;if(core.exitCode!==null)throw Error(logs);await new Promise(r=>setTimeout(r,100))}assert.ok(ready)
  browser=await chromium.launch({headless:true});const admin=await browser.newContext(),visitor=await browser.newContext()
  for(const c of [admin,visitor])await c.addInitScript(()=>localStorage.setItem('nodedance.language','en'))
  const csrf=await(await admin.request.get(base+'/api/v1/auth/csrf')).json(),credentials=JSON.parse(fs.readFileSync(path.join(directory,'initial-admin.json'),'utf8'))
  const login=await admin.request.post(base+'/api/v1/auth/login',{headers:{Origin:base,'X-CSRF-Token':csrf.token},data:credentials});assert.equal(login.status(),200)
  const headers={Origin:base,'X-CSRF-Token':(await login.json()).csrfToken}
  const options=await(await admin.request.get(base+'/api/v1/guest/settings')).json();assert.equal(options.settings.enabled,false);assert.deepEqual(options.settings.nodes,[]);assert.deepEqual(options.settings.metrics,[])
  const page=await visitor.newPage();page.setDefaultTimeout(10000);const errors=[];page.on('pageerror',e=>errors.push(e.message))
  let guestReads=0;const privateReads=[];page.on('request',r=>{if(new URL(r.url()).pathname==='/api/v1/public/guest/display')guestReads++;const p=new URL(r.url()).pathname;if(p.startsWith('/api/v1/nodes')||p.startsWith('/api/v1/guest/settings')||p.startsWith('/api/v1/guest/preview'))privateReads.push(p)})
  await page.goto(base);await page.locator('#login-title').waitFor();assert.equal(await page.getByRole('button',{name:'Enter guest mode',exact:true}).count(),0)
  assert.equal((await visitor.request.get(base+'/api/v1/public/guest/display')).status(),404)
  const enrollment=await(await admin.request.post(base+'/api/v1/agents/enrollments',{headers,data:{displayName:'PRIVATE HOST NAME',coreUrl:base}})).json()
  const credential=crypto.randomBytes(32).toString('hex'),agentHeaders={Authorization:'Bearer '+credential}
  const enrolled=await visitor.request.post(base+'/api/v1/agents/enroll',{headers:{Authorization:'Enrollment '+enrollment.token},data:{requestId:crypto.randomUUID(),credential}});assert.equal(enrolled.status(),201);const identity=await enrolled.json()
  const capabilities=['agent.heartbeat.v1','agent.os-permissions.v1','agent.metrics.v2']
  const opened=await visitor.request.post(base+'/api/v1/agents/channel',{headers:agentHeaders,data:{version:1,type:'hello',payload:{agentId:identity.agentId,nodeId:identity.nodeId,agentVersion:'audit',capabilities,permissions:{os:'linux',architecture:'amd64',effectiveUid:0,effectiveGid:0,supplementaryGroups:[0]}}}});assert.equal(opened.status(),201)
  const channel=base+'/api/v1/agents/channel/'+(await opened.json()).channelId
  const welcome=await visitor.request.get(channel,{headers:agentHeaders}),generation=(await welcome.json()).payload.generation;let ack=welcome.headers()['x-channel-sequence'],write=0,sequence=0
  function snapshot(){const at=new Date().toISOString(),unknown=()=>({status:'unknown',reason:'unavailable',sampledAt:at,sampleAgeMillis:0}),known=value=>({status:'known',value,sampledAt:at,sampleAgeMillis:0});return {collectedAt:at,system:{hostname:known('PRIVATE HOST NAME'),os:unknown(),architecture:unknown(),platform:unknown(),platformFamily:unknown(),platformVersion:unknown(),kernelVersion:unknown()},cpu:{usagePercent:known(25),logicalCores:known(2)},memory:known({totalBytes:8192,availableBytes:4096,usedBytes:4096,usedPercent:50}),network:{summary:known({receivedBytesPerSecond:1048576,sentBytesPerSecond:524288}),interfaces:[],detailsTruncated:false},disk:{status:'unknown',reason:'unavailable',sampledAt:at,sampleAgeMillis:0,mounts:[],detailsTruncated:false},uptime:known({seconds:86400,bootId:'audit-boot'})}}
  let sending=false;async function report(){if(sending)return;sending=true;try{sequence++;const post=async(type,payload)=>{write++;const response=await visitor.request.post(channel,{headers:{...agentHeaders,'X-Channel-Sequence':String(write),'X-Channel-Ack':ack},data:{version:1,type,generation,sequence,payload}});assert.ok(response.ok(),await response.text())};await post('heartbeat',{capabilities});const received=await visitor.request.get(channel,{headers:{...agentHeaders,'X-Channel-Ack':ack}});assert.ok(received.ok());ack=received.headers()['x-channel-sequence'];await post('metrics',snapshot())}finally{sending=false}}
  await report();keepalive=setInterval(()=>report().catch(e=>errors.push(String(e))),10000)
  let metricsReady=false;for(let i=0;i<100;i++){const sample=await(await admin.request.get(base+`/api/v1/nodes/${identity.nodeId}/metrics`)).json();if(sample.metrics?.cpu?.usagePercent?.value===25){metricsReady=true;break}await new Promise(r=>setTimeout(r,25))}assert.ok(metricsReady,'Core did not apply simulated metrics')
  const probeResponse=await admin.request.post(base+'/api/v1/probes',{headers,data:{nodeId:identity.nodeId,name:'PRIVATE PROBE NAME',kind:'tcp',target:'tcp://127.0.0.1:9',intervalSeconds:60,timeoutSeconds:3,enabled:false}});assert.equal(probeResponse.status(),201);const probe=await probeResponse.json()
  const policy={...options.settings,enabled:true,showStatus:true,metrics:['cpu.usage_percent','network.received_bytes_per_second'],historyHours:1,nodes:[{id:identity.nodeId,name:'Public node'}],probes:[{id:probe.id,name:'Public service'}]}
  const saved=await admin.request.put(base+'/api/v1/guest/settings',{headers,data:policy});assert.equal(saved.status(),200);let current=await saved.json()
  const raw=await(await visitor.request.get(base+'/api/v1/public/guest/display')).text();for(const secret of [identity.nodeId,identity.agentId,'PRIVATE HOST NAME','PRIVATE PROBE NAME','tcp://','hostname','memory.used_percent','credential'])assert.ok(!raw.includes(secret),secret)
  const privilegedGuest=await admin.request.get(base+'/api/v1/public/guest/display');assert.equal(privilegedGuest.status(),200);assert.deepEqual(await privilegedGuest.json(),JSON.parse(raw),'Administrator cookie must not expand guest projection')
  await page.reload();await page.getByRole('button',{name:'Enter guest mode',exact:true}).waitFor();assert.equal(guestReads,0,'Display data must not be loaded before guest selection')
  for(const theme of ['light','dark'])for(const width of [390,1440]){await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);await page.waitForTimeout(350);assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));await page.screenshot({path:path.join(artifacts,`login-${theme}-${width}.png`),fullPage:true})}
  await page.getByRole('button',{name:'Enter guest mode',exact:true}).click();await page.getByRole('heading',{name:'Public node',exact:true}).waitFor()
  const display=page.locator('[data-testid=guest-display]');try{await display.getByText('25.0 %',{exact:true}).waitFor()}catch(e){console.error('Guest response:',raw,'Guest text:',await display.textContent());throw e};await display.getByRole('button',{name:'Refresh',exact:true}).click()
  assert.deepEqual(privateReads,[],'Guest screen must not load private APIs');assert.equal(await page.locator('#workspace-navigation').count(),0)
  assert.equal((await visitor.request.get(base+'/api/v1/nodes')).status(),401);assert.equal((await visitor.request.post(base+'/api/v1/updates/apply',{data:{component:'core',confirm:true}})).status(),401)
  await page.evaluate(()=>{localStorage.setItem('nodedance.mode','admin');sessionStorage.setItem('nodedance.mode','admin');document.querySelector('.app-shell').dataset.authenticated='true'})
  assert.equal((await visitor.request.get(base+'/api/v1/guest/settings',{headers:{'X-Mode':'admin'}})).status(),401,'A browser mode marker cannot authenticate')
  await display.getByRole('button',{name:'Back to login',exact:true}).click();await page.locator('#login-title').waitFor();assert.equal(await page.locator('#workspace-navigation').count(),0,'Guest exit must revalidate the session')
  await page.getByRole('button',{name:'Enter guest mode',exact:true}).click();await page.getByRole('heading',{name:'Public node',exact:true}).waitFor()
  const adminPage=await admin.newPage();adminPage.setDefaultTimeout(10000);adminPage.on('pageerror',e=>errors.push(e.message));await adminPage.goto(base);await adminPage.getByRole('button',{name:'Guest display',exact:true}).click()
  const form=adminPage.locator('.guest-settings');await form.locator('input[type=checkbox]').first().waitFor();assert.equal(await form.locator('input[type=checkbox]').filter({visible:true}).count()>0,true)
  await form.getByRole('button',{name:'Preview saved guest view',exact:true}).click();await form.locator('[data-testid=guest-display]').getByRole('heading',{name:'Public node',exact:true}).waitFor()
  // Exercise settings UI with the same server-owned policy, without changing selections.
  const response=adminPage.waitForResponse(r=>r.url().endsWith('/guest/settings')&&r.request().method()==='PUT');await form.getByRole('button',{name:'Save display settings',exact:true}).click();assert.equal((await response).status(),200)
  current=(await(await admin.request.get(base+'/api/v1/guest/settings')).json()).settings
  for(const [name,p] of [['guest',page],['settings',adminPage]])for(const theme of ['light','dark'])for(const width of [390,1440]){await p.setViewportSize({width,height:1000});await p.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);await p.waitForTimeout(350);assert.ok(await p.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`${name} overflow`);await p.screenshot({path:path.join(artifacts,`${name}-${theme}-${width}.png`),fullPage:true})}
  if(process.env.NODEDANCE_GUEST_ATTACKS==='1'){
   console.log('GUEST SECURITY: starting browser attack matrix')
   const attacks=await require('./guest-attacks.cjs')({base,page,visitor,admin,headers,current,identity,probe,stopReporting:async()=>{clearInterval(keepalive);while(sending)await new Promise(r=>setTimeout(r,10))},artifacts})
   current=attacks.policy
   assert.equal(attacks.failures.length,0,'Guest attack findings: '+JSON.stringify(attacks.failures))
  }
  const disabled=await admin.request.put(base+'/api/v1/guest/settings',{headers,data:{...current,enabled:false}});assert.equal(disabled.status(),200)
  await display.getByRole('button',{name:'Refresh',exact:true}).click();await page.waitForFunction(()=>!document.querySelector('[data-testid=guest-display] h2'));assert.equal((await visitor.request.get(base+'/api/v1/public/guest/display')).status(),404)
  assert.deepEqual(errors,[]);console.log('Guest audit passed: real Core policy, anonymous entry, explicit projection with administrator cookies, session-checked exit, mode-spoof denial, administrator preview/save, immediate disable; 12 light/dark/mobile snapshots.')
 }finally{if(keepalive)clearInterval(keepalive);if(browser)await browser.close();core.kill('SIGTERM');if(core.exitCode===null)await new Promise(r=>{core.once('exit',r);setTimeout(()=>{core.kill('SIGKILL');r()},5000).unref()});fs.rmSync(directory,{recursive:true,force:true})}
})().catch(e=>{console.error(e);process.exitCode=1})
