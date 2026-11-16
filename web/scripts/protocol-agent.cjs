const assert=require('node:assert/strict'),crypto=require('node:crypto')
const {request}=require('playwright')
const defaultCapabilities=['agent.heartbeat.v1','agent.os-permissions.v1','agent.metrics.v2']
module.exports=async function protocolAgent({session,displayName,capabilities=defaultCapabilities}){
 const base=session.base,api=await request.newContext()
 const enrollment=await session.call('agents.enrollment',{data:{displayName,coreUrl:base}});assert.equal(enrollment.status(),201,await enrollment.text())
 const issued=await enrollment.json(),credential=crypto.randomBytes(32).toString('hex'),authorization={Authorization:'Bearer '+credential}
 const enrolled=await api.post(base+'/api/v1/agents/enroll',{headers:{Authorization:'Enrollment '+issued.token},data:{requestId:crypto.randomUUID(),credential}});assert.equal(enrolled.status(),201,await enrolled.text())
 const identity=await enrolled.json()
 const agent={nodeId:identity.nodeId,agentId:identity.agentId,credential,displayName,capabilities,generation:0,sequence:0,channel:'',write:0,ack:'0'}
 agent.hello=()=>api.post(base+'/api/v1/agents/channel',{headers:authorization,data:{version:1,type:'hello',payload:{agentId:agent.agentId,nodeId:agent.nodeId,agentVersion:'agent-v0.1.10',capabilities,permissions:{os:'linux',architecture:'amd64',effectiveUid:0,effectiveGid:0,supplementaryGroups:[0]}}}})
 agent.open=async()=>{
  const opened=await agent.hello();assert.equal(opened.status(),201,await opened.text())
  agent.channel=base+'/api/v1/agents/channel/'+(await opened.json()).channelId;agent.write=0;agent.sequence=0
  const welcome=await api.get(agent.channel,{headers:authorization});assert.equal(welcome.status(),200,await welcome.text())
  const envelope=await welcome.json();assert.equal(envelope.type,'welcome');agent.generation=envelope.payload.generation;agent.ack=welcome.headers()['x-channel-sequence'];agent.welcome=envelope.payload
  return agent
 }
 agent.post=(envelope)=>api.post(agent.channel,{headers:{...authorization,'X-Channel-Sequence':String(++agent.write),'X-Channel-Ack':agent.ack},data:{version:1,generation:agent.generation,...envelope}})
 agent.receive=async(timeout=25000)=>{
  const response=await api.get(agent.channel,{headers:{...authorization,'X-Channel-Ack':agent.ack},timeout})
  if(response.status()===204)return null
  if(response.status()!==200)return {closed:true,status:response.status()}
  agent.ack=response.headers()['x-channel-sequence']
  return response.json()
 }
 agent.receiveType=async(type,attempts=6)=>{for(let attempt=0;attempt<attempts;attempt++){const envelope=await agent.receive();if(envelope?.closed)return envelope;if(envelope?.type===type)return envelope}throw new Error(`Protocol Agent did not receive ${type}`)}
 agent.heartbeat=async()=>{
  agent.sequence++
  const sent=await agent.post({type:'heartbeat',sequence:agent.sequence,payload:{capabilities}});assert.ok(sent.ok(),await sent.text())
  const ack=await agent.receiveType('heartbeat_ack');assert.equal(ack.sequence,agent.sequence)
 }
 agent.snapshot=({cpuPercent=12,at=new Date().toISOString(),mutate}={})=>{
  const known=value=>({status:'known',value,sampledAt:at,sampleAgeMillis:0}),unknown=reason=>({status:'unknown',reason,sampledAt:at,sampleAgeMillis:0})
  const snapshot={collectedAt:at,system:{hostname:known(displayName),os:known('linux'),architecture:known('amd64'),platform:known('debian'),platformFamily:known('debian'),platformVersion:known('13'),kernelVersion:known('fixture')},cpu:{usagePercent:known(cpuPercent),logicalCores:known(4)},memory:known({totalBytes:8e9,availableBytes:6e9,usedBytes:2e9,usedPercent:25}),network:{summary:known({receivedBytesPerSecond:8192,sentBytesPerSecond:4096}),interfaces:[],detailsTruncated:false},disk:{...unknown('unavailable'),mounts:[],detailsTruncated:false},uptime:known({seconds:86400,bootId:'protocol-fixture'})}
  if(mutate)mutate(snapshot,{known,unknown,at})
  return snapshot
 }
 agent.metrics=(options)=>agent.post({type:'metrics',sequence:agent.sequence,payload:agent.snapshot(options)})
 agent.report=async(options)=>{await agent.heartbeat();const sent=await agent.metrics(options);assert.ok(sent.ok(),await sent.text())}
 agent.channelStatus=async()=>(await api.post(agent.channel,{headers:{...authorization,'X-Channel-Sequence':String(agent.write+1),'X-Channel-Ack':agent.ack},data:{version:1,generation:agent.generation,type:'heartbeat',sequence:agent.sequence+1,payload:{capabilities}}})).status()
 agent.close=async()=>{if(agent.channel)await api.delete(agent.channel,{headers:authorization}).catch(()=>{});await api.dispose()}
 return agent
}
