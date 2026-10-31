const {closeNavigation}=require('./sidebar-navigation.cjs')
// Project-wide UI tasks are intercepted; never call Docker or a real Agent.
const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,base,node,catalog,artifacts}){
 const operation=name=>{const value=catalog.operations.find(item=>item.name===name);assert.ok(value);return value}
 const routePath=(name,params={})=>operation(name).path.replace(/\{([^}]+)\}/g,(_,key)=>params[key])
 const keys=['d','e','f'].map(letter=>letter.repeat(64)),now=()=>new Date().toISOString()
 const all=keys.map((key,index)=>({ref:{key,name:'compose-fixture-'+index,workingDirectory:'/fixtures/project-'+index,configFiles:['/fixtures/project-'+index+'/compose.yaml']},configAvailable:true,services:[{name:'web',instances:[{containerId:'123456789abc'+'0'.repeat(52),containerName:'long-project-name-web-instance-1',state:'running',health:'healthy'}]}]}))
 let projects=[...all],stale=false,fail=true;const submissions=[],tasks=new Map()
 const list=routePath('compose.list',{nodeId:node}),taskPrefix=routePath('tasks.get',{nodeId:node,taskId:'TASK'}).replace('TASK','')
 const actions=new Map(keys.map(key=>[routePath('compose.action',{nodeId:node,projectKey:key}),key]))
 await page.route('**/api/**',async route=>{
  const request=route.request(),pathname=new URL(request.url()).pathname
  if(pathname===list&&request.method()==='GET')return route.fulfill({json:{projects,dataStale:stale,serverTime:now()}})
  if(actions.has(pathname)&&request.method()==='POST'){
   const target=actions.get(pathname),body=request.postDataJSON(),key=request.headers()['idempotency-key'];assert.ok(key);submissions.push({target,body,key})
   if(target===keys[1]&&body.action==='start'&&fail)return route.fulfill({status:503,body:'Lost project response'})
   if(body.action==='delete')all.find(project=>project.ref.key===target).services=[]
   const id=(submissions.length+1000).toString(16).padStart(32,'0')
   tasks.set(id,{taskId:id,nodeId:node,targetId:target,action:'compose_'+body.action,status:'succeeded',deliveryState:'acknowledged',reconciliationRequired:false,progress:{phase:'complete',completed:1,total:1},result:{observedState:body.action==='delete'?'removed':'running'},createdAt:now(),updatedAt:now()})
   return route.fulfill({status:202,json:{taskId:id,status:'succeeded'}})
  }
  if(pathname.startsWith(taskPrefix)&&tasks.has(pathname.slice(taskPrefix.length)))return route.fulfill({json:tasks.get(pathname.slice(taskPrefix.length))})
  return route.fallback()
 })
 await page.goto(base+`/#/nodes?node=${node}&section=compose`)
 const cards=page.locator('.compose-project-card'),toolbar=page.getByTestId('compose-toolbar'),button=action=>toolbar.locator(`[data-action=${action}]`),selection=page.locator('.compose-selection-bar')
 await cards.first().waitFor();assert.equal(await page.locator('.compose-create-form').count(),0);assert.ok((await selection.innerText()).includes('projects'));assert.ok(!(await page.locator('#compose-projects-title').innerText()).includes(node));assert.equal(await page.locator('.compose-project-actions').count(),0)
 const select=async index=>cards.nth(index).getByRole('checkbox').check()
 const clear=async()=>{const button=selection.getByRole('button',{name:'Clear selection',exact:true});if(await button.isEnabled())await button.click()}
 const refresh=async()=>page.locator('.compose-projects > .section-toolbar').getByRole('button',{name:'Refresh',exact:true}).click()
 const confirm=()=>page.locator('.ui-dialog').getByRole('button',{name:'Confirm',exact:true}).click()
 await select(0);await select(1);assert.ok(await button('rename').isDisabled());assert.ok(await button('edit').isDisabled())
 await button('start').click();await cards.nth(1).getByRole('alert').waitFor()
 assert.deepEqual(submissions.map(value=>value.target),keys.slice(0,2));assert.ok((await cards.nth(0).innerText()).includes('Start'));assert.ok(!(await cards.nth(0).innerText()).includes('undefined'))
 fail=false;await cards.nth(1).getByRole('button',{name:'Retry this project',exact:true}).click();await page.waitForFunction(()=>!document.querySelector('.compose-project-error'))
 assert.equal(submissions[1].key,submissions[2].key,'Retry only failed project with its original intent');assert.equal(submissions[2].target,keys[1])
 for(const action of ['stop','restart','deploy']){
  const before=submissions.length;await button(action).click();await confirm()
  await page.waitForFunction(()=>!document.querySelector('.compose-project-select [role=checkbox]:disabled'))
  assert.equal(submissions.length,before+2);assert.ok(submissions.slice(before).every(value=>value.body.action===action))
 }
 // Confirmations cannot bypass a stale inventory or a removed selected target.
 let before=submissions.length;await button('delete').click();stale=true;await confirm();await page.locator('.compose-error').waitFor();assert.equal(submissions.length,before)
 stale=false;await refresh();await page.waitForFunction(()=>!document.querySelector('.compose-stale'));await button('delete').click();projects=all.slice(1);await confirm();await page.waitForFunction(expected=>document.querySelectorAll('.compose-project-card').length===expected,all.length-1);assert.equal(submissions.length,before)
 projects=[...all];await refresh();await page.waitForFunction(expected=>document.querySelectorAll('.compose-project-card').length===expected,all.length)
 await clear();await select(0);await button('rename').click();await page.locator('.ui-dialog input').fill('My project display name');await confirm();await cards.nth(0).getByRole('heading',{name:'My project display name',exact:true}).waitFor()
 const preferences=await(await page.request.get(base+routePath('nodes.preferences',{nodeId:node}))).json();assert.equal(preferences.preferences.find(item=>item.identity==='compose_project:'+keys[0]).alias,'My project display name')
 assert.equal(all[0].ref.name,'compose-fixture-0','Display name never changes Docker project identity')
 await select(1);await button('delete').click();assert.ok((await page.locator('.ui-dialog').innerText()).includes('Data volumes'));await confirm();await page.waitForFunction(()=>!document.querySelector('.compose-project-select [role=checkbox]:disabled'))
 assert.deepEqual(submissions.slice(-2).map(value=>value.body.deleteConfirmationKey),keys.slice(0,2))
 assert.equal(await cards.nth(0).locator('.compose-services').count(),0,'Deleted deployment retains its config entry without live instances')
 await selection.getByRole('checkbox').check();assert.equal(await cards.locator('[role=checkbox][data-state=checked]').count(),3);await clear();assert.equal(await cards.locator('[role=checkbox][data-state=checked]').count(),0)
 all[2].configAvailable=false;await refresh();await selection.getByRole('checkbox').check();assert.ok(await button('start').isDisabled());assert.ok(await button('delete').isDisabled());all[2].configAvailable=true;await refresh()
 for(const [theme,width] of [['light',1440],['dark',390]]){
  await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  await closeNavigation(page)
  await page.locator('#workspace-navigation').evaluate(async el=>await Promise.all(el.getAnimations({subtree:true}).map(a=>a.finished.catch(()=>{}))))
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
  assert.ok(await page.locator('.compose-service li').evaluateAll(rows=>rows.every(row=>row.querySelector('code').getBoundingClientRect().right<=row.querySelector('[data-state]').getBoundingClientRect().left+1)))

 }
 console.log("compose-regressions passed.")
}
