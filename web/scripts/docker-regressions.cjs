const {setBrowserLanguage}=require('./browser-language.cjs')
const {closeNavigation}=require('./sidebar-navigation.cjs')
// Presentation checks use a real isolated Core contract and synthetic Docker
// responses. Every mutation is intercepted; no Docker daemon or Agent is used.
const assert = require('node:assert/strict'), path = require('node:path')
module.exports = async function ({page, base, node, artifacts, catalog}) {
  const errors=[];const collectError=error=>errors.push(error.message);page.on('pageerror',collectError)
  function routePath(name, params = {}) {
    const operation = catalog.operations.find(operation => operation.name === name)
    assert.ok(operation, `Core advertises ${name}`)
    return operation.path.replace(/\{([^}]+)\}/g, (_, key) => encodeURIComponent(params[key]))
  }
  const now = () => new Date().toISOString(), agent = 'docker-browser-fixture'
  let online = true, stale = false, sequence = 0, dashboardSocket
  const records = Array.from({length:24}, (_, index) => {
    const running = [0,1,4,5].includes(index), paused = index === 4
    return {container:{id:(index+1).toString(16).padStart(64,'0'), name:`docker-fixture-${String(index+1).padStart(2,'0')}`, image:'example/very-long-image-name:fixture', imageId:'sha256:fixture', state:paused?'paused':running?'running':'exited', running, paused, restarting:false, hostNetwork:false, health:'none', healthcheckConfigured:false, stale:false, restartCount:0, ports:[{containerPort:8080,protocol:'tcp',exposed:true,configured:[],published:[{ip:'127.0.0.1',port:'18080'}]}], networks:[], mounts:[], observedAt:now(), startedAt:now(), ...(index===5?{compose:{project:'fixture',service:'web'}}:{})},generation:1,sequence:1,receivedAt:now()}
  })
  const submissions = [], tasks = new Map(), failures = new Set()
  function state() { return {status:online?'online':'offline',generation:1,serverTime:now(),leaseValidUntil:new Date(Date.now()+3600000).toISOString()} }
  function inventory() {
    const s=state()
    return {type:'node_containers',nodeId:node,state:s,inventory:{nodeId:node,agentId:agent,agentOnline:online,activeGeneration:1,dockerAvailability:'available',dockerEventsConnected:true,dockerSnapshotFresh:true,dataStale:stale,serverTime:s.serverTime,containers:records.map(record=>({...record,sequence:++sequence}))},preferenceIdentities:Object.fromEntries(records.map(record=>[record.container.id,'container:'+record.container.id]))}
  }
  const nodesPath = routePath('nodes.list'), containersPath = routePath('containers.list',{nodeId:node}), metricsPath = routePath('nodes.metrics',{nodeId:node}), settingsPath = routePath('dashboard.settings'), preferencesPath = routePath('nodes.preferences',{nodeId:node}), tasksPath = routePath('tasks.list',{nodeId:node})
  const actions = new Map(records.map(record=>[routePath('containers.action',{nodeId:node,containerId:record.container.id}),record.container.id]))
  const taskRoute = routePath('tasks.get',{nodeId:node,taskId:'TASK'}).replace('TASK','')
  const handler = async route => {
    const request=route.request(), pathname=new URL(request.url()).pathname
    if(pathname===nodesPath) return route.fulfill({json:{nodes:[{nodeId:node,agentId:agent,displayName:'Docker browser fixture',status:online?'online':'offline',generation:1,leaseValidUntil:state().leaseValidUntil,agentVersion:'agent-v0.1.13',capabilities:[]}],serverTime:now()}})
    if(pathname===containersPath && request.method()==='GET') return route.fulfill({json:inventory()})
    if(pathname===metricsPath) return route.fulfill({json:{type:'node_status',nodeId:node,state:state()}})
    if(pathname===settingsPath) return route.fulfill({json:{viewMode:'monitor',groupBy:'node',sortBy:'custom',nodeGroupBy:'status',nodeSortBy:'custom',customFields:['state','image','ports','health','uptime']}})
    if(pathname===preferencesPath && request.method()==='GET') return route.fulfill({json:{preferences:[]}})
    if(pathname===tasksPath) return route.fulfill({json:{tasks:[...tasks.values()],nextCursor:''}})
    if(actions.has(pathname) && request.method()==='POST') {
      const target=actions.get(pathname), body=request.postDataJSON(), key=request.headers()['idempotency-key']
      assert.ok(key,'Every selected target uses a discovered idempotency key')
      submissions.push({target,body,key})
      if(failures.has(target)) return route.fulfill({status:503,body:'Fixture response lost'})
      const id=(submissions.length+100).toString(16).padStart(32,'0')
      tasks.set(id,{taskId:id,nodeId:node,targetId:target,action:body.action,status:'succeeded',deliveryState:'acknowledged',reconciliationRequired:false,progress:{phase:'complete',completed:1,total:1},result:{observedState:'fixture'},createdAt:now(),updatedAt:now()})
      return route.fulfill({status:202,json:{taskId:id,status:'succeeded'}})
    }
    if(pathname.startsWith(taskRoute) && tasks.has(pathname.slice(taskRoute.length))) return route.fulfill({json:tasks.get(pathname.slice(taskRoute.length))})
    return route.fallback()
  }
  await page.route('**/api/**',handler)
  const wsPattern='**'+routePath('dashboard.watch')
  await page.routeWebSocket(wsPattern,ws=>{dashboardSocket=ws})
  const row=index=>page.locator(`[data-testid=docker-table] tr[data-container-id="${records[index].container.id}"]`)
  const checkbox=index=>row(index).getByRole('checkbox')
  const toolbar=page.getByTestId('docker-toolbar'), action=name=>toolbar.locator(`[data-action="${name}"]`), search=page.locator('.docker-view-controls input[type=search]')
  const clear=()=>page.getByRole('button',{name:'Clear selection',exact:true}).click()
  const dialog=page.locator('dialog.ui-dialog')
  async function confirm(value){await dialog.locator('input').fill(value);await dialog.locator('button[type=submit]').click();await dialog.waitFor({state:'hidden'})}
  async function publish(){assert.ok(dashboardSocket);dashboardSocket.send(JSON.stringify(inventory()));await page.waitForTimeout(100)}
  await page.goto(base+`/#/nodes?node=${node}&section=docker`)
  await row(0).waitFor()
  const filters=page.locator('.docker-view-controls')
  const filterControls=filters.locator('input[type=search], button[data-dropdown-field]')
  assert.equal(await filterControls.count(),3,'Search, state and sort controls exist')
  assert.equal(await toolbar.locator('button').count(),6,'Primary actions and More exist')
  const visibleControls=[...await filterControls.all(),page.locator('.docker-selection-bar').getByRole('button',{name:'Clear selection',exact:true}),...await toolbar.locator('button').all()]
  const heights=await Promise.all(visibleControls.map(control=>control.evaluate(element=>element.getBoundingClientRect().height)))
  assert.ok(Math.max(...heights)-Math.min(...heights)<1,'Docker filters and actions share one visible height: '+heights.join(', '))
  if(process.env.NODEDANCE_DOCKER_REVIEW)await page.screenshot({path:process.env.NODEDANCE_DOCKER_REVIEW})

  assert.equal(await page.locator('.docker-row[draggable=true], .drag-handle').count(),0,'Docker drag sorting is removed')
  assert.equal(await row(0).getByRole('button',{name:'Stop',exact:true}).count(),0,'Actions are not repeated on rows')
  assert.ok(await action('restart').isDisabled(),'No selection cannot run a task')
  await checkbox(0).check();await checkbox(1).check()
  assert.ok(await action('rename').isDisabled(),'Rename requires exactly one container')
  assert.ok(await action('delete').isDisabled(),'Running containers cannot be deleted')
  await action('restart').click()
  await page.waitForFunction(()=>document.querySelectorAll('.container-task-status').length>=2)
  assert.deepEqual(submissions.map(item=>item.target).sort(),[records[0].container.id,records[1].container.id].sort(),'Batch dispatch targets exactly the selected containers')
  assert.ok(submissions.every(item=>item.body.action==='restart'))
  assert.notEqual(submissions[0].key,submissions[1].key,'Each target has its own task identity')
  await search.fill('docker-fixture-01');assert.equal(await page.getByTestId('docker-table').getByRole('checkbox').count(),1)
  assert.equal(await page.getByTestId('docker-table').getByRole('checkbox').filter({visible:true}).count(),1)
  assert.ok(await action('rename').isEnabled(),'Filtering removes hidden selected targets')
  await search.fill('no-matching-container');await page.waitForFunction(()=>!document.querySelector('[data-testid=docker-table]'))
  assert.ok(await action('restart').isDisabled(),'An empty filter removes all selected targets')
  await search.fill('');await row(0).waitFor()
  await checkbox(2).check();await checkbox(3).check();const beforeDelete=submissions.length
  await action('delete').click();await confirm(records[2].container.id)
  assert.equal(submissions.length,beforeDelete,'Incomplete batch confirmation submits nothing')
  await action('delete').click();await confirm(`${records[2].container.id}, ${records[3].container.id}`)
  await page.waitForFunction(()=>document.querySelectorAll('.container-task-status').length>=4)
  assert.deepEqual(submissions.slice(beforeDelete).map(item=>[item.target,item.body.deleteConfirmed,item.body.deleteConfirmationId]),[2,3].map(index=>[records[index].container.id,true,records[index].container.id]),'Each delete carries its exact Core confirmation')
  await clear();await checkbox(2).check();await action('delete').click()
  records[2].container.running=true;records[2].container.state='running';await publish()
  const beforeChanged=submissions.length;await confirm(records[2].container.id)
  assert.equal(submissions.length,beforeChanged,'Changed runtime state while confirming prevents deletion')
  records[2].container.running=false;records[2].container.state='exited';await publish()
  await clear();await checkbox(5).check();assert.ok(await action('rename').isDisabled(),'Compose containers retain the rename restriction')
  await clear();await checkbox(0).check();stale=true;await publish();assert.ok(await action('restart').isDisabled(),'Stale inventory disables toolbar mutations')
  stale=false;online=false;await publish();assert.ok(await action('restart').isDisabled(),'Offline nodes disable toolbar mutations')
  online=true;await publish();await clear()
  await page.getByRole('checkbox',{name:'Select this page',exact:true}).check()
  assert.equal(await page.getByTestId('docker-table').locator('[role=checkbox][data-state=checked]').count(),20,'Select all applies to the current page')
  await page.getByRole('button',{name:'Next',exact:true}).click();assert.equal(await page.getByTestId('docker-table').getByRole('checkbox').count(),4)
  assert.ok(!(await page.getByRole('checkbox',{name:'Select this page',exact:true}).isChecked()))
  await clear();await page.getByRole('button',{name:'Previous',exact:true}).click();await checkbox(0).check();await checkbox(1).check()
  failures.add(records[1].container.id);await action('restart').click();await row(1).getByRole('alert').waitFor()
  const lost=submissions.filter(item=>item.target===records[1].container.id).at(-1)
  await action('restart').click();await page.waitForTimeout(150)
  assert.equal(submissions.filter(item=>item.target===records[1].container.id).at(-1).key,lost.key,'Uncertain batch retry preserves each target identity')
  failures.clear();await clear();await row(0).getByRole('button',{name:records[0].container.name,exact:true}).click()
  await page.getByTestId('container-detail').waitFor()
  await publish();assert.ok(await page.getByTestId('container-detail').isVisible(),'Inventory refresh preserves the selected detail')
  const more=page.getByRole('menu'), moreToggle=toolbar.getByRole('button',{name:'More',exact:true})
  await moreToggle.click();await more.getByRole('menuitem',{name:'Logs & monitoring',exact:true}).click()
  await page.getByTestId('container-streams').waitFor()
  assert.equal(await page.getByTestId('container-streams').count(),1,'Streams exist only for the selected container')
  assert.ok(await moreToggle.evaluate(element=>document.activeElement===element),'Closing the action menu preserves keyboard focus')
  await checkbox(1).check();await page.getByTestId('container-streams').waitFor({state:'detached'})
  await clear();await row(0).getByRole('button',{name:records[0].container.name,exact:true}).click()
  await moreToggle.click();await page.keyboard.press('Escape');await more.waitFor({state:'detached'});assert.equal(await more.count(),0,'Escape closes the top action menu')
  for(const language of ['en','zh-CN']) {
    await setBrowserLanguage(page,language)
    for(const [theme,width] of [['light',1440],['dark',390]]){
      await page.setViewportSize({width,height:1000});await page.waitForTimeout(100)
      await closeNavigation(page)
      await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
      assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Docker list fits the viewport')

    }
  }
  await setBrowserLanguage(page,'en');await page.setViewportSize({width:1440,height:1000})
  await page.unroute('**/api/**',handler)
  page.off('pageerror',collectError);assert.deepEqual(errors,[],'Docker management must not produce browser errors')
  console.log("docker-regressions passed.")
}
