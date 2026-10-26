const {closeNavigation}=require('./sidebar-navigation.cjs')
// Stream panel checks use a real isolated Core contract plus a routed
// container-log WebSocket. No Docker daemon, Agent or host effect is used.
// Covers: OpenUI-ported scroll-edge fading, streaming auto-follow that yields
// to user scroll, the jump-to-latest pill, reduced-motion flattening, spring
// press feedback, and 44px touch hit targets on icon controls.
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
  // The stream socket carries nodeId/containerId in its query string; the
  // trailing wildcard must remain part of the routing pattern.
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
  const pinState = () => output.evaluate(element => ({
    scrollTop: element.scrollTop,
    distance: element.scrollHeight - element.scrollTop - element.clientHeight,
    top: element.style.getPropertyValue('--scroll-fade-top'),
    bottom: element.style.getPropertyValue('--scroll-fade-bottom'),
  }))
  const lineCount = () => output.evaluate(element=>element.textContent.split('\n').length)
  async function openStreams(){
    await page.goto(base+`/#/nodes?node=${node}&section=docker`)
    // The dashboard watch socket survives same-document navigations; only the
    // per-stream fixture state resets between sessions.
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
  // Register the fixture stream before the app opens its socket so the very
  // first connection is intercepted.
  await page.routeWebSocket(streamPattern,ws=>{logSocket=ws;streamSequence=0
    logSocket.send(JSON.stringify({version:1,type:'container_stream_ready',generation:1,sequence:++streamSequence,requestId:'fixture',payload:{kind:'logs',containerId:container.id}}))
    emitBurst(80)})
  // Pinned streaming: frames emitted after connect must stay anchored to the
  // bottom edge, and the bottom fade must be disabled while anchored.
  await openStreams()
  await page.waitForFunction(()=>{const el=document.querySelector('[data-testid=stream-log-output]');return el?.style.getPropertyValue('--scroll-fade-top')==='28px'&&el?.style.getPropertyValue('--scroll-fade-bottom')==='0px'})
  let pins=await pinState()
  assert.ok(pins.distance<=40,'Fresh frames must keep the panel pinned to the bottom edge: '+JSON.stringify(pins))
  assert.equal(pins.bottom,'0px','Anchored bottom edge must not fade')
  assert.equal(pins.top,'28px','Distant top edge fades with the OpenUI 28px feather')
  assert.equal(await page.getByTestId('stream-log-follow').count(),0,'Pinned streams must not show the jump control')

  // User intent yields immediately: one wheel-up stops all auto-following,
  // even as more frames arrive, and the jump control appears.
  await output.scrollIntoViewIfNeeded()
  await output.hover()
  await page.mouse.wheel(0,-500)
  // A pinned panel reports a large scrollTop, so detachment is measured as
  // bottom distance, not an absolute offset.
  await page.waitForFunction(()=>{const el=document.querySelector('[data-testid=stream-log-output]');return el.scrollHeight-el.scrollTop-el.clientHeight>40})
  const detached=await pinState()
  assert.ok(detached.distance>40,'Wheel-up must detach from the bottom edge')
  await emitBurst(30)
  await page.waitForFunction(()=>document.querySelector('[data-testid=stream-log-output]')?.textContent.split('\n').length>=110)
  await page.waitForFunction(()=>document.querySelector('[data-testid=stream-log-output]')?.style.getPropertyValue('--scroll-fade-bottom')==='28px')
  await page.waitForTimeout(120)
  pins=await pinState()
  assert.equal(pins.scrollTop,detached.scrollTop,'Appended frames must not scroll the panel while the user is reading')
  assert.equal(pins.bottom,'28px','A detached bottom edge fades with the OpenUI 28px feather')
  await jump.waitFor()
  assert.ok(await jump.isVisible(),'The jump control appears once far enough from the bottom')

  // Jump re-anchors instantly (OpenUI behavior "auto") and streaming resumes.
  await jump.click()
  await page.waitForFunction(()=>{const el=document.querySelector('[data-testid=stream-log-output]');return el.scrollHeight-el.scrollTop-el.clientHeight<=40})
  await page.waitForFunction(()=>!document.querySelector('[data-testid=stream-log-follow]'))
  await page.waitForFunction(()=>document.querySelector('[data-testid=stream-log-output]')?.style.getPropertyValue('--scroll-fade-bottom')==='0px')
  await emitBurst(20)
  await page.waitForTimeout(120)
  pins=await pinState()
  assert.ok(pins.distance<=40,'Auto-follow resumes after an explicit jump: '+JSON.stringify(pins))
  assert.equal(pins.bottom,'0px')

  // 44px touch hit targets keep the visible icon size (.touch-target).
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
  // Clicking inside the hit box but above the visible icon must trigger it.
  const point=await iconTarget.evaluate(element=>{const r=element.getBoundingClientRect();return {x:r.x+r.width/2,y:r.top-4}})
  await page.mouse.click(point.x,point.y)
  await page.locator('dialog.context-settings[open]').waitFor()
  await page.keyboard.press('Escape')

  // Reduced motion flattens the spring press, the jump-pill entrance, and
  // selects the unsmoothed jump path. The stream fixture handler stays
  // registered; it re-fires on the second session's connection.
  await page.emulateMedia({reducedMotion:'reduce'})
  const reducedTransition=await iconTarget.evaluate(element=>getComputedStyle(element).transitionDuration)
  assert.ok(['0s','0.00001s'].includes(reducedTransition),'Reduced motion must flatten the press spring: '+reducedTransition)
  await openStreams()
  await output.scrollIntoViewIfNeeded()
  await output.hover()
  await page.mouse.wheel(0,-500)
  await jump.waitFor()
  const jumpStarted=Date.now()
  await jump.click()
  await page.waitForFunction(()=>{const el=document.querySelector('[data-testid=stream-log-output]');return el.scrollHeight-el.scrollTop-el.clientHeight<=40})
  assert.ok(Date.now()-jumpStarted<200,'Reduced-motion jump must not linger: '+(Date.now()-jumpStarted)+'ms')
  // Frames continue to follow.
  await emitBurst(15)
  await page.waitForFunction(()=>document.querySelector('[data-testid=stream-log-output]')?.textContent.split('\n').length>=95)
  await page.waitForTimeout(120)
  pins=await pinState()
  assert.ok(pins.distance<=40,'Reduced-motion streams still auto-follow: '+JSON.stringify(pins))

  // Layout coverage for the streaming surface (light/dark × desktop/mobile).
  await page.emulateMedia({reducedMotion:'no-preference'})
  for(const [theme,width] of [['light',1440],['dark',390]]){
    await page.setViewportSize({width,height:1000});await page.waitForTimeout(120)
    await closeNavigation(page)
    await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
    await page.waitForTimeout(250) // let the shared theme transition settle before capturing
    assert.equal(await page.locator('.navigation-scrim').count(),0,'A captured layout must not be dimmed by the mobile navigation scrim')
    assert.equal(await page.locator('dialog[open]').count(),0,'A captured layout must not be dimmed by an open dialog')
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Stream layout fits the viewport')

  }
  await page.setViewportSize({width:1440,height:1000})
  await page.unroute('**/api/**',handler)
  page.off('pageerror',collectError);assert.deepEqual(errors,[],'Stream panel must not produce browser errors')
  console.log("stream-panel-regressions passed.")
}
