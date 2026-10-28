// Real Core + browser behavior checks. The isolated Core has no Agent and no
// privileged update/deployment service. Synthetic routes test transport loss
// and future operation discovery without performing host-side effects.
const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const net = require('node:net')
const { spawn, execFileSync } = require('node:child_process')
const { chromium } = require('playwright')
const root = path.resolve(__dirname, '../..')
const binary = path.join(root, '.build/nodedance')
const artifacts = path.join(root, '.build/interface-audit')
const operationsOnly = process.env.NODEDANCE_AUDIT_OPERATIONS_ONLY === '1'
const alertsOnly = process.env.NODEDANCE_AUDIT_ALERTS_ONLY === '1'
const hostsOnly = process.env.NODEDANCE_AUDIT_HOSTS_ONLY === '1'
const metricsOnly = process.env.NODEDANCE_AUDIT_METRICS_ONLY === '1'
const dockerOnly = process.env.NODEDANCE_AUDIT_DOCKER_ONLY === '1'
fs.mkdirSync(artifacts, {recursive:true})


async function port() {
  const listener = net.createServer()
  await new Promise(resolve=>listener.listen(0,'127.0.0.1',resolve))
  const result=listener.address().port
  await new Promise(resolve=>listener.close(resolve))
  return result
}

;(async()=>{
  const dataDir=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-interface-audit-'))
  const base=`http://127.0.0.1:${await port()}`
  const core=spawn(binary,['serve','--dev','--listen',new URL(base).host,'--data-dir',dataDir],{cwd:root,stdio:['ignore','pipe','pipe']})
  let coreOutput=''
  core.stdout.on('data',data=>coreOutput+=data);core.stderr.on('data',data=>coreOutput+=data)
  let browser
  try {
    let ready=false
    for(let attempt=0;attempt<100;attempt++){
      if(core.exitCode!==null)throw new Error('Isolated Core exited: '+coreOutput)
      try{ready=(await fetch(base+'/api/v1/health')).ok}catch{}
      if(ready)break
      await new Promise(resolve=>setTimeout(resolve,100))
    }
    assert.ok(ready,'Isolated Core failed to start')
    browser=await chromium.launch({headless:true})
    const context=await browser.newContext({locale:'en-US'})
    const csrf=await(await context.request.get(base+'/api/v1/auth/csrf')).json()
    const credentials=JSON.parse(fs.readFileSync(path.join(dataDir,'initial-admin.json'),'utf8'))
    const login=await context.request.post(base+'/api/v1/auth/login',{data:{username:credentials.username,password:credentials.password},headers:{Origin:base,'X-CSRF-Token':csrf.token}})
    assert.equal(login.status(),200)
    const headers={Origin:base,'X-CSRF-Token':(await login.json()).csrfToken}
    const catalog=await(await context.request.get(base+'/api/v1/operations')).json()
    const enrollment=await context.request.post(base+'/api/v1/agents/enrollments',{headers,data:{displayName:'Interface audit',coreUrl:base}})
    assert.equal(enrollment.status(),201)
    const node=(await enrollment.json()).nodeId
    if(!operationsOnly&&!alertsOnly&&!hostsOnly&&!metricsOnly&&!dockerOnly)await require('./overview-regressions.cjs')({context,base,node,headers,artifacts,catalog})
    const page=await context.newPage()
    page.setDefaultTimeout(10000)
    const pageErrors=[];page.on('pageerror',error=>pageErrors.push(error.message))
    if(dockerOnly){await page.setViewportSize({width:1440,height:1000});await require('./docker-regressions.cjs')({page,base,node,artifacts,catalog});assert.deepEqual(pageErrors,[]);return}
    if(metricsOnly){await require('./metrics-regressions.cjs')({page,base,node,artifacts,catalog});assert.deepEqual(pageErrors,[]);return}
    if(hostsOnly){await require('./host-management-regressions.cjs')({page,context,base,node,headers,catalog});assert.deepEqual(pageErrors,[]);return}
    if(alertsOnly){await require('./alert-center-regressions.cjs')({page,context,base,node,headers,catalog});assert.deepEqual(pageErrors,[]);return}
    const {submissions}=await require('./operation-console-regressions.cjs')({page,context,base,node,headers,catalog})
    if(operationsOnly){assert.deepEqual(pageErrors,[]);return}
    let response
    await require('./alert-center-regressions.cjs')({page,context,base,node,headers,catalog})
    const updateChecks=await require('./update-center-regressions.cjs')({page,base,artifacts})
    await require('./host-management-regressions.cjs')({page,context,base,node,headers,catalog})
    // Real browser Back/Forward and reload must restore views and tabs without
    // repeating the mutations exercised earlier in this fixture.
    await page.getByRole('button',{name:'Alerts',exact:true}).click()
    await page.locator('.alerts-page').waitFor();assert.equal(new URL(page.url()).hash,'#/alerts')
    await page.getByRole('tab',{name:'Alert rules',exact:true}).click()
    await page.waitForFunction(()=>document.querySelector('.alert-tabs [aria-selected=true]')?.textContent==='Alert rules')
    const rulesURL=page.url()
    await page.goBack();await page.waitForFunction(()=>document.querySelector('.alert-tabs [aria-selected=true]')?.textContent.startsWith('Active alerts'))
    assert.equal(new URL(page.url()).hash,'#/alerts')
    await page.goForward();await page.waitForFunction(()=>document.querySelector('.alert-tabs [aria-selected=true]')?.textContent==='Alert rules')
    assert.equal(page.url(),rulesURL)
    await page.reload();await page.locator('.alert-form-card').waitFor();assert.equal(page.url(),rulesURL)
    await page.goto(base+'/#/settings');await page.locator('.guest-settings').waitFor()
    await page.goBack();await page.locator('.alert-form-card').waitFor();assert.equal(page.url(),rulesURL)
    await page.goForward();await page.locator('.guest-settings').waitFor();assert.equal(new URL(page.url()).hash,'#/settings')
    await page.reload();await page.locator('.guest-settings').waitFor()
    await page.getByRole('button',{name:'Host management',exact:true}).click();await page.locator('.host-page [role=tablist]').waitFor()
    await page.locator('.host-page [role=tablist]').getByRole('tab',{name:'Assets and quotas',exact:true}).click()
    await page.waitForFunction(()=>location.hash.includes('tab=assets'))
    await page.goBack();await page.waitForFunction(()=>!location.hash.includes('tab='))
    await page.goForward();await page.waitForFunction(()=>document.querySelector('.host-page [role=tab][aria-selected=true]')?.textContent==='Assets and quotas')
    await page.reload();await page.waitForFunction(()=>document.querySelector('.host-page [role=tab][aria-selected=true]')?.textContent==='Assets and quotas')
    let permissionReads=0;page.on('request',r=>{if(r.url().endsWith('/api/v1/auth/me'))permissionReads++})
    await page.evaluate(()=>window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true})))
    await page.waitForFunction(()=>document.querySelector('.host-page [role=tab][aria-selected=true]')?.textContent==='Assets and quotas')
    assert.equal(permissionReads,1,'Restoring a cached document must revalidate its administrator session')
    await require('./review-regressions.cjs')({page,context,base,node,headers,artifacts,catalog})
    const controlsPage=await context.newPage()
    controlsPage.setDefaultTimeout(10000)
    try { await require('./theme-regressions.cjs')({page:controlsPage,context,base,headers,artifacts,catalog}); await require('./operation-locale-regressions.cjs')({page:controlsPage,base,catalog,artifacts}); await require('./ui-controls-regressions.cjs')({page:controlsPage,base,artifacts,catalog}) }
    finally { await controlsPage.close() }
    const dockerPage=await context.newPage()
    dockerPage.setDefaultTimeout(10000)
    await dockerPage.setViewportSize({width:1440,height:1000})
    try { await require('./docker-regressions.cjs')({page:dockerPage,base,node,artifacts,catalog}) }
    finally { await dockerPage.close() }
    const streamPage=await context.newPage()
    streamPage.setDefaultTimeout(10000)
    await streamPage.setViewportSize({width:1440,height:1000})
    try { await require('./stream-panel-regressions.cjs')({page:streamPage,base,node,artifacts,catalog}) }
    finally { await streamPage.close() }
    const composePage=await context.newPage()
    composePage.setDefaultTimeout(10000)
    try { await require('./compose-regressions.cjs')({page:composePage,base,node,artifacts,catalog}) }
    finally { await composePage.close() }
    const feedbackPage=await context.newPage();feedbackPage.setDefaultTimeout(10000)
    try { await require('./feedback-regressions.cjs')({page:feedbackPage,base,artifacts}) }
    finally { await feedbackPage.close() }
    const metricsPage=await context.newPage()
    metricsPage.setDefaultTimeout(10000)
    try { await require('./metrics-regressions.cjs')({page:metricsPage,base,node,artifacts,catalog}) }
    finally { await metricsPage.close() }
    const lazyPage=await context.newPage();lazyPage.setDefaultTimeout(10000)
    try { await require('./lazy-view-regressions.cjs')({page:lazyPage,base,node}) }
    finally { await lazyPage.close() }
    const sshPage=await context.newPage()
    sshPage.setDefaultTimeout(10000)
    try { await require('./ssh-shortcut-regressions.cjs')({page:sshPage,base,headers,artifacts,catalog}) }
    finally { await sshPage.close() }
    const mobileNavigationPage=await context.newPage();mobileNavigationPage.setDefaultTimeout(10000)
    try { await require('./mobile-navigation-regressions.cjs')({page:mobileNavigationPage,base,artifacts}) }
    finally { await mobileNavigationPage.close() }
    const navigationPage=await context.newPage()
    navigationPage.setDefaultTimeout(10000)
    try { await require('./node-navigation-regressions.cjs')({page:navigationPage,base,node,artifacts,headers,catalog}) }
    finally { await navigationPage.close() }
    const enrollmentPage=await context.newPage()
    enrollmentPage.setDefaultTimeout(10000)
    await enrollmentPage.setViewportSize({width:1440,height:1000})
    try { await require('./enrollment-regressions.cjs')({page:enrollmentPage,base,artifacts,catalog}) }
    finally { await enrollmentPage.close() }
    if(await page.getByTestId('node-detail-navigation').count())await page.getByRole('button',{name:'Back to home',exact:true}).click()
    await page.getByRole('button',{name:'Nodes',exact:true}).click();await page.locator('.nodes-dashboard').waitFor()
    await page.getByRole('button',{name:'Alerts',exact:true}).click();await page.locator('.alerts-page').waitFor()
    await page.locator('.account-trigger').click();await page.locator('.account-panel').getByRole('menuitem',{name:'Sign out',exact:true}).click();await page.locator('#login-title').waitFor()
    await page.goBack();await page.locator('#login-title').waitFor()
    assert.equal(await page.locator('#workspace-navigation').count(),0,'Back after logout must not restore a private page')
    assert.equal(updateChecks.updateSubmissions,1);assert.equal(updateChecks.rollbacks,1);assert.equal(submissions,2,'History restoration must not resubmit tasks')
    assert.deepEqual(pageErrors,[])
    console.log("Core/Web behavior checks passed.")
  }finally{
    if(browser)await browser.close()
    core.kill('SIGTERM')
    if(core.exitCode===null)await new Promise(resolve=>{core.once('exit',resolve);setTimeout(()=>{core.kill('SIGKILL');resolve()},5000).unref()})
    fs.rmSync(dataDir,{recursive:true,force:true})
  }
})().catch(error=>{console.error(error);process.exitCode=1})
