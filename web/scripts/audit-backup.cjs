const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http'),crypto=require('node:crypto')
const {execFileSync}=require('node:child_process'),{request}=require('playwright')
const {startCore,runCore,signIn,sqlite,eventually}=require('./core-fixture.cjs'),protocolAgent=require('./protocol-agent.cjs')
const hourNanos=3600e9,hourSeconds=3600,expiredHours=25,keptHours=23
const digest=file=>crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex')
const listing=directory=>fs.readdirSync(directory).sort()
const refused=(result,reason,label)=>{assert.notEqual(result.status,0,`${label}: command succeeded`);assert.match(result.stderr,reason,`${label}: refused for another reason`);assert.ok(!result.stdout.includes('Core backup created.')&&!result.stdout.includes('Core data restored.'),`${label}: reported success`)}
const leftovers=(directory,prefix)=>fs.readdirSync(directory).filter(name=>name.startsWith(prefix))
const privateTree=directory=>{assert.equal(fs.statSync(directory).mode&0o777,0o700,`${directory} must be owner-only`);for(const entry of fs.readdirSync(directory,{withFileTypes:true}))if(entry.isFile())assert.equal(fs.statSync(path.join(directory,entry.name)).mode&0o077,0,`${entry.name} must be owner-only`)}
;(async()=>{
 const hits=[],receiver=http.createServer((incoming,response)=>{let body='';incoming.on('data',chunk=>body+=chunk);incoming.on('end',()=>{hits.push({authorization:incoming.headers.authorization,body});response.end('ok')})})
 await new Promise(resolve=>receiver.listen(0,'127.0.0.1',resolve))
 const scratch=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-backup-audit-')),secret='backup-'+'webhook-credential',viewerPassword='viewer-'+crypto.randomBytes(9).toString('hex'),latePassword='late-'+crypto.randomBytes(9).toString('hex')
 const keyFile=path.join(scratch,'key.pem'),certificateFile=path.join(scratch,'certificate.pem')
 execFileSync('openssl',['req','-x509','-newkey','ec','-pkeyopt','ec_paramgen_curve:P-256','-nodes','-subj','/CN=backup.example','-addext','subjectAltName=DNS:backup.example','-addext','basicConstraints=critical,CA:FALSE','-days','2','-keyout',keyFile,'-out',certificateFile],{stdio:'ignore'})
 const privateKey=fs.readFileSync(keyFile,'utf8'),certificate=fs.readFileSync(certificateFile,'utf8')
 let core=await startCore(),restored,agent,idle;const opened=[],anonymous=await request.newContext()
 const open=async(base,credentials)=>{const session=await signIn(base,credentials);opened.push(session);return session}
 const read=async(session,name,options)=>{const response=await session.call(name,options);assert.equal(response.status(),200,`${name}: ${await response.text()}`);return response.json()}
 const loginStatus=async(base,username,password)=>{const api=await request.newContext();try{const token=(await(await api.get(base+'/api/v1/auth/csrf')).json()).token;return(await api.post(base+'/api/v1/auth/login',{headers:{Origin:base,'X-CSRF-Token':token},data:{username,password}})).status()}finally{await api.dispose()}}
 const inventory=async session=>{
  const accounts=(await read(session,'accounts.list')).accounts.map(account=>[account.username,account.role,account.enabled]).sort()
  const channels=(await read(session,'channels.list')).channels.map(channel=>[channel.id,channel.name,channel.kind,channel.hasSecret,channel.enabled]).sort()
  const certificates=(await read(session,'certificates.list')).certificates.map(item=>[item.id,item.name,item.revision]).sort()
  const nodes=(await read(session,'nodes.list')).nodes.map(node=>[node.nodeId,node.displayName,node.status]).sort()
  const rules=Object.fromEntries((await read(session,'rules.list')).rules.map(rule=>[rule.id,[rule.name,rule.kind,rule.nodeId,rule.threshold??null,rule.channelIds,rule.enabled]]))
  const memo=await read(session,'dashboard.memo')
  return {accounts,channels,certificates,nodes,rules,memo:memo.content}
 }
 const sameRecords=(actual,expected,label)=>{
  for(const key of ['accounts','channels','certificates','nodes','memo'])assert.deepEqual(actual[key],expected[key],`${label}: ${key} differ`)
  for(const [id,rule] of Object.entries(expected.rules))assert.deepEqual(actual.rules[id],rule,`${label}: alert rule ${id} differs`)
 }
 const delivers=async(session,channelId,label)=>{
  hits.length=0;const tested=await session.call('channels.test',{parameters:{id:channelId},data:{}});assert.ok(tested.ok(),await tested.text())
  await eventually(()=>hits.length>0,`${label}: stored webhook credential did not reach the local receiver`);assert.equal(hits[0].authorization,'Bearer '+secret,`${label}: webhook credential changed`)
 }
 const exportsKey=async(session,managed,label)=>{
  const pem=await session.call('certificates.pem',{parameters:{id:managed.id}});assert.equal(pem.status(),200,await pem.text());assert.ok((await pem.text()).includes(certificate.trim().split('\n')[1]),`${label}: certificate body changed`)
  const archive=await session.call('certificates.export',{parameters:{id:managed.id},data:{confirm:true,revision:managed.revision}});assert.equal(archive.status(),200,await archive.text())
  const file=path.join(scratch,'export.zip');fs.writeFileSync(file,await archive.body());const names=execFileSync('python3',['-c','import sys,zipfile\nz=zipfile.ZipFile(sys.argv[1])\nprint("\\n".join(n for n in z.namelist() if z.read(n).decode("utf8","replace").strip()==open(sys.argv[2]).read().strip()))',file,keyFile],{encoding:'utf8'}).trim()
  assert.ok(names.length>0,`${label}: private export must return the original private key`)
 }
 const restart=async(args=[])=>{await core.stop();core=await startCore({directory:core.directory,port:core.port,args})}
 try{
  const directory=core.directory,initialPath=path.join(directory,'initial-admin.json'),pending=digest(initialPath),administrator=core.credentials()
  await restart()
  assert.equal(digest(initialPath),pending,'Restart before the first login must keep the same first-login credentials')
  let session=await open(core.base,administrator)
  assert.ok(!fs.existsSync(initialPath),'First login consumes the first-login credentials file')
  const viewer=await session.call('accounts.create',{data:{username:'backup-viewer',displayName:'Backup viewer',password:viewerPassword,role:'viewer'}});assert.ok(viewer.ok(),await viewer.text())
  const webhook=await session.call('channels.create',{data:{name:'Backup webhook',kind:'webhook',config:{webhookUrl:`http://127.0.0.1:${receiver.address().port}/backup`,method:'POST'},secret,enabled:true}});assert.equal(webhook.status(),200,await webhook.text());const channel=(await webhook.json()).channel
  const enrollment=await session.call('agents.enrollment',{data:{displayName:'Backup pending node',coreUrl:core.base}});assert.equal(enrollment.status(),201,await enrollment.text());const pendingNode=(await enrollment.json()).nodeId
  idle=await protocolAgent({session,displayName:'Backup idle node'})
  const seeded=await session.call('rules.create',{data:{name:'Backup processor rule',kind:'cpu',nodeId:idle.nodeId,severity:'critical',threshold:97,durationSeconds:120,cooldownSeconds:600,enabled:true,channelIds:[channel.id]}});assert.equal(seeded.status(),200,await seeded.text());const seededRule=(await seeded.json()).rule
  const imported=await session.call('certificates.import',{data:{name:'Backup certificate',certificatePem:certificate,privateKeyPem:privateKey,domainIds:[],channelIds:[]}});assert.equal(imported.status(),201,await imported.text());const managed=await imported.json()
  const memo=await session.call('dashboard.save-memo',{data:{content:'Backup memo fixture',revision:(await read(session,'dashboard.memo')).revision}});assert.ok(memo.ok(),await memo.text())
  const atBackup=await inventory(session)
  assert.ok(atBackup.accounts.some(account=>account[0]==='backup-viewer'&&account[1]==='viewer'));assert.ok(atBackup.nodes.some(node=>node[0]===pendingNode&&node[2]==='pending'));assert.ok(atBackup.nodes.some(node=>node[0]===idle.nodeId));assert.ok(atBackup.rules[seededRule.id]);assert.equal(atBackup.certificates.length,1);assert.equal(atBackup.channels.length,1)

  const snapshots=leftovers(os.tmpdir(),'nodedance-backup-snapshot-'),backup=path.join(scratch,'core.ndbackup'),created=runCore(['backup','--data-dir',directory,'--output',backup])
  assert.equal(created.status,0,created.stderr);assert.ok(created.stdout.includes('Core backup created.'))
  assert.equal(fs.statSync(backup).mode&0o777,0o600,'Backup archive must be owner-only');const archiveDigest=digest(backup)
  assert.ok((await fetch(core.base+'/api/v1/health')).ok,'Core keeps serving during an online backup')
  const late=await session.call('accounts.create',{data:{username:'after-backup',displayName:'After backup',password:latePassword,role:'viewer'}});assert.ok(late.ok(),await late.text())
  const liveListing=listing(directory)
  refused(runCore(['backup','--data-dir',directory,'--output',backup]),/already exists/,'Backup over an existing output');assert.equal(digest(backup),archiveDigest,'Rejected backup changed the existing archive')
  const inside=path.join(directory,'inside.ndbackup');refused(runCore(['backup','--data-dir',directory,'--output',inside]),/outside the Core data directory/,'Backup into the data directory');assert.ok(!fs.existsSync(inside))
  const orphan=path.join(scratch,'orphan.ndbackup');refused(runCore(['backup','--data-dir',path.join(scratch,'no-such-data'),'--output',orphan]),/existing real directory/,'Backup of a missing data directory');assert.ok(!fs.existsSync(orphan))
  refused(runCore(['backup','--data-dir',directory,'--output',path.join(scratch,'no-such-parent','core.ndbackup')]),/parent directory is unavailable/,'Backup into a missing parent directory');assert.ok(!fs.existsSync(path.join(scratch,'no-such-parent')))
  assert.deepEqual(listing(directory),liveListing,'Rejected backups must not write into the data directory');assert.deepEqual(leftovers(scratch,'.nodedance-backup-'),[],'Backups must not leave temporary archives');assert.deepEqual(leftovers(os.tmpdir(),'nodedance-backup-snapshot-'),snapshots,'Backups must not leave database snapshots')

  const archive=fs.readFileSync(backup),header=archive.indexOf(Buffer.from('SQLite format 3\u0000','latin1')),checksum=archive.indexOf(Buffer.from('"sha256":"'))
  assert.ok(header>0&&checksum>0&&archive.length>header+8192,'Backup archive must carry a manifest and the database')
  const damaged={
   content:[(()=>{const copy=Buffer.from(archive);copy[header+4096+64]^=0xff;return copy})(),/checksum mismatch/],
   manifest:[(()=>{const copy=Buffer.from(archive),index=checksum+10;copy[index]=copy[index]===0x30?0x31:0x30;return copy})(),/checksum mismatch/],
   truncated:[archive.subarray(0,Math.floor(archive.length/2)),/unexpected EOF/],
   empty:[Buffer.alloc(0),/manifest is missing or invalid/],
  }
  for(const [name,[data,reason]] of Object.entries(damaged)){
   const input=path.join(scratch,`damaged-${name}.ndbackup`),target=path.join(scratch,`rejected-${name}`),prepared=path.join(scratch,`prepared-${name}`)
   fs.writeFileSync(input,data,{mode:0o600});fs.mkdirSync(prepared,{mode:0o700})
   refused(runCore(['restore','--input',input,'--data-dir',target]),reason,`Restore of a backup with damaged ${name}`);assert.ok(!fs.existsSync(target),`${name}: rejected restore left a data directory`)
   refused(runCore(['restore','--input',input,'--data-dir',prepared]),reason,`Restore of a backup with damaged ${name} into an empty directory`);assert.deepEqual(listing(prepared),[],`${name}: rejected restore left files in the target`)
   assert.deepEqual(leftovers(scratch,'.nodedance-restore-'),[],`${name}: rejected restore left a staging directory`)
  }
  const linked=path.join(scratch,'linked.ndbackup');fs.symlinkSync(backup,linked)
  refused(runCore(['restore','--input',linked,'--data-dir',path.join(scratch,'rejected-link')]),/must be a regular file/,'Restore from a symlinked backup');assert.ok(!fs.existsSync(path.join(scratch,'rejected-link')))
  refused(runCore(['restore','--input',backup,'--data-dir',directory]),/must be empty/,'Restore over the live data directory');assert.deepEqual(listing(directory),liveListing,'Rejected restore changed the live data directory')
  assert.ok((await fetch(core.base+'/api/v1/health')).ok,'Rejected restore must leave the running Core healthy')

  const target=path.join(scratch,'restored'),recovered=runCore(['restore','--input',backup,'--data-dir',target]);assert.equal(recovered.status,0,recovered.stderr);assert.ok(recovered.stdout.includes('Core data restored.'))
  privateTree(target);assert.ok(listing(target).includes('nodedance.sqlite'));assert.ok(!fs.existsSync(path.join(target,'initial-admin.json')),'Restore must not invent first-login credentials')
  for(const name of ['alert-channel-encryption.key','certificate-encryption.key','csrf-signing.key'])assert.equal(digest(path.join(target,name)),digest(path.join(directory,name)),`${name} must be restored unchanged`)
  refused(runCore(['restore','--input',backup,'--data-dir',target]),/must be empty/,'Restore into a restored directory');assert.equal(digest(backup),archiveDigest)
  const prepared=path.join(scratch,'restored-prepared');fs.mkdirSync(prepared,{mode:0o700})
  const second=runCore(['restore','--input',backup,'--data-dir',prepared]);assert.equal(second.status,0,second.stderr);privateTree(prepared)
  assert.deepEqual(listing(prepared),listing(target));for(const name of listing(target).filter(name=>fs.statSync(path.join(target,name)).isFile()))assert.equal(digest(path.join(prepared,name)),digest(path.join(target,name)),`${name} differs between restores`)
  assert.deepEqual(leftovers(scratch,'.nodedance-restore-'),[],'Restore must not leave a staging directory')

  restored=await startCore({directory:target})
  assert.equal((await(await anonymous.get(restored.base+'/api/v1/auth/setup/status')).json()).initialized,true,'Restored Core must stay initialized')
  const recoveredSession=await open(restored.base,administrator)
  sameRecords(await inventory(recoveredSession),atBackup,'Restored Core')
  assert.equal(await loginStatus(restored.base,'backup-viewer',viewerPassword),200,'Restored viewer account must keep its password');assert.equal(await loginStatus(restored.base,'after-backup',latePassword),401,'Accounts created after the backup must not be restored')
  await delivers(recoveredSession,channel.id,'Restored Core');await exportsKey(recoveredSession,managed,'Restored Core')
  const hello=await anonymous.post(restored.base+'/api/v1/agents/channel',{headers:{Authorization:'Bearer '+idle.credential},data:{version:1,type:'hello',payload:{agentId:idle.agentId,nodeId:idle.nodeId,agentVersion:'agent-v0.1.10',capabilities:idle.capabilities,permissions:{os:'linux',architecture:'amd64',effectiveUid:0,effectiveGid:0,supplementaryGroups:[0]}}}})
  assert.equal(hello.status(),201,'Restored Core must accept the enrolled Agent credential: '+await hello.text())
  await anonymous.delete(restored.base+'/api/v1/agents/channel/'+(await hello.json()).channelId,{headers:{Authorization:'Bearer '+idle.credential}})
  await recoveredSession.close();await restored.stop();restored=undefined
  assert.equal((await read(session,'accounts.list')).accounts.filter(account=>account.username==='after-backup').length,1,'Restoring elsewhere must not change the source Core')

  const beforeRestart=await inventory(session),users=sqlite(core.database,'SELECT id,username,role,enabled,password_hash FROM users ORDER BY id'),keys=['alert-channel-encryption.key','certificate-encryption.key','csrf-signing.key'].map(name=>digest(path.join(directory,name)))
  assert.equal(users.length,3)
  await restart()
  assert.ok(!fs.existsSync(initialPath),'Restart must not regenerate first-login credentials');assert.ok(!fs.existsSync(path.join(directory,'setup-credential.txt')),'Restart must not reopen setup')
  assert.equal((await(await anonymous.get(core.base+'/api/v1/auth/setup/status')).json()).initialized,true)
  assert.deepEqual(sqlite(core.database,'SELECT id,username,role,enabled,password_hash FROM users ORDER BY id'),users,'Restart must keep every account and password');assert.deepEqual(['alert-channel-encryption.key','certificate-encryption.key','csrf-signing.key'].map(name=>digest(path.join(directory,name))),keys,'Restart must keep the stored keys')
  assert.equal((await session.api.get(core.base+'/api/v1/auth/me')).status(),200,'Sessions survive a Core restart')
  for(const [username,password] of [[administrator.username,administrator.password],['backup-viewer',viewerPassword],['after-backup',latePassword]])assert.equal(await loginStatus(core.base,username,password),200,`${username} must still sign in after restart`)
  session=await open(core.base,administrator);sameRecords(await inventory(session),beforeRestart,'Restarted Core')
  await delivers(session,channel.id,'Restarted Core');await exportsKey(session,managed,'Restarted Core')

  agent=await protocolAgent({session,displayName:'Retention task node',capabilities:['agent.heartbeat.v1','agent.os-permissions.v1','agent.metrics.v2','agent.files.v1','agent.task-bridge.v1']});await agent.open();await agent.report({})
  const journalId=crypto.randomBytes(32).toString('hex'),announced=await agent.post({type:'task_journal_hello',payload:{nodeId:agent.nodeId,journalId,capacity:1,capacityLimit:1}});assert.ok(announced.ok(),await announced.text())
  assert.equal((await agent.receiveType('task_journal_status')).payload.accepted,true)
  const wanted=(await agent.receiveType('task_snapshot_request')).payload,page=await agent.post({type:'task_snapshot_page',requestId:wanted.snapshotId,sequence:1,payload:{snapshotId:wanted.snapshotId,journalId,page:0,final:true,reports:[]}});assert.ok(page.ok(),await page.text())
  assert.equal((await agent.receiveType('task_journal_status')).payload.snapshotAccepted,true)
  const submit=async name=>{const response=await session.call('files.action',{parameters:{nodeId:agent.nodeId},headers:{'Idempotency-Key':crypto.randomUUID()},data:{action:'mkdir',path:'/retention-'+name}});assert.equal(response.status(),202,await response.text());return(await response.json()).taskId}
  const cancel=async taskId=>{const response=await session.call('tasks.cancel',{parameters:{nodeId:agent.nodeId,taskId}});assert.equal(response.status(),200,await response.text());const task=await response.json();assert.equal(task.status,'canceled');assert.equal(task.deliveryState,'done')}
  const tasks={dispatched:await submit('dispatched')}
  await eventually(()=>sqlite(core.database,'SELECT delivery_state FROM core_tasks WHERE task_id=?',[tasks.dispatched])[0][0]==='sent','Core did not dispatch the first task to the protocol Agent')
  tasks.expired=await submit('expired');tasks.kept=await submit('kept');tasks.queued=await submit('queued')
  await cancel(tasks.expired);await cancel(tasks.kept)
  assert.equal((await session.call('tasks.cancel',{parameters:{nodeId:agent.nodeId,taskId:tasks.dispatched}})).status(),409,'A dispatched task cannot be cancelled as undelivered')
  assert.deepEqual(sqlite(core.database,'SELECT task_id FROM core_task_resource_claims ORDER BY task_id').flat(),[tasks.dispatched,tasks.queued].sort(),'Only unfinished tasks hold resource claims')
  const taskNode=agent.nodeId,unlinked=sqlite(core.database,'SELECT id FROM core_task_audit_events WHERE task_id IS NULL AND node_id=?',[taskNode]).flat();assert.equal(unlinked.length,1)
  await agent.close();agent=undefined

  const rule=async name=>{const response=await session.call('rules.create',{data:{name,kind:'node_offline',nodeId:idle.nodeId,severity:'warning',durationSeconds:0,cooldownSeconds:86400,enabled:true,channelIds:[channel.id]}});assert.equal(response.status(),200,await response.text());return(await response.json()).rule}
  const rules={expired:await rule('Retention expired alert'),kept:await rule('Retention kept alert'),active:await rule('Retention active alert')},alerts={}
  await eventually(async()=>{const active=(await read(session,'alerts.list')).alerts;for(const [name,item] of Object.entries(rules))alerts[name]=active.find(alert=>alert.ruleId===item.id)?.id;return Object.values(alerts).every(Boolean)},'Offline rules did not raise alerts',400,100)
  const unfinished=()=>sqlite(core.database,"SELECT count(*) FROM alert_deliveries WHERE status IN ('queued','sending','retry')")[0][0]
  await eventually(()=>unfinished()===0&&sqlite(core.database,"SELECT count(*) FROM alert_deliveries WHERE status='sent' AND alert_id IN (?,?,?)",Object.values(alerts))[0][0]>=3,'Alert notifications were not delivered')
  for(const name of ['expired','kept']){const removed=await session.call('rules.delete',{parameters:{id:rules[name].id},data:{revision:rules[name].revision,confirm:true}});assert.equal(removed.status(),204,await removed.text())}
  await eventually(async()=>{const history=(await read(session,'alerts.history')).alerts;return ['expired','kept'].every(name=>history.find(alert=>alert.id===alerts[name])?.status==='resolved')},'Removed rules did not resolve their alerts')
  assert.ok((await read(session,'alerts.list')).alerts.some(alert=>alert.id===alerts.active))
  const testsBefore=sqlite(core.database,'SELECT id FROM alert_deliveries WHERE test_send=1').flat()
  for(let index=0;index<2;index++){const tested=await session.call('channels.test',{parameters:{id:channel.id},data:{}});assert.ok(tested.ok(),await tested.text())}
  await eventually(()=>unfinished()===0&&sqlite(core.database,"SELECT count(*) FROM alert_deliveries WHERE test_send=1 AND status='sent'")[0][0]>=testsBefore.length+2,'Test notifications were not delivered')
  const [expiredTest,keptTest]=sqlite(core.database,"SELECT id FROM alert_deliveries WHERE test_send=1 AND status='sent' ORDER BY created_at DESC LIMIT 2").flat()
  const now=Date.now(),window=async(reason,startsAt,endsAt)=>{const response=await session.call('windows.create',{data:{kind:'maintenance',scopeType:'global',startsAt:new Date(startsAt).toISOString(),endsAt:new Date(endsAt).toISOString(),reason}});assert.equal(response.status(),201,await response.text());return(await response.json()).window.id}
  const windows={expired:await window('Retention expired window',now-72*3600e3,now-expiredHours*3600e3),kept:await window('Retention kept window',now-72*3600e3,now-keptHours*3600e3),upcoming:await window('Retention upcoming window',now+3600e3,now+7200e3)}
  const entries=sqlite(core.database,'SELECT id FROM audit_entries ORDER BY id').flat();assert.ok(entries.length>=6);const audit={expired:entries[0],kept:entries[1],recent:entries[entries.length-1]}
  const deliveriesOf=alertId=>sqlite(core.database,'SELECT id FROM alert_deliveries WHERE alert_id=? ORDER BY id',[alertId]).flat(),eventsOf=alertId=>sqlite(core.database,'SELECT id FROM alert_events WHERE alert_id=? ORDER BY id',[alertId]).flat(),taskEventsOf=taskId=>sqlite(core.database,'SELECT id FROM core_task_audit_events WHERE task_id=? ORDER BY id',[taskId]).flat()
  const exists=(table,column,value)=>sqlite(core.database,`SELECT count(*) FROM ${table} WHERE ${column}=?`,[value])[0][0]===1
  const history={deliveries:{},events:{},taskEvents:{}}
  for(const name of Object.keys(alerts)){history.deliveries[name]=deliveriesOf(alerts[name]);history.events[name]=eventsOf(alerts[name]);assert.ok(history.deliveries[name].length>0&&history.events[name].length>0,`${name} alert has no history`)}
  for(const name of Object.keys(tasks)){history.taskEvents[name]=taskEventsOf(tasks[name]);assert.ok(history.taskEvents[name].length>0,`${name} task has no audit events`)}
  assert.ok(history.events.expired.length>=2&&history.events.kept.length>=2,'Resolved alerts record firing and recovery')

  await eventually(()=>unfinished()===0,'Notifications were still in flight');await session.close();await core.stop()
  const shiftTask=(taskId,hours)=>{sqlite(core.database,'UPDATE core_tasks SET created_at_ns=created_at_ns-?,updated_at_ns=updated_at_ns-?,started_at_ns=started_at_ns-?,finished_at_ns=finished_at_ns-? WHERE task_id=?',[hours*hourNanos,hours*hourNanos,hours*hourNanos,hours*hourNanos,taskId]);sqlite(core.database,'UPDATE core_task_audit_events SET occurred_at_ns=occurred_at_ns-? WHERE task_id=?',[hours*hourNanos,taskId])}
  const shiftAlert=(alertId,hours,resolved)=>{
   if(resolved)sqlite(core.database,'UPDATE alerts SET first_seen_at=first_seen_at-?,last_seen_at=last_seen_at-?,resolved_at=resolved_at-?,last_notified_at=last_notified_at-? WHERE id=?',[hours*hourNanos,hours*hourNanos,hours*hourNanos,hours*hourNanos,alertId])
   else sqlite(core.database,'UPDATE alerts SET first_seen_at=first_seen_at-? WHERE id=?',[hours*hourNanos,alertId])
   sqlite(core.database,'UPDATE alert_events SET occurred_at=occurred_at-? WHERE alert_id=?',[hours*hourNanos,alertId])
   sqlite(core.database,'UPDATE alert_deliveries SET created_at=created_at-?,updated_at=updated_at-?,delivered_at=delivered_at-? WHERE alert_id=?',[hours*hourNanos,hours*hourNanos,hours*hourNanos,alertId])
  }
  const shiftDelivery=(id,hours)=>sqlite(core.database,'UPDATE alert_deliveries SET created_at=created_at-?,updated_at=updated_at-?,delivered_at=delivered_at-? WHERE id=?',[hours*hourNanos,hours*hourNanos,hours*hourNanos,id])
  shiftTask(tasks.expired,expiredHours);shiftTask(tasks.kept,keptHours);shiftTask(tasks.queued,expiredHours);shiftTask(tasks.dispatched,expiredHours)
  sqlite(core.database,'UPDATE core_task_audit_events SET occurred_at_ns=occurred_at_ns-? WHERE id=?',[expiredHours*hourNanos,unlinked[0]])
  shiftAlert(alerts.expired,expiredHours,true);shiftAlert(alerts.kept,keptHours,true);shiftAlert(alerts.active,expiredHours,false)
  shiftDelivery(expiredTest,expiredHours);shiftDelivery(keptTest,keptHours)
  sqlite(core.database,'UPDATE audit_entries SET occurred_at=occurred_at-? WHERE id=?',[expiredHours*hourSeconds,audit.expired]);sqlite(core.database,'UPDATE audit_entries SET occurred_at=occurred_at-? WHERE id=?',[keptHours*hourSeconds,audit.kept])
  const present=()=>({
   tasks:Object.fromEntries(Object.entries(tasks).map(([name,id])=>[name,exists('core_tasks','task_id',id)])),
   taskEvents:Object.fromEntries(Object.entries(tasks).map(([name,id])=>[name,taskEventsOf(id).length])),
   unlinkedTaskEvent:exists('core_task_audit_events','id',unlinked[0]),
   claims:sqlite(core.database,'SELECT task_id FROM core_task_resource_claims ORDER BY task_id').flat(),
   alerts:Object.fromEntries(Object.entries(alerts).map(([name,id])=>[name,exists('alerts','id',id)])),
   alertEvents:Object.fromEntries(Object.entries(alerts).map(([name,id])=>[name,history.events[name].filter(event=>exists('alert_events','id',event)).length])),
   alertDeliveries:Object.fromEntries(Object.entries(alerts).map(([name,id])=>[name,history.deliveries[name].filter(delivery=>exists('alert_deliveries','id',delivery)).length])),
   tests:{expired:exists('alert_deliveries','id',expiredTest),kept:exists('alert_deliveries','id',keptTest)},
   windows:Object.fromEntries(Object.entries(windows).map(([name,id])=>[name,exists('alert_windows','id',id)])),
   audit:Object.fromEntries(Object.entries(audit).map(([name,id])=>[name,exists('audit_entries','id',id)])),
  })
  const aged=present()
  assert.deepEqual(aged.tasks,{dispatched:true,expired:true,kept:true,queued:true});assert.deepEqual(aged.alerts,{expired:true,kept:true,active:true});assert.deepEqual(aged.windows,{expired:true,kept:true,upcoming:true});assert.deepEqual(aged.audit,{expired:true,kept:true,recent:true});assert.deepEqual(aged.tests,{expired:true,kept:true});assert.equal(aged.unlinkedTaskEvent,true)

  core=await startCore({directory,port:core.port})
  assert.deepEqual(present(),aged,'The default retention must keep history that is days old')
  await core.stop()
  core=await startCore({directory,port:core.port,args:['--history-retention-days','1']})
  const cleaned=present()
  assert.deepEqual(cleaned.tasks,{dispatched:true,expired:false,kept:true,queued:true},'Only finished, released and expired tasks are removed')
  assert.deepEqual(cleaned.taskEvents,{dispatched:aged.taskEvents.dispatched,expired:0,kept:aged.taskEvents.kept,queued:aged.taskEvents.queued},'Task audit events follow their task')
  assert.equal(cleaned.unlinkedTaskEvent,false,'Expired task events without a task are removed');assert.deepEqual(cleaned.claims,aged.claims,'Resource claims of unfinished tasks are kept')
  assert.deepEqual(cleaned.alerts,{expired:false,kept:true,active:true},'Only resolved and expired alerts are removed')
  assert.deepEqual(cleaned.alertEvents,{expired:0,kept:aged.alertEvents.kept,active:aged.alertEvents.active},'Alert events follow their alert');assert.deepEqual(cleaned.alertDeliveries,{expired:0,kept:aged.alertDeliveries.kept,active:aged.alertDeliveries.active},'Deliveries of active or recent alerts are kept')
  assert.deepEqual(cleaned.tests,{expired:false,kept:true},'Only expired completed deliveries are removed');assert.deepEqual(cleaned.windows,{expired:false,kept:true,upcoming:true},'Only expired alert windows are removed');assert.deepEqual(cleaned.audit,{expired:false,kept:true,recent:true},'Only expired audit entries are removed')
  session=await open(core.base,administrator)
  const listed=(await read(session,'tasks.list',{parameters:{nodeId:taskNode}})).tasks.map(task=>task.taskId)
  assert.deepEqual(listed.filter(id=>Object.values(tasks).includes(id)).sort(),[tasks.dispatched,tasks.kept,tasks.queued].sort(),'Task history lists exactly the retained tasks')
  assert.equal((await session.call('tasks.get',{parameters:{nodeId:taskNode,taskId:tasks.expired}})).status(),404,'Removed task must not be readable');assert.equal((await read(session,'tasks.get',{parameters:{nodeId:taskNode,taskId:tasks.kept}})).status,'canceled')
  const remembered=(await read(session,'alerts.history')).alerts.map(alert=>alert.id);assert.ok(!remembered.includes(alerts.expired),'Removed alert must leave the alert history');assert.ok(remembered.includes(alerts.kept),'Recently resolved alert stays in the alert history')
  assert.ok((await read(session,'alerts.list')).alerts.some(alert=>alert.id===alerts.active),'Old but active alert stays active')
  assert.ok((await read(session,'windows.list')).windows.some(item=>item.id===windows.upcoming),'Upcoming window stays listed')
  const afterCleanup=await inventory(session);for(const key of ['accounts','channels','certificates','memo'])assert.deepEqual(afterCleanup[key],beforeRestart[key],`Retention cleanup changed ${key}`)
  assert.deepEqual(afterCleanup.rules[seededRule.id],beforeRestart.rules[seededRule.id],'Retention cleanup must not touch alert rules');assert.ok(afterCleanup.rules[rules.active.id],'Retention cleanup must keep live alert rules')
  assert.equal(await loginStatus(core.base,'backup-viewer',viewerPassword),200,'Retention cleanup must not touch accounts')
  console.log('audit-backup passed.')
 }finally{
  receiver.close();if(agent)await agent.close().catch(()=>{});if(idle)await idle.close().catch(()=>{});for(const session of opened)await session.close().catch(()=>{});await anonymous.dispose().catch(()=>{})
  if(restored)await restored.stop();await core.stop();fs.rmSync(core.directory,{recursive:true,force:true});fs.rmSync(scratch,{recursive:true,force:true})
 }
})().catch(error=>{console.error(error);process.exit(1)})
