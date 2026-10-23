const {selectValue,selectedValue,readOptions}=require('./arc-controls.cjs')
const {setBrowserLanguage}=require('./browser-language.cjs')
// Isolated real Core and fresh embedded Web. Fixtures only touch this script's
// temporary SQLite database; production resources and real Agents are never used.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),net=require('node:net')
const {spawn,execFileSync}=require('node:child_process'),{chromium}=require('playwright')
const root=path.resolve(__dirname,'../..'),binary=path.join(root,'.build/nodedance'),artifacts=path.join(root,'.build/feed-audit')
fs.mkdirSync(artifacts,{recursive:true})
async function freePort(){const s=net.createServer();await new Promise(r=>s.listen(0,'127.0.0.1',r));const p=s.address().port;await new Promise(r=>s.close(r));return p}
;(async()=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-feed-audit-')),base='http://127.0.0.1:'+await freePort()
 const core=spawn(binary,['serve','--dev','--listen',new URL(base).host,'--data-dir',directory],{cwd:root,stdio:['ignore','pipe','pipe']});let logs='',browser
 core.stdout.on('data',x=>logs+=x);core.stderr.on('data',x=>logs+=x)
 try{
  let ready=false;for(let i=0;i<100;i++){try{ready=(await fetch(base+'/api/v1/health')).ok}catch{}if(ready)break;if(core.exitCode!==null)throw Error(logs);await new Promise(r=>setTimeout(r,100))}assert.ok(ready)
  browser=await chromium.launch({headless:true});const context=await browser.newContext({locale:'en-US'})
  const csrf=await(await context.request.get(base+'/api/v1/auth/csrf')).json(),credentials=JSON.parse(fs.readFileSync(path.join(directory,'initial-admin.json'),'utf8'))
  const login=await context.request.post(base+'/api/v1/auth/login',{headers:{Origin:base,'X-CSRF-Token':csrf.token},data:credentials});assert.equal(login.status(),200);const headers={Origin:base,'X-CSRF-Token':(await login.json()).csrfToken}
  const page=await context.newPage();page.setDefaultTimeout(10000);const errors=[];page.on('pageerror',e=>errors.push(e.message));await page.goto(base+'/#/feeds');const view=page.getByTestId('feeds-center');await view.waitFor()
  const form=view.locator('.feed-form');assert.equal(await selectedValue(form.locator('button[data-dropdown-field][data-field-name=feedInterval]')),'30')
  const choices=await readOptions(form.locator('button[data-dropdown-field][data-field-name=feedInterval]')).then(options=>options.map(option=>Number(option.value)));assert.ok(choices.includes(1)&&choices.includes(30)&&choices.includes(1440))
  const malicious='<img src=/rss-beacon onerror=window.__rssXss=1>'
  await form.locator('[name=feedName]').fill(malicious);await form.locator('[name=feedUrl]').fill('http://127.0.0.1/feed');await selectValue(form.locator('button[data-dropdown-field][data-field-name=feedInterval]'),'1');await form.getByRole('checkbox').first().uncheck()
  let response=page.waitForResponse(r=>r.url()===base+'/api/v1/feeds'&&r.request().method()==='POST');await form.getByRole('button',{name:'Save',exact:true}).click();const created=await response;assert.equal(created.status(),200);let subscription=await created.json();assert.equal(subscription.intervalMinutes,1)
  await view.locator('.feed-select').filter({hasText:malicious}).waitFor()
  // Seed untrusted strings directly into the isolated store to verify the Web
  // rendering boundary independently of the parser's own plain-text conversion.
  execFileSync('python3',['-c',`import sqlite3,sys,time,hashlib
c=sqlite3.connect(sys.argv[1]);feed=sys.argv[2];now=int(time.time())
for i in range(55):
 ident=hashlib.sha256(str(i).encode()).hexdigest()
 c.execute('INSERT INTO feed_items(id,feed_id,title,summary,url,first_seen,last_seen) VALUES(?,?,?,?,?,?,?)',(ident,feed,'Entry '+str(i),sys.argv[3],'https://example.org/article/'+str(i),now-i,now))
c.commit();c.close()`,path.join(directory,'nodedance.sqlite'),subscription.id,malicious])
  await assertEventually(async()=>await view.locator('.feed-entry h3').count()===50)
  assert.equal(await view.locator('img,script,iframe').count(),0);assert.equal(await page.evaluate(()=>window.__rssXss||0),0)
  await view.getByRole('button',{name:'Next',exact:true}).click();await assertEventually(async()=>await view.locator('.feed-entry h3').count()===5)
  await view.getByRole('button',{name:'Previous',exact:true}).click();await assertEventually(async()=>await view.locator('.feed-entry h3').count()===50)
  await view.getByRole('button',{name:'Mark read',exact:true}).first().click();await view.getByRole('button',{name:'Mark unread',exact:true}).first().waitFor()
  await view.getByRole('checkbox',{name:'Unread only',exact:true}).check();await assertEventually(async()=>await view.locator('.feed-entry h3').count()===50)
  await view.getByRole('searchbox',{name:'Search',exact:true}).fill('Entry 54');await view.getByRole('button',{name:'Search',exact:true}).click();await assertEventually(async()=>await view.locator('.feed-entry h3').count()===1)
  await view.getByRole('searchbox').fill('');await view.getByRole('button',{name:'Search',exact:true}).click()
  // A real Core fetch must reject loopback before connecting to port 80.
  await view.getByRole('button',{name:'Enable',exact:true}).click();await assertEventually(async()=>{const r=await context.request.get(base+'/api/v1/feeds/'+subscription.id);subscription=await r.json();return subscription.status==='failed'})
  await view.getByRole('button',{name:'Refresh',exact:true}).click();await view.getByRole('button',{name:'Cancel refresh',exact:true}).waitFor();await view.getByRole('button',{name:'Cancel refresh',exact:true}).click();await assertEventually(async()=>{const r=await context.request.get(base+'/api/v1/feeds/'+subscription.id);return (await r.json()).status==='canceled'})
  await view.getByRole('button',{name:'Edit',exact:true}).click();await form.locator('[name=feedCustomInterval]').fill('7');await form.getByRole('button',{name:'Save',exact:true}).click();await assertEventually(async()=>{const r=await context.request.get(base+'/api/v1/feeds/'+subscription.id);return (await r.json()).intervalMinutes===7})
  const layouts=[]
  for(const language of ['en','zh-CN'])for(const theme of ['light','dark'])for(const mobile of [false,true]){
   await setBrowserLanguage(page,language)
   const appearance=await(await context.request.get(base+'/api/v1/public/appearance')).json();const r=await context.request.put(base+'/api/v1/settings/appearance',{headers,data:{displayName:appearance.displayName,backgroundColor:appearance.backgroundColor,theme}});assert.equal(r.status(),200)
   await page.setViewportSize({width:mobile?390:1440,height:mobile?844:1000});await page.reload();await view.waitFor();await assertEventually(async()=>await page.locator('.app-shell').getAttribute('data-theme')===theme)
   const overflow=await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1);assert.equal(overflow,false,'Horizontal overflow')
   assert.equal(await page.locator('html').getAttribute('lang'),language);const name=language+'-'+theme+'-'+(mobile?'mobile':'desktop');await page.screenshot({path:path.join(artifacts,name+'.png'),fullPage:true});layouts.push(name)
  }
  await setBrowserLanguage(page,'en');await page.reload();await view.waitFor()
  await view.getByRole('checkbox',{name:'Confirm deletion of this subscription and its entries',exact:true}).check();await view.getByRole('button',{name:'Delete',exact:true}).click();await assertEventually(async()=>(await context.request.get(base+'/api/v1/feeds/'+subscription.id)).status()===404)
  assert.deepEqual(errors,[]);fs.writeFileSync(path.join(artifacts,'report.json'),JSON.stringify({layouts,checks:['defaults','intervals','create','stored-XSS','pagination','search','shared-read-state','SSRF','refresh-cancel','custom-interval','delete'],pageErrors:errors},null,2));console.log('FEEDS AUDIT PASSED',JSON.stringify({layouts,checks:11}))
 }finally{if(browser)await browser.close();if(core.exitCode===null){core.kill('SIGTERM');await new Promise(r=>core.once('exit',r))};fs.rmSync(directory,{recursive:true,force:true})}
})().catch(e=>{console.error(e);process.exitCode=1})
async function assertEventually(check){for(let i=0;i<100;i++){if(await check())return;await new Promise(r=>setTimeout(r,100))}throw Error('Timed out waiting for feed behavior')}
