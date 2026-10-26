const {selectValue,selectedValue,readOptions}=require('./arc-controls.cjs')
const {setBrowserLanguage}=require('./browser-language.cjs')
const assert=require('node:assert/strict'),path=require('node:path')
module.exports=async function({page,base,catalog,artifacts}){
 await page.goto(base+'/#/operations');const source=page.locator('.operation-layout aside button[data-dropdown-field]'),form=page.locator('.operation-form')
 await source.waitFor();await setBrowserLanguage(page,'zh-CN')
 const labels=await readOptions(source).then(options=>options.map(option=>option.label))
 for(const operation of catalog.operations){const label=labels.find(text=>text.endsWith(' · '+operation.name));assert.ok(label&&/\p{Script=Han}/u.test(label),`Missing translated description: ${operation.name}`)}
 await page.locator('.operation-layout input[type=search]').fill('创建并部署')
 assert.deepEqual(await readOptions(source).then(options=>options.map(option=>option.value)),['compose.create'])
 await page.locator('.operation-layout input[type=search]').fill('')
 await selectValue(source,'nodes.save-asset')
 const accounting=form.locator('label').filter({hasText:/^trafficAccounting/}).locator('button[data-dropdown-field]')
 assert.equal(await readOptions(accounting).then(options=>options.find(option=>option.value==='up').label),'上传流量');assert.equal(await readOptions(accounting).then(options=>options.find(option=>option.value==='down').label),'下载流量')
 await selectValue(source,'commands.submit');await form.locator('label').filter({hasText:/^script/}).waitFor()
 assert.ok((await form.locator('label').filter({hasText:/^script/}).textContent()).includes('脚本'))
 const confirm=form.locator('label').filter({hasText:/^confirm/}).locator('button[data-dropdown-field]');assert.equal(await readOptions(confirm).then(options=>options.find(option=>option.value==='true').label),'是')
 await selectValue(confirm,'true');assert.equal(await selectedValue(confirm),'true')
 assert.ok((await form.textContent()).includes('重试同一操作意图时复用此键'))
 await setBrowserLanguage(page,'en')
 const englishDescription=catalog.operations.find(operation=>operation.name==='commands.submit').description
 await form.getByRole('heading',{level:2,name:englishDescription,exact:true}).waitFor()
 assert.equal(await readOptions(confirm).then(options=>options.find(option=>option.value==='true').label),'true');assert.equal(await selectedValue(confirm),'true')
 for(const [language,theme,width] of [['en','light',1440],['zh-CN','dark',390]]){
  await setBrowserLanguage(page,language);await page.setViewportSize({width,height:1000})
  await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1))

 }
 await setBrowserLanguage(page,'en')
 console.log("operation-locale-regressions passed.")
}
