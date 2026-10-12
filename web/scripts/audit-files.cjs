// Isolated real Core + root Agent + Chromium. Only disposable file fixtures
// are modified. No systemd installation, host commands or Docker mutations.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),net=require('node:net'),crypto=require('node:crypto')
const {spawn,execFileSync}=require('node:child_process'),{chromium}=require('playwright')
const root=path.resolve(__dirname,'../..'),artifacts=path.join(root,'.build/file-audit')
fs.mkdirSync(artifacts,{recursive:true})
for(const [name,pkg]of[['core','./cmd/nodedance'],['agent','./cmd/nodedance-agent']])execFileSync('go',['build','-o',path.join(artifacts,name),pkg],{cwd:root,stdio:'inherit'})
async function port(){const s=net.createServer();await new Promise(r=>s.listen(0,'127.0.0.1',r));const value=s.address().port;await new Promise(r=>s.close(r));return value}
async function stop(p){if(!p||p.exitCode!==null)return;p.kill('SIGTERM');await Promise.race([new Promise(r=>p.once('exit',r)),new Promise(r=>setTimeout(r,5000))]);if(p.exitCode===null)p.kill('SIGKILL')}
;(async()=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-file-audit-')),data=path.join(dir,'core'),files=path.join(dir,'files'),state=execFileSync('sudo',['mktemp','-d','/run/nodedance-file-audit-XXXXXX'],{encoding:'utf8'}).trim(),base=`http://127.0.0.1:${await port()}`
 fs.mkdirSync(files);fs.mkdirSync(path.join(files,'many'));fs.mkdirSync(path.join(files,'out'))
 fs.writeFileSync(path.join(files,'hello.txt'),'hello fixture\n');fs.writeFileSync(path.join(files,'image.png'),Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aZMsAAAAASUVORK5CYII=','base64'))
 for(let i=0;i<205;i++)fs.writeFileSync(path.join(files,'many',`entry-${String(i).padStart(3,'0')}.txt`),'fixture')
 fs.symlinkSync('hello.txt',path.join(files,'link'))
 const core=spawn(path.join(artifacts,'core'),['serve','--dev','--listen',new URL(base).host,'--data-dir',data],{cwd:root,stdio:['ignore','pipe','pipe']});let logs='',browser,agent
 core.stdout.on('data',v=>logs+=v);core.stderr.on('data',v=>logs+=v)
 try{
  for(let i=0;i<100;i++){try{if((await fetch(base+'/api/v1/health')).ok)break}catch{};await new Promise(r=>setTimeout(r,100))}
  browser=await chromium.launch({headless:true});const context=await browser.newContext();await context.addInitScript(()=>localStorage.setItem('nodedance.language','en'))
  const csrf=await(await context.request.get(base+'/api/v1/auth/csrf')).json(),credentials=JSON.parse(fs.readFileSync(path.join(data,'initial-admin.json')))
  const login=await context.request.post(base+'/api/v1/auth/login',{headers:{Origin:base,'X-CSRF-Token':csrf.token},data:credentials});assert.equal(login.status(),200)
  const headers={Origin:base,'X-CSRF-Token':(await login.json()).csrfToken}
  const catalog=(await(await context.request.get(base+'/api/v1/operations')).json()).operations
  const operation=name=>{const o=catalog.find(o=>o.name===name);assert.ok(o,`Missing ${name}`);return o}
  const enrollmentResponse=await context.request.post(base+'/api/v1/agents/enrollments',{headers,data:{displayName:'Disposable file fixture',coreUrl:base}});assert.equal(enrollmentResponse.status(),201)
  const enrollment=await enrollmentResponse.json(),node=enrollment.nodeId
  execFileSync('sudo',['install','-d','-m','0700',state])
  execFileSync('sudo',[path.join(artifacts,'agent'),'enroll','--server',base,'--dev','--config',path.join(state,'agent.json'),'--token-stdin'],{input:enrollment.token+'\n',stdio:['pipe','pipe','pipe']})
  agent=spawn('sudo',['env',`NODEDANCE_AGENT_FILE_ROOT=${files}`,'DOCKER_HOST=unix:///nonexistent/nodedance-file-fixture.sock',path.join(artifacts,'agent'),'run','--config',path.join(state,'agent.json')],{stdio:['ignore','pipe','pipe']});agent.stderr.on('data',v=>logs+=v)
  const endpoint=(name,query={})=>{const o=operation(name);return base+o.path.replace('{nodeId}',node)+(Object.keys(query).length?'?'+new URLSearchParams(query):'')}
  let online=false;for(let i=0;i<200;i++){const response=await context.request.get(endpoint('files.list',{path:'/'}));if(response.ok()){online=true;break};await new Promise(r=>setTimeout(r,100))};assert.ok(online,logs)
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
  const preview=await context.request.get(endpoint('files.preview',{path:'/image.png'}),{headers:{Range:'bytes=0-7'}});assert.equal(preview.status(),206);assert.ok(preview.headers()['content-security-policy'].includes('sandbox'))
  const visitor=await browser.newContext();assert.equal((await visitor.request.get(endpoint('files.preview',{path:'/image.png'}))).status(),401)
  const page=await context.newPage();page.setDefaultTimeout(15000);const errors=[];page.on('pageerror',e=>errors.push(e.message))
  await page.goto(base+`/#/nodes?node=${node}&section=files`)
  await page.locator('.vuefinder').waitFor();await page.getByText('hello.txt',{exact:true}).first().waitFor()
  if(process.env.NODEDANCE_FILES_DEBUG)console.log('CONTROLS',await page.locator('.node-files [title]').evaluateAll(items=>items.map(e=>({text:e.textContent,title:e.title,tag:e.tagName,role:e.getAttribute('role')}))))
  await page.getByRole('button',{name:'New Folder',exact:true}).focus();await page.keyboard.press('Enter')
  await page.locator('.vuefinder__new-folder-modal__input').fill('browser-dir')
  await page.locator('.vuefinder__modal-layout__footer').getByRole('button',{name:'Create',exact:true}).click()
  await page.getByText('browser-dir',{exact:true}).first().waitFor()
  assert.ok(fs.statSync(path.join(files,'browser-dir')).isDirectory())
  const empty=await context.request.get(endpoint('files.list',{path:'/browser-dir'}));assert.deepEqual((await empty.json()).entries,[])
  // Exercise the actual VueFinder create/edit/upload dialogs, not just APIs.
  await page.locator('.node-files [title="New File"]').click()
  await page.locator('.vuefinder__new-file-modal__input').fill('browser.json')
  await page.locator('.vuefinder__modal-layout__footer').getByRole('button',{name:'Create',exact:true}).click()
  await page.getByText('browser.json',{exact:true}).first().waitFor()
  assert.equal(fs.readFileSync(path.join(files,'browser.json'),'utf8'),'')
  const previewVersion=await(await context.request.get(endpoint('files.text',{path:'/browser.json'}))).json()
  await action({action:'save_text',path:'/browser.json',content:JSON.stringify({server:'example.test',enabled:true,notes:'Disposable preview fixture'},null,2),expectedVersion:previewVersion.version})
  await page.getByText('browser.json',{exact:true}).first().locator('xpath=ancestor::div[contains(@class,"vf-explorer-item")][1]').dblclick()
  await page.locator('.cm-content').waitFor()
  async function checkFileDialog(mode){
   for(const theme of ['light','dark'])for(const width of [390,1440]){
    await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);await page.waitForTimeout(300)
    const colors=await page.locator('.vuefinder__modal-layout').evaluate(el=>{
     const selectors=['.vuefinder__modal-layout__body','.vuefinder__modal-layout__content','.cm-editor','.cm-scroller','.cm-content','.cm-gutters']
     return selectors.map(selector=>{const item=el.querySelector(selector),style=getComputedStyle(item);return {selector,bg:style.backgroundColor,color:style.color}})
    })
    for(const value of colors)assert.match(value.bg,/^rgb\(/,`${theme}/${mode} ${value.selector} must be opaque: ${value.bg}`)
    const luminance=color=>{const channels=color.match(/[\d.]+/g).slice(0,3).map(v=>{const n=Number(v)/255;return n<=.04045?n/12.92:((n+.055)/1.055)**2.4});return channels[0]*.2126+channels[1]*.7152+channels[2]*.0722}
    for(const value of colors.slice(4)){const bg=luminance(value.bg),fg=luminance(value.color);assert.ok((Math.max(bg,fg)+.05)/(Math.min(bg,fg)+.05)>=4.5,`${theme}/${mode} ${value.selector} text contrast`)}
    const body=await page.locator('.vuefinder__modal-layout__body').boundingBox();assert.ok(body.x>=0&&body.x+body.width<=width+1,'File dialog fits viewport')
    await page.screenshot({path:path.join(artifacts,`file-${mode}-${theme}-${width}.png`),fullPage:true})
   }
  }
  await checkFileDialog('preview')
  await page.getByRole('button',{name:'Edit',exact:true}).click()
  await page.locator('.cm-content[contenteditable=true]').fill('browser edited fixture')
  await checkFileDialog('edit')
  await page.getByRole('button',{name:'Save',exact:true}).click()
  for(let i=0;i<100&&fs.readFileSync(path.join(files,'browser.json'),'utf8')!=='browser edited fixture';i++)await page.waitForTimeout(25)
  assert.equal(fs.readFileSync(path.join(files,'browser.json'),'utf8'),'browser edited fixture')
  await page.getByRole('button',{name:'Edit',exact:true}).waitFor()
  await page.locator('.vuefinder__modal-layout').getByRole('button',{name:'Close',exact:true}).last().click()
  await page.locator('.vuefinder__modal-layout').waitFor({state:'hidden'})
  await page.locator('.node-files [title="Upload"]').click()
  await page.locator('.vuefinder__modal-layout input[type=file]:not([webkitdirectory])').setInputFiles({name:'browser-upload.txt',mimeType:'text/plain',buffer:Buffer.from('browser upload fixture')})
  const uploaded=page.waitForResponse(r=>r.url().includes('/files/upload')&&r.request().method()===operation('files.upload').method)
  await page.locator('.vuefinder__modal-layout__footer').getByRole('button',{name:'Upload',exact:true}).click()
  const uploadResult=await uploaded;assert.equal(uploadResult.status(),200);assert.equal((await uploadResult.json()).status,'succeeded')
  for(let i=0;i<200&&!fs.existsSync(path.join(files,'browser-upload.txt'));i++)await page.waitForTimeout(25)
  assert.equal(fs.readFileSync(path.join(files,'browser-upload.txt'),'utf8'),'browser upload fixture')
  await page.locator('.vuefinder__modal-layout').getByRole('button',{name:'Close',exact:true}).last().click()
  await page.locator('.vuefinder__modal-layout').waitFor({state:'hidden'})
  const folder=path.join(dir,'upload-folder');fs.mkdirSync(folder);fs.writeFileSync(path.join(folder,'child.txt'),'directory upload fixture')
  await page.locator('.node-files [title="Upload"]').click()
  await page.locator('.vuefinder__modal-layout input[webkitdirectory]').setInputFiles(folder)
  const folderUploaded=page.waitForResponse(r=>r.url().includes('/files/upload')&&r.request().method()===operation('files.upload').method)
  await page.locator('.vuefinder__modal-layout__footer').getByRole('button',{name:'Upload',exact:true}).click()
  assert.equal((await folderUploaded).status(),200)
  assert.equal(fs.readFileSync(path.join(files,'upload-folder','child.txt'),'utf8'),'directory upload fixture')
  await page.locator('.vuefinder__modal-layout').getByRole('button',{name:'Close',exact:true}).last().click();await page.locator('.vuefinder__modal-layout').waitFor({state:'hidden'})
  const showLast=async()=>{await page.getByText('entry-000.txt',{exact:true}).first().waitFor();await page.locator('.vuefinder').evaluate(el=>{for(const item of el.querySelectorAll('*'))if(item.clientHeight>0&&item.scrollHeight>item.clientHeight)item.scrollTop=item.scrollHeight});await page.getByText('entry-204.txt',{exact:true}).first().waitFor()}
  await page.getByText('many',{exact:true}).first().dblclick();await showLast()
  await page.goBack();await page.getByText('hello.txt',{exact:true}).first().waitFor()
  await page.goForward();await showLast()
  await page.reload();await showLast()
  for(const theme of ['light','dark'])for(const width of [390,1440]){
   await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);await page.waitForTimeout(300)
   assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'File manager layout overflow');await page.screenshot({path:path.join(artifacts,`files-${theme}-${width}.png`),fullPage:true})
  }
  assert.deepEqual(errors,[])
  console.log('File manager passed: real root Agent operations, pagination, search, copy/move, ZIP, idempotency, edit conflict, passive range preview, actual create/edit/file/folder-upload dialogs, browser navigation and four layouts.')
 }catch(error){if(browser){const p=browser.contexts()[0]?.pages()[0];if(p){console.error('File browser state:',p.url(),(await p.locator('body').innerText()).slice(-2000));await p.screenshot({path:path.join(artifacts,'failure.png'),fullPage:true})}};throw error}
 finally{if(browser)await browser.close();await stop(agent);await stop(core);execFileSync('sudo',['rm','-rf','--',dir,state])}
})().catch(e=>{console.error(e);process.exit(1)})
