const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),net=require('node:net'),{spawn}=require('node:child_process'),{chromium}=require('playwright');
const root=path.resolve(__dirname,'../..'),artifacts=path.join(root,'.build/update-audit');
(async()=>{const dir=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-update-audit-'));let core,browser;try{
 const socket=net.createServer();await new Promise(resolve=>socket.listen(0,'127.0.0.1',resolve));const base='http://127.0.0.1:'+socket.address().port;await new Promise(resolve=>socket.close(resolve));fs.mkdirSync(artifacts,{recursive:true});
 core=spawn(root+'/.build/nodedance',['serve','--dev','--listen',new URL(base).host,'--data-dir',dir],{stdio:'ignore'});
 for(let attempt=0;attempt<100;attempt++){try{if((await fetch(base+'/api/v1/health')).ok)break}catch{}await new Promise(resolve=>setTimeout(resolve,100))}
 browser=await chromium.launch();const context=await browser.newContext({locale:'en-US',reducedMotion:'reduce'}),csrf=(await(await context.request.get(base+'/api/v1/auth/csrf')).json()).token;
 assert.equal((await context.request.post(base+'/api/v1/auth/login',{headers:{Origin:base,'X-CSRF-Token':csrf},data:JSON.parse(fs.readFileSync(dir+'/initial-admin.json'))})).status(),200);
 const page=await context.newPage(),errors=[];page.setDefaultTimeout(10000);page.on('pageerror',error=>errors.push(error.message));await page.setViewportSize({width:1440,height:1000});await page.goto(base+'/#/nodes');await page.getByRole('button',{name:'Updates',exact:true}).waitFor();
 await require('./update-center-regressions.cjs')({page,base,artifacts});assert.deepEqual(errors,[]);
}finally{if(browser)await browser.close();if(core){core.kill('SIGTERM');await new Promise(resolve=>core.exitCode!==null?resolve():core.once('exit',resolve))}fs.rmSync(dir,{recursive:true,force:true})}})().catch(error=>{console.error(error);process.exitCode=1});
