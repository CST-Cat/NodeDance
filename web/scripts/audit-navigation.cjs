const {fillExpanded}=require('./expanded-textarea.cjs')
// Run after make build. Uses only a temporary real Core with no Agent.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),net=require('node:net')
const {spawn}=require('node:child_process'),{chromium}=require('playwright')
const root=path.resolve(__dirname,'../..'),directory=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-navigation-')),artifacts=path.join(root,'.build/navigation-audit')
;(async()=>{
 const listener=net.createServer();await new Promise(r=>listener.listen(0,'127.0.0.1',r));const port=listener.address().port;await new Promise(r=>listener.close(r))
 const base=`http://127.0.0.1:${port}`,core=spawn(path.join(root,'.build/nodedance'),['serve','--dev','--listen',`127.0.0.1:${port}`,'--data-dir',directory],{stdio:['ignore','pipe','pipe']})
 let logs='',browser;core.stdout.on('data',x=>logs+=x);core.stderr.on('data',x=>logs+=x)
 try{
  let ready=false;for(let i=0;i<100;i++){if(core.exitCode!==null)throw Error(logs);try{ready=(await fetch(base+'/api/v1/health')).ok}catch{}if(ready)break;await new Promise(r=>setTimeout(r,100))}assert.ok(ready)
  browser=await chromium.launch({headless:true});const context=await browser.newContext({locale:'en-US'});const csrf=await(await context.request.get(base+'/api/v1/auth/csrf')).json(),credentials=JSON.parse(fs.readFileSync(path.join(directory,'initial-admin.json'),'utf8'))
  const login=await context.request.post(base+'/api/v1/auth/login',{headers:{Origin:base,'X-CSRF-Token':csrf.token},data:credentials});assert.equal(login.status(),200)
  const page=await context.newPage();page.setDefaultTimeout(10000);const errors=[];page.on('pageerror',e=>errors.push(e.message));fs.mkdirSync(artifacts,{recursive:true})
  const coldCatalog=await(await context.request.get(base+'/api/v1/operations')).json(),publicAppearance=coldCatalog.operations.find(operation=>operation.name==='appearance.get');assert.ok(publicAppearance);
  const publicReads=[],observePublic=request=>{if(new URL(request.url()).pathname===publicAppearance.path)publicReads.push(request.url())};page.on('request',observePublic);
  await page.goto(base+'/#/nodes');await page.locator('.nodes-dashboard').waitFor();page.off('request',observePublic);assert.deepEqual(publicReads,[],'An authenticated cold entry reads the private profile without a preceding public appearance read');
  const catalog=await(await context.request.get(base+'/api/v1/operations')).json(),routes=Object.fromEntries(['auth.me','profile.get','sessions.list'].map(name=>{const operation=catalog.operations.find(operation=>operation.name===name);assert.ok(operation);return [name,operation.path]}));
  const redundant=[];let release;const gate=new Promise(resolve=>release=resolve),intercept=async route=>{const pathname=new URL(route.request().url()).pathname;if(pathname===routes['sessions.list'])await gate;else if([routes['auth.me'],routes['profile.get']].includes(pathname))redundant.push(pathname);await route.fallback()};
  await page.route('**/api/**',intercept);
  const refreshing=page.waitForRequest(request=>new URL(request.url()).pathname===routes['sessions.list']);
  try{await page.locator('.workspace-nav').getByRole('button',{name:'Settings',exact:true}).click();await page.locator('.personal-settings').waitFor({timeout:1000});await refreshing;await page.locator('.workspace-nav').getByRole('button',{name:'Alerts',exact:true}).click();await page.locator('.alerts-page').waitFor({timeout:1000});await page.locator('.workspace-nav').getByRole('button',{name:'Operations',exact:true}).click();await page.locator('.operation-form').waitFor({timeout:1000});assert.deepEqual(redundant,[],'Authenticated navigation does not revalidate identity or reload the profile')}finally{release()}
  await page.unroute('**/api/**',intercept);
  await require('./arc-controls.cjs').selectValue(page.locator('.operation-layout aside button[data-dropdown-field]'),'appearance.update');const genericForm=page.locator('.operation-form form');await genericForm.getByRole('checkbox').first().check();await fillExpanded(genericForm.locator('[data-expanded-textarea-trigger]'),'{');const invalidJSON=page.waitForResponse(response=>new URL(response.url()).pathname==='/api/v1/settings/appearance'&&response.request().method()==='PUT');await genericForm.locator('button[type=submit]').click();assert.equal((await invalidJSON).status(),400,'Raw JSON syntax is judged by Core');await page.locator('.operation-console [role=alert]').waitFor();

  const {nodeA}=await require('./host-read-regressions.cjs')({page,base,catalog});
  const op=name=>{const result=catalog.operations.find(operation=>operation.name===name);assert.ok(result);return result};

  const probeId='isolated-probe',probesPath=op('probes.list').path,probeHistoryPath=op('probes.history').path.replace('{id}',probeId),probeQualityPath=op('probes.quality').path.replace('{id}',probeId);
  let releaseQuality,qualityStarted;const qualityGate=new Promise(resolve=>releaseQuality=resolve),qualityPending=new Promise(resolve=>qualityStarted=resolve);
  const probeRoute=route=>route.fulfill({json:{probes:[{id:probeId,nodeId:nodeA,name:'Independent probe',kind:'icmp',target:'icmp://[2001:db8::1]',intervalSeconds:30,timeoutSeconds:3,enabled:true,revision:1,status:'healthy'}]}}),probeHistoryRoute=route=>route.fulfill({json:{runs:[{runId:'isolated-run',probeId,status:'healthy',checkedAt:new Date().toISOString(),latencyMs:123.4}]}}),probeQualityRoute=async route=>{qualityStarted();await qualityGate;try{await route.fulfill({status:503,body:'isolated quality unavailable'})}catch{}};
  await page.route('**'+nodesPath,nodesRoute);await page.route('**'+probesPath,probeRoute);await page.route('**'+probeHistoryPath+'?*',probeHistoryRoute);await page.route('**'+probeQualityPath,probeQualityRoute);
  try{
   await page.goto(base+'/#/nodes?node='+nodeA+'&section=probes');const probes=page.getByTestId('service-probes');await probes.getByText('Independent probe',{exact:true}).waitFor();
   await probes.getByRole('button',{name:'View history',exact:true}).click();await qualityPending;await probes.locator('.probe-history li[data-status]').waitFor();assert.ok((await probes.locator('.probe-history').innerText()).includes('123.4'),'History renders before quality statistics finish');
   releaseQuality();await probes.locator('[role=alert]').waitFor();assert.equal(await probes.locator('.probe-history li[data-status]').count(),1,'Failed statistics do not hide history');
   await probes.getByRole('button',{name:'Edit',exact:true}).click();const probeForm=page.getByTestId('probe-form');
   const targetInput=probeForm.locator('input').filter({visible:true}).nth(1);assert.equal(await targetInput.inputValue(),'icmp://[2001:db8::1]','Editing preserves a valid IPv6 target');
   const kindControl=probeForm.locator('button[data-dropdown-field]').first();await require('./arc-controls.cjs').selectValue(kindControl,'tcp');await targetInput.fill('tcp://[2001:db8::1]:443');await require('./arc-controls.cjs').selectValue(kindControl,'icmp');assert.equal(await targetInput.inputValue(),'icmp://[2001:db8::1]','Changing probe kind keeps the complete IPv6 host');

  }finally{releaseQuality();await page.unroute('**'+nodesPath,nodesRoute);await page.unroute('**'+probesPath,probeRoute);await page.unroute('**'+probeHistoryPath+'?*',probeHistoryRoute);await page.unroute('**'+probeQualityPath,probeQualityRoute)}

  const alertPaths=Object.fromEntries(['alerts.list','alerts.history','alerts.events','rules.list','channels.list','windows.list','deliveries.list','metrics.catalog'].map(name=>[name,op(name).path]));
  const alertReads=[];let releaseEvents,eventsStarted;const eventsGate=new Promise(resolve=>releaseEvents=resolve),eventsPending=new Promise(resolve=>eventsStarted=resolve);
  const alertObserver=request=>{if(request.method()==='GET'&&Object.values(alertPaths).includes(new URL(request.url()).pathname))alertReads.push(new URL(request.url()).pathname)};page.on('request',alertObserver);
  const alertFixture={id:'isolated-alert',ruleName:'Independent active alert',nodeId:nodeA,nodeName:'Host',severity:'warning',status:'active',message:'Isolated fixture',firstSeenAt:new Date().toISOString(),lastSeenAt:new Date().toISOString()};
  const alertRoute=route=>route.fulfill({json:{alerts:[alertFixture]}}),historyRoute=route=>route.fulfill({json:{alerts:[{...alertFixture,ruleName:'Independent historical alert',status:'resolved'}]}});
  const eventsRoute=async route=>{eventsStarted();await eventsGate;try{await route.fulfill({json:{events:[]}})}catch{}};
  await page.route('**'+alertPaths['alerts.list'],alertRoute);await page.route('**'+alertPaths['alerts.history'],historyRoute);await page.route('**'+alertPaths['alerts.events'],eventsRoute);
  try{
   await page.goto(base+'/#/alerts');await page.getByText('Independent active alert',{exact:true}).waitFor();assert.deepEqual(alertReads,[alertPaths['alerts.list']],'The active view does not read unrelated rule/channel/history data');
   const historyLoaded=page.waitForResponse(response=>new URL(response.url()).pathname===alertPaths['alerts.history']);await page.locator('.alert-tabs').getByRole('tab',{name:'History and events',exact:true}).click();await eventsPending;await historyLoaded;await page.getByText('Independent historical alert',{exact:true}).waitFor();
   const rulesLoaded=page.waitForResponse(response=>new URL(response.url()).pathname===alertPaths['rules.list']);await page.locator('.alert-tabs').getByRole('tab',{name:'Alert rules',exact:true}).click();await rulesLoaded;assert.ok(alertReads.includes(alertPaths['channels.list']));assert.ok(!alertReads.includes(alertPaths['windows.list'])&&!alertReads.includes(alertPaths['deliveries.list']),'Rules only load their own dependencies');releaseEvents();
  }finally{releaseEvents();page.off('request',alertObserver);await page.unroute('**'+alertPaths['alerts.list'],alertRoute);await page.unroute('**'+alertPaths['alerts.history'],historyRoute);await page.unroute('**'+alertPaths['alerts.events'],eventsRoute)}
  await page.locator('.alert-tabs').getByRole('tab',{name:'Notification channels',exact:true}).click();const smtpForm=page.locator('.alerts-page [role=tabpanel][data-state=active] form');await smtpForm.waitFor();
  await smtpForm.getByLabel('Name',{exact:true}).fill('Named SMTP addresses');await require('./arc-controls.cjs').selectValue(smtpForm.locator('button[data-dropdown-field]').first(),'smtp');
  await smtpForm.getByLabel('SMTP host',{exact:true}).fill('smtp.example.test');await smtpForm.getByLabel('Sender',{exact:true}).fill('NodeDance <alerts@example.test>');await smtpForm.getByLabel('Recipient',{exact:true}).fill('运维 <ops@example.test>');
  await page.setViewportSize({width:390,height:844});await page.locator('.app-shell').evaluate(element=>{element.dataset.theme='dark';document.documentElement.dataset.theme='dark'});
  const smtpInputs=smtpForm.locator('input[name=smtpFrom],input[name=smtpTo]');assert.equal(await smtpInputs.count(),2);await smtpForm.getByLabel('Sender',{exact:true}).focus();
  assert.ok(await smtpInputs.evaluateAll(inputs=>inputs.every(input=>{const bounds=input.getBoundingClientRect(),form=input.closest('form').getBoundingClientRect();return bounds.width>0&&bounds.left>=form.left&&bounds.right<=form.right&&parseFloat(getComputedStyle(input).borderRadius)>0})),'The actual SMTP controls fit the narrow form');
  try{await page.waitForFunction(()=>{const input=document.querySelector('input[name=smtpFrom]');if(!input)return false;const style=getComputedStyle(input);return input.matches(':focus-visible')&&style.borderTopColor===style.color},null,{timeout:2500})}catch(error){console.error('SMTP focus state',await smtpForm.getByLabel('Sender',{exact:true}).evaluate(input=>({focused:document.activeElement===input,visible:input.matches(':focus-visible'),border:getComputedStyle(input).borderTopColor,color:getComputedStyle(input).color})));throw error}
  const smtpSaved=page.waitForResponse(response=>new URL(response.url()).pathname===op('channels.create').path&&response.request().method()==='POST');await smtpForm.getByRole('button',{name:'Save channel',exact:true}).click();const smtpResponse=await smtpSaved;assert.equal(smtpResponse.status(),200,'Core-supported mail display names pass the browser form');assert.equal((await smtpResponse.json()).channel.config.smtpTo,'运维 <ops@example.test>');

  const domainPath=op('domains.list').path,channelPath=op('channels.list').path;
  let releaseDomainChannels,domainChannelsStarted;const domainChannelGate=new Promise(resolve=>releaseDomainChannels=resolve),domainChannelsPending=new Promise(resolve=>domainChannelsStarted=resolve);
  const domainsRoute=route=>route.fulfill({json:{domains:[{id:'isolated-domain',name:'independent.example.test',revision:1}]}}),domainChannelsRoute=async route=>{domainChannelsStarted();await domainChannelGate;try{await route.fulfill({status:503,body:'isolated channel unavailable'})}catch{}};
  await page.route('**'+domainPath,domainsRoute);await page.route('**'+channelPath,domainChannelsRoute);
  try{
   await page.goto(base+'/#/domain-certificates');const domains=page.getByTestId('domain-certificates');await domainChannelsPending;await domains.getByText('independent.example.test',{exact:true}).waitFor();
   releaseDomainChannels();await domains.locator('[role=alert]').waitFor();assert.equal(await domains.getByText('independent.example.test',{exact:true}).count(),1,'Channel failure does not discard independently loaded domains');
  }finally{releaseDomainChannels();await page.unroute('**'+domainPath,domainsRoute);await page.unroute('**'+channelPath,domainChannelsRoute)}

  const loginContext=await browser.newContext({locale:'en-US'});
  try{
   const loginPage=await loginContext.newPage();await loginPage.goto(base+'/#/login');await loginPage.locator('#login-title').waitFor();
   const afterLogin=[];loginPage.on('request',request=>{const pathname=new URL(request.url()).pathname;if([routes['auth.me'],routes['sessions.list']].includes(pathname))afterLogin.push(pathname)});
   await loginPage.locator('input[name=username]').fill(credentials.username);await loginPage.locator('input[name=password]').fill(credentials.password);await loginPage.locator('form').getByRole('button',{name:'Sign in',exact:true}).click();await loginPage.locator('.nodes-dashboard').waitFor();assert.deepEqual(afterLogin,[],'Successful sign-in reuses its authenticated user without an extra identity or sessions read');
   let releaseSessions;const pendingSessions=new Promise(resolve=>releaseSessions=resolve);await loginPage.route('**'+routes['sessions.list'],async route=>{await pendingSessions;await route.fallback()});
   try{await loginPage.goto(base+'/#/settings');await loginPage.locator('.personal-settings').waitFor({timeout:1500})}finally{releaseSessions()}
  }finally{await loginContext.close()}

  if(!process.argv.includes('--reads-only')){
   await require('./browser-language-regressions.cjs')({browser,base,credentials,artifacts})
   await require('./mobile-navigation-regressions.cjs')({page,base,artifacts})
  }
  assert.deepEqual(errors,[]);console.log('Navigation read lifecycle passed.')
 }finally{if(browser)await browser.close();core.kill('SIGTERM');await new Promise(r=>core.exitCode!==null?r():core.once('exit',r));fs.rmSync(directory,{recursive:true,force:true})}
})().catch(e=>{console.error(e);process.exitCode=1})
