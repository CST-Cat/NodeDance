const {closeNavigation}=require('./sidebar-navigation.cjs')
// Synthetic samples against the isolated Core contract; no real Agent or host.
const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,base,node,artifacts,catalog}){
 const routePath=name=>{const operation=catalog.operations.find(o=>o.name===name);assert.ok(operation);return operation.path.replace('{nodeId}',node)}
 const now=()=>new Date().toISOString(),metric=value=>({status:'known',value,sampledAt:now(),sampleAgeMillis:0})
 const state=()=>({status:'online',generation:1,serverTime:now(),leaseValidUntil:new Date(Date.now()+3600000).toISOString()})
 const view=()=>({agentId:'metrics-fixture',nodeId:node,generation:1,sequence:1,collectedAt:now(),receivedAt:now(),clockOffsetMs:0,nodeStatus:'online',activeGeneration:1,...state(),metrics:{
  system:Object.fromEntries(['hostname','os','architecture','platform','platformFamily','platformVersion','kernelVersion'].map(key=>[key,metric('fixture')])),cpu:{usagePercent:metric(12.5),logicalCores:metric(4)},memory:metric({totalBytes:8e9,availableBytes:6e9,usedBytes:2e9,usedPercent:25}),uptime:metric({seconds:3600,bootId:'fixture'}),
  network:{summary:metric({receivedBytesPerSecond:504.5159015380504,sentBytesPerSecond:331.9446676786221}),interfaces:['eth0','ens3','bond0','lo','docker0','br-fixture','tailscale0','vethfixture'].map((name,index)=>({name,includedInSummary:index<3,summaryReason:index<3?'':'virtual_interface',rate:metric({receivedBytesPerSecond:index===0?504.5159015380504:5500.123456789,sentBytesPerSecond:331.9446676786221})}))},
  host:{diskIO:metric({readBytesPerSecond:8192,writeBytesPerSecond:4096}),swap:metric({usedPercent:99.9,totalBytes:1e9,usedBytes:999e6}),load:metric({one:0.1,five:0.2,fifteen:0.3}),processes:metric(193),sockets:metric({tcp:116,udp:10}),traffic:{...metric({periodReceivedBytes:22.8e9,periodSentBytes:20.6e9,receivedBytes:22.8e9,sentBytes:20.6e9,periodStart:'2026-10-01T00:00:00Z'}),reason:'partial_coverage'},cpu:metric({model:'VPS CPU',physicalCores:2}),virtualization:metric('KVM'),memory:metric({}),memoryMode:'available',addresses:metric([{interface:'eth0',address:'192.168.1.2',family:4},{interface:'eth0',address:'192.0.2.1',family:4},{interface:'docker0',address:'172.17.0.1',family:4},{interface:'br-fixture',address:'172.18.0.1',family:4},{interface:'tailscale0',address:'100.64.0.1',family:4},{interface:'tailscale0',address:'fd7a:115c:a1e0::1',family:6}]),publicIpv4:metric('192.0.2.1'),publicIpv6:metric('2001:db8::1')},
  disk:{status:'known',sampledAt:now(),sampleAgeMillis:0,mounts:[{mountpoint:'/',device:'/dev/vda1',filesystem:'ext4',usage:metric({totalBytes:100e9,usedBytes:40e9,availableBytes:60e9,usedPercent:40})}]}
 }})
 await page.route('**'+routePath('nodes.list'),route=>route.fulfill({json:{nodes:[{nodeId:node,agentId:'metrics-fixture',displayName:'Metrics fixture',status:'online',generation:1,leaseValidUntil:state().leaseValidUntil,capabilities:[]}],serverTime:now()}}))
 await page.route('**'+routePath('nodes.metrics'),route=>route.fulfill({json:view()}))
 let summaryFixture=false;const historyRanges=[];
 await page.route('**'+routePath('nodes.history')+'?*',route=>{
  const data=require('./recent-history-fixture.cjs')(route.request().url());historyRanges.push(Date.parse(data.to)-Date.parse(data.from));
  if(summaryFixture){
   const end=Math.floor(Date.parse(data.to)/60000)*60000;
   data.series=data.series.map(series=>({...series,points:series.key==='network.received_bytes_per_second'?[{bucketAt:new Date(end-120000).toISOString(),value:1024,samples:12},{bucketAt:new Date(end-60000).toISOString(),value:2048,samples:12}]:series.key==='disk.read_bytes_per_second'?[{bucketAt:new Date(end-60000).toISOString(),value:0,samples:12}]:[]}));
  }
  return route.fulfill({json:data});
 });
 let source;await page.routeWebSocket('**'+routePath('dashboard.watch'),ws=>{source=ws})
 let quota={trafficLimitBytes:0,trafficAccounting:'sum',trafficWarningPercent:80};
 await page.route('**'+routePath('nodes.asset'),r=>r.fulfill({json:quota}));
 await page.goto(base+'/#/nodes?node='+node+'&section=overview')
 const network=page.getByTestId('network-card');await network.waitFor()
 assert.equal(await page.locator('.metrics-eyebrow,.detail-heading-navigation').count(),0,'Redundant detail/monitoring eyebrow labels are removed');
 assert.ok(!/agent-v/.test(await page.locator('.detail-heading').innerText()),'Agent version is omitted from the page heading');
 assert.equal(await page.locator('.metrics-grid > .metric-card:not(.hardware-card) > .card-heading > h3').count(),3,'Network, disk I/O and process cards retain their top titles')
 assert.equal(await page.locator('.metrics-panel .resource-gauge').count(),5,'CPU, memory, disk, swap and load reuse the VPS reading')
 assert.equal(await page.locator('.metrics-panel .gauge-needle').count(),0,'No extra pointer or alternate gauge design')
 assert.equal(await page.locator('[data-hardware=ipv4] dd').innerText(),'192.0.2.1','Public IPv4 is retained');
 assert.equal(await page.locator('[data-hardware=ipv6] dd').innerText(),'2001:db8::1','Public IPv6 is retained');
 assert.equal(await page.locator('[data-hardware="interface:eth0"] dd').innerText(),'192.168.1.2','Public IP is not repeated in local addresses');
 assert.equal(await page.locator('[data-hardware="interface:docker0"],[data-hardware="interface:br-fixture"]').count(),0,'Container bridge noise is omitted');
 assert.equal(await page.locator('[data-hardware="interface:tailscale0"]').count(),1,'Both Tailscale addresses share one row');

 assert.equal(await page.locator('.metrics-panel .telemetry-plot').count(),3,'Network, disk I/O and processes use the shared chart')
 for(let attempt=0;!source&&attempt<50;attempt++)await page.waitForTimeout(20)
 assert.ok(source,'Live metric transport is connected')
 const beginning=Date.now();let seq=1
 async function publish(change={}){
  const sample=view();sample.sequence=++seq;sample.collectedAt=new Date(beginning+seq*1000).toISOString();sample.metrics.cpu.usagePercent.value=seq%100
  Object.assign(sample,change);sample.metrics.cpu.usagePercent.value=seq%100;source.send(JSON.stringify({type:'node_metrics',nodeId:node,metrics:sample}))
  await page.waitForFunction(text=>document.querySelector('[data-testid=cpu-value]')?.textContent===text,sample.metrics.cpu.usagePercent.status==='known'?(seq%100).toFixed(1)+'%':'—')
 }
 await page.waitForFunction(()=>document.querySelector('[data-testid=network-card] .chart-line')?.getAttribute('d')?.includes('L'));
 const savedPath=await network.locator('.chart-line').first().getAttribute('d');
 const io=page.getByTestId('io-card');
 await page.waitForFunction(()=>document.querySelector('[data-testid=io-card] .chart-line')?.getAttribute('d')?.includes('L'));
 assert.ok((await io.locator('.monitor-values').innerText()).includes('8.0 KiB/s'),'Disk read speed is live');
 assert.equal(await io.locator('.history-range').innerText(),await network.locator('.history-range').innerText(),'Network and I/O share exactly the same time window');
 assert.equal(await io.locator('[data-history-kind=io]').count(),1,'The I/O card reuses the shared history component');

 await publish();await publish()
 assert.ok((await network.locator('.chart-line').first().getAttribute('d')).includes('L'),'Persisted data forms a curve before browser-session samples')
 const gap=view();gap.metrics.network.summary.status='unknown';await publish({metrics:gap.metrics});await publish()
 assert.ok(((await network.locator('.chart-line').first().getAttribute('d')).match(/M/g)||[]).length>=2,'Missing persisted five-second samples leave gaps instead of invented zeros')
 for(let sample=0;sample<62;sample++)await publish()
 assert.equal(await page.locator('.metrics-grid .rate-chart').nth(2).getAttribute('data-points'),'60','The unrelated process chart retains its existing bound');
 assert.ok(Number(await network.locator('.rate-chart').getAttribute('data-points'))>=2880,'Network retains the complete four-hour window')
 const reboot=view();reboot.metrics.uptime.value.bootId='reboot-fixture';await publish({metrics:reboot.metrics})
 assert.equal(await page.locator('.metrics-grid .rate-chart').nth(2).getAttribute('data-points'),'1','A reboot starts a new process series');
 assert.ok((await network.locator('.chart-line').first().getAttribute('d')).includes('L'),'An Agent reboot does not erase persisted network history')
 for(let point=0;point<4;point++){const live=view();live.metrics.uptime.value.bootId='reboot-fixture';await publish({metrics:live.metrics})}
 assert.equal(await network.getByTestId('network-interface').count(),0,'Interface rows are omitted from the summary card')
 assert.equal(await page.locator('.metrics-panel .resource-reading > span').count(),0,'Card headings replace repeated labels beneath gauges')
 assert.ok(!(await page.getByTestId('disk-card').innerText()).includes('/dev/vda1'),'Disk device metadata is omitted')
 assert.ok((await network.innerText()).includes('505 B/s'));assert.ok((await network.innerText()).includes('332 B/s'))
 assert.ok(!/\d+\.\d{2,}/.test(await network.innerText()),'No excessive rate precision')
 for(const theme of ['light','dark'])for(const width of [320,390,768,1440]){
  await page.setViewportSize({width,height:1000})
  await closeNavigation(page)
  await page.locator('#workspace-navigation').evaluate(async element=>await Promise.all(element.getAnimations({subtree:true}).map(animation=>animation.finished.catch(()=>{}))))
  await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
  const geometry=await page.locator('.metrics-panel').evaluate(panel=>{
   const cards=[...panel.querySelectorAll('.metric-card:not(.hardware-card):not(.resource-summary)')].map(card=>{const r=card.getBoundingClientRect();return {width:r.width,height:r.height,top:r.top,left:r.left,overflowX:card.scrollWidth>card.clientWidth+1}})
   return {containerWidth:panel.getBoundingClientRect().width,cards}
  })
  assert.equal(geometry.cards.length,3,'The three chart cards remain visible')
  assert.ok(geometry.cards.every(card=>Math.abs(card.width-card.height)<1&&Math.abs(card.width-geometry.cards[0].width)<1&&!card.overflowX),'Cards are equal squares without horizontal overflow: '+JSON.stringify(geometry))
  assert.equal(geometry.cards.filter(card=>Math.abs(card.top-geometry.cards[0].top)<1).length,geometry.containerWidth<=760?2:3,'Network and disk I/O lead the responsive chart grid')
  assert.ok(await page.locator('.metrics-panel .resource-reading').evaluateAll(readings=>readings.every(reading=>{const gauge=reading.querySelector('.resource-gauge'),label=reading.querySelector(':scope > span'),detail=reading.querySelector(':scope > small'),r=gauge.getBoundingClientRect();return gauge.querySelector('svg .ring-track')&&!gauge.classList.contains('ring')&&getComputedStyle(gauge).boxShadow==='none'&&Math.abs(r.width-Math.min(96,reading.clientWidth))<1&&Math.abs(r.height-r.width)<1&&getComputedStyle(gauge.querySelector('strong')).fontSize==='20px'&&label===null&&detail.getBoundingClientRect().top>=r.bottom})), 'Readings use enlarged equal-size rings with details directly below')
  const columns=await page.locator('.hardware-details').evaluate(e=>getComputedStyle(e).gridTemplateColumns.split(' ').length);
  assert.equal(columns,geometry.containerWidth<=760?1:2,'Hardware uses compact two/one columns');
  const netRect=await network.boundingBox(),ioRect=await io.boundingBox();
  assert.ok(ioRect.x>netRect.x&&Math.abs(ioRect.y-netRect.y)<1,'Disk I/O is directly right of Network at every width');
  const summary=page.getByTestId('resource-summary');
  assert.deepEqual(await summary.locator('[data-summary-item]').evaluateAll(items=>items.map(e=>e.dataset.summaryItem)),['load','cpu','memory','disk','swap','traffic']);
  const positions=await summary.locator('[data-summary-item]').evaluateAll(items=>items.map(e=>{const r=e.getBoundingClientRect();return {x:r.x,y:r.y}}));
  assert.equal(new Set(positions.map(p=>Math.round(p.x))).size,geometry.containerWidth<=760?3:6,'Combined resource rectangle has six/three columns');
  assert.equal(await summary.locator('.metric-card').count(),0,'The combined rectangle has no nested card borders');
  const trafficText=await page.locator('[data-summary-item=traffic]').innerText()
  assert.ok(!/UTC|Partial coverage|覆盖不完整|partial_coverage|Period start|账期起点|Observed lifetime|累计观测流量|↓|↑/.test(trafficText),'Billing traffic only retains the ring and used/quota reading')
  assert.equal(await network.locator('.interface-rates').count(),0,'Per-interface rates do not repeat the summary')
  assert.equal(await network.locator('.monitor-values strong').count(),2,'Receive and send summary values remain visible')
  assert.equal(await page.locator('.metrics-panel .status-pill, .metrics-panel .node-state, .detail-heading-status').count(),0,'Metrics have no repeated status badges')
  await page.screenshot({path:path.join(artifacts,`metrics-${theme}-${width}.png`),fullPage:true})
  await network.screenshot({path:path.join(artifacts,`network-${theme}-${width}.png`)})
 }
 await publish({metrics:view().metrics});
 assert.equal(await page.locator('.metrics-panel [data-resource=load] .resource-gauge strong').innerText(),'1.7%','Load reuses the VPS percentage formula')
 const unavailable=view();unavailable.metrics.cpu.usagePercent.status='error';unavailable.metrics.memory.status='unknown';unavailable.metrics.disk.mounts[0].usage.status='unknown';unavailable.metrics.host.swap.status='unknown';unavailable.metrics.host.load.status='unknown';unavailable.metrics.host.publicIpv4=metric(null);unavailable.metrics.host.publicIpv6=metric(null);await publish({metrics:unavailable.metrics})
 assert.equal(await page.locator('.metrics-panel .ring-value').count(),0,'Unavailable metrics have no filled ring')
 assert.equal(await page.getByTestId('cpu-value').innerText(),'—')
 assert.equal(await page.locator('[data-hardware=ipv4],[data-hardware=ipv6]').count(),0,'Only empty public address rows are omitted');
 for(const mode of ['sum','up','down','max','min']){
  quota={trafficLimitBytes:100e9,trafficAccounting:mode,trafficWarningPercent:80};
  await page.reload();const ring=page.locator('[data-resource="traffic"] .resource-gauge strong');await ring.waitFor();
  const used={sum:43.4,up:20.6,down:22.8,max:22.8,min:20.6}[mode];
  assert.equal(await ring.innerText(),used.toFixed(1)+'%','Traffic ring uses the configured accounting mode');
 }
 quota={trafficLimitBytes:10e9,trafficAccounting:'sum',trafficWarningPercent:80};await page.reload();
 await page.waitForFunction(()=>document.querySelector('[data-resource="traffic"] strong')?.textContent==='434.0%');
 assert.equal(await page.locator('[data-resource="traffic"] .ring-value.is-high').count(),1,'Over-quota traffic retains the full percentage and warning color');
 for(const theme of ['light','dark'])for(const width of [320,390,1440]){
  await page.setViewportSize({width,height:1000});
  await page.locator('.app-shell').evaluate((e,theme)=>{e.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);
  const summary=page.getByTestId('resource-summary');
  assert.equal(await summary.locator('.resource-gauge').count(),6,'All six quota-enabled readings use rings');
  assert.ok(await summary.evaluate(e=>e.scrollWidth<=e.clientWidth+1),'The six-reading rectangle does not overflow');
  await summary.screenshot({path:path.join(artifacts,`summary-quota-${theme}-${width}.png`)});
 }
 const staleTraffic=view();staleTraffic.metrics.host.traffic.status='unknown';await publish({metrics:staleTraffic.metrics});
 assert.equal(await page.locator('[data-resource="traffic"] strong').innerText(),'—','Unknown traffic is not fabricated as zero');
 quota={trafficLimitBytes:0,trafficAccounting:'sum',trafficWarningPercent:80};await page.reload();await network.waitFor();
 assert.equal(await page.locator('[data-resource="traffic"]').count(),0,'Unlimited traffic has no invented utilization ring');
 await page.waitForTimeout(5300);
 assert.ok(historyRanges.some(range=>range<20000),'Polling only requests incremental five-second history');
 assert.ok(Number(await network.locator('.rate-chart').getAttribute('data-points'))>=2880,'Incremental refresh retains the four-hour window');
 summaryFixture=true;await page.reload();await network.waitFor();
 await page.waitForFunction(()=>document.querySelector('[data-testid=network-card] .history-totals strong')?.textContent==='15.0 KiB');
 assert.deepEqual(await network.locator('.history-totals strong').allInnerTexts(),['15.0 KiB','—'],'Known five-second volumes add up, missing upload history is not zero');
 assert.deepEqual(await io.locator('.history-totals strong').allInnerTexts(),['0 B','—'],'Measured zero reads remain zero while missing writes remain unknown');
 summaryFixture=false;await page.reload();await network.waitFor();
 await require('./observability-regressions.cjs')({page,base,node,artifacts,catalog,view,publish});
 console.log('Metrics regressions passed: primary interfaces, bounded rate precision, summary rates without duplicate interface rates; 8 light/dark/mobile layouts; combined six/three-column resource rectangle and three square chart cards, including a shared I/O history card.')
}
