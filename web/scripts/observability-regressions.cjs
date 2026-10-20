const {setBrowserLanguage}=require('./browser-language.cjs')
// Diagnostics add square cards using the original metric-card styling. Telemetry is
// a protocol fixture on an isolated Core; no real host is touched.
const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,base,node,artifacts,catalog,view,publish}){
 const routePath=name=>{const o=catalog.operations.find(o=>o.name===name);assert.ok(o,`Missing discovered operation ${name}`);return o.path.replace('{nodeId}',node)}
 const now=()=>new Date().toISOString(),metric=value=>({status:'known',value,sampledAt:now(),sampleAgeMillis:0})
 const metricCatalog=await(await page.request.get(base+routePath('metrics.catalog'))).json()
 const definitions=metricCatalog.definitions
 await page.route('**/api/v1/metrics/catalog',r=>r.fulfill({json:metricCatalog}))
 let coreCount=4;
 const observation=()=>({slowIntervalSeconds:60,truncated:false,values:{
  ...Object.fromEntries(Object.entries(definitions).filter(([key])=>!['cpu.usage_percent','memory.used_percent','memory.used_bytes','network.received_bytes_per_second','network.sent_bytes_per_second','swap.used_percent','disk.read_bytes_per_second','disk.write_bytes_per_second'].includes(key)).map(([key,d])=>[key,metric(d.unit==='bytes'?1048576:d.unit==='%'?2.5:1)])),
  'cpu.steal_percent':metric(12.5),'cpu.user_percent':metric(0),'disk.read_latency_ms':{status:'unknown',reason:'no_operations',sampledAt:now(),sampleAgeMillis:0}},entities:[
  ...Array.from({length:coreCount},(_,i)=>({kind:'cpu',id:'cpu'+i,name:'cpu'+i,values:{usage_percent:metric(44)}})),
  {kind:'process',id:'1/100',name:'fixture-process',values:{rss_bytes:metric(1048576),cpu_percent:metric(1.2)}}]})
 const sample=()=>{const v=view();v.metrics.host.observability=observation();v.metrics.cpu.logicalCores.value=coreCount;return v}
 await page.route('**'+routePath('nodes.metrics'),r=>r.fulfill({json:sample()}))
 await page.goto(base+'/#/nodes?node='+node+'&section=overview');await page.reload()
 const cards=page.locator('.metrics-grid > .metric-card:not(.hardware-card)')
 await page.waitForFunction(()=>document.querySelector('[data-diagnostic="cpu.steal_percent"] strong')?.textContent==='12.5%')
 assert.equal(await cards.count(),8,'Three chart cards plus five diagnostic cards reuse the same square-card layout')
 assert.equal(await page.getByTestId('cpu-card').locator('[data-diagnostic]').count(),0,'New parameters must not be crammed into the original cards')
 assert.equal(await page.locator('.observability-panel,.observation-tabs,.observation-value,.observation-entities,.observation-analysis').count(),0,'No independent diagnostic UI or second card layout')
 assert.equal(await page.locator('[data-diagnostic-card="kernelMemory"]').count(),0)
 assert.equal(await page.locator('[data-diagnostic-card="scheduling"] [data-diagnostic]').first().getAttribute('data-diagnostic'),'cpu.steal_percent')
 assert.equal(await page.locator('[data-diagnostic-card="scheduling"] [data-diagnostic]').count(),4)
 assert.equal(await page.locator('[data-diagnostic-card="scheduling"]').locator('[data-diagnostic="cpu.user_percent"] strong').innerText(),'0%','A measured zero remains zero')
 assert.equal(await page.locator('[data-diagnostic-card="diskIO"]').locator('[data-diagnostic="disk.read_latency_ms"] strong').innerText(),'—','Idle disk latency is unknown, never a fabricated zero')
 assert.equal(await page.locator('[data-diagnostic-card="cores"]').locator('[data-diagnostic="core:cpu0"] strong').innerText(),'44.0%')
 assert.equal(await page.locator('[data-diagnostic="process:1/100"] strong').innerText(),'1.2% · 1.0 MiB')
 assert.equal(await page.locator('.metrics-footer').count(),0,'The duplicate bottom metadata block and divider are removed');
 assert.equal(await page.locator('[data-diagnostic-card="collector"] [data-testid="clock-offset"]').count(),1,'Clock and receipt metadata are merged into the collector card');
 assert.equal(await page.locator('[data-diagnostic-card="collector"] [data-testid="sample-identity"]').count(),1);
 assert.equal(await page.locator('.hardware-card [data-hardware="system"]').count(),1,'System metadata appears in hardware');
 assert.equal(await page.getByTestId('boot-id').count(),0,'The raw boot identifier is omitted from the compact hardware view');

 for(const language of ['en','zh-CN'])for(const theme of ['light','dark'])for(const width of [1440,390]){
  await setBrowserLanguage(page,language);await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((e,theme)=>{e.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  const geometry=await cards.evaluateAll(cards=>cards.map(c=>{const r=c.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height,scroll:c.scrollWidth,client:c.clientWidth}}))
  assert.ok(geometry.every(c=>Math.abs(c.width-c.height)<=1&&c.scroll<=c.client+1),'Reused cards remain equal squares without horizontal overflow: '+JSON.stringify(geometry))
  assert.equal(new Set(geometry.map(c=>Math.round(c.x))).size,width===390?2:4,'All cards use the same responsive two/four columns')
  assert.ok(geometry.every(c=>Math.abs(c.width-geometry[0].width)<1),'All new and original cards have the same width')
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1))
  await page.locator('.metrics-panel').screenshot({path:path.join(artifacts,`diagnostics-existing-cards-${language}-${theme}-${width}.png`)})
 }
 for(const width of [1440,390,320])for(const count of [1,2,4,8,16,32]){
  coreCount=count;await page.setViewportSize({width,height:1000});await publish({...sample()});
  const card=page.locator('[data-diagnostic-card="cores"]');
  assert.equal(await card.count(),count===1?0:1,'Single-core machines hide the core card');
  if(count>1){
   assert.equal(await card.locator('.ring').count(),count,'Every reported core has a ring');
   assert.ok(await card.evaluate(e=>e.scrollWidth<=e.clientWidth+1),'Core rings do not overflow horizontally');
   await card.screenshot({path:path.join(artifacts,`cores-${count}-${width}.png`)});
  }
 }
 coreCount=4;await publish({...sample()});
 await setBrowserLanguage(page,'en');const offline=sample();offline.nodeStatus='offline';offline.metrics.cpu.usagePercent.status='stale';offline.metrics.host.observability.values['cpu.steal_percent'].value=98.7;await publish({...offline});assert.equal(await page.locator('[data-diagnostic="cpu.steal_percent"] strong').innerText(),'—','Lease loss hides retained diagnostic readings')
 console.log('Diagnostic card regressions passed: combined resource rectangle, three chart cards and five diagnostic square cards, no separate UI, known zero/idle unknown/per-core/process readings, lease loss, English/Chinese/light/dark/wide/mobile.')
}
