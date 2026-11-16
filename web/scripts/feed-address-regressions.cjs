const assert=require('node:assert/strict'),fs=require('node:fs'),http=require('node:http')
const {startCore,signIn,eventually,sqlite}=require('./core-fixture.cjs')
module.exports=async function feedAddress(){
 let hits=0
 const listener=http.createServer((request,response)=>{hits++;response.setHeader('Content-Type','application/rss+xml');response.end('<?xml version="1.0"?><rss version="2.0"><channel><title>Loopback fixture</title><item><title>Loopback entry</title><link>http://127.0.0.1/entry</link><guid>loopback-entry</guid></item></channel></rss>')})
 await new Promise(resolve=>listener.listen(0,'127.0.0.1',resolve))
 const port=listener.address().port,core=await startCore();let session
 try{
  session=await signIn(core.base,core.credentials())
  const input=(name,url,extra={})=>({name,url,intervalMinutes:60,enabled:true,keywords:[],channelIds:[],notificationMode:'off',digestMinutes:60,...extra})
  const list=async()=>(await(await session.call('feeds.list')).json()).subscriptions
  const get=async id=>(await(await session.call('feeds.get',{parameters:{id}})).json())
  const stored=()=>sqlite(core.database,'SELECT COUNT(*) FROM feed_subscriptions')[0][0]
  for(const [label,url] of [
   ['loopback listener port',`http://127.0.0.1:${port}/feed`],
   ['localhost listener port',`http://localhost:${port}/feed`],
   ['IPv6 loopback listener port',`http://[::1]:${port}/feed`],
   ['embedded credentials','http://reader:secret@127.0.0.1/feed'],
   ['fragment','http://127.0.0.1/feed#entries'],
   ['file scheme','file:///etc/hostname'],
   ['gopher scheme','gopher://127.0.0.1/feed'],
   ['missing host','http:///feed'],
  ]){
   const rejected=await session.call('feeds.create',{data:input(label,url)})
   assert.equal(rejected.status(),400,`${label}: subscription URL was accepted`)
  }
  assert.deepEqual(await list(),[],'Rejected subscription URLs must not be stored');assert.equal(stored(),0);assert.equal(hits,0,'Rejected subscription URLs must not be fetched')
  const targets=[
   ['IPv4 loopback','http://127.0.0.1/feed'],
   ['IPv6 loopback','http://[::1]/feed'],
   ['IPv4-mapped loopback','http://[::ffff:127.0.0.1]/feed'],
   ['unspecified address','http://0.0.0.0/feed'],
   ['private network','http://10.0.0.1/feed'],
   ['link-local metadata','http://169.254.169.254/latest/meta-data/'],
   ['unique local IPv6','http://[fd00::1]/feed'],
   ['HTTPS loopback','https://127.0.0.2/feed'],
  ]
  if(/^\s*(127\.0\.0\.1|::1)\s[^#\n]*\blocalhost\b/m.test(fs.readFileSync('/etc/hosts','utf8')))targets.push(['localhost name','http://localhost/feed'])
  const subscriptions=[]
  for(const [label,url] of targets){
   const created=await session.call('feeds.create',{data:input(label,url)});assert.equal(created.status(),200,`${label}: ${await created.text()}`)
   subscriptions.push({label,url,...await created.json()})
  }
  const refused=async(subscription,failures)=>{
   const current=await eventually(async()=>{const value=await get(subscription.id);assert.notEqual(value.status,'succeeded',`${subscription.label}: non-public target was fetched`);return value.status==='failed'&&value.failures>=failures&&value},`${subscription.label}: fetch did not end as refused`,360,250)
   assert.equal(current.failures,failures,`${subscription.label}: unexpected attempt count`)
   assert.equal(current.error,'subscription fetch failed',`${subscription.label}: refusal must be visible without exposing the target`)
   assert.ok(!current.error.includes(new URL(subscription.url).hostname.replace(/^\[|\]$/g,'')))
   assert.equal(current.lastSuccess,'0001-01-01T00:00:00Z',`${subscription.label}: refused target must never record a success`)
   assert.ok(new Date(current.lastChecked).getTime()>0);assert.ok(new Date(current.nextCheck).getTime()>new Date(current.lastChecked).getTime())
   const items=await session.call('feeds.items',{parameters:{id:subscription.id}});assert.equal(items.status(),200,await items.text());assert.deepEqual((await items.json()).items,[],`${subscription.label}: refused target produced entries`)
   return current
  }
  for(const subscription of subscriptions)await refused(subscription,1)
  for(const subscription of subscriptions){
   const queued=await session.call('feeds.refresh',{parameters:{id:subscription.id},data:{}});assert.equal(queued.status(),202,`${subscription.label}: ${await queued.text()}`)
   const body=await queued.json();assert.equal(body.status,'queued');assert.equal(body.error,'')
  }
  const settled=[];for(const subscription of subscriptions)settled.push(await refused(subscription,2))
  const moved=settled[0]
  const retarget=await session.call('feeds.update',{parameters:{id:moved.id},data:input(moved.name,`http://127.0.0.1:${port}/feed`,{revision:moved.revision})});assert.equal(retarget.status(),400,'Updating a subscription to a non-standard port must be rejected')
  assert.equal((await get(moved.id)).url,subscriptions[0].url)
  assert.equal(sqlite(core.database,'SELECT COUNT(*) FROM feed_items')[0][0],0);assert.equal(sqlite(core.database,'SELECT COUNT(*) FROM feed_subscriptions WHERE last_success<>0')[0][0],0)
  assert.equal(stored(),subscriptions.length);assert.equal(hits,0,'Local listener must never receive a subscription request')
  console.log('feed-address-regressions passed.')
 }finally{
  listener.close();if(session)await session.close().catch(()=>{});await core.stop();fs.rmSync(core.directory,{recursive:true,force:true})
 }
}
