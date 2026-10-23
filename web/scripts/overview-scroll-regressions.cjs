// Width chooses the presentation; scroll input must never reshape the toolbar.
const assert=require('node:assert/strict'),path=require('node:path')
async function settle(page){await page.evaluate(()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))))}
async function checkStableScroll(page){
 const measure=()=>page.locator('.overview-toolbar-slot').evaluate(el=>({height:el.offsetHeight,transform:getComputedStyle(el).transform,styles:[el,...el.querySelectorAll('*')].map(x=>x.getAttribute('style')),heightOfPage:document.documentElement.scrollHeight}))
 await page.evaluate(()=>{document.querySelector('.nodes-dashboard').style.minHeight='3500px';scrollTo(0,0)})
 await settle(page);const before=await measure()
 for(const y of [100,400,180,500,450,0]){await page.evaluate(y=>scrollTo(0,y),y);await settle(page);const after=await measure();assert.equal(after.height,before.height,'Scroll preserves toolbar height');assert.equal(after.transform,'none');assert.deepEqual(after.styles,before.styles,'Scroll writes no toolbar projection styles');assert.equal(after.heightOfPage,before.heightOfPage)}
 await page.evaluate(()=>document.querySelector('.nodes-dashboard').style.removeProperty('min-height'))
}
module.exports=async function({page,base,artifacts}){
 for(const theme of ['light','dark'])for(const width of [320,390,700,1440]){
  await page.setViewportSize({width,height:900});await page.goto(base+'/#/nodes');await page.locator('.node-summary').waitFor()
  await page.locator('.app-shell').evaluate((el,t)=>{el.dataset.theme=t;document.documentElement.dataset.theme=t},theme)
  await settle(page);const mobile=width<=900,search=page.locator('#overview-node-search'),preferences=page.locator('.view-preferences-button')
  assert.equal(await page.locator('.overview-search-toggle').isVisible(),mobile)
  assert.equal(await search.isVisible(),!mobile,'Only desktop has an inline search field')
  if(mobile){
   const strip=await page.locator('.overview-toolbar-panel').boundingBox();assert.ok(strip.height<=50,'Compact toolbar stays on one row')
   const row=await page.locator('.node-summary,.overview-search-toggle,.dashboard-toolbar-actions > button').evaluateAll(items=>items.map(el=>{const r=el.getBoundingClientRect();return {center:(r.top+r.bottom)/2,left:r.left,right:r.right}}))
   assert.ok(row.every(r=>Math.abs(r.center-(strip.y+strip.height/2))<1&&r.left>=0&&r.right<=width),'Counts and all three icons share one contained row')
  }
  assert.equal(await page.locator('.overview-toolbar-panel .node-count-label').first().isVisible(),!mobile)
  const actions=await page.locator('.dashboard-toolbar-actions > button').evaluateAll(buttons=>buttons.map(el=>{const b=el.getBoundingClientRect(),icon=el.querySelector('svg'),i=icon.getBoundingClientRect();const label=el.querySelector('.overview-action-label').getBoundingClientRect();return {height:b.height,centerError:Math.abs((b.top+b.bottom-i.top-i.bottom)/2),labelCenterError:label.height?Math.abs((label.top+label.bottom-i.top-i.bottom)/2):0,transform:getComputedStyle(icon).transform}}))
  for(const action of actions){assert.ok(action.height>=44);assert.ok(action.centerError<1,'Action icons stay vertically centered');assert.equal(action.transform,'none');assert.ok(action.labelCenterError<1,'Action labels align with their icons')}
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'No horizontal overflow')
  await checkStableScroll(page)
  if(mobile){
   const trigger=page.locator('.overview-search-toggle');await trigger.click();const dialog=page.locator('.overview-search-dialog[open]');await dialog.waitFor();assert.equal(await page.locator('#overview-node-search').count(),1)
   await search.fill('retained-search');await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});assert.ok(await trigger.evaluate(el=>el===document.activeElement),'Search restores trigger focus '+theme+'-'+width+': '+await page.evaluate(()=>document.activeElement?.outerHTML.slice(0,400)))
   await trigger.click();assert.equal(await search.inputValue(),'retained-search');await search.fill('');await dialog.locator('.ui-dialog-close').click()
  }
  await preferences.click();const controls=page.locator('#node-view-preferences');await controls.waitFor({state:'visible'})
  assert.equal(await controls.evaluate(el=>!!el.closest('dialog[open]')),mobile,'Preferences reuse the shared dialog on narrow screens only')
  const selector=controls.locator('.ui-select').first(),combo=selector.locator('button[data-dropdown-field]');await combo.click()
  const list=page.locator('.product-dropdown-menu').last();await list.waitFor();await list.getByRole('menuitemradio',{name:'Online',exact:true}).click();assert.equal(await selector.locator('select').inputValue(),'online')
  await combo.click();await page.locator('.product-dropdown-menu').last().getByRole('menuitemradio',{name:'All states',exact:true}).click()
  const save=controls.getByRole('button',{name:'Save view',exact:true}),response=page.waitForResponse(r=>r.url().endsWith('/dashboard/settings')&&r.request().method()==='PUT');await save.click();assert.equal((await response).status(),204,'Both presentations save through the existing Core operation')
  await page.screenshot({path:path.join(artifacts,`preferences-${theme}-${width}.png`)})
  if(mobile){await page.keyboard.press('Escape');assert.ok(await preferences.evaluate(el=>el===document.activeElement),'Preferences restore trigger focus')}
  else await preferences.click()
  await page.screenshot({path:path.join(artifacts,`overview-${theme}-${width}.png`)})
 }
 await page.setViewportSize({width:390,height:900});await page.goto(base+'/#/nodes');await page.locator('.node-summary').waitFor()
 await page.locator('.overview-search-toggle').click();await page.locator('#overview-node-search').fill('resize-value');await page.setViewportSize({width:1440,height:900});await page.locator('.overview-search-dialog').waitFor({state:'detached'});assert.equal(await page.locator('#overview-node-search').inputValue(),'resize-value');await page.locator('#overview-node-search').fill('')
 await page.setViewportSize({width:390,height:900});await page.locator('.view-preferences-button').click();await page.locator('.overview-preferences-dialog[open]').waitFor();await page.setViewportSize({width:1440,height:900});await page.locator('.overview-preferences-dialog').waitFor({state:'detached'});assert.ok(await page.locator('#node-view-preferences').isVisible());await page.locator('.view-preferences-button').click()
 await page.emulateMedia({reducedMotion:'reduce'});await page.setViewportSize({width:320,height:900});await page.reload();await page.locator('.node-summary').waitFor();await checkStableScroll(page)
 await page.evaluate(()=>localStorage.setItem('nodedance.locale','zh-CN'));await page.reload();await page.locator('.node-summary').waitFor();assert.deepEqual(await page.locator('.node-count-compact-label').allTextContents(),['总数','在线','离线','待接入']);assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
 await page.locator('.navigation-toggle').click();assert.equal(await page.locator('.overview-toolbar-panel').isVisible(),false);await page.locator('.navigation-toggle').click();assert.equal(await page.locator('.overview-toolbar-panel').isVisible(),true)
 await page.locator('.overview-enrollment-button').click();await page.waitForURL('**/#/agent-enrollment')
 await page.evaluate(()=>localStorage.setItem('nodedance.locale','en'));await page.emulateMedia({reducedMotion:'no-preference'})
 console.log('Overview presentation passed: fixed width layouts, scroll stability, centered icons, shared modal search/preferences, focus, selection/save, resize, Chinese and light/dark/mobile layouts.')
}
module.exports.checkStableScroll=checkStableScroll
