const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),crypto=require('node:crypto')
const {spawnSync,execFileSync}=require('node:child_process'),{request}=require('playwright')
const {startCore,signIn,eventually,sleep}=require('./core-fixture.cjs'),rootAgent=require('./agent-fixture.cjs')
const outputLimit=128*1024,readable=48*1024
module.exports=async function hostCommands(){
 const files=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-host-command-')),marker=name=>path.join(files,name)
 const lines=name=>fs.existsSync(marker(name))?fs.readFileSync(marker(name),'utf8').split('\n').filter(Boolean):[]
 const unique=()=>String(700000+crypto.randomInt(200000)),alive=duration=>spawnSync('pgrep',['-f',`^sleep ${duration}$`],{encoding:'utf8'}).stdout.split('\n').filter(Boolean)
 const core=await startCore(),anonymous=await request.newContext(),opened=[];let session,agent
 const open=async credentials=>{const value=await signIn(core.base,credentials);opened.push(value);return value}
 try{
  session=await open(core.credentials())
  agent=await rootAgent({session,displayName:'Host command fixture',files});await agent.start()
  const node={nodeId:agent.nodeId}
  const submit=(script,{key=crypto.randomUUID(),timeoutSeconds=30,confirm=true,actor=session}={})=>actor.call('commands.submit',{parameters:node,headers:key===null?{}:{'Idempotency-Key':key},data:{script,timeoutSeconds,confirm}})
  const accepted=async(script,options)=>{const response=await submit(script,options);assert.equal(response.status(),202,await response.text());return (await response.json()).job}
  const read=async id=>{const response=await session.call('commands.get',{parameters:{...node,jobId:id}});assert.equal(response.status(),200,await response.text());return (await response.json()).job}
  const settled=id=>eventually(async()=>{const job=await read(id);return job.status==='running'?null:job},`Command ${id} did not finish`,600,50)
  const listed=async()=>{const response=await session.call('commands.list',{parameters:node});assert.equal(response.status(),200,await response.text());return (await response.json()).jobs}
  const refused=async(label,response,status,text)=>{const body=await response.text();assert.equal(response.status(),status,`${label}: ${response.status()} ${body}`);if(text)assert.ok(body.includes(text),`${label}: ${body}`)}
  const gone=duration=>eventually(()=>alive(duration).length===0,`sleep ${duration} survived its command`,100,50)

  const key=crypto.randomUUID(),script=`echo run >> ${marker('once')}; id -u; echo stdout-$((40+2)); echo stderr-line >&2; exit 3`
  const started=await accepted(script,{key});assert.match(started.id,/^[0-9a-f]{64}$/);assert.equal(started.output,'')
  const finished=await settled(started.id)
  assert.equal(finished.status,'failed');assert.equal(finished.exitCode,3);assert.equal(finished.output,'0\nstdout-42\nstderr-line\n','Commands run as root and capture both streams in order');assert.equal(finished.truncated,false);assert.equal(finished.error,'command failed');assert.ok(Date.parse(finished.finishedAt)>=Date.parse(finished.startedAt))
  const replayed=await accepted(script,{key});assert.deepEqual(replayed,finished,'Replaying a key returns the recorded job');assert.deepEqual(lines('once'),['run'],'A replayed key must not run the command again')
  const burst=crypto.randomUUID(),racing=await Promise.all(Array.from({length:6},()=>accepted(`echo run >> ${marker('burst')}`,{key:burst})));assert.equal(new Set(racing.map(job=>job.id)).size,1,'Concurrent submissions of one key share one job')
  assert.equal((await settled(racing[0].id)).status,'succeeded');assert.deepEqual(lines('burst'),['run'],'Concurrent submissions of one key run once')
  await refused('Reusing a key for another script',await submit(`echo other >> ${marker('conflict')}`,{key}),400,'idempotency key was used for a different command')
  await refused('Reusing a key with another timeout',await submit(script,{key,timeoutSeconds:31}),400,'idempotency key was used for a different command')
  assert.deepEqual(lines('once'),['run']);assert.deepEqual(lines('conflict'),[],'A conflicting replay must not run')

  const before=(await listed()).length
  await refused('Missing idempotency key',await submit(`echo run >> ${marker('invalid')}`,{key:null}),400,'Idempotency-Key required')
  await refused('Short idempotency key',await submit(`echo run >> ${marker('invalid')}`,{key:'short'}),400,'Idempotency-Key')
  await refused('Unconfirmed command',await submit(`echo run >> ${marker('invalid')}`,{confirm:false}),400)
  for(const timeoutSeconds of [0,-1,3601])await refused(`Timeout ${timeoutSeconds}`,await submit(`echo run >> ${marker('invalid')}`,{timeoutSeconds}),400)
  await refused('Blank script',await submit('  \n'),400)
  await refused('Oversized script',await submit(`echo run >> ${marker('invalid')}; : ${'x'.repeat(32*1024)}`),400)
  await refused('Script with a NUL byte',await submit(`echo run >> ${marker('invalid')}\u0000`),400)
  await refused('Malformed job ID',await session.call('commands.get',{parameters:{...node,jobId:'not-a-job'}}),400,'invalid command job ID')
  await refused('Unknown job',await session.call('commands.get',{parameters:{...node,jobId:'0'.repeat(64)}}),404,'command job not found')
  await refused('Cancelling an unknown job',await session.call('commands.cancel',{parameters:{...node,jobId:'0'.repeat(64)}}),404,'command job not found')
  await refused('Anonymous submission',await anonymous.post(session.url('commands.submit',node),{headers:{Origin:core.base,'Idempotency-Key':crypto.randomUUID()},data:{script:`echo run >> ${marker('invalid')}`,timeoutSeconds:30,confirm:true}}),401)
  const viewerAccount={username:'command-viewer',displayName:'Command Viewer',password:'Command-viewer-password-5527',role:'viewer'}
  const createdViewer=await session.call('accounts.create',{data:viewerAccount});assert.equal(createdViewer.status(),201,await createdViewer.text())
  const viewer=await open(viewerAccount)
  await refused('Viewer submission',await submit(`echo run >> ${marker('invalid')}`,{actor:viewer}),403)
  await refused('Viewer cancellation',await viewer.call('commands.cancel',{parameters:{...node,jobId:started.id}}),403)
  await sleep(300);assert.deepEqual(lines('invalid'),[],'Rejected submissions must not run');assert.equal((await listed()).length,before,'Rejected submissions must not create jobs')

  const timeoutSleep=unique(),timedOut=await accepted(`echo before; sleep ${timeoutSleep} & wait`,{timeoutSeconds:1}),expired=await settled(timedOut.id)
  assert.equal(expired.status,'timed_out');assert.equal(expired.exitCode,-1);assert.equal(expired.output,'before\n');assert.equal(expired.error,'command timeout');await gone(timeoutSleep)
  const cancelSleep=unique(),running=await accepted(`echo started; sleep ${cancelSleep} & wait`,{timeoutSeconds:600})
  await eventually(()=>alive(cancelSleep).length===1,'Cancellable command did not start',100,50)
  const cancelling=await session.call('commands.cancel',{parameters:{...node,jobId:running.id}});assert.equal(cancelling.status(),200,await cancelling.text())
  const cancelled=await settled(running.id);assert.equal(cancelled.status,'cancelled');assert.equal(cancelled.exitCode,-1);assert.equal(cancelled.output,'started\n');assert.equal(cancelled.error,'command cancelled');await gone(cancelSleep)
  const again=await session.call('commands.cancel',{parameters:{...node,jobId:running.id}});assert.equal(again.status(),200);assert.deepEqual((await again.json()).job,cancelled,'Cancelling a finished job leaves its record unchanged')

  const sized=await settled((await accepted(`head -c ${readable} /dev/zero | tr '\\0' x`)).id)
  assert.equal(sized.status,'succeeded');assert.equal(sized.exitCode,0);assert.equal(sized.truncated,false);assert.equal(sized.output.length,readable);assert.ok(/^x+$/.test(sized.output))
  const flood=await accepted(`head -c ${outputLimit+70000} /dev/zero | tr '\\0' x`)
  const capped=await eventually(async()=>{const job=(await listed()).find(item=>item.id===flood.id);return job&&job.status!=='running'?job:null},'Flooding command did not finish',400,50)
  assert.equal(capped.status,'succeeded');assert.equal(capped.truncated,true,'Output beyond the limit is reported as truncated');assert.equal(capped.output,'','Job listings never carry output')
  const stored=JSON.parse(execFileSync('sudo',['cat',path.join(agent.state,'commands',flood.id+'.json')],{encoding:'utf8',maxBuffer:4*1024*1024}))
  assert.equal(stored.output.length,outputLimit,'The Agent keeps at most the output limit');assert.equal(stored.truncated,true)

  const diagnostics=await session.call('agents.diagnostics',{parameters:node});assert.equal(diagnostics.status(),200,await diagnostics.text())
  const report=await diagnostics.text(),credential=JSON.parse(execFileSync('sudo',['cat',agent.config],{encoding:'utf8'})).credential
  assert.ok(typeof credential==='string'&&credential.length>=32,'Agent fixture credential');assert.ok(!report.includes(credential),'Diagnostics must not disclose the Agent credential')
  assert.equal(JSON.parse(report).diagnostics.uid,0);assert.equal(JSON.parse(report).diagnostics.credential,'[redacted]')

  const interruptedKey=crypto.randomUUID(),interruptedSleep=unique(),interruptedScript=`echo begun >> ${marker('interrupted')}; sleep ${interruptedSleep} & wait; echo resumed >> ${marker('interrupted')}`
  const interrupting=await accepted(interruptedScript,{key:interruptedKey,timeoutSeconds:600})
  await eventually(()=>lines('interrupted').length===1&&alive(interruptedSleep).length===1,'Interruptible command did not start',100,50)
  await agent.kill();await gone(interruptedSleep);await agent.start()
  const interrupted=await read(interrupting.id);assert.equal(interrupted.status,'interrupted');assert.equal(interrupted.error,'Agent restarted during execution; command was not replayed');assert.ok(interrupted.finishedAt)
  const resubmitted=await accepted(interruptedScript,{key:interruptedKey,timeoutSeconds:600});assert.equal(resubmitted.id,interrupting.id);assert.equal(resubmitted.status,'interrupted','An interrupted key is not run again')
  assert.deepEqual(await read(started.id),finished,'Finished jobs survive an Agent restart')
  assert.deepEqual(await accepted(script,{key}),finished);await sleep(500)
  assert.deepEqual(lines('interrupted'),['begun'],'An interrupted command must not be replayed');assert.deepEqual(lines('once'),['run']);assert.deepEqual(alive(interruptedSleep),[])

  const settingsResponse=await session.call('agents.settings',{parameters:node});assert.equal(settingsResponse.status(),200,await settingsResponse.text())
  const settings=(await settingsResponse.json()).settings;assert.ok(!settings.disableRemoteControl)
  const configure=async disableRemoteControl=>{const response=await session.call('agents.configure',{parameters:node,data:{...settings,disableRemoteControl}});assert.equal(response.status(),200,await response.text());assert.equal(Boolean((await response.json()).settings.disableRemoteControl),disableRemoteControl)}
  const policySleep=unique(),policed=await accepted(`echo started; sleep ${policySleep} & wait`,{timeoutSeconds:600})
  await eventually(()=>alive(policySleep).length===1,'Policy fixture command did not start',100,50)
  await configure(true)
  assert.equal((await settled(policed.id)).status,'cancelled','Disabling remote control cancels running commands');await gone(policySleep)
  await refused('Submission while remote control is disabled',await submit(`echo run >> ${marker('disabled')}`),400,'remote script execution disabled by Agent settings')
  await agent.stop();await agent.start()
  await refused('Submission after restarting a disabled Agent',await submit(`echo run >> ${marker('disabled')}`),400,'remote script execution disabled by Agent settings')
  await sleep(300);assert.deepEqual(lines('disabled'),[],'A disabled Agent must not run commands')
  await configure(false)
  assert.equal((await settled((await accepted(`echo run >> ${marker('enabled')}`)).id)).status,'succeeded');assert.deepEqual(lines('enabled'),['run'])
  console.log('host-command-regressions passed.')
 }finally{
  if(agent)await agent.remove().catch(()=>{});for(const value of opened)await value.close().catch(()=>{})
  await anonymous.dispose();await core.stop();fs.rmSync(core.directory,{recursive:true,force:true});spawnSync('sudo',['rm','-rf',files])
 }
}
