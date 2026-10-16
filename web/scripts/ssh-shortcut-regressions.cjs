// Isolated Core preferences plus synthetic machine observations; never connect over SSH.
const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,base,headers,artifacts,catalog}){
 const operation=name=>{const result=catalog.operations.find(o=>o.name===name);assert.ok(result);return result}
 const created=await page.request.post(base+operation('agents.enrollment').path,{headers,data:{displayName:'SSH discovery fixture',coreUrl:base}})
 assert.equal(created.status(),201);const node=(await created.json()).nodeId
 const routePath=name=>operation(name).path.replace('{nodeId}',node)
 let online=true,reads=0,ambiguous=false,diagDelay=100,parkDiagnostics=false,parked=[],socket
 const state=()=>({status:online?'online':'offline',generation:1,serverTime:new Date().toISOString(),leaseValidUntil:new Date(Date.now()+(online?3600000:-1000)).toISOString()})
 await page.route('**'+routePath('nodes.list'),route=>route.fulfill({json:{nodes:[{nodeId:node,agentId:'ssh-shortcut-fixture',displayName:'SSH discovery fixture',...state(),capabilities:[]}],serverTime:state().serverTime}}))
 await page.route('**'+routePath('nodes.metrics'),route=>route.fulfill({json:{type:'node_status',nodeId:node,state:state()}}))
 await page.route('**'+routePath('agents.diagnostics'),async route=>{
  reads++
  if(parkDiagnostics){parked.push(route);return}
  await new Promise(resolve=>setTimeout(resolve,diagDelay))
  const users=ambiguous?['machine_user','other_user']:['machine_user']
  return route.fulfill({json:{status:200,ssh:{status:'available',hosts:['2001:db8::1'],ports:[2222],users,host:'2001:db8::1',port:2222,...(ambiguous?{}:{user:'machine_user'}),command:ambiguous?'ssh -p 2222 2001:db8::1':'ssh -p 2222 machine_user@2001:db8::1',commands:Object.fromEntries(users.map(user=>[user,`ssh -p 2222 ${user}@2001:db8::1`])),source:'discovered',sampledAt:new Date().toISOString()}}})
 })
 await page.routeWebSocket('**'+routePath('dashboard.watch'),ws=>{socket=ws})
 await page.addInitScript(()=>Object.defineProperty(navigator,'clipboard',{value:{writeText:async value=>{window.sshCopiedCommand=value}}}))
 const initial=await page.request.get(base+routePath('nodes.preferences'));assert.deepEqual((await initial.json()).preferences,[],'No stored connection or remembered login is required')
 // Park discovery from the start so the clicked Copy SSH button is guaranteed
 // to hit a pending detection below.
 parkDiagnostics=true
 await page.goto(base+'/#/nodes')
 const shortcut=page.getByTestId('node-ssh-shortcut')
 const dialog=page.locator('dialog.ui-dialog')
 await shortcut.waitFor();assert.ok(await shortcut.isEnabled())
 // Copy feedback: every bubble is a pure floating overlay. The card, telemetry,
 // Docker toggle and every action row must keep exactly the same boxes before,
 // during and after the popup; only the popup itself may have geometry.
 const layoutProbe=()=>page.evaluate(()=>{
  const card=document.querySelector('.vps-card')
  const c=card.getBoundingClientRect()
  const rects=[]
  for(const el of [card,...card.querySelectorAll('*')]){
   if(el.classList.contains('vps-card-ssh-pop'))continue
   const r=el.getBoundingClientRect()
   if(r.width===0&&r.height===0)continue // zero-area anchors (svg titles) have no geometry
   rects.push([Math.round((r.left-c.left)*2)/2,Math.round((r.top-c.top)*2)/2,Math.round(r.width*2)/2,Math.round(r.height*2)/2])
  }
  return rects
 })
 const assertLayoutStable=async (label,before)=>{
  const current=await layoutProbe()
  const stable=before.length===current.length&&before.every((rect,index)=>rect.every((value,axis)=>Math.abs(value-current[index][axis])<=1))
  assert.ok(stable,'Card and control geometry must be identical before/during/after the popup: '+label)
 }
 const anchor=async ()=>{
  const popBox=await pop.boundingBox(),buttonBox=await shortcut.boundingBox()
  const above=popBox&&buttonBox&&popBox.y+popBox.height<=buttonBox.y+1
  assert.ok(above,'Copy feedback must float immediately above the clicked Copy SSH button')
  if(popBox&&buttonBox)assert.ok(popBox.x+popBox.width>buttonBox.x&&popBox.x<buttonBox.x+buttonBox.width,'Copy feedback must be anchored to the clicked button column')
 }
 // Let the dashboard finish its initial telemetry render so the baseline
 // reflects steady layout, not a transient settling state.
 await page.waitForFunction(()=>document.querySelectorAll('.vps-telemetry .ring').length>=4,'telemetry rendered',{timeout:8000}).catch(()=>{})
 await page.waitForTimeout(1000)
 const beforeLayout=await layoutProbe()
 await shortcut.click()
 const pop=page.locator('.vps-card .vps-card-ssh-pop')
 await pop.waitFor()
 const detectingText=await pop.innerText()
 assert.ok(detectingText.includes('Detecting SSH connection'),'Pending discovery shows the detecting bubble, got: '+detectingText)
 assert.ok(reads>=1,'Connection is discovered through the shared Core operation')
 await anchor()
 await assertLayoutStable('detecting bubble visible',beforeLayout)
 await pop.waitFor({state:'detached',timeout:6000})
 await assertLayoutStable('after detecting bubble hides',beforeLayout)
 assert.equal(await page.evaluate(()=>window.sshCopiedCommand),undefined,'The detecting bubble dismissed itself while discovery is still pending')
 assert.ok(parked.length>=1,'Discovery is still pending when the detecting bubble dismisses')
 parkDiagnostics=false
 const discovered={status:200,ssh:{status:'available',hosts:['2001:db8::1'],ports:[2222],users:['machine_user'],host:'2001:db8::1',port:2222,user:'machine_user',command:'ssh -p 2222 machine_user@2001:db8::1',commands:{machine_user:'ssh -p 2222 machine_user@2001:db8::1'},source:'discovered',sampledAt:new Date().toISOString()}}
 for(const held of parked)await held.fulfill({json:discovered})
 parked=[]
 // Resolved discovery opens the account dialog; nothing is copied until the
 // user explicitly confirms, even for a single account.
 await dialog.waitFor()
 assert.equal(await page.locator('dialog.ui-dialog[open]').count(),1,'Resolved discovery must open the account dialog instead of copying')
 const single=dialog.getByRole('radio')
 assert.equal(await single.count(),1,'A single detected account is offered alone')
 assert.ok(await single.first().isChecked(),'The resolved default account starts selected')
 assert.ok(await single.first().evaluate(el=>el===document.activeElement),'The dialog opens focused on the checked account')
 assert.deepEqual(await dialog.locator('.ui-dialog-choice-detail').allInnerTexts(),['ssh -p 2222 machine_user@2001:db8::1'],'The single account shows its command')
 assert.ok(await dialog.getByRole('button',{name:'Copy SSH',exact:true}).isVisible(),'The primary dialog action says Copy SSH, not a generic Confirm')
 await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'})
 assert.equal(await page.evaluate(()=>window.sshCopiedCommand),undefined,'Escape must not copy even with a single account')
 assert.ok(await shortcut.evaluate(el=>el===document.activeElement),'Closing returns the focus to the shortcut')
 await shortcut.click();await dialog.waitFor()
 await dialog.getByRole('button',{name:'Copy SSH',exact:true}).click()
 await page.waitForFunction(()=>window.sshCopiedCommand==='ssh -p 2222 machine_user@2001:db8::1')
 await dialog.waitFor({state:'hidden'});assert.ok(await shortcut.evaluate(el=>el===document.activeElement),'Copying returns the focus to the shortcut')
 await pop.waitFor()
 const copiedText=await pop.innerText()
 assert.ok(copiedText.includes('SSH command copied'),'A confirmed copy surfaces a fresh success bubble, got: '+copiedText)
 await anchor()
 await assertLayoutStable('success bubble visible',beforeLayout)
 await pop.waitFor({state:'detached',timeout:6000})
 await assertLayoutStable('after success bubble hides',beforeLayout)
 assert.equal(await page.locator('.vps-card-ssh-status,.vps-card-status').count(),0,'No persistent SSH feedback or status lines remain under the card')
 // The same overlay behaviour must hold on the narrow 390px layout, where the
 // Copy SSH button shares the card with a second row.
 await page.setViewportSize({width:390,height:1000})
 const beforeNarrow=await layoutProbe()
 await shortcut.click();await dialog.waitFor()
 assert.ok(await dialog.getByRole('button',{name:'Copy SSH',exact:true}).isEnabled(),'390px keeps the explicit copy confirmation')
 await dialog.getByRole('button',{name:'Copy SSH',exact:true}).click()
 await pop.waitFor()
 const narrowCopied=await pop.innerText()
 assert.ok(narrowCopied.includes('SSH command copied'),'390px re-copy surfaces the transient success bubble, got: '+narrowCopied)
 await anchor()
 await assertLayoutStable('390px success bubble visible',beforeNarrow)
 await pop.waitFor({state:'detached',timeout:6000})
 await assertLayoutStable('390px after bubble hides',beforeNarrow)
 for(const theme of ['light','dark'])for(const width of [390,1440]){
  await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  const scrim=page.locator('.navigation-scrim');if(await scrim.isVisible())await scrim.click({position:{x:width-10,y:20}})
  const overlay=page.locator('.navigation-scrim');if(await overlay.isVisible())await overlay.click({position:{x:width-10,y:20}})
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
  await page.screenshot({path:path.join(artifacts,`ssh-discovery-${theme}-${width}.png`),fullPage:true})
 }
 // Ambiguous accounts move the choice behind the copy dialog; the card no
 // longer hosts an account picker and nothing is copied without explicit
 // confirmation in the dialog, whatever the account count.
 ambiguous=true;await page.reload();await shortcut.click();await dialog.waitFor()
 const options=dialog.getByRole('radio')
 assert.equal(await options.count(),3,'The resolved default plus every discovered account is offered')
 assert.ok(await options.first().isChecked(),'The resolved default account starts selected')
 assert.ok(await options.first().evaluate(el=>el===document.activeElement),'The dialog opens focused on the checked account')
 assert.ok((await dialog.locator('.ui-dialog-choice-detail').allInnerTexts()).every(text=>text.startsWith('ssh -p ')),'Every choice shows its command')
 await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'})
 assert.equal(await page.evaluate(()=>window.sshCopiedCommand),undefined,'Escape must not copy')
 assert.ok(await shortcut.evaluate(el=>el===document.activeElement),'Closing returns the focus to the shortcut')
 await shortcut.click();await dialog.waitFor()
 await dialog.getByRole('button',{name:'Cancel',exact:true}).click();await dialog.waitFor({state:'hidden'})
 assert.equal(await page.evaluate(()=>window.sshCopiedCommand),undefined,'Cancel must not copy')
 await shortcut.click();await dialog.waitFor()
 await options.nth(2).check()
 for(const theme of ['light','dark'])for(const width of [390,1440]){
  await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Account dialog must not overflow')
  assert.ok(await options.first().isVisible())
  await page.screenshot({animations:'disabled',path:path.join(artifacts,`ssh-accounts-${theme}-${width}.png`),fullPage:true})
 }
 await dialog.getByRole('button',{name:'Copy SSH',exact:true}).click()
 await page.waitForFunction(()=>window.sshCopiedCommand==='ssh -p 2222 other_user@2001:db8::1')
 await dialog.waitFor({state:'hidden'});assert.ok(await shortcut.evaluate(el=>el===document.activeElement),'Confirming returns the focus to the shortcut')
 online=false;assert.ok(socket);socket.send(JSON.stringify({type:'node_status',nodeId:node,state:state()}));await page.waitForFunction(()=>document.querySelector('.vps-card')?.dataset.online==='false')
 await page.waitForFunction(()=>document.querySelector('.vps-card .vps-card-version')?.textContent.includes('Agent not connected'),'Registered but disconnected nodes read as Agent not connected')
 assert.ok(await shortcut.isEnabled());await shortcut.click();await dialog.waitFor()
 assert.ok(await options.nth(2).isChecked(),'The confirmed account stays preselected offline')
 await dialog.getByRole('button',{name:'Copy SSH',exact:true}).click()
 await page.waitForFunction(()=>window.sshCopiedCommand==='ssh -p 2222 other_user@2001:db8::1')
 assert.equal(await page.evaluate(()=>window.sshCopiedCommand),'ssh -p 2222 other_user@2001:db8::1','Last observed connection remains copyable offline')
 console.log('SSH discovery regressions passed: no saved connection, automatic address/account/custom port, dialog-first copy for a single and for several accounts, explicit Copy SSH confirmation with cancel/Escape refusal, focus return, pending detection bubble auto-dismiss, delayed dialog after discovery, offline reuse; 8 card layouts and 8 dialog layouts.')
}
