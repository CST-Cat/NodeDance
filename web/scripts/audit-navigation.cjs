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
  await require('./browser-language-regressions.cjs')({browser,base,credentials,artifacts})
  await require('./mobile-navigation-regressions.cjs')({page,base,artifacts});assert.deepEqual(errors,[])
 }finally{if(browser)await browser.close();core.kill('SIGTERM');await new Promise(r=>core.exitCode!==null?r():core.once('exit',r));fs.rmSync(directory,{recursive:true,force:true})}
})().catch(e=>{console.error(e);process.exitCode=1})
