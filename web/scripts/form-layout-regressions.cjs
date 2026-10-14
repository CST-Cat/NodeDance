const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,base,artifacts}){
 const errors=[];page.on('pageerror',error=>errors.push(error.message))
 let layouts=0
 for(const theme of ['light','dark'])for(const width of [390,1100,1440,1920]){
  await page.setViewportSize({width,height:1000})
  for(const tab of ['windows','rules','channels']){
   await page.goto(base+'/#/alerts?tab='+tab);await page.locator('.alert-form-card').waitFor()
   await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
   const outside=await page.locator('.alert-form-card').evaluate(form=>{
    const card=form.getBoundingClientRect(),style=getComputedStyle(form),left=card.left+parseFloat(style.paddingLeft),right=card.right-parseFloat(style.paddingRight)
    return [...form.querySelectorAll('input:not([type=checkbox]),textarea,.u031-btn,button[type=submit]')].filter(el=>{const r=el.getBoundingClientRect();return r.width&&r.height&&(r.left<left-1||r.right>right+1)}).map(el=>el.outerHTML.slice(0,160))
   })
   assert.deepEqual(outside,[],`Alert ${tab} controls outside card (${theme}, ${width})`)
   assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1))
   await page.locator('.app-shell').evaluate(async el=>await Promise.all(el.getAnimations({subtree:true}).map(animation=>animation.finished.catch(()=>{}))))
   await page.screenshot({path:path.join(artifacts,`form-${tab}-${theme}-${width}.png`),fullPage:true});layouts++
  }
  await page.goto(base+'/#/feeds');const form=page.locator('.feed-form');await form.waitFor()
  await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  for(const mode of ['off','immediate','digest']){
   await form.locator('[name=feedNotificationMode]').selectOption(mode)
   const outside=await form.evaluate(form=>{
    const card=form.getBoundingClientRect(),style=getComputedStyle(form),left=card.left+parseFloat(style.paddingLeft),right=card.right-parseFloat(style.paddingRight)
    return [...form.querySelectorAll('input:not([type=checkbox]),textarea,.u031-btn,fieldset')].filter(el=>{const r=el.getBoundingClientRect();return r.width&&r.height&&(r.left<left-1||r.right>right+1)}).map(el=>el.outerHTML.slice(0,160))
   })
   assert.deepEqual(outside,[],`Feed ${mode} controls outside card (${theme}, ${width})`)
   const rects=await form.evaluate(form=>{
    const rect=selector=>{const r=form.querySelector(selector).getBoundingClientRect();return {top:r.top,height:r.height}}
    return {name:rect('[name=feedName]'),url:rect('[name=feedUrl]'),keywords:rect('[name=feedKeywords]'),notification:rect('[name=feedNotificationMode]').top,notificationButton:rect('[name=feedNotificationMode]').height,interval:rect('[name=feedInterval]'),custom:rect('[name=feedCustomInterval]')}
   })
   assert.ok(rects.name.height>=40&&Math.abs(rects.name.height-rects.url.height)<1,'Consistent single-line input height')
   assert.ok(Math.abs(rects.interval.top-rects.custom.top)<1,'Interval preset and custom input align')
   if(width>768){assert.ok(Math.abs(rects.name.top-rects.url.top)<1,'Name and URL inputs align');assert.ok(Math.abs(rects.keywords.top-rects.notification)<1,'Keywords and notification inputs align')}
   assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1))
   await page.locator('.app-shell').evaluate(async el=>await Promise.all(el.getAnimations({subtree:true}).map(animation=>animation.finished.catch(()=>{}))))
   await page.screenshot({path:path.join(artifacts,`form-feeds-${mode}-${theme}-${width}.png`),fullPage:true});layouts++
  }
 }
 assert.deepEqual(errors,[])
 console.log(`Form layouts passed: alert card containment, date columns, feed row/control alignment, notification states; ${layouts} light/dark/mobile/desktop layouts.`)
}
