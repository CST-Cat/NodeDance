// Chromium's actual language preference survives reload. Dispatch the native
// notification that a browser sends when its preferred languages change.
const sessions=new WeakMap()
async function setBrowserLanguage(page,language,expected=language.toLowerCase().startsWith('zh')?'zh-CN':'en'){
 let session=sessions.get(page)
 if(!session){session=await page.context().newCDPSession(page);sessions.set(page,session)}
 await session.send('Network.setUserAgentOverride',{userAgent:await page.evaluate(()=>navigator.userAgent),acceptLanguage:language})
 await page.evaluate(()=>window.dispatchEvent(new Event('languagechange')))
 await page.waitForFunction(expected=>document.documentElement.lang===expected,expected)
}
module.exports={setBrowserLanguage}
