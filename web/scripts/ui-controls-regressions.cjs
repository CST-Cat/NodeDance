const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,base,artifacts,catalog}){
 const errors=[];page.on('pageerror',error=>errors.push(error.message))
 await page.setViewportSize({width:1440,height:1000});await page.goto(base+'/#/agent-enrollment')
 const source=page.getByTestId('asset-cycle'),control=source.locator('..').locator('button[data-dropdown-field]')
 await control.waitFor();assert.equal(await source.getAttribute('aria-hidden'),'true')
 await control.click();const list=page.locator('.product-dropdown-menu');await list.waitFor()
 assert.ok(await list.getByRole('menuitemradio',{name:'Year',exact:true}).isVisible())
 await list.getByRole('menuitemradio',{name:'Year',exact:true}).click();assert.equal(await source.inputValue(),'year');await list.waitFor({state:'detached'})
 await control.focus();await page.keyboard.press('ArrowDown');await list.waitFor();await page.keyboard.press('h');await page.waitForFunction(()=>document.activeElement?.getAttribute('data-option-value')==='hour');await page.keyboard.press('Enter');assert.equal(await source.inputValue(),'hour');await list.waitFor({state:'detached'})
 await control.click();await list.waitFor();await page.keyboard.press('Escape');await list.waitFor({state:'detached'});assert.ok(await control.evaluate(el=>el===document.activeElement))
 await control.click();await list.waitFor();assert.equal(await page.locator('body').evaluate(el=>getComputedStyle(el).pointerEvents),'none','Registry modal menu blocks underlying fields');const outside=await page.locator('.agent-setup-form input[name=displayName]').boundingBox();await page.mouse.click(outside.x+outside.width/2,outside.y+outside.height/2);await list.waitFor({state:'detached'})
 await control.click();await list.waitFor();await page.keyboard.press('Tab');assert.ok(await list.isVisible(),'Registry menu keeps keyboard focus while open');await page.keyboard.press('Escape');await list.waitFor({state:'detached'})
 await page.locator('.agent-setup-form input[name=displayName]').fill('Disabled-control fixture')
 const operation=catalog.operations.find(item=>item.name==='agents.enrollment');assert.ok(operation)
 let release,started;const held=new Promise(resolve=>release=resolve),pending=new Promise(resolve=>started=resolve)
 await page.route('**'+operation.path,async route=>{if(route.request().method()!=='POST')return route.fallback();started();await held;return route.fulfill({status:503,body:'Isolated held submission'})})
 await page.locator('.agent-setup-form .primary-button, .agent-enrollment-form .primary-button').click();await pending
 assert.ok(await control.isDisabled(),'Custom field honors the disabled fieldset')
 release();await page.locator('.agent-setup-form [role=alert]').waitFor();await page.waitForFunction(el=>!el.disabled,await control.elementHandle());assert.ok(await control.isEnabled())
 for(const [theme,width] of [['light',1440],['dark',375]]){
  await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  await control.scrollIntoViewIfNeeded();await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));await control.click();await list.waitFor()
  const bounds=await list.boundingBox();assert.ok(bounds.x>=0&&bounds.x+bounds.width<=width+1)
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
  await page.waitForFunction(()=>{const list=document.querySelector('.product-dropdown-menu');return list&&getComputedStyle(list).opacity==='1'&&getComputedStyle(list).transform==='none'})
  assert.ok(await list.isVisible());await page.keyboard.press('Escape')
 }
 await page.emulateMedia({reducedMotion:'reduce'});await control.click();await list.waitFor();assert.ok(await list.evaluate(el=>getComputedStyle(el).transform==='none'&&getComputedStyle(el).transitionDuration.split(',').every(s=>parseFloat(s)<=.15)));await page.keyboard.press('Escape')
 await page.goto(base+'/#/settings');const check=page.locator('.guest-settings').getByRole('checkbox').first();await check.waitFor()
 const previous=await check.isChecked();await check.focus();await page.keyboard.press('Space');assert.equal(await check.isChecked(),!previous)
 assert.equal(await check.getAttribute('data-state'),previous?'unchecked':'checked');await check.click();assert.equal(await check.isChecked(),previous)
 await page.goto(base+'/#/alerts');await page.locator('.alert-tabs').getByRole('tab',{name:'Alert rules',exact:true}).click();const alertCheck=page.locator('.alerts-page button[role=checkbox]').first();await alertCheck.waitFor();
 const alertWasChecked=await alertCheck.isChecked();await alertCheck.focus();await page.keyboard.press('Space');assert.equal(await alertCheck.isChecked(),!alertWasChecked);await alertCheck.click();assert.equal(await alertCheck.isChecked(),alertWasChecked);
 await page.goto(base+'/#/settings');await check.waitFor()
 for(const [theme,width] of [['light',1440],['dark',375]]){
  await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
 }
 assert.deepEqual(errors,[])
 console.log("ui-controls-regressions passed.")
}
