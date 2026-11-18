const {closeNavigation}=require('./sidebar-navigation.cjs')
const assert = require('node:assert/strict'), path = require('node:path')
module.exports = async function ({page, base, node, artifacts, catalog}) {
  const errors=[];const collectError=error=>errors.push(error.message);page.on('pageerror',collectError)
  function routePath(name, params = {}) {
    const operation = catalog.operations.find(operation => operation.name === name)
    assert.ok(operation, `Core advertises ${name}`)
    return operation.path.replace(/\{([^}]+)\}/g, (_, key) => encodeURIComponent(params[key]))
  }
  const now = () => new Date().toISOString(), agent = 'stream-browser-fixture'
  let sequence = 0, dashboardSocket, logSocket
  const container = {id:'a1'.padEnd(64,'0'), name:'stream-fixture-01', image:'example/streams:fixture', imageId:'sha256:fixture', state:'running', running:true, paused:false, restarting:false, hostNetwork:false, health:'none', healthcheckConfigured:false, stale:false, restartCount:0, ports:[], networks:[], mounts:[], observedAt:now(), startedAt:now()}
  function fixtureState(){ return {status:'online',generation:1,serverTime:now(),leaseValidUntil:new Date(Date.now()+3600000).toISOString()} }
  function inventory(){
    const s=fixtureState()
    return {type:'node_containers',nodeId:node,state:s,inventory:{nodeId:node,agentId:agent,agentOnline:true,activeGeneration:1,dockerAvailability:'available',dockerEventsConnected:true,dockerSnapshotFresh:true,dataStale:false,serverTime:s.serverTime,containers:[{container,generation:1,sequence:++sequence,receivedAt:now()}]},preferenceIdentities:{[container.id]:'container:'+container.id}}
  }
  const nodesPath = routePath('nodes.list'), containersPath = routePath('containers.list',{nodeId:node}), metricsPath = routePath('nodes.metrics',{nodeId:node}), settingsPath = routePath('dashboard.settings'), preferencesPath = routePath('nodes.preferences',{nodeId:node}), tasksPath = routePath('tasks.list',{nodeId:node})
  const handler = async route => {
    const request=route.request(), pathname=new URL(request.url()).pathname
    if(pathname===nodesPath) return route.fulfill({json:{nodes:[{nodeId:node,agentId:agent,displayName:'Stream browser fixture',status:'online',generation:1,leaseValidUntil:fixtureState().leaseValidUntil,agentVersion:'agent-v0.1.13',capabilities:[]}],serverTime:now()}})
    if(pathname===containersPath && request.method()==='GET') return route.fulfill({json:inventory()})
    if(pathname===metricsPath) return route.fulfill({json:{type:'node_status',nodeId:node,state:fixtureState()}})
    if(pathname===settingsPath) return route.fulfill({json:{viewMode:'monitor',groupBy:'node',sortBy:'custom',nodeGroupBy:'status',nodeSortBy:'custom',customFields:['state','image','ports','health','uptime']}})
    if(pathname===preferencesPath && request.method()==='GET') return route.fulfill({json:{preferences:[]}})
    if(pathname===tasksPath) return route.fulfill({json:{tasks:[],nextCursor:''}})
    return route.fallback()
  }
  await page.route('**/api/**',handler)
  await page.routeWebSocket('**'+routePath('dashboard.watch'),ws=>{dashboardSocket=ws})
  const streamPattern='**'+routePath('containers.logs')+'*'
  let streamSequence=0
  const emitLog = text => logSocket.send(JSON.stringify({version:1,type:'container_log',generation:1,sequence:++streamSequence,requestId:'fixture',payload:{containerId:container.id,channel:'stdout',data:Buffer.from(text+'\n').toString('base64')}}))
  const emitBurst = count => { for(let i=1;i<=count;i++) emitLog(`fixture log line ${String(i+streamSequence).padStart(4,'0')} — streaming well past the panel fold for scroll assertions`) }
  const publish = () => { assert.ok(dashboardSocket);dashboardSocket.send(JSON.stringify(inventory())) }
  async function waitWatch(){
    for(let attempt=0;attempt<100&&!dashboardSocket;attempt++)await page.waitForTimeout(50)
    assert.ok(dashboardSocket,'Dashboard socket reconnected')
  }
  const output = page.getByTestId('stream-log-output')
  const jump = page.getByTestId('stream-log-follow')
  const measure = element => {
    const line=parseFloat(getComputedStyle(element).lineHeight), distance=element.scrollHeight-element.scrollTop-element.clientHeight
    return {scrollTop:element.scrollTop,distance,line,viewport:element.clientHeight,atBottom:distance<line/2,top:element.style.getPropertyValue('--scroll-fade-top'),bottom:element.style.getPropertyValue('--scroll-fade-bottom')}
  }
  const pinState = () => output.evaluate(measure)
  const lineCount = () => output.evaluate(element=>element.textContent.split('\n').length)
  const appendLines = async count => {
    const lines=await lineCount()
    emitBurst(count)
    await page.waitForFunction(expected=>document.querySelector('[data-testid=stream-log-output]')?.textContent.split('\n').length>=expected,lines+count)
  }
  const settle = () => output.evaluate(element=>new Promise(resolve=>{let top=element.scrollTop;const tick=()=>requestAnimationFrame(()=>{if(element.scrollTop===top)resolve();else{top=element.scrollTop;tick()}});tick()}))
  const wheelBy = async delta => {
    const from=(await pinState()).scrollTop
    await page.mouse.wheel(0,delta)
    await page.waitForFunction(top=>document.querySelector('[data-testid=stream-log-output]').scrollTop!==top,from)
    await settle()
    return pinState()
  }
  const reachBottom = () => page.waitForFunction(()=>{const el=document.querySelector('[data-testid=stream-log-output]');return el.scrollHeight-el.scrollTop-el.clientHeight<parseFloat(getComputedStyle(el).lineHeight)/2&&el.style.getPropertyValue('--scroll-fade-bottom')==='0px'}).then(pinState,pinState)
  async function openStreams(){
    await page.goto(base+`/#/nodes?node=${node}&section=docker`)
    logSocket=undefined;streamSequence=0
    await page.locator(`[data-testid=docker-table] tr[data-container-id="${container.id}"]`).waitFor()
    await waitWatch()
    publish()
    await page.locator(`[data-testid=docker-table] tr[data-container-id="${container.id}"]`).waitFor()
    const more=page.getByRole('menu')
    await page.locator(`[data-testid=docker-table] tr[data-container-id="${container.id}"]`).getByRole('button',{name:container.name,exact:true}).click()
    await page.getByTestId('container-detail').waitFor()
    await page.getByTestId('docker-toolbar').getByRole('button',{name:'More',exact:true}).click()
    await more.getByRole('menuitem',{name:'Logs & monitoring',exact:true}).click()
    await page.getByTestId('container-streams').waitFor()
    await page.getByRole('button',{name:'View logs',exact:true}).click()
    await page.getByTestId('stream-log-state').getByText('Receiving live data').waitFor()
    await page.waitForFunction(()=>document.querySelector('[data-testid=stream-log-output]')?.textContent.split('\n').length>=80)
  }

  await page.goto(base+`/#/nodes?node=${node}&section=docker`)
  await page.locator(`[data-testid=docker-table] tr[data-container-id="${container.id}"]`).waitFor()
  await page.routeWebSocket(streamPattern,ws=>{logSocket=ws;streamSequence=0
    logSocket.send(JSON.stringify({version:1,type:'container_stream_ready',generation:1,sequence:++streamSequence,requestId:'fixture',payload:{kind:'logs',containerId:container.id}}))
    emitBurst(80)})
  await openStreams()
  await page.waitForFunction(()=>{const el=document.querySelector('[data-testid=stream-log-output]');return el?.style.getPropertyValue('--scroll-fade-top')==='28px'&&el?.style.getPropertyValue('--scroll-fade-bottom')==='0px'})
  let pins=await pinState()
  assert.ok(pins.line>0&&pins.viewport>pins.line,'The log panel exposes a measurable line height inside its viewport: '+JSON.stringify(pins))
  assert.ok(pins.atBottom,'Fresh frames must keep the panel pinned to the bottom edge: '+JSON.stringify(pins))
  assert.equal(pins.bottom,'0px','Anchored bottom edge must not fade')
  assert.equal(pins.top,'28px','Distant top edge fades with the OpenUI 28px feather')
  assert.equal(await jump.count(),0,'Pinned streams must not show the jump control')
  const screenful=Math.ceil(pins.viewport/pins.line)
  const follows = async panel => {
    for(const batch of [1,screenful]){
      const before=await pinState()
      await appendLines(batch)
      pins=await reachBottom()
      assert.ok(pins.atBottom,`${panel} follows a ${batch}-line batch down to the bottom edge: `+JSON.stringify(pins))
      assert.ok(pins.scrollTop>before.scrollTop,`${panel} moves with a ${batch}-line batch: `+JSON.stringify({before,pins}))
      assert.equal(pins.bottom,'0px',`${panel} keeps the bottom edge unfaded`)
    }
  }
  await follows('A panel resting on the bottom edge')

  await output.scrollIntoViewIfNeeded()
  await output.hover()
  const pushed=await pinState(),pushedAt=Date.now()
  await page.mouse.wheel(0,pushed.line)
  await appendLines(1)
  pins=await reachBottom()
  const hold=Date.now()-pushedAt
  assert.ok(pins.atBottom&&pins.scrollTop>pushed.scrollTop,'Wheeling against the bottom edge keeps the panel following: '+JSON.stringify({pushed,pins}))
  assert.equal(await jump.count(),0,'Wheeling against the bottom edge must not offer the jump control')

  const detached=await wheelBy(-pins.line)
  assert.ok(detached.scrollTop<pins.scrollTop&&!detached.atBottom&&detached.distance<detached.line*3/2,'A one-line wheel-up leaves the bottom edge by one line: '+JSON.stringify(detached))
  await page.waitForTimeout(hold)
  for(let frame=1;frame<=screenful;frame++){
    await appendLines(1)
    pins=await pinState()
    assert.equal(pins.scrollTop,detached.scrollTop,`Frame ${frame} of ${screenful} must not pull a reader one line above the bottom edge back down: `+JSON.stringify(pins))
  }
  await jump.waitFor()
  await page.waitForTimeout(hold)
  pins=await pinState()
  assert.equal(pins.scrollTop,detached.scrollTop,'Appended frames must not scroll the panel while the user is reading: '+JSON.stringify(pins))
  assert.equal(pins.bottom,'28px','A detached bottom edge fades with the OpenUI 28px feather')
  assert.ok(await jump.isVisible(),'The jump control appears once far enough from the bottom')

  const short=await wheelBy(pins.distance-2*pins.line)
  assert.ok(short.scrollTop>pins.scrollTop&&short.distance>short.line,'Wheeling down stops more than one line short of the bottom edge: '+JSON.stringify(short))
  await appendLines(1)
  await page.waitForTimeout(hold)
  pins=await pinState()
  assert.equal(pins.scrollTop,short.scrollTop,'Stopping more than one line short of the bottom edge must not resume following: '+JSON.stringify(pins))
  const near=await wheelBy(pins.distance-pins.line*3/4)
  await page.waitForTimeout(hold)
  pins=await pinState()
  assert.ok(near.scrollTop>short.scrollTop&&!near.atBottom&&near.distance<near.line,'Wheeling down stops within one line of the bottom edge without touching it: '+JSON.stringify(near))
  assert.equal(pins.scrollTop,near.scrollTop,'A panel within one line of the bottom edge waits for new frames instead of moving on its own: '+JSON.stringify(pins))
  await follows('A panel wheeled back to within one line of the bottom edge')
  assert.equal(await jump.count(),0,'A panel that returned to the bottom edge must not show the jump control')

  const away=await wheelBy(-pins.viewport)
  assert.ok(away.distance>away.line,'A screenful wheel-up detaches from the bottom edge: '+JSON.stringify(away))
  await jump.waitFor()
  await appendLines(screenful)
  await page.waitForTimeout(hold)
  pins=await pinState()
  assert.equal(pins.scrollTop,away.scrollTop,'A burst must not scroll the panel while the user is reading a screenful above: '+JSON.stringify(pins))
  assert.ok(await jump.isVisible(),'The jump control stays available while detached')
  const landed=await wheelBy(pins.distance+pins.viewport)
  assert.ok(landed.atBottom,'Wheeling past the end lands on the bottom edge: '+JSON.stringify(landed))
  await follows('A panel wheeled back onto the bottom edge')

  await wheelBy(-pins.viewport)
  await jump.click()
  const jumped=await reachBottom()
  assert.ok(jumped.atBottom&&jumped.bottom==='0px','The jump control lands on the unfaded bottom edge: '+JSON.stringify(jumped))
  await page.waitForFunction(()=>!document.querySelector('[data-testid=stream-log-follow]'))
  await appendLines(screenful)
  pins=await reachBottom()
  assert.ok(pins.atBottom,'Auto-follow resumes after an explicit jump: '+JSON.stringify(pins))
  assert.ok(pins.scrollTop>jumped.scrollTop,'A panel resumed by the jump control moves with new frames: '+JSON.stringify({jumped,pins}))
  assert.equal(pins.bottom,'0px')

  await page.goto(base+'/#/nodes')
  await page.locator(`.vps-card[data-node-id="${node}"]`).waitFor()
  await page.getByTestId('overview-host-info').waitFor()
  const toggle=page.locator('.navigation-toggle.touch-target')
  await toggle.waitFor()
  const navigationHit=await toggle.evaluate(element=>{const s=getComputedStyle(element,'::after');return {hit:s.height,visual:element.getBoundingClientRect().height,pointer:s.pointerEvents}})
  assert.equal(navigationHit.hit,'44px','Navigation toggle gains a 44px hit box')
  assert.ok(navigationHit.visual<44,'Visible toggle stays compact: '+navigationHit.visual)
  assert.equal(navigationHit.pointer,'auto')
  const iconTarget=page.locator('.overview-context .header-actions button.touch-target[title="Display settings"]')
  await iconTarget.waitFor()
  const iconHit=await iconTarget.evaluate(element=>{const s=getComputedStyle(element);const after=getComputedStyle(element,'::after');const box=element.getBoundingClientRect();return {hitW:after.width,hitH:after.height,visualW:Math.round(box.width),visualH:Math.round(box.height),inline:s.getPropertyValue('--touch-target-inline')}})
  assert.equal(iconHit.hitH,'44px','Header icon gains a 44px touch target')
  assert.ok(iconHit.visualH<44&&iconHit.visualW<44,'Visible icon button stays compact: '+JSON.stringify(iconHit))
  assert.ok(parseFloat(iconHit.hitW)<=34,'Side-by-side icons only grow vertically: '+JSON.stringify(iconHit))
  const point=await iconTarget.evaluate(element=>{const r=element.getBoundingClientRect();return {x:r.x+r.width/2,y:r.top-4}})
  await page.mouse.click(point.x,point.y)
  await page.locator('dialog.context-settings[open]').waitFor()
  await page.keyboard.press('Escape')

  await page.emulateMedia({reducedMotion:'reduce'})
  const reducedTransition=await iconTarget.evaluate(element=>getComputedStyle(element).transitionDuration)
  assert.ok(['0s','0.00001s'].includes(reducedTransition),'Reduced motion must flatten the press spring: '+reducedTransition)
  await openStreams()
  await output.scrollIntoViewIfNeeded()
  await output.hover()
  pins=await reachBottom()
  assert.ok(pins.atBottom,'A reopened stream rests on the bottom edge: '+JSON.stringify(pins))
  const reducedAway=await wheelBy(-pins.viewport)
  assert.ok(reducedAway.distance>reducedAway.line,'Reduced-motion wheel-up detaches from the bottom edge: '+JSON.stringify(reducedAway))
  await jump.waitFor()
  const landing=await output.evaluateHandle(element=>({distance:new Promise(resolve=>document.addEventListener('click',()=>resolve(element.scrollHeight-element.scrollTop-element.clientHeight),{once:true}))}))
  await jump.click()
  const lingering=await landing.evaluate(result=>result.distance)
  assert.ok(lingering<reducedAway.line/2,'Reduced-motion jump must land on the bottom edge within its own click: '+lingering)
  const reducedJumped=await reachBottom()
  await appendLines(screenful)
  pins=await reachBottom()
  assert.ok(pins.atBottom,'Reduced-motion streams still auto-follow: '+JSON.stringify(pins))
  assert.ok(pins.scrollTop>reducedJumped.scrollTop,'Reduced-motion streams move with new frames: '+JSON.stringify({reducedJumped,pins}))

  await page.emulateMedia({reducedMotion:'no-preference'})
  for(const [theme,width] of [['light',1440],['dark',390]]){
    await page.setViewportSize({width,height:1000});await page.waitForTimeout(120)
    await closeNavigation(page)
    await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
    await page.waitForTimeout(250)
    assert.equal(await page.locator('.navigation-scrim').count(),0,'A captured layout must not be dimmed by the mobile navigation scrim')
    assert.equal(await page.locator('dialog[open]').count(),0,'A captured layout must not be dimmed by an open dialog')
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Stream layout fits the viewport')

  }
  await page.setViewportSize({width:1440,height:1000})
  await page.unroute('**/api/**',handler)
  page.off('pageerror',collectError);assert.deepEqual(errors,[],'Stream panel must not produce browser errors')
  console.log("stream-panel-regressions passed.")
}
