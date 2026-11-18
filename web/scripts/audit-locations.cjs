const assert = require('node:assert/strict'), fs = require('node:fs'), os = require('node:os'), path = require('node:path'), net = require('node:net');
const {spawn,execFileSync} = require('node:child_process'), {chromium} = require('playwright');
const {selectValue} = require('./arc-controls.cjs');
const {setBrowserLanguage}=require('./browser-language.cjs');
const root = path.resolve(__dirname, '../..'), artifacts = path.join(root, '.build/location-audit');
(async () => {
 const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'nd-locations-')), listener = net.createServer();
 await new Promise(r => listener.listen(0, '127.0.0.1', r)); const port = listener.address().port; await new Promise(r => listener.close(r));
 const base = `http://127.0.0.1:${port}`, core = spawn(root+'/.build/nodedance', ['serve','--dev','--listen',`127.0.0.1:${port}`,'--data-dir',dir], {stdio:'ignore'});
 let browser, globeBrowser, fixture;
 try {
  for(let i=0;i<100;i++){try{if((await fetch(base+'/api/v1/health')).ok)break}catch{}await new Promise(r=>setTimeout(r,100))}
  browser = await chromium.launch({args:["--enable-unsafe-swiftshader"]}); const context = await browser.newContext({locale:'en-US'});
  const csrf = (await(await context.request.get(base+'/api/v1/auth/csrf')).json()).token;
  const credentials=JSON.parse(fs.readFileSync(dir+'/initial-admin.json'));
  const login = await context.request.post(base+'/api/v1/auth/login', {headers:{Origin:base,'X-CSRF-Token':csrf},data:credentials}); assert.equal(login.status(),200);
  const headers = {Origin:base,'X-CSRF-Token':(await login.json()).csrfToken};
  fixture = await require('./overview-motion-fixture.cjs')({context,base});
  const nodes = (await(await context.request.get(base+'/api/v1/nodes')).json()).nodes;
  const errors = [], watch = opened => {opened.on('pageerror',e=>{errors.push(e.message);console.error('Browser error:',e.message)});opened.setDefaultTimeout(20000);return opened}, page = watch(await context.newPage());fs.mkdirSync(artifacts,{recursive:true});
  const catalog=await(await context.request.get(base+'/api/v1/operations')).json();
  const stateDir=path.join(dir,'cli'),binary=root+'/.build/nodedance';
  const cli=(args,input)=>JSON.parse(execFileSync(binary,[...args,'--server',base,'--dev','--state-dir',stateDir],{input:input?JSON.stringify(input):undefined,encoding:'utf8'}));
  cli(['auth','login','--body','-'],credentials);
  const point=city=>({countryCode:city.countryCode,city:city.city,region:city.region,latitude:city.latitude,longitude:city.longitude});
  const search=async q=>(await(await context.request.get(base+'/api/v1/locations',{params:{q,countryCode:'US'}})).json()).cities;
  const la=(await search('Los Angeles')).find(c=>c.city==='Los Angeles'),sj=(await search('San Jose')).find(c=>c.city==='San Jose'),ny=(await search('New York')).find(c=>c.city==='New York City');assert.ok(la&&sj&&ny);
  const node=nodes[0],editor=page.locator('.preference-editor').first();
  await page.goto(base+`/#/nodes?node=${node.nodeId}&section=settings`);await editor.waitFor();
  assert.equal(await editor.getByRole('button',{name:'Location source',exact:true}).getAttribute('data-selected-value'),'auto');
  await editor.getByLabel('Display name',{exact:true}).fill('Los Angeles VPS');
  await selectValue(editor.getByRole('button',{name:'Location source',exact:true}),'city');
  await editor.getByLabel('Search cities',{exact:true}).fill('Los Angeles');
  await page.waitForResponse(r=>r.url().includes('/api/v1/locations?')&&r.url().includes('Los'));
  const city=editor.getByRole('button',{name:'City',exact:true});
  await city.click();await page.locator('[role=menuitemradio][data-option-value='+JSON.stringify(la.id)+']').waitFor();
  assert.equal(await page.locator('[role=menuitemradio][aria-checked=true]:not([data-option-value=""])').count(),0,'Choosing manual city mode requires an explicit city, never the first catalogue option');
  await page.keyboard.press('Escape');await page.getByRole('menu').waitFor({state:'detached'});
  assert.equal(await city.getAttribute('data-selected-value'),'');assert.equal(await city.getAttribute('aria-required'),'true');
  await selectValue(city,la.id);
  await page.waitForTimeout(2200);assert.equal(await editor.getByLabel('Display name',{exact:true}).inputValue(),'Los Angeles VPS','Live samples preserve the position/name draft');
  let response=page.waitForResponse(r=>r.request().method()==='PUT'&&r.url().includes('/preferences'));await editor.getByRole('button',{name:'Save preferences',exact:true}).click();assert.equal((await response).status(),204);
  let saved=(await(await context.request.get(base+`/api/v1/nodes/${node.nodeId}/preferences`)).json()).preferences.find(p=>p.targetKind==='node');assert.deepEqual(saved.location,point(la));
  await page.reload();await editor.getByRole('button',{name:'Location source',exact:true}).waitFor();await page.waitForFunction(()=>document.querySelector('.location-picker button[data-dropdown-field]')?.getAttribute('data-selected-value')==='city');
  for(const [index,city] of [[1,sj],[2,ny]]){const n=nodes[index];assert.equal(cli(['call','nodes.save-preference','--params',JSON.stringify({nodeId:n.nodeId}),'--body','-'],{targetKind:'node',identity:'node:'+n.nodeId,alias:city.city+' VPS',visible:true,location:point(city)}).status,204)}
  const listed=(await(await context.request.get(base+'/api/v1/nodes')).json()).nodes;const placedCountries=new Set([la,sj,ny].map(city=>city.countryCode)).size,placedCities=new Set([la,sj,ny].map(city=>city.city)).size;assert.equal(new Set(listed.map(n=>n.country.code)).size,placedCountries);assert.equal(new Set(listed.map(n=>n.location.position.city)).size,placedCities);
  const globe=async(url,viewport,check)=>{globeBrowser=await chromium.launch({args:["--use-gl=angle","--use-angle=swiftshader","--enable-unsafe-swiftshader"]});const view=watch(await globeBrowser.newPage({locale:'en-US',storageState:await context.storageState(),viewport}));await view.goto(base+url);await view.locator(`.global-orbit[data-marker-count="${placedCities}"]`).waitFor();await check(view);await globeBrowser.close();globeBrowser=null};
  await globe('/#/global-overview',undefined,view=>view.waitForFunction(expected=>document.querySelectorAll('[style*="anchor-name: --cobe-fleet-US-"]').length===expected,placedCities));
  const appearance=await(await context.request.get(base+'/api/v1/public/appearance')).json();
  for(const [theme,width] of [['light',1440],['dark',390]]){
   await context.request.put(base+'/api/v1/settings/appearance',{headers,data:{displayName:appearance.displayName,backgroundColor:appearance.backgroundColor,theme}});
   const viewport={width,height:1000};await page.setViewportSize(viewport);await globe('/?city-visual='+theme+'-'+width+'#/global-overview',viewport,async view=>assert.ok(await view.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)));

   await page.reload();await editor.getByRole('button',{name:'Location source',exact:true}).waitFor();
   await editor.getByRole('button',{name:'City',exact:true}).click();await page.getByRole('menu').waitFor();await page.keyboard.press('Escape');
  }
  await selectValue(editor.getByRole('button',{name:'Location source',exact:true}),'coordinates');
  await editor.getByLabel('City',{exact:true}).fill('Zero coordinate test');await editor.getByLabel('Latitude',{exact:true}).fill('0');await editor.getByLabel('Longitude',{exact:true}).fill('0');
  response=page.waitForResponse(r=>r.request().method()==='PUT'&&r.url().includes('/preferences'));await editor.getByRole('button',{name:'Save preferences',exact:true}).click();assert.equal((await response).status(),204);
  saved=(await(await context.request.get(base+`/api/v1/nodes/${node.nodeId}/preferences`)).json()).preferences.find(p=>p.targetKind==='node');assert.equal(saved.location.latitude,0);assert.equal(saved.location.longitude,0);
  await selectValue(editor.getByRole('button',{name:'Location source',exact:true}),'auto');response=page.waitForResponse(r=>r.request().method()==='PUT'&&r.url().includes('/preferences'));await editor.getByRole('button',{name:'Save preferences',exact:true}).click();assert.equal((await response).status(),204);
  saved=(await(await context.request.get(base+`/api/v1/nodes/${node.nodeId}/preferences`)).json()).preferences.find(p=>p.targetKind==='node');assert.equal(saved.location,null);assert.equal(saved.countryCode,'');
  const reset=(await(await context.request.get(base+'/api/v1/nodes')).json()).nodes.find(n=>n.nodeId===node.nodeId);assert.equal(reset.location.status,'unknown','Private fixture addresses are never externally geolocated or guessed');
  await setBrowserLanguage(page,'zh-CN');await editor.getByRole('button',{name:'位置来源',exact:true}).waitFor();
  assert.deepEqual(errors,[]);assert.deepEqual(fixture.errors,[]);console.log("audit-locations passed.");
 } catch(error) {if(browser)for(const c of browser.contexts())for(const p of c.pages())try{}catch{}throw error} finally {if(fixture)await fixture.close();if(globeBrowser)await globeBrowser.close();if(browser)await browser.close();core.kill('SIGTERM');await new Promise(r=>core.exitCode!==null?r():core.once('exit',r));fs.rmSync(dir,{recursive:true,force:true})}
})().catch(e=>{console.error(e);process.exitCode=1});
