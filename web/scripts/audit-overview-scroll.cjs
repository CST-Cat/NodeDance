// Run after make build. Temporary real Core and protocol-only Agent fixtures;
// no real Agent process, host commands, filesystem fixtures or Docker controls.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),net=require('node:net')
const {spawn}=require('node:child_process'),{chromium}=require('playwright')
const root=path.resolve(__dirname,'../..'),directory=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-overview-scroll-')),artifacts=path.join(root,'.build/overview-scroll-audit')
;(async()=>{
 const listener=net.createServer();await new Promise(r=>listener.listen(0,'127.0.0.1',r));const port=listener.address().port;await new Promise(r=>listener.close(r))
 const base=`http://127.0.0.1:${port}`,core=spawn(path.join(root,'.build/nodedance'),['serve','--dev','--listen',`127.0.0.1:${port}`,'--data-dir',directory],{stdio:['ignore','pipe','pipe']})
 let logs='',browser,fixture;core.stdout.on('data',x=>logs+=x);core.stderr.on('data',x=>logs+=x)
 try{
  let ready=false;for(let i=0;i<100;i++){if(core.exitCode!==null)throw Error(logs);try{ready=(await fetch(base+'/api/v1/health')).ok}catch{}if(ready)break;await new Promise(r=>setTimeout(r,100))}assert.ok(ready)
  browser=await chromium.launch({headless:true});const context=await browser.newContext({locale:'en-US'});const csrf=await(await context.request.get(base+'/api/v1/auth/csrf')).json(),credentials=JSON.parse(fs.readFileSync(path.join(directory,'initial-admin.json'),'utf8'))
  const login=await context.request.post(base+'/api/v1/auth/login',{headers:{Origin:base,'X-CSRF-Token':csrf.token},data:credentials});assert.equal(login.status(),200)
  const page=await context.newPage();page.setDefaultTimeout(10000);const errors=[];page.on('pageerror',e=>errors.push(e.message));fs.mkdirSync(artifacts,{recursive:true})
  const regression=require('./overview-scroll-regressions.cjs')
  await regression({page,base,artifacts})
  fixture=await require('./overview-motion-fixture.cjs')({context,base})
  await page.emulateMedia({reducedMotion:'no-preference'});await page.setViewportSize({width:393,height:852})
  await page.goto(base+'/#/nodes');await page.reload();await page.locator('.vps-card').first().waitFor();await page.locator('.resource-gauge').first().waitFor()
  for(const theme of ['light','dark']) {
   await page.evaluate(theme=>document.querySelector('.app-shell').dataset.theme=theme,theme);await page.waitForTimeout(350)
   await regression.checkStableScroll(page)
   const gauges=await page.locator('.resource-gauge').evaluateAll(items=>items.map(el=>({shadow:getComputedStyle(el).boxShadow,border:getComputedStyle(el).borderTopWidth,outline:getComputedStyle(el).outlineStyle})))
   assert.ok(gauges.length>=4,'Real metric fixtures render multiple gauges')
   for(const gauge of gauges){assert.equal(gauge.shadow,'none','No Tailwind ring square around percentages');assert.equal(gauge.border,'0px');assert.equal(gauge.outline,'none')}
   await page.screenshot({path:path.join(artifacts,`populated-${theme}-393.png`),fullPage:true})
  }
  assert.deepEqual(fixture.errors,[]);assert.deepEqual(errors,[])
 }finally{if(fixture)await fixture.close();if(browser)await browser.close();core.kill('SIGTERM');await new Promise(r=>core.exitCode!==null?r():core.once('exit',r));fs.rmSync(directory,{recursive:true,force:true})}
})().catch(e=>{console.error(e);process.exitCode=1})
