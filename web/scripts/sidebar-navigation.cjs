async function openNavigation(page) {
 const sidebar=page.locator('#workspace-navigation')
 if(await sidebar.evaluate(element=>element.inert))await page.locator('.navigation-toggle').click()
 await sidebar.evaluate(async element=>await Promise.all(element.getAnimations({subtree:true}).map(animation=>animation.finished.catch(()=>{}))))
}
async function selectNodeSection(page,name) {
 await openNavigation(page)
 await page.getByTestId('node-detail-navigation').getByRole('button',{name,exact:true}).click()
}
async function closeNavigation(page) {
 const sidebar=page.locator('#workspace-navigation')
 const open=await page.evaluate(()=>matchMedia('(max-width: 900px)').matches&&document.querySelector('.navigation-toggle')?.getAttribute('aria-expanded')==='true')
 if(open)await page.locator('.navigation-toggle').click()
 if(await sidebar.count())await sidebar.evaluate(async el=>await Promise.all(el.getAnimations({subtree:true}).map(a=>a.finished.catch(()=>{}))))
}
module.exports={openNavigation,closeNavigation,selectNodeSection}
