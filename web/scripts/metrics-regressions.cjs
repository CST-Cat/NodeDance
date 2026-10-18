// Synthetic samples against the isolated Core contract; no real Agent or host.
const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,base,node,artifacts,catalog}){
 const routePath=name=>{const operation=catalog.operations.find(o=>o.name===name);assert.ok(operation);return operation.path.replace('{nodeId}',node)}
 const now=()=>new Date().toISOString(),metric=value=>({status:'known',value,sampledAt:now(),sampleAgeMillis:0})
 const state=()=>({status:'online',generation:1,serverTime:now(),leaseValidUntil:new Date(Date.now()+3600000).toISOString()})
 const view=()=>({agentId:'metrics-fixture',nodeId:node,generation:1,sequence:1,collectedAt:now(),receivedAt:now(),clockOffsetMs:0,nodeStatus:'online',activeGeneration:1,...state(),metrics:{
  system:Object.fromEntries(['hostname','os','architecture','platform','platformFamily','platformVersion','kernelVersion'].map(key=>[key,metric('fixture')])),cpu:{usagePercent:metric(12.5),logicalCores:metric(4)},memory:metric({totalBytes:8e9,availableBytes:6e9,usedBytes:2e9,usedPercent:25}),uptime:metric({seconds:3600,bootId:'fixture'}),
  network:{summary:metric({receivedBytesPerSecond:504.5159015380504,sentBytesPerSecond:331.9446676786221}),interfaces:['eth0','ens3','bond0','lo','docker0','br-fixture','tailscale0','vethfixture'].map((name,index)=>({name,includedInSummary:index<3,summaryReason:index<3?'':'virtual_interface',rate:metric({receivedBytesPerSecond:index===0?504.5159015380504:5500.123456789,sentBytesPerSecond:331.9446676786221})}))},
  host:{swap:metric({usedPercent:99.9,totalBytes:1e9,usedBytes:999e6}),load:metric({one:0.1,five:0.2,fifteen:0.3}),processes:metric(193),sockets:metric({tcp:116,udp:10}),traffic:{...metric({periodReceivedBytes:22.8e9,periodSentBytes:20.6e9,receivedBytes:22.8e9,sentBytes:20.6e9,periodStart:'2026-10-01T00:00:00Z'}),reason:'partial_coverage'},cpu:metric({model:'VPS CPU',physicalCores:2}),virtualization:metric('KVM'),memory:metric({}),memoryMode:'available',addresses:metric([]),publicIpv4:metric('192.0.2.1'),publicIpv6:metric(null)},
  disk:{status:'known',sampledAt:now(),sampleAgeMillis:0,mounts:[{mountpoint:'/',device:'/dev/vda1',filesystem:'ext4',usage:metric({totalBytes:100e9,usedBytes:40e9,availableBytes:60e9,usedPercent:40})}]}
 }})
 await page.route('**'+routePath('nodes.list'),route=>route.fulfill({json:{nodes:[{nodeId:node,agentId:'metrics-fixture',displayName:'Metrics fixture',status:'online',generation:1,leaseValidUntil:state().leaseValidUntil,capabilities:[]}],serverTime:now()}}))
 await page.route('**'+routePath('nodes.metrics'),route=>route.fulfill({json:view()}))
 await page.routeWebSocket('**'+routePath('dashboard.watch'),()=>{})
 await page.goto(base+'/#/nodes?node='+node+'&section=overview')
 const network=page.getByTestId('network-card');await network.waitFor()
 assert.deepEqual(await network.getByTestId('network-interface').evaluateAll(rows=>rows.map(row=>row.dataset.interfaceName)),['eth0','ens3','bond0'])
 assert.ok((await network.innerText()).includes('505 B/s'));assert.ok((await network.innerText()).includes('332 B/s'))
 assert.ok(!/\d+\.\d{2,}/.test(await network.innerText()),'No excessive rate precision')
 for(const theme of ['light','dark'])for(const width of [320,390,768,1440]){
  await page.setViewportSize({width,height:1000})
  const scrim=page.locator('.navigation-scrim');if(await scrim.isVisible())await scrim.click({position:{x:width-10,y:20}})
  await page.locator('#workspace-navigation').evaluate(async element=>await Promise.all(element.getAnimations({subtree:true}).map(animation=>animation.finished.catch(()=>{}))))
  await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
  const geometry=await page.locator('.metrics-panel').evaluate(panel=>{
   const cards=[...panel.querySelectorAll('.metric-card:not(.hardware-card)')].map(card=>{const r=card.getBoundingClientRect();return {width:r.width,height:r.height,top:r.top,left:r.left,overflowX:card.scrollWidth>card.clientWidth+1}})
   return {containerWidth:panel.getBoundingClientRect().width,cards}
  })
  assert.equal(geometry.cards.length,8,'All eight metric cards remain visible')
  assert.ok(geometry.cards.every(card=>Math.abs(card.width-card.height)<1&&Math.abs(card.width-geometry.cards[0].width)<1&&!card.overflowX),'Cards are equal squares without horizontal overflow: '+JSON.stringify(geometry))
  assert.equal(geometry.cards.filter(card=>Math.abs(card.top-geometry.cards[0].top)<1).length,geometry.containerWidth<=760?2:4,'Four columns on wide containers, two on narrow containers')
  assert.equal(await network.locator('.interface-rates').count(),0,'Per-interface rates do not repeat the summary')
  assert.equal(await network.locator('.network-pair strong').count(),2,'Receive and send summary values remain visible')
  assert.equal(await page.locator('.metrics-panel .status-pill, .metrics-panel .node-state, .detail-heading-status').count(),0,'Metrics have no repeated status badges')
  await page.screenshot({path:path.join(artifacts,`metrics-${theme}-${width}.png`),fullPage:true})
  await network.screenshot({path:path.join(artifacts,`network-${theme}-${width}.png`)})
 }
 console.log('Metrics regressions passed: primary interfaces, bounded rate precision, summary rates without duplicate interface rates; 8 light/dark/mobile layouts; eight equal square cards in four/two columns.')
}
