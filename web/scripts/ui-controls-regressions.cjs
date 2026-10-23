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
 await control.click();await list.waitFor();assert.equal(await page.locator('body').evaluate(el=>getComputedStyle(el).pointerEvents),'none','Registry modal menu blocks underlying fields');const outside=await page.locator('.agent-enrollment-form input').nth(1).boundingBox();await page.mouse.click(outside.x+outside.width/2,outside.y+outside.height/2);await list.waitFor({state:'detached'})
 await control.click();await list.waitFor();await page.keyboard.press('Tab');assert.ok(await list.isVisible(),'Registry menu keeps keyboard focus while open');await page.keyboard.press('Escape');await list.waitFor({state:'detached'})
 await page.locator('.agent-enrollment-form input').nth(1).fill('Disabled-control fixture')
 const operation=catalog.operations.find(item=>item.name==='agents.enrollment');assert.ok(operation)
 let release,started;const held=new Promise(resolve=>release=resolve),pending=new Promise(resolve=>started=resolve)
 await page.route('**'+operation.path,async route=>{if(route.request().method()!=='POST')return route.fallback();started();await held;return route.fulfill({status:503,body:'Isolated held submission'})})
 await page.locator('.agent-enrollment-form .primary-button').click();await pending
 assert.ok(await control.isDisabled(),'Custom field honors the disabled fieldset')
 release();await page.locator('.agent-enrollment-form [role=alert]').waitFor();await page.waitForFunction(()=>!document.querySelector('[data-testid=asset-cycle]').closest('.ui-select').querySelector('button').disabled);assert.ok(await control.isEnabled())
 for(const theme of ['light','dark'])for(const width of [375,1440]){
  await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  await control.scrollIntoViewIfNeeded();await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));await control.click();await list.waitFor()
  const corners=await list.evaluate(el=>{
   const measure=()=>{const outer=getComputedStyle(el),child=el.querySelector('[role=menuitemradio]'),inner=getComputedStyle(child);return {outer:parseFloat(outer.borderTopLeftRadius),inner:parseFloat(inner.borderTopLeftRadius),gap:parseFloat(outer.paddingTop)+parseFloat(outer.borderTopWidth),width:el.offsetWidth,height:el.offsetHeight}}
   const original=measure();el.style.setProperty('--radius-panel','20px');const changed=measure();el.style.removeProperty('--radius-panel');return {original,changed}
  })
  assert.equal(corners.original.outer,12,'Dropdown retains the existing panel radius token')
  for(const corner of [corners.original,corners.changed])assert.equal(corner.inner,Math.max(0,corner.outer-corner.gap),'Options follow the actual CSS padding and border gap')
  assert.equal(corners.changed.width,corners.original.width,'Radius token changes preserve width');assert.equal(corners.changed.height,corners.original.height,'Radius token changes preserve height')
  const bounds=await list.boundingBox();assert.ok(bounds.x>=0&&bounds.x+bounds.width<=width+1)
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
  await page.waitForFunction(()=>{const list=document.querySelector('.product-dropdown-menu');return list&&getComputedStyle(list).opacity==='1'&&getComputedStyle(list).transform==='none'})
  await page.screenshot({animations:'disabled',path:path.join(artifacts,`controls-select-${theme}-${width}.png`)});assert.ok(await list.isVisible());await page.keyboard.press('Escape')
 }
 await page.emulateMedia({reducedMotion:'reduce'});await control.click();await list.waitFor();assert.ok(await list.evaluate(el=>getComputedStyle(el).transform==='none'&&getComputedStyle(el).transitionDuration.split(',').every(s=>parseFloat(s)<=.15)));await page.keyboard.press('Escape')
 await page.goto(base+'/#/guest-settings');const check=page.locator('.guest-settings').getByRole('checkbox').first();await check.waitFor()
 const previous=await check.isChecked();await check.focus();await page.keyboard.press('Space');assert.equal(await check.isChecked(),!previous)
 assert.equal(await check.getAttribute('data-state'),previous?'unchecked':'checked');await check.click();assert.equal(await check.isChecked(),previous)
 // Keep coverage for native controls still used outside these five screens.
 await page.goto(base+'/#/alerts');await page.locator('.alert-tabs').getByRole('button',{name:'Alert rules',exact:true}).click();const native=page.locator('.alerts-page input[type=checkbox]').first();await native.waitFor()
 assert.equal(await native.evaluate(el=>getComputedStyle(el).appearance),'none')
 await native.evaluate(el=>el.indeterminate=true);assert.ok(await native.evaluate(el=>el.matches(':indeterminate')))
 await native.click();assert.ok(!(await native.evaluate(el=>el.indeterminate)))
 await page.goto(base+'/#/guest-settings');await check.waitFor()
 for(const theme of ['light','dark'])for(const width of [375,1440]){
  await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));await page.screenshot({path:path.join(artifacts,`controls-check-${theme}-${width}.png`),fullPage:true})
 }
 assert.deepEqual(errors,[])
 console.log('Shared controls passed: pointer/keyboard/typeahead, Escape focus, registry modal blocking/Tab behavior, fieldset disable, value handoff, viewport positioning, registry checkbox keyboard/state and native indeterminate, reduced motion; 8 layouts.')
}
