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
  for(const command of [enrollment.nativeInstallCommand,enrollment.dockerInstallCommand])assert.ok(command.includes('--token '+enrollment.token),'Administrator enrollment supplies a complete installation command')
  const credential=crypto.randomBytes(32).toString('hex'),agentHeaders={Authorization:'Bearer '+credential}
  const enrolled=await visitor.request.post(base+'/api/v1/agents/enroll',{headers:{Authorization:'Enrollment '+enrollment.token},data:{requestId:crypto.randomUUID(),credential}});assert.equal(enrolled.status(),201);const identity=await enrolled.json()
  const capabilities=['agent.heartbeat.v1','agent.os-permissions.v1','agent.metrics.v2']
  const opened=await visitor.request.post(base+'/api/v1/agents/channel',{headers:agentHeaders,data:{version:1,type:'hello',payload:{agentId:identity.agentId,nodeId:identity.nodeId,agentVersion:'agent-v0.1.10',capabilities,permissions:{os:'linux',architecture:'amd64',effectiveUid:0,effectiveGid:0,supplementaryGroups:[0]}}}});assert.equal(opened.status(),201)
  const channel=base+'/api/v1/agents/channel/'+(await opened.json()).channelId
  const welcome=await visitor.request.get(channel,{headers:agentHeaders}),generation=(await welcome.json()).payload.generation;let ack=welcome.headers()['x-channel-sequence'],write=0,sequence=0
  function snapshot(){const at=new Date().toISOString(),unknown=()=>({status:'unknown',reason:'unavailable',sampledAt:at,sampleAgeMillis:0}),known=value=>({status:'known',value,sampledAt:at,sampleAgeMillis:0});return {collectedAt:at,system:{hostname:known('PRIVATE HOST NAME'),os:unknown(),architecture:unknown(),platform:unknown(),platformFamily:unknown(),platformVersion:unknown(),kernelVersion:unknown()},cpu:{usagePercent:known(25),logicalCores:known(2)},memory:known({totalBytes:8192,availableBytes:4096,usedBytes:4096,usedPercent:50}),network:{summary:known({receivedBytesPerSecond:1048576,sentBytesPerSecond:524288}),interfaces:[],detailsTruncated:false},disk:{status:'unknown',reason:'unavailable',sampledAt:at,sampleAgeMillis:0,mounts:[],detailsTruncated:false},uptime:known({seconds:86400,bootId:'audit-boot'})}}
  let sending=false;async function report(){if(sending)return;sending=true;try{sequence++;const post=async(type,payload)=>{write++;const response=await visitor.request.post(channel,{headers:{...agentHeaders,'X-Channel-Sequence':String(write),'X-Channel-Ack':ack},data:{version:1,type,generation,sequence,payload}});assert.ok(response.ok(),await response.text())};await post('heartbeat',{capabilities});const received=await visitor.request.get(channel,{headers:{...agentHeaders,'X-Channel-Ack':ack}});assert.ok(received.ok());ack=received.headers()['x-channel-sequence'];await post('metrics',snapshot())}finally{sending=false}}
  await report();keepalive=setInterval(()=>report().catch(e=>errors.push(String(e))),10000)
  let metricsReady=false;for(let i=0;i<100;i++){const sample=await(await admin.request.get(base+`/api/v1/nodes/${identity.nodeId}/metrics`)).json();if(sample.metrics?.cpu?.usagePercent?.value===25){metricsReady=true;break}await new Promise(r=>setTimeout(r,25))}assert.ok(metricsReady,'Core did not apply simulated metrics')
  const privateComposeAlias=await admin.request.put(base+`/api/v1/nodes/${identity.nodeId}/preferences`,{headers,data:{targetKind:'compose_project',identity:'compose_project:'+'a'.repeat(64),alias:'PRIVATE COMPOSE DISPLAY NAME',visible:true}});assert.equal(privateComposeAlias.status(),204)
  const probeResponse=await admin.request.post(base+'/api/v1/probes',{headers,data:{nodeId:identity.nodeId,name:'PRIVATE PROBE NAME',kind:'tcp',target:'tcp://127.0.0.1:9',intervalSeconds:60,timeoutSeconds:3,enabled:false}});assert.equal(probeResponse.status(),201);const probe=await probeResponse.json()
  const policy={...options.settings,enabled:true,showStatus:true,metrics:['cpu.usage_percent','network.received_bytes_per_second'],historyHours:1,nodes:[{id:identity.nodeId,name:'Public node'}],probes:[{id:probe.id,name:'Public service'}]}
  const saved=await admin.request.put(base+'/api/v1/guest/settings',{headers,data:policy});assert.equal(saved.status(),200);let current=await saved.json()
  const raw=await(await visitor.request.get(base+'/api/v1/public/guest/display')).text();for(const secret of [identity.nodeId,identity.agentId,'PRIVATE HOST NAME','PRIVATE PROBE NAME','PRIVATE COMPOSE DISPLAY NAME','tcp://','hostname','memory.used_percent','credential'])assert.ok(!raw.includes(secret),secret)
  const publicDisplay=JSON.parse(raw);let historyPoints=0
  for(const node of publicDisplay.nodes){
   assert.ok(node.history,'Selected history must be included')
   const from=Date.parse(node.history.from),to=Date.parse(node.history.to)
   assert.ok(Number.isFinite(from)&&Number.isFinite(to)&&from<to)
   for(const series of node.history.series)for(const point of series.points){
    const bucket=Date.parse(point.bucketAt);historyPoints++
    assert.ok(bucket>=from&&bucket<to,'Guest history bucket must respect the exact [from,to) window')
   }
  }
  assert.ok(historyPoints>0,'History bounds must be checked against actual persisted fixture samples')
  const privilegedGuest=await admin.request.get(base+'/api/v1/public/guest/display');assert.equal(privilegedGuest.status(),200);assert.deepEqual(await privilegedGuest.json(),JSON.parse(raw),'Administrator cookie must not expand guest projection')
  await page.reload();await page.getByRole('button',{name:'Enter guest mode',exact:true}).waitFor();assert.equal(guestReads,0,'Display data must not be loaded before guest selection')
  for(const theme of ['light','dark'])for(const width of [390,1440]){await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);await page.waitForTimeout(350);assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));await page.screenshot({path:path.join(artifacts,`login-${theme}-${width}.png`),fullPage:true})}
  await page.getByRole('button',{name:'Enter guest mode',exact:true}).click();await page.getByRole('heading',{name:'Public node',exact:true}).waitFor()
  const display=page.locator('[data-testid=guest-display]');try{await display.getByText('25.0 %',{exact:true}).waitFor()}catch(e){console.error('Guest response:',raw,'Guest text:',await display.textContent());throw e};await display.getByRole('button',{name:'Refresh',exact:true}).click()
  assert.deepEqual(privateReads,[],'Guest screen must not load private APIs');assert.equal(await page.locator('#workspace-navigation').count(),0)
  assert.equal(new URL(page.url()).hash,'#/guest')
  await page.goBack();await page.locator('#login-title').waitFor();assert.equal(new URL(page.url()).hash,'#/login')
  await page.goForward();await page.getByRole('heading',{name:'Public node',exact:true}).waitFor()
  const restoredReads=[];const observeRestore=r=>{const p=new URL(r.url()).pathname;if(p.startsWith('/api/')&&!p.startsWith('/api/v1/public/')&&p!=='/api/v1/operations')restoredReads.push(p)}
  page.on('request',observeRestore);await page.reload();await page.getByRole('heading',{name:'Public node',exact:true}).waitFor();page.off('request',observeRestore)
  assert.deepEqual(restoredReads,[],'Restoring a guest URL must not fetch administrator data')
  assert.equal(new URL(page.url()).hash,'#/guest')

  await page.evaluate(()=>{history.replaceState({authenticated:true,role:'admin'},'',location.href);location.hash='/guest-settings'})
  await page.locator('#login-title').waitFor();assert.equal(await page.locator('#workspace-navigation').count(),0,'A history marker cannot grant administrator access')
  await page.goBack();await page.getByRole('heading',{name:'Public node',exact:true}).waitFor()
  assert.equal((await visitor.request.get(base+'/api/v1/nodes')).status(),401);assert.equal((await visitor.request.post(base+'/api/v1/updates/apply',{data:{component:'core',confirm:true}})).status(),401)
  await page.evaluate(()=>{localStorage.setItem('nodedance.mode','admin');sessionStorage.setItem('nodedance.mode','admin');document.querySelector('.app-shell').dataset.authenticated='true'})
  assert.equal((await visitor.request.get(base+'/api/v1/guest/settings',{headers:{'X-Mode':'admin'}})).status(),401,'A browser mode marker cannot authenticate')
  await display.getByRole('button',{name:'Back to login',exact:true}).click();await page.locator('#login-title').waitFor();assert.equal(await page.locator('#workspace-navigation').count(),0,'Guest exit must revalidate the session')
  await page.getByRole('button',{name:'Enter guest mode',exact:true}).click();await page.getByRole('heading',{name:'Public node',exact:true}).waitFor()
  const adminPage=await admin.newPage();adminPage.setDefaultTimeout(10000);adminPage.on('pageerror',e=>errors.push(e.message));await adminPage.goto(base)
  const card=adminPage.locator('.vps-card').first();await card.waitFor()
  assert.ok(!(await card.innerText()).includes(identity.nodeId),'Node cards must hide internal UUIDs')
  assert.equal(await card.locator('.vps-card-version').innerText(),'agent-v0.1.10')
  for(const theme of ['light','dark'])for(const width of [390,1440]){
   await adminPage.setViewportSize({width,height:1000});await adminPage.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
   await adminPage.evaluate(()=>window.scrollTo(0,0));await adminPage.waitForTimeout(350)
   assert.ok(await adminPage.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Node card layout overflow')
   await adminPage.screenshot({path:path.join(artifacts,`nodes-${theme}-${width}.png`),fullPage:true})
  }
  await card.locator('.vps-name').click();await adminPage.locator('#detail-title').waitFor()
  assert.ok(!(await adminPage.locator('.detail-heading').innerText()).includes(identity.nodeId),'Node detail headings must hide internal UUIDs')
  for(const theme of ['light','dark'])for(const width of [390,1440]){
   await adminPage.setViewportSize({width,height:1000});await adminPage.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
   await adminPage.evaluate(()=>window.scrollTo(0,0));await adminPage.waitForTimeout(350)
   assert.ok(await adminPage.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Node detail layout overflow')
   await adminPage.screenshot({path:path.join(artifacts,`node-detail-${theme}-${width}.png`),fullPage:true})
  }
  await adminPage.setViewportSize({width:1440,height:1000});await adminPage.waitForTimeout(350)
  const detailURL=adminPage.url();assert.ok(new URL(detailURL).hash.includes('node='+identity.nodeId))
  await adminPage.goBack();await card.waitFor();assert.equal(new URL(adminPage.url()).hash,'#/nodes')
  await adminPage.goForward();await adminPage.locator('#detail-title').waitFor();assert.equal(adminPage.url(),detailURL)
  const detailTabs=adminPage.locator('.node-detail-navigation')
  const overviewTab=detailTabs.getByRole('button',{name:'Overview',exact:true})
  const probesTab=detailTabs.getByRole('button',{name:'Service probes',exact:true})
  const historyTab=detailTabs.getByRole('button',{name:'History',exact:true})
  const probesPanel=adminPage.getByTestId('service-probes')
  assert.equal(await probesPanel.count(),0,'Overview must not contain the full service probe panel')
  await probesTab.click();await probesPanel.waitFor()
  assert.ok(new URL(adminPage.url()).hash.includes('section=probes'),'Service probes have their own detail URL')
  assert.equal(await probesTab.getAttribute('aria-current'),'page')
  await adminPage.reload();await probesPanel.waitFor()
  assert.equal(await probesTab.getAttribute('aria-current'),'page','Reload preserves the service probes tab')
  await adminPage.goBack();await adminPage.waitForFunction(()=>!location.hash.includes('section='))
  await probesPanel.waitFor({state:'detached'})
  assert.equal(await overviewTab.getAttribute('aria-current'),'page')
  assert.equal(await probesPanel.count(),0,'Returning to overview removes the full service probe panel')
  await historyTab.click();await adminPage.waitForFunction(()=>location.hash.includes('section=history'))
  assert.equal(await probesPanel.count(),0,'History must not retain the service probe panel')
  await adminPage.locator('[data-testid=history-chart] canvas').waitFor()
  assert.ok(await adminPage.locator('[data-testid=history-chart] canvas').isVisible(),'First asynchronous history load initializes the actual chart')
  await adminPage.reload();await adminPage.locator('#detail-title').waitFor();assert.equal(await historyTab.getAttribute('aria-current'),'page')
  await adminPage.goBack();await overviewTab.waitFor();await adminPage.waitForFunction(()=>!location.hash.includes('section='))
  assert.equal(await overviewTab.getAttribute('aria-current'),'page','Browser Back restores overview after history')
  assert.ok(!new URL(adminPage.url()).hash.includes('section='))
  // A valid administrator cookie still gets only public requests on guest URL
  // restore; returning to management requires a fresh valid session.
  const savedAdminCookies=await admin.cookies(base),managementURL=adminPage.url()
  const guestPrivateRequests=[]
  const trackGuest=r=>{const p=new URL(r.url()).pathname;if(p.startsWith('/api/')&&!p.startsWith('/api/v1/public/')&&p!=='/api/v1/operations')guestPrivateRequests.push(p)}
  adminPage.on('request',trackGuest)
  await adminPage.locator('.ui-select:has(.language-selector)').getByRole('combobox').click();await adminPage.locator('body > .u031-list').waitFor()
  await adminPage.evaluate(()=>{location.hash='/guest'})
  await adminPage.getByRole('heading',{name:'Public node',exact:true}).waitFor()
  assert.equal(await adminPage.locator('body > .u031-list').count(),0,'Guest entry immediately removes private dropdown portals')
  await adminPage.reload();await adminPage.getByRole('heading',{name:'Public node',exact:true}).waitFor()
  adminPage.off('request',trackGuest);assert.deepEqual(guestPrivateRequests,[],'Administrator cookies must not expand guest history restoration')
  await adminPage.goBack();await adminPage.locator('#detail-title').waitFor();assert.equal(adminPage.url(),managementURL)
  // Hold a real authenticated permission read, switch to guest, and release the
  // old response. It must not overwrite the newer public navigation.
  let releaseGuard,guardStarted
  const held=new Promise(r=>releaseGuard=r),started=new Promise(r=>guardStarted=r)
  await adminPage.route('**/api/v1/auth/me',async route=>{const response=await route.fetch();assert.equal(response.status(),200);guardStarted();await held;await route.fulfill({response})})
  await adminPage.evaluate(()=>{location.hash='/alerts'});await started
  guestPrivateRequests.length=0;adminPage.on('request',trackGuest)
  await adminPage.evaluate(()=>{location.hash='/guest'});await adminPage.getByRole('heading',{name:'Public node',exact:true}).waitFor()
  releaseGuard();await adminPage.waitForTimeout(200);await adminPage.unroute('**/api/v1/auth/me')
  adminPage.off('request',trackGuest);assert.deepEqual(guestPrivateRequests,[],'A stale permission response must not load a private page after entering guest mode')
  assert.equal(new URL(adminPage.url()).hash,'#/guest');assert.equal(await adminPage.locator('#workspace-navigation').count(),0)
  await admin.clearCookies();await adminPage.goBack();await adminPage.locator('#login-title').waitFor()
  assert.equal(await adminPage.locator('#workspace-navigation').count(),0,'Back to management must reject a missing administrator session')
  await admin.addCookies(savedAdminCookies);await adminPage.goto(managementURL);await adminPage.locator('#detail-title').waitFor()
  // A node-list response that completes after its page unmounts must not
  // trigger follow-up private metric/container/preference reads in guest mode.
  await adminPage.getByTestId('node-detail-navigation').getByRole('button',{name:'Back to home',exact:true}).click();await adminPage.locator('.nodes-dashboard').waitFor()
  await adminPage.getByRole('button',{name:'Guest display',exact:true}).click();await adminPage.locator('.guest-settings').waitFor()
  let releaseNodes,nodesStarted
  const heldNodes=new Promise(r=>releaseNodes=r),startedNodes=new Promise(r=>nodesStarted=r)
  await adminPage.route('**/api/v1/nodes',async route=>{const response=await route.fetch();nodesStarted();await heldNodes;await route.fulfill({response})})
  await adminPage.getByRole('button',{name:'Nodes',exact:true}).click();await startedNodes
  await adminPage.evaluate(()=>{location.hash='/guest'});await adminPage.getByRole('heading',{name:'Public node',exact:true}).waitFor()
  guestPrivateRequests.length=0;adminPage.on('request',trackGuest)
  releaseNodes();await adminPage.waitForTimeout(200);await adminPage.unroute('**/api/v1/nodes');adminPage.off('request',trackGuest)
  assert.deepEqual(guestPrivateRequests,[],'An unmounted node page must not start new private reads after entering guest mode')
  // File-manager reads also honor guest entry after component disposal. This
  // protocol-only Agent intentionally has no file capability; hold its real
  // unavailable response, then release it after the public page is visible.
  let releaseFiles,filesStarted
  const heldFiles=new Promise(r=>releaseFiles=r),startedFiles=new Promise(r=>filesStarted=r)
  const filesRoute='**/api/v1/nodes/*/files?*'
  await adminPage.route(filesRoute,async route=>{const response=await route.fetch();filesStarted();await heldFiles;await route.fulfill({response})})
  await adminPage.goto(base+`/#/nodes?node=${identity.nodeId}&section=files`);await startedFiles
  await adminPage.evaluate(()=>{location.hash='/guest'});await adminPage.getByRole('heading',{name:'Public node',exact:true}).waitFor()
  guestPrivateRequests.length=0;adminPage.on('request',trackGuest)
  releaseFiles();await adminPage.waitForTimeout(300);await adminPage.unroute(filesRoute);adminPage.off('request',trackGuest)
  assert.deepEqual(guestPrivateRequests,[],'Disposed file manager must not start private reads after guest entry')
  // Grant creation and revocation must not start private follow-up reads
  // after their host-management component has unmounted into guest mode.
  for(const method of ['POST','DELETE']){
    await adminPage.goto(base+'/#/host-management?tab=enrollment')
    await adminPage.locator('.host-page form .primary-button').waitFor()
    let releaseGrant,grantStarted
    const heldGrant=new Promise(r=>releaseGrant=r),startedGrant=new Promise(r=>grantStarted=r)
    const grantRoute='**/api/v1/agents/enrollment-grants**'
    await adminPage.route(grantRoute,async route=>{
      if(route.request().method()!==method){await route.continue();return}
      const response=await route.fetch();assert.equal(response.status(),method==='POST'?201:204)
      grantStarted();await heldGrant;await route.fulfill({response})
    })
    if(method==='POST')await adminPage.locator('.host-page form .primary-button').click()
    else await adminPage.locator('.host-result button').first().click()
    await startedGrant
    await adminPage.evaluate(()=>{location.hash='/guest'});await adminPage.getByRole('heading',{name:'Public node',exact:true}).waitFor()
    guestPrivateRequests.length=0;adminPage.on('request',trackGuest)
    releaseGrant();await adminPage.waitForTimeout(500)
    await adminPage.unroute(grantRoute);adminPage.off('request',trackGuest)
    assert.deepEqual(guestPrivateRequests,[],`${method} grant completion must not read private data after guest entry`)
    assert.equal(await adminPage.locator('#workspace-navigation').count(),0)
  }
  await adminPage.goto(managementURL);await adminPage.locator('#detail-title').waitFor()
  await adminPage.getByTestId('node-detail-navigation').getByRole('button',{name:'Back to home',exact:true}).click();await adminPage.locator('.nodes-dashboard').waitFor()
  const guestIcon=await adminPage.getByRole('button',{name:'Guest display',exact:true}).locator('svg path').getAttribute('d')
  const nodeIcon=await adminPage.getByRole('button',{name:'Nodes',exact:true}).locator('svg path').getAttribute('d')
  assert.ok(guestIcon && guestIcon!==nodeIcon,'Guest display must have its own icon')
  await adminPage.getByRole('button',{name:'Guest display',exact:true}).click()
  const form=adminPage.locator('.guest-settings');await form.locator('input[type=checkbox]').first().waitFor();assert.equal(await form.locator('input[type=checkbox]').filter({visible:true}).count()>0,true)
  await form.getByRole('button',{name:'Preview saved guest view',exact:true}).click();await form.locator('[data-testid=guest-display]').getByRole('heading',{name:'Public node',exact:true}).waitFor()
  // Exercise settings UI with the same server-owned policy, without changing selections.
  const response=adminPage.waitForResponse(r=>r.url().endsWith('/guest/settings')&&r.request().method()==='PUT');await form.getByRole('button',{name:'Save display settings',exact:true}).click();assert.equal((await response).status(),200)
  current=(await(await admin.request.get(base+'/api/v1/guest/settings')).json()).settings
  for(const [name,p] of [['guest',page],['settings',adminPage]])for(const theme of ['light','dark'])for(const width of [390,1440]){await p.setViewportSize({width,height:1000});await p.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);await p.evaluate(()=>window.scrollTo(0,0));await p.waitForTimeout(350);assert.ok(await p.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`${name} overflow`);await p.screenshot({path:path.join(artifacts,`${name}-${theme}-${width}.png`),fullPage:true})}
  if(process.env.NODEDANCE_GUEST_ATTACKS==='1'){
   console.log('GUEST SECURITY: starting browser attack matrix')
   const attacks=await require('./guest-attacks.cjs')({base,page,visitor,admin,headers,current,identity,probe,stopReporting:async()=>{clearInterval(keepalive);while(sending)await new Promise(r=>setTimeout(r,10))},artifacts})
   current=attacks.policy
   assert.equal(attacks.failures.length,0,'Guest attack findings: '+JSON.stringify(attacks.failures))
  }
  const disabled=await admin.request.put(base+'/api/v1/guest/settings',{headers,data:{...current,enabled:false}});assert.equal(disabled.status(),200)
  await display.getByRole('button',{name:'Refresh',exact:true}).click();await page.waitForFunction(()=>!document.querySelector('[data-testid=guest-display] h2'));assert.equal((await visitor.request.get(base+'/api/v1/public/guest/display')).status(),404)
  assert.deepEqual(errors,[]);console.log('Guest audit passed: real Core policy, anonymous entry, explicit projection with administrator cookies, session-checked exit, mode-spoof denial, administrator preview/save, immediate disable; 20 light/dark/mobile snapshots.')
 }finally{if(keepalive)clearInterval(keepalive);if(browser)await browser.close();core.kill('SIGTERM');if(core.exitCode===null)await new Promise(r=>{core.once('exit',r);setTimeout(()=>{core.kill('SIGKILL');r()},5000).unref()});fs.rmSync(directory,{recursive:true,force:true})}
})().catch(e=>{console.error(e);process.exitCode=1})
