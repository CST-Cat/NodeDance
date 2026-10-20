const assert = require('node:assert/strict'), path = require('node:path'), fs = require('node:fs')
async function checkSearchTravel(page, source, target, action) {
 const origin=await page.locator(source).evaluate(el=>{const r=el.getBoundingClientRect();return {x:r.x,y:r.y+scrollY}})
 await action()
 await page.waitForFunction(selector=>{
  const el=document.querySelector(selector)
  return el?.getBoundingClientRect().width>0 && el.getAnimations().some(a=>a.effect.getKeyframes().some(f=>f.transform))
 },target)
 const frames=await page.locator(target).evaluate(el=>{
  const animation=el.getAnimations().find(a=>a.effect.getKeyframes().some(f=>f.transform))
  animation.pause()
  const rect=()=>{const r=el.getBoundingClientRect();return {x:r.x,y:r.y+scrollY}}
  animation.currentTime=0;const start=rect(),opacity=getComputedStyle(el).opacity
  animation.currentTime=animation.effect.getTiming().duration/2;const middle=rect()
  animation.finish();const end=rect()
  return {start,middle,end,opacity}
 })
 assert.ok(Math.hypot(frames.start.x-origin.x,frames.start.y-origin.y)<1,'Search starts at the previously visible glyph: '+JSON.stringify({origin,frames}))
 assert.equal(Number(frames.opacity),1,'The same magnifier moves without a replacement fading in at the endpoint')
 assert.ok(Math.hypot(frames.middle.x-frames.start.x,frames.middle.y-frames.start.y)>3,'Magnifier travels away from its origin')
 assert.ok(Math.hypot(frames.middle.x-frames.end.x,frames.middle.y-frames.end.y)>3,'Magnifier is still between endpoints halfway through')
}
module.exports = async function ({page, base, artifacts}) {
 const mode = () => page.locator('.overview-toolbar-slot').getAttribute('data-mode')
 const settle = () => page.waitForTimeout(600)
 for (const theme of ['light', 'dark']) for (const width of [320, 390, 700, 1440]) {
  await page.setViewportSize({width, height:800})
  await page.goto(base+'/#/nodes');await page.reload()
  await page.locator('.node-summary').waitFor();await page.evaluate(()=>scrollTo(0,0));await settle()
  await page.evaluate(theme => {
   document.querySelector('.app-shell').dataset.theme=theme
   const spacer=document.createElement('div');spacer.style.height='2000px'
   document.querySelector('.nodes-dashboard').append(spacer)
  },theme)
  const initial=await page.locator('.overview-toolbar-slot').evaluate(el=>({top:el.getBoundingClientRect().top+scrollY,height:el.offsetHeight,document:document.documentElement.scrollHeight}))
  if(width===390) {
   await checkSearchTravel(page,'.node-search svg','.overview-search-toggle svg',()=>page.evaluate(()=>scrollTo(0,80)))
   await settle()
   await checkSearchTravel(page,'.overview-search-toggle svg','.node-search svg',()=>page.evaluate(()=>scrollBy(0,-30)))
   await settle()
   // Reverse with the expanded glyph paused mid-flight, so the next morph
   // must inherit its actual rendered position rather than either endpoint.
   await page.evaluate(()=>scrollBy(0,40));await settle()
   await page.evaluate(()=>scrollBy(0,-30))
   await page.waitForFunction(()=>document.querySelector('.overview-toolbar-slot').dataset.mode==='expanded')
   await page.locator('.node-search svg').evaluate(el=>{const a=el.getAnimations().find(a=>a.effect.getKeyframes().some(f=>f.transform));a.pause();a.currentTime=a.effect.getTiming().duration/2})
   await checkSearchTravel(page,'.node-search svg','.overview-search-toggle svg',()=>page.evaluate(()=>scrollBy(0,40)))
   await settle();await page.evaluate(()=>scrollTo(0,0));await settle()
  }
  await page.evaluate(()=>scrollTo(0,80))
  if(width===390 && theme==='light') {
   await page.waitForTimeout(80)
   assert.ok(await page.locator('.overview-toolbar-slot').evaluate(el=>el.getAnimations().some(a=>a.playState==='running')),'Large reflow remains in motion after 80ms')
   assert.equal(await page.locator('.node-summary').evaluate(el=>getComputedStyle(el).overflowY),'visible','Moving numbers are not clipped by the compact strip')
   const capture=await page.context().newCDPSession(page)
   fs.writeFileSync(path.join(artifacts,'overview-motion-early.png'),Buffer.from((await capture.send('Page.captureScreenshot')).data,'base64'))
   await page.waitForTimeout(120)
   fs.writeFileSync(path.join(artifacts,'overview-motion-middle.png'),Buffer.from((await capture.send('Page.captureScreenshot')).data,'base64'))
   await capture.detach()
  }
  await settle()
  if(width>900){assert.equal(await mode(),'inline');continue}
  assert.equal(await page.locator('.overview-toolbar-slot').getAttribute('data-animating'),null,'Motion cleanup restores normal overflow')
  assert.equal(await mode(),'compact')
  const compact=await page.locator('.overview-toolbar-slot').evaluate(el=>{
   const panel=el.firstElementChild, summary=el.querySelector('.node-summary')
   return {top:el.getBoundingClientRect().top+scrollY,y:scrollY,height:el.offsetHeight,document:document.documentElement.scrollHeight,position:getComputedStyle(panel).position,
    reserve:el.closest(".nodes-dashboard").style.cssText,padding:getComputedStyle(el.closest(".nodes-dashboard")).paddingBottom,overflow:document.documentElement.scrollWidth>innerWidth,
    rows:[...el.querySelectorAll('.node-summary article,.overview-search-toggle,.dashboard-toolbar-actions>button')].map(e=>({x:e.getBoundingClientRect().x,y:e.getBoundingClientRect().y,h:e.getBoundingClientRect().height})),summaryWidth:summary.clientWidth,summaryScroll:summary.scrollWidth}
  })
  assert.ok(Math.abs(compact.top-initial.top)<1,'Original card upper edge stays anchored')
  assert.equal(compact.y,80,'Collapsing never scrolls the page')
  assert.ok(Math.abs(compact.document-initial.document)<2,'Document height remains stable '+JSON.stringify({width,initial,compact}))
  assert.ok(compact.height<initial.height)
  assert.ok(!['fixed','sticky'].includes(compact.position))
  assert.equal(compact.overflow,false)
  assert.equal(compact.rows.length,7)
  assert.ok(compact.summaryScroll<=compact.summaryWidth+1,'All four normal counts fit without clipping')
  assert.ok(compact.rows.every(r=>Math.abs((r.y+r.h/2)-(compact.rows[0].y+compact.rows[0].h/2))<5),'Counts and actions occupy one row')
  assert.ok(compact.rows.slice(4).every(r=>r.h>=44),'Actions retain touch targets')
  await page.screenshot({path:path.join(artifacts,`compact-${theme}-${width}.png`)})
  await page.evaluate(()=>scrollBy(0,-5));await settle();assert.equal(await mode(),'compact','Small scroll jitter is ignored')
  await page.evaluate(()=>scrollBy(0,-25));await settle();assert.equal(await mode(),'expanded')
  await page.evaluate(()=>scrollBy(0,40));await settle();assert.equal(await mode(),'compact')
  await page.locator('[data-expand-search]').click();await settle()
  assert.equal(await mode(),'expanded');await page.locator('#overview-node-search').fill('retained')
  await page.keyboard.press('Escape');await settle();assert.equal(await mode(),'compact')
  await page.locator('[data-expand-search]').click();await settle()
  assert.equal(await page.locator('#overview-node-search').inputValue(),'retained')
  await page.locator('#overview-node-search').fill('');await page.keyboard.press('Escape');await settle()
  await page.locator('.view-preferences-button').click();await settle()
  assert.equal(await mode(),'expanded');assert.ok(await page.locator('#node-view-preferences').isVisible())
  await page.screenshot({path:path.join(artifacts,`expanded-${theme}-${width}.png`)})
  await page.locator('.view-preferences-button').click();await settle()
  const beforeReversal=await page.evaluate(()=>document.documentElement.scrollHeight)
  await page.evaluate(()=>scrollBy(0,50));await page.waitForTimeout(40)
  await page.evaluate(()=>scrollBy(0,-35));await page.waitForTimeout(40)
  await page.evaluate(()=>scrollBy(0,35));await settle()
  assert.equal(await mode(),'compact','Interrupted motion reaches the latest state')
  assert.ok(Math.abs(await page.evaluate(()=>document.documentElement.scrollHeight)-beforeReversal)<3,'Reversal preserves document height '+JSON.stringify({initial,now:await page.evaluate(()=>({height:document.documentElement.scrollHeight,reserve:document.querySelector('.nodes-dashboard').style.cssText}))}))
  await page.evaluate(()=>scrollTo(0,0));await settle()
  const bottom=await page.evaluate(()=>{const bottom=document.documentElement.scrollHeight-innerHeight;scrollTo(0,bottom);return bottom})
  await settle();assert.ok(Math.abs(await page.evaluate(()=>scrollY)-bottom)<1,'Collapse near the bottom does not clamp or jump scrolling')
  await page.setViewportSize({width:1200,height:800});await settle();assert.equal(await mode(),'inline')
  assert.equal(await page.locator('.overview-search-toggle').isVisible(),false)

 }
 await page.emulateMedia({reducedMotion:'reduce'})
 await page.setViewportSize({width:390,height:800});await page.reload();await page.locator('.node-summary').waitFor()
 await page.evaluate(()=>{document.querySelector('.nodes-dashboard').style.minHeight='3000px';scrollTo(0,80)})
 await settle();assert.equal(await mode(),'compact')
 assert.equal(await page.locator('.overview-toolbar-slot').evaluate(el=>el.getAnimations({subtree:true}).length),0)
 await page.evaluate(()=>localStorage.setItem('nodedance.locale','zh-CN'))
 await page.setViewportSize({width:320,height:800});await page.reload();await page.locator('.node-summary').waitFor();await page.evaluate(()=>scrollTo(0,0));await settle()
 await page.evaluate(()=>{document.querySelector('.nodes-dashboard').style.minHeight='3000px';scrollTo(0,80)})
 await settle();assert.equal(await mode(),'compact')
 assert.deepEqual(await page.locator('.node-count-compact-label').allTextContents(),['总数','在线','离线','待接入'])
 await page.locator('.navigation-toggle').click();await settle()
 assert.equal(await page.locator('.overview-toolbar-panel').isVisible(),false)
 await page.locator('.navigation-toggle').click();await settle()
 assert.equal(await page.locator('.overview-toolbar-panel').isVisible(),true)
 await page.locator('.dashboard-toolbar-actions > .primary-button').click()
 await page.waitForURL('**/#/agent-enrollment')
 console.log('Overview scroll: anchored collapse, direction thresholds, search, preferences, desktop, themes and reduced motion passed')
}
