const {selectValue,selectedValue}=require('./arc-controls.cjs')
// Real Core + browser behavior checks. The isolated Core has no Agent and no
// privileged update/deployment service. Synthetic routes test transport loss
// and future operation discovery without performing host-side effects.
const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const net = require('node:net')
const { spawn, execFileSync } = require('node:child_process')
const { chromium } = require('playwright')
const root = path.resolve(__dirname, '../..')
const binary = path.join(root, '.build/nodedance')
const artifacts = path.join(root, '.build/interface-audit')
fs.mkdirSync(artifacts, {recursive:true})


async function port() {
  const listener = net.createServer()
  await new Promise(resolve=>listener.listen(0,'127.0.0.1',resolve))
  const result=listener.address().port
  await new Promise(resolve=>listener.close(resolve))
  return result
}

;(async()=>{
  const dataDir=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-interface-audit-'))
  const base=`http://127.0.0.1:${await port()}`
  const core=spawn(binary,['serve','--dev','--listen',new URL(base).host,'--data-dir',dataDir],{cwd:root,stdio:['ignore','pipe','pipe']})
  let coreOutput=''
  core.stdout.on('data',data=>coreOutput+=data);core.stderr.on('data',data=>coreOutput+=data)
  let browser
  try {
    let ready=false
    for(let attempt=0;attempt<100;attempt++){
      if(core.exitCode!==null)throw new Error('Isolated Core exited: '+coreOutput)
      try{ready=(await fetch(base+'/api/v1/health')).ok}catch{}
      if(ready)break
      await new Promise(resolve=>setTimeout(resolve,100))
    }
    assert.ok(ready,'Isolated Core failed to start')
    browser=await chromium.launch({headless:true})
    const context=await browser.newContext({locale:'en-US'})
    const csrf=await(await context.request.get(base+'/api/v1/auth/csrf')).json()
    const credentials=JSON.parse(fs.readFileSync(path.join(dataDir,'initial-admin.json'),'utf8'))
    const login=await context.request.post(base+'/api/v1/auth/login',{data:{username:credentials.username,password:credentials.password},headers:{Origin:base,'X-CSRF-Token':csrf.token}})
    assert.equal(login.status(),200)
    const headers={Origin:base,'X-CSRF-Token':(await login.json()).csrfToken}
    const catalog=await(await context.request.get(base+'/api/v1/operations')).json()
    const submit={name:'future.submit',description:'Future task',method:'POST',path:'/api/v1/audit/start',parameters:[],query:[],transport:'json',bodySchema:{type:'object',required:['revision'],properties:{revision:{type:'integer'}}},requiresIdempotencyKey:true,headers:{'Idempotency-Key':{type:'string'}},completion:{operation:'future.observe',params:{id:'data.id'},statusPath:'status',terminalStatuses:['succeeded','unknown'],successStatuses:['succeeded']}}
    const observe={name:'future.observe',description:'Future observation',method:'GET',path:'/api/v1/audit/{id}',parameters:['id'],query:[],transport:'json'}
    await context.route('**/api/v1/operations',route=>route.fulfill({json:{...catalog,operations:[...catalog.operations,submit,observe]}}))
    const enrollment=await context.request.post(base+'/api/v1/agents/enrollments',{headers,data:{displayName:'Interface audit',coreUrl:base}})
    assert.equal(enrollment.status(),201)
    const node=(await enrollment.json()).nodeId
    await require('./overview-regressions.cjs')({context,base,node,headers,artifacts,catalog})
    const page=await context.newPage()
    page.setDefaultTimeout(10000)
    const pageErrors=[];page.on('pageerror',error=>pageErrors.push(error.message))
    await page.setViewportSize({width:1,height:1});await page.setContent('<html><body></body></html>')
    const png=await page.screenshot()
    await page.setViewportSize({width:1440,height:1000});await page.goto(base)
    await page.getByRole('button',{name:'Operations',exact:true}).click()
    const select=page.locator('.operation-layout aside button[data-dropdown-field]')
    const form=page.locator('.operation-form form').first()
    const execute=()=>form.locator('button[type=submit]').click()
    const field=name=>form.locator('label').filter({hasText:new RegExp('^'+name)}).locator('input:not([aria-hidden=true]),textarea,button[data-dropdown-field]').first()

    await selectValue(select,'appearance.upload-avatar')
    await form.locator('input[type=file]').setInputFiles({name:'pixel.png',mimeType:'image/png',buffer:png})
    let response=page.waitForResponse(r=>r.url().endsWith('/api/v1/settings/appearance/avatar')&&r.request().method()==='POST')
    await execute();assert.equal((await response).status(),200,'Generic multipart must match real Core')

    await selectValue(select,'nodes.save-preference')
    await field('nodeId').fill(node);await field('targetKind').fill('node');await field('identity').fill('node:'+node)
    await field('alias').fill('');await field('notes').fill('');await field('sshPort').fill('22')
    response=page.waitForResponse(r=>r.url().endsWith('/preferences')&&r.request().method()==='PUT')
    await execute();try{assert.equal((await response).status(),204,'Empty strings and omitted optional fields remain legal')}catch(error){console.error(await form.evaluate(el=>({fields:[...el.querySelectorAll('input,select,textarea')].map(x=>({label:x.parentElement.textContent,value:x.value,valid:x.validity.valid,message:x.validationMessage})),alert:el.parentElement.querySelector('[role=alert]')?.textContent})));throw error}

    const commands=[]
    await page.route('**/api/v1/nodes/*/commands',route=>{
      assert.equal(route.request().method(),'POST')
      commands.push(route.request().headers()['idempotency-key'])
      return route.fulfill({status:503,body:'Agent unavailable'})
    })
    await selectValue(select,'commands.submit')
    await field('nodeId').fill(node);await field('script').fill('true');await field('timeoutSeconds').fill('60');await selectValue(field('confirm'),'true')
    for(let i=0;i<2;i++){
      response=page.waitForResponse(r=>r.url().endsWith('/commands'))
      await execute();await response
    }
    assert.equal(commands.length,2);assert.equal(commands[0],commands[1],'Uncertain retry must reuse intent')
    await form.getByRole('button',{name:'New execution key'}).click()
    response=page.waitForResponse(r=>r.url().endsWith('/commands'));await execute();await response
    assert.notEqual(commands[2],commands[1],'Explicit new execution must obtain a new key')

    await page.reload();await page.getByRole('button',{name:'Operations',exact:true}).click()
    await page.locator('.pending-observations button').filter({hasText:'Restore request key'}).last().click()
    assert.equal(await selectedValue(select),'commands.submit')
    assert.equal(await field('nodeId').inputValue(),node)
    await field('script').fill('true');await field('timeoutSeconds').fill('60');await selectValue(field('confirm'),'true')
    response=page.waitForResponse(r=>r.url().endsWith('/commands'));await execute();await response
    assert.equal(commands[3],commands[2],'Reload must restore the uncertain execution key')

    await selectValue(select,'files.upload');await field('nodeId').fill(node);await field('path').fill('/audit.txt');await field('X-File-Version').fill('observed-version')
    await form.locator('input[type=file]').setInputFiles({name:'audit.txt',mimeType:'text/plain',buffer:Buffer.from('audit')})
    let version
    await page.route('**/api/v1/nodes/*/files/upload?*',route=>{version=route.request().headers()['x-file-version'];return route.fulfill({json:{taskId:'file-audit',status:'succeeded'}})})
    response=page.waitForResponse(r=>r.url().includes('/files/upload?'));await execute();await response
    assert.equal(version,'observed-version')

    await selectValue(select,'channels.create');assert.equal(await field('secret').getAttribute('type'),'password')
    await selectValue(select,'deployments.create');assert.equal(await field('credentials').getAttribute('type'),'password','Nested credentials must not use a visible JSON textarea')

    let submissions=0,polls=0
    await page.route('**/api/v1/audit/start',route=>{assert.equal(route.request().postData(),'{"revision":18446744073709551615}');submissions++;return route.fulfill({status:202,json:{id:'work',status:'queued'}})})
    await page.route('**/api/v1/audit/work',route=>{polls++;return route.fulfill({json:{id:'work',status:polls===1?'running':'succeeded'}})})
    await selectValue(select,'future.submit');await field('revision').fill('18446744073709551615');await form.getByRole('checkbox').last().check();await execute()
    await page.waitForFunction(()=>document.querySelector('[data-testid=operation-result]')?.textContent.includes('succeeded'))
    assert.equal(submissions,1);assert.equal(polls,2,'Observation must not repeat submission')

    await form.getByRole('checkbox').last().uncheck();await execute()
    await page.waitForFunction(()=>JSON.parse(sessionStorage.getItem('nodedance.pending-observations')||'[]').length===1)
    await page.reload();await page.getByRole('button',{name:'Operations',exact:true}).click()
    await page.getByRole('button',{name:'Resume observation',exact:true}).click()
    await page.waitForFunction(()=>document.querySelector('[data-testid=operation-result]')?.textContent.includes('succeeded'))
    assert.equal(submissions,2,'Reload observation must never resubmit')

    const assetResponse=await context.request.get(base+`/api/v1/nodes/${node}/asset`)
    const asset=await assetResponse.json();asset.trafficLimitBytes=0
    const exactAsset=JSON.stringify(asset).replace('"trafficLimitBytes":0','"trafficLimitBytes":18446744073709551615')
    const savedAsset=await context.request.put(base+`/api/v1/nodes/${node}/asset`,{headers:{...headers,'Content-Type':'application/json'},data:exactAsset})
    assert.equal(savedAsset.status(),200)
    await selectValue(select,'nodes.asset');await field('nodeId').fill(node);await execute()
    await page.waitForFunction(()=>document.querySelector('[data-testid=operation-result]')?.textContent.includes('18446744073709551615'))

    // A successful HTTP status without the required JSON payload must become a
    // useful error, and Refresh must retry the metric catalog as well as lists.
    let metricReply='empty',metricReads=0
    await page.route('**/api/v1/metrics/catalog',route=>{
      metricReads++
      if(metricReply==='empty')return route.fulfill({status:200,body:''})
      if(metricReply==='no-content')return route.fulfill({status:204})
      return route.continue()
    })
    await page.getByRole('button',{name:'Alerts',exact:true}).click()
    await page.locator('.alerts-page [role=alert]').filter({hasText:'Server returned an unrecognized response.'}).waitFor()
    assert.equal(metricReads,1)
    metricReply='no-content'
    response=page.waitForResponse(r=>r.url().endsWith('/api/v1/metrics/catalog'))
    await page.getByRole('button',{name:'Refresh data',exact:true}).click();assert.equal((await response).status(),204)
    await page.locator('.alerts-page [role=alert]').filter({hasText:'Server returned an unrecognized response.'}).waitFor()
    assert.equal(metricReads,2)
    metricReply='valid'
    response=page.waitForResponse(r=>r.url().endsWith('/api/v1/metrics/catalog'))
    await page.getByRole('button',{name:'Refresh data',exact:true}).click();assert.equal((await response).status(),200)
    await page.waitForFunction(()=>!document.querySelector('.alerts-page [role=alert]'))
    assert.equal(metricReads,3,'Refresh must recover the catalog without reentering the page')
    for(const theme of ['light','dark'])for(const width of [390,1440]){
      await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
      await page.evaluate(()=>window.scrollTo(0,0));await page.waitForTimeout(350)
      assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Alert layout must not overflow')
      await page.screenshot({path:path.join(artifacts,`alerts-${theme}-${width}.png`),fullPage:true})
    }
    await page.setViewportSize({width:1440,height:1000})
    await page.getByRole('button',{name:'Alert rules',exact:true}).click()
    const ruleForm=page.locator('.alert-form-card').first()
    await ruleForm.locator('select').nth(1).selectOption('metric')
    const metricSelect=ruleForm.locator('select').nth(2)
    const metrics=await(await context.request.get(base+'/api/v1/metrics/catalog')).json()
    assert.deepEqual((await metricSelect.locator('option').evaluateAll(options=>options.map(option=>option.value))).sort(),Object.keys(metrics.metrics).sort())
    await metricSelect.selectOption('swap.used_percent');await ruleForm.locator('input').first().fill('Browser metric rule')
    response=page.waitForResponse(r=>r.url().endsWith('/api/v1/alerts/rules')&&r.request().method()==='POST')
    await ruleForm.locator('button[type=submit]').click();assert.equal((await response).status(),200,'Actual metric rule must be accepted')

    for(const theme of ['light','dark'])for(const width of [390,1440]){
      await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
      await page.evaluate(()=>window.scrollTo(0,0));await page.waitForTimeout(350)
      assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Alert rule layout must not overflow')
      await page.screenshot({path:path.join(artifacts,`alert-rules-${theme}-${width}.png`),fullPage:true})
    }
    await page.setViewportSize({width:1440,height:1000})

    let updateSubmissions=0,rollbacks=0
    const updateJob={id:'audit-update',component:'core',current:'core-v0.1.12',target:'core-v0.1.13',status:'succeeded',stage:'complete',started:'2026-10-12T00:00:00Z',finished:'2026-10-12T00:00:01Z'}
    let holdUpdateCheck, updateCheckStarted;
    await page.route('**/api/v1/updates/check?*',async route=>{if(holdUpdateCheck){updateCheckStarted();await holdUpdateCheck;}return route.fulfill({json:{component:'core',current:'core-v0.1.12',latest:'core-v0.1.13',available:true,configured:true,mode:'docker',release:{url:'https://github.com/CST-Cat/NodeDance/releases/tag/core-v0.1.13',notes:'UI audit'}}})})
    await page.route('**/api/v1/updates/status?*',route=>route.fulfill({json:{jobs:updateSubmissions?[updateJob]:[]}}))
    await page.route('**/api/v1/updates/apply',route=>{assert.deepEqual(route.request().postDataJSON(),{component:'core',confirm:true,version:'core-v0.1.13'});updateSubmissions++;return route.fulfill({json:updateJob})})
    await page.route('**/api/v1/updates/rollback',route=>{assert.deepEqual(route.request().postDataJSON(),{component:'core',confirm:true,jobId:'audit-update'});rollbacks++;return route.fulfill({json:updateJob})})
    await page.getByRole('button',{name:'Updates',exact:true}).click()
    const center=page.locator('.update-center')
    await center.locator('.update-confirm .primary-button').waitFor()
    assert.equal(await center.getByRole('checkbox').count(),0,'Ordinary update must have no extra confirmation')
    let releaseUpdateCheck;holdUpdateCheck=new Promise(resolve=>releaseUpdateCheck=resolve);const checkStarted=new Promise(resolve=>updateCheckStarted=resolve);
    await center.getByRole('button',{name:'Check updates',exact:true}).click();await checkStarted;
    assert.ok(await center.locator('.update-details').isVisible(),'Refresh retains the existing release check');
    assert.ok(await center.locator('.update-confirm .primary-button').isDisabled(),'Retained check cannot submit until refreshed');
    await center.locator('.update-card [aria-busy=true]').waitFor();
    releaseUpdateCheck();holdUpdateCheck=undefined;
    await center.locator('.update-confirm .primary-button:not(:disabled)').waitFor();

    response=page.waitForResponse(r=>r.url().endsWith('/updates/apply'))
    await center.locator('.update-confirm .primary-button').click();assert.equal((await response).status(),200)
    assert.equal(updateSubmissions,1)
    await center.locator('.update-job .management-task[data-status=succeeded]').waitFor();
    assert.equal(await center.locator('.update-job progress').count(),0,'Stage-only updates do not fabricate percentages');
    await center.locator('.update-job button').click()
    assert.ok(await center.locator('.update-confirm .primary-button').isDisabled(),'Rollback still requires explicit confirmation')
    await center.getByRole('checkbox').check()
    response=page.waitForResponse(r=>r.url().endsWith('/updates/rollback'))
    await center.locator('.update-confirm .primary-button').click();await response;assert.equal(rollbacks,1)
    for(const theme of ['light','dark'])for(const width of [390,1440]){
      await page.setViewportSize({width,height:1000})
      await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
      await page.evaluate(()=>window.scrollTo(0,0));await page.waitForTimeout(350)
      assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Update layout must not overflow horizontally')
      await page.screenshot({path:path.join(artifacts,`updates-${theme}-${width}.png`),fullPage:true})
    }
    await page.setViewportSize({width:1440,height:1000});await page.waitForTimeout(350)
    await page.getByRole('button',{name:'Operations',exact:true}).click();await selectValue(select,'files.upload')
    for(const theme of ['light','dark'])for(const width of [390,1440]){
      await page.setViewportSize({width,height:1000})
      await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
      await page.evaluate(()=>window.scrollTo(0,0));await page.waitForTimeout(350)
      assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Layout must not overflow horizontally')
      await page.screenshot({path:path.join(artifacts,`operations-${theme}-${width}.png`),fullPage:true})
      assert.ok(await page.locator('.operation-form').isVisible())
    }
    await page.setViewportSize({width:1440,height:1000})
    const nodeList=await(await context.request.get(base+'/api/v1/nodes')).json()
    await page.route('**/api/v1/nodes',route=>route.fulfill({json:{...nodeList,nodes:nodeList.nodes.map(n=>({...n,status:'online'}))}}))
    const settingFields=catalog.operations.find(o=>o.name==='agents.configure').bodySchema.properties
    const settings=Object.fromEntries(Object.entries(settingFields).filter(([,f])=>f.default!==undefined).map(([k,f])=>[k,f.default]))
    await page.route('**/api/v1/nodes/*/agent-settings',route=>route.fulfill({json:{status:200,settings}}))
    await page.getByRole('button',{name:'Host management',exact:true}).click()
    for(const tab of ['Agent settings','Assets and quotas']){
      await page.locator('.host-page [role=tablist]').getByRole('tab',{name:tab,exact:true}).click()
      if(tab==='Agent settings')await page.waitForFunction(()=>[...document.querySelectorAll('.contract-fields label')].find(x=>x.textContent.includes('Command history retention'))?.parentElement.querySelector('input')?.value==='90')
      if(tab==='Assets and quotas'){
        const quota=page.getByLabel('Traffic quota (bytes)',{exact:true})
        await quota.waitFor();assert.equal(await quota.inputValue(),'18446744073709551615')
        await quota.fill('9007199254740993')
        const purchase=page.getByLabel('Purchase date',{exact:true})
        await purchase.fill('2026-10-13T00:00:00Z')
        response=page.waitForResponse(r=>r.url().endsWith('/asset')&&r.request().method()==='PUT')
        await page.locator('.host-card form button.primary-button').click()
        const saved=await response;assert.equal(saved.status(),200);assert.ok(saved.request().postData().includes('"trafficLimitBytes":9007199254740993'))
        assert.equal((await saved.json()).purchasedAt,'2026-10-13T00:00:00Z')
        await purchase.fill('')
        response=page.waitForResponse(r=>r.url().endsWith('/asset')&&r.request().method()==='PUT')
        await page.locator('.host-card form button.primary-button').click()
        const clearedPurchase=await response;assert.equal(clearedPurchase.status(),200);assert.equal((await clearedPurchase.json()).purchasedAt,undefined)
      }
      for(const theme of ['light','dark'])for(const width of [390,1440]){
        await page.setViewportSize({width,height:1000})
        await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
        await page.waitForTimeout(350)
        assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Host forms must not overflow')
        await page.screenshot({path:path.join(artifacts,`host-${tab.split(' ')[0]}-${theme}-${width}.png`),fullPage:true})
      }
      await page.setViewportSize({width:1440,height:1000})
    }
    // Specialized batch forms reuse the shared execution intent after a lost
    // response and reload. Only a completed submission starts a fresh intent.
    for(const [tab,name,route] of [['Batch scripts','commands.batch','**/api/v1/commands/batches'],['Multi-node probes','probes.batch','**/api/v1/probes/batch']]){
      const batchKeys=[]
      await page.route(route,async intercepted=>{
        if(intercepted.request().method()!=='POST'){await intercepted.continue();return}
        batchKeys.push(intercepted.request().headers()['idempotency-key'])
        if(batchKeys.length===1)await intercepted.fulfill({status:503,body:'Simulated lost batch response'})
        else await intercepted.fulfill({status:201,json:{id:'browser-batch-fixture',results:[]}})
      })
      const fillBatch=async()=>{
        await page.getByRole('button',{name:'Host management',exact:true}).click()
        await page.locator('.host-page [role=tablist]').getByRole('tab',{name:tab,exact:true}).click()
        await page.locator('.host-check').getByRole('checkbox').first().check()
        if(name==='commands.batch'){
          await page.locator('.contract-fields textarea').fill('printf isolated-browser-fixture')
          await page.getByRole('checkbox',{name:'Confirm root execution',exact:true}).check()
        }
        await page.locator('.host-page form .primary-button').waitFor({state:'visible'})
      }
      await fillBatch()
      let batchResponse=page.waitForResponse(r=>r.request().method()==='POST'&&r.url().endsWith(name==='commands.batch'?'/commands/batches':'/probes/batch'))
      await page.locator('.host-page form .primary-button').click();assert.equal((await batchResponse).status(),503)
      await page.locator('.host-error').filter({hasText:'Simulated lost batch response'}).waitFor()
      await page.reload();await fillBatch()
      batchResponse=page.waitForResponse(r=>r.request().method()==='POST'&&r.url().endsWith(name==='commands.batch'?'/commands/batches':'/probes/batch'))
      await page.locator('.host-page form .primary-button').click();assert.equal((await batchResponse).status(),201)
      await page.locator('.host-page form .primary-button').waitFor()
      await page.waitForFunction(()=>!document.querySelector('.host-page form .primary-button')?.disabled)
      assert.equal(batchKeys[0],batchKeys[1],`${name} must retain uncertain execution identity across reload`)
      batchResponse=page.waitForResponse(r=>r.request().method()==='POST'&&r.url().endsWith(name==='commands.batch'?'/commands/batches':'/probes/batch'))
      await page.locator('.host-page form .primary-button').click();await batchResponse
      assert.notEqual(batchKeys[1],batchKeys[2],`${name} must permit a fresh execution after success`)
      await page.unroute(route)
    }
    // Real browser Back/Forward and reload must restore views and tabs without
    // repeating the mutations exercised earlier in this fixture.
    await page.getByRole('button',{name:'Alerts',exact:true}).click()
    await page.locator('.alerts-page').waitFor();assert.equal(new URL(page.url()).hash,'#/alerts')
    await page.getByRole('button',{name:'Alert rules',exact:true}).click()
    await page.waitForFunction(()=>document.querySelector('.alert-tabs [aria-current=page]')?.textContent==='Alert rules')
    const rulesURL=page.url()
    await page.goBack();await page.waitForFunction(()=>document.querySelector('.alert-tabs [aria-current=page]')?.textContent.startsWith('Active alerts'))
    assert.equal(new URL(page.url()).hash,'#/alerts')
    await page.goForward();await page.waitForFunction(()=>document.querySelector('.alert-tabs [aria-current=page]')?.textContent==='Alert rules')
    assert.equal(page.url(),rulesURL)
    await page.reload();await page.locator('.alert-form-card').waitFor();assert.equal(page.url(),rulesURL)
    await page.getByRole('button',{name:'Guest display',exact:true}).click();await page.locator('.guest-settings').waitFor()
    await page.goBack();await page.locator('.alert-form-card').waitFor();assert.equal(page.url(),rulesURL)
    await page.goForward();await page.locator('.guest-settings').waitFor();assert.equal(new URL(page.url()).hash,'#/guest-settings')
    await page.reload();await page.locator('.guest-settings').waitFor()
    await page.getByRole('button',{name:'Host management',exact:true}).click();await page.locator('.host-page [role=tablist]').waitFor()
    await page.locator('.host-page [role=tablist]').getByRole('tab',{name:'Assets and quotas',exact:true}).click()
    await page.waitForFunction(()=>location.hash.includes('tab=assets'))
    await page.goBack();await page.waitForFunction(()=>!location.hash.includes('tab='))
    await page.goForward();await page.waitForFunction(()=>document.querySelector('.host-page [role=tab][aria-selected=true]')?.textContent==='Assets and quotas')
    await page.reload();await page.waitForFunction(()=>document.querySelector('.host-page [role=tab][aria-selected=true]')?.textContent==='Assets and quotas')
    let permissionReads=0;page.on('request',r=>{if(r.url().endsWith('/api/v1/auth/me'))permissionReads++})
    await page.evaluate(()=>window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true})))
    await page.waitForFunction(()=>document.querySelector('.host-page [role=tab][aria-selected=true]')?.textContent==='Assets and quotas')
    assert.equal(permissionReads,1,'Restoring a cached document must revalidate its administrator session')
    await require('./review-regressions.cjs')({page,context,base,node,headers,artifacts,catalog})
    const controlsPage=await context.newPage()
    controlsPage.setDefaultTimeout(10000)
    try { await require('./theme-regressions.cjs')({page:controlsPage,context,base,headers,artifacts,catalog}); await require('./operation-locale-regressions.cjs')({page:controlsPage,base,catalog,artifacts}); await require('./form-layout-regressions.cjs')({page:controlsPage,base,artifacts}); await require('./ui-controls-regressions.cjs')({page:controlsPage,base,artifacts,catalog}) }
    finally { await controlsPage.close() }
    const dockerPage=await context.newPage()
    dockerPage.setDefaultTimeout(10000)
    await dockerPage.setViewportSize({width:1440,height:1000})
    try { await require('./docker-regressions.cjs')({page:dockerPage,base,node,artifacts,catalog}) }
    finally { await dockerPage.close() }
    const streamPage=await context.newPage()
    streamPage.setDefaultTimeout(10000)
    await streamPage.setViewportSize({width:1440,height:1000})
    try { await require('./stream-panel-regressions.cjs')({page:streamPage,base,node,artifacts,catalog}) }
    finally { await streamPage.close() }
    const composePage=await context.newPage()
    composePage.setDefaultTimeout(10000)
    try { await require('./compose-regressions.cjs')({page:composePage,base,node,artifacts,catalog}) }
    finally { await composePage.close() }
    const feedbackPage=await context.newPage();feedbackPage.setDefaultTimeout(10000)
    try { await require('./feedback-regressions.cjs')({page:feedbackPage,base,artifacts}) }
    finally { await feedbackPage.close() }
    const metricsPage=await context.newPage()
    metricsPage.setDefaultTimeout(10000)
    try { await require('./metrics-regressions.cjs')({page:metricsPage,base,node,artifacts,catalog}) }
    finally { await metricsPage.close() }
    const sshPage=await context.newPage()
    sshPage.setDefaultTimeout(10000)
    try { await require('./ssh-shortcut-regressions.cjs')({page:sshPage,base,headers,artifacts,catalog}) }
    finally { await sshPage.close() }
    const mobileNavigationPage=await context.newPage();mobileNavigationPage.setDefaultTimeout(10000)
    try { await require('./mobile-navigation-regressions.cjs')({page:mobileNavigationPage,base,artifacts}) }
    finally { await mobileNavigationPage.close() }
    const navigationPage=await context.newPage()
    navigationPage.setDefaultTimeout(10000)
    try { await require('./node-navigation-regressions.cjs')({page:navigationPage,base,node,artifacts,headers,catalog}) }
    finally { await navigationPage.close() }
    const enrollmentPage=await context.newPage()
    enrollmentPage.setDefaultTimeout(10000)
    await enrollmentPage.setViewportSize({width:1440,height:1000})
    try { await require('./enrollment-regressions.cjs')({page:enrollmentPage,base,artifacts,catalog}) }
    finally { await enrollmentPage.close() }
    if(await page.getByTestId('node-detail-navigation').count())await page.getByRole('button',{name:'Back to home',exact:true}).click()
    await page.getByRole('button',{name:'Nodes',exact:true}).click();await page.locator('.nodes-dashboard').waitFor()
    await page.getByRole('button',{name:'Alerts',exact:true}).click();await page.locator('.alerts-page').waitFor()
    await page.locator('.account-trigger').click();await page.locator('.account-panel').getByRole('menuitem',{name:'Sign out',exact:true}).click();await page.locator('#login-title').waitFor()
    await page.goBack();await page.locator('#login-title').waitFor()
    assert.equal(await page.locator('#workspace-navigation').count(),0,'Back after logout must not restore a private page')
    assert.equal(updateSubmissions,1);assert.equal(rollbacks,1);assert.equal(submissions,2,'History restoration must not resubmit tasks')
    assert.deepEqual(pageErrors,[])
    console.log(`Real Core/Web interface checks passed (${catalog.operations.length} discovered operations; multipart, empty fields, retry intent, version header, secrets, completion, metric rule, direct update, confirmed rollback; 24 layout snapshots).`)
  }finally{
    if(browser)await browser.close()
    core.kill('SIGTERM')
    if(core.exitCode===null)await new Promise(resolve=>{core.once('exit',resolve);setTimeout(()=>{core.kill('SIGKILL');resolve()},5000).unref()})
    fs.rmSync(dataDir,{recursive:true,force:true})
  }
})().catch(error=>{console.error(error);process.exitCode=1})
