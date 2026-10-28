const assert=require('node:assert/strict')
module.exports=async function({page,admin,base}){
 const section=page.locator('.guest-settings')
 const field=section.locator('.publication-metric-field')
 const trigger=field.locator('button[aria-haspopup=listbox]')
 await trigger.waitFor();assert.equal(await trigger.count(),1)
 const initial=await(await admin.request.get(base+'/api/v1/guest/settings')).json()
 const original=initial.settings.metrics
 const keys=initial.metrics.map(metric=>metric.key)
 const select=async values=>{
  await trigger.click()
  const options=field.getByRole('option');assert.equal(await options.count(),keys.length)
  for(let index=0;index<keys.length;index++){
   const option=options.nth(index)
   if((await option.getAttribute('aria-selected')==='true')!==values.includes(keys[index]))await option.click()
  }
  await trigger.focus();await page.keyboard.press('Escape');await field.getByRole('listbox').waitFor({state:'detached'})
 }
 const save=async()=>{
  const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/guest/settings'&&r.request().method()==='PUT')
  await section.getByRole('button',{name:'Save display settings',exact:true}).click()
  const result=await response;assert.equal(result.status(),200)
  const saved=await result.json()
  await page.waitForFunction(()=>!document.querySelector('.publication-form fieldset').disabled)
  return saved
 }
 await select(keys)
 assert.deepEqual((await(await admin.request.get(base+'/api/v1/guest/settings')).json()).settings.metrics,original,'Metric edits remain a draft until saved')
 let saved=await save();assert.deepEqual([...saved.metrics].sort(),[...keys].sort())
 await page.reload();await trigger.waitFor()
 for(const width of [1440,390]){
  await page.setViewportSize({width,height:1000})
  const bounds=await field.boundingBox(),button=await trigger.boundingBox()
  assert.ok(bounds&&button&&button.height>0);assert.equal(bounds.height,button.height)
  assert.ok(button.x>=0&&button.x+button.width<=width)
  const clear=field.getByRole('button',{name:'Clear selections',exact:true}),clearBounds=await clear.boundingBox()
  assert.ok(clearBounds);assert.equal(clearBounds.width,22);assert.equal(clearBounds.height,22)
  await trigger.focus();await page.keyboard.press('ArrowDown');await field.getByRole('listbox').waitFor()
  assert.equal(await field.getByRole('option',{selected:true}).count(),keys.length)
  await page.keyboard.press('Escape');await field.getByRole('listbox').waitFor({state:'detached'})
  assert.ok(await trigger.evaluate(el=>el===document.activeElement))
 }
 await page.setViewportSize({width:1440,height:1000})
 const selectedHeight=(await trigger.boundingBox()).height
 await field.getByRole('button',{name:'Clear selections',exact:true}).click()
 assert.equal((await trigger.boundingBox()).height,selectedHeight)
 await trigger.click();assert.equal(await field.getByRole('option',{selected:true}).count(),0)
 await trigger.focus();await page.keyboard.press('Escape');await field.getByRole('listbox').waitFor({state:'detached'})
 assert.deepEqual([...(await(await admin.request.get(base+'/api/v1/guest/settings')).json()).settings.metrics].sort(),[...keys].sort(),'Clearing remains an unsaved draft')
 await select(original);saved=await save()
 assert.deepEqual([...saved.metrics].sort(),[...original].sort())
 console.log('Guest metric selector: Core options, draft/save/reload, compact controls and keyboard passed.')
 return saved
}
