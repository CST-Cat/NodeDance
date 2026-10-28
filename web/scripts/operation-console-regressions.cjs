const {fillExpanded}=require('./expanded-textarea.cjs')
const assert=require('node:assert/strict')
const {selectValue,selectedValue}=require('./arc-controls.cjs')
const {setBrowserLanguage}=require('./browser-language.cjs')
module.exports=async function({page,context,base,node,headers,catalog}){
    const submit={name:'future.submit',description:'Future task',method:'POST',path:'/api/v1/audit/start',parameters:[],query:[],transport:'json',bodySchema:{type:'object',required:['revision'],properties:{revision:{type:'integer'}}},requiresIdempotencyKey:true,headers:{'Idempotency-Key':{type:'string'}},completion:{operation:'future.observe',params:{id:'data.id'},statusPath:'status',terminalStatuses:['succeeded','unknown'],successStatuses:['succeeded']}}
    const observe={name:'future.observe',description:'Future observation',method:'GET',path:'/api/v1/audit/{id}',parameters:['id'],query:[],transport:'json'}
    const socket={name:'future.socket',description:'Future stream',method:'GET',path:'/api/v1/audit/socket',parameters:[],query:[],transport:'websocket',duplex:true}
    let added=[],catalogVersion=1
    await context.route('**/api/v1/operations',route=>route.fulfill({headers:{ETag:`"audit-${catalogVersion}"`},json:{...catalog,operations:[...catalog.operations,submit,observe,socket,...added]}}))
    const png=Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR4nGP4DwQACfsD/fteaysAAAAASUVORK5CYII=','base64')
    await page.setViewportSize({width:1440,height:1000});await page.goto(base)
    await page.getByRole('button',{name:'Operations',exact:true}).click()
    const select=page.locator('.operation-layout aside button[data-dropdown-field]')
    await select.waitFor()
    assert.equal(await select.count(),1,'Expected a single discovered operation picker')
    const form=page.locator('.operation-form form').first()
    const execute=()=>form.locator('button[type=submit]').click()
    const field=name=>form.locator('label').filter({hasText:new RegExp('^'+name)}).locator('input:not([aria-hidden=true]),[data-expanded-textarea-trigger],button[data-dropdown-field]').first()

    await selectValue(select,'future.submit');await field('revision').fill('123')
    added=[{...observe,name:'future.added'}];catalogVersion++
    const refreshed=page.waitForResponse(r=>r.url().endsWith('/api/v1/operations'))
    await page.evaluate(()=>document.dispatchEvent(new Event('visibilitychange')));await refreshed
    await select.click();await page.locator('[role=menuitemradio][data-option-value="future.added"]').waitFor()
    await page.keyboard.press('Escape');await page.getByRole('menu').waitFor({state:'detached'})
    assert.equal(await field('revision').inputValue(),'123','Revalidation preserves the current draft')

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
    await field('nodeId').fill(node);await field('timeoutSeconds').fill('60');await selectValue(field('confirm'),'true')
    await field('script').fill('true')
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

    await selectValue(select,'channels.create');assert.equal(await field('secret').getAttribute('type'),'password');await fillExpanded(field('config'),'{"webhookUrl":"https://example.invalid/hook"}');const configEditor=await require('./expanded-textarea.cjs').editorFor(field('config'));assert.equal(await configEditor.inputValue(),'{"webhookUrl":"https://example.invalid/hook"}');await page.keyboard.press('Escape');await configEditor.waitFor({state:'detached'})
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


    let releaseHealth,healthStarted;
    const healthGate=new Promise(resolve=>releaseHealth=resolve),started=new Promise(resolve=>healthStarted=resolve);
    await page.route('**/api/v1/health',async route=>{healthStarted();await healthGate;await route.fulfill({json:{status:'ok',version:'isolated'}})});
    await selectValue(select,'core.health');await execute();await started;
    await form.getByRole('button',{name:'Stop observing (task continues)',exact:true}).click();await form.getByRole('button',{name:'Execute',exact:true}).waitFor();releaseHealth();
    assert.ok(await form.getByRole('button',{name:'Execute',exact:true}).isEnabled(),'Cancellation restores submission controls');
    await page.unroute('**/api/v1/health');
    const frames=[];
    await page.routeWebSocket('**/api/v1/audit/socket',ws=>{ws.onMessage(message=>{frames.push(String(message));ws.send(message)})});
    await selectValue(select,'future.socket');await execute();
    const sendForm=page.locator('.operation-stream-form');await sendForm.waitFor();assert.ok(await form.locator('button[type=submit]').isDisabled());
    await fillExpanded(sendForm.getByRole('button',{name:'JSON',exact:true}),'{"fixture":"echo"}');await sendForm.getByRole('button',{name:'Send',exact:true}).click();
    await page.waitForFunction(()=>document.querySelector('[data-testid=operation-result]')?.textContent.includes('echo'));assert.deepEqual(frames,['{"fixture":"echo"}']);
    await form.getByRole('button',{name:'Disconnect',exact:true}).click();await sendForm.waitFor({state:'detached'});assert.ok(await form.locator('button[type=submit]').isEnabled());
    await selectValue(select,'files.upload');
    for(const [theme,width] of [['light',1440],['dark',390]]){
      await setBrowserLanguage(page,theme==='dark'?'zh-CN':'en-US');await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);
      const rows=form.locator('.parameter-row');assert.ok(await rows.count()>=3,'Expected visible target, header and file controls');
      const boxes=await rows.evaluateAll(rows=>rows.map(row=>{const label=row.querySelector('span'),control=row.querySelector('input,[data-expanded-textarea-trigger],button[data-dropdown-field]');if(!label||!control||!control.getClientRects().length)throw Error('Expected visible label and control');const a=label.getBoundingClientRect(),b=control.getBoundingClientRect();return {labelRight:a.right,x:b.x,right:b.right,width:b.width}}));
      assert.ok(boxes.every(box=>box.x>=box.labelRight),'Labels stay left of their actual controls');assert.ok(Math.max(...boxes.map(box=>box.right))-Math.min(...boxes.map(box=>box.right))<1,'Actual controls share their right edge');assert.ok(Math.max(...boxes.map(box=>box.width))-Math.min(...boxes.map(box=>box.width))<1,'Actual controls share their width');
      assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Operation controls must fit the viewport');
    }
    await setBrowserLanguage(page,'en-US');await page.setViewportSize({width:1440,height:1000});
    console.log('operation-console-regressions passed.');
    return {submissions};
};
