// Synthetic samples against the isolated Core contract; no real Agent or host.
const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,base,node,artifacts,catalog}){
 const routePath=name=>{const operation=catalog.operations.find(o=>o.name===name);assert.ok(operation);return operation.path.replace('{nodeId}',node)}
 const now=()=>new Date().toISOString(),metric=value=>({status:'known',value,sampledAt:now(),sampleAgeMillis:0})
 const state=()=>({status:'online',generation:1,serverTime:now(),leaseValidUntil:new Date(Date.now()+3600000).toISOString()})
 const view=()=>({agentId:'metrics-fixture',nodeId:node,generation:1,sequence:1,collectedAt:now(),receivedAt:now(),clockOffsetMs:0,nodeStatus:'online',activeGeneration:1,...state(),metrics:{
  system:Object.fromEntries(['hostname','os','architecture','platform','platformFamily','platformVersion','kernelVersion'].map(key=>[key,metric('fixture')])),cpu:{usagePercent:metric(12.5),logicalCores:metric(4)},memory:metric({totalBytes:8e9,availableBytes:6e9,usedBytes:2e9,usedPercent:25}),uptime:metric({seconds:3600,bootId:'fixture'}),
  network:{summary:metric({receivedBytesPerSecond:504.5159015380504,sentBytesPerSecond:331.9446676786221}),interfaces:['eth0','ens3','bond0','lo','docker0','br-fixture','tailscale0','vethfixture'].map((name,index)=>({name,includedInSummary:index<3,summaryReason:index<3?'':'virtual_interface',rate:metric({receivedBytesPerSecond:index===0?504.5159015380504:5500.123456789,sentBytesPerSecond:331.9446676786221})}))},
  disk:{status:'known',sampledAt:now(),sampleAgeMillis:0,mounts:[{mountpoint:'/',device:'/dev/vda1',filesystem:'ext4',usage:metric({totalBytes:100e9,usedBytes:40e9,availableBytes:60e9,usedPercent:40})}]}
 }})
 await page.route('**'+routePath('nodes.list'),route=>route.fulfill({json:{nodes:[{nodeId:node,agentId:'metrics-fixture',displayName:'Metrics fixture',status:'online',generation:1,leaseValidUntil:state().leaseValidUntil,capabilities:[]}],serverTime:now()}}))
 await page.route('**'+routePath('nodes.metrics'),route=>route.fulfill({json:view()}))
 await page.routeWebSocket('**'+routePath('dashboard.watch'),()=>{})
 await page.goto(base+'/#/nodes?node='+node+'&section=overview')
 const network=page.getByTestId('network-card');await network.waitFor()
 assert.deepEqual(await network.getByTestId('network-interface').evaluateAll(rows=>rows.map(row=>row.dataset.interfaceName)),['eth0','ens3','bond0'])
 assert.ok((await network.innerText()).includes('505 B/s'));assert.ok((await network.innerText()).includes('332 B/s'));assert.ok((await network.innerText()).includes('5.4 KiB/s'))
 assert.ok(!/\d+\.\d{2,}/.test(await network.innerText()),'No excessive rate precision')
 for(const theme of ['light','dark'])for(const width of [390,1440]){
  await page.setViewportSize({width,height:1000})
  const scrim=page.locator('.navigation-scrim');if(await scrim.isVisible())await scrim.click({position:{x:width-10,y:20}})
  await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
  assert.ok(await network.getByTestId('network-interface').evaluateAll(rows=>rows.every(row=>{const name=row.querySelector('div').getBoundingClientRect(),rates=[...row.querySelectorAll(':scope > span')].map(el=>el.getBoundingClientRect());return rates.every(rate=>name.right<=rate.left+1)&&rates[0].right<=rates[1].left+1})), 'Interface names and rates do not overlap')
  await page.screenshot({path:path.join(artifacts,`metrics-${theme}-${width}.png`),fullPage:true})
 }
 console.log('Metrics regressions passed: primary interfaces, bounded rate precision, no name/rate overlap; 4 light/dark/mobile layouts.')
}
