// Synthetic samples against the isolated Core contract; no real Agent or host.
const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,base,node,artifacts,catalog}){
 module.exports.checkRecentHistory()
 const routePath=name=>{const operation=catalog.operations.find(o=>o.name===name);assert.ok(operation);return operation.path.replace('{nodeId}',node)}
 const now=()=>new Date().toISOString(),metric=value=>({status:'known',value,sampledAt:now(),sampleAgeMillis:0});let wideTraffic=false
 const encode=value=>JSON.stringify(value,(_,entry)=>typeof entry==='bigint'?JSON.rawJSON(entry.toString()):entry)
 const state=()=>({status:'online',generation:1,serverTime:now(),leaseValidUntil:new Date(Date.now()+3600000).toISOString()})
 const view=()=>({agentId:'metrics-fixture',nodeId:node,generation:1,sequence:1,collectedAt:now(),receivedAt:now(),clockOffsetMs:0,nodeStatus:'online',activeGeneration:1,...state(),metrics:{
  system:Object.fromEntries(['hostname','os','architecture','platform','platformFamily','platformVersion','kernelVersion'].map(key=>[key,metric('fixture')])),cpu:{usagePercent:metric(12.5),logicalCores:metric(4)},memory:metric({totalBytes:8e9,availableBytes:6e9,usedBytes:2e9,usedPercent:25}),uptime:metric({seconds:3600,bootId:'fixture'}),
  network:{summary:metric({receivedBytesPerSecond:504.5159015380504,sentBytesPerSecond:331.9446676786221}),interfaces:['eth0','ens3','bond0','lo','docker0','br-fixture','tailscale0','vethfixture'].map((name,index)=>({name,includedInSummary:index<3,summaryReason:index<3?'':'virtual_interface',rate:metric({receivedBytesPerSecond:index===0?504.5159015380504:5500.123456789,sentBytesPerSecond:331.9446676786221})}))},
  host:{diskIO:metric({readBytesPerSecond:8192,writeBytesPerSecond:4096}),swap:metric({usedPercent:99.9,totalBytes:1e9,usedBytes:999e6}),load:metric({one:0.1,five:0.2,fifteen:0.3}),processes:metric(193),sockets:metric({tcp:116,udp:10}),traffic:{...metric({periodReceivedBytes:wideTraffic?9223372036854775807n:22.8e9,periodSentBytes:20.6e9,receivedBytes:wideTraffic?9223372036854775807n:22.8e9,sentBytes:20.6e9,periodStart:'2026-10-01T00:00:00Z'}),reason:'partial_coverage'},cpu:metric({model:'VPS CPU',physicalCores:2}),virtualization:metric('KVM'),memory:metric({}),memoryMode:'available',addresses:metric([{interface:'eth0',address:'192.168.1.2',family:4},{interface:'eth0',address:'192.0.2.1',family:4},{interface:'docker0',address:'172.17.0.1',family:4},{interface:'br-fixture',address:'172.18.0.1',family:4},{interface:'tailscale0',address:'100.64.0.1',family:4},{interface:'tailscale0',address:'fd7a:115c:a1e0::1',family:6}]),publicIpv4:metric('192.0.2.1'),publicIpv6:metric('2001:db8::1')},
  disk:{status:'known',sampledAt:now(),sampleAgeMillis:0,mounts:[{mountpoint:'/',device:'/dev/vda1',filesystem:'ext4',usage:metric({totalBytes:100e9,usedBytes:40e9,availableBytes:60e9,usedPercent:40})}]}
 }})
 await page.route('**'+routePath('nodes.list'),route=>route.fulfill({json:{nodes:[{nodeId:node,agentId:'metrics-fixture',displayName:'Metrics fixture',status:'online',generation:1,leaseValidUntil:state().leaseValidUntil,capabilities:[]}],serverTime:now()}}))
 await page.route('**'+routePath('nodes.metrics'),route=>route.fulfill({contentType:'application/json',body:encode(view())}))
 let summaryFixture=false;const historyRanges=[];
 await page.route('**'+routePath('nodes.history')+'?*',route=>{
  const query=new URL(route.request().url()).searchParams;
  const data=query.get('resolution')==='5s'?require('./recent-history-fixture.cjs')(route.request().url()):{nodeId:node,resolution:query.get('resolution'),from:query.get('from'),to:query.get('to'),series:[{key:'cpu.usage_percent',points:Array.from({length:101},(_,index)=>({bucketAt:new Date(Date.parse(query.get('from'))+index*60000).toISOString(),value:index,minimum:index,maximum:index,samples:1}))}]};historyRanges.push(Date.parse(data.to)-Date.parse(data.from));
  if(summaryFixture){
   const end=Math.floor(Date.parse(data.to)/60000)*60000;
   data.series=data.series.map(series=>({...series,points:series.key==='network.received_bytes_per_second'?[{bucketAt:new Date(end-120000).toISOString(),value:1024,samples:12},{bucketAt:new Date(end-60000).toISOString(),value:2048,samples:12}]:series.key==='disk.read_bytes_per_second'?[{bucketAt:new Date(end-60000).toISOString(),value:0,samples:12}]:[]}));
  }
  return route.fulfill({json:data});
 });
 let source;await page.routeWebSocket('**'+routePath('dashboard.watch'),ws=>{source=ws})
 let quota={trafficLimitBytes:0,trafficAccounting:'sum',trafficWarningPercent:80};
 await page.route('**'+routePath('nodes.asset'),r=>r.fulfill({contentType:'application/json',body:encode(quota)}));
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
 await publish({metrics:view().metrics});
 assert.equal(await page.locator('.metrics-panel [data-resource=load] .resource-gauge strong').innerText(),'1.7%','Load reuses the VPS percentage formula')
 const unavailable=view();unavailable.metrics.cpu.usagePercent.status='error';unavailable.metrics.memory.status='unknown';unavailable.metrics.disk.mounts[0].usage.status='unknown';unavailable.metrics.host.swap.status='unknown';unavailable.metrics.host.load.status='unknown';unavailable.metrics.host.publicIpv4=metric(null);unavailable.metrics.host.publicIpv6=metric(null);await publish({metrics:unavailable.metrics})
 assert.equal(await page.locator('.metrics-panel .ring-value').count(),0,'Unavailable metrics have no filled ring')
 assert.equal(await page.getByTestId('cpu-value').innerText(),'—')
 assert.equal(await page.locator('[data-hardware=ipv4],[data-hardware=ipv6]').count(),0,'Only empty public address rows are omitted');
 wideTraffic=true;quota={trafficLimitBytes:18446744073709551615n,trafficAccounting:'sum',trafficWarningPercent:80};await page.reload();
 await page.waitForFunction(()=>document.querySelector('[data-resource=traffic] .resource-gauge strong')?.textContent==='50.0%');
 assert.ok((await page.locator('[data-resource=traffic]').innerText()).includes('8.0 EiB / 16.0 EiB'),'Full uint64 counters and quota render without a bigint/number error');
 wideTraffic=false;
 for(const mode of ['sum','up','down','max','min']){
  quota={trafficLimitBytes:100e9,trafficAccounting:mode,trafficWarningPercent:80};
  await page.reload();const ring=page.locator('[data-resource="traffic"] .resource-gauge strong');await ring.waitFor();
  const used={sum:43.4,up:20.6,down:22.8,max:22.8,min:20.6}[mode];
  assert.equal(await ring.innerText(),used.toFixed(1)+'%','Traffic ring uses the configured accounting mode');
 }
 quota={trafficLimitBytes:10e9,trafficAccounting:'sum',trafficWarningPercent:80};await page.reload();
 await page.waitForFunction(()=>document.querySelector('[data-resource="traffic"] strong')?.textContent==='434.0%');
 assert.equal(await page.locator('[data-resource="traffic"] .ring-value.is-high').count(),1,'Over-quota traffic retains the full percentage and warning color');
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
 await page.goto(base+'/#/nodes?node='+node+'&section=history');
 const dataPanel=page.getByTestId('history-data');await dataPanel.waitFor();
 assert.equal(await dataPanel.locator('tbody tr').count(),100);
 const csvEvent=page.waitForEvent('download');await dataPanel.getByRole('button',{name:'CSV',exact:true}).click();
 const csvDownload=await csvEvent,csv=await require('node:fs/promises').readFile(await csvDownload.path(),'utf8');
 assert.equal(csv.trim().split('\r\n').length,102,'CSV exports every loaded sample, including other pages');
 const jsonEvent=page.waitForEvent('download');await dataPanel.getByRole('button',{name:'JSON',exact:true}).click();
 const jsonDownload=await jsonEvent,downloaded=JSON.parse(await require('node:fs/promises').readFile(await jsonDownload.path(),'utf8'));
 assert.equal(downloaded.nodeId,node);assert.equal(downloaded.series[0].points.length,101);
 await page.goto(base+'/#/settings');
 const retention=page.getByTestId('monitoring-settings');await retention.waitFor();
 const field=retention.locator('[data-selected-value]');await require('./arc-controls.cjs').selectValue(field,'7');
 const saved=page.waitForResponse(response=>response.url().includes('/api/v1/settings/monitoring')&&response.request().method()==='PUT');
 await retention.getByRole('button',{name:'Save',exact:true}).click();assert.equal((await saved).status(),204);
 assert.equal((await(await page.request.get(base+'/api/v1/settings/monitoring')).json()).retentionDays,7);
 await page.reload();await retention.waitFor();await page.waitForFunction(()=>document.querySelector('[data-testid=monitoring-settings] [data-selected-value]')?.getAttribute('data-selected-value')==='7');
 await page.goto(base+'/#/nodes?node='+node+'&section=overview');await network.waitFor();
 await require('./observability-regressions.cjs')({page,base,node,artifacts,catalog,view,publish});
 console.log("metrics-regressions passed.")
}

module.exports.checkRecentHistory=function(){
 const fs=require('node:fs'),vm=require('node:vm'),root=path.resolve(__dirname,'..'),ts=require(path.join(root,'node_modules/typescript')),{signal,computed}=require(path.join(root,'node_modules/@preact/signals-react'))
 const source=fs.readFileSync(path.join(root,'src/recent-history.ts'),'utf8'),moduleObject={exports:{}}
 vm.runInNewContext(ts.transpileModule(source,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText,{module:moduleObject,exports:moduleObject.exports,require:name=>{
  if(name==='./state')return {cell:signal,derived:computed,beforeDispose:()=>{},watchState:()=>{}}
  if(name==='./api')return {operationRequest:()=>{throw Error('History projection must not fetch')}}
  throw Error(name)
 },setTimeout,clearTimeout,Date,Map,Set,JSON,Number,Math})
 const history=moduleObject.exports.recentHistory(()=> 'isolated-history'),keys=['rx'];history.range.value={from:0,to:15000}
 history.history.value={series:[{key:'rx',points:[{bucketAt:new Date(0).toISOString(),value:1024,samples:1},{bucketAt:new Date(5000).toISOString(),value:0,samples:1},{bucketAt:new Date(10000).toISOString(),value:-1,samples:1}]}]}
 const points=history.points(keys),totals=history.totals(keys)
 assert.equal(totals[0],5120);assert.equal(points.find(point=>point.at===10000).values[0],null,'Invalid samples remain gaps')
 assert.equal(history.points(['rx']),points);assert.equal(history.totals(['rx']),totals,'Unchanged data reuses its projected totals')
 history.loading.value=true;history.failed.value=true
 assert.equal(history.points(['rx']),points,'Loading/error state does not repeat projection or sample checks')
 history.history.value={series:[{key:'rx',points:[{bucketAt:new Date(0).toISOString(),value:2048,samples:1}]}]}
 assert.notEqual(history.points(keys),points);assert.equal(history.totals(keys)[0],10240,'New data invalidates the projection')
 const updated=history.points(keys);history.range.value={from:5000,to:10000}
 assert.notEqual(history.points(keys),updated);assert.equal(history.totals(keys)[0],null,'A new time range does not reuse old totals')
 assert.equal(history.points(['tx']).every(point=>point.values[0]===null),true,'Distinct series retain separate projections')
}
