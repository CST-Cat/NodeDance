const {closeNavigation,selectNodeSection}=require('./sidebar-navigation.cjs')
// Real browser presentation regressions against the isolated interface Core.
// Compose responses are protocol fixtures; no remote mutation is executed.
const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,context,base,node,headers,artifacts,catalog}){
 async function layouts(name){
  for(const [theme,width] of [['light',1440],['dark',390]]){
   await page.setViewportSize({width,height:1000});await page.waitForTimeout(100)
   await closeNavigation(page)
   await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
   assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`${name} layout fits viewport`)
   if(name==='compose'){
    assert.ok(!(await page.locator('#compose-projects-title').innerText()).includes(node),'Compose title omits node UUID')
    assert.ok(await page.locator('.compose-service li').evaluateAll(rows=>rows.every(row=>{const id=row.querySelector('code').getBoundingClientRect(),state=row.querySelector('[data-state]').getBoundingClientRect();return id.right<=state.left+1})), 'Compose container ID and state do not overlap')
   }

  }
  await page.setViewportSize({width:1440,height:1000})
 }
 await page.goto(base+'/#/settings')
 await page.locator('.theme-options').waitFor()
 await page.emulateMedia({reducedMotion:'reduce'});await page.locator('.theme-options').getByRole('button',{name:'Light',exact:true}).click();await page.waitForFunction(()=>document.documentElement.dataset.theme==='light');assert.equal(await page.locator('.theme-options').evaluate(el=>el.getAnimations({subtree:true}).filter(animation=>animation.playState==='running').length),0,'Reduced motion selection settles immediately');await page.emulateMedia({reducedMotion:'no-preference'})
 await page.locator('.theme-options button').first().focus();await page.keyboard.press('End');assert.equal(await page.locator('.theme-options').getByRole('button',{name:'Light',exact:true}).evaluate(el=>el===document.activeElement),true);await page.keyboard.press('ArrowLeft');assert.equal(await page.locator('.theme-options').getByRole('button',{name:'Dark',exact:true}).evaluate(el=>el===document.activeElement),true);assert.equal(await page.locator('.theme-options button[data-value=dark]').getAttribute('aria-pressed'),'true','Arrow navigation selects the focused theme')
 await page.waitForFunction(()=>document.documentElement.dataset.theme==='dark')
 const profileOperation=catalog.operations.find(o=>o.name==='appearance.update');assert.ok(profileOperation);const saved=page.waitForResponse(r=>new URL(r.url()).pathname===profileOperation.path&&r.request().method()===profileOperation.method)
 await page.locator('.profile-form button[type=submit]').click();assert.equal((await saved).status(),200)
 await page.reload();await page.waitForFunction(()=>document.documentElement.dataset.theme==='dark')
 await layouts('settings')

 const keys=['a'.repeat(64),'b'.repeat(64)]
 const projects=keys.map((key,i)=>({ref:{key,name:'fixture-'+i,workingDirectory:'/fixture-'+i,configFiles:['/fixture-'+i+'/compose.yaml']},configAvailable:true,services:[{name:'homepage',instances:[{containerId:'f97e27dbe0bf'+'1'.repeat(52),containerName:'1Panel-homepage-long-container-name',state:'running',health:'healthy'}]}]}))
 const listRoute='**/api/v1/nodes/*/compose/projects'
 const configRoute='**/api/v1/nodes/*/compose/projects/*/config/0'
 const validateRoute='**/api/v1/nodes/*/compose/projects/*/config/0/validate'
 const submissions=[]
 await page.route(listRoute,route=>route.request().method()==='POST'?(submissions.push(route.request().headers()['idempotency-key']),route.fulfill({status:503,body:'Simulated lost Compose response'})):route.fulfill({json:{projects,dataStale:false,serverTime:new Date().toISOString()}}))
 await page.route(configRoute,route=>route.fulfill({json:{fileIndex:0,content:route.request().url().includes(keys[0])?'services: {} # first':'services: {} # second',sha256:'c'.repeat(64)}}))
 await page.goto(base+`/#/nodes?node=${node}&section=compose`)
 await page.locator('.compose-project-card').first().waitFor()
 const openEditor=async i=>{const clear=page.locator('.compose-selection-bar').getByRole('button',{name:'Clear selection',exact:true});if(await clear.isEnabled())await clear.click();await page.locator('.compose-project-card').nth(i).locator('input[type=checkbox]').check();await page.getByTestId('compose-toolbar').locator('[data-action=edit]').click()}
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
 const preview=editor.getByRole('button',{name:'Review changes',exact:true})
 const draft='services: {} # changed\n# <img src=x onerror=alert(1)>'
 await editor.locator('textarea').fill(draft)
 const previewWrites=[];const observe=request=>{if(!['GET','HEAD'].includes(request.method()))previewWrites.push(request.url())};page.on('request',observe)
 await preview.click();const diff=page.locator('.config-diff-dialog[open]');await diff.waitFor()
 assert.equal(await diff.locator('img').count(),0,'Configuration is escaped, never rendered as HTML')
 assert.ok((await diff.innerText()).includes('<img src=x onerror=alert(1)>'))
 assert.ok((await diff.innerText()).includes('services: {} # second'),'The diff uses the loaded version of the selected file')
 await diff.getByRole('button',{name:'Side by side',exact:true}).click()
 await layouts('diff')
 await page.setViewportSize({width:390,height:1000})
 const splitCells=diff.locator('.diff-split tbody tr').first().locator('td')
 assert.ok((await splitCells.nth(2).boundingBox()).y>(await splitCells.first().boundingBox()).y,'Narrow comparison stacks before/after')
 await page.keyboard.press('Escape');await diff.waitFor({state:'detached'})
 await page.waitForFunction(()=>document.activeElement?.textContent==='Review changes')
 assert.equal(await editor.locator('textarea').inputValue(),draft,'Review preserves the exact unsaved draft')
 assert.ok(await editor.locator('.compose-editor-actions button').nth(1).isDisabled(),'Review cannot validate or authorize a save')
 assert.deepEqual(previewWrites,[],'Review is a browser-local presentation and submits no mutations')
 page.off('request',observe)
 await page.setViewportSize({width:1440,height:1000})


 await page.getByTestId('compose-toolbar').getByRole('button',{name:'New project',exact:true}).click()
 const create=page.locator('.compose-create-form')
 await create.locator('input').first().fill('retry-fixture');await create.locator('input').nth(1).fill('/retry-fixture');await create.locator('textarea').fill('services: {}')
 for(let attempt=0;attempt<2;attempt++){
  await create.getByRole('button',{name:'Create and deploy',exact:true}).click()
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
 await page.locator('.node-detail-navigation button[aria-current=page]').filter({hasText:/^Overview$/}).waitFor()
 await overviewResponse
 assert.equal(await page.getByTestId('service-probes').count(),0)
 await page.waitForTimeout(500)
 assert.equal(await page.getByTestId('probe-overview-summary').count(),0,'Unconfigured overview has no empty probe panel')
 let backgroundProbeReads=0
 const observeProbe=r=>{if(new URL(r.url()).pathname==='/api/v1/probes')backgroundProbeReads++}
 await selectNodeSection(page,'Event history')
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
 await page.goto(base+'/#/agent-enrollment');await page.locator('.peer-card').first().waitFor()
 await page.locator('.peer-card').first().locator('button').click()
 await page.locator('.hostkey-panel .secondary-button').click();await fingerprintPending
 await page.locator('.peer-card').nth(1).locator('button').click();releaseFingerprint()
 await page.waitForFunction(()=>!document.querySelector('.hostkey-panel .secondary-button')?.disabled)
 assert.equal(await page.locator('[data-testid=ssh-fingerprint]').count(),0,'Late SSH fingerprint cannot be assigned to a different peer')
 await page.locator('.hostkey-panel .secondary-button').click();await page.locator('[data-testid=ssh-fingerprint]').waitFor()
 await layouts('discovery')
 await page.locator('.deploy-form input[autocomplete=username]').fill('root')
 await page.locator('.deploy-form input[type=password]').fill('browser-only-fixture')
 await page.locator('.agent-setup-form input[name=coreUrl]').fill('https://core.example')
 await page.locator('.hostkey-panel').getByRole('checkbox').first().check()
 const accepted=page.waitForResponse(r=>r.url().endsWith('/discovery/deployments')&&r.request().method()==='POST')
 await page.locator('.agent-setup-form .primary-button').click();assert.equal((await accepted).status(),200)
 await page.locator('[data-testid=deployment-task]').waitFor()
 await page.locator('[data-testid=deployment-task] .ui-task-progress[data-status=queued]').waitFor();assert.equal(await page.locator('[data-testid=deployment-task] progress').count(),0,'Deployment stages do not invent a denominator')
 let latePolls=0
 const trackPoll=r=>{if(new URL(r.url()).pathname==='/api/v1/discovery/deployments/browser-deployment')latePolls++}
 page.on('request',trackPoll)
 await page.evaluate(()=>{location.hash='/guest'})
 await page.locator('[data-testid=guest-display]').waitFor();await page.waitForTimeout(1800)
 page.off('request',trackPoll);assert.equal(latePolls,0,'Unmounted deployment page must stop private polling')
 await page.goto(base+'/#/nodes');await page.locator('.nodes-dashboard').waitFor()
 console.log("review-regressions passed.")
}
