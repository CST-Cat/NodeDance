const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),crypto=require('node:crypto')
const {spawnSync}=require('node:child_process')
const {startCore,signIn,eventually,sleep}=require('./core-fixture.cjs'),rootAgent=require('./agent-fixture.cjs')
const relayTransports=['rsync_relay','chunk_relay'],megabyte=1024*1024,rate=0.262144
const digest=data=>crypto.createHash('sha256').update(data).digest('hex')
function tree(directory,prefix=''){
 return fs.readdirSync(directory,{withFileTypes:true}).flatMap(entry=>{
  const file=path.join(directory,entry.name),name=prefix+entry.name
  if(entry.isSymbolicLink())return [[name,'symlink']]
  if(entry.isDirectory())return [[name,'directory'],...tree(file,name+'/')]
  return [[name,'file',fs.statSync(file).mode&0o777,digest(fs.readFileSync(file))]]
 }).sort((left,right)=>left[0]<right[0]?-1:1)
}
module.exports=async function peerTransfer(){
 const outer=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-peer-transfer-')),from=path.join(outer,'source'),to=path.join(outer,'target')
 const write=(file,data,mode)=>{fs.mkdirSync(path.dirname(file),{recursive:true});fs.writeFileSync(file,data);if(mode)fs.chmodSync(file,mode)}
 const content={small:Buffer.from('peer transfer fixture\n'),direct:crypto.randomBytes(megabyte/4),strict:crypto.randomBytes(megabyte/4),slow:crypto.randomBytes(megabyte),killed:crypto.randomBytes(megabyte),orphan:crypto.randomBytes(megabyte)}
 for(const [name,data] of Object.entries(content))write(path.join(from,name==='small'?'small.txt':name+'.bin'),data)
 write(path.join(from,'tree','a.txt'),'a',0o750);write(path.join(from,'tree','sub','b.txt'),'b');write(path.join(from,'tree','sub','deep','c.bin'),crypto.randomBytes(70000));write(path.join(from,'tree','名称 with space.txt'),'unicode');fs.mkdirSync(path.join(from,'tree','empty'))
 write(path.join(from,'unsafe','plain.txt'),'plain');fs.symlinkSync('/etc/hostname',path.join(from,'unsafe','link'))
 write(path.join(from,'clash-dir','member.txt'),'member');write(path.join(from,'clash-file'),'file');fs.mkdirSync(path.join(from,'back'))
 write(path.join(outer,'outside.txt'),'outside\n');fs.mkdirSync(path.join(outer,'outside-dir'));fs.symlinkSync(path.join(outer,'outside.txt'),path.join(from,'escape-link'))
 fs.mkdirSync(path.join(to,'in'),{recursive:true});write(path.join(to,'in','clash-dir'),'occupied');fs.mkdirSync(path.join(to,'in','clash-file'));fs.symlinkSync(path.join(outer,'outside-dir'),path.join(to,'escape-dir'))
 const outside=()=>({listing:fs.readdirSync(outer).sort(),file:fs.readFileSync(path.join(outer,'outside.txt'),'utf8'),directory:fs.readdirSync(path.join(outer,'outside-dir'))}),untouched=outside()
 const core=await startCore(),opened=[];let session,source,target
 const open=async credentials=>{const value=await signIn(core.base,credentials);opened.push(value);return value}
 try{
  session=await open(core.credentials())
  source=await rootAgent({session,displayName:'Transfer source fixture',files:from});target=await rootAgent({session,displayName:'Transfer target fixture',files:to});await source.start();await target.start()
  const transfer=(data,{key=crypto.randomUUID(),sender=source,actor=session}={})=>actor.call('files.transfer',{parameters:{nodeId:sender.nodeId},headers:key===null?{}:{'Idempotency-Key':key},data,timeout:180000})
  const outcome=async(label,response)=>{assert.equal(response.status(),200,`${label}: ${response.status()} ${await response.text()}`);return response.json()}
  const transferred=async(label,data,options)=>{const body=await outcome(label,await transfer(data,options));assert.equal(body.failed,undefined,`${label}: ${JSON.stringify(body.failed)}`);assert.equal(body.transferred.length,1,label);return body.transferred[0]}
  const failed=async(label,data,text,options)=>{const body=await outcome(label,await transfer(data,options));assert.deepEqual(body.transferred,[],label);assert.equal(body.failed.length,1,label);assert.ok(body.failed[0].error.includes(text),`${label}: ${body.failed[0].error}`);assert.deepEqual(outside(),untouched,`${label}: reached outside the file roots`);return body.failed[0]}
  const refused=async(label,response,status,text)=>{const body=await response.text();assert.equal(response.status(),status,`${label}: ${response.status()} ${body}`);if(text)assert.ok(body.includes(text),`${label}: ${body}`);assert.deepEqual(outside(),untouched,`${label}: reached outside the file roots`)}
  const task=async(taskId,node=target)=>{const response=await session.call('tasks.get',{parameters:{nodeId:node.nodeId,taskId}});assert.equal(response.status(),200,await response.text());return response.json()}
  const settled=(taskId,status)=>eventually(async()=>{const value=await task(taskId);return value.status===status?value:null},`Transfer task ${taskId} did not become ${status}`,400,50)
  const retry=(taskId,{key=crypto.randomUUID(),node=target}={})=>session.call('files.transfer-retry',{parameters:{nodeId:node.nodeId,taskId},headers:key===null?{}:{'Idempotency-Key':key},timeout:180000})
  const progressing=(key,size)=>eventually(async()=>{const response=await session.call('files.transfer-status',{parameters:{nodeId:target.nodeId},query:{requestKey:key,count:1}});assert.equal(response.status(),200,await response.text());const item=(await response.json()).items[0];if(item?.task.status!=='running'||!(item.task.progress?.completed>0))return null;assert.equal(item.index,0);assert.equal(item.task.progress.total,size);assert.ok(item.task.progress.completed<size,'Rate-limited transfer finished before it could be observed');return item.task},'Rate-limited transfer never reported progress',400,25)
  const partial=taskId=>{const stated=spawnSync('sudo',['stat','--format=%s',path.join(target.state,'file-transfers',taskId,'payload')],{encoding:'utf8'});return stated.status===0?Number(stated.stdout):-1}
  const transferTasks=async sourcePath=>(await(await session.call('tasks.list',{parameters:{nodeId:target.nodeId},query:{limit:'100'}})).json()).tasks.filter(item=>item.transfer?.file?.transfer?.sourcePath===sourcePath)
  const delivered=(name,data,directory='in')=>assert.deepEqual(fs.readFileSync(path.join(to,directory,name)),data,`${name} must arrive intact`)
  const absent=(name,directory='in')=>assert.ok(!fs.existsSync(path.join(to,directory,name)),`${name} must not be published`)

  const relayKey=crypto.randomUUID(),relayRequest={targetNodeId:target.nodeId,paths:['/small.txt'],targetPath:'/in',mode:'relay'}
  const relayed=await transferred('Explicit relay',relayRequest,{key:relayKey})
  assert.ok(relayTransports.includes(relayed.transport),`Explicit relay reported ${relayed.transport}`);assert.equal(relayed.sourcePath,'/small.txt');assert.equal(relayed.targetPath,'/in/small.txt');assert.equal(relayed.size,content.small.length);assert.equal(relayed.sha256,digest(content.small))
  delivered('small.txt',content.small);assert.deepEqual(fs.readFileSync(path.join(from,'small.txt')),content.small,'A transfer copies and keeps its source')
  const recorded=await settled(relayed.taskId,'succeeded');assert.equal(recorded.result.observedState,relayed.transport);assert.equal(recorded.result.resourceRevision,relayed.sha256);assert.equal(recorded.transfer.file.transfer.sourceNodeId,source.nodeId);assert.equal(recorded.transfer.file.transfer.mode,'relay')
  assert.deepEqual(await transferred('Replayed relay',relayRequest,{key:relayKey}),relayed,'Replaying a key returns the recorded transfer');assert.equal((await transferTasks('/small.txt')).length,1,'A replayed key must not create another task')
  const returned=await transferred('Default mode in the other direction',{targetNodeId:source.nodeId,paths:['/in/small.txt'],targetPath:'/back'},{sender:target})
  assert.equal(returned.transport,relayed.transport,'The default mode is the relay');assert.deepEqual(fs.readFileSync(path.join(from,'back','small.txt')),content.small)

  await refused('Unknown mode',await transfer({...relayRequest,mode:'auto'}),400,'invalid file transfer')
  await refused('Traversal source',await transfer({...relayRequest,paths:['/../outside.txt']}),400,'invalid file transfer')
  await refused('Traversal destination',await transfer({...relayRequest,targetPath:'/in/../..'}),400,'invalid file transfer')
  await refused('Missing idempotency key',await transfer(relayRequest,{key:null}),400,'Idempotency-Key is required')
  await refused('Unknown target node',await transfer({...relayRequest,targetNodeId:crypto.randomUUID()}),404)
  await refused('Destination that is a file',await transfer({...relayRequest,targetPath:'/in/small.txt'}),400,'transfer target is not a directory')
  await refused('Destination behind a symlink',await transfer({...relayRequest,targetPath:'/escape-dir'}),400,'transfer target is not a directory')
  const viewerAccount={username:'transfer-viewer',displayName:'Transfer Viewer',password:'Transfer-viewer-password-6612',role:'viewer'}
  const createdViewer=await session.call('accounts.create',{data:viewerAccount});assert.equal(createdViewer.status(),201,await createdViewer.text())
  await refused('Viewer transfer',await transfer({...relayRequest,paths:['/clash-file'],targetPath:'/'},{actor:await open(viewerAccount)}),403);absent('clash-file','.')
  await failed('Symlinked source',{...relayRequest,paths:['/escape-link']},'invalid_path');absent('escape-link')
  await failed('Missing source',{...relayRequest,paths:['/missing.txt']},'not_found')
  await failed('Source root',{...relayRequest,paths:['/']},'transfer destination must be outside the source')

  const sourceTree=tree(path.join(from,'tree')),folder=await transferred('Directory relay',{targetNodeId:target.nodeId,paths:['/tree'],targetPath:'/in'})
  assert.equal(folder.targetPath,'/in/tree');assert.ok(relayTransports.includes(folder.transport));assert.equal((await task(folder.taskId)).transfer.file.transfer.directory,true)
  assert.deepEqual(tree(path.join(to,'in','tree')),sourceTree,'A directory arrives with the same members, modes and bytes');assert.ok(sourceTree.some(entry=>entry[0]==='empty'&&entry[1]==='directory')&&sourceTree.some(entry=>entry[0]==='a.txt'&&entry[2]===0o750))
  write(path.join(from,'tree','late.txt'),'late')
  await failed('Directory onto an existing directory',{targetNodeId:target.nodeId,paths:['/tree'],targetPath:'/in'},'transfer target exists and is not a file');assert.deepEqual(tree(path.join(to,'in','tree')),sourceTree,'A refused directory transfer must not merge into the destination')
  await failed('Directory onto an existing file',{targetNodeId:target.nodeId,paths:['/clash-dir'],targetPath:'/in'},'destination_exists');assert.equal(fs.readFileSync(path.join(to,'in','clash-dir'),'utf8'),'occupied')
  await failed('File onto an existing directory',{targetNodeId:target.nodeId,paths:['/clash-file'],targetPath:'/in'},'transfer target exists and is not a file');assert.deepEqual(fs.readdirSync(path.join(to,'in','clash-file')),[])
  await failed('Directory with a symlinked member',{targetNodeId:target.nodeId,paths:['/unsafe'],targetPath:'/in'},'invalid_path');absent('unsafe')
  const before=tree(from)
  await failed('Directory into itself',{targetNodeId:source.nodeId,paths:['/tree'],targetPath:'/tree/sub'},'transfer destination must be outside the source')
  await failed('Directory onto itself',{targetNodeId:source.nodeId,paths:['/tree'],targetPath:'/'},'transfer destination must be outside the source')
  assert.deepEqual(tree(from),before,'Refused recursive transfers must leave the source unchanged')

  if(process.env.NODEDANCE_AUDIT_DIRECT_TRANSFER==='1'){
   const direct=await transferred('Direct transfer',{targetNodeId:target.nodeId,paths:['/direct.bin'],targetPath:'/in',mode:'direct'})
   assert.equal(direct.transport,'rsync_direct');delivered('direct.bin',content.direct);assert.equal((await task(direct.taskId)).result.observedState,'rsync_direct')
  }
  for(const node of [source,target]){
   const asset=await(await session.call('nodes.asset',{parameters:{nodeId:node.nodeId}})).json(),saved=await session.call('nodes.save-asset',{parameters:{nodeId:node.nodeId},data:{...asset,lineType:'homelab'}})
   assert.equal(saved.status(),200,await saved.text());assert.equal((await saved.json()).lineType,'homelab')
  }
  const strictRequest={targetNodeId:target.nodeId,paths:['/strict.bin'],targetPath:'/in',mode:'direct'},strict=await outcome('Strict direct between home-lab nodes',await transfer(strictRequest))
  assert.ok(!relayTransports.some(name=>JSON.stringify(strict).includes(name)),'Strict direct must never report a relay transport')
  let strictOutcome='rsync_direct'
  if(strict.transferred.length){
   assert.equal(strict.transferred[0].transport,'rsync_direct');delivered('strict.bin',content.strict)
  }else{
   strictOutcome='unavailable';assert.equal(strict.failed.length,1);const attempt=strict.failed[0]
   assert.deepEqual(attempt.error.split('\n').map(line=>line.split(':')[0]),['direct/tcp_punch','direct/ice'],'Home-lab nodes attempt only NAT traversal: '+attempt.error)
   absent('strict.bin');const refusedTask=await settled(attempt.taskId,'canceled');assert.equal(refusedTask.result.observedState,'unchanged');assert.equal(refusedTask.transfer.file.transfer.mode,'direct')
   const fallback=await transferred('Explicit relay after strict direct',{...strictRequest,mode:'relay'});assert.ok(relayTransports.includes(fallback.transport));delivered('strict.bin',content.strict)
  }

  const slowKey=crypto.randomUUID(),slow=transfer({targetNodeId:target.nodeId,paths:['/slow.bin'],targetPath:'/in',bandwidthLimit:rate},{key:slowKey}),active=await progressing(slowKey,megabyte)
  await eventually(()=>partial(active.taskId)>0,'Cancellation fixture never wrote a partial',100,25)
  const cancelling=await session.call('tasks.cancel',{parameters:{nodeId:target.nodeId,taskId:active.taskId}});assert.equal(cancelling.status(),202,await cancelling.text())
  const interrupted=await outcome('Canceled transfer',await slow);assert.deepEqual(interrupted.transferred,[]);assert.equal(interrupted.failed[0].taskId,active.taskId)
  const canceled=await settled(active.taskId,'canceled');assert.equal(canceled.result.observedState,'unchanged');absent('slow.bin')
  const kept=partial(active.taskId);assert.ok(kept>0&&kept<megabyte,'A canceled transfer keeps its private partial');assert.ok(!(await(await session.call('files.list',{parameters:{nodeId:target.nodeId},query:{path:'/in'}})).json()).entries.some(entry=>entry.name==='slow.bin'),'A partial is never listed')
  await refused('Retry without an idempotency key',await retry(active.taskId,{key:null}),400,'Idempotency-Key is required')
  await refused('Retry through another node',await retry(active.taskId,{node:source}),404)
  await refused('Retry of a finished transfer',await retry(relayed.taskId),409,'retry requires a confirmed failed or canceled file transfer')
  const resumed=await outcome('Retried transfer',await retry(active.taskId));assert.equal(resumed.failed,undefined,JSON.stringify(resumed.failed));assert.notEqual(resumed.transferred[0].taskId,active.taskId);assert.ok(relayTransports.includes(resumed.transferred[0].transport))
  delivered('slow.bin',content.slow);assert.equal((await task(resumed.transferred[0].taskId)).transfer.file.transfer.retryOfTaskId,active.taskId)

  const killedKey=crypto.randomUUID(),killed=transfer({targetNodeId:target.nodeId,paths:['/killed.bin'],targetPath:'/in',bandwidthLimit:rate},{key:killedKey}),receiving=await progressing(killedKey,megabyte)
  await target.kill()
  const orphaned=await outcome('Transfer whose target Agent died',await killed);assert.deepEqual(orphaned.transferred,[]);assert.equal(orphaned.failed[0].taskId,receiving.taskId)
  await refused('Retry before the interrupted result is known',await retry(receiving.taskId),409,'observe unknown results first')
  await target.start()
  const reconciled=await settled(receiving.taskId,'failed');assert.equal(reconciled.result.observedState,'unchanged');assert.equal(reconciled.result.resourceRevision,'interrupted')
  await sleep(1500);absent('killed.bin');assert.deepEqual((await transferTasks('/killed.bin')).map(item=>item.status),['failed'],'A restarted Agent must not replay an interrupted transfer')
  const recovered=await outcome('Retry after the target Agent restarted',await retry(receiving.taskId));assert.equal(recovered.failed,undefined,JSON.stringify(recovered.failed));assert.notEqual(recovered.transferred[0].taskId,receiving.taskId);delivered('killed.bin',content.killed)

  const orphanKey=crypto.randomUUID(),orphan=transfer({targetNodeId:target.nodeId,paths:['/orphan.bin'],targetPath:'/in',bandwidthLimit:rate},{key:orphanKey}),sending=await progressing(orphanKey,megabyte)
  await source.kill()
  const starved=await outcome('Transfer whose source Agent died',await orphan);assert.deepEqual(starved.transferred,[]);assert.equal(starved.failed[0].taskId,sending.taskId)
  const abandoned=await settled(sending.taskId,'canceled');assert.equal(abandoned.result.observedState,'unchanged');absent('orphan.bin')
  const offline=await outcome('Retry while the source Agent is offline',await retry(sending.taskId));assert.deepEqual(offline.transferred,[]);assert.equal(offline.failed.length,1);absent('orphan.bin')
  await source.start()
  const restored=await outcome('Retry after the source Agent restarted',await retry(sending.taskId));assert.equal(restored.failed,undefined,JSON.stringify(restored.failed));delivered('orphan.bin',content.orphan)
  assert.deepEqual(outside(),untouched)
  console.log(`peer-transfer-regressions passed (relay: ${relayed.transport}; strict direct between home-lab nodes: ${strictOutcome}; direct TCP case: ${process.env.NODEDANCE_AUDIT_DIRECT_TRANSFER==='1'?'run':'skipped'}).`)
 }finally{
  if(source)await source.remove().catch(()=>{});if(target)await target.remove().catch(()=>{});for(const value of opened)await value.close().catch(()=>{})
  await core.stop();fs.rmSync(core.directory,{recursive:true,force:true});spawnSync('sudo',['rm','-rf',outer])
 }
}
