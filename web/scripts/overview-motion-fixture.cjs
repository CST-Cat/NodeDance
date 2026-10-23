// Protocol-only fixtures for an isolated real Core. No Agent process, host
// command, Docker connection, file service or external resource is used.
const assert=require('node:assert/strict'),crypto=require('node:crypto')
module.exports=async function({context,base,count=3,cpuPercent}) {
 const headers={Origin:base,'X-CSRF-Token':(await(await context.request.get(base+'/api/v1/auth/csrf')).json()).token}
 const reports=[],errors=[]
 for(let index=0;index<count;index++) {
  const displayName=`Motion fixture ${index+1}`
  const enrollment=await context.request.post(base+'/api/v1/agents/enrollments',{headers,data:{displayName,coreUrl:base}});assert.equal(enrollment.status(),201)
  const credential=crypto.randomBytes(32).toString('hex'),token=(await enrollment.json()).token,agentHeaders={Authorization:'Bearer '+credential}
  const enrolled=await context.request.post(base+'/api/v1/agents/enroll',{headers:{Authorization:'Enrollment '+token},data:{requestId:crypto.randomUUID(),credential}});assert.equal(enrolled.status(),201)
  const identity=await enrolled.json(),capabilities=['agent.heartbeat.v1','agent.os-permissions.v1','agent.metrics.v2']
  const opened=await context.request.post(base+'/api/v1/agents/channel',{headers:agentHeaders,data:{version:1,type:'hello',payload:{agentId:identity.agentId,nodeId:identity.nodeId,agentVersion:'agent-v0.1.10',capabilities,permissions:{os:'linux',architecture:'amd64',effectiveUid:0,effectiveGid:0,supplementaryGroups:[0]}}}});assert.equal(opened.status(),201)
  const channel=base+'/api/v1/agents/channel/'+(await opened.json()).channelId
  const welcome=await context.request.get(channel,{headers:agentHeaders}),generation=(await welcome.json()).payload.generation
  let ack=welcome.headers()['x-channel-sequence'],write=0,sequence=0
  reports.push(async()=>{
   sequence++
   const post=async(type,payload)=>{const response=await context.request.post(channel,{headers:{...agentHeaders,'X-Channel-Sequence':String(++write),'X-Channel-Ack':ack},data:{version:1,type,generation,sequence,payload}});assert.ok(response.ok(),await response.text())}
   await post('heartbeat',{capabilities})
   const received=await context.request.get(channel,{headers:{...agentHeaders,'X-Channel-Ack':ack}});assert.ok(received.ok());ack=received.headers()['x-channel-sequence']
   const at=new Date().toISOString(),known=value=>({status:'known',value,sampledAt:at,sampleAgeMillis:0}),unknown=()=>({status:'unknown',reason:'unavailable',sampledAt:at,sampleAgeMillis:0})
   await post('metrics',{collectedAt:at,system:{hostname:known(displayName),os:known('linux'),architecture:known('amd64'),platform:known('debian'),platformFamily:known('debian'),platformVersion:known('13'),kernelVersion:known('fixture')},cpu:{usagePercent:known(cpuPercent ?? 12+(index%3)*7+sequence%5),logicalCores:known(4)},memory:known({totalBytes:8e9,availableBytes:6e9,usedBytes:2e9,usedPercent:25}),network:{summary:known({receivedBytesPerSecond:8192+sequence*100,sentBytesPerSecond:4096}),interfaces:[],detailsTruncated:false},disk:{...unknown(),mounts:[],detailsTruncated:false},uptime:known({seconds:86400,bootId:'motion-fixture'})})
  })
 }
 let pending=Promise.all(reports.map(report=>report()));await pending
 let busy=false
 const timer=setInterval(()=>{if(busy)return;busy=true;pending=Promise.all(reports.map(report=>report())).catch(e=>errors.push(String(e))).finally(()=>{busy=false})},2000)
 return {errors,close:async()=>{clearInterval(timer);await pending}}
}
