// Bounded adversarial requests from the actual guest Chromium page. All targets
// belong to the temporary fixture; no production server or real Agent is used.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),http=require('node:http')
module.exports=async function attack({base,page,visitor,admin,headers,current,identity,probe,stopReporting,artifacts}){
 const results=[],record=(name,ok,detail)=>{results.push({name,ok,detail});if(!ok)console.error('FINDING',name,detail)}
 const browserRequest=(url,method='GET',body,extra={})=>page.evaluate(async({url,method,body,extra})=>{try{const r=await fetch(url,{method,headers:extra,body:['GET','HEAD'].includes(method)?undefined:JSON.stringify(body??{}),credentials:'include',redirect:'manual',cache:'no-store',signal:AbortSignal.timeout(5000)});return {status:r.status,type:r.type,body:await r.text(),cache:r.headers.get('cache-control')}}catch(e){return {status:0,error:String(e)}}},{url,method,body,extra})
 const csrf=(await browserRequest('/api/v1/auth/csrf')).body
 const guestToken=JSON.parse(csrf).token
 for(const [name,origin,token] of [['origin','https://attacker.invalid',guestToken],['missing-token',base,''],['forged-token',base,'forged']]){const denied=await visitor.request.post(base+'/api/v1/auth/login',{headers:{Origin:origin,'X-CSRF-Token':token},data:{username:'admin',password:'forged-password',persistent:true}});record('login-boundary:'+name,denied.status()===403,{status:denied.status()})}
 const hostileHeaders={'Content-Type':'application/json','Idempotency-Key':'guest-attack-matrix','X-CSRF-Token':guestToken,'X-NodeDance-Session':'persistent','User-Agent':'NodeDance CLI/TUI','X-Role':'admin','X-Mode':'admin','X-Forwarded-User':'admin','X-Authenticated-User':'admin'}
 const catalogResponse=await browserRequest('/api/v1/operations');record('catalog-revalidation',catalogResponse.status===200&&catalogResponse.cache==='no-cache',{status:catalogResponse.status,cache:catalogResponse.cache})
 const catalog=JSON.parse(catalogResponse.body)
 const publicPaths=new Set(['/api/v1/operations','/api/v1/health','/api/v1/protocol','/api/v1/auth/csrf','/api/v1/auth/setup/status','/api/v1/auth/setup','/api/v1/auth/login'])
 const values={nodeId:identity.nodeId,agentId:identity.agentId,probeId:probe.id,containerId:'a'.repeat(64),imageId:'sha256:'+'b'.repeat(64),taskId:'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',streamId:'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',sessionId:'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'}
 const target=o=>o.path.replace(/\{([^}]+)\}/g,(_,k)=>encodeURIComponent(values[k]??'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'))+(o.query?.length?'?'+o.query.map(k=>encodeURIComponent(k)+'='+encodeURIComponent(k==='path'?'/etc/shadow':k==='component'?'core':k==='nodeId'?identity.nodeId:k==='revision'?'1':'guest-attack')).join('&'):'')
 const payload={confirm:true,mode:'admin',role:'admin',component:'core',nodeId:identity.nodeId,script:'printf guest-attack',timeoutSeconds:1,action:'delete',path:'/etc/shadow',content:'guest-attack',enabled:true}
 const historyOperation=catalog.operations.find(o=>o.name==='nodes.history');assert.ok(historyOperation);
 const fineHistory=historyOperation.path.replace('{nodeId}',encodeURIComponent(identity.nodeId))+'?'+new URLSearchParams({resolution:'5s',from:new Date(Date.now()-4*3600000).toISOString(),to:new Date().toISOString()});
 const fineDenied=await browserRequest(fineHistory,'GET',null,hostileHeaders);record('fine-history-remains-private',fineDenied.status===401,{status:fineDenied.status});
 const shareOp=name=>{const o=catalog.operations.find(v=>v.name===name);assert.ok(o,`Missing ${name}`);return o}
 const shareIntent=require('node:crypto').randomBytes(32).toString('hex')
 const uploadShare=shareOp('shares.upload')
 const shareUpload=await admin.request.put(base+uploadShare.path+'?'+new URLSearchParams({name:'guest-share.txt',expiryDays:'1',maxDownloads:'0',requestId:shareIntent}),{headers:{...headers,'Content-Type':'application/octet-stream'},data:'EXPLICIT-SHARE-CONTENT'})
 assert.equal(shareUpload.status(),201);const fileShare=await shareUpload.json()
 const sharedPath=shareOp('shares.public').path.replace('{id}',fileShare.id),hotlinkPath=shareOp('shares.hotlink').path.replace('{id}',fileShare.id)
 const shareAnon=await visitor.request.get(base+sharedPath),shareAdmin=await admin.request.get(base+sharedPath)
 record('share-public-cookie-independent',shareAnon.status()===200&&JSON.stringify(await shareAnon.json())===JSON.stringify(await shareAdmin.json()),{status:shareAnon.status()})
 for(const suffix of ['?path=/etc/shadow','?role=admin','?id='+identity.nodeId]){const r=await browserRequest(sharedPath+suffix);record('share-query-expansion:'+suffix,r.status===400,{status:r.status})}
 for(const method of ['PUT','PATCH','DELETE','HEAD']){const r=await browserRequest(hotlinkPath+'?path=guest-share.txt',method,{},hostileHeaders);record('share-method-boundary:'+method,r.status===405,{status:r.status})}
 const escaped=await browserRequest(hotlinkPath+'?path=../etc/shadow');record('share-path-cannot-read-host',escaped.status===404,{status:escaped.status})
 const readShare=await browserRequest(hotlinkPath+'?path=guest-share.txt');record('share-only-explicit-content',readShare.status===200&&readShare.body==='EXPLICIT-SHARE-CONTENT',{status:readShare.status})
 const revokeShare=shareOp('shares.revoke');assert.equal((await admin.request.delete(base+revokeShare.path.replace('{id}',fileShare.id)+'?revision='+fileShare.revision,{headers})).status(),204)
 const withdrawnShare=await browserRequest(hotlinkPath+'?path=guest-share.txt');record('share-immediate-withdrawal',withdrawnShare.status===404,{status:withdrawnShare.status})
 const privateOperations=catalog.operations.filter(o=>!publicPaths.has(o.path)&&!o.path.startsWith('/api/v1/public/'))
 console.log('GUEST SECURITY: private operations',privateOperations.length)
 for(let i=0;i<privateOperations.length;i+=8){await Promise.all(privateOperations.slice(i,i+8).map(async o=>{const r=await browserRequest(target(o),o.method,payload,hostileHeaders);record('catalog:'+o.name,r.status===401,{status:r.status})}))}

 // Core runtime metadata and the shared memo are never part of public projection.
 const savedMemo=await(await admin.request.get(base+'/api/v1/dashboard/memo')).json()
 const memoWrite=await admin.request.put(base+'/api/v1/dashboard/memo',{headers,data:{content:'PRIVATE-OVERVIEW-MEMO <script>window.__memoLeak=1</script>',revision:savedMemo.revision}})
 assert.equal(memoWrite.status(),200)
 for(const actor of [page.context(),admin]){const name=actor===admin?'administrator':'guest';const response=await actor.request.get(base+'/api/v1/public/guest/display');const body=await response.text();record('overview-memo-private:'+name,response.status()===200&&!body.includes('PRIVATE-OVERVIEW-MEMO')&&!body.includes('kernelVersion')&&!body.includes('environment'),{status:response.status()});const forged=await actor.request.get(base+'/api/v1/public/guest/display?memo=true&core=true');record('overview-query-rejected:'+name,forged.status()===400,{status:forged.status()})}
 // Enrollment token-bearing installation commands remain private.
 const enrollment=await admin.request.post(base+'/api/v1/agents/enrollments',{headers,data:{displayName:'PRIVATE INSTALL NAME',coreUrl:base,deploymentMethod:'docker',asset:{price:98765.4321,currency:'JPY',billingCycleUnit:'minute',billingCycleDays:30,purchasedAt:'2098-04-03T00:00:00Z',expiresAt:'2099-05-06T07:08:00Z',expiryNoticeDays:7,trafficLimitBytes:1234567890123,trafficAccounting:'sum',trafficWarningPercent:90}}})
 assert.equal(enrollment.status(),201);const install=await enrollment.json()
 for(const command of [install.nativeInstallCommand,install.dockerInstallCommand])assert.ok(command.includes('--token '+install.token))
 const projection=await browserRequest('/api/v1/public/guest/display')
 record('guest-projection-no-store',projection.cache?.includes('no-store'),{cache:projection.cache})
 record('private-install-command-projection',projection.status===200&&!projection.body.includes(install.token)&&!projection.body.includes('PRIVATE INSTALL NAME'))
 const assetPath=catalog.operations.find(o=>o.name==='nodes.asset').path.replace('{nodeId}',install.nodeId)
 const savedAsset=await admin.request.get(base+assetPath);assert.equal(savedAsset.status(),200);assert.equal((await savedAsset.json()).billingCycleUnit,'minute')
 // Currency values outside the contract enum are rejected by Core validation.
 const catalogEnrollment=catalog.operations.find(o=>o.name==='agents.enrollment');assert.ok(catalogEnrollment?.bodySchema?.properties?.asset?.properties?.currency?.enum)
 const forgedCurrency=await admin.request.post(base+'/api/v1/agents/enrollments',{headers,data:{displayName:'FORGED CURRENCY',coreUrl:base,deploymentMethod:'docker',asset:{price:1,currency:'PRIVBILL',billingCycleUnit:'month',billingCycleDays:30,trafficAccounting:'sum'}}})
 record('forged-currency-rejected',forgedCurrency.status()===400,{status:forgedCurrency.status()})
 for(const cookieContext of [page.context(),admin]){
  const response=await cookieContext.request.get(base+'/api/v1/public/guest/display');const body=await response.text()
  record('private-billing-projection:'+ (cookieContext===admin?'administrator':'guest'),response.status()===200&&!['98765.4321','1234567890123','2099-05-06','2098-04-03','purchasedAt','billingCycleUnit','trafficLimitBytes'].some(value=>body.includes(value)))
 }

 const channelOptions=catalog.operations.find(o=>o.name==='channels.options');assert.ok(channelOptions)
 const privateChannel=await admin.request.post(base+'/api/v1/alerts/channels',{headers,data:{name:'PRIVATE CHANNEL FIXTURE',kind:'webhook',config:{webhookUrl:'https://example.invalid/private-notifications'},secret:'PRIVATE_BEARER_FIXTURE',headers:{'X-Private':'PRIVATE_HEADER_FIXTURE'},enabled:false}})
 assert.equal(privateChannel.status(),200);const savedChannel=(await privateChannel.json()).channel
 const adminChannels=await admin.request.get(base+'/api/v1/alerts/channels'),adminChannelBody=await adminChannels.text()
 record('channel-admin-secret-redaction',adminChannels.status()===200&&!adminChannelBody.includes('PRIVATE_BEARER_FIXTURE')&&!adminChannelBody.includes('PRIVATE_HEADER_FIXTURE'))
 for(const cookieContext of [page.context(),admin]){
  const response=await cookieContext.request.get(base+'/api/v1/public/guest/display'),body=await response.text()
  record('channel-public-projection:'+(cookieContext===admin?'administrator':'guest'),response.status()===200&&!['PRIVATE CHANNEL FIXTURE','PRIVATE_BEARER_FIXTURE','PRIVATE_HEADER_FIXTURE','private-notifications'].some(v=>body.includes(v)))
 }
 const deleteChannel=await admin.request.delete(base+'/api/v1/alerts/channels/'+savedChannel.id,{headers,data:{revision:savedChannel.revision,confirm:true}});assert.equal(deleteChannel.status(),204)
 const anonymousInstall=await browserRequest('/api/v1/agents/enrollments','POST',{displayName:'forged-install',coreUrl:base},hostileHeaders)
 record('private-install-command-anonymous',anonymousInstall.status===401)
 // Private RSS subscriptions never enter guest projections, even with admin cookies.
 const rss=await admin.request.post(base+'/api/v1/feeds',{headers,data:{name:'PRIVATE RSS NAME',url:'https://example.invalid/feed?token=RSS_PRIVATE_TOKEN',enabled:false,keywords:[],channelIds:[],notificationMode:'off'}})
 assert.equal(rss.status(),200);const subscription=await rss.json()
 for(const suffix of ['', '/'+subscription.id, '/'+subscription.id+'/items', '/'+subscription.id+'/deliveries']){const r=await browserRequest('/api/v1/feeds'+suffix);record('private-RSS:'+suffix,r.status===401,{status:r.status})}
 const rssGuest=await browserRequest('/api/v1/public/guest/display'),rssAdmin=await admin.request.get(base+'/api/v1/public/guest/display'),rssAdminBody=await rssAdmin.text()
 record('RSS-excluded-with-admin-cookie',rssGuest.status===200&&rssAdmin.status()===200&&rssGuest.body===rssAdminBody&&!rssAdminBody.includes('PRIVATE RSS NAME')&&!rssAdminBody.includes('RSS_PRIVATE_TOKEN'),{status:rssGuest.status})
 const rssDelete=await admin.request.delete(base+'/api/v1/feeds/'+subscription.id,{headers,data:{revision:subscription.revision,confirm:true}});record('private-RSS-removal',rssDelete.status()===204,{status:rssDelete.status()})
 const fileAction=catalog.operations.find(o=>o.name==='files.action');assert.ok(fileAction)
 for(const format of fileAction.bodySchema.properties.format.enum){
  const r=await browserRequest(target(fileAction),fileAction.method,{action:'archive',path:'/tmp/guest.'+format,format,paths:['/etc/shadow']},hostileHeaders)
  record('file-archive-format:'+format,r.status===401,{status:r.status})
 }
 console.log('GUEST SECURITY: public scope and IDOR')
 const selected=JSON.parse((await browserRequest('/api/v1/public/guest/display')).body)
 const publicID=selected.nodes[0].id
 const idorTargets=[...['metrics','history','preferences','containers','files','commands','agent-settings'].flatMap(suffix=>['/api/v1/nodes/'+identity.nodeId+'/'+suffix,'/api/v1/nodes/'+publicID+'/'+suffix]),'/api/v1/agents/'+publicID+'/revoke']
 for(const url of idorTargets){const r=await browserRequest(url,url.endsWith('/revoke')?'POST':'GET',payload,hostileHeaders);record('IDOR:'+url.split('/').pop()+':'+results.length,r.status===401,{status:r.status})}
 for(const query of ['nodeId='+identity.nodeId,'nodeId='+publicID,'metrics=memory.used_percent','metrics[]=system.hostname','historyHours=8760','from=1900-01-01&to=2999-01-01','mode=admin&includePrivate=true','__proto__[enabled]=true','nodeId=../guest/settings','nodeId=%00','nodeId=a;b','nodeId=%zz']){
  const r=await browserRequest('/api/v1/public/guest/display?'+query);record('public-query:'+query.split('=')[0]+':'+results.length,r.status===400,{status:r.status})
 }
 for(const method of ['POST','PUT','PATCH','DELETE','OPTIONS'])for(const url of ['/api/v1/public/guest','/api/v1/public/guest/display']){
  const r=await browserRequest(url,method,payload,{...hostileHeaders,'X-HTTP-Method-Override':'PUT'});record('method:'+method+':'+url,r.status===405,{status:r.status})
 }
 for(const method of ['GET','POST','PUT','PATCH','DELETE','HEAD','OPTIONS'])for(const url of ['/api/v1/guest/settings','/api/v1/updates/apply','/api/v1/updates/release?component=core','/api/v1/updates/delete-backup','/api/v1/nodes/'+identity.nodeId+'/commands']){
  const r=await browserRequest(url,method,payload,{...hostileHeaders,'X-HTTP-Method-Override':'GET'});record('private-method:'+method+':'+url.split('/').pop(),r.status===401,{status:r.status})
 }
 const paths=['/%61pi/v1/nodes','/api//v1/nodes','/api/v1/%6eodes','/api/v1/public/guest/../../../v1/nodes','/api/v1/public/guest/display/../../../../v1/nodes','/api/v1/public/guest/display%2f..%2f..%2f..%2fnodes','/api/v1/public/guest/display%252f..%252fnodes','/api/v1/public/guest/display;/../../guest/settings','/API/v1/nodes','/api/v1/nodes%00','/.git/config','/../../etc/shadow']
 for(const url of paths){const r=await browserRequest(url);record('encoded-path:'+url,!r.body?.includes('PRIVATE HOST NAME')&&r.status>=400&&r.status<500,{status:r.status})}
 for(const cookie of ['admin','true','0','f'.repeat(64),Buffer.from(JSON.stringify({role:'admin',authenticated:true})).toString('base64url')]){
  await visitor.addCookies([{name:'nodedance_session',value:cookie,url:base,httpOnly:true,sameSite:'Strict'}])
  const r=await browserRequest('/api/v1/nodes');record('forged-session:'+cookie.length,r.status===401,{status:r.status})
 };await visitor.clearCookies()
 for(const authorization of ['Bearer '+'f'.repeat(64),'Enrollment '+'f'.repeat(64),'Basic '+Buffer.from('admin:admin').toString('base64')]){
  const r=await browserRequest('/api/v1/nodes','GET',null,{Authorization:authorization});record('forged-authorization:'+authorization.split(' ')[0],r.status===401,{status:r.status})
 }
 const sockets=['/ws/v1/dashboard','/ws/v1/streams/logs?nodeId='+identity.nodeId+'&containerId='+values.containerId,'/ws/v1/streams/stats?nodeId='+identity.nodeId+'&containerId='+values.containerId,'/ws/v1/streams/terminal?ticket='+'f'.repeat(64),'/ws/v1/agent']
 for(const target of sockets){const r=await page.evaluate(target=>new Promise(resolve=>{const ws=new WebSocket(location.origin.replace(/^http/,'ws')+target);let opened=false;const timer=setTimeout(()=>{ws.close();resolve({opened,timeout:true})},3000);ws.onopen=()=>{opened=true;ws.close()};ws.onclose=e=>{clearTimeout(timer);resolve({opened,code:e.code})};ws.onerror=()=>{} }),target);record('websocket:'+target.split('?')[0],!r.opened&&!r.timeout,r)}
 // Real browser HTML-form CSRF from another origin, using administrator cookies.
 const cross=http.createServer((req,res)=>{res.setHeader('Content-Type','text/html');res.end('<!doctype html><title>Foreign origin</title>')});await new Promise(r=>cross.listen(0,'127.0.0.1',r))
 const crossPage=await admin.newPage();try{await crossPage.goto('http://127.0.0.1:'+cross.address().port);const response=crossPage.waitForResponse(r=>r.url()===base+'/api/v1/updates/apply');await crossPage.evaluate(url=>{const form=document.createElement('form');form.method='POST';form.action=url;document.body.append(form);form.submit()},base+'/api/v1/updates/apply');record('cross-origin-form-CSRF',(await response).status()===403,{status:(await response).status()})}finally{void crossPage.close().catch(()=>{});cross.close();cross.closeAllConnections()}
 console.log('GUEST SECURITY: injection and cache withdrawal')
 // Browser-side stored HTML/script injection in every public alias/title.
 let policy=current;const save=async overrides=>{const r=await admin.request.put(base+'/api/v1/guest/settings',{headers,data:{...policy,...overrides}});if(r.ok())policy=await r.json();return r}
 const evil='<img src=/guest-attack-beacon onerror=window.__guestXss=1>'
 const injected=await save({title:evil,nodes:[{id:identity.nodeId,name:evil}],probes:[{id:probe.id,name:evil}]});assert.equal(injected.status(),200)
 await page.evaluate(()=>{window.__guestXss=0});await page.locator('[data-testid=guest-display]').getByRole('button',{name:'Refresh',exact:true}).click();await page.waitForFunction(evil=>document.querySelector('[data-testid=guest-display] h1')?.textContent===evil,evil)
 const xss=await page.evaluate(()=>({executed:window.__guestXss,images:document.querySelectorAll('[data-testid=guest-display] img').length}));record('stored-title-node-service-XSS',xss.executed===0&&xss.images===0,xss)
 assert.equal((await save({title:current.title,nodes:current.nodes,probes:current.probes})).status(),200)
 // Read storm is bounded and all responses must retain the exact selected scope.
 const burst=page.evaluate(async()=>{const responses=[];for(let wave=0;wave<10;wave++)responses.push(...await Promise.all(Array.from({length:24},async()=>{const r=await fetch('/api/v1/public/guest/display',{cache:'no-store'});return {status:r.status,text:await r.text()}})));return responses})
 const start=Date.now(),management=await admin.request.get(base+'/api/v1/guest/settings');record('management-during-guest-storm',management.status()===200&&Date.now()-start<2000,{status:management.status(),ms:Date.now()-start})
 const burstResults=await burst;for(let i=0;i<burstResults.length;i++){const r=burstResults[i];record('burst:'+i,[200,503].includes(r.status)&&!r.text.includes('PRIVATE HOST NAME')&&!r.text.includes('memory.used_percent'),{status:r.status})}
 // Subsequent reads after a policy change must stop returning removed fields.
 assert.equal((await save({metrics:['memory.used_percent'],historyHours:0})).status(),200)
 const changed=await browserRequest('/api/v1/public/guest/display');record('warm-cache-field-withdrawal',changed.status===200&&!changed.body.includes('cpu.usage_percent')&&changed.body.includes('memory.used_percent'),{status:changed.status})
 assert.equal((await save({metrics:current.metrics,historyHours:current.historyHours})).status(),200)
 await browserRequest('/api/v1/public/guest/display')
 // Withdraw a real probe and revoke the protocol fixture after warming the cache.
 const deleted=await admin.request.delete(base+'/api/v1/probes/'+probe.id+'?revision='+probe.revision,{headers});assert.equal(deleted.status(),204)
 policy=(await(await admin.request.get(base+'/api/v1/guest/settings')).json()).settings;assert.ok(!policy.probes.some(p=>p.id===probe.id),'Deleting a probe removes its saved publication');
 const afterDelete=await browserRequest('/api/v1/public/guest/display');record('warm-cache-deleted-probe',afterDelete.status===200&&!JSON.parse(afterDelete.body).probes.some(p=>p.name==='Public service'),{status:afterDelete.status,probes:JSON.parse(afterDelete.body).probes.length})
 await stopReporting();const revoked=await admin.request.post(base+'/api/v1/agents/'+identity.agentId+'/revoke',{headers});assert.equal(revoked.status(),200)
 policy=(await(await admin.request.get(base+'/api/v1/guest/settings')).json()).settings;assert.ok(!policy.nodes.some(n=>n.id===identity.nodeId),'Revocation removes the saved node publication');
 const afterRevoke=await browserRequest('/api/v1/public/guest/display');record('warm-cache-revoked-node',afterRevoke.status===200&&JSON.parse(afterRevoke.body).nodes.length===0,{status:afterRevoke.status,nodes:JSON.parse(afterRevoke.body).nodes.length})
 // A deleted node must disappear immediately even with a warmed public cache.
 const fresh=await admin.request.post(base+'/api/v1/agents/enrollments',{headers,data:{displayName:'Deletion fixture',coreUrl:base}});assert.equal(fresh.status(),201)
 const candidate=await fresh.json()
 const enrolled=await admin.request.post(base+'/api/v1/agents/enroll',{headers:{Origin:base,Authorization:'Enrollment '+candidate.token},data:{credential:'d'.repeat(64),requestId:'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'}});assert.equal(enrolled.status(),201)
 const deletionPolicy=await save({nodes:[{id:candidate.nodeId,name:'Published deletion fixture'}],probes:[]});assert.equal(deletionPolicy.status(),200)
 const warm=await browserRequest('/api/v1/public/guest/display');assert.ok(JSON.parse(warm.body).nodes.some(n=>n.name==='Published deletion fixture'))
 const removed=await admin.request.delete(base+'/api/v1/nodes/'+candidate.nodeId,{headers,data:{confirm:true}});assert.equal(removed.status(),200)
 policy=(await(await admin.request.get(base+'/api/v1/guest/settings')).json()).settings;assert.ok(!policy.nodes.some(n=>n.id===candidate.nodeId),'Deleting a node removes its saved publication');
 const settingsPage=await admin.newPage();await settingsPage.goto(base+'/#/settings');await settingsPage.locator('.guest-settings').waitFor();await settingsPage.locator('.guest-settings [type=submit]').waitFor();assert.equal(await settingsPage.locator('.guest-resource').filter({hasText:/Published deletion fixture|resource unavailable/i}).count(),0);await settingsPage.reload();await settingsPage.locator('.guest-settings [type=submit]').waitFor();assert.equal(await settingsPage.locator('.guest-resource').filter({hasText:/Published deletion fixture|resource unavailable/i}).count(),0);await settingsPage.close();
 const afterNodeDelete=await browserRequest('/api/v1/public/guest/display');record('warm-cache-deleted-node',afterNodeDelete.status===200&&JSON.parse(afterNodeDelete.body).nodes.length===0,{status:afterNodeDelete.status})
 const rejected=await admin.request.post(base+'/api/v1/agents/enroll',{headers:{Origin:base,Authorization:'Enrollment '+candidate.token},data:{credential:'e'.repeat(64),requestId:'cccccccc-cccc-4ccc-8ccc-cccccccccccc'}});record('deleted-node-enrollment-rejected',rejected.status()===401,{status:rejected.status()})
 const disabled=await save({enabled:false});record('disable-with-unavailable-selected-resources',disabled.status()===200,{status:disabled.status()})
 if(!disabled.ok())assert.equal((await save({enabled:false,nodes:[],probes:[]})).status(),200)
 const closed=await browserRequest('/api/v1/public/guest/display');record('warm-cache-global-disable',closed.status===404,{status:closed.status})
 const failures=results.filter(r=>!r.ok),counts={cases:results.length,failed:failures.length,privateOperations:privateOperations.length,burstRequests:burstResults.length,burst503:burstResults.filter(r=>r.status===503).length}
 fs.writeFileSync(path.join(artifacts,'security-results.json'),JSON.stringify({counts,failures,results},null,2)+'\n');console.log('GUEST SECURITY RESULT',JSON.stringify({counts,failures}))
 return {policy,counts,failures}
}
