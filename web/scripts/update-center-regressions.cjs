const assert=require('node:assert/strict'),path=require('node:path');
const {setBrowserLanguage}=require('./browser-language.cjs');
module.exports=async function({page,base,artifacts}){
 let response;
    let updateSubmissions=0,rollbacks=0,backupDeletes=0,deletionGate,deletionStarted,failDeletion=false
    const updateJob={id:'audit-update',component:'core',current:'core-v0.1.12',target:'core-v0.1.13',status:'succeeded',stage:'complete',started:'2026-10-12T00:00:00Z',finished:'2026-10-12T00:00:01Z'}
    let holdUpdateCheck, updateCheckStarted,checkRequests=0;
    await page.route('**/api/v1/updates/check?*',async route=>{checkRequests++;if(holdUpdateCheck){updateCheckStarted();await holdUpdateCheck;}return route.fulfill({json:{component:'core',current:'core-v0.1.12',latest:'core-v0.1.13',available:true,configured:true,mode:'docker',release:{url:'https://github.com/CST-Cat/NodeDance/releases/tag/core-v0.1.13',notes:'UI audit'}}})})
    await page.route('**/api/v1/updates/release?*',route=>{const component=new URL(route.request().url()).searchParams.get('component');return route.fulfill({json:{component,version:component+'-v0.1.13',url:'https://github.com/CST-Cat/NodeDance/releases/tag/'+component+'-v0.1.13',notes:'Published '+component+' changelog fixture'}})});
    await page.route('**/api/v1/updates/delete-backup',async route=>{assert.deepEqual(route.request().postDataJSON(),{component:'core',jobId:'audit-update',confirm:true});assert.ok(route.request().headers()['idempotency-key']);deletionStarted?.();if(deletionGate)await deletionGate;if(failDeletion){failDeletion=false;return route.fulfill({status:503,body:'Isolated cleanup failure'})}backupDeletes++;updateJob.backupDeleted=true;return route.fulfill({json:updateJob})});
    await page.route('**/api/v1/updates/status?*',route=>route.fulfill({json:{jobs:updateSubmissions?[updateJob]:[]}}))
    await page.route('**/api/v1/updates/apply',route=>{assert.deepEqual(route.request().postDataJSON(),{component:'core',confirm:true,version:'core-v0.1.14'});updateSubmissions++;return route.fulfill({json:updateJob})})
    await page.route('**/api/v1/updates/rollback',route=>{assert.deepEqual(route.request().postDataJSON(),{component:'core',confirm:true,jobId:'audit-update'});rollbacks++;return route.fulfill({json:updateJob})})
    await page.getByRole('button',{name:'Updates',exact:true}).click()
    const center=page.locator('.preferences-page')
    await center.locator('.update-target-actions .update-submit').waitFor()
    assert.equal(await center.getByRole('checkbox').count(),0,'Ordinary update must have no extra confirmation')
    assert.equal(checkRequests,0,'Opening updates does not run a runtime eligibility preflight');
    let releaseUpdateCheck;holdUpdateCheck=new Promise(resolve=>releaseUpdateCheck=resolve);const checkStarted=new Promise(resolve=>updateCheckStarted=resolve);
    await center.getByRole('button',{name:'Check updates',exact:true}).click();await checkStarted;
    assert.ok(await center.locator('.update-target-actions .update-submit').isEnabled(),'Optional checking does not block Core apply');
    const version=center.locator('.update-card input');await version.fill('core-v0.1.14');
    response=page.waitForResponse(r=>r.url().endsWith('/updates/apply'));
    await center.locator('.update-target-actions .update-submit').click();assert.equal((await response).status(),200);
    releaseUpdateCheck();holdUpdateCheck=undefined;
    await page.waitForResponse(r=>r.url().includes('/updates/check?'));
    assert.equal(await center.locator('.update-details').count(),0,'A stale optional check cannot replace the selected version');

    assert.equal(updateSubmissions,1)
    assert.ok(await center.getByRole('heading',{name:'Update history',exact:true}).isVisible());assert.equal(await center.locator('[aria-expanded]').filter({hasText:'Update history'}).count(),0);
    await center.locator('.update-job .settings-record-status[data-status=succeeded]').waitFor();
    assert.equal(await center.locator('.update-job progress').count(),0,'Stage-only updates do not fabricate percentages');assert.equal(await center.locator('.update-job').getByText('complete',{exact:true}).count(),0,'Completion does not repeat the finished badge');
    await center.locator('.update-job').getByRole('button',{name:'Rollback',exact:true}).click()
    const rollbackTrigger=center.locator('.update-job').getByRole('button',{name:'Rollback',exact:true});
    const rollbackDialog=page.getByRole('dialog',{name:'Rollback',exact:true});await rollbackDialog.waitFor();assert.equal(rollbacks,0,'Opening rollback must not submit');
    await rollbackDialog.getByRole('button',{name:'Cancel',exact:true}).click();await rollbackDialog.waitFor({state:'hidden'});await page.waitForFunction(()=>document.activeElement?.getAttribute('aria-haspopup')==='dialog');assert.equal(rollbacks,0);
    await rollbackTrigger.click();await rollbackDialog.waitFor();await page.keyboard.press('Escape');await rollbackDialog.waitFor({state:'hidden'});assert.equal(rollbacks,0);
    for(const [language,theme,width] of [['en','light',1440],['zh-CN','dark',320]]){
      await setBrowserLanguage(page,language);await page.setViewportSize({width,height:1000});await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme);
      const labels=language==='en'?{rollback:'Rollback',remove:'Delete backup',apply:'Update now',check:'Check updates'}:{rollback:'回滚',remove:'删除备份',apply:'立即更新',check:'检查更新'};
      const buttons=center.locator('.update-target-actions > button');await page.waitForFunction(labels=>{const buttons=[...document.querySelectorAll('.update-target-actions > button')];return buttons.length===labels.length&&buttons.every((button,index)=>button.textContent===labels[index])},[labels.apply,labels.check]);assert.deepEqual(await buttons.allTextContents(),[labels.apply,labels.check]);
      const boxes=await buttons.evaluateAll(buttons=>buttons.map(button=>button.getBoundingClientRect().toJSON()));assert.ok(boxes[0].right<=boxes[1].left+1&&Math.abs(boxes[0].top-boxes[1].top)<1,'Apply stays left of check');assert.equal(await center.getByText('The service will restart.',{exact:false}).count(),0);assert.equal(await center.locator('.update-confirm').count(),0);
      for(const [action,label] of [['rollback',labels.rollback],['backup',labels.remove]]){
        await center.locator('.update-job').getByRole('button',{name:label,exact:true}).click();const dialog=page.getByRole('dialog',{name:label,exact:true});await dialog.waitFor();
        await page.waitForTimeout(200);
        const bounds=await dialog.boundingBox();assert.ok(bounds.x>=0&&bounds.x+bounds.width<=width,'Confirmation fits viewport');
        const close=dialog.getByRole('button',{name:'Close dialog',exact:true});await close.hover();const closeStyle=await close.evaluate(el=>{const icon=el.querySelector('svg').getBoundingClientRect(),title=el.closest('[role=dialog]').querySelector('h2').getBoundingClientRect(),css=getComputedStyle(el);return {background:css.backgroundColor,border:css.borderColor,offset:icon.top+icon.height/2-title.top-title.height/2,width:el.getBoundingClientRect().width,height:el.getBoundingClientRect().height,titleFont:getComputedStyle(el.closest('[role=dialog]').querySelector('h2')).fontSize,titleLine:getComputedStyle(el.closest('[role=dialog]').querySelector('h2')).lineHeight,textLg:css.getPropertyValue('--text-lg'),leading:css.getPropertyValue('--leading-body'),margin:css.marginTop,titleHeight:title.height,svgTop:icon.top,buttonTop:el.getBoundingClientRect().top,svgHeight:icon.height}});assert.equal(closeStyle.background,'rgba(0, 0, 0, 0)');assert.equal(closeStyle.border,'rgba(0, 0, 0, 0)');assert.ok(Math.abs(closeStyle.offset)<1,JSON.stringify(closeStyle));assert.equal(closeStyle.width,44);assert.equal(closeStyle.height,44);
        await close.click();await dialog.waitFor({state:'hidden'});
      }
    }
    await setBrowserLanguage(page,'en');await page.setViewportSize({width:1440,height:1000});await rollbackTrigger.click();await rollbackDialog.waitFor();
    response=page.waitForResponse(r=>r.url().endsWith('/updates/rollback'));await rollbackDialog.getByRole('button',{name:'Rollback',exact:true}).click();await response;await rollbackDialog.waitFor({state:'hidden'});assert.equal(rollbacks,1);
    const backupButton=center.locator('.update-job').getByRole('button',{name:'Delete backup',exact:true});await backupButton.click();const backupDialog=page.getByRole('dialog',{name:'Delete backup',exact:true});await backupDialog.waitFor();await backupDialog.getByRole('button',{name:'Cancel',exact:true}).click();await backupDialog.waitFor({state:'hidden'});assert.equal(backupDeletes,0);
    await backupButton.click();await backupDialog.waitFor();await page.keyboard.press('Escape');await backupDialog.waitFor({state:'hidden'});assert.equal(backupDeletes,0);
    await backupButton.click();await backupDialog.waitFor();response=page.waitForResponse(r=>r.url().endsWith('/updates/delete-backup'));await backupDialog.getByRole('button',{name:'Delete backup',exact:true}).click();assert.equal((await response).status(),200);await backupDialog.waitFor({state:'hidden'});await center.locator('.update-job').waitFor({state:'detached'});await center.locator('.update-history').waitFor({state:'detached'});assert.equal(await center.locator('.update-history').count(),0,'No empty history section remains after cleanup');assert.equal(backupDeletes,1);assert.equal(await center.locator('.update-job button').count(),0,'A deleted backup cannot be offered for rollback');
    await center.locator('.update-changelog').getByText('Published agent changelog fixture',{exact:true}).waitFor();assert.equal(await center.locator('.update-changelog [aria-expanded],.update-history [aria-expanded]').count(),0,'Release notes and history are expanded independent sections');assert.ok((await center.locator('.update-component-control').boundingBox()).width<=160,'Component picker stays compact');

    for(const [theme,width] of [['light',1440],['dark',390]]){
      await page.setViewportSize({width,height:1000})
      await page.locator('.app-shell').evaluate((element,theme)=>{element.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
      await page.evaluate(()=>window.scrollTo(0,0));await page.waitForTimeout(350)
      assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Update layout must not overflow horizontally')

    }
    await page.setViewportSize({width:1440,height:1000});await page.waitForTimeout(350)

 for(const mode of ['no-preference','reduce']){
   await page.emulateMedia({reducedMotion:mode,colorScheme:'light'});updateJob.backupDeleted=false;
   const historyLoaded=page.waitForResponse(r=>r.url().includes('/updates/status?'));await page.reload();await historyLoaded;const record=center.locator('.update-job');await record.waitFor();
   await record.getByRole('button',{name:'Delete backup',exact:true}).click();const dialog=page.getByRole('dialog',{name:'Delete backup',exact:true});await dialog.waitFor();
   if(mode==='no-preference'){
     failDeletion=true;const failed=page.waitForResponse(r=>r.url().endsWith('/updates/delete-backup'));await dialog.getByRole('button',{name:'Delete backup',exact:true}).click();assert.equal((await failed).status(),503);await dialog.getByRole('alert').waitFor();assert.ok(await record.isVisible(),'A failed deletion retains its record');assert.equal(await record.locator('..').getAttribute('aria-hidden'),null);
   }
   let releaseDeletion;deletionGate=new Promise(resolve=>releaseDeletion=resolve);const started=new Promise(resolve=>deletionStarted=resolve);await dialog.getByRole('button',{name:'Delete backup',exact:true}).click();await started;await page.waitForTimeout(200);
   const before=await record.evaluate(el=>({x:new DOMMatrixReadOnly(getComputedStyle(el).transform).m41,opacity:getComputedStyle(el).opacity,height:el.parentElement.getBoundingClientRect().height}));assert.equal(before.x,0);assert.equal(before.opacity,'1');
   await page.evaluate(()=>{window.__updateExitFrames=[];window.__updateExitSampling=true;const sample=()=>{if(!window.__updateExitSampling)return;const row=document.querySelector('.update-job');if(row){const css=getComputedStyle(row);window.__updateExitFrames.push({x:new DOMMatrixReadOnly(css.transform).m41,opacity:Number(css.opacity),height:row.parentElement.getBoundingClientRect().height,inert:row.parentElement.inert})}requestAnimationFrame(sample)};requestAnimationFrame(sample)});
   releaseDeletion();deletionGate=undefined;deletionStarted=undefined;
   await record.waitFor({state:'detached'});await center.locator('.update-history').waitFor({state:'detached'});
   const frames=await page.evaluate(()=>{window.__updateExitSampling=false;return window.__updateExitFrames});
   if(mode==='no-preference'){assert.ok(frames.some(frame=>frame.x>1&&frame.opacity>0&&frame.opacity<1),JSON.stringify(frames));assert.ok(frames.some(frame=>frame.height>0&&frame.height<before.height-1),'The removed row closes its gap');assert.ok(frames.filter(frame=>frame.x>1).every(frame=>frame.inert),'An exiting record cannot be activated again')}else{assert.ok(frames.every(frame=>Math.abs(frame.x)<1),'Reduced motion avoids horizontal movement')}
   const reloaded=page.waitForResponse(r=>r.url().includes('/updates/status?'));await page.reload();await reloaded;await center.locator('.update-target-actions').waitFor();assert.equal(await center.locator('.update-job,.update-history').count(),0,'Cleaned records stay absent after reload');
 }
 console.log("update-center-regressions passed.");
 return {get updateSubmissions(){return updateSubmissions},get rollbacks(){return rollbacks}};
};
