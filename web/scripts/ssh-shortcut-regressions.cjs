// Isolated Core preferences plus synthetic machine observations; never connect over SSH.
const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,base,headers,artifacts,catalog}){
 const operation=name=>{const result=catalog.operations.find(o=>o.name===name);assert.ok(result);return result}
 const created=await page.request.post(base+operation('agents.enrollment').path,{headers,data:{displayName:'SSH discovery fixture',coreUrl:base}})
 assert.equal(created.status(),201);const node=(await created.json()).nodeId
 const routePath=name=>operation(name).path.replace('{nodeId}',node)
 let online=true,reads=0,ambiguous=false,socket
 const state=()=>({status:online?'online':'offline',generation:1,serverTime:new Date().toISOString(),leaseValidUntil:new Date(Date.now()+(online?3600000:-1000)).toISOString()})
 await page.route('**'+routePath('nodes.list'),route=>route.fulfill({json:{nodes:[{nodeId:node,agentId:'ssh-shortcut-fixture',displayName:'SSH discovery fixture',...state(),capabilities:[]}],serverTime:state().serverTime}}))
 await page.route('**'+routePath('nodes.metrics'),route=>route.fulfill({json:{type:'node_status',nodeId:node,state:state()}}))
 await page.route('**'+routePath('agents.diagnostics'),async route=>{
  reads++;await new Promise(resolve=>setTimeout(resolve,100))
  const users=ambiguous?['machine_user','other_user']:['machine_user']
  return route.fulfill({json:{status:200,ssh:{status:'available',hosts:['2001:db8::1'],ports:[2222],users,host:'2001:db8::1',port:2222,...(ambiguous?{}:{user:'machine_user'}),command:ambiguous?'ssh -p 2222 2001:db8::1':'ssh -p 2222 machine_user@2001:db8::1',commands:Object.fromEntries(users.map(user=>[user,`ssh -p 2222 ${user}@2001:db8::1`])),source:'discovered',sampledAt:new Date().toISOString()}}})
 })
 await page.routeWebSocket('**'+routePath('dashboard.watch'),ws=>{socket=ws})
 await page.addInitScript(()=>Object.defineProperty(navigator,'clipboard',{value:{writeText:async value=>{window.sshCopiedCommand=value}}}))
 const initial=await page.request.get(base+routePath('nodes.preferences'));assert.deepEqual((await initial.json()).preferences,[],'No stored connection or remembered login is required')
 await page.goto(base+'/#/nodes')
 const shortcut=page.getByTestId('node-ssh-shortcut')
 await shortcut.waitFor();assert.ok(await shortcut.isEnabled());await shortcut.click()
 await page.waitForFunction(()=>window.sshCopiedCommand==='ssh -p 2222 machine_user@2001:db8::1')
 assert.ok(reads>=1,'Connection is discovered through the shared Core operation')
 for(const theme of ['light','dark'])for(const width of [390,1440]){
  await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  const scrim=page.locator('.navigation-scrim');if(await scrim.isVisible())await scrim.click({position:{x:width-10,y:20}})
  const overlay=page.locator('.navigation-scrim');if(await overlay.isVisible())await overlay.click({position:{x:width-10,y:20}})
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
  await page.screenshot({path:path.join(artifacts,`ssh-discovery-${theme}-${width}.png`),fullPage:true})
 }
 // Ambiguous accounts are offered as discovered choices; no text entry is needed.
 ambiguous=true;await page.reload();await page.locator('.ssh-user-choice select').waitFor()
 await page.locator('.ssh-user-choice select').selectOption('other_user');await shortcut.click()
 assert.equal(await page.evaluate(()=>window.sshCopiedCommand),'ssh -p 2222 other_user@2001:db8::1')
 for(const theme of ['light','dark'])for(const width of [390,1440]){
  await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  const overlay=page.locator('.navigation-scrim');if(await overlay.isVisible())await overlay.click({position:{x:width-10,y:20}})
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
  await page.screenshot({path:path.join(artifacts,`ssh-accounts-${theme}-${width}.png`),fullPage:true})
 }
 online=false;assert.ok(socket);socket.send(JSON.stringify({type:'node_status',nodeId:node,state:state()}));await page.waitForFunction(()=>document.querySelector('.vps-card')?.dataset.online==='false')
 assert.ok(await shortcut.isEnabled());await shortcut.click();assert.equal(await page.evaluate(()=>window.sshCopiedCommand),'ssh -p 2222 other_user@2001:db8::1','Last observed connection remains copyable offline')
 console.log('SSH discovery regressions passed: no saved connection, automatic address/account/custom port, delayed discovery copy, account choice, offline copy; 8 layouts.')
}
