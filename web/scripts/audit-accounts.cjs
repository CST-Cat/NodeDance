// Isolated real Core and disposable users; never touches production or Agents.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),net=require('node:net');
const {spawn,execFileSync}=require('node:child_process'),{chromium}=require('playwright');
const {openNavigation}=require('./sidebar-navigation.cjs');
const assertSettingsSpacing=require('./settings-spacing.cjs');
const root=path.resolve(__dirname,'../..'),binary=path.join(root,'.build/accounts-audit-core'),artifacts=path.join(root,'.build/accounts-audit');
fs.mkdirSync(artifacts,{recursive:true});execFileSync('go',['build','-o',binary,'./cmd/nodedance'],{cwd:root,stdio:'inherit'});
async function freePort(){const server=net.createServer();await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const port=server.address().port;await new Promise(resolve=>server.close(resolve));return port}
(async()=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-accounts-audit-')),base=`http://127.0.0.1:${await freePort()}`;
 const core=spawn(binary,['serve','--dev','--listen',new URL(base).host,'--data-dir',directory],{cwd:root,stdio:['ignore','pipe','pipe']});let logs='',browser,page;
 core.stdout.on('data',data=>logs+=data);core.stderr.on('data',data=>logs+=data);
 try{
  let ready=false;for(let attempt=0;attempt<100;attempt++){try{ready=(await fetch(base+'/api/v1/health')).ok}catch{}if(ready)break;if(core.exitCode!==null)throw Error(logs);await new Promise(resolve=>setTimeout(resolve,100))}assert.ok(ready);
  const credentials=JSON.parse(fs.readFileSync(path.join(directory,'initial-admin.json'),'utf8'));
  browser=await chromium.launch({headless:true});const context=await browser.newContext({viewport:{width:1440,height:1000}});
  await context.addInitScript(()=>localStorage.setItem('nodedance.language','en'));
  async function mutation(method,url,data){const csrf=(await(await context.request.get(base+'/api/v1/auth/csrf')).json()).token;return context.request.fetch(base+url,{method,headers:{Origin:base,'X-CSRF-Token':csrf},data})}
  assert.equal((await mutation('POST','/api/v1/auth/login',credentials)).status(),200);
  page=await context.newPage();page.setDefaultTimeout(10000);const errors=[];page.on('pageerror',error=>errors.push(error.message));
  await page.goto(base+'/#/settings');const manager=page.locator('.account-management');await manager.waitFor();assert.equal(await page.locator('.settings-sidebar').count(),0);
  await manager.getByRole('button',{name:'Add account',exact:true}).click();const create=manager.locator('.account-edit-form');
  await create.locator('[name=accountUsername]').fill('browser-reader');await create.locator('[name=accountDisplayName]').fill('Read-only browser account');await create.locator('[name=accountPassword]').fill('reader-password-long');
  await create.getByRole('button',{name:'Save account',exact:true}).click();await manager.getByRole('status').waitFor();
  let accounts=(await(await context.request.get(base+'/api/v1/accounts')).json()).accounts;const reader=accounts.find(account=>account.username==='browser-reader');assert.equal(reader.role,'viewer');
  const readerRow=()=>manager.locator('.account-management-row').filter({hasText:'browser-reader'});
  // Editing, cancellation, reset and disable all use the same Core operations.
  // Native modal editing reuses UiDialog without expanding the account card.
  const editTrigger=readerRow().getByRole('button',{name:'Edit account',exact:true});await editTrigger.click();const editor=manager.locator('dialog.account-dialog');await editor.waitFor();await page.keyboard.press('Escape');await editor.waitFor({state:'hidden'});
  const beforeEdit=await manager.boundingBox();await editTrigger.click();await editor.waitFor();
  assert.ok(await editor.evaluate(el=>el.matches(':modal')));assert.ok(Math.abs((await manager.boundingBox()).height-beforeEdit.height)<1);
  const roleSelect=editor.getByRole('combobox');await roleSelect.click();const options=editor.getByRole('listbox');await options.waitFor();
  const selectBounds=await roleSelect.boundingBox(),optionsBounds=await options.boundingBox();assert.ok(Math.abs(selectBounds.x-optionsBounds.x)<2,'Role menu stays anchored inside the modal');
  await options.getByRole('option',{name:'Administrator',exact:true}).click();await roleSelect.click();await editor.getByRole('listbox').getByRole('option',{name:'Read-only user',exact:true}).click();
  await page.keyboard.press('Escape');await editor.waitFor({state:'hidden'});assert.ok(await editTrigger.evaluate(el=>el===document.activeElement));
  for(const theme of ['light','dark'])for(const width of [1440,390,320]){
   await page.setViewportSize({width,height:1000});await page.emulateMedia({colorScheme:theme});await assertSettingsSpacing(page);await editTrigger.click();await editor.waitFor();
   const bounds=await editor.boundingBox();assert.ok(bounds.x>=0&&bounds.x+bounds.width<=width&&bounds.y>=0&&bounds.y+bounds.height<=1000);
   assert.ok(await editor.evaluate(el=>el.scrollWidth<=el.clientWidth),'Editor must fit the modal');
   await editor.getByRole('combobox').click();await editor.getByRole('listbox').getByRole('option',{name:'Read-only user',exact:true}).click();
   await page.screenshot({path:path.join(artifacts,`account-editor-${theme}-${width}.png`),fullPage:true,animations:'disabled'});
   await editor.getByRole('button',{name:'Cancel',exact:true}).click();await editor.waitFor({state:'hidden'});assert.ok(await editTrigger.evaluate(el=>el===document.activeElement));
  }
  await page.setViewportSize({width:1440,height:1000});await page.emulateMedia({colorScheme:'light'});
  await readerRow().getByRole('button',{name:'Edit account',exact:true}).click();await manager.locator('[name=accountDisplayName]').fill('Reader renamed');await manager.locator('.account-edit-form').getByRole('button',{name:'Save account',exact:true}).click();await page.locator('dialog[aria-describedby=ui-dialog-message]').getByRole('button',{name:'Cancel',exact:true}).click();
  assert.equal((await(await context.request.get(base+'/api/v1/accounts')).json()).accounts.find(account=>account.id===reader.id).displayName,'Read-only browser account');
  await manager.locator('.account-edit-form').getByRole('button',{name:'Save account',exact:true}).click();await page.locator('dialog[aria-describedby=ui-dialog-message]').getByRole('button',{name:'Confirm',exact:true}).click();await manager.locator('.account-edit-form').waitFor({state:'hidden'});
  await readerRow().getByRole('button',{name:'Reset password',exact:true}).click();await manager.locator('[name=resetPassword]').fill('reset-reader-password-long');await manager.locator('.account-edit-form').getByRole('button',{name:'Reset password',exact:true}).click();await page.locator('dialog[aria-describedby=ui-dialog-message]').getByRole('button',{name:'Confirm',exact:true}).click();await manager.locator('.account-edit-form').waitFor({state:'hidden'});
  // Six real layouts: upward panel, two-column account cards, Escape focus recovery.
  for(const theme of ['light','dark'])for(const width of [1440,390,320]){
   await page.setViewportSize({width,height:1000});await page.emulateMedia({colorScheme:theme});await openNavigation(page);
   const trigger=page.locator('.account-trigger'),panel=page.locator('.account-panel');await trigger.click();await panel.waitFor({state:'visible'});await panel.locator('.account-card').first().waitFor();
   const anchor=await trigger.boundingBox(),bounds=await panel.boundingBox();assert.ok(bounds.y>=0&&bounds.y+bounds.height<=anchor.y+1,'Account panel must open upward');assert.ok(bounds.x>=0&&bounds.x+bounds.width<=width,'Panel must fit viewport');
   assert.ok(await panel.getByRole('button',{name:'Sign out',exact:true}).evaluate(el=>el.getBoundingClientRect().width>100&&el.scrollWidth<=el.clientWidth),'Sign out must remain a full-width visible action');
   assert.equal(await panel.locator('.account-card').count(),accounts.length);assert.equal(await panel.locator('.account-card[aria-pressed=true]').count(),1);
   await page.screenshot({path:path.join(artifacts,`account-menu-${theme}-${width}.png`),fullPage:true,animations:'disabled'});
   await page.keyboard.press('Escape');await panel.waitFor({state:'hidden'});assert.ok(await trigger.evaluate(element=>document.activeElement===element),JSON.stringify({theme,width,focus:await page.evaluate(()=>({tag:document.activeElement.tagName,cls:document.activeElement.className,text:document.activeElement.textContent?.slice(0,80),side:document.querySelector('#workspace-navigation')?.className}))}));
  }
  await page.setViewportSize({width:1440,height:1000});await page.emulateMedia({colorScheme:'light'});await openNavigation(page);
  // Collapsed sidebar still has a usable avatar and upward account card.
  await page.locator('.navigation-toggle').click();await page.locator('.workspace-sidebar.is-collapsed').waitFor();await page.locator('.account-trigger').click();await page.locator('.account-panel').waitFor({state:'visible'});await page.locator('.account-card').first().waitFor();assert.ok(await page.locator('.account-trigger .profile-avatar').isVisible());await page.keyboard.press('Escape');await page.locator('.navigation-toggle').click();
  const originalCookies=await context.cookies();
  await page.locator('.account-trigger').click();const panel=page.locator('.account-panel');await panel.locator('.account-card').filter({hasText:'browser-reader'}).click();
  await panel.locator('[name=password]').fill('incorrect-password');await panel.locator('.account-switch-form').getByRole('button',{name:'Switch account',exact:true}).click();await panel.getByRole('alert').waitFor();assert.equal((await(await context.request.get(base+'/api/v1/auth/me')).json()).user.role,'administrator');assert.ok(await manager.isVisible());
  await panel.locator('[name=password]').fill('reset-reader-password-long');await panel.locator('.account-switch-form').getByRole('button',{name:'Switch account',exact:true}).click();await page.waitForFunction(()=>!document.querySelector('.account-management')&&document.querySelector('.settings-main'));
  assert.equal((await(await context.request.get(base+'/api/v1/auth/me')).json()).user.id,reader.id);assert.equal(await page.locator('.color-field').count(),0);
  const stale=await browser.newContext();await stale.addCookies(originalCookies);assert.equal((await stale.request.get(base+'/api/v1/auth/me')).status(),401);await stale.close();
  assert.equal((await mutation('POST','/api/v1/accounts',{username:'forged',role:'administrator'})).status(),403);
  await page.locator('[name=profileDisplayName]').fill('Reader own profile');await page.locator('.profile-form').getByRole('button',{name:/Save appearance/}).click();await page.getByRole('status').filter({hasText:'saved'}).waitFor();assert.equal((await(await context.request.get(base+'/api/v1/auth/profile')).json()).displayName,'Reader own profile');
  // Switching back also requires the target password.
  await page.locator('.account-trigger').click();await panel.locator('.account-card').filter({hasText:credentials.username}).click();await panel.locator('[name=password]').fill(credentials.password);await panel.locator('.account-switch-form').getByRole('button',{name:'Switch account',exact:true}).click();await manager.waitFor();
  const csrf=(await(await context.request.get(base+'/api/v1/auth/csrf')).json()).token;assert.ok(csrf);
  await readerRow().getByRole('button',{name:'Edit account',exact:true}).click();await manager.getByLabel('Account enabled',{exact:true}).uncheck();await manager.locator('.account-edit-form').getByRole('button',{name:'Save account',exact:true}).click();await page.locator('dialog[aria-describedby=ui-dialog-message]').getByRole('button',{name:'Confirm',exact:true}).click();await manager.locator('.account-edit-form').waitFor({state:'hidden'});
  await page.locator('.account-trigger').click();await panel.locator('.account-card').filter({hasText:'browser-reader'}).waitFor();assert.ok(await panel.locator('.account-card').filter({hasText:'browser-reader'}).isDisabled());await page.keyboard.press('Escape');
  assert.equal(await readerRow().getByRole('button',{name:'Delete',exact:true}).count(),0);await readerRow().getByRole('button',{name:'Edit account',exact:true}).click();await manager.locator('.account-edit-form').getByRole('button',{name:'Delete',exact:true}).click();await page.locator('dialog[aria-describedby=ui-dialog-message]').getByRole('button',{name:'Confirm',exact:true}).click();await readerRow().waitFor({state:'hidden'});
  await page.locator('.account-trigger').click();await panel.getByRole('button',{name:'Sign out',exact:true}).click();await page.locator('#login-title').waitFor();assert.equal((await context.request.get(base+'/api/v1/auth/me')).status(),401);assert.equal(await page.locator('.account-panel,.account-trigger').count(),0);assert.deepEqual(errors,[]);
  console.log('Accounts browser audit passed: CRUD, confirmation cancellation, reset, disable, isolated profiles, password-verified switching, failed-switch identity retention, stale-session denial, read-only enforcement, real logout, six theme/layout combinations and collapsed navigation.');
 }catch(error){if(page)await page.screenshot({path:path.join(artifacts,'failure.png'),fullPage:true}).catch(()=>{});throw error}
 finally{if(browser)await browser.close();core.kill('SIGTERM');if(core.exitCode===null)await new Promise(resolve=>{core.once('exit',resolve);setTimeout(()=>{core.kill('SIGKILL');resolve()},5000).unref()});fs.rmSync(directory,{recursive:true,force:true})}
})().catch(error=>{console.error(error);process.exitCode=1});
