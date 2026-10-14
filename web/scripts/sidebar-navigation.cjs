async function openNavigation(page) {
 const sidebar=page.locator('#workspace-navigation')
 if(await sidebar.evaluate(element=>element.inert))await page.locator('.navigation-toggle').click()
 await sidebar.evaluate(async element=>await Promise.all(element.getAnimations({subtree:true}).map(animation=>animation.finished.catch(()=>{}))))
}
async function selectNodeSection(page,name) {
 await openNavigation(page)
 await page.getByTestId('node-detail-navigation').getByRole('button',{name,exact:true}).click()
}
module.exports={openNavigation,selectNodeSection}
