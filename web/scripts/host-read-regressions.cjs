const assert=require('node:assert/strict')
module.exports=async function({page,base,catalog}){
  const nodeA='185ee25a-e84c-4400-8a99-88773b2b5c8f',nodeB='204cbb59-91e2-4d16-bd67-c9eefca88925';
  const op=name=>{const result=catalog.operations.find(operation=>operation.name===name);assert.ok(result);return result};
  const nodesPath=op('nodes.list').path,settingsA=op('agents.settings').path.replace('{nodeId}',nodeA),settingsB=op('agents.settings').path.replace('{nodeId}',nodeB),assetPath=op('nodes.asset').path.replace('{nodeId}','*');
  let releaseOld,oldStarted,oldFinished,savedHost,grantReads=0,batchReads=0;const oldGate=new Promise(resolve=>releaseOld=resolve),startedOld=new Promise(resolve=>oldStarted=resolve),finishedOld=new Promise(resolve=>oldFinished=resolve);
  const nodesRoute=route=>route.fulfill({json:{nodes:[nodeA,nodeB].map((nodeId,index)=>({nodeId,displayName:'Host '+index,status:'online',generation:1,capabilities:[]})),serverTime:new Date().toISOString()}});
  const currentRoute=route=>{if(route.request().method()==='PUT')savedHost={path:new URL(route.request().url()).pathname,body:route.request().postDataJSON()};return route.fulfill({json:{settings:{customIpv4:'192.0.2.1',memoryMode:'available',fastIntervalSeconds:5,diskIntervalSeconds:30,billingDay:1,transport:'auto',updateIntervalHours:6,commandHistoryRetentionDays:90}}})};
  const staleRoute=async route=>{oldStarted();await oldGate;try{await route.fulfill({json:{settings:{customIpv4:'192.0.2.2',memoryMode:'available',fastIntervalSeconds:5,diskIntervalSeconds:30,billingDay:1,transport:'auto',updateIntervalHours:6,commandHistoryRetentionDays:90}}})}finally{oldFinished()}};
  const assetsRoute=route=>route.fulfill({json:{trafficLimitBytes:0,price:0,currency:'USD',billingCycleUnit:'month',billingCycleDays:0}});
  const grantsPath=op('agents.grants').path,batchesPath=op('commands.batches').path,grantsRoute=route=>{grantReads++;return route.fulfill({json:{grants:[]}})},batchesRoute=route=>{batchReads++;return route.fulfill({json:{batches:[]}})};
  await page.route('**'+grantsPath,grantsRoute);await page.route('**'+batchesPath,batchesRoute);
  const diagnosticsPath=op('agents.diagnostics').path.replace('{nodeId}',nodeA),diagnosticsRoute=route=>route.fulfill({json:{host:'Host A diagnostic'}});await page.route('**'+diagnosticsPath,diagnosticsRoute);
  await page.route('**'+nodesPath,nodesRoute);await page.route('**'+settingsA,currentRoute);await page.route('**'+settingsB,staleRoute);await page.route('**'+assetPath,assetsRoute);
  try{
   await page.goto(base+'/#/host-management?node='+nodeA);await page.reload();const host=page.locator('.host-page');await page.waitForFunction(()=>[...document.querySelectorAll('.host-page input')].some(input=>input.value==='192.0.2.1'));
   assert.equal(grantReads,0,'Host settings do not wait for unrelated grants');assert.equal(batchReads,0,'Host settings do not read batch history');
   await host.getByRole('button',{name:'Diagnostics',exact:true}).click();await host.locator('pre').filter({hasText:'Host A diagnostic'}).waitFor();
   await require('./arc-controls.cjs').selectValue(host.locator('.host-node button[data-dropdown-field]'),nodeB);await startedOld;assert.equal(await host.locator('pre').filter({hasText:'Host A diagnostic'}).count(),0,'Switching node clears the old diagnostics');await page.goBack();
   await page.waitForFunction(()=>[...document.querySelectorAll('.host-page input')].some(input=>input.value==='192.0.2.1'));releaseOld();await finishedOld;
   assert.equal(await host.locator('.host-node button[data-dropdown-field]').getAttribute('data-selected-value'),nodeA);assert.equal(await host.locator('input').filter({visible:true}).evaluateAll(inputs=>inputs.some(input=>input.value==='192.0.2.2')),false,'Late reads cannot fill a different node form');
   const saved=page.waitForResponse(response=>new URL(response.url()).pathname===settingsA&&response.request().method()==='PUT');await host.locator('[role=tabpanel][data-state=active] form').getByRole('button',{name:'Save',exact:true}).click();await saved;assert.equal(savedHost.path,settingsA);assert.equal(savedHost.body.customIpv4,'192.0.2.1','Save retains the current node data after back navigation');
   const grantsLoaded=page.waitForResponse(response=>new URL(response.url()).pathname===grantsPath);await host.locator('[role=tab][data-value=enrollment]').click();await grantsLoaded;assert.equal(grantReads,1);assert.equal(batchReads,0);
   const batchesLoaded=page.waitForResponse(response=>new URL(response.url()).pathname===batchesPath);await host.locator('[role=tab][data-value=commands]').click();await batchesLoaded;assert.equal(batchReads,1);console.log('Host reads passed: current-node submission, stale-read cancellation and section-specific lists.');
  }finally{releaseOld();await page.unroute('**'+diagnosticsPath,diagnosticsRoute);await page.unroute('**'+grantsPath,grantsRoute);await page.unroute('**'+batchesPath,batchesRoute);await page.unroute('**'+nodesPath,nodesRoute);await page.unroute('**'+settingsA,currentRoute);await page.unroute('**'+settingsB,staleRoute);await page.unroute('**'+assetPath,assetsRoute)}

  return {nodeA};
};
