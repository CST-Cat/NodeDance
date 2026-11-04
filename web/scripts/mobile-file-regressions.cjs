const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async({browser,context,url,artifacts,longGridName})=>{
 const phone=await browser.newContext({viewport:{width:390,height:844},isMobile:true,hasTouch:true,deviceScaleFactor:1,locale:'en-US',storageState:await context.storageState()})
 const page=await phone.newPage();page.setDefaultTimeout(15000)
 try{
  await page.goto(url)
  const ui=page.getByTestId('node-files'),toolbar=ui.getByRole('toolbar',{name:'File manager',exact:true})
  await ui.locator('[data-file-name="hello.txt"]').waitFor();await page.waitForFunction(()=>document.querySelector('[data-testid="node-files"]')?.dataset.compact==='true')
  for(const width of [390,320]){
   await page.setViewportSize({width,height:844});await toolbar.scrollIntoViewIfNeeded()
   assert.equal(await ui.locator('.file-browser-tree').count(),0,'Directory tree does not consume narrow-screen width')
   assert.ok(await ui.evaluate(el=>el.scrollWidth<=el.clientWidth+1),'File manager has no horizontal overflow')
   assert.ok(await toolbar.evaluate(el=>el.scrollWidth<=el.clientWidth+1),'Primary actions fit without horizontal scrolling')
   for(const name of ['Directories','Back','Parent directory','Search filenames','Upload files','More']){
    const control=toolbar.getByRole('button',{name,exact:true});await control.waitFor()
    const box=await control.boundingBox(),frame=await toolbar.boundingBox()
    assert.ok(box&&frame&&box.width>=36&&box.height>=36&&box.x>=frame.x-1&&box.x+box.width<=frame.x+frame.width+1,`${name} remains a usable visible touch target at ${width}`)
   }
   const list=await ui.locator('.file-browser-list').boundingBox(),body=await ui.locator('.file-browser-body').boundingBox()
   assert.ok(list&&body&&list.width>=body.width-3,'Files use the complete workspace width')
   const file=ui.locator('[data-file-name="'+longGridName+'"]');await file.waitFor()
   assert.ok(await file.locator('.file-name-end').isVisible(),'Long names retain their ending')
   assert.ok((await file.locator('.file-browser-entry-meta').innerText()).length>0,'Metadata is visible below the filename')
  }
  await page.setViewportSize({width:390,height:844})
  const rowActions=ui.getByRole('button',{name:'Actions: hello.txt',exact:true})
  await rowActions.tap();await page.getByRole('menuitem',{name:'File information',exact:true}).tap()
  const info=page.getByRole('dialog',{name:'File information',exact:true});await info.waitFor();await info.getByRole('button',{name:'Close dialog',exact:true}).tap();await info.waitFor({state:'detached'})
  await toolbar.getByRole('button',{name:'Directories',exact:true}).tap()
  const directories=page.getByRole('dialog',{name:'Directories',exact:true});await directories.waitFor()
  await directories.getByRole('treeitem',{name:'many',exact:true}).tap();await directories.waitFor({state:'detached'});await ui.locator('[data-file-name="entry-000.txt"]').waitFor()
  await ui.getByRole('checkbox',{name:'Select entry-000.txt',exact:true}).tap()
  await ui.locator('.file-browser-footer').getByRole('button',{name:'Actions',exact:true}).tap();await page.getByRole('menuitem',{name:'Copy',exact:true}).waitFor();await page.keyboard.press('Escape')
  for(const view of ['Grid','List']){
   await toolbar.getByRole('button',{name:'More',exact:true}).tap();await page.getByRole('menuitem',{name:'View',exact:true}).tap();await page.getByRole('menuitemcheckbox',{name:view,exact:true}).tap()
   if(view==='Grid')await ui.locator('.file-browser-grid').waitFor();else await ui.getByRole('table').waitFor()
   assert.ok(await ui.locator('.file-browser-footer').innerText().then(text=>text.includes('1 selected')),'View changes preserve selection')
  }
  await ui.getByRole('button',{name:'Page 2',exact:true}).tap();await ui.locator('[data-file-name="entry-050.txt"]').waitFor()
  await toolbar.getByRole('button',{name:'Parent directory',exact:true}).tap();await ui.locator('[data-file-name="hello.txt"]').waitFor()
  await toolbar.getByRole('button',{name:'More',exact:true}).tap();await page.getByRole('menuitem',{name:'New',exact:true}).tap();await page.getByRole('menuitem',{name:'New folder',exact:true}).tap();await page.getByRole('dialog',{name:'New folder',exact:true}).waitFor();await page.getByRole('dialog',{name:'New folder',exact:true}).getByRole('button',{name:'Cancel',exact:true}).tap()
  await page.locator('.app-shell').evaluate(el=>{el.dataset.theme='dark';document.documentElement.dataset.theme='dark'})
  await toolbar.getByRole('button',{name:'More',exact:true}).tap();const menu=page.getByRole('menu').first();await menu.waitFor();assert.notEqual(await menu.evaluate(el=>getComputedStyle(el).color),await menu.evaluate(el=>getComputedStyle(el).backgroundColor),'Mobile menu keeps theme contrast');await page.keyboard.press('Escape')
  if(process.env.NODEDANCE_FILE_MOBILE_REVIEW==='1')await page.screenshot({path:path.join(artifacts,'mobile-files-review.png')})
  await page.setViewportSize({width:320,height:844})
  await page.goto(new URL(url).origin+'/#/nodes');await page.getByTestId('node-files-shortcut').tap()
  const overlay=page.getByRole('dialog').getByTestId('node-files');await overlay.locator('[data-file-name]').first().waitFor()
  const overlayToolbar=overlay.getByRole('toolbar',{name:'File manager',exact:true})
  assert.ok(await overlayToolbar.evaluate(el=>el.scrollWidth<=el.clientWidth+1),'Overview file dialog keeps its toolbar inside a narrow viewport')
  await overlayToolbar.getByRole('button',{name:'More',exact:true}).tap();await page.getByRole('menuitem',{name:'New',exact:true}).tap();await page.getByRole('menuitem',{name:'New folder',exact:true}).tap()
  await page.getByRole('dialog',{name:'New folder',exact:true}).waitFor();await page.getByRole('dialog',{name:'New folder',exact:true}).getByRole('button',{name:'Cancel',exact:true}).tap()
  assert.equal(await page.getByRole('dialog').count(),1,'Canceling a nested mobile form returns to the file dialog')
  const mouse=await context.newPage()
  try{
   await mouse.setViewportSize({width:390,height:844});await mouse.goto(url)
   const files=mouse.getByTestId('node-files'),controls=files.getByRole('toolbar',{name:'File manager',exact:true})
   await files.locator('[data-file-name="hello.txt"]').waitFor()
   for(const name of ['Grid','List']){
    await files.getByRole('button',{name:longGridName,exact:true}).click({button:'right'});await mouse.keyboard.press('Escape');await mouse.locator('[data-slot=context-menu-content]').waitFor({state:'detached'})
    await controls.getByRole('button',{name:'More',exact:true}).click();await mouse.getByRole('menuitem',{name:'View',exact:true}).click();
    const item=mouse.getByRole('menuitemcheckbox',{name,exact:true}),target=await item.boundingBox();assert.ok(target,'View choice is visible')
    await mouse.mouse.move(target.x+target.width/2,target.y+target.height/2,{steps:8});await item.click()
    if(name==='Grid')await files.locator('.file-browser-grid').waitFor();else await files.getByRole('table').waitFor()
   }
  }finally{await mouse.close()}
  console.log('Mobile files: touch navigation, full-width list, long names, metadata, row and batch actions, view selection, pagination, new-folder dialog and themed menus passed at 320/390px.')
 }finally{await phone.close()}
}
