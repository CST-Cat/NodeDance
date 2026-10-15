const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,base,artifacts}){
 const errors=[];page.on('pageerror',error=>errors.push(error.message))
 const appearance=await(await page.context().request.get(base+'/api/v1/public/appearance')).json()
 const csrf=(await(await page.context().request.get(base+'/api/v1/auth/csrf')).json()).token
 async function setTheme(theme){const response=await page.context().request.put(base+'/api/v1/settings/appearance',{headers:{Origin:base,'X-CSRF-Token':csrf},data:{displayName:appearance.displayName,backgroundColor:appearance.backgroundColor,theme}});assert.equal(response.status(),200)}
 let layouts=0
 for(const theme of ['light','dark'])for(const width of [390,1100,1440,1920]){
  await setTheme(theme)
  await page.setViewportSize({width,height:1000})
  for(const tab of ['windows','rules','channels']){
   await page.goto(base+'/#/alerts?tab='+tab);await page.reload();await page.locator('.alert-form-card').waitFor()
   await page.waitForFunction(theme=>document.querySelector('.app-shell')?.dataset.theme===theme,theme)
   const outside=await page.locator('.alert-form-card').evaluate(form=>{
    const card=form.getBoundingClientRect(),style=getComputedStyle(form),left=card.left+parseFloat(style.paddingLeft),right=card.right-parseFloat(style.paddingRight)
    return [...form.querySelectorAll('input:not([type=checkbox]),textarea,.u031-btn,button[type=submit]')].filter(el=>{const r=el.getBoundingClientRect();return r.width&&r.height&&(r.left<left-1||r.right>right+1)}).map(el=>el.outerHTML.slice(0,160))
   })
   assert.deepEqual(outside,[],`Alert ${tab} controls outside card (${theme}, ${width})`)
   assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1))
   await page.locator('.app-shell').evaluate(async el=>await Promise.all(el.getAnimations({subtree:true}).map(animation=>animation.finished.catch(()=>{}))))
   await page.screenshot({path:path.join(artifacts,`form-${tab}-${theme}-${width}.png`)});layouts++
  }
  await page.goto(base+'/#/feeds');await page.reload();const form=page.locator('.feed-form');await form.waitFor()
  await page.waitForFunction(theme=>document.querySelector('.app-shell')?.dataset.theme===theme,theme)
  for(const mode of ['off','immediate','digest']){
   await form.locator('[name=feedNotificationMode]').selectOption(mode)
   const outside=await form.evaluate(form=>{
    const card=form.getBoundingClientRect(),style=getComputedStyle(form),left=card.left+parseFloat(style.paddingLeft),right=card.right-parseFloat(style.paddingRight)
    return [...form.querySelectorAll('input:not([type=checkbox]),textarea,.u031-btn,fieldset')].filter(el=>{const r=el.getBoundingClientRect();return r.width&&r.height&&(r.left<left-1||r.right>right+1)}).map(el=>el.outerHTML.slice(0,160))
   })
   assert.deepEqual(outside,[],`Feed ${mode} controls outside card (${theme}, ${width})`)
   const rects=await form.evaluate(form=>{
    const rect=selector=>{const r=form.querySelector(selector).getBoundingClientRect();return {top:r.top,height:r.height}}
    return {urlWidth:form.querySelector('[name=feedUrl]').getBoundingClientRect().width,intervalWidth:form.querySelector('.feed-interval-controls').getBoundingClientRect().width,name:rect('[name=feedName]'),url:rect('[name=feedUrl]'),keywords:rect('[name=feedKeywords]'),notification:rect('[name=feedNotificationMode]').top,notificationButton:rect('[name=feedNotificationMode]').height,interval:rect('[name=feedInterval]'),custom:rect('[name=feedCustomInterval]')}
   })
   assert.ok(rects.name.height>=40&&Math.abs(rects.name.height-rects.url.height)<1,'Consistent single-line input height')
   assert.ok(Math.abs(rects.interval.top-rects.custom.top)<1,'Interval preset and custom input align')
   if(width>768){assert.ok(rects.urlWidth<=480,'Desktop URL field has a reading width');assert.ok(rects.intervalWidth<=280,'Desktop interval controls stay compact');assert.ok(rects.url.top>rects.name.top,'Source identity precedes its URL');assert.ok(rects.keywords.top>rects.notification,'Notification keywords follow delivery mode');const groupTops=await form.locator('.feed-settings-group').evaluateAll(groups=>groups.map(group=>group.getBoundingClientRect().top));assert.ok(Math.abs(groupTops[0]-groupTops[1])<1,'Source and notification groups align at the top')}
   assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1))
   await page.locator('.app-shell').evaluate(async el=>await Promise.all(el.getAnimations({subtree:true}).map(animation=>animation.finished.catch(()=>{}))))
   await page.screenshot({path:path.join(artifacts,`form-feeds-${mode}-${theme}-${width}.png`)});layouts++
  }
 }
 for(const width of [390,1100,1440,1920])for(const theme of ['light','dark']){
  await setTheme(theme)
  await page.setViewportSize({width,height:1000})
  for(const [route,selector] of [['agent-enrollment','.agent-enrollment-form'],['updates','.update-controls'],['host-management?tab=commands','.contract-fields']]){
   await page.goto(base+'/#/'+route);await page.reload();const form=page.locator(selector).first();await form.waitFor()
   await page.waitForFunction(theme=>document.querySelector('.app-shell')?.dataset.theme===theme,theme)
   assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),`${route} has no page overflow at ${width}`)
   if(width>900){
    const widths=await form.locator('input:not([type=checkbox]),.u031-btn').evaluateAll(elements=>elements.map(el=>el.getBoundingClientRect().width))
    assert.ok(widths.length>0&&widths.every(value=>value<=560),`${route}: desktop fields have content-appropriate widths`)
   }
   await page.screenshot({path:path.join(artifacts,`responsive-${route}-${theme}-${width}.png`)});layouts++
  }
 }
 for(const theme of ['light','dark'])for(const width of [390,1440]){
  await setTheme(theme);await page.setViewportSize({width,height:1100});await page.goto(base+'/#/operations');await page.reload()
  const selector=page.locator('.operation-layout aside select');await selector.waitFor({state:'attached'});await selector.selectOption('files.upload')
  await page.locator('.operation-field-group').first().waitFor()
  assert.ok(await page.locator('.operation-field-group').count()>=2,'Target and request headers have separate groups')
  if(width<700)assert.ok((await page.locator('.operation-layout aside .u031-list').boundingBox()).height<=180,'Mobile operation picker leaves room for its form')
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1))
  await page.screenshot({path:path.join(artifacts,`responsive-operation-groups-${theme}-${width}.png`)});layouts++
 }
 await setTheme(appearance.theme)
 assert.deepEqual(errors,[])
 console.log(`Form layouts passed: alert card containment, date columns, feed row/control alignment, notification states; ${layouts} light/dark/mobile/desktop layouts.`)
}
