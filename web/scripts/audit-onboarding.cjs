// Real isolated Core; network peers, SSH and remote deployment are protocol-only fixtures.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),net=require('node:net');
const {spawn}=require('node:child_process'),{chromium}=require('playwright');
const assertSettingsSpacing=require('./settings-spacing.cjs');
const root=path.resolve(__dirname,'../..'),out=path.join(root,'.build/onboarding-audit');fs.mkdirSync(out,{recursive:true});
async function freePort(){const s=net.createServer();await new Promise(r=>s.listen(0,'127.0.0.1',r));const p=s.address().port;await new Promise(r=>s.close(r));return p}
(async()=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-onboarding-')),base='http://127.0.0.1:'+await freePort();
 const core=spawn(path.join(root,'.build/nodedance'),['serve','--dev','--listen',new URL(base).host,'--data-dir',dir],{stdio:['ignore','pipe','pipe']});let logs='',browser;
 core.stdout.on('data',x=>logs+=x);core.stderr.on('data',x=>logs+=x);
 try{
  for(let i=0;i<100;i++){try{if((await fetch(base+'/api/v1/health')).ok)break}catch{}await new Promise(r=>setTimeout(r,100))}
  browser=await chromium.launch();const context=await browser.newContext({locale:'en-US',viewport:{width:1440,height:1000},reducedMotion:'reduce'}),page=await context.newPage();page.setDefaultTimeout(10000);
  const errors=[];page.on('pageerror',e=>errors.push(e.message));await page.addInitScript(()=>Object.defineProperty(navigator,'clipboard',{value:{writeText:async value=>{window.onboardingCopied=value}}}));
  const credentials=JSON.parse(fs.readFileSync(path.join(dir,'initial-admin.json'),'utf8')),csrf=(await(await context.request.get(base+'/api/v1/auth/csrf')).json()).token;
  assert.equal((await context.request.post(base+'/api/v1/auth/login',{headers:{Origin:base,'X-CSRF-Token':csrf},data:credentials})).status(),200);
  const catalog=await(await context.request.get(base+'/api/v1/operations')).json();
  const operation=name=>{const found=catalog.operations.find(o=>o.name===name);assert.ok(found,name);return found};
  const peers=['peer-a','peer-b'].map((identity,index)=>({identity,name:identity,os:'linux',class:'linux',online:true,ips:['100.64.0.'+(index+1)],managed:false}));
  await page.route('**'+operation('discovery.peers').path,route=>route.fulfill({json:{peers,discoveredAt:new Date().toISOString()}}));
  await page.goto(base+'/#/agent-enrollment');await page.getByTestId('agent-enrollment').waitFor();await page.locator('.peer-card').first().waitFor();
  assert.equal(await page.getByRole('tablist').count(),0,'Target discovery is part of the form, never a second page tab');
  assert.equal(await page.locator('#workspace-navigation').getByRole('button',{name:'Network discovery',exact:true}).count(),0);
  assert.equal(await page.locator('#workspace-navigation').getByRole('button',{name:'Guest display',exact:true}).count(),0);
  const name=page.locator('input[name=displayName]'),url=page.locator('input[name=coreUrl]');
  await name.fill('Shared connection draft');await url.fill('https://core.example');
  const formBefore=await page.locator('.agent-setup-form').elementHandle();
  await page.locator('.peer-card').first().getByRole('button').click();
  assert.equal(await name.inputValue(),'Shared connection draft');assert.equal(await url.inputValue(),'https://core.example');
  assert.ok(await page.locator('.agent-setup-form').evaluate((el,before)=>el===before,formBefore),'Selecting a target retains the actual shared form DOM');
  assert.equal(await page.locator('input[name=coreUrl]').count(),1);assert.equal(await page.locator('input[name=displayName]').count(),1);
  assert.equal(await page.locator('.setup-assets').count(),0,'Only parameters supported by the selected Core operation are presented');
  await page.getByRole('button',{name:/Install on another host/}).click();
  assert.equal(await name.inputValue(),'Shared connection draft');assert.equal(await url.inputValue(),'https://core.example');await page.locator('.setup-assets').waitFor();
  const layouts=[['manual',()=>page.getByRole('button',{name:/Install on another host/}).click()],['network',()=>page.locator('.peer-card').first().getByRole('button').click()],['settings',async()=>{await page.goto(base+'/#/settings');await page.locator('.account-management').waitFor();await page.locator('.guest-settings [role=checkbox]').first().waitFor();assert.equal(await page.getByRole('tablist').count(),0);for(const selector of ['.profile-settings','.security-settings','.session-settings','.account-management','.guest-settings'])assert.ok(await page.locator(selector).isVisible(),selector+' is visible with the other settings');assert.equal((await page.locator('.session-count').innerText()).trim(),'1 sessions')}]];
  for(const [label,prepare] of layouts){
   await prepare();
   for(const [theme,width] of [['light',1920],['dark',320]]){
    await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);await page.waitForTimeout(100);
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),label+' fits '+width);
    const clipped=await page.locator('.agent-onboarding, .personal-settings').first().evaluate(root=>[...root.querySelectorAll('button,input,textarea')].filter(el=>{const b=el.getBoundingClientRect(),s=getComputedStyle(el);return b.width>8&&b.height>8&&s.visibility!=='hidden'&&s.opacity!=='0'&&!el.closest('[role=group],[inert]')&&(b.left < -1 || b.right > innerWidth+1 || (el.tagName==='BUTTON'&&el.scrollWidth>el.clientWidth+2))}).map(el=>el.textContent.trim()||el.getAttribute('aria-label')||el.name));
    assert.deepEqual(clipped,[],label+' controls and complete labels fit '+width);
    if(width<=390){
     const unbalanced=await page.locator('.workspace-actions,.form-actions,.publication-actions,.setup-submit,.account-management-row .account-buttons').evaluateAll(groups=>groups.filter(group=>{
      const buttons=[...group.querySelectorAll(':scope > button')].filter(button=>button.getBoundingClientRect().width>0);
      if(!buttons.length)return false;
      const g=group.getBoundingClientRect(),style=getComputedStyle(group),rects=buttons.map(button=>button.getBoundingClientRect());
      return Math.abs(rects[0].left-g.left-parseFloat(style.paddingLeft))>2||Math.abs(rects[rects.length-1].right-g.right+parseFloat(style.paddingRight))>2||Math.max(...rects.map(r=>r.width))-Math.min(...rects.map(r=>r.width))>2;
     }).map(group=>group.className));
     assert.deepEqual(unbalanced,[],label+' mobile actions fill balanced tracks');
    }
    if(label==='settings'){
     await assertSettingsSpacing(page);
     for(const row of await page.locator('.account-management-row').all()){
      const identity=await row.locator('.workspace-account-copy').boundingBox(),actions=await row.locator('.account-buttons').boundingBox();
      assert.ok(identity&&actions);assert.ok(identity.y+identity.height<=actions.y+1||identity.x+identity.width<=actions.x+1,'Account actions cannot overlap the identity');
     }
    }

   }
  }
  await page.setViewportSize({width:1440,height:1000});
  const {setBrowserLanguage}=require('./browser-language.cjs');
  await setBrowserLanguage(page,'en-US');await page.goto(base+'/#/settings');await page.locator('.security-settings').waitFor();
  const password=page.locator('.security-settings input[name=currentPassword]');
  await password.fill('isolated-password-fixture');
  let reveal=page.locator('.security-settings').getByRole('button',{name:'Show password for 5 seconds',exact:true}).first();
  await reveal.click();assert.equal(await password.getAttribute('type'),'text');
  await page.locator('[name=profileDisplayName]').focus();assert.equal(await password.getAttribute('type'),'password');
  await reveal.focus();await page.keyboard.press('Enter');assert.equal(await password.getAttribute('type'),'text');
  await page.waitForFunction(()=>document.querySelector('.security-settings input[name=currentPassword]').type==='password',null,{timeout:6500});
  await password.fill('');
  await page.setViewportSize({width:1440,height:1000});await page.locator('[name=profileDisplayName]').fill('Unsaved profile fixture');await page.locator('.guest-settings').getByRole('checkbox').first().check();assert.equal(await page.locator('[name=profileDisplayName]').inputValue(),'Unsaved profile fixture','Publication edits do not replace the personal draft');
  const profilePath=operation('profile.get').path;let releaseProfile,profileStarted;const held=new Promise(r=>releaseProfile=r),started=new Promise(r=>profileStarted=r);
  await page.route('**'+profilePath,async route=>{const response=await route.fetch();profileStarted();await held;await route.fulfill({response})});
  await page.getByRole('button',{name:'Host management',exact:true}).click();await started;
  await page.getByRole('button',{name:'Settings',exact:true}).click();releaseProfile();await page.waitForResponse(r=>new URL(r.url()).pathname===profilePath);await page.unroute('**'+profilePath);
  assert.equal(new URL(page.url()).hash,'#/settings','A stale appearance read cannot replace the newer settings route');assert.equal(await page.locator('.host-page').count(),0);
  await page.goto(base+'/#/agent-enrollment');await page.locator('.peer-card').first().waitFor();
  let releaseFingerprint,fingerprintStarted;const fingerprintHeld=new Promise(r=>releaseFingerprint=r),pendingFingerprint=new Promise(r=>fingerprintStarted=r);
  await page.route('**'+operation('discovery.host-key').path,async route=>{const peer=route.request().postDataJSON().peerIdentity;if(peer==='peer-a'){fingerprintStarted();await fingerprintHeld}await route.fulfill({json:{fingerprint:'SHA256:fixture-'+peer}})});
  await page.locator('.peer-card').first().getByRole('button').click();await page.locator('.hostkey-panel .secondary-button').click();await pendingFingerprint;
  await page.locator('.peer-card').nth(1).getByRole('button').click();releaseFingerprint();await page.waitForFunction(()=>!document.querySelector('.hostkey-panel .secondary-button').disabled);assert.equal(await page.getByTestId('ssh-fingerprint').count(),0);
  await page.locator('.hostkey-panel .secondary-button').click();await page.getByTestId('ssh-fingerprint').waitFor();await page.locator('input[name=coreUrl]').fill('https://core.example');await page.locator('.setup-ssh input[autocomplete=username]').fill('root');await page.locator('.setup-ssh input[type=password]').fill('fixture-only-password');await page.locator('.hostkey-panel').getByRole('checkbox').first().check();
  let submitted;await page.route('**'+operation('deployments.create').path,async route=>{submitted=route.request().postDataJSON();await route.fulfill({json:{taskId:'isolated-deployment',peerIdentity:'peer-b',peerName:'peer-b',status:'queued',phase:'accepted'}})});
  const accepted=page.waitForResponse(r=>new URL(r.url()).pathname===operation('deployments.create').path&&r.request().method()==='POST');await page.locator('.agent-setup-form .primary-button').click();assert.equal((await accepted).status(),200);assert.equal(submitted.peerIdentity,'peer-b');assert.equal(submitted.coreUrl,'https://core.example');assert.equal(submitted.confirmHostKey,true);await page.getByTestId('deployment-task').waitFor();
  await page.getByRole('button',{name:/Install on another host/}).click();assert.equal(await page.locator('.setup-ssh input[type=password]').count(),0);await page.locator('.peer-card').nth(1).getByRole('button').click();assert.equal(await page.locator('.setup-ssh input[type=password]').inputValue(),'','Target changes clear SSH secrets');
  await page.getByRole('button',{name:/Install on another host/}).click();await page.locator('input[name=displayName]').fill('Pending target scope');
  let releaseEnrollment,enrollmentStarted;const heldEnrollment=new Promise(r=>releaseEnrollment=r),startedEnrollment=new Promise(r=>enrollmentStarted=r);
  await page.route('**'+operation('agents.enrollment').path,async route=>{const response=await route.fetch();enrollmentStarted();await heldEnrollment;await route.fulfill({response})});
  await page.locator('.agent-setup-form .primary-button').click();await startedEnrollment;
  await page.locator('.peer-card').first().getByRole('button').click();releaseEnrollment();await page.waitForResponse(r=>new URL(r.url()).pathname===operation('agents.enrollment').path&&r.request().method()==='POST');await page.unroute('**'+operation('agents.enrollment').path);
  assert.equal(await page.evaluate(()=>window.onboardingCopied),undefined,'A manual enrollment response cannot copy credentials after selecting an SSH target');assert.equal(await page.locator('.agent-enrollment-actions .secondary-button').count(),0);
  const enrollmentPage=await context.newPage();enrollmentPage.setDefaultTimeout(10000);await enrollmentPage.setViewportSize({width:1440,height:1000});try{await require('./enrollment-regressions.cjs')({page:enrollmentPage,base,artifacts:out,catalog})}finally{await enrollmentPage.close()}
  assert.deepEqual(errors,[]);console.log("audit-onboarding passed.");
 }catch(error){if(browser)for(const c of browser.contexts())for(const p of c.pages()){console.error((await p.locator('body').innerText()).slice(-3000));}throw error}
 finally{if(browser)await browser.close();core.kill('SIGTERM');await new Promise(r=>core.exitCode!==null?r():core.once('exit',r));fs.rmSync(dir,{recursive:true,force:true})}
})().catch(e=>{console.error(e);process.exitCode=1});
