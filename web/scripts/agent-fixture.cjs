const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path')
const {spawn,spawnSync,execFileSync}=require('node:child_process')
const {agentBinary,sleep,eventually}=require('./core-fixture.cjs')
function descendants(pid){
 const found=[],pending=[pid]
 while(pending.length){const current=pending.pop(),listed=spawnSync('pgrep',['-P',String(current)],{encoding:'utf8'}).stdout.split('\n').filter(Boolean).map(Number);for(const child of listed){found.push(child);pending.push(child)}}
 return found
}
module.exports=async function rootAgent({session,displayName,files,env={}}){
 const base=session.base,owned=!files,fileRoot=files??fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-e2e-files-'))
 const enrollment=await session.call('agents.enrollment',{data:{displayName,coreUrl:base}});assert.equal(enrollment.status(),201,await enrollment.text())
 const issued=await enrollment.json(),state=execFileSync('sudo',['mktemp','-d','/run/nodedance-e2e-agent-XXXXXX'],{encoding:'utf8'}).trim(),config=path.join(state,'agent.json')
 execFileSync('sudo',[agentBinary,'enroll','--server',base,'--dev','--config',config,'--token-stdin'],{input:issued.token+'\n',stdio:['pipe','pipe','pipe']})
 const agent={nodeId:issued.nodeId,displayName,state,config,files:fileRoot,logs:'',process:null}
 agent.reachable=async()=>(await session.call('files.list',{parameters:{nodeId:agent.nodeId},query:{path:'/'}})).ok()
 agent.start=async()=>{
  const variables=Object.entries({NODEDANCE_AGENT_FILE_ROOT:fileRoot,DOCKER_HOST:'unix:///nonexistent/nodedance-e2e-fixture.sock',NODEDANCE_STUN_SERVERS:'off',NODEDANCE_TCP_STUN_SERVERS:'off',...env}).map(([name,value])=>`${name}=${value}`)
  const child=spawn('sudo',['env',...variables,agentBinary,'run','--config',config],{stdio:['ignore','pipe','pipe']})
  child.stdout.on('data',data=>agent.logs+=data);child.stderr.on('data',data=>agent.logs+=data);agent.process=child
  await eventually(async()=>{if(child.exitCode!==null)throw new Error('Agent exited: '+agent.logs);return agent.reachable()},`${displayName} did not come online: ${agent.logs}`,300,100)
  return agent
 }
 agent.exited=()=>!agent.process||agent.process.exitCode!==null||agent.process.signalCode!==null
 agent.stop=async()=>{
  const child=agent.process;if(!child||agent.exited())return
  child.kill('SIGTERM');await Promise.race([new Promise(resolve=>child.once('exit',resolve)),sleep(8000)])
  if(!agent.exited())await agent.kill()
 }
 agent.kill=async()=>{
  const child=agent.process;if(!child||agent.exited())return
  const targets=[...descendants(child.pid),child.pid]
  spawnSync('sudo',['kill','-KILL',...targets.map(String)])
  await Promise.race([new Promise(resolve=>agent.exited()?resolve():child.once('exit',resolve)),sleep(8000)])
 }
 agent.remove=async()=>{await agent.stop();spawnSync('sudo',['rm','-rf',state]);if(owned)spawnSync('sudo',['rm','-rf',fileRoot])}
 return agent
}
