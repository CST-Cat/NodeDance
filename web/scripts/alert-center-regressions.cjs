const assert=require('node:assert/strict')
const {selectValue}=require('./arc-controls.cjs')
const {setBrowserLanguage}=require('./browser-language.cjs')
module.exports=async function({page,context,base,node,headers,catalog}){
    let response;
    // A successful HTTP status without the required JSON payload must become a
    // useful error, and Refresh must retry the metric catalog as well as lists.
    let metricReply='empty',metricReads=0
    await page.route('**/api/v1/metrics/catalog',route=>{
      metricReads++
      if(metricReply==='empty')return route.fulfill({status:200,body:''})
      if(metricReply==='no-content')return route.fulfill({status:204})
      return route.continue()
    })
    await page.goto(base+'/#/alerts')
    await page.getByRole('tab',{name:'Alert rules',exact:true}).click()
    const selectedTab=page.locator('.alert-tabs [aria-selected=true]');assert.equal(await selectedTab.count(),1);
    assert.equal(await selectedTab.evaluate(el=>getComputedStyle(el).outlineColor),'rgba(0, 0, 0, 0)','Mouse selection does not add a black focus outline');

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
    const ruleForm=page.locator('.alert-form-card').first()
    await require('./arc-controls.cjs').selectValue(ruleForm.locator('#alert-rule-kind'),'metric')
    const metricSelect=ruleForm.locator('#alert-rule-subject')
    const metrics=await(await context.request.get(base+'/api/v1/metrics/catalog')).json()
    assert.deepEqual((await require('./arc-controls.cjs').readOptions(metricSelect)).map(option=>option.value).sort(),Object.keys(metrics.metrics).sort())
    await require('./arc-controls.cjs').selectValue(metricSelect,'swap.used_percent');await ruleForm.locator('input').first().fill('Browser metric rule')
    response=page.waitForResponse(r=>r.url().endsWith('/api/v1/alerts/rules')&&r.request().method()==='POST')
    await ruleForm.locator('button[type=submit]').click();assert.equal((await response).status(),200,'Actual metric rule must be accepted')



    await page.unroute('**/api/v1/metrics/catalog');
    const screen=page.locator('.alerts-page'),tabs=screen.locator('.alert-tabs');
    const op=name=>{const value=catalog.operations.find(op=>op.name===name);assert.ok(value,name);return value};
    const routePath=(name,id)=>op(name).path.replace('{id}',id);
    const waitOperation=(name,id)=>page.waitForResponse(r=>new URL(r.url()).pathname===routePath(name,id)&&r.request().method()===op(name).method);
    const enroll=await context.request.post(base+op('agents.enrollment').path,{headers,data:{displayName:'Other threshold node',coreUrl:base}});assert.equal(enroll.status(),201);const otherNode=(await enroll.json()).nodeId;
    for(const id of [node,otherNode]){const defaults=await context.request.post(base+op('rules.defaults').path,{headers,data:{nodeId:id}});assert.equal(defaults.status(),200)}
    const listRules=async()=> (await(await context.request.get(base+op('rules.list').path)).json()).rules;
    const before=await listRules(),nodeCPU=before.find(rule=>rule.nodeId===node&&rule.kind==='cpu'),otherCPU=before.find(rule=>rule.nodeId===otherNode&&rule.kind==='cpu');assert.ok(nodeCPU&&otherCPU);
    await screen.getByRole('button',{name:'Refresh data',exact:true}).click();await page.waitForFunction(()=>document.querySelector('.alerts-page')?.getAttribute('aria-busy')==='false');
    await selectValue(screen.locator('#alert-rule-filter'),node);const cpuRow=screen.getByRole('row').filter({hasText:nodeCPU.name});await cpuRow.waitFor();assert.equal(await cpuRow.count(),1,'Node filter isolates its existing rule');
    const actionButtons=cpuRow.locator('.alert-rule-actions button');assert.equal(await actionButtons.count(),2);const [editBox,deleteBox]=await actionButtons.evaluateAll(buttons=>buttons.map(button=>{const r=button.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height}}));assert.ok(Math.abs(editBox.y+editBox.height/2-deleteBox.y-deleteBox.height/2)<1,'Edit and delete share a horizontal line '+JSON.stringify({editBox,deleteBox,style:await cpuRow.locator('.alert-rule-actions').evaluate(el=>({display:getComputedStyle(el).display,align:getComputedStyle(el).alignItems,direction:getComputedStyle(el).flexDirection,wrap:getComputedStyle(el).flexWrap}))}));
    const pageScroll=await page.evaluate(()=>scrollY);
    await cpuRow.getByRole('button',{name:'Edit',exact:true}).click();const ruleDialog=screen.getByRole('dialog',{name:'Edit rule',exact:true});await ruleDialog.waitFor();assert.equal(await screen.locator('.alert-rule-form').count(),1,'The editor is in the dialog rather than a second form below');assert.equal(await ruleDialog.locator('.alert-rule-form').count(),1);assert.equal(await page.evaluate(()=>scrollY),pageScroll,'Opening edit does not scroll to the bottom form');assert.equal(await ruleForm.locator('#alert-rule-threshold').inputValue(),'90');assert.equal(await ruleForm.locator('#alert-rule-duration').inputValue(),'300');
    await ruleForm.locator('#alert-rule-threshold').fill('98');await ruleForm.locator('#alert-rule-duration').fill('600');response=waitOperation('rules.update',nodeCPU.id);await ruleForm.getByRole('button',{name:'Save rule',exact:true}).click();const savedReply=await response;assert.equal(savedReply.status(),200,await savedReply.text());const customized=(await savedReply.json()).rule;assert.equal(customized.id,nodeCPU.id);assert.equal(customized.threshold,98);assert.equal(customized.durationSeconds,600);assert.equal(savedReply.request().postDataJSON().revision,nodeCPU.revision);assert.ok(!Object.hasOwn(savedReply.request().postDataJSON(),'id'),'Rule ID stays in the discovered path, not the body');
    const after=await listRules();assert.equal(after.length,before.length,'Editing must not add a competing threshold rule');assert.equal(after.find(rule=>rule.id===otherCPU.id).threshold,90,'Other node retains the default');
    await cpuRow.getByRole('button',{name:'Edit',exact:true}).click();await ruleDialog.waitFor();await ruleForm.locator('#alert-rule-threshold').fill('99');await page.keyboard.press('Escape');await ruleDialog.waitFor({state:'detached'});assert.equal((await listRules()).find(rule=>rule.id===nodeCPU.id).threshold,98,'Escape cancels without submitting');assert.ok(await cpuRow.getByRole('button',{name:'Edit',exact:true}).evaluate(el=>el===document.activeElement),'Closing restores focus to the row edit button');
    await cpuRow.getByRole('button',{name:'Edit',exact:true}).click();await ruleDialog.waitFor();await ruleForm.locator('#alert-rule-threshold').fill('99');const {id:unusedID,createdAt,updatedAt,...currentBody}=customized;const concurrent=await context.request.put(base+routePath('rules.update',nodeCPU.id),{headers,data:{...currentBody,threshold:97}});assert.equal(concurrent.status(),200);const latest=(await concurrent.json()).rule;
    response=waitOperation('rules.update',nodeCPU.id);await ruleForm.getByRole('button',{name:'Save rule',exact:true}).click();assert.equal((await response).status(),409);await ruleDialog.getByRole('alert').waitFor();assert.equal(await ruleForm.locator('#alert-rule-threshold').inputValue(),'99','Conflict retains the local draft');assert.equal((await listRules()).find(rule=>rule.id===nodeCPU.id).threshold,97,'Stale edit cannot overwrite a newer node threshold');
    await ruleDialog.getByRole('button',{name:'Cancel editing',exact:true}).click();await ruleDialog.waitFor({state:'detached'});assert.equal((await listRules()).find(rule=>rule.id===nodeCPU.id).revision,latest.revision,'Cancel does not submit changes');await screen.getByRole('button',{name:'Refresh data',exact:true}).click();await page.waitForFunction(()=>document.querySelector('.alerts-page')?.getAttribute('aria-busy')==='false');
    await cpuRow.getByRole('button',{name:'Edit',exact:true}).click();assert.equal(await ruleForm.locator('#alert-rule-threshold').inputValue(),'97');await ruleForm.locator('#alert-rule-threshold').fill('98');response=waitOperation('rules.update',nodeCPU.id);await ruleForm.getByRole('button',{name:'Save rule',exact:true}).click();assert.equal((await response).status(),200,'A refreshed revision can be retried');
    await selectValue(screen.locator('#alert-rule-filter'),'');
    const ruleRow=screen.getByRole('row').filter({hasText:'Browser metric rule'});await ruleRow.waitFor();assert.ok((await ruleRow.innerText()).includes('> 90'),'Rule condition retains its threshold');
    response=waitOperation('rules.delete',(await(await context.request.get(base+op('rules.list').path)).json()).rules.find(rule=>rule.name==='Browser metric rule').id);
    await ruleRow.getByRole('button',{name:'Delete',exact:true}).click();assert.ok((await response).ok());await ruleRow.waitFor({state:'detached'});
    await tabs.getByRole('tab',{name:'Notification channels',exact:true}).click();const channelForm=screen.locator('.alert-channel-form');await channelForm.waitFor();
    await channelForm.getByLabel('Name',{exact:true}).fill('Isolated SMTP');await selectValue(channelForm.locator('#alert-channel-kind'),'smtp');
    await channelForm.getByLabel('SMTP host',{exact:true}).fill('smtp.example.test');await channelForm.getByLabel('Sender',{exact:true}).fill('NodeDance <alerts@example.test>');await channelForm.getByLabel('Recipient',{exact:true}).fill('运维 <ops@example.test>');
    await channelForm.getByLabel('Username (optional)',{exact:true}).fill('isolated-user');await channelForm.getByLabel('Password / App Password',{exact:true}).fill('isolated-secret');await channelForm.getByRole('checkbox',{name:'Enable channel',exact:true}).uncheck();
    response=waitOperation('channels.create');await channelForm.getByRole('button',{name:'Save channel',exact:true}).click();const created=await response;assert.equal(created.status(),200,await created.text());const channel=(await created.json()).channel;assert.equal(channel.config.smtpTo,'运维 <ops@example.test>');assert.equal(channel.enabled,false);
    const channelRow=screen.locator('.channel-list article').filter({hasText:'Isolated SMTP'});await channelRow.waitFor();
    await channelRow.getByRole('button',{name:'Edit',exact:true}).click();assert.equal(await channelForm.getByLabel('Password / App Password',{exact:true}).inputValue(),'');
    await channelForm.getByLabel('Name',{exact:true}).fill('Cancelled SMTP edit');await channelForm.getByRole('button',{name:'Cancel editing',exact:true}).click();assert.equal((await(await context.request.get(base+op('channels.list').path)).json()).channels.find(value=>value.id===channel.id).name,'Isolated SMTP');
    await channelRow.getByRole('button',{name:'Edit',exact:true}).click();await channelForm.getByLabel('Name',{exact:true}).fill('Updated SMTP');
    response=waitOperation('channels.update',channel.id);await channelForm.getByRole('button',{name:'Save channel',exact:true}).click();assert.equal((await response).status(),200);
    const updated=screen.locator('.channel-list article').filter({hasText:'Updated SMTP'});await updated.waitFor();
    assert.ok(!(await page.evaluate(()=>JSON.stringify({...localStorage,...sessionStorage}))).includes('isolated-secret'),'SMTP secret remains outside browser storage');
    const testPath=routePath('channels.test',channel.id);await page.route('**'+testPath,route=>route.fulfill({status:202,json:{status:'queued'}}));
    response=waitOperation('channels.test',channel.id);await updated.getByRole('button',{name:'Send test',exact:true}).click();assert.equal((await response).status(),202);await screen.locator('.alert-deliveries').waitFor();await page.unroute('**'+testPath);
    await tabs.getByRole('tab',{name:'Notification channels',exact:true}).click();await updated.waitFor();
    let failedDelete=true;const deletePath=routePath('channels.delete',channel.id);await page.route('**'+deletePath,route=>{if(route.request().method()!==op('channels.delete').method)return route.fallback();if(failedDelete){failedDelete=false;return route.fulfill({status:503,body:'Isolated delete failure'})}return route.fallback()});
    response=waitOperation('channels.delete',channel.id);await updated.getByRole('button',{name:'Delete',exact:true}).click();assert.equal((await response).status(),503);await screen.getByRole('alert').waitFor();assert.ok(await updated.isVisible(),'Failed deletion keeps the channel');
    response=waitOperation('channels.delete',channel.id);await updated.getByRole('button',{name:'Delete',exact:true}).click();assert.ok((await response).ok());await updated.waitFor({state:'detached'});await page.unroute('**'+deletePath);
    await require('./notification-channel-regressions.cjs')({page,context,base,headers,catalog});
    await tabs.getByRole('tab',{name:'Silence and maintenance',exact:true}).click();const windowForm=screen.locator('.alert-window-form');await windowForm.waitFor();
    await windowForm.getByLabel('Reason',{exact:true}).fill('Isolated maintenance');response=waitOperation('windows.create');await windowForm.getByRole('button',{name:'Create window',exact:true}).click();const windowReply=await response;assert.equal(windowReply.status(),201);const createdWindow=(await windowReply.json()).window;
    const windowRow=screen.locator('.window-list article').filter({hasText:'Isolated maintenance'});await windowRow.waitFor();response=waitOperation('windows.delete',createdWindow.id);await windowRow.getByRole('button',{name:'Disable',exact:true}).click();assert.ok((await response).ok());await windowRow.waitFor({state:'detached'});
    const sample={id:'isolated-alert',ruleId:'isolated-rule',ruleName:'Fixture alert',nodeId:node,nodeName:'Isolated node',severity:'critical',status:'active',message:'Fixture condition',firstSeenAt:new Date().toISOString(),lastSeenAt:new Date().toISOString()};
    const samples={
      'alerts.list':()=>({alerts:[sample]}),
      'alerts.history':()=>({alerts:[{...sample,id:'history-alert',status:'resolved',ruleName:'Recovered fixture',resolvedAt:new Date().toISOString()}]}),
      'alerts.events':()=>({events:[{id:'fixture-event',alertId:sample.id,kind:'resolved',message:'Fixture recovery event',occurredAt:new Date().toISOString()}]}),
      'deliveries.list':()=>({deliveries:[{id:'fixture-delivery',channelId:'fixture-channel',channelName:'Isolated delivery',kind:'smtp',testSend:true,status:'failed',attempts:3,maxAttempts:3,lastError:'Fixture delivery error',createdAt:new Date().toISOString()}]}),
    };
    for(const [name,value] of Object.entries(samples))await page.route('**'+op(name).path,route=>route.fulfill({json:value()}));
    const ackPath=routePath('alerts.acknowledge',sample.id),silencePath=routePath('alerts.silence',sample.id);
    await page.route('**'+ackPath,route=>{assert.deepEqual(route.request().postDataJSON(),{note:''});sample.acknowledgedAt=new Date().toISOString();return route.fulfill({json:{}})});
    await page.route('**'+silencePath,route=>{const body=route.request().postDataJSON();assert.ok(new Date(body.until)>new Date());sample.suppressionReason='maintenance';return route.fulfill({json:{}})});
    await tabs.locator('[role=tab][data-value=active]').click();const activeRow=screen.locator('.alert-list article').filter({hasText:'Fixture alert'});await activeRow.waitFor();response=waitOperation('alerts.acknowledge',sample.id);await activeRow.getByRole('button',{name:'Acknowledge',exact:true}).click();assert.ok((await response).ok());await page.waitForFunction(()=>document.querySelector('.alert-list button')?.disabled);
    response=waitOperation('alerts.silence',sample.id);await activeRow.getByRole('button',{name:'Silence for 1 hour',exact:true}).click();assert.ok((await response).ok());
    await tabs.locator('[role=tab][data-value=history]').click();await screen.getByText('Recovered fixture',{exact:true}).waitFor();await screen.getByText('Fixture recovery event',{exact:true}).waitFor();
    await tabs.locator('[role=tab][data-value=deliveries]').click();await screen.getByRole('row').filter({hasText:'Fixture delivery error'}).waitFor();
    for(const name of Object.keys(samples))await page.unroute('**'+op(name).path);
    await page.unroute('**'+ackPath);await page.unroute('**'+silencePath);
    for(const [language,theme,width] of [['en-US','light',1440],['zh-CN','dark',390]]){
      await setBrowserLanguage(page,language);await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);
      for(const target of ['rules','channels','windows']){
        await tabs.locator(`[role=tab][data-value=${target}]`).focus();await page.keyboard.press('Enter');await page.waitForFunction(value=>document.querySelector('.alert-tabs [aria-selected=true]')?.getAttribute('data-value')===value,target);const form=screen.locator('[role=tabpanel][data-state=active] form');await form.waitFor();await page.waitForFunction(()=>!document.querySelector('.alerts-page [role=tabpanel][data-state=inactive]'));
        const labels=form.locator('.parameter-row > label');assert.ok(await labels.count()>0,'Expected labelled controls');
        const boxes=await labels.evaluateAll(labels=>labels.map(label=>{const control=document.getElementById(label.htmlFor);if(!control||!control.getClientRects().length)throw Error('Expected visible control '+label.textContent);const a=label.getBoundingClientRect(),b=control.getBoundingClientRect();return {label:label.textContent,labelRight:a.right,x:b.x,right:b.right,width:b.width,checkbox:control.getAttribute('role')==='checkbox'}}));
        assert.ok(boxes.every(box=>box.width>0&&box.x>=box.labelRight&&box.right<=width),'Labels stay left of actual controls');const fields=boxes.filter(box=>!box.checkbox);assert.ok(Math.max(...fields.map(box=>box.right))-Math.min(...fields.map(box=>box.right))<1,'Actual input and select right edges align '+JSON.stringify({language,target,boxes}));assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Alert view must fit the viewport');
        if(target==='rules'){
          await selectValue(screen.locator('#alert-rule-filter'),node);const row=screen.getByRole('row').filter({hasText:nodeCPU.name});assert.equal(await row.count(),1);const buttons=row.locator('.alert-rule-actions button');assert.equal(await buttons.count(),2);const [a,b]=await buttons.evaluateAll(buttons=>buttons.map(button=>{const r=button.getBoundingClientRect();return {y:r.y,height:r.height}}));assert.ok(Math.abs(a.y+a.height/2-b.y-b.height/2)<1,'Row actions remain side by side');await buttons.nth(0).click();const editor=screen.getByRole('dialog');await editor.waitFor();const bounds=await editor.boundingBox();assert.ok(bounds.x>=0&&bounds.x+bounds.width<=width+1);assert.equal(await editor.locator('#alert-rule-threshold').inputValue(),'98');await page.keyboard.press('Escape');await editor.waitFor({state:'detached'});
        }

      }
    }
    assert.notEqual(await selectedTab.evaluate(el=>getComputedStyle(el).outlineColor),'rgba(0, 0, 0, 0)','Keyboard navigation retains its focus outline');
    await setBrowserLanguage(page,'en-US');await page.setViewportSize({width:1440,height:1000});

    console.log('alert-center-regressions passed.');
};
