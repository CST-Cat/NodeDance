const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),net=require('node:net'),crypto=require('node:crypto')
const {spawn,spawnSync,execFileSync}=require('node:child_process')
const {agentBinary,startCore,signIn,eventually,sleep}=require('./core-fixture.cjs'),rootAgent=require('./agent-fixture.cjs')
const rootOnly=/requires root privileges for full host management/,secureOnly=/Agent connections require HTTPS; HTTP is allowed only for a literal loopback address in development mode/,authorityOnly=/server URL must contain only scheme and authority/
module.exports=async function agentEntry(){
 let connections=0
 const listener=net.createServer(socket=>{connections++;socket.on('error',()=>{});socket.destroy()})
 await new Promise(resolve=>listener.listen(0,'127.0.0.1',resolve))
 const port=listener.address().port,plain=`http://127.0.0.1:${port}`,token=crypto.randomBytes(32).toString('hex')
 const shared=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-agent-entry-'));fs.chmodSync(shared,0o755)
 const unprivilegedBinary=path.join(shared,'nodedance-agent');fs.copyFileSync(agentBinary,unprivilegedBinary);fs.chmodSync(unprivilegedBinary,0o755)
 const state=execFileSync('sudo',['mktemp','-d','/run/nodedance-e2e-entry-XXXXXX'],{encoding:'utf8'}).trim(),states=[state]
 const environment=['env','NODEDANCE_STUN_SERVERS=off','NODEDANCE_TCP_STUN_SERVERS=off','DOCKER_HOST=unix:///nonexistent/nodedance-e2e-fixture.sock','NODEDANCE_AGENT_FILE_ACCESS=disabled']
 const asRoot=(args,input)=>spawnSync('sudo',[...environment,agentBinary,...args],{input,encoding:'utf8',timeout:20000})
 const asNobody=(args,input)=>spawnSync('sudo',['setpriv','--reuid=65534','--regid=65534','--clear-groups',...environment,unprivilegedBinary,...args],{input,encoding:'utf8',timeout:20000})
 const exists=file=>spawnSync('sudo',['test','-e',file]).status===0||spawnSync('sudo',['test','-L',file]).status===0
 const mode=file=>execFileSync('sudo',['stat','--format=%a %u',file],{encoding:'utf8'}).trim()
 const refused=(label,result,pattern)=>{
  assert.equal(result.error,undefined,`${label}: Agent kept running`);assert.notEqual(result.status,0,`${label}: Agent accepted the request: ${result.stdout}`)
  assert.match(result.stderr+result.stdout,pattern,`${label}: refused for another reason`);assert.ok(!(result.stdout+result.stderr).includes(token),`${label}: output repeated the enrollment credential`)
  assert.equal(connections,0,`${label}: Agent contacted the Core before refusing`)
 }
 const core=await startCore();let session,agent,running
 try{
  assert.equal(asNobody(['version']).status,0,'Fixture cannot run the Agent binary without privileges')
  const unprivilegedConfig=path.join(shared,'state','agent.json')
  refused('Unprivileged enrollment',asNobody(['enroll','--server',plain,'--dev','--config',unprivilegedConfig,'--token-stdin'],token+'\n'),rootOnly)
  refused('Unprivileged runtime',asNobody(['run','--config',unprivilegedConfig]),rootOnly)
  refused('Unprivileged recovery',asNobody(['recover','--config',unprivilegedConfig]),rootOnly)
  refused('Unprivileged runtime with a readable root config',asNobody(['run','--config',path.join(state,'agent.json')]),rootOnly)
  assert.ok(!fs.existsSync(path.join(shared,'state')),'A refused unprivileged Agent must not create a state directory')

  const config=path.join(state,'agent.json')
  const enroll=(server,flags=[],input=token+'\n')=>asRoot(['enroll','--server',server,...flags,'--config',config,'--token-stdin'],input)
  for(const [label,server,flags,pattern] of [
   ['plain HTTP to loopback without --dev',plain,[],secureOnly],
   ['plain HTTP to IPv6 loopback without --dev',`http://[::1]:${port}`,[],secureOnly],
   ['plain HTTP to a host name with --dev',`http://localhost:${port}`,['--dev'],secureOnly],
   ['plain HTTP to a host name without --dev',`http://localhost:${port}`,[],secureOnly],
   ['another scheme',`ftp://127.0.0.1:${port}`,['--dev'],/server URL must use HTTPS/],
   ['a URL without a scheme',`127.0.0.1:${port}`,['--dev'],/server URL must/],
   ['a URL without a host','https://',['--dev'],authorityOnly],
   ['a URL with a path',`${plain}/api`,['--dev'],authorityOnly],
   ['a URL with a query',`${plain}?next=1`,['--dev'],authorityOnly],
   ['a URL with a fragment',`${plain}#fragment`,['--dev'],authorityOnly],
   ['a URL with credentials',`http://operator:secret@127.0.0.1:${port}`,['--dev'],authorityOnly],
   ['a zero port','http://127.0.0.1:0',['--dev'],/server URL has an invalid port/],
   ['an oversized port','http://127.0.0.1:99999',['--dev'],/server URL has an invalid port|only scheme and authority/],
  ]){refused(`Enrollment against ${label}`,enroll(server,flags),pattern);assert.ok(!exists(config),`${label}: refused enrollment stored a credential file`)}
  refused('Enrollment without --server',asRoot(['enroll','--dev','--config',config,'--token-stdin'],token+'\n'),/--server is required/)
  refused('Enrollment without --token-stdin',asRoot(['enroll','--server',plain,'--dev','--config',config],token+'\n'),/enrollment credentials are never accepted as command-line arguments/)
  refused('Enrollment with the credential as an argument',asRoot(['enroll','--server',plain,'--dev','--config',config,'--token-stdin','argument-credential-'.repeat(3)],''),/unexpected enroll arguments/)
  refused('Enrollment with an empty credential',enroll(plain,['--dev'],'\n'),/enrollment token input is empty or invalid/)
  refused('Enrollment with a short credential',enroll(plain,['--dev'],'short\n'),/enrollment token input is empty or invalid/)
  refused('Enrollment with a spaced credential',enroll(plain,['--dev'],token.slice(0,32)+' '+token.slice(32)+'\n'),/enrollment token input is empty or invalid/)
  assert.ok(!exists(config),'Refused enrollments must not store a credential file');assert.equal(execFileSync('sudo',['ls','-A',state],{encoding:'utf8'}).trim(),'','Refused enrollments must leave the state directory empty')

  const valid={schema:1,server:plain,shell:'/bin/sh',development:true,credential:crypto.randomBytes(32).toString('hex'),nodeId:crypto.randomUUID(),agentId:crypto.randomUUID(),createdAt:new Date().toISOString()}
  const draft=path.join(shared,'draft.json'),install=(file,content,permissions='0600')=>{fs.writeFileSync(draft,content);execFileSync('sudo',['install','-m',permissions,'-o','root','-g','root',draft,file]);fs.rmSync(draft)}
  const fresh=()=>{const directory=execFileSync('sudo',['mktemp','-d','/run/nodedance-e2e-entry-XXXXXX'],{encoding:'utf8'}).trim();states.push(directory);return directory}
  const run=file=>asRoot(['run','--config',file])
  for(const [label,content,pattern] of [
   ['plain HTTP without development mode',JSON.stringify({...valid,development:false}),secureOnly],
   ['plain HTTP to a host name',JSON.stringify({...valid,server:`http://localhost:${port}`}),secureOnly],
   ['a server URL with a path',JSON.stringify({...valid,server:plain+'/api'}),authorityOnly],
   ['trailing data',JSON.stringify(valid)+' '+JSON.stringify(valid),/Agent config has trailing data/],
   ['an unknown field',JSON.stringify({...valid,fileRoot:'/'}),/decode Agent config/],
   ['a short credential',JSON.stringify({...valid,credential:'abcd'}),/Agent config is incomplete or unsupported/],
   ['another schema',JSON.stringify({...valid,schema:2}),/Agent config is incomplete or unsupported/],
   ['a relative shell',JSON.stringify({...valid,shell:'sh'}),/Agent config is incomplete or unsupported/],
   ['half an identity',JSON.stringify({...valid,agentId:undefined}),/only half of its assigned identity/],
   ['a malformed identity',JSON.stringify({...valid,nodeId:'node-1'}),/Agent identity in config is invalid/],
   ['a pending credential without a rotation',JSON.stringify({...valid,pendingCredential:crypto.randomBytes(32).toString('hex')}),/Agent pending credential state is invalid/],
   ['an empty document','',/decode Agent config/],
  ]){const directory=fresh(),file=path.join(directory,'agent.json');install(file,content);refused(`Runtime with ${label} in its config`,run(file),pattern)}
  const loose=fresh();install(path.join(loose,'agent.json'),JSON.stringify(valid),'0644');refused('Runtime with a group-readable credential file',run(path.join(loose,'agent.json')),/Agent credential file mode is 0644, want 0600/)
  const open=fresh();install(path.join(open,'agent.json'),JSON.stringify(valid));execFileSync('sudo',['chmod','0755',open]);refused('Runtime with a readable state directory',run(path.join(open,'agent.json')),/Agent credential directory mode is 0755/)
  const foreign=fresh();install(path.join(foreign,'agent.json'),JSON.stringify(valid));execFileSync('sudo',['chown','65534',path.join(foreign,'agent.json')]);refused('Runtime with a credential file owned by another user',run(path.join(foreign,'agent.json')),/Agent credential file must be owned by root/)
  const linked=fresh();install(path.join(linked,'real.json'),JSON.stringify(valid));execFileSync('sudo',['ln','-s','real.json',path.join(linked,'agent.json')]);refused('Runtime with a symlinked credential file',run(path.join(linked,'agent.json')),/Agent credential path may not contain a symlink/)
  const alias=fresh()+'-alias';execFileSync('sudo',['ln','-s',linked,alias]);states.push(alias);refused('Runtime through a symlinked state directory',run(path.join(alias,'real.json')),/Agent credential path may not contain a symlink/)
  refused('Runtime without a credential file',run(path.join(fresh(),'agent.json')),/no such file or directory/)
  refused('Runtime with an unexpected argument',asRoot(['run','--config',path.join(loose,'agent.json'),'extra']),/unexpected run arguments/)
  refused('Unknown Agent command',asRoot(['serve']),/unknown command/)

  const accepted=fresh(),acceptedConfig=path.join(accepted,'agent.json');install(acceptedConfig,JSON.stringify(valid))
  running=spawn('sudo',[...environment,agentBinary,'run','--config',acceptedConfig],{stdio:'ignore'})
  await eventually(()=>connections>0,'Fixture listener never observed a valid Agent dialing the Core; refusal checks would be vacuous',300,100)
  spawnSync('sudo',['pkill','-KILL','-P',String(running.pid)]);await Promise.race([new Promise(resolve=>running.once('exit',resolve)),sleep(8000)]);running=null

  session=await signIn(core.base,core.credentials())
  agent=await rootAgent({session,displayName:'Agent entry fixture'})
  assert.equal(mode(agent.state),'700 0','Agent state directory must be owner-only');assert.equal(mode(agent.config),'600 0','Agent credential file must be owner-only')
  const stored=JSON.parse(execFileSync('sudo',['cat',agent.config],{encoding:'utf8'}))
  assert.equal(stored.server,core.base);assert.equal(stored.development,true);assert.equal(stored.nodeId,agent.nodeId);assert.match(stored.credential,/^[0-9a-f]{64}$/);assert.equal(stored.enrollmentToken,undefined,'A consumed enrollment credential must not stay on disk')
  assert.equal(asNobody(['run','--config',agent.config]).status===0,false);assert.equal(spawnSync('sudo',['setpriv','--reuid=65534','--regid=65534','--clear-groups','cat',agent.config]).status===0,false,'Another user must not read the Agent credential')
  const before=execFileSync('sudo',['sha256sum',agent.config],{encoding:'utf8'})
  const again=asRoot(['enroll','--server',core.base,'--dev','--config',agent.config,'--token-stdin'],token+'\n')
  assert.notEqual(again.status,0);assert.match(again.stderr,/Agent credential file already exists; use recover if enrollment may have completed/);assert.equal(execFileSync('sudo',['sha256sum',agent.config],{encoding:'utf8'}),before,'A second enrollment must not replace the device identity')
  await agent.start()
  const listed=(await(await session.call('nodes.list')).json()).nodes;assert.equal(listed.length,1,'Refused enrollments must not register nodes');assert.equal(listed[0].nodeId,agent.nodeId);assert.equal(listed[0].status,'online')
  console.log('agent-entry-regressions passed.')
 }finally{
  if(running)spawnSync('sudo',['pkill','-KILL','-P',String(running.pid)])
  if(agent)await agent.remove().catch(()=>{});if(session)await session.close().catch(()=>{})
  listener.close();await core.stop();fs.rmSync(core.directory,{recursive:true,force:true})
  for(const directory of states)spawnSync('sudo',['rm','-rf',directory]);fs.rmSync(shared,{recursive:true,force:true})
 }
}
