const assert=require('node:assert/strict'),path=require('node:path')
const {setBrowserLanguage}=require('./browser-language.cjs')
module.exports=async function({browser,base,credentials,artifacts}){
 for(const item of [{locale:'zh-CN',expected:'zh-CN'},{locale:'en-US',expected:'en'},{locale:'zh-TW',expected:'zh-CN'},{locale:'fr-FR',expected:'en'}]){
  const context=await browser.newContext({locale:item.locale})
  try{
   const page=await context.newPage();page.setDefaultTimeout(10000)
   await page.goto(base);await page.locator('#login-title').waitFor()
   assert.equal(await page.locator('html').getAttribute('lang'),item.expected)
   assert.equal(await page.locator('.language-selector,.language-options,.topbar-right').count(),0)
   for(const theme of ['light','dark'])for(const width of [390,1440]){
    await page.setViewportSize({width,height:900})
    await page.locator('.app-shell').evaluate((el,theme)=>{el.dataset.theme=theme;document.documentElement.dataset.theme=theme},theme)
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Header without language control fits viewport')
    await page.locator('.topbar').screenshot({path:path.join(artifacts,`browser-language-${item.locale}-${theme}-${width}.png`)})
   }
   const csrf=await(await context.request.get(base+'/api/v1/auth/csrf')).json()
   assert.equal((await context.request.post(base+'/api/v1/auth/login',{headers:{Origin:base,'X-CSRF-Token':csrf.token},data:credentials})).status(),200)
   await page.goto(base+'/#/operations');await page.locator('.operation-layout aside select').waitFor()
   assert.equal(await page.locator('html').getAttribute('lang'),item.expected)
   assert.equal(await page.locator('.language-selector,.language-options,.topbar-right').count(),0)
   await page.reload();await page.locator('.operation-layout aside select').waitFor()
   assert.equal(await page.locator('html').getAttribute('lang'),item.expected)
   await setBrowserLanguage(page,item.expected==='en'?'zh-CN':'en')
   assert.ok(page.url().endsWith('#/operations'),'Browser language change preserves the view')
   if(item.locale==='en-US'){
    const catalog=await(await context.request.get(base+'/api/v1/operations')).json()
    await require('./operation-locale-regressions.cjs')({page,base,catalog,artifacts})
    await require('./profile-language-regressions.cjs')({page,base,artifacts})
   }
  }finally{await context.close()}
 }
 console.log('Browser language passed: preference detection, Chinese variants, English fallback, reload, live languagechange, operation drafts and light/dark/mobile/desktop headers.')
}
