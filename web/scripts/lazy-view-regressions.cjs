const assert=require('node:assert/strict')
module.exports=async({page,base,node})=>{
 let blocked=0
 const chunk='**/assets/NodeServiceProbes-*.js'
 await page.route(chunk,route=>{blocked++;return route.abort('failed')})
 await page.goto(base+'/#/nodes?node='+node+'&section=probes')
 await page.getByRole('alert').filter({hasText:'This view could not be loaded.'}).waitFor()
 assert.ok(blocked>0)
 assert.equal(await page.locator('.app-shell[data-authenticated=true]').count(),1)
 await page.unroute(chunk)
 await page.getByRole('button',{name:'Reload page',exact:true}).click()
 await page.getByTestId('service-probes').waitFor()
 console.log('Lazy view load failure and recovery passed.')
}
