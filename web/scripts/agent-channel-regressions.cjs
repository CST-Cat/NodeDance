const assert=require('node:assert/strict'),fs=require('node:fs'),crypto=require('node:crypto')
const {request}=require('playwright')
const {startCore,signIn,eventually}=require('./core-fixture.cjs'),protocolAgent=require('./protocol-agent.cjs')
module.exports=async function agentChannel(){
 const core=await startCore();let session,agent,other
 try{
  session=await signIn(core.base,core.credentials())
  agent=await protocolAgent({session,displayName:'Channel boundary fixture'});await agent.open();await agent.report({cpuPercent:37})
  const metrics=async()=>(await(await session.call('nodes.metrics',{parameters:{nodeId:agent.nodeId}})).json())
  const accepted=await eventually(async()=>{const value=await metrics();return value.metrics?.cpu?.usagePercent?.value===37&&value},'Accepted sample was not published')
  const rejected=async(label,options)=>{
   await agent.heartbeat();const sent=await agent.metrics(options);assert.ok(sent.status()===204||sent.status()===410,`${label}: ${sent.status()}`)
   await eventually(async()=>(await agent.channelStatus())===410,`${label}: Core kept the channel open after an invalid report`)
   const current=await metrics();assert.equal(current.metrics.cpu.usagePercent.value,37,`${label}: invalid report replaced the accepted sample`);assert.equal(current.sequence,accepted.sequence,`${label}: invalid report advanced the accepted sequence`);assert.equal(current.generation,accepted.generation)
   assert.ok(!JSON.stringify(current).includes('rejected-fixture'),`${label}: rejected values were published`)
   await agent.open()
  }
  await rejected('cpu above range',{cpuPercent:250})
  await rejected('negative cpu',{cpuPercent:-1})
  await rejected('known metric without value',{mutate:snapshot=>{delete snapshot.cpu.logicalCores.value}})
  await rejected('unknown metric with value',{mutate:(snapshot,{at})=>{snapshot.cpu.logicalCores={status:'unknown',reason:'unavailable',value:4,sampledAt:at,sampleAgeMillis:0}}})
  await rejected('not sampled disk with mounts',{mutate:(snapshot,{known})=>{snapshot.disk={status:'unknown',reason:'not_sampled',sampleAgeMillis:0,mounts:[{device:'/dev/rejected-fixture',mountpoint:'/rejected-fixture',filesystem:'ext4',usage:known({totalBytes:100,availableBytes:50,usedBytes:50,usedPercent:50})}],detailsTruncated:false}}})
  await rejected('unavailable disk with mounts',{mutate:(snapshot,{known,at})=>{snapshot.disk={status:'unknown',reason:'unavailable',sampledAt:at,sampleAgeMillis:0,mounts:[{device:'/dev/rejected-fixture',mountpoint:'/rejected-fixture',filesystem:'ext4',usage:known({totalBytes:100,availableBytes:50,usedBytes:50,usedPercent:50})}],detailsTruncated:false}}})
  await rejected('inconsistent memory',{mutate:(snapshot,{known})=>{snapshot.memory=known({totalBytes:100,availableBytes:500,usedBytes:900,usedPercent:25})}})
  await rejected('missing collection time',{mutate:snapshot=>{delete snapshot.collectedAt}})
  await rejected('oversized boot identity',{mutate:(snapshot,{known})=>{snapshot.uptime=known({seconds:1,bootId:'rejected-fixture'.repeat(16)})}})
  await rejected('unknown field',{mutate:snapshot=>{snapshot.rejectedFixture=true}})
  await agent.heartbeat();const unsequenced=await agent.post({type:'metrics',sequence:0,payload:agent.snapshot({cpuPercent:99})});assert.ok(unsequenced.status()===204||unsequenced.status()===410)
  await eventually(async()=>(await agent.channelStatus())===410,'Core kept the channel open after an unsequenced report');assert.equal((await metrics()).metrics.cpu.usagePercent.value,37);await agent.open()
  await agent.heartbeat();const stale=await agent.post({type:'metrics',generation:agent.generation+7,sequence:agent.sequence,payload:agent.snapshot({cpuPercent:98})});assert.ok(stale.status()===204||stale.status()===410)
  await eventually(async()=>(await agent.channelStatus())===410,'Core kept the channel open after a forged generation');assert.equal((await metrics()).metrics.cpu.usagePercent.value,37);await agent.open()
  await agent.report({cpuPercent:41});await eventually(async()=>(await metrics()).metrics.cpu.usagePercent.value===41,'Valid report after rejection was not accepted')
  other=await protocolAgent({session,displayName:'Channel neighbour fixture'});await other.open();await other.report({cpuPercent:19})
  const stranger=await request.newContext()
  try{
   const crossed=await stranger.post(agent.channel,{headers:{Authorization:'Bearer '+other.credential,'X-Channel-Sequence':'1'},data:{version:1,type:'heartbeat',generation:agent.generation,sequence:99,payload:{capabilities:agent.capabilities}}});assert.equal(crossed.status(),410,'Another Agent credential cannot write into this channel')
   const anonymous=await stranger.post(agent.channel,{data:{version:1,type:'heartbeat',generation:agent.generation,sequence:99,payload:{capabilities:agent.capabilities}}});assert.equal(anonymous.status(),401)
   const forged=await stranger.post(core.base+'/api/v1/agents/channel',{headers:{Authorization:'Bearer '+crypto.randomBytes(32).toString('hex')},data:{version:1,type:'hello',payload:{agentId:agent.agentId,nodeId:agent.nodeId,agentVersion:'agent-v0.1.10',capabilities:agent.capabilities}}});assert.equal(forged.status(),401)
   const browser=await stranger.post(core.base+'/api/v1/agents/channel',{headers:{Authorization:'Bearer '+agent.credential,Origin:core.base},data:{version:1,type:'hello',payload:{}}});assert.equal(browser.status(),403,'Browser origins cannot open Agent channels')
   const impersonated=await stranger.post(core.base+'/api/v1/agents/channel',{headers:{Authorization:'Bearer '+other.credential},data:{version:1,type:'hello',payload:{agentId:agent.agentId,nodeId:agent.nodeId,agentVersion:'agent-v0.1.10',capabilities:agent.capabilities,permissions:{os:'linux',architecture:'amd64',effectiveUid:0,effectiveGid:0,supplementaryGroups:[0]}}}})
   if(impersonated.status()===201){const channel=core.base+'/api/v1/agents/channel/'+(await impersonated.json()).channelId;const answer=await stranger.get(channel,{headers:{Authorization:'Bearer '+other.credential}});assert.ok(answer.status()!==200||(await answer.json()).type!=='welcome','A credential cannot greet as another Agent')}
   assert.equal((await metrics()).metrics.cpu.usagePercent.value,41)
   const revoke=await session.call('agents.revoke',{parameters:{agentId:agent.agentId},data:{}});assert.equal(revoke.status(),200,await revoke.text());assert.equal((await revoke.json()).status,'revoked')
   assert.equal(await agent.channelStatus(),401,'Revoked credential must be rejected on the open channel')
   assert.equal((await agent.hello()).status(),401,'Revoked credential cannot open a new channel')
   const polled=await stranger.get(agent.channel,{headers:{Authorization:'Bearer '+agent.credential}});assert.equal(polled.status(),401)
   const again=await session.call('agents.revoke',{parameters:{agentId:agent.agentId},data:{}});assert.equal(again.status(),409)
   const nodes=(await(await session.call('nodes.list')).json()).nodes;assert.equal(nodes.find(node=>node.nodeId===agent.nodeId).status,'revoked')
   await other.report({cpuPercent:23});assert.equal((await(await session.call('nodes.metrics',{parameters:{nodeId:other.nodeId}})).json()).metrics.cpu.usagePercent.value,23,'Revoking one Agent leaves other Agents connected')
  }finally{await stranger.dispose()}
  console.log('agent-channel-regressions passed.')
 }finally{
  if(agent)await agent.close().catch(()=>{});if(other)await other.close().catch(()=>{});if(session)await session.close().catch(()=>{})
  await core.stop();fs.rmSync(core.directory,{recursive:true,force:true})
 }
}
