// Real browser presentation regressions against the isolated interface Core.
// Compose responses are protocol fixtures; no remote mutation is executed.
const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,context,base,node,headers,artifacts}){
 async function layouts(name){
  for(const theme of ['light','dark'])for(const width of [390,1440]){
   await page.setViewportSize({width,height:1000});await page.waitForTimeout(100)
   const scrim=page.locator('.navigation-scrim');if(await scrim.isVisible()){await scrim.click({position:{x:width-10,y:20}});await scrim.waitFor({state:'hidden'})}
   await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
   assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`${name} layout fits viewport`)
   await page.screenshot({animations:'disabled',path:path.join(artifacts,`review-${name}-${theme}-${width}.png`),fullPage:true})
  }
  await page.setViewportSize({width:1440,height:1000})
 }
 await page.goto(base+'/#/settings')
 await page.locator('.theme-options').waitFor()
 await page.locator('.theme-options button').first().click()
 assert.equal(await page.locator('.theme-options button').first().getAttribute('aria-pressed'),'true')
 await page.waitForFunction(()=>document.documentElement.dataset.theme==='dark')
 const saved=page.waitForResponse(r=>r.url().endsWith('/settings/appearance')&&r.request().method()==='PUT')
 await page.locator('.profile-form button[type=submit]').click();assert.equal((await saved).status(),200)
 await page.reload();await page.waitForFunction(()=>document.documentElement.dataset.theme==='dark')
 await layouts('settings')

 const keys=['a'.repeat(64),'b'.repeat(64)]
 const projects=keys.map((key,i)=>({ref:{key,name:'fixture-'+i,workingDirectory:'/fixture-'+i,configFiles:['/fixture-'+i+'/compose.yaml']},configAvailable:true,services:[]}))
 const listRoute='**/api/v1/nodes/*/compose/projects'
 const configRoute='**/api/v1/nodes/*/compose/projects/*/config/0'
 const validateRoute='**/api/v1/nodes/*/compose/projects/*/config/0/validate'
 const submissions=[]
 await page.route(listRoute,route=>route.request().method()==='POST'?(submissions.push(route.request().headers()['idempotency-key']),route.fulfill({status:503,body:'Simulated lost Compose response'})):route.fulfill({json:{projects,dataStale:false,serverTime:new Date().toISOString()}}))
 await page.route(configRoute,route=>route.fulfill({json:{fileIndex:0,content:route.request().url().includes(keys[0])?'services: {} # first':'services: {} # second',sha256:'c'.repeat(64)}}))
 await page.goto(base+`/#/nodes?node=${node}&section=compose`)
 await page.locator('.compose-project-card').first().waitFor()
 const openEditor=i=>page.locator('.compose-project-card').nth(i).locator('.compose-project-actions button').last().click()
 const editor=page.locator('.compose-editor')
 await openEditor(0);await page.waitForFunction(()=>document.querySelector('.compose-editor textarea')?.value==='services: {} # first')
 await editor.locator('textarea').fill('draft of first project')
 await openEditor(1);await page.waitForFunction(()=>document.querySelector('.compose-editor textarea')?.value==='services: {} # second')
 assert.equal(await editor.locator('textarea').inputValue(),'services: {} # second','Switching projects must not retain another project draft/base version')
 let releaseValidation,validationStarted
 const held=new Promise(r=>releaseValidation=r),started=new Promise(r=>validationStarted=r)
 await page.route(validateRoute,async route=>{validationStarted();await held;await route.fulfill({json:{valid:true}})})
 await editor.locator('textarea').fill('services: {} # validated draft')
 await editor.locator('.compose-editor-actions button').first().click();await started
 await editor.locator('textarea').fill('services: {} # changed while validating')
 releaseValidation();await page.waitForFunction(()=>!document.querySelector('.compose-editor-actions button')?.disabled)
 assert.ok(await editor.locator('.compose-editor-actions button').nth(1).isDisabled(),'Old validation cannot authorize changed contents')
 await page.unroute(validateRoute)

 const create=page.locator('.compose-create-form')
 await create.locator('input').first().fill('retry-fixture');await create.locator('input').nth(1).fill('/retry-fixture');await create.locator('textarea').fill('services: {}')
 for(let attempt=0;attempt<2;attempt++){
  await create.locator('button').click()
  const reply=page.waitForResponse(r=>r.url().endsWith('/compose/projects')&&r.request().method()==='POST')
  await page.locator('.ui-dialog').getByRole('button',{name:'Confirm',exact:true}).click()
  assert.equal((await reply).status(),503)
  await create.locator('[role=alert]').filter({hasText:'Simulated lost Compose response'}).waitFor()
 }
 assert.equal(submissions.length,2);assert.ok(submissions[0]);assert.equal(submissions[0],submissions[1],'Specialized Compose retries must use the shared stable execution identity')
 await layouts('compose')
 await page.unroute(listRoute);await page.unroute(configRoute)
 await page.setViewportSize({width:1440,height:1000})

 const created=await context.request.post(base+'/api/v1/probes',{headers,data:{nodeId:node,name:'Review probe',kind:'tcp',target:'tcp://127.0.0.1:9',intervalSeconds:60,timeoutSeconds:3,enabled:false}})
 assert.equal(created.status(),201);const probe=await created.json()
 await page.goto(base+`/#/nodes?node=${node}`)
 await page.getByTestId('probe-overview-summary').waitFor()
 assert.equal(await page.getByTestId('service-probes').count(),0,'Overview must show only a compact probe summary')
 await layouts('probe-overview')
 await page.getByRole('button',{name:'View service checks',exact:true}).click()
 assert.ok(page.url().includes('section=probes'),'Summary opens the dedicated node section')
 const card=page.locator('.probe-card').filter({hasText:'Review probe'})
 await card.waitFor();await layouts('probes');await card.locator('.probe-actions button').nth(1).click()
 const changed=await context.request.put(base+'/api/v1/probes/'+probe.id,{headers,data:{nodeId:node,name:'Review probe changed remotely',kind:'tcp',target:probe.target,intervalSeconds:60,timeoutSeconds:3,enabled:false,revision:probe.revision}})
 assert.equal(changed.status(),200);const remote=await changed.json()
 await page.locator('.probe-card').filter({hasText:remote.name}).waitFor()
 const form=page.locator('.probe-form');await form.locator('input').first().fill('Stale browser draft')
 let response=page.waitForResponse(r=>r.url().endsWith('/probes/'+probe.id)&&r.request().method()==='PUT')
 await form.locator('button[type=submit]').click();assert.equal((await response).status(),409,'Automatic list refresh must not replace the editor original revision')
 assert.equal((await(await context.request.get(base+'/api/v1/probes')).json()).probes.find(p=>p.id===probe.id).name,remote.name)
 assert.equal((await context.request.delete(base+'/api/v1/probes/'+probe.id+'?revision='+remote.revision,{headers})).status(),204)
 await page.locator('.probe-card').filter({hasText:remote.name}).waitFor({state:'detached'})
 response=page.waitForResponse(r=>r.url().endsWith('/probes/'+probe.id)&&r.request().method()==='PUT')
 await form.locator('button[type=submit]').click();assert.equal((await response).status(),404,'Editing a removed probe must never create a replacement')
 assert.ok(!(await(await context.request.get(base+'/api/v1/probes')).json()).probes.some(p=>p.name==='Stale browser draft'))

 const overviewResponse=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/probes'&&r.request().method()==='GET')
 await page.goto(base+`/#/nodes?node=${node}`)
 await page.locator('.dashboard-tabs button[aria-current=page]').filter({hasText:/^Overview$/}).waitFor()
 await overviewResponse
 assert.equal(await page.getByTestId('service-probes').count(),0)
 await page.waitForTimeout(500)
 assert.equal(await page.getByTestId('probe-overview-summary').count(),0,'Unconfigured overview has no empty probe panel')
 let backgroundProbeReads=0
 const observeProbe=r=>{if(new URL(r.url()).pathname==='/api/v1/probes')backgroundProbeReads++}
 await page.locator('.dashboard-tabs').getByRole('button',{name:'Event history',exact:true}).click()
 page.on('request',observeProbe);await page.waitForTimeout(5500);page.off('request',observeProbe)
 assert.equal(backgroundProbeReads,0,'Leaving service checks must stop its private polling')
 assert.equal(await page.getByTestId('service-probes').count(),0)
 await page.goto(base+`/#/nodes?node=${node}&section=probes`)
 await page.getByTestId('service-probes').waitFor()
 await page.reload();await page.getByTestId('service-probes').waitFor()
 assert.ok(page.url().includes('section=probes'),'Dedicated section survives reload')
 // Existing Tailscale is simulated at the API boundary; no network setup,
 // SSH connection or real deployment is performed by this browser fixture.
 const peers=['peer-a','peer-b'].map(identity=>({identity,name:identity,os:'linux',class:'linux',online:true,ips:['100.64.0.1'],managed:false}))
 await page.route('**/api/v1/discovery/tailscale',route=>route.fulfill({json:{peers,discoveredAt:new Date().toISOString()}}))
 await page.route('**/api/v1/discovery/deployments',route=>route.request().method()==='POST'?route.fulfill({json:{taskId:'browser-deployment',peerIdentity:'peer-b',peerName:'peer-b',status:'queued',phase:'accepted',createdAt:new Date().toISOString(),updatedAt:new Date().toISOString()}}):route.fulfill({json:{tasks:[]}}))
 let releaseFingerprint,fingerprintStarted
 const fingerprintHeld=new Promise(r=>releaseFingerprint=r),fingerprintPending=new Promise(r=>fingerprintStarted=r)
 await page.route('**/api/v1/discovery/tailscale/host-key',async route=>{if(route.request().postDataJSON().peerIdentity==='peer-a'){fingerprintStarted();await fingerprintHeld};await route.fulfill({json:{fingerprint:'SHA256:fixture-'+route.request().postDataJSON().peerIdentity}})})
 await page.goto(base+'/#/discovery');await page.locator('.peer-card').first().waitFor()
 await page.locator('.peer-card').first().locator('button').click()
 await page.locator('.hostkey-panel .secondary-button').click();await fingerprintPending
 await page.locator('.peer-card').nth(1).locator('button').click();releaseFingerprint()
 await page.waitForFunction(()=>!document.querySelector('.hostkey-panel .secondary-button')?.disabled)
 assert.equal(await page.locator('[data-testid=ssh-fingerprint]').count(),0,'Late SSH fingerprint cannot be assigned to a different peer')
 await page.locator('.hostkey-panel .secondary-button').click();await page.locator('[data-testid=ssh-fingerprint]').waitFor()
 await layouts('discovery')
 await page.locator('.deploy-form input[autocomplete=username]').fill('root')
 await page.locator('.deploy-form input[type=password]').fill('browser-only-fixture')
 await page.locator('.deploy-form input[inputmode=url]').first().fill('https://core.example')
 await page.locator('.hostkey-panel input[type=checkbox]').first().check()
 const accepted=page.waitForResponse(r=>r.url().endsWith('/discovery/deployments')&&r.request().method()==='POST')
 await page.locator('.hostkey-panel .primary-button').click();assert.equal((await accepted).status(),200)
 await page.locator('[data-testid=deployment-task]').waitFor()
 let latePolls=0
 const trackPoll=r=>{if(new URL(r.url()).pathname==='/api/v1/discovery/deployments/browser-deployment')latePolls++}
 page.on('request',trackPoll)
 await page.evaluate(()=>{location.hash='/guest'})
 await page.locator('[data-testid=guest-display]').waitFor();await page.waitForTimeout(1800)
 page.off('request',trackPoll);assert.equal(latePolls,0,'Unmounted deployment page must stop private polling')
 await page.goto(base+'/#/nodes');await page.locator('.nodes-dashboard').waitFor()
 console.log('Review regressions passed: dark-theme persistence, Compose project isolation/stale validation/stable retry, probe revision conflict/deleted editor, SSH fingerprint identity and deployment polling cleanup; 20 settings/Compose/probe-overview/probes/discovery layouts.')
}
