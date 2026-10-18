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
 let source;await page.routeWebSocket('**'+routePath('dashboard.watch'),ws=>{source=ws})
 await page.goto(base+'/#/nodes?node='+node+'&section=overview')
 const network=page.getByTestId('network-card');await network.waitFor()
 assert.equal(await page.locator('.metrics-grid > .metric-card:not(.hardware-card) > .card-heading > h3').count(),8,'Every square card retains its top title')
 assert.equal(await page.locator('.metrics-panel .resource-gauge').count(),5,'CPU, memory, disk, swap and load reuse the VPS reading')
 assert.equal(await page.locator('.metrics-panel .gauge-needle').count(),0,'No extra pointer or alternate gauge design')
 assert.equal(await page.locator('.metrics-panel .telemetry-plot').count(),2,'Network and processes use sampled charts; billing remains unchanged')
 for(let attempt=0;!source&&attempt<50;attempt++)await page.waitForTimeout(20)
 assert.ok(source,'Live metric transport is connected')
 const beginning=Date.now();let seq=1
 async function publish(change={}){
  const sample=view();sample.sequence=++seq;sample.collectedAt=new Date(beginning+seq*1000).toISOString();sample.metrics.cpu.usagePercent.value=seq%100
  Object.assign(sample,change);sample.metrics.cpu.usagePercent.value=seq%100;source.send(JSON.stringify({type:'node_metrics',nodeId:node,metrics:sample}))
  await page.waitForFunction(text=>document.querySelector('[data-testid=cpu-value]')?.textContent===text,sample.metrics.cpu.usagePercent.status==='known'?(seq%100).toFixed(1)+'%':'—')
 }
 await publish();await publish()
 assert.ok((await network.locator('.chart-line').first().getAttribute('d')).includes('L'),'Live readings form a real sampled curve')
 const gap=view();gap.metrics.network.summary.status='unknown';await publish({metrics:gap.metrics});await publish()
 assert.ok(((await network.locator('.chart-line').first().getAttribute('d')).match(/M/g)||[]).length>=2,'Unknown samples leave gaps instead of invented zeros')
 for(let sample=0;sample<62;sample++)await publish()
 assert.equal(await network.locator('.rate-chart').getAttribute('data-points'),'60','Chart memory is bounded to sixty real samples')
 const reboot=view();reboot.metrics.uptime.value.bootId='reboot-fixture';await publish({metrics:reboot.metrics})
 assert.equal(await network.locator('.rate-chart').getAttribute('data-points'),'1','A reboot starts a new series')
 for(let point=0;point<4;point++){const live=view();live.metrics.uptime.value.bootId='reboot-fixture';await publish({metrics:live.metrics})}
 assert.equal(await network.getByTestId('network-interface').count(),0,'Interface rows are omitted from the summary card')
 assert.equal(await page.locator('.metrics-panel .resource-reading > span').count(),0,'Card headings replace repeated labels beneath gauges')
 assert.ok(!(await page.getByTestId('disk-card').innerText()).includes('/dev/vda1'),'Disk device metadata is omitted')
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
  assert.ok(await page.locator('.metrics-panel .resource-reading').evaluateAll(readings=>readings.every(reading=>{const gauge=reading.querySelector('.resource-gauge'),label=reading.querySelector(':scope > span'),detail=reading.querySelector(':scope > small'),r=gauge.getBoundingClientRect();return Math.abs(r.width-76)<1&&Math.abs(r.height-76)<1&&getComputedStyle(gauge.querySelector('strong')).fontSize==='16px'&&label===null&&detail.getBoundingClientRect().top>=r.bottom})), 'Readings retain the VPS gauge size and type with details directly below it')
  const trafficText=await page.locator('.host-metrics > .metric-card').nth(3).innerText()
  assert.ok(!/UTC|Partial coverage|覆盖不完整|partial_coverage/.test(trafficText),'Billing traffic omits redundant timezone and coverage labels')
  assert.equal(await network.locator('.interface-rates').count(),0,'Per-interface rates do not repeat the summary')
  assert.equal(await network.locator('.monitor-values strong').count(),2,'Receive and send summary values remain visible')
  assert.equal(await page.locator('.metrics-panel .status-pill, .metrics-panel .node-state, .detail-heading-status').count(),0,'Metrics have no repeated status badges')
  await page.screenshot({path:path.join(artifacts,`metrics-${theme}-${width}.png`),fullPage:true})
  await network.screenshot({path:path.join(artifacts,`network-${theme}-${width}.png`)})
 }
 assert.equal(await page.locator('.metrics-panel [data-resource=load] .ring strong').innerText(),'1.7%','Load reuses the VPS percentage formula')
 const unavailable=view();unavailable.metrics.cpu.usagePercent.status='error';unavailable.metrics.memory.status='unknown';unavailable.metrics.disk.mounts[0].usage.status='unknown';unavailable.metrics.host.swap.status='unknown';unavailable.metrics.host.load.status='unknown';await publish({metrics:unavailable.metrics})
 assert.equal(await page.locator('.metrics-panel .resource-gauge .ring-value').count(),0,'Unavailable metrics never show a healthy zero gauge')
 assert.equal(await page.getByTestId('cpu-value').innerText(),'—')
 await require('./observability-regressions.cjs')({page,base,node,artifacts,catalog,view,publish});
 console.log('Metrics regressions passed: primary interfaces, bounded rate precision, summary rates without duplicate interface rates; 8 light/dark/mobile layouts; eight equal square cards in four/two columns.')
}
