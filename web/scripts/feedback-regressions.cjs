// Presentation-only fixture: shared components receive real task-state shapes;
// no Core/Agent mutation or updater execution is involved.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path')
module.exports=async function({page,base,artifacts}){
 const root=path.resolve(__dirname,'../..'),out=path.join(artifacts,'feedback-components'),entry=path.join(out,'entry.tsx');fs.mkdirSync(out,{recursive:true})
 fs.writeFileSync(entry,`
 import {createElement as h} from '${root}/web/node_modules/react/index.js';
 import {createRoot} from '${root}/web/node_modules/react-dom/client.js';
 import {flushSync} from '${root}/web/node_modules/react-dom/index.js';
 import Progress from '${root}/web/src/components/UiTaskProgress';
 import Loading from '${root}/web/src/components/UiLoadState';
 import Path from '${root}/web/src/components/UiPath';
 import '${root}/web/src/style.css';
 const root=createRoot(document.getElementById('fixture'));
 window.feedback=(props)=>flushSync(()=>root.render(h('main',{className:'app-shell ui-load-region','data-theme':props.theme||'light'},
 h(Loading,{loading:props.loading,ready:props.ready}),h('div',{'data-testid':'retained'},'existing content'),
 h(Progress,props),h(Path,{value:'/very/long/目录/'+ 'long-name-'.repeat(12)+'.tar.gz'}))));
 window.feedback({status:'queued',loading:true,ready:false});`)
 const {build}=await import('vite'),react=(await import('@vitejs/plugin-react')).default
 await build({configFile:false,root:path.join(root,'web'),plugins:[react()],logLevel:'error',define:{'process.env.NODE_ENV':JSON.stringify('production')},build:{lib:{entry,formats:['es'],fileName:()=> 'fixture.js'},outDir:path.join(out,'dist'),emptyOutDir:true}})
 const dist=path.join(out,'dist'),css=fs.readdirSync(dist).filter(n=>n.endsWith('.css')).map(n=>`<link rel="stylesheet" href="/feedback-fixture/${n}">`).join('')
 await page.route('**/feedback-fixture/**',route=>{const name=new URL(route.request().url()).pathname.split('/').pop();return name==='index.html'?route.fulfill({contentType:'text/html',body:`${css}<div id="fixture"></div><script type="module" src="/feedback-fixture/fixture.js"></script>`}):route.fulfill({path:path.join(dist,name)})})
 await page.goto(base+'/feedback-fixture/index.html');await page.locator('.ui-task-progress').waitFor()
 assert.equal(await page.locator('progress').count(),0,'Queued tasks have no invented progress');await page.locator('.ui-load-state[data-phase=initial]').waitFor()
 await page.evaluate(()=>{window.retained=document.querySelector('[data-testid=retained]');window.feedback({status:'running',phase:'pull',completed:2,total:5,loading:true,ready:true})})
 assert.equal(await page.locator('progress').getAttribute('value'),'2');assert.equal(await page.locator('progress').getAttribute('max'),'5')
 assert.ok(await page.evaluate(()=>window.retained===document.querySelector('[data-testid=retained]')),'Refresh keeps existing DOM');await page.locator('.ui-load-state[data-phase=refresh]').waitFor()
 await page.evaluate(()=>window.feedback({status:'running',phase:'verify',completed:0,total:0}));assert.equal(await page.locator('progress').count(),0,'Stage-only tasks have no fake zero denominator')
 await page.evaluate(()=>window.feedback({status:'failed',phase:'verify',error:'checksum <script>literal</script>'}));assert.ok((await page.getByRole('alert').innerText()).includes('<script>literal</script>'));assert.equal(await page.locator('.ui-task-progress script').count(),0)
 for(const theme of ['light','dark'])for(const width of [390,1440]){
  await page.setViewportSize({width,height:900});await page.evaluate(theme=>window.feedback({status:'running',phase:'download',completed:2,total:5,theme}),theme)
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Feedback and long paths fit the viewport')
  await page.screenshot({path:path.join(artifacts,`feedback-${theme}-${width}.png`),fullPage:true})
 }
 await page.getByRole('button',{name:/View full path:/}).click();await page.getByRole('dialog',{name:'Full path',exact:true}).waitFor();assert.ok((await page.locator('.ui-path-full').innerText()).endsWith('.tar.gz'));await page.keyboard.press('Escape');await page.locator('dialog').waitFor({state:'detached'})
 console.log('Shared feedback passed: real fractions, stage-only/queued states, escaped errors, retained refresh DOM, full paths and four theme/width layouts.')
}
