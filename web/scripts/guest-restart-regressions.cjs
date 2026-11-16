const assert=require('node:assert/strict'),fs=require('node:fs')
const {request}=require('playwright')
const {startCore,signIn,eventually}=require('./core-fixture.cjs'),protocolAgent=require('./protocol-agent.cjs')
module.exports=async function guestRestart(){
 let core=await startCore(),session,kept,removed
 const visitor=await request.newContext()
 const names={kept:'Published survivor fixture',removed:'Published removed fixture',keptPrivate:'Private survivor host',removedPrivate:'Private removed host'}
 const publicRead=path=>eventually(async()=>{const response=await visitor.get(core.base+path);return response.status()===503?null:response},`Guest endpoint ${path} stayed busy`,40,250)
 const display=async()=>{const response=await publicRead('/api/v1/public/guest/display');assert.equal(response.status(),200,await response.text());const text=await response.text();return {text,body:JSON.parse(text)}}
 const entry=async()=>(await(await publicRead('/api/v1/public/guest')).json()).enabled
 const settings=async()=>(await(await session.call('guest.settings')).json()).settings
 const withdrawn=async(label,identity)=>{
  const policy=await settings(),policyText=JSON.stringify(policy)
  assert.ok(!policy.nodes.some(node=>node.id===identity.nodeId),`${label}: deleted node is still selected`)
  assert.ok(!policyText.includes(names.removed)&&!policyText.includes(identity.nodeId),`${label}: guest settings still describe the deleted node`)
  const preview=await(await session.call('guest.preview')).text();assert.ok(!preview.includes(names.removed),`${label}: preview still shows the deleted node`)
  const nodes=await(await session.call('nodes.list')).text();assert.ok(!nodes.includes(identity.nodeId),`${label}: deleted node is listed again`)
  return policy
 }
 const published=async(label,identity,publicIds)=>{
  assert.equal(await entry(),true,`${label}: guest entry is not enabled`)
  const shown=await display()
  assert.deepEqual(shown.body.nodes.map(node=>node.name),[names.kept],`${label}: published nodes`)
  assert.equal(shown.body.nodes[0].id,publicIds.kept,`${label}: surviving node changed its public identity`)
  for(const secret of [names.removed,publicIds.removed,identity.nodeId,identity.agentId,names.removedPrivate,names.keptPrivate,kept.nodeId,kept.agentId])assert.ok(!shown.text.includes(secret),`${label}: public display leaked ${secret}`)
  return shown.body
 }
 try{
  const credentials=core.credentials();session=await signIn(core.base,credentials)
  kept=await protocolAgent({session,displayName:names.keptPrivate});await kept.open();await kept.report({cpuPercent:21})
  removed=await protocolAgent({session,displayName:names.removedPrivate});await removed.open();await removed.report({cpuPercent:83})
  const identity={nodeId:removed.nodeId,agentId:removed.agentId}
  const initial=await settings();assert.equal(initial.enabled,false);assert.deepEqual(initial.nodes,[])
  const saved=await session.call('guest.configure',{data:{...initial,enabled:true,title:'Restart fixture',showStatus:true,metrics:['cpu.usage_percent'],historyHours:0,nodes:[{id:kept.nodeId,name:names.kept},{id:removed.nodeId,name:names.removed}],probes:[]}})
  assert.equal(saved.status(),200,await saved.text());const policy=await saved.json()
  assert.equal(await entry(),true)
  const before=await display();assert.deepEqual(before.body.nodes.map(node=>node.name).sort(),[names.kept,names.removed].sort())
  const publicIds={kept:before.body.nodes.find(node=>node.name===names.kept).id,removed:before.body.nodes.find(node=>node.name===names.removed).id}
  assert.equal(before.body.nodes.find(node=>node.name===names.removed).metrics['cpu.usage_percent'].value,83)
  const deleted=await session.call('nodes.delete',{parameters:{nodeId:removed.nodeId},data:{confirm:true}});assert.equal(deleted.status(),200,await deleted.text())
  const pruned=await withdrawn('after deletion',identity);assert.ok(pruned.revision>policy.revision);assert.deepEqual(pruned.nodes,[{id:kept.nodeId,name:names.kept}])
  await published('after deletion',identity,publicIds)
  const restart=async()=>{
   await session.close();session=null;const directory=core.directory,port=core.port;await core.stop()
   core=await startCore({directory,port});session=await signIn(core.base,credentials)
  }
  await restart()
  const restarted=await withdrawn('after restart',identity);assert.deepEqual(restarted,pruned,'Restart must keep the pruned guest policy unchanged')
  const offline=await published('after restart',identity,publicIds);assert.notEqual(offline.nodes[0].status,'online','A restarted Core must not report a disconnected node as online')
  await kept.open();await kept.report({cpuPercent:34})
  await eventually(async()=>{const preview=await(await session.call('guest.preview')).json();return preview.nodes.length===1&&preview.nodes[0].status==='online'&&preview.nodes[0].metrics['cpu.usage_percent'].value===34},'Surviving node did not publish fresh data after restart')
  const rejected=await session.call('guest.configure',{data:{...restarted,nodes:[...restarted.nodes,{id:identity.nodeId,name:names.removed}]}});assert.equal(rejected.status(),400,'An enabled guest display must not accept a deleted node')
  const paused=await session.call('guest.configure',{data:{...restarted,enabled:false,nodes:[...restarted.nodes,{id:identity.nodeId,name:names.removed}]}});assert.equal(paused.status(),200,await paused.text())
  const stale=await paused.json();assert.ok(stale.nodes.some(node=>node.id===identity.nodeId)&&(await settings()).nodes.some(node=>node.id===identity.nodeId),'Fixture needs a stored selection of the deleted node')
  assert.equal(await entry(),false);assert.equal((await publicRead('/api/v1/public/guest/display')).status(),404)
  await restart()
  const repaired=await withdrawn('after restart with a stale selection',identity);assert.ok(repaired.revision>stale.revision,'Startup pruning must advance the policy revision');assert.deepEqual(repaired.nodes,[{id:kept.nodeId,name:names.kept}]);assert.equal(repaired.enabled,false)
  assert.equal(await entry(),false);assert.equal((await publicRead('/api/v1/public/guest/display')).status(),404)
  const conflicting=await session.call('guest.configure',{data:{...stale,enabled:true,nodes:[{id:kept.nodeId,name:names.kept}]}});assert.equal(conflicting.status(),409,'The policy revision from before the restart must be stale')
  const resumed=await session.call('guest.configure',{data:{...repaired,enabled:true}});assert.equal(resumed.status(),200,await resumed.text())
  await published('after resuming publication',identity,publicIds)
  const reenrolled=await visitor.post(core.base+'/api/v1/agents/channel',{headers:{Authorization:'Bearer '+removed.credential},data:{version:1,type:'hello',payload:{agentId:identity.agentId,nodeId:identity.nodeId,agentVersion:'agent-v0.1.10',capabilities:removed.capabilities,permissions:{os:'linux',architecture:'amd64',effectiveUid:0,effectiveGid:0,supplementaryGroups:[0]}}}})
  assert.equal(reenrolled.status(),401,'A deleted node must not reconnect after restart')
  await published('after the deleted Agent retried',identity,publicIds)
  console.log('guest-restart-regressions passed.')
 }finally{
  if(kept)await kept.close().catch(()=>{});if(removed)await removed.close().catch(()=>{});if(session)await session.close().catch(()=>{})
  await visitor.dispose();await core.stop();fs.rmSync(core.directory,{recursive:true,force:true})
 }
}
