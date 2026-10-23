// Real isolated Core and protocol-only Agent samples; no production resources.
const assert = require('node:assert/strict'), fs = require('node:fs'), os = require('node:os'), path = require('node:path'), net = require('node:net');
const {spawn,execFileSync} = require('node:child_process'), {chromium} = require('playwright');
const {selectValue} = require('./arc-controls.cjs');
const {setBrowserLanguage}=require('./browser-language.cjs');
const root = path.resolve(__dirname, '../..'), artifacts = path.join(root, '.build/preference-audit');
(async () => {
 const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'nd-preferences-')), listener = net.createServer();
 await new Promise(r => listener.listen(0, '127.0.0.1', r)); const port = listener.address().port; await new Promise(r => listener.close(r));
 const base = `http://127.0.0.1:${port}`, core = spawn(root+'/.build/nodedance', ['serve','--dev','--listen',`127.0.0.1:${port}`,'--data-dir',dir], {stdio:'ignore'});
 let browser, fixture;
 try {
  for(let i=0;i<100;i++){try{if((await fetch(base+'/api/v1/health')).ok)break}catch{}await new Promise(r=>setTimeout(r,100))}
  browser = await chromium.launch(); const context = await browser.newContext({locale:'en-US'});
  const csrf = (await(await context.request.get(base+'/api/v1/auth/csrf')).json()).token;
  const credentials=JSON.parse(fs.readFileSync(dir+'/initial-admin.json'));
  const login = await context.request.post(base+'/api/v1/auth/login', {headers:{Origin:base,'X-CSRF-Token':csrf},data:credentials}); assert.equal(login.status(),200);
  const headers = {Origin:base,'X-CSRF-Token':(await login.json()).csrfToken};
  fixture = await require('./overview-motion-fixture.cjs')({context,base});
  const nodes = (await(await context.request.get(base+'/api/v1/nodes')).json()).nodes;
  const page = await context.newPage(), errors = []; page.on('pageerror',e=>errors.push(e.message));page.setDefaultTimeout(10000);fs.mkdirSync(artifacts,{recursive:true});
  const catalog=await(await context.request.get(base+'/api/v1/operations')).json();
  const choices=catalog.operations.find(o=>o.name==='nodes.save-preference').bodySchema.properties.icon.enum;
  const stateDir=path.join(dir,'cli'),binary=root+'/.build/nodedance';
  const cli=(args,input)=>JSON.parse(execFileSync(binary,[...args,'--server',base,'--dev','--state-dir',stateDir],{input:input?JSON.stringify(input):undefined,encoding:'utf8'}));
  assert.equal(cli(['auth','login','--body','-'],credentials).status,200);
  const preference={nodeId:nodes[0].nodeId,targetKind:'node',identity:'node:'+nodes[0].nodeId,alias:'',icon:'',notes:'',serviceUrl:'',sshHost:'',sshPort:22,sshUser:'',group:'',sortOrder:-1,visible:true,pinned:true};
  for(const icon of choices){assert.equal(cli(['call','nodes.save-preference','--params',JSON.stringify({nodeId:nodes[0].nodeId}),'--body','-'],{...preference,icon}).status,204)}
  assert.equal(cli(['call','nodes.save-preference','--params',JSON.stringify({nodeId:nodes[0].nodeId}),'--body','-'],preference).status,204);
  const countries=catalog.operations.find(o=>o.name==='nodes.save-preference').bodySchema.properties.countryCode.enum;
  assert.ok(countries.includes('')&&countries.includes('CN')&&countries.includes('JP'));
  for(const countryCode of countries)assert.equal(cli(['call','nodes.save-preference','--params',JSON.stringify({nodeId:nodes[0].nodeId}),'--body','-'],{...preference,countryCode}).status,204);
  for(const countryCode of ['ZZ','cn','China','../CN'])assert.equal((await context.request.put(base+'/api/v1/nodes/'+nodes[0].nodeId+'/preferences',{headers,data:{...preference,countryCode}})).status(),400);
  assert.equal(cli(['call','nodes.save-preference','--params',JSON.stringify({nodeId:nodes[0].nodeId}),'--body','-'],{...preference,countryCode:''}).status,204);
  for(const [index,node] of nodes.entries()) {
   await page.goto(base+`/#/nodes?node=${node.nodeId}&section=settings`);
   const editor=page.locator('.preference-editor').first(), alias=editor.getByLabel('Display name',{exact:true});await alias.waitFor();
   const name=`Saved node ${index+1}`;await alias.fill(name);await page.waitForTimeout(2600);
   assert.equal(await alias.inputValue(),name,'Live samples must not reset unsaved name');
   await selectValue(editor.getByRole('button',{name:'Country / region',exact:true}),'JP');
   const icon=['redis','docker','postgresql'][index];await selectValue(editor.getByRole('button',{name:'Icon',exact:true}),icon);await page.waitForTimeout(2600);
   assert.equal(await alias.inputValue(),name);assert.equal(await editor.getByRole('button',{name:'Icon',exact:true}).getAttribute('data-selected-value'),icon,'Live samples must not reset selected icon');
   const response=page.waitForResponse(r=>r.request().method()==='PUT'&&r.url().includes('/preferences'));
   await editor.getByRole('button',{name:'Save preferences',exact:true}).click();assert.equal((await response).status(),204);
   const persisted=await(await context.request.get(base+'/api/v1/nodes/'+node.nodeId+'/preferences')).json();assert.equal(persisted.preferences.find(p=>p.targetKind==='node').countryCode,'JP');assert.equal(persisted.preferences.find(p=>p.targetKind==='node').alias,name);assert.equal(persisted.preferences.find(p=>p.targetKind==='node').icon,icon);await page.reload();await alias.waitFor();await page.waitForFunction(({name,icon})=>document.querySelector('.preference-editor input')?.value===name&&document.querySelector('.preference-editor button[data-dropdown-field]')?.getAttribute('data-selected-value')===icon,{name,icon});assert.equal(await alias.inputValue(),name);assert.equal(await editor.getByRole('button',{name:'Icon',exact:true}).getAttribute('data-selected-value'),icon);
   await page.goto(base+'/#/nodes');const card=page.locator(`.vps-card[data-node-id="${node.nodeId}"]`);await card.getByRole('button',{name,exact:true}).waitFor();assert.equal(await card.locator('.vps-node-icon [data-node-icon]').getAttribute('data-node-icon'),icon);await card.locator('[data-country-logo=jp]').waitFor();assert.ok(await card.locator('.vps-node-icon :is(svg,img)').evaluateAll(items=>items.length===2&&items.every(el=>{const r=el.getBoundingClientRect();return r.width===28&&r.height===28})), 'Flag and logo use identical 28px geometry');
  }
  const node=nodes[0];await page.goto(base+`/#/nodes?node=${node.nodeId}&section=settings`);const editor=page.locator('.preference-editor').first();await editor.waitFor();
  await editor.getByLabel('Display name',{exact:true}).fill('');await selectValue(editor.getByRole('button',{name:'Icon',exact:true}),'');await selectValue(editor.getByRole('button',{name:'Country / region',exact:true}),'');
  const saved=page.waitForResponse(r=>r.request().method()==='PUT'&&r.url().includes('/preferences'));await editor.getByRole('button',{name:'Save preferences',exact:true}).click();assert.equal((await saved).status(),204);
  await page.goto(base+'/#/nodes');const card=page.locator(`.vps-card[data-node-id="${node.nodeId}"]`);await card.getByRole('button',{name:node.displayName,exact:true}).waitFor();await card.locator('[data-os-logo=debian]').waitFor();await card.locator('[data-country-logo=unknown]').waitFor();
  const appearance=await(await context.request.get(base+'/api/v1/public/appearance')).json();
  for(const [theme,width] of [['light',1440],['dark',390]]){
   assert.equal((await context.request.put(base+'/api/v1/settings/appearance',{headers,data:{displayName:appearance.displayName,backgroundColor:appearance.backgroundColor,theme}})).status(),200);
   await page.setViewportSize({width,height:1000});await page.goto(base+`/#/nodes?node=${nodes[1].nodeId}&section=settings`);await page.reload();await editor.waitFor();
   await editor.getByRole('button',{name:'Icon',exact:true}).click();await page.getByRole('menu').waitFor();assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));
   const list=page.getByRole('menu');assert.ok(await list.locator('[role=menuitemradio]').evaluateAll(items=>items.every(item=>{const icon=item.querySelector('.product-logo'),label=item.querySelector('[data-option-label]');return !icon||!!label&&label.getBoundingClientRect().left>=icon.getBoundingClientRect().right+8})), 'Logo options retain an actual gap after their 28px icons');const bounds=await list.boundingBox();assert.ok(bounds.x>=0&&bounds.y>=0&&bounds.x+bounds.width<=width+1&&bounds.y+bounds.height<=1001,'Logo menu stays inside viewport');assert.ok(await list.isVisible());await page.keyboard.press('Escape');await editor.getByRole('button',{name:'Country / region',exact:true}).click();await page.getByRole('menu').waitFor();const countryBounds=await page.getByRole('menu').boundingBox();assert.ok(countryBounds.x>=0&&countryBounds.x+countryBounds.width<=width+1&&countryBounds.y+countryBounds.height<=1001);await page.keyboard.press('Escape');
  }
  await setBrowserLanguage(page,'zh-CN');await page.setViewportSize({width:390,height:1000});await page.reload();await editor.waitFor();await editor.getByRole('button',{name:'图标',exact:true}).click();await page.getByRole('menu').waitFor();await page.keyboard.press('Escape');
  await page.goto(base+'/#/nodes');await page.locator('.vps-card').first().waitFor();
  for(const [index,n] of nodes.entries())assert.equal(await page.locator(`.vps-card[data-node-id="${n.nodeId}"]`).locator('.vps-node-icon :is(svg,img)').count(),2);
  assert.ok(await page.locator('.vps-card-heading').evaluateAll(headings=>headings.every(h=>{const name=h.querySelector('h2').getBoundingClientRect(),version=h.querySelector('.vps-card-version').getBoundingClientRect();const country=h.querySelector('[data-country-logo]').getBoundingClientRect(),logo=h.querySelector('[data-node-icon],[data-os-logo]').getBoundingClientRect(),bounds=h.getBoundingClientRect();return name.width>=120&&Math.abs(name.left-version.left)<1&&Math.abs((country.left-bounds.left)-(bounds.right-logo.right))<1&&Math.abs(country.top-logo.top)<1&&h.getBoundingClientRect().height<100})), 'Country and service logos occupy symmetric top corners; names and metadata retain usable width');

  assert.deepEqual(errors,[]);assert.deepEqual(fixture.errors,[]);console.log("audit-preferences passed.");
 } catch(error) {if(browser)for(const c of browser.contexts())for(const p of c.pages())try{console.error(await p.locator('.preference-editor').evaluate(el=>({text:el.innerText,fields:[...el.querySelectorAll('input,buttonbutton[data-dropdown-field]')].map(x=>({tag:x.tagName,role:x.getAttribute('role'),value:x.value,selected:x.getAttribute('data-selected-value'),text:x.tagName==='BUTTON'?x.textContent:undefined}))})));}catch{}throw error} finally {if(fixture)await fixture.close();if(browser)await browser.close();core.kill('SIGTERM');await new Promise(r=>core.exitCode!==null?r():core.once('exit',r));fs.rmSync(dir,{recursive:true,force:true})}
})().catch(e=>{console.error(e);process.exitCode=1});
