const {fillExpanded}=require('./expanded-textarea.cjs')
const assert=require('node:assert/strict')
const {selectValue}=require('./arc-controls.cjs')
const {setBrowserLanguage}=require('./browser-language.cjs')
module.exports=async function({page,context,base,node,headers,catalog}){
    let response;
    const op=name=>{const value=catalog.operations.find(operation=>operation.name===name);assert.ok(value,name);return value};
    const assetPath=op('nodes.asset').path.replace('{nodeId}',node);
    const originalAsset=await(await context.request.get(base+assetPath)).json();originalAsset.trafficLimitBytes=0;
    const seeded=await context.request.put(base+assetPath,{headers:{...headers,'Content-Type':'application/json'},data:JSON.stringify(originalAsset).replace('"trafficLimitBytes":0','"trafficLimitBytes":18446744073709551615')});assert.equal(seeded.status(),200);
    const nodeList=await(await context.request.get(base+'/api/v1/nodes')).json()
    await page.route('**/api/v1/nodes',route=>route.fulfill({json:{...nodeList,nodes:nodeList.nodes.map(n=>({...n,status:'online'}))}}))
    const settingFields=catalog.operations.find(o=>o.name==='agents.configure').bodySchema.properties
    const settings=Object.fromEntries(Object.entries(settingFields).filter(([,f])=>f.default!==undefined).map(([k,f])=>[k,f.default]))
    await page.route('**/api/v1/nodes/*/agent-settings',route=>route.fulfill({json:{status:200,settings}}))
    await page.goto(base+'/#/host-management?node='+node)
    for(const tab of ['Agent settings','Assets and quotas']){
      await page.locator('.host-page [role=tablist]').getByRole('tab',{name:tab,exact:true}).click()
      if(tab==='Agent settings')await page.waitForFunction(()=>[...document.querySelectorAll('.contract-fields label')].find(x=>x.textContent.includes('Command history retention'))?.parentElement.querySelector('input')?.value==='90')
      if(tab==='Assets and quotas'){
        const quota=page.locator('.host-page [role=tabpanel][data-state=active]').getByLabel('Traffic quota (bytes)',{exact:true})
        await quota.waitFor();assert.equal(await quota.inputValue(),'18446744073709551615')
        await quota.fill('9007199254740993')
        const purchase=page.locator('.host-page [role=tabpanel][data-state=active]').getByLabel('Purchase date',{exact:true})
        await purchase.fill('2026-10-13T00:00:00Z')
        response=page.waitForResponse(r=>r.url().endsWith('/asset')&&r.request().method()==='PUT')
        await page.locator('.host-settings-form button[type=submit]').click()
        const saved=await response;assert.equal(saved.status(),200);assert.ok(saved.request().postData().includes('"trafficLimitBytes":9007199254740993'))
        assert.equal((await saved.json()).purchasedAt,'2026-10-13T00:00:00Z')
        await purchase.fill('')
        response=page.waitForResponse(r=>r.url().endsWith('/asset')&&r.request().method()==='PUT')
        await page.locator('.host-settings-form button[type=submit]').click()
        const clearedPurchase=await response;assert.equal(clearedPurchase.status(),200);assert.equal((await clearedPurchase.json()).purchasedAt,undefined)
      }
      for(const [theme,width] of [['light',1440],['dark',390]]){
        await page.setViewportSize({width,height:1000})
        await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
        await page.waitForTimeout(350)
        assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Host forms must not overflow')

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
        await page.locator('.host-page [role=tabpanel][data-state=active] .host-check').getByRole('checkbox').first().check()
        if(name==='commands.batch'){
          await fillExpanded(page.locator('.host-page [role=tabpanel][data-state=active] .contract-fields [data-expanded-textarea-trigger]'),'printf isolated-browser-fixture')
          await page.getByRole('checkbox',{name:'Confirm root execution',exact:true}).check()
        }
        await page.locator('.host-page [role=tabpanel][data-state=active] form .primary-button').waitFor({state:'visible'})
      }
      await fillBatch()
      let batchResponse=page.waitForResponse(r=>r.request().method()==='POST'&&r.url().endsWith(name==='commands.batch'?'/commands/batches':'/probes/batch'))
      await page.locator('.host-page [role=tabpanel][data-state=active] form .primary-button').click();assert.equal((await batchResponse).status(),503)
      await page.locator('.host-error').filter({hasText:'Simulated lost batch response'}).waitFor()
      await page.reload();await fillBatch()
      batchResponse=page.waitForResponse(r=>r.request().method()==='POST'&&r.url().endsWith(name==='commands.batch'?'/commands/batches':'/probes/batch'))
      await page.locator('.host-page [role=tabpanel][data-state=active] form .primary-button').click();assert.equal((await batchResponse).status(),201)
      await page.locator('.host-page [role=tabpanel][data-state=active] form .primary-button').waitFor()
      await page.waitForFunction(()=>!document.querySelector('.host-page [role=tabpanel][data-state=active] form .primary-button')?.disabled)
      assert.equal(batchKeys[0],batchKeys[1],`${name} must retain uncertain execution identity across reload`)
      batchResponse=page.waitForResponse(r=>r.request().method()==='POST'&&r.url().endsWith(name==='commands.batch'?'/commands/batches':'/probes/batch'))
      await page.locator('.host-page [role=tabpanel][data-state=active] form .primary-button').click();await batchResponse
      assert.notEqual(batchKeys[1],batchKeys[2],`${name} must permit a fresh execution after success`)
      await page.unroute(route)
    }

    const active=()=>page.locator('.host-page [role=tabpanel][data-state=active]');
    const choose=async value=>{const target=page.locator('.host-page [role=tab][data-value='+value+']');await target.focus();await page.keyboard.press('Enter');await page.waitForFunction(value=>document.querySelector('.host-page [role=tab][aria-selected=true]')?.getAttribute('data-value')===value,value);await page.waitForFunction(()=>!document.querySelector('.host-page [role=tabpanel][data-state=inactive]'))};
    await choose('commands');await active().locator('.host-check').getByRole('checkbox').first().check();await fillExpanded(active().locator('.contract-fields [data-expanded-textarea-trigger]'),'printf isolated-result');await active().getByRole('checkbox',{name:'Confirm root execution',exact:true}).check();
    const batchId='isolated-host-batch',batchPath=op('commands.batch').path;
    const batch={id:batchId,results:[{nodeId:node,jobId:'isolated-job',stale:true,job:{id:'isolated-job',status:'running',output:'Isolated command output',truncated:true}}]};
    const batchSubmit=route=>route.request().method()==='POST'?route.fulfill({status:201,json:batch}):route.fallback();await page.route('**'+batchPath,batchSubmit);
    let submitted=page.waitForResponse(r=>new URL(r.url()).pathname===batchPath&&r.request().method()==='POST');await active().locator('form .primary-button').click();assert.equal((await submitted).status(),201);await active().getByText('Isolated command output',{exact:true}).waitFor();await active().getByText('Output reached its size limit.',{exact:true}).waitFor();
    for(const [name,status] of [['commands.batch-status','succeeded'],['commands.batch-cancel','canceled']]){
      const target=op(name),path=target.path.replace('{batchId}',batchId);const handler=route=>route.fulfill({json:{...batch,results:batch.results.map(result=>({...result,job:{...result.job,status,exitCode:0}}))}});await page.route('**'+path,handler);
      response=page.waitForResponse(r=>new URL(r.url()).pathname===path&&r.request().method()===target.method);await active().getByRole('button',{name:name.endsWith('cancel')?'Cancel running jobs':'Refresh results',exact:true}).click();assert.ok((await response).ok());await page.unroute('**'+path,handler);
    }
    await page.unroute('**'+batchPath,batchSubmit);
    await choose('enrollment');const grantForm=active().locator('form');await grantForm.waitFor();
    const grantOp=op('agents.create-grant');response=page.waitForResponse(r=>new URL(r.url()).pathname===grantOp.path&&r.request().method()===grantOp.method);await grantForm.locator('button[type=submit]').click();const grantReply=await response;assert.equal(grantReply.status(),201);const issued=await grantReply.json();assert.ok(issued.grant.token);await active().locator('.host-grant-result').waitFor();assert.equal(await active().locator('.host-grant-result pre').count(),3,'Token and both generated installer commands remain available');assert.ok(!(await page.evaluate(()=>JSON.stringify({...localStorage,...sessionStorage}))).includes(issued.grant.token),'Enrollment secret stays out of browser storage');
    const revoke=op('agents.revoke-grant'),revokePath=revoke.path.replace('{grantId}',issued.grant.id);response=page.waitForResponse(r=>new URL(r.url()).pathname===revokePath&&r.request().method()===revoke.method);await active().locator('.host-grant-list').getByRole('button',{name:'Revoke key',exact:true}).click();assert.ok((await response).ok());await page.waitForFunction(()=>document.querySelector('.host-grant-list button')?.disabled);
    for(const [language,theme,width] of [['en-US','light',1440],['zh-CN','dark',390]]){
      await setBrowserLanguage(page,language);await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);
      for(const value of ['settings','assets','commands','probes','enrollment']){
        await choose(value);await page.waitForFunction(()=>!document.querySelector('.host-page [role=tabpanel][data-state=active] form .primary-button')?.disabled || document.querySelector('.host-page [role=tabpanel][data-state=active] .contract-field'));
        const fields=active().locator('.contract-field');assert.ok(await fields.count()>0,'Expected discovered visible contract fields');
        const geometry=await fields.evaluateAll(rows=>rows.map(row=>{const label=row.querySelector('label[for]'),control=label&&document.getElementById(label.htmlFor);if(!label||!control||!control.getClientRects().length)throw Error('Missing visible contract control');const a=label.getBoundingClientRect(),b=control.getBoundingClientRect();return {label:label.textContent,left:a.left,labelRight:a.right,x:b.x,right:b.right,width:b.width,checkbox:control.getAttribute('role')==='checkbox'}}));
        assert.ok(geometry.every(box=>box.width>0&&box.x>=box.labelRight&&box.right<=width+1),JSON.stringify({value,geometry}));const inputs=geometry.filter(box=>!box.checkbox);assert.ok(Math.max(...inputs.map(box=>box.right))-Math.min(...inputs.map(box=>box.right))<1,'Inputs, selects and textareas share a right edge');assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Host view fits the viewport');
      }
    }
    await setBrowserLanguage(page,'en-US');await page.setViewportSize({width:1440,height:1000});
    await require('./host-read-regressions.cjs')({page,base,catalog});

    console.log('host-management-regressions passed.');
};
