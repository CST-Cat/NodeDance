const assert=require('node:assert/strict'),path=require('node:path')
function channels(color){const v=color.startsWith('#')?(color.length===4?color.slice(1).split('').map(c=>c+c).join(''):color.slice(1)).match(/../g).map(v=>parseInt(v,16)):color.match(/[\d.]+/g).map(Number);return [...v.slice(0,3),v.length>3?(color.startsWith('#')?v[3]/255:v[3]):1]}
function luminance(color,under='#ffffff'){const c=channels(color),b=channels(under);return c.slice(0,3).map((v,i)=>v*c[3]+b[i]*(1-c[3])).map(v=>{v/=255;return v<=.04045?v/12.92:((v+.055)/1.055)**2.4}).reduce((n,v,i)=>n+v*[.2126,.7152,.0722][i],0)}
function contrast(a,b){const x=luminance(a,b),y=luminance(b);return (Math.max(x,y)+.05)/(Math.min(x,y)+.05)}
module.exports=async function({page,context,base,headers,artifacts,catalog}){
 const original=await(await context.request.get(base+'/api/v1/public/appearance')).json()
 async function theme(value){const r=await context.request.put(base+'/api/v1/settings/appearance',{headers,data:{displayName:original.displayName,backgroundColor:original.backgroundColor,theme:value}});assert.equal(r.status(),200)}
 const enrollment=catalog.operations.find(o=>o.name==='agents.enrollment'),deletion=catalog.operations.find(o=>o.name==='nodes.delete');assert.ok(enrollment&&deletion)
 const accepted=await context.request.fetch(base+enrollment.path,{method:enrollment.method,headers,data:{displayName:'Theme layout fixture',coreUrl:base}});assert.equal(accepted.status(),201);const fixtureNode=(await accepted.json()).nodeId
 const errors=[];page.on('pageerror',e=>errors.push(e.message))
 try{
  await theme('system')
  for(const mode of ['light','dark']){
   await page.emulateMedia({colorScheme:mode});await page.goto(base+'/#/agent-enrollment')
   await page.waitForFunction(mode=>document.querySelector('.app-shell')?.dataset.theme===mode,mode)
   const palette=await page.locator('.app-shell').evaluate(el=>{const s=getComputedStyle(el);return Object.fromEntries(['canvas','recessed','surface','text','muted','muted-strong','control-bg','control-border','accent-fill','accent-foreground','focus'].map(k=>[k,s.getPropertyValue('--'+k).trim()]))})
   for(const surface of ['canvas','surface','control-bg'])for(const text of ['text','muted','muted-strong'])assert.ok(contrast(palette[text],palette[surface])>=4.5,`${mode} ${text} on ${surface}: ${contrast(palette[text],palette[surface])}`)
   // The primary Figma reference specifies a 30% resting border. Its exact
   // alpha replaces the old 3:1 resting-border requirement; focus stays explicit.
   const kit=await page.locator('.app-shell').evaluate(el=>{const s=getComputedStyle(el);return Object.fromEntries(['background-tertiary','background-primary','border-secondary'].map(k=>[k,s.getPropertyValue('--figma-'+k).trim()]))})
   assert.equal(palette.canvas,kit['background-tertiary'])
   assert.equal(palette['control-bg'],kit['background-primary'])
   assert.equal(palette['control-border'],kit['border-secondary'])
   assert.deepEqual(channels(palette.canvas),channels(mode==='dark'?'#141413':'#faf9f5'))
   assert.deepEqual(channels(palette['control-bg']),channels(mode==='dark'?'#30302e':'#ffffff'))
   assert.ok(contrast(palette.focus,palette['control-bg'])>=3,'Keyboard focus contrast')
   if(mode==='dark')assert.ok(luminance(palette.canvas)<luminance(palette.recessed)&&luminance(palette.recessed)<luminance(palette.surface),'Warm dark surface hierarchy')
   await page.locator('.agent-enrollment-form input').nth(1).fill('Theme fixture')
   const button=page.locator('.agent-enrollment-form .primary-button');await button.waitFor();await page.waitForFunction(()=>!document.querySelector('.agent-enrollment-form .primary-button').disabled)
   const colors=await button.evaluate(el=>({fg:getComputedStyle(el).color,bg:getComputedStyle(el).backgroundColor}));assert.ok(contrast(colors.fg,colors.bg)>=4.5,`${mode} primary button contrast: ${JSON.stringify(colors)}`)
   await button.hover();const hover=await button.evaluate(el=>({fg:getComputedStyle(el).color,bg:getComputedStyle(el).backgroundColor}));assert.ok(contrast(hover.fg,hover.bg)>=4.5,'Primary hover contrast');await page.mouse.down();const pressed=await button.evaluate(el=>({fg:getComputedStyle(el).color,bg:getComputedStyle(el).backgroundColor,filter:getComputedStyle(el).filter}));assert.equal(pressed.filter,'none');assert.ok(contrast(pressed.fg,pressed.bg)>=4.5,'Primary pressed contrast');await page.mouse.move(1,1);await page.mouse.up();
   await button.focus();await page.keyboard.press('Shift+Tab');await page.keyboard.press('Tab');assert.ok(await button.evaluate(el=>el===document.activeElement));assert.notEqual(await button.evaluate(el=>getComputedStyle(el).outlineStyle),'none')
   for(const width of [390,1440]){
    await page.setViewportSize({width,height:1000});await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));await page.evaluate(()=>window.scrollTo(0,0));await page.waitForFunction(()=>[...document.querySelectorAll('.app-shell,.workspace-sidebar')].every(el=>el.getAnimations().every(a=>a.playState==='finished'||a.playState==='idle')));assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1))
    const sidebar=await page.locator('.workspace-sidebar').boundingBox();assert.ok(width<=900?sidebar.x+sidebar.width<=1:sidebar.x>=-1,'Sidebar stays offscreen when the mobile drawer is closed');if(width>900){const content=await page.locator('.workspace-page').boundingBox();assert.ok(content.x>=sidebar.x+sidebar.width,'Desktop content stays alongside the sidebar')}
    await page.screenshot({path:path.join(artifacts,`theme-enrollment-${mode}-${width}.png`)})
   }
   await page.goto(base+'/#/nodes');await page.locator('.nodes-dashboard').waitFor()
   assert.equal(await page.locator('.workspace-nav button[aria-current=page]').evaluate(el=>getComputedStyle(el).boxShadow),'none','Navigation selection has no left accent stripe')
   const readings=page.locator('.vps-card[data-node-id="'+fixtureNode+'"] .resource-rings>.resource-reading');await readings.first().waitFor()
   assert.ok(await readings.evaluateAll(items=>items.every(el=>getComputedStyle(el).borderTopWidth==='0px'&&getComputedStyle(el).borderLeftWidth==='0px')),'Metrics do not create nested card boundaries')
   await page.screenshot({path:path.join(artifacts,`theme-dashboard-${mode}.png`),fullPage:true})
  }
  await theme('light');await page.emulateMedia({colorScheme:'dark'});await page.reload();await page.waitForFunction(()=>document.querySelector('.app-shell')?.dataset.theme==='light')
  await theme('system');await page.reload();await page.waitForFunction(()=>document.querySelector('.app-shell')?.dataset.theme==='dark');await page.emulateMedia({colorScheme:'light'});await page.waitForFunction(()=>document.querySelector('.app-shell')?.dataset.theme==='light')
  assert.deepEqual(errors,[])
  console.log('Theme regression passed: real persisted light/system modes, live system switching, Figma surfaces/resting borders, text/button/focus contrast, dark depth, flat node readings; 6 layouts.')
 }finally{const removed=await context.request.fetch(base+deletion.path.replace('{nodeId}',fixtureNode),{method:deletion.method,headers,data:{confirm:true}});assert.ok(removed.ok());await theme(original.theme);await page.emulateMedia({colorScheme:'light'})}
}
