const {setBrowserLanguage}=require('./browser-language.cjs')
// Isolated real Core + root Agent + Chromium. Only disposable file fixtures
// are modified. No systemd installation, host commands or Docker mutations.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),net=require('node:net'),crypto=require('node:crypto')
const {spawn,execFileSync}=require('node:child_process'),{chromium}=require('playwright')
const root=path.resolve(__dirname,'../..'),artifacts=path.join(root,'.build/file-audit')
fs.mkdirSync(artifacts,{recursive:true})
async function port(){const s=net.createServer();await new Promise(r=>s.listen(0,'127.0.0.1',r));const value=s.address().port;await new Promise(r=>s.close(r));return value}
async function stop(p){if(!p||p.exitCode!==null)return;p.kill('SIGTERM');await Promise.race([new Promise(r=>p.once('exit',r)),new Promise(r=>setTimeout(r,5000))]);if(p.exitCode===null)p.kill('SIGKILL')}
;(async()=>{
 // Build the production Driver separately for exhaustive browser/API probes.
 // This test-only module is served by Playwright; no test hook enters the app.
 const {build}=await import('vite')
 await build({configFile:false,logLevel:'error',define:{'process.env.NODE_ENV':JSON.stringify('production')},build:{lib:{entry:path.join(root,'web/src/file-manager.ts'),formats:['es'],fileName:()=> 'driver-audit.js'},outDir:path.join(artifacts,'driver'),emptyOutDir:true}})
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-file-audit-')),data=path.join(dir,'core'),files=path.join(dir,'files'),state=execFileSync('sudo',['mktemp','-d','/run/nodedance-file-audit-XXXXXX'],{encoding:'utf8'}).trim(),base=`http://127.0.0.1:${await port()}`
 fs.mkdirSync(files);fs.mkdirSync(path.join(files,'many'));fs.mkdirSync(path.join(files,'out'))
 fs.writeFileSync(path.join(files,'hello.txt'),'hello fixture\n');fs.writeFileSync(path.join(files,'image.png'),Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aZMsAAAAASUVORK5CYII=','base64'))
 for(let i=0;i<205;i++)fs.writeFileSync(path.join(files,'many',`entry-${String(i).padStart(3,'0')}.txt`),'fixture')
 fs.mkdirSync(path.join(files,'private-dir'),{mode:0700});fs.writeFileSync(path.join(files,'private-dir','child.txt'),'private fixture',{mode:0644})
 fs.writeFileSync(path.join(files,'bzip.tar.bz2'),Buffer.from('QlpoOTFBWSZTWcHlGiYAAG97gMqAAQBAAW+AAEBzIF5QCAggAFQ0UGmg0ZPUMRvVBJRqaAAMmQH3Vw9CCNSEIzpCw0vatAhwOKGTJNs8Rxegu3fJmc5RItGBFfRWwJX7ay5EcIiAfi7kinChIYPKNEw=','base64'))
 fs.symlinkSync('hello.txt',path.join(files,'link'))
 const longName='long-文件名-'+ 'abcdefghij'.repeat(12)+'.tar.gz';fs.writeFileSync(path.join(files,longName),'long name fixture')
 const core=spawn(path.join(root,'.build/nodedance'),['serve','--dev','--listen',new URL(base).host,'--data-dir',data],{cwd:root,stdio:['ignore','pipe','pipe']});let logs='',browser,agent
 core.stdout.on('data',v=>logs+=v);core.stderr.on('data',v=>logs+=v)
 try{
  for(let i=0;i<100;i++){try{if((await fetch(base+'/api/v1/health')).ok)break}catch{};await new Promise(r=>setTimeout(r,100))}
  browser=await chromium.launch({headless:true});const context=await browser.newContext({locale:'en-US'});const csrf=await(await context.request.get(base+'/api/v1/auth/csrf')).json(),credentials=JSON.parse(fs.readFileSync(path.join(data,'initial-admin.json')))
  const login=await context.request.post(base+'/api/v1/auth/login',{headers:{Origin:base,'X-CSRF-Token':csrf.token},data:credentials});assert.equal(login.status(),200)
  const headers={Origin:base,'X-CSRF-Token':(await login.json()).csrfToken}
  const catalog=(await(await context.request.get(base+'/api/v1/operations')).json()).operations
  const operation=name=>{const o=catalog.find(o=>o.name===name);assert.ok(o,`Missing ${name}`);return o}
  const enrollmentResponse=await context.request.post(base+'/api/v1/agents/enrollments',{headers,data:{displayName:'Disposable file fixture',coreUrl:base}});assert.equal(enrollmentResponse.status(),201)
  const enrollment=await enrollmentResponse.json(),node=enrollment.nodeId
  execFileSync('sudo',['install','-d','-m','0700',state])
  execFileSync('sudo',[path.join(root,'.build/nodedance-agent'),'enroll','--server',base,'--dev','--config',path.join(state,'agent.json'),'--token-stdin'],{input:enrollment.token+'\n',stdio:['pipe','pipe','pipe']})
  agent=spawn('sudo',['env',`NODEDANCE_AGENT_FILE_ROOT=${files}`,'DOCKER_HOST=unix:///nonexistent/nodedance-file-fixture.sock',path.join(root,'.build/nodedance-agent'),'run','--config',path.join(state,'agent.json')],{stdio:['ignore','pipe','pipe']});agent.stderr.on('data',v=>logs+=v)
  const endpoint=(name,query={})=>{const o=operation(name);return base+o.path.replace('{nodeId}',node)+(Object.keys(query).length?'?'+new URLSearchParams(query):'')}
  let online=false;for(let i=0;i<200;i++){const response=await context.request.get(endpoint('files.list',{path:'/'}));if(response.ok()){online=true;break};await new Promise(r=>setTimeout(r,100))};assert.ok(online,logs)
  // Selected roots use the same symlink/traversal rejection as full-host roots.
  // All targets below remain inside this disposable fixture.
  for(const filePath of ['/link','/../hello.txt','/many/../hello.txt']){
   const rejected=await context.request.get(endpoint('files.text',{path:filePath}))
   assert.equal(rejected.status(),400,`Unsafe file path accepted: ${filePath}`)
  }
  assert.equal(fs.readFileSync(path.join(files,'hello.txt'),'utf8'),'hello fixture\n')
  async function action(body,key=crypto.randomUUID()){
   const response=await context.request.post(endpoint('files.action'),{headers:{...headers,'Idempotency-Key':key},data:body});assert.ok(response.ok(),await response.text());const accepted=await response.json()
   for(let i=0;i<200;i++){const task=await(await context.request.get(base+operation('tasks.get').path.replace('{nodeId}',node).replace('{taskId}',accepted.taskId))).json();if(!['queued','running'].includes(task.status)){assert.equal(task.status,'succeeded',JSON.stringify(task));return task};await new Promise(r=>setTimeout(r,25))};throw Error('File task timeout')
  }
  let count=0,after='';do{const response=await context.request.get(endpoint('files.list',{path:'/many',...(after?{after}:{})}));assert.equal(response.status(),200);const page=await response.json();count+=page.entries.length;assert.ok(page.entries.length<=100);after=page.nextAfter}while(after);assert.equal(count,205)
  const search=await(await context.request.get(endpoint('files.search',{path:'/',filter:'entry-204',recursive:'true'}))).json();assert.equal(search.entries.length,1)
  const key=crypto.randomUUID(),body={action:'copy',path:'/hello.txt',newPath:'/copy.txt'};const copied=await action(body,key),repeated=await action(body,key);assert.equal(copied.taskId,repeated.taskId)
  await action({action:'move',path:'/copy.txt',newPath:'/out/moved.txt'})
  await action({action:'archive',path:'/bundle.zip',paths:['/hello.txt']})
  await action({action:'extract',path:'/bundle.zip',newPath:'/out'})
  assert.equal(fs.readFileSync(path.join(files,'out','hello.txt'),'utf8'),'hello fixture\n')
  const text=await(await context.request.get(endpoint('files.text',{path:'/hello.txt'}))).json()
  await action({action:'save_text',path:'/hello.txt',content:'edited fixture\n',expectedVersion:text.version})
  const bad=await context.request.post(endpoint('files.action'),{headers:{...headers,'Idempotency-Key':crypto.randomUUID()},data:{action:'save_text',path:'/hello.txt',content:'stale',expectedVersion:text.version}});assert.ok(bad.ok());const badTask=await bad.json()
  let failed;for(let i=0;i<100;i++){failed=await(await context.request.get(base+operation('tasks.get').path.replace('{nodeId}',node).replace('{taskId}',badTask.taskId))).json();if(failed.status==='failed')break;await new Promise(r=>setTimeout(r,25))};assert.equal(failed.status,'failed');assert.equal(fs.readFileSync(path.join(files,'hello.txt'),'utf8'),'edited fixture\n')
  for(const name of ['files.download','files.preview'])assert.equal((await context.request.get(endpoint(name,{path:'/missing.txt'}))).status(),404,'Missing downloads preserve Agent not-found semantics')
  const preview=await context.request.get(endpoint('files.preview',{path:'/image.png'}),{headers:{Range:'bytes=0-7'}});assert.equal(preview.status(),206);assert.ok(preview.headers()['content-security-policy'].includes('sandbox'))
  const visitor=await browser.newContext({locale:'en-US'});assert.equal((await visitor.request.get(endpoint('files.preview',{path:'/image.png'}))).status(),401)
  const page=await context.newPage();page.setDefaultTimeout(15000);const errors=[],externalRequests=[];page.on('pageerror',e=>errors.push(e.message));page.on('request',r=>{if(r.url().startsWith('http')&&new URL(r.url()).origin!==base)externalRequests.push(r.url())})
  // Cold startup must not wait for one network round trip per widget/script.
  const startupScripts=[];page.on('request',r=>{if(r.resourceType()==='script')startupScripts.push(new URL(r.url()).pathname)});
  let releaseVendors;const vendorsReady=new Promise(resolve=>releaseVendors=resolve),vendorRequests=[];let firstVendorBatch;
  const coldVendorRoute=async route=>{
   vendorRequests.push(new URL(route.request().url()).pathname);if(vendorRequests.length===3)releaseVendors();
   await Promise.race([vendorsReady,new Promise(resolve=>setTimeout(resolve,750))]);
   firstVendorBatch??=vendorRequests.length;await route.continue();
  };
  await page.route('**/vendor/elfinder/*.js',coldVendorRoute);
  let releaseFiles;const filesReady=new Promise(resolve=>releaseFiles=resolve),initialFileReads=[];
  const initialFilesRoute=async route=>{initialFileReads.push(route.request().url());await filesReady;await route.continue()};
  await page.route('**/api/v1/nodes/*/files**',initialFilesRoute);
  await page.goto(base+`/#/nodes?node=${node}&section=overview`);
  await page.waitForFunction(()=>!!window.elFinder);
  assert.equal(initialFileReads.length,0,'Preparing the interface never reads remote files');
  const scriptsBeforeClick=startupScripts.length;
  await page.evaluate(()=>{
   document.addEventListener('click',()=>{
    const clicked=performance.now(),observer=new MutationObserver(()=>{if(document.querySelector('[data-testid="file-loading"]')){window.fileFeedbackMs=performance.now()-clicked;observer.disconnect()}});
    observer.observe(document.body,{childList:true,subtree:true});
   },{once:true,capture:true});
  });
  await page.getByRole('button',{name:'File manager',exact:true}).click();
  await page.getByTestId('file-loading').waitFor({timeout:1000});
  assert.ok(await page.evaluate(()=>window.fileFeedbackMs<1000),'A warmed file manager responds immediately while directory requests are blocked');
  assert.equal(startupScripts.length,scriptsBeforeClick,'Clicking downloads no additional interface scripts');
  releaseFiles();
  await page.locator('.elfinder-cwd-filename').filter({hasText:'hello.txt'}).first().waitFor();
  await page.getByTestId('file-loading').waitFor({state:'detached'});
  await page.unroute('**/api/v1/nodes/*/files**',initialFilesRoute);
  assert.equal(await page.locator('.detail-heading-status').count(),0,'File manager heading has no redundant online or Engine badges');
  assert.equal(firstVendorBatch,3,'All three ordered vendor scripts download concurrently');
  assert.equal(startupScripts.filter(url=>/\/elfinder-widgets-[^/]+\.js$/.test(url)).length,1,'All dependent widgets load in one bundle');
  assert.ok(!startupScripts.some(url=>/\/(version|widget|draggable|resizable|sortable)-[^/]+\.js$/.test(url)),'No serial per-widget chunk requests');
  await page.unroute('**/vendor/elfinder/*.js',coldVendorRoute);
  await page.route('**/__file-driver-audit.js',route=>route.fulfill({contentType:'text/javascript',body:fs.readFileSync(path.join(artifacts,'driver','driver-audit.js'))}))
  await page.evaluate(async node=>{const {createFileManager}=await import('/__file-driver-audit.js');window.fileDriverAudit=await createFileManager(node,()=>{})},node)
  const H=value=>'nd_'+Buffer.from(value).toString('base64url')
  const call=(cmd,data={})=>page.evaluate(({cmd,data})=>window.fileDriverAudit.request({cmd,...data}),{cmd,data})
  // Under one delayed RPC, elFinder's open/tree/ls must share the list.
  const browseRequests=[];let browseActive=0,browsePeak=0;
  const browseRoute=async route=>{
   const request=route.request();if(!['files.list','files.stat'].some(name=>new URL(request.url()).pathname===new URL(endpoint(name)).pathname))return route.continue();
   browseRequests.push(request.url());browseActive++;browsePeak=Math.max(browsePeak,browseActive);
   try{await new Promise(r=>setTimeout(r,75));await route.fulfill({response:await route.fetch()})}finally{browseActive--}
  };
  await page.route('**/api/v1/**',browseRoute);
  await page.evaluate(async target=>{await Promise.all(['open','tree','ls'].map(cmd=>window.fileDriverAudit.request({cmd,target})))},H('/many'));
  assert.equal(browseRequests.filter(url=>new URL(url).pathname===new URL(endpoint('files.list')).pathname).length,3,'205 entries require exactly three shared pages');
  assert.ok(browsePeak<=2,'Browsing reserves transfer capacity');
  browseRequests.length=0;await call('parents',{target:H('/many')});assert.equal(browseRequests.length,0,'Fresh parent directory metadata is reused');
  fs.writeFileSync(path.join(files,'many','fresh.txt'),'new listing');
  const refreshed=await call('open',{target:H('/many')});assert.ok(refreshed.files.some(file=>file.name==='fresh.txt'),'Directory contents are always fetched fresh');
  fs.unlinkSync(path.join(files,'many','fresh.txt'));
  await page.unroute('**/api/v1/**',browseRoute);
  const upload=files=>page.evaluate(async files=>{const items=files.map(f=>new File([(f.bytes?new Uint8Array(f.bytes):new Uint8Array(f.size??0).fill(7))],f.name,{type:'application/octet-stream'}));return await window.fileDriverAudit.transport().upload({target:window.fileDriverAudit.hash('/'),files:items})},files)
  await call('rename',{target:H('/hello.txt'),name:'driver-renamed.txt'})
  await assert.rejects(call('paste',{dst:H('/missing'),targets:[H('/driver-renamed.txt')],cut:0}));assert.ok(fs.existsSync(path.join(files,'driver-renamed.txt')))
  await call('mkdir',{target:H('/'),name:'driver-target'})
  await call('paste',{dst:H('/driver-target'),targets:[H('/driver-renamed.txt'),H('/private-dir')],cut:0})
  assert.equal(fs.statSync(path.join(files,'driver-target/private-dir')).mode&0777,0700)
  await call('paste',{dst:H('/out'),targets:[H('/driver-renamed.txt')],cut:1})
  assert.equal(fs.readFileSync(path.join(files,'driver-target/driver-renamed.txt'),'utf8'),'edited fixture\n')
  for(const extension of ['zip','tar','tar.gz','tgz']){
   await call('archive',{target:H('/'),name:'format.'+extension,targets:[H('/driver-target')],type:extension==='zip'?'application/zip':extension==='tar'?'application/x-tar':'application/x-gzip'})
   const archiveDownload=await context.request.get(endpoint('files.download',{path:'/format.'+extension}));assert.equal(archiveDownload.status(),200);const archiveBytes=await archiveDownload.body();assert.equal(extension==='zip'?archiveBytes.subarray(0,2).toString():extension==='tar'?archiveBytes.subarray(257,262).toString():archiveBytes.subarray(0,2).toString('hex'),extension==='zip'?'PK':extension==='tar'?'ustar':'1f8b')
   await call('extract',{target:H('/format.'+extension),makedir:1})
   assert.equal(fs.statSync(path.join(files,'format/driver-target/private-dir')).mode&0777,0700)
   await call('rm',{targets:[H('/format')]})
  }
  await call('extract',{target:H('/bzip.tar.bz2'),makedir:1})
  assert.equal((await call('get',{target:H('/bzip/bzip.txt')})).content,'bzip fixture')
  await call('rm',{targets:[H('/driver-target/driver-renamed.txt')]});assert.ok(!fs.existsSync(path.join(files,'driver-target/driver-renamed.txt')))
  await assert.rejects(call('rename',{target:H('/out/driver-renamed.txt'),name:'hello.txt'}))
  await call('paste',{dst:H('/'),targets:[H('/out/driver-renamed.txt')],cut:1});await call('rename',{target:H('/driver-renamed.txt'),name:'hello.txt'})
  assert.equal((await call('open',{target:H('/many')})).files.filter(f=>f.mime!=='directory').length,205)
  assert.equal((await call('search',{target:H('/'),q:'entry-204'})).files.length,1)
  await assert.rejects(call('put',{target:H('/out/hello.txt'),content:'unopened editor'}))
  const original=await call('get',{target:H('/hello.txt')})
  const external=await(await context.request.get(endpoint('files.text',{path:'/hello.txt'}))).json()
  await action({action:'save_text',path:'/hello.txt',content:'changed outside editor',expectedVersion:external.version})
  await call('open',{target:H('/')});await assert.rejects(call('put',{target:H('/hello.txt'),content:'must not overwrite'}))
  assert.equal(fs.readFileSync(path.join(files,'hello.txt'),'utf8'),'changed outside editor')
  await call('get',{target:H('/hello.txt')});await call('put',{target:H('/hello.txt'),content:original.content})
  let replaceAfterSave=true
  await page.route('**/api/v1/nodes/*/files/text?*',async route=>{if(replaceAfterSave&&new URL(route.request().url()).searchParams.get('path')==='/hello.txt'){replaceAfterSave=false;fs.writeFileSync(path.join(files,'hello.txt'),'replacement after save')};await route.continue()})
  await assert.rejects(call('put',{target:H('/hello.txt'),content:'first saved text'}));await assert.rejects(call('put',{target:H('/hello.txt'),content:'must not overwrite unseen replacement'}));assert.equal(fs.readFileSync(path.join(files,'hello.txt'),'utf8'),'replacement after save')
  await page.unroute('**/api/v1/nodes/*/files/text?*');await call('get',{target:H('/hello.txt')});await call('put',{target:H('/hello.txt'),content:original.content})
  const editLimit=operation('files.action').bodySchema.properties.content.maxBytes,largeText=('editable fixture line\n'.repeat(Math.ceil(editLimit/22))).slice(0,editLimit)
  await call('mkfile',{target:H('/'),name:'large-editor.txt'});await call('put',{target:H('/large-editor.txt'),content:largeText});assert.equal((await call('get',{target:H('/large-editor.txt')})).content.length,editLimit);await assert.rejects(call('put',{target:H('/large-editor.txt'),content:largeText+'!'}))
  fs.writeFileSync(path.join(files,'escaped-editor.txt'),'<'.repeat(editLimit))
  const cli=path.join(dir,'cli-session'),flags=['--server',base,'--dev','--state-dir',cli]
  execFileSync(path.join(root,'.build/nodedance'),['auth','login','--body','-',...flags],{input:JSON.stringify(credentials),stdio:['pipe','pipe','pipe']})
  const cliRead=JSON.parse(execFileSync(path.join(root,'.build/nodedance'),['call','files.text','--params',JSON.stringify({nodeId:node}),'--query',JSON.stringify({path:'/escaped-editor.txt'}),...flags],{encoding:'utf8',maxBuffer:64*1024*1024}));assert.equal(cliRead.data.text.length,editLimit)
  const uploaded=await upload([{name:'hello.txt',bytes:[1]},{name:'after-conflict.txt',bytes:[65,66,67]},{name:'large-upload.bin',size:11*1024*1024}]);assert.equal(uploaded.added.length,2);assert.ok(uploaded.warning.length);assert.equal(fs.readFileSync(path.join(files,'hello.txt'),'utf8'),original.content);assert.equal(fs.statSync(path.join(files,'large-upload.bin')).size,11*1024*1024)
  await page.evaluate(async()=>{const f=new File(['directory upload fixture'],'child.txt');Object.defineProperty(f,'webkitRelativePath',{value:'upload-folder/child.txt'});await window.fileDriverAudit.transport().upload({target:window.fileDriverAudit.hash('/'),files:[f]})});assert.equal(fs.readFileSync(path.join(files,'upload-folder/child.txt'),'utf8'),'directory upload fixture')
  await page.evaluate(async()=>{await window.fileDriverAudit.request({cmd:'open',target:window.fileDriverAudit.hash('/')},true);await window.fileDriverAudit.request({cmd:'mkdir',target:window.fileDriverAudit.hash('/'),name:'directory-only'},true)})
  await assert.rejects(page.evaluate(()=>window.fileDriverAudit.request({cmd:'rm',targets:[window.fileDriverAudit.hash('/many')]},true)));assert.ok(fs.existsSync(path.join(files,'many')))
  // Image editing must retain the version read before the editor opened.
  const image = await call('get',{target:H('/image.png')});assert.match(image.content,/^data:image\/png;base64,/)
  const imageBefore=await(await context.request.get(endpoint('files.stat',{path:'/image.png'}))).json()
  fs.appendFileSync(path.join(files,'image.png'),Buffer.from([0]))
  await assert.rejects(call('put',{target:H('/image.png'),content:image.content}))
  await call('get',{target:H('/image.png')});await call('put',{target:H('/image.png'),content:image.content})
  assert.ok((await(await context.request.get(endpoint('files.stat',{path:'/image.png'}))).json()).version!==imageBefore.version)
  await assert.rejects(call('put',{target:H('/image.png'),content:image.content}))
  const filtered=await context.request.get(endpoint('files.search',{path:'/',minSize:String(10*1024*1024),recursive:'true'}));assert.equal(filtered.status(),200);const largeEntries=(await filtered.json()).entries;assert.ok(largeEntries.some(e=>e.name==='large-upload.bin'));assert.ok(largeEntries.every(e=>e.kind==='directory'||e.size>=10*1024*1024))
  await page.evaluate(()=>window.fileDriverAudit.dispose())
  page.on('requestfailed',request=>console.error('File audit request failed',new URL(request.url()).pathname,request.failure()));
  page.on('response',async response=>{if(response.status()>=400&&new URL(response.url()).pathname.includes('/files'))console.error('File audit HTTP error',response.status(),response.url(),(await response.text()).slice(0,500))});
  await page.reload();const finder=page.locator('.nodedance-elfinder');await finder.locator('.elfinder-cwd-filename').filter({hasText:'hello.txt'}).first().waitFor()
  async function eventually(test){for(let i=0;i<200;i++){if(test())return;await page.waitForTimeout(25)}assert.ok(test())}
  const toolbar=name=>finder.locator('.elfinder-button').filter({has:page.locator('.elfinder-button-icon-'+name)})
  async function menu(name){await toolbar(name).waitFor();await page.waitForFunction(()=>!document.querySelector('.nodedance-elfinder .elfinder-cwd-file-tmp')&&!document.querySelector('.nodedance-elfinder .elfinder-notify:has(.elfinder-notify-msg)'));await toolbar(name).click();try{await finder.locator('.elfinder-button-'+name+'-menu.elfinder-frontmost').waitFor()}catch(error){console.error('Menu debug',await finder.evaluate((el,name)=>({button:el.querySelector('.elfinder-button-icon-'+name)?.parentElement.outerHTML,menus:[...el.querySelectorAll('.elfinder-button-menu')].map(e=>({class:e.className,style:e.getAttribute('style'),bounds:e.getBoundingClientRect().toJSON()}))}),name));throw error}}
  const item=name=>finder.locator('.elfinder-cwd-filename').filter({hasText:new RegExp('^'+name.replace(/[.*+?^${}()|[\]\\]/g,'\\$&')+'$')}).first()
  const inline=finder.locator('.elfinder-cwd-file input:visible,.elfinder-cwd-file textarea:visible')
  const longItem=item(longName);
  await longItem.waitFor();
  assert.equal(await longItem.locator('.ui-path-extension').innerText(),'.tar.gz','Native names retain compound extensions');
  await longItem.click();await toolbar('info').click();
  const info=finder.locator('.elfinder-dialog:visible').filter({has:page.locator('.elfinder-info-path')});
  await info.waitFor();
  await context.grantPermissions(['clipboard-read','clipboard-write']);
  await info.getByRole('button',{name:'Copy full path',exact:true}).click();
  assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),'/'+longName,'Info copies exact original full path');
  for(const theme of ['light','dark'])for(const width of [390,1440]){
   await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);
   await page.waitForTimeout(200);
   const infoBounds=await info.boundingBox();assert.ok(infoBounds.x>=0&&infoBounds.x+infoBounds.width<=width+1,'Native information fits the viewport');
   assert.ok(await info.evaluate(el=>el.scrollWidth<=el.clientWidth+2),'Native information wraps the complete long path inside its dialog');

  }
  await info.locator('.ui-dialog-titlebar-close').click();
  await page.setViewportSize({width:1440,height:1000});
  let releaseRefresh, startedRefresh;const refreshHeld=new Promise(resolve=>releaseRefresh=resolve),refreshStarted=new Promise(resolve=>startedRefresh=resolve);
  const holdRefresh=async route=>{startedRefresh();await refreshHeld;await route.continue()};
  const refreshPattern=endpoint('files.list')+'?*';
  await page.route(refreshPattern,holdRefresh);
  await finder.evaluate(el=>{void window.jQuery(el).elfinder('instance').exec('reload')});await Promise.race([refreshStarted,new Promise((_,reject)=>setTimeout(()=>reject(Error('Refresh did not use the discovered file-list route')),10000))]);
  await finder.locator('.elfinder-cwd-filename').filter({hasText:'hello.txt'}).first().waitFor({state:'visible'});
  await page.locator('.ui-load-state[data-phase=refresh]').waitFor();
  releaseRefresh();await page.unroute(refreshPattern,holdRefresh);
  await page.getByTestId('file-loading').waitFor({state:'detached'});

  await toolbar('mkdir').click();await inline.fill('browser-dir');await inline.press('Enter');await item('browser-dir').waitFor();await eventually(()=>fs.existsSync(path.join(files,'browser-dir')));assert.ok(fs.statSync(path.join(files,'browser-dir')).isDirectory())
  await page.waitForTimeout(250);await menu('mkfile');await finder.getByText('TXT: Plain text',{exact:true}).click();await inline.fill('browser.txt');await inline.press('Enter');await item('browser.txt').waitFor();await eventually(()=>fs.existsSync(path.join(files,'browser.txt')))
  await item('browser.txt').dblclick();const editor=finder.locator('.elfinder-to-editing');await editor.waitFor();await editor.locator('textarea:visible').fill('browser edited fixture');await editor.locator('button').filter({hasText:/^Save$/}).click()
  await page.waitForFunction(()=>!document.querySelector('.elfinder-to-editing .elfinder-button-icon-spinner'))
  for(let i=0;i<100&&fs.readFileSync(path.join(files,'browser.txt'),'utf8')!=='browser edited fixture';i++)await page.waitForTimeout(25)
  assert.equal(fs.readFileSync(path.join(files,'browser.txt'),'utf8'),'browser edited fixture')
  for(const theme of ['light','dark'])for(const width of [390,1440]){
   await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);await page.waitForTimeout(200)
   const bounds=await editor.boundingBox();assert.ok(bounds.x>=0&&bounds.x+bounds.width<=width+1,'Editor fits viewport')
   const colors=await editor.evaluate(el=>({bg:getComputedStyle(el).backgroundColor,fg:getComputedStyle(el).color}));assert.match(colors.bg,/^rgb\(/)

  }
  await editor.locator('.ui-dialog-titlebar-close').click();await editor.waitFor({state:'detached'})
  await page.setViewportSize({width:1440,height:1000});await item('browser.txt').click();await toolbar('edit').click();await editor.locator('textarea:visible').fill('saved-as fixture');await editor.getByRole('button',{name:'Save As',exact:true}).click();await inline.fill('saved-as.txt');await inline.press('Enter');await editor.waitFor({state:'detached'});await eventually(()=>fs.existsSync(path.join(files,'saved-as.txt'))&&fs.readFileSync(path.join(files,'saved-as.txt'),'utf8')==='saved-as fixture');assert.equal(fs.readFileSync(path.join(files,'saved-as.txt'),'utf8'),'saved-as fixture');assert.equal(fs.readFileSync(path.join(files,'browser.txt'),'utf8'),'browser edited fixture');await finder.locator('.elfinder-dialog-edit').waitFor({state:'detached'})
  await item('large-editor.txt').click();await toolbar('edit').click();await editor.locator('textarea:visible').waitFor({timeout:60000});assert.equal((await editor.locator('textarea:visible').inputValue()).length,editLimit);await editor.locator('.ui-dialog-titlebar-close').click();await editor.waitFor({state:'detached'})
  await item('browser.txt').click();await menu('archive');await finder.locator('.elfinder-button-archive-menu .elfinder-button-menu-item').filter({hasText:/^ZIP archive$/}).click();await inline.fill('browser-archive.zip');await inline.press('Enter');await item('browser-archive.zip').waitFor();await eventually(()=>fs.existsSync(path.join(files,'browser-archive.zip')));assert.ok(fs.existsSync(path.join(files,'browser-archive.zip')))
  await item('browser.txt').click();await menu('archive');await finder.locator('.elfinder-button-archive-menu .elfinder-button-menu-item').filter({hasText:/^GZIP archive$/}).click();await inline.fill('browser-gzip.tar.gz');await inline.press('Enter');await item('browser-gzip.tar.gz').waitFor();await eventually(()=>fs.existsSync(path.join(files,'browser-gzip.tar.gz')));const gz=await context.request.get(endpoint('files.download',{path:'/browser-gzip.tar.gz'}));assert.equal(gz.status(),200);assert.equal((await gz.body()).subarray(0,2).toString('hex'),'1f8b')
  await item('browser.txt').click();await toolbar('rm').click();const confirm=finder.locator('.elfinder-dialog-confirm');await confirm.waitFor();await confirm.locator('button').filter({hasText:/^(Remove|Delete)$/}).click();await item('browser.txt').waitFor({state:'detached'});await eventually(()=>!fs.existsSync(path.join(files,'browser.txt')));assert.ok(!fs.existsSync(path.join(files,'browser.txt')))
  await item('browser-archive.zip').click();await menu('extract');await finder.getByText('Here',{exact:true}).filter({visible:true}).click();await finder.locator('.elfinder-dialog-confirm').getByRole('button',{name:'Yes',exact:true}).click();await item('browser.txt').waitFor();await eventually(()=>fs.existsSync(path.join(files,'browser.txt')));assert.equal(fs.readFileSync(path.join(files,'browser.txt'),'utf8'),'browser edited fixture');await page.waitForFunction(()=>{const fm=window.jQuery(document.querySelector('.nodedance-elfinder')).elfinder('instance'),selected=fm.selected();return selected.length===1&&fm.file(selected[0]).name==='browser.txt'});await confirm.waitFor({state:'detached'});
  await toolbar('fullscreen').click();await page.waitForFunction(()=>document.querySelector('.nodedance-elfinder')?.classList.contains('elfinder-fullscreen'));assert.ok(await finder.evaluate(el=>el.classList.contains('elfinder-fullscreen')));await toolbar('fullscreen').click();await page.waitForFunction(()=>!document.querySelector('.nodedance-elfinder')?.classList.contains('elfinder-fullscreen'));assert.ok(!await finder.evaluate(el=>el.classList.contains('elfinder-fullscreen')))
  await item('image.png').click();await toolbar('edit').click();await finder.locator('.tui-image-editor-canvas-container').waitFor();fs.writeFileSync(path.join(artifacts,'image-theme.json'),JSON.stringify(await finder.locator('.tui-image-editor-container').evaluate(el=>({html:el.outerHTML.slice(0,5000),icons:[...el.querySelectorAll('svg use')].slice(0,8).map(e=>({class:e.getAttribute('class'),fill:getComputedStyle(e).fill,href:e.getAttribute('xlink:href')}))}))));await finder.locator('.elfinder-to-editing .ui-dialog-titlebar-close').click();await finder.locator('.elfinder-to-editing').waitFor({state:'detached'})
  const download=page.waitForEvent('download');await item('hello.txt').click();await toolbar('download').click();const attachment=await download;assert.equal(attachment.suggestedFilename(),'hello.txt')
  await toolbar('upload').click();await finder.locator('input[type=file]:not([webkitdirectory])').first().setInputFiles({name:'browser-upload.txt',mimeType:'text/plain',buffer:Buffer.from('browser upload fixture')});await item('browser-upload.txt').waitFor();assert.equal(fs.readFileSync(path.join(files,'browser-upload.txt'),'utf8'),'browser upload fixture')
  await page.keyboard.press('Escape');await page.waitForFunction(()=>!document.querySelector('.nodedance-elfinder .elfinder-dialog:not([style*="display: none"])'))
  await item('many').dblclick();await item('entry-000.txt').waitFor();await finder.locator('.elfinder-cwd-wrapper').evaluate(el=>el.scrollTop=el.scrollHeight);await item('entry-204.txt').waitFor()
  await page.goBack();await item('hello.txt').waitFor();await page.goForward();await item('entry-000.txt').waitFor();await page.reload();await item('entry-000.txt').waitFor()
  await setBrowserLanguage(page,'zh-CN');await finder.locator('.elfinder-button').filter({has:page.locator('.elfinder-button-icon-mkdir')}).waitFor();await setBrowserLanguage(page,'en')
  for(const theme of ['light','dark'])for(const width of [390,1440]){await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);await page.waitForTimeout(200);assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));}
  const composeList=operation('compose.list').path.replace('{nodeId}',node);let composeSubmission
  await page.route('**'+composeList,route=>{if(route.request().method()==='POST'){composeSubmission=route.request().postDataJSON();return route.fulfill({status:503,body:'Isolated deployment interception'})};return route.fulfill({json:{projects:[],dataStale:false,serverTime:new Date().toISOString()}})})
  await page.setViewportSize({width:1440,height:1000});await page.goto(base+`/#/nodes?node=${node}&section=compose`);await page.getByTestId('compose-toolbar').getByRole('button',{name:'New project',exact:true}).click();const form=page.getByTestId('compose-create-form');await form.locator('input').first().fill('directory-fixture');await form.locator('textarea').fill('services: {} # preserved draft');await page.getByTestId('compose-directory-open').click()
  const picker=page.getByTestId('compose-directory-picker'),originalURL=page.url();await picker.locator('.elfinder-cwd-filename').filter({hasText:'many'}).first().waitFor();assert.equal(await picker.locator('.elfinder-cwd-filename').filter({hasText:'hello.txt'}).count(),0)
  const pickerToolbar=name=>picker.locator('.elfinder-button').filter({has:page.locator('.elfinder-button-icon-'+name)})
  for(const name of ['mkfile','upload','rm','rename','edit','archive','extract'])assert.equal(await pickerToolbar(name).count(),0)
  await pickerToolbar('mkdir').click();const field=picker.locator('.elfinder-cwd-file input:visible,.elfinder-cwd-file textarea:visible');await field.fill('compose-workspace');await field.press('Enter');await picker.locator('.elfinder-cwd-filename').filter({hasText:'compose-workspace'}).dblclick();await picker.locator('footer span').filter({hasText:/^\/compose-workspace$/}).waitFor();await page.getByTestId('compose-directory-use').click();await picker.waitFor({state:'detached'});assert.equal(await page.getByTestId('compose-workdir').inputValue(),'/compose-workspace');assert.equal(page.url(),originalURL);assert.equal(await form.locator('textarea').inputValue(),'services: {} # preserved draft')
  await page.getByTestId('compose-directory-open').click();await picker.locator('.elfinder-cwd-filename').filter({hasText:'many'}).waitFor()
  for(const theme of ['light','dark'])for(const width of [390,1440]){await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);await page.waitForTimeout(150);assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));}
  await picker.getByRole('button',{name:'Cancel',exact:true}).click();await picker.waitFor({state:'detached'});assert.equal(await page.getByTestId('compose-workdir').inputValue(),'/compose-workspace');assert.ok(await page.getByTestId('compose-directory-open').evaluate(el=>el===document.activeElement));await form.getByRole('button',{name:'Create and deploy',exact:true}).click();await page.locator('.ui-dialog').getByRole('button',{name:'Confirm',exact:true}).click();await form.getByRole('alert').filter({hasText:'Isolated deployment interception'}).waitFor();assert.equal(composeSubmission.workingDirectory,'/compose-workspace')
  assert.deepEqual(errors,[]);assert.deepEqual(externalRequests,[],'File UI and editors use only local assets')
  console.log('elFinder file audit passed: real isolated Core/root Agent, 205 entries, search, tasks, permissions, archives, editor conflicts, 8 MiB text/CLI, uploads, downloads, stock UI, themes/mobile, navigation, Compose directory selection.')
 }catch(error){if(browser){const p=browser.contexts()[0]?.pages()[0];if(p){fs.writeFileSync(path.join(artifacts,'failure.html'),await p.content());console.error('File browser state:',p.url(),(await p.locator('body').innerText()).slice(-2000));}};throw error}
 finally{if(browser)await browser.close();await stop(agent);await stop(core);execFileSync('sudo',['rm','-rf','--',dir,state])}
})().catch(e=>{console.error(e);process.exit(1)})
