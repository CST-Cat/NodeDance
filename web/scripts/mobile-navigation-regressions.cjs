const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,base,artifacts}){
 await page.addInitScript(()=>{
  window.__navigationMotion=[]
  for(const event of ['animationstart','transitionrun'])document.addEventListener(event,e=>{
   if(e.target.id==='workspace-navigation')window.__navigationMotion.push({type:e.type,name:e.animationName||e.propertyName})
  },true)
 })
 for(const [theme,width] of [['light',1440],['dark',390]]){
  await page.setViewportSize({width,height:900})
  await page.goto(base+'/#/nodes')
  await page.reload() // Each layout must exercise a fresh document, not a hash navigation.
  const sidebar=page.locator('#workspace-navigation'),toggle=page.locator('.navigation-toggle')
  await sidebar.waitFor({state:'attached'})
  await page.waitForTimeout(500)
  assert.deepEqual(await page.evaluate(()=>window.__navigationMotion),[],`No initial navigation motion at ${width}px`)
  await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  const before=await toggle.evaluate(el=>{const s=getComputedStyle(el);return {border:s.borderTopWidth,outline:s.outlineStyle}})
  assert.equal(before.border,'0px','Only the menu toggle is borderless')
  if(width>900){
   assert.ok(await sidebar.locator('.wordmark').isVisible(),'Desktop retains its brand')

   continue
  }
  assert.equal(await toggle.getAttribute('aria-expanded'),'false')
  assert.ok(await sidebar.evaluate(el=>el.inert))
  assert.equal(await sidebar.evaluate(el=>el.getAnimations().length),0)

  await toggle.click()
  await page.waitForFunction(()=>document.querySelector('#workspace-navigation').getAnimations().some(a=>a.transitionProperty==='clip-path'))
  await sidebar.evaluate(el=>Promise.all(el.getAnimations().map(a=>a.finished)))
  assert.equal(await toggle.getAttribute('aria-expanded'),'true')
  assert.equal(await sidebar.evaluate(el=>getComputedStyle(el).backgroundColor),'rgb(240, 238, 230)','Mobile menu uses the requested warm background')
  assert.equal(await sidebar.locator('.workspace-nav button').first().evaluate(el=>getComputedStyle(el).color),'rgb(20, 20, 19)','Mobile navigation remains readable in both themes')
  assert.equal(await sidebar.locator('.wordmark').isVisible(),false,'Mobile menu omits the logo and brand wordmark')
  assert.equal(await sidebar.locator('.workspace-account').isVisible(),false,'Mobile menu omits account selection and its divider')
  const dividers=await sidebar.locator('.workspace-nav button').evaluateAll(elements=>elements.map(el=>{const s=getComputedStyle(el);return {width:s.borderBottomWidth,color:s.borderBottomColor,top:s.borderTopWidth}}))
  assert.ok(dividers.every(line=>line.width==='1px'&&line.top==='0px'&&(()=>{const rgba=line.color.match(/[\d.]+/g).map(Number);return rgba[0]===20&&rgba[1]===20&&rgba[2]===19&&Math.abs(rgba[3]-26/255)<0.003})()),'Each mobile menu item, including the selected item, has one subtle bottom divider: '+JSON.stringify(dividers))
  assert.equal(await sidebar.locator('.sidebar-context').evaluate(el=>getComputedStyle(el).borderTopWidth),'0px','Mobile menu omits the brand divider')
  const row=await sidebar.locator('.workspace-nav button').first().evaluate(el=>({height:el.getBoundingClientRect().height,padding:parseFloat(getComputedStyle(el).paddingTop),rem:parseFloat(getComputedStyle(document.documentElement).fontSize)}))
  assert.ok(row.height>=44&&row.padding<1.4*row.rem,'Mobile spacing is tighter while preserving touch targets: '+JSON.stringify(row))

  assert.equal(await page.locator('.navigation-scrim').count(),0,'Full-screen navigation has no dimming overlay')
  assert.equal(await page.evaluate(()=>getComputedStyle(document.body).overflow),'hidden')

  const box=await toggle.boundingBox()
  await page.mouse.move(box.x+box.width/2,box.y+box.height/2)
  await page.mouse.down()
  assert.deepEqual(await toggle.evaluate(el=>{const s=getComputedStyle(el);return [s.borderTopWidth,s.borderRightWidth,s.borderBottomWidth,s.borderLeftWidth]}),['0px','0px','0px','0px'],'Pressed X has no border')

  await page.mouse.up()
  assert.equal(await toggle.getAttribute('aria-expanded'),'false')
  await sidebar.evaluate(el=>Promise.all(el.getAnimations().map(a=>a.finished)))
  assert.ok(await sidebar.evaluate(el=>el.inert))
  assert.equal(await toggle.evaluate(el=>getComputedStyle(el).borderTopWidth),'0px','Closed hamburger has no border')
  await page.keyboard.press('Tab');await toggle.focus()
  assert.ok(await toggle.evaluate(el=>el.matches(':focus-visible')))
  assert.equal(await toggle.evaluate(el=>getComputedStyle(el).outlineStyle),'none')
  await page.keyboard.press('Enter')
  await sidebar.evaluate(el=>Promise.all(el.getAnimations().map(a=>a.finished)))
  const selected=sidebar.locator('[aria-current=page]')
  await selected.focus()
  assert.ok(await selected.evaluate(el=>parseFloat(getComputedStyle(el).outlineWidth)>0),'Other controls retain global keyboard focus outlines')
  await page.keyboard.press('Escape')
  assert.ok(await toggle.evaluate(el=>el===document.activeElement))
  await sidebar.evaluate(el=>Promise.all(el.getAnimations().map(a=>a.finished)))
 }
 // A lower selected entry must not scroll either layer as focus moves during
 // reveal. Sample every frame while repeatedly reversing the live transition.
 for(const theme of ['light','dark'])for(const viewport of [{width:390,height:600},{width:700,height:480}]){
  await page.setViewportSize(viewport)
  await page.goto(base+'/#/settings');await page.reload()
  const sidebar=page.locator('#workspace-navigation'),toggle=page.locator('.navigation-toggle')
  await sidebar.waitFor({state:'attached'});await page.waitForTimeout(500)
  await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  await sidebar.evaluate(el=>{el.scrollTop=0;el.querySelector('.sidebar-context').scrollTop=40})
  await page.evaluate(()=>{
   const menu=document.querySelector('#workspace-navigation'),list=menu.querySelector('.sidebar-context')
   const baseline={outer:menu.scrollTop,inner:list.scrollTop,y:scrollY,left:menu.getBoundingClientRect().left,width:menu.getBoundingClientRect().width}
   window.__menuFrames={baseline,violations:[],count:0,stop:false};let previous
   function sample(){
    const log=window.__menuFrames;if(log.stop)return
    const rect=menu.getBoundingClientRect(),clip=getComputedStyle(menu).clipPath
    const progress=parseFloat(clip.split(',')[2].trim().split(/\s+/)[1])
    const frame={open:document.querySelector('.navigation-toggle').getAttribute('aria-expanded')==='true',outer:menu.scrollTop,inner:list.scrollTop,y:scrollY,left:rect.left,width:rect.width,progress}
    log.count++
    for(const key of Object.keys(baseline))if(Math.abs(frame[key]-baseline[key])>1)log.violations.push({key,baseline:baseline[key],actual:frame[key]})
    if(previous&&previous.open===frame.open&&((frame.open&&progress<previous.progress-0.1)||(!frame.open&&progress>previous.progress+0.1)))log.violations.push({direction:frame.open?'opening':'closing',from:previous.progress,to:progress})
    previous=frame;requestAnimationFrame(sample)
   }sample()
  })
  const box=await toggle.boundingBox()
  for(let i=0;i<20;i++){
   await page.mouse.click(box.x+box.width/2,box.y+box.height/2)
   await page.waitForTimeout([35,95,160,20][i%4])
  }
  if(await toggle.getAttribute('aria-expanded')==='true')await page.mouse.click(box.x+box.width/2,box.y+box.height/2)
  await page.waitForTimeout(450)
  const sampled=await page.evaluate(()=>{window.__menuFrames.stop=true;return window.__menuFrames})
  assert.ok(sampled.count>20,'Rapid open/close captures rendered frames')
  assert.deepEqual(sampled.violations,[],`No scroll jumps or reveal reversals in ${theme} ${viewport.width}x${viewport.height}`)
  assert.ok(await sidebar.evaluate(el=>el.scrollHeight<=el.clientHeight+1),'The outer menu never becomes a second scroll container')

 }
 await page.emulateMedia({reducedMotion:'reduce'})
 await page.setViewportSize({width:390,height:900})
 await page.reload();await page.locator('#workspace-navigation').waitFor({state:'attached'})
 assert.equal(await page.locator('#workspace-navigation').evaluate(el=>el.getAnimations().length),0)
 await require('./sidebar-navigation.cjs').openNavigation(page)
 await require('./sidebar-navigation.cjs').closeNavigation(page)
 assert.equal(await page.locator('.navigation-toggle').getAttribute('aria-expanded'),'false')
 console.log('Mobile navigation passed: still first entry, opening/closing, no scrim, borderless pressed toggle, scoped keyboard focus, light/dark/mobile/desktop, frame-sampled rapid reversals with lower selected entries and reduced motion.')
}
