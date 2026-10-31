// Isolated real Core and protocol-only Agents. No production or remote controls.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),net=require('node:net');
const {spawn}=require('node:child_process'),{chromium}=require('playwright');
const {setBrowserLanguage}=require('./browser-language.cjs');
const root=path.resolve(__dirname,'../..'),artifacts=path.join(root,'.build/global-audit');
(async()=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'nd-global-')),listener=net.createServer();await new Promise(r=>listener.listen(0,'127.0.0.1',r));const port=listener.address().port;await new Promise(r=>listener.close(r));
 const base=`http://127.0.0.1:${port}`,core=spawn(root+'/.build/nodedance',['serve','--dev','--listen',`127.0.0.1:${port}`,'--data-dir',dir],{stdio:'ignore'});let browser,fixture,page;
 fs.mkdirSync(artifacts,{recursive:true});
 try{
  for(let i=0;i<100;i++){try{if((await fetch(base+'/api/v1/health')).ok)break}catch{}await new Promise(r=>setTimeout(r,100))}
  browser=await chromium.launch({args:['--use-gl=angle','--use-angle=swiftshader','--enable-unsafe-swiftshader']});const context=await browser.newContext({locale:'en-US',viewport:{width:1440,height:1100}});
  const csrf=(await(await context.request.get(base+'/api/v1/auth/csrf')).json()).token,credentials=JSON.parse(fs.readFileSync(dir+'/initial-admin.json'));
  const login=await context.request.post(base+'/api/v1/auth/login',{headers:{Origin:base,'X-CSRF-Token':csrf},data:credentials});assert.equal(login.status(),200);const headers={Origin:base,'X-CSRF-Token':(await login.json()).csrfToken};
  const fixtureCount=24;fixture=await require('./overview-motion-fixture.cjs')({context,base,count:fixtureCount,cpuPercent:0});
  const nodes=(await(await context.request.get(base+'/api/v1/nodes')).json()).nodes,codes=['US','BR','DE','ZA','JP','AU'];assert.equal(nodes.length,fixtureCount);const placeCount=new Set(nodes.map((_,index)=>codes[index%codes.length])).size;
  for(const [index,node] of nodes.entries())assert.equal((await context.request.put(base+`/api/v1/nodes/${node.nodeId}/preferences`,{headers,data:{targetKind:'node',identity:'node:'+node.nodeId,alias:`VPS ${String(index+1).padStart(2,'0')} · ${codes[index%codes.length]}`,countryCode:codes[index%codes.length],icon:index%2?'docker':'',visible:true}})).status(),204);
  const anonymous=await browser.newContext();const anonPage=await anonymous.newPage(),anonReads=[];anonPage.on('request',r=>{if(new URL(r.url()).pathname.startsWith('/api/v1/nodes'))anonReads.push(r.url())});await anonPage.goto(base+'/#/global-overview');await anonPage.locator('#login-title').waitFor();assert.deepEqual(anonReads,[]);assert.equal(await anonPage.locator('.global-overview').count(),0);await anonymous.close();
  page=await context.newPage();page.setDefaultTimeout(15000);const errors=[],external=[];page.on('pageerror',e=>{errors.push(e.message);console.error('Browser:',e.message)});page.on('request',r=>{if(!r.url().startsWith(base)&&!r.url().startsWith('data:')&&!r.url().startsWith('blob:'))external.push(r.url())});
  await page.addInitScript(()=>{
    const state=window.__cobeAudit={programs:new Set(),created:0,markers:0,arcs:0,draws:0};
    for(const type of [WebGLRenderingContext,WebGL2RenderingContext]){
      const p=type.prototype,create=p.createProgram,remove=p.deleteProgram;
      p.createProgram=function(){const program=create.call(this);state.programs.add(program);state.created++;return program};
      p.deleteProgram=function(program){state.programs.delete(program);return remove.call(this,program)};
    }
    const p=WebGL2RenderingContext.prototype,draw=p.drawArraysInstanced;
    p.drawArraysInstanced=function(mode,first,count,instances){state.draws++;if(mode===this.TRIANGLE_STRIP)state.arcs=instances;else state.markers=instances;return draw.call(this,mode,first,count,instances)};
  });
  let releaseInitialMetrics,initialMetricsStarted,initialMetricReads=0;
  const initialMetricsGate=new Promise(resolve=>releaseInitialMetrics=resolve),initialMetricsPending=new Promise(resolve=>initialMetricsStarted=resolve);
  const initialMetricPath=`/api/v1/nodes/${nodes[0].nodeId}/metrics`,initialMetricRoute=async route=>{initialMetricReads++;initialMetricsStarted();await initialMetricsGate;try{await route.continue()}catch{}};
  await page.route('**'+initialMetricPath,initialMetricRoute);
  try{
   await page.goto(base+'/#/global-overview');await initialMetricsPending;await page.locator('.global-orbit[data-metrics-count="23"]').waitFor();assert.equal(initialMetricReads,1,'Bulk prefetch and selected-node refresh share one in-flight metrics read');
  }finally{releaseInitialMetrics()}
  await page.unroute('**'+initialMetricPath,initialMetricRoute);
  await page.locator(`.global-orbit[data-marker-count="${placeCount}"]`).waitFor();
  await page.locator(`.global-orbit[data-metrics-count="${nodes.length}"]`).waitFor();
  await page.waitForFunction(()=>{const canvas=document.querySelector('.global-orbit canvas');return canvas&&canvas.width>100&&getComputedStyle(canvas).opacity==='1'});
  assert.equal(await page.locator('.global-globe-fallback').count(),0,'Use the actual WebGL globe');
  await page.waitForFunction(({codes,ids,placeCount})=>{const place=id=>codes[ids.indexOf(id)%codes.length];const front=new Set([...document.querySelectorAll('.global-cell')].map(cell=>place(cell.getAttribute('data-node-id'))));const focused=document.querySelector('.global-cell[data-focused]');const expected=front.size-(focused&&front.has(place(focused.getAttribute('data-node-id')))?1:0);return window.__cobeAudit.markers===placeCount&&expected>=1&&window.__cobeAudit.arcs===expected},{codes,ids:nodes.map(node=>node.nodeId),placeCount});
  const render=await page.locator('.global-orbit canvas').evaluate(c=>{
    const g=c.getContext('webgl2'),state=window.__cobeAudit;
    const uniforms=[...state.programs].flatMap(p=>Array.from({length:g.getProgramParameter(p,g.ACTIVE_UNIFORMS)},(_,i)=>{const n=g.getActiveUniform(p,i).name,v=g.getUniform(p,g.getUniformLocation(p,n));return typeof v==='number'?v:Array.from(v)})).flat();
    return{finite:uniforms.every(Number.isFinite),created:state.created,active:state.programs.size,dpr:c.width/c.offsetWidth};
  });
  assert.ok(render.finite,'All native v2 shader uniforms are finite');assert.equal(render.created,3);assert.equal(render.active,3);assert.ok(Math.abs(render.dpr-2)<.02,'v2 applies devicePixelRatio exactly once');
  await page.waitForFunction(expected=>document.querySelectorAll('[style*="anchor-name: --cobe-fleet-"]').length===expected&&document.querySelectorAll('[style*="anchor-name: --cobe-arc-"]').length===window.__cobeAudit.arcs,placeCount);
  assert.equal(await page.locator('.global-marker-label').count(),nodes.length,'Every located VPS has its own anchored name');
  await page.waitForFunction(()=>[...document.querySelectorAll('.global-marker-label')].some(label=>getComputedStyle(label).visibility==='visible'));
  assert.ok(await page.locator('.global-marker-label:visible').count()>1,'All VPS names on the visible hemisphere are shown together');
  const labelGeometry=await page.locator('.global-marker-label:visible').first().evaluate(el=>{const r=el.getBoundingClientRect(),orbit=el.closest('.global-orbit').getBoundingClientRect();return{inside:r.left>=orbit.left-60&&r.right<=orbit.right+60&&r.bottom>=orbit.top&&r.top<=orbit.bottom,anchor:getComputedStyle(el).positionAnchor}});
  assert.ok(labelGeometry.inside);assert.ok(labelGeometry.anchor.startsWith('--cobe-fleet-'));
  const labels=await page.locator('.workspace-nav button').allTextContents();assert.equal(labels.indexOf('Global overview')+1,labels.indexOf('Settings'),'Navigation places the new page immediately above personal settings');
  await page.locator('.global-cell[data-focused] .global-bar[data-resource="cpu"][data-state="known"]').waitFor();assert.equal(await page.locator('.global-cell[data-focused] .global-bar[data-resource="cpu"] strong').textContent(),'0.0%','A real zero CPU sample is not an unknown');
  const cellCount=await page.locator('.global-cell').count();assert.ok(cellCount>=1&&cellCount<=nodes.length,'Matrix lists the in-view VPS without a display cap');assert.equal(await page.locator('.global-matrix-scroll').count(),1,'Matrix scrolls inside its own region');
  const first=await page.locator('.global-cell[data-focused]').getAttribute('data-node-id');await page.waitForFunction(id=>document.querySelector('.global-cell[data-focused]')?.getAttribute('data-node-id')!==id,first,{timeout:30000});
  assert.equal(await page.locator('.global-cell[data-focused] .global-bar[data-state="known"]').count(),2,'Tour handoff reuses prefetched CPU and memory metrics instead of clearing the card');
  assert.equal(await page.evaluate(()=>window.__cobeAudit.created),3,'Touring/config updates preserve native WebGL instance');
  const canvas=page.locator('.global-orbit canvas'),bounds=await canvas.boundingBox();
  const anchor=page.locator('[style*="anchor-name: --cobe-fleet-US"]');
  await page.mouse.move(bounds.x+bounds.width/2,bounds.y+bounds.height/2);await page.mouse.down();await page.waitForTimeout(120);
  const held=await anchor.evaluate(el=>el.style.left);await page.waitForTimeout(200);assert.equal(await anchor.evaluate(el=>el.style.left),held,'Holding the globe pauses its rotation');
  await page.mouse.move(bounds.x+bounds.width/2+120,bounds.y+bounds.height/2,{steps:6});await page.waitForTimeout(200);assert.notEqual(await anchor.evaluate(el=>el.style.left),held,'Native spring drag changes the marker projection');await page.mouse.up();
  assert.equal(await page.evaluate(()=>window.__cobeAudit.created),3,'Pointer interaction preserves the renderer');
  const appearance=await(await context.request.get(base+'/api/v1/public/appearance')).json();
  for(const [theme,width] of [['light',1440],['dark',390]]){
   assert.equal((await context.request.put(base+'/api/v1/settings/appearance',{headers,data:{displayName:appearance.displayName,backgroundColor:appearance.backgroundColor,theme}})).status(),200);
   await page.setViewportSize({width,height:1100});await page.reload();await page.locator(`.global-orbit[data-marker-count="${placeCount}"]`).waitFor();await page.locator('.global-node-card').waitFor();
   assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));
   const geometry=await page.locator('.global-layout').evaluate(el=>{const map=el.children[0].getBoundingClientRect(),side=el.children[1].getBoundingClientRect();return{map:map.toJSON(),side:side.toJSON()}});
   if(width>1000){assert.ok(geometry.side.x>geometry.map.x+geometry.map.width);assert.ok(Math.abs(geometry.map.width/geometry.side.width-2)<.15,'Wide globe/card ratio is 2:1')}
   else assert.ok(geometry.side.y>=geometry.map.bottom,'Narrow view stacks the card after the globe');
   await page.waitForTimeout(800);
  }
  await setBrowserLanguage(page,'zh-CN');await page.locator('.workspace-nav button').filter({hasText:'总览全球'}).waitFor();
  const cardNodeId=await page.locator('.global-cell[data-focused]').evaluate(el=>{el.querySelector('.global-cell-name').click();return el.dataset.nodeId});await page.locator('#detail-title').waitFor();assert.ok(page.url().includes(cardNodeId));assert.equal(await page.evaluate(()=>window.__cobeAudit.programs.size),0,'Leaving the globe destroys all native WebGL programs');assert.equal(await page.locator('[style*="anchor-name: --cobe-"]').count(),0);await page.goBack();await page.locator('.global-overview').waitFor();
  const last=nodes.at(-1);assert.equal((await context.request.put(base+`/api/v1/nodes/${last.nodeId}/preferences`,{headers,data:{targetKind:'node',identity:'node:'+last.nodeId,alias:'未定位节点',countryCode:'',visible:true}})).status(),204);
  await page.reload();await page.waitForFunction(expected=>document.querySelector('.global-orbit')?.getAttribute('data-marker-count')===expected,String(placeCount),{timeout:30000});assert.equal(await page.locator('.global-unlocated').count(),0,'Unlocated nodes get no card below the matrix');
  const metrics=await(await context.request.get(base+`/api/v1/nodes/${last.nodeId}/metrics`)).json();assert.equal(metrics.metrics.cpu.usagePercent.value,0);
  await page.route('**/api/v1/nodes',route=>route.fulfill({json:{nodes:[],serverTime:new Date().toISOString()}}));await page.reload();await page.getByText('还没有节点',{exact:true}).waitFor();assert.equal(await page.locator('.global-orbit').getAttribute('data-marker-count'),'0','Empty fleet never uses registry example markers');assert.equal(await page.locator('[style*="anchor-name: --cobe-"]').count(),0,'Removed fleet clears native marker and arc anchors');await page.unroute('**/api/v1/nodes');
  assert.deepEqual(errors,[]);assert.deepEqual(external,[]);assert.deepEqual(fixture.errors,[]);
  console.log("audit-global passed.");
 }catch(e){if(page){}throw e}
 finally{if(fixture)await fixture.close();if(browser)await browser.close();core.kill('SIGTERM');if(core.exitCode===null)await new Promise(r=>core.once('exit',r));fs.rmSync(dir,{recursive:true,force:true})}
})().catch(e=>{console.error(e);process.exitCode=1});
