// Real isolated Core enrollments; commands are inspected/copied, never executed.
const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,base,artifacts,catalog}){
 const operation=catalog.operations.find(o=>o.name==='agents.enrollment');assert.ok(operation)
 let issued,requests=0,hold=false,release,started
 const handler=async route=>{
  if(route.request().method()!==operation.method)return route.fallback()
  requests++;const response=await route.fetch();assert.equal(response.status(),201);issued=await response.json()
  if(hold){started();await new Promise(resolve=>release=resolve)}
  await route.fulfill({response})
 }
 await page.route('**'+operation.path,handler)
 await page.addInitScript(()=>Object.defineProperty(navigator,'clipboard',{value:{writeText:async value=>{window.enrollmentCopiedText=value}}}))
 await page.goto(base+'/#/agent-enrollment')
 const choices=page.getByTestId('agent-deployment-method'),native=choices.getByRole('button',{name:'Direct installation (systemd)',exact:true}),docker=choices.getByRole('button',{name:'Docker deployment',exact:true})
 await native.waitFor();assert.ok(await native.isVisible()&&await docker.isVisible(),'Both deployment choices are visible before generating a command')
 assert.equal(await choices.locator('select').count(),0,'Deployment uses side-by-side options')
 await native.click();await page.locator('.agent-enrollment-form input').nth(1).fill('Enrollment fixture')
 await page.getByTestId('asset-traffic').fill('1200.5')
 await page.getByTestId('asset-price').fill('12.34')
 await page.getByTestId('asset-cycle').selectOption('year')
 await page.getByTestId('asset-purchase').fill('2026-10-13')
 await page.getByTestId('asset-expiry').fill('2027-05-01T12:30')
 const generate=page.locator('.agent-enrollment-form .primary-button'),command=page.getByTestId('agent-install-command'),expiry=page.getByTestId('agent-enrollment-expiry')
 await generate.click();await command.waitFor();assert.equal(requests,1)
 const assetOperation=catalog.operations.find(o=>o.name==='nodes.asset')
 const readAsset=async()=>{const response=await page.request.get(base+assetOperation.path.replace('{nodeId}',issued.nodeId));assert.equal(response.status(),200);return response.json()}
 const initialAsset=await readAsset();assert.equal(initialAsset.price,12.34);assert.equal(initialAsset.billingCycleUnit,'year');assert.equal(initialAsset.trafficLimitBytes,1200500000000);assert.ok(initialAsset.expiresAt);assert.equal(initialAsset.purchasedAt,'2026-10-13T00:00:00Z')
 const first=await command.inputValue();assert.ok(first.includes('--token '+issued.token)&&first.includes('--mode native'));assert.ok(first.length<600,'Copied command is short and complete')
 assert.equal(await page.getByTestId('agent-enrollment-token').count(),0,'No separate token copy/input step is displayed')
 const expected=new Date(Date.parse(issued.expiresAt)+8*3600000).toISOString().slice(0,19).replace('T',' ')
 assert.equal(await expiry.innerText(),expected,'Expiry is Beijing time regardless of browser timezone')
 assert.equal(await expiry.getAttribute('datetime'),issued.expiresAt,'The underlying timestamp stays ISO UTC')
 assert.ok((await expiry.locator('..').innerText()).includes('UTC+8'))
 await docker.click();assert.equal(await docker.getAttribute('aria-pressed'),'true');assert.equal(await native.getAttribute('aria-pressed'),'false')
 const selected=await command.inputValue();assert.ok(selected.includes('--mode docker')&&selected.includes('--token '+issued.token));assert.equal(requests,1,'Changing deployment reuses the same enrollment')
 await page.locator('.agent-enrollment-result .secondary-button').click();assert.equal(await page.evaluate(()=>window.enrollmentCopiedText),selected,'Copy produces the complete selected command')
 hold=true;const pending=new Promise(resolve=>started=resolve);await generate.click();await pending
 await page.locator('.agent-enrollment-form input').nth(1).fill('Changed while generating');release()
 await page.waitForFunction(()=>!document.querySelector('.agent-enrollment-form .primary-button').disabled)
 assert.equal(await command.count(),0,'A delayed response cannot publish a command for the previous node name')
 hold=false;await generate.click();await command.waitFor()
 const layouts=[]
 for(const language of ['en','zh-CN']){
  await page.locator('.language-selector').selectOption(language)
  for(const theme of ['light','dark'])for(const width of [390,1440]){
   await page.setViewportSize({width,height:1000});await page.waitForTimeout(100)
   const scrim=page.locator('.navigation-scrim');if(await scrim.isVisible())await scrim.click({position:{x:width-10,y:20}})
   await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
   assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Enrollment fits the viewport')
   const name=`enrollment-${language}-${theme}-${width}`;await page.screenshot({path:path.join(artifacts,name+'.png'),fullPage:true});layouts.push(name)
  }
 }
 // Revisit a pending node after leaving the original command page.
 const pendingNode=issued.nodeId,oldToken=issued.token
 await page.locator('.language-selector').selectOption('en')
 await page.goto(base+'/#/nodes?node='+pendingNode)
 await page.getByTestId('node-detail').waitFor()
 assert.equal(await page.getByTestId('node-delete').count(),0,'Deletion is absent from the detail heading and overview')
 await page.getByTestId('node-install-command').click()
 const renewalResponse=page.waitForResponse(r=>r.url().endsWith('/nodes/'+pendingNode+'/enrollment')&&r.request().method()==='POST')
 await page.getByRole('button',{name:'Regenerate installation command',exact:true}).click()
 const renewed=await (await renewalResponse).json();assert.equal(renewed.nodeId,pendingNode);assert.notEqual(renewed.token,oldToken)
 await command.waitFor();assert.ok((await command.inputValue()).includes('--token '+renewed.token))
 await page.locator('.agent-enrollment-result .secondary-button').click()
 assert.equal(await page.evaluate(()=>window.enrollmentCopiedText),await command.inputValue())
 await page.locator('.agent-enrollment-result .secondary-button').click()
 assert.equal(await page.evaluate(()=>window.enrollmentCopiedText),await command.inputValue(),'Pending command can be copied repeatedly')
 // Switching presentation preserves the same replacement credential.
 await page.getByTestId('agent-deployment-method').getByRole('button',{name:'Docker deployment',exact:true}).click()
 assert.ok((await command.inputValue()).includes('--mode docker'))
 await page.locator('.dashboard-tabs').getByRole('button',{name:'Node settings',exact:true}).click()
 const settings=page.getByTestId('node-asset-settings');await settings.getByTestId('asset-price').waitFor()
 assert.equal(await settings.getByTestId('asset-purchase').inputValue(),'2026-10-13')
 await settings.getByTestId('asset-purchase').fill('2026-10-01')
 assert.equal(await settings.getByTestId('asset-price').inputValue(),'12.34')
 assert.equal(await settings.getByTestId('asset-traffic').inputValue(),'1200.5')
 for(const unit of ['month','hour','minute','year']){
  await settings.getByTestId('asset-cycle').selectOption(unit)
  const response=page.waitForResponse(r=>r.request().method()==='PUT'&&r.url().endsWith('/asset'))
  await settings.getByTestId('asset-save').click();assert.equal((await response).status(),200)
  await settings.getByRole('status').waitFor();assert.equal((await readAsset()).billingCycleUnit,unit);assert.equal((await readAsset()).purchasedAt,'2026-10-01T00:00:00Z')
 }
 await settings.getByTestId('asset-purchase').fill('');await settings.getByTestId('asset-expiry').fill('');await settings.getByTestId('asset-traffic').fill('0');await settings.getByTestId('asset-price').fill('0')
 const clearResponse=page.waitForResponse(r=>r.request().method()==='PUT'&&r.url().endsWith('/asset'))
 await settings.getByTestId('asset-save').click();assert.equal((await clearResponse).status(),200);await settings.getByRole('status').waitFor()
 const cleared=await readAsset();assert.equal(cleared.purchasedAt,undefined);assert.equal(cleared.expiresAt,undefined);assert.equal(cleared.trafficLimitBytes,0);assert.equal(cleared.price,0)
 assert.ok(await settings.evaluate(el=>el.nextElementSibling?.dataset.testid==='node-settings-delete'),'Billing settings immediately precede deletion')
 // Reopening registration after editing settings loads fresh asset values.
 await page.getByTestId('node-install-command').click()
 await page.getByTestId('agent-enrollment').getByTestId('asset-price').waitFor()
 assert.equal(await page.getByTestId('agent-enrollment').getByTestId('asset-price').inputValue(),'0')
 assert.equal(await page.getByTestId('agent-enrollment').getByTestId('asset-purchase').inputValue(),'')
 await page.locator('.dashboard-tabs').getByRole('button',{name:'Node settings',exact:true}).click()
 const deletion=page.getByTestId('node-settings').getByTestId('node-delete')
 await deletion.waitFor()
 assert.equal(await deletion.count(),1,'Deletion belongs to node settings')
 assert.ok(await page.getByTestId('node-settings-delete').evaluate(el=>el===el.parentElement.lastElementChild),'Deletion is the final settings section')
 for(const theme of ['light','dark'])for(const width of [390,1440]){
  await page.setViewportSize({width,height:1000});await page.waitForTimeout(200)
  const scrim=page.locator('.navigation-scrim');if(await scrim.isVisible()){await scrim.click({position:{x:width-10,y:20}});await page.waitForTimeout(200)}
  await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  assert.ok(await page.getByTestId('node-delete').isVisible())
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Pending management fits mobile')
  await page.screenshot({path:path.join(artifacts,`pending-node-${theme}-${width}.png`),fullPage:true})
 }
 await page.getByTestId('node-delete').click()
 await page.locator('.ui-dialog .secondary-button').click()
 assert.ok(await page.getByTestId('node-detail').isVisible(),'Cancel preserves the node')
 await page.getByTestId('node-delete').click()
 const deleted=page.waitForResponse(r=>r.url().endsWith('/nodes/'+pendingNode)&&r.request().method()==='DELETE')
 await page.locator('.ui-dialog .primary-button').click();assert.equal((await deleted).status(),200)
 await page.getByTestId('node-detail').waitFor({state:'detached'})
 assert.equal(new URL(page.url()).hash,'#/nodes','Deletion returns to the node list')
 await page.unroute('**'+operation.path,handler)
 console.log('Enrollment regressions passed: side-by-side modes, complete copied token command, fixed Beijing expiry, shared enrollment, stale-response suppression; '+layouts.length+' layouts.')
}
