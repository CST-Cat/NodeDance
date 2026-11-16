const assert=require('node:assert/strict'),fs=require('node:fs'),http=require('node:http'),net=require('node:net'),crypto=require('node:crypto')
const {startCore,signIn,sqlite,eventually,sleep}=require('./core-fixture.cjs'),protocolAgent=require('./protocol-agent.cjs')
const securityMode='SMTP security mode must be starttls or implicit_tls',inFlight=['queued','sending','retry']
;(async()=>{
 const hits=[],receiver=http.createServer((incoming,response)=>{let body='';incoming.on('data',chunk=>body+=chunk);incoming.on('end',()=>{hits.push({path:incoming.url,method:incoming.method,authorization:incoming.headers.authorization,contentType:incoming.headers['content-type'],body:JSON.parse(body)});response.writeHead(204);response.end()})})
 await new Promise(resolve=>receiver.listen(0,'127.0.0.1',resolve))
 const mail={connections:0,lines:[],bytes:Buffer.alloc(0)},mailbox=net.createServer(socket=>{
  mail.connections++;socket.on('error',()=>{});socket.write('220 fixture.invalid ESMTP\r\n');let pending=''
  socket.on('data',chunk=>{
   mail.bytes=Buffer.concat([mail.bytes,chunk]);pending+=chunk.toString('latin1');let index
   while((index=pending.indexOf('\r\n'))>=0){
    const line=pending.slice(0,index),verb=line.split(' ')[0].toUpperCase();pending=pending.slice(index+2);mail.lines.push(line)
    if(verb==='EHLO')socket.write('250-fixture.invalid\r\n250 AUTH PLAIN LOGIN\r\n');else if(verb==='QUIT')socket.end('221 bye\r\n');else socket.write('250 ok\r\n')
   }
  })
 })
 await new Promise(resolve=>mailbox.listen(0,'127.0.0.1',resolve))
 const secret='alert-'+crypto.randomBytes(12).toString('hex'),mailPassword='mail-'+crypto.randomBytes(12).toString('hex'),nodes=[]
 const core=await startCore();let session
 try{
  session=await signIn(core.base,core.credentials())
  const read=async(name,options)=>{const response=await session.call(name,options);assert.equal(response.status(),200,`${name}: ${await response.text()}`);return response.json()}
  const refused=async(label,response,status,text)=>{const body=await response.text();assert.equal(response.status(),status,`${label}: ${response.status()} ${body}`);if(text)assert.ok(body.includes(text),`${label}: ${body}`)}
  const channels=async()=>(await read('channels.list')).channels
  const deliveries=async()=>(await read('deliveries.list',{query:{limit:500}})).deliveries
  const stored=table=>sqlite(core.database,`SELECT COUNT(*) FROM ${table}`)[0][0]

  const smtpInput=(name,mode,extra={})=>({name,kind:'smtp',config:{smtpHost:'127.0.0.1',smtpPort:mailbox.address().port,smtpFrom:'core@fixture.invalid',smtpTo:'operator@fixture.invalid',smtpUsername:'mailer',...(mode===undefined?{}:{smtpSecurityMode:mode}),...extra},secret:mailPassword,enabled:true})
  for(const [label,mode] of [['absent',undefined],['empty',''],['none','none'],['plain','plain'],['tls','tls'],['ssl','ssl'],['uppercase','STARTTLS'],['padded',' starttls']])await refused(`SMTP channel with ${label} security mode`,await session.call('channels.create',{data:smtpInput('Insecure mail '+label,mode)}),400,securityMode)
  await refused('SMTP channel carrying webhook settings',await session.call('channels.create',{data:smtpInput('Mixed mail','starttls',{webhookUrl:`http://127.0.0.1:${receiver.address().port}/mixed`})}),400,'SMTP channel cannot contain webhook settings')
  assert.deepEqual(await channels(),[],'Rejected SMTP channels must not be listed');assert.equal(stored('alert_channels'),0,'Rejected SMTP channels must not be stored');assert.equal(mail.connections,0,'Rejected SMTP channels must not contact the mail server')
  const mailChannels={}
  for(const mode of ['starttls','implicit_tls']){
   const created=await session.call('channels.create',{data:smtpInput('Mail '+mode,mode)});assert.equal(created.status(),200,await created.text())
   const text=await created.text(),channel=JSON.parse(text).channel;mailChannels[mode]=channel
   assert.equal(channel.kind,'smtp');assert.equal(channel.config.smtpSecurityMode,mode);assert.equal(channel.hasSecret,true);assert.ok(!text.includes(mailPassword),'SMTP password must not be echoed')
  }
  const downgrade=smtpInput('Mail starttls',undefined);downgrade.revision=mailChannels.starttls.revision;delete downgrade.secret
  await refused('Removing the security mode from an SMTP channel',await session.call('channels.update',{parameters:{id:mailChannels.starttls.id},data:downgrade}),400,securityMode)
  assert.deepEqual((await channels()).map(channel=>[channel.id,channel.config.smtpSecurityMode,channel.revision]).sort(),Object.values(mailChannels).map(channel=>[channel.id,channel.config.smtpSecurityMode,channel.revision]).sort(),'A rejected update must leave the stored security mode unchanged')
  const refusedMail=async(mode,reason)=>{
   const channel=mailChannels[mode],before=mail.connections,tested=await session.call('channels.test',{parameters:{id:channel.id},data:{}});assert.equal(tested.status(),202,await tested.text());assert.deepEqual(await tested.json(),{status:'queued'})
   const delivery=await eventually(async()=>(await deliveries()).find(item=>item.channelId===channel.id&&item.testSend&&item.attempts>=1&&item.status!=='sending'),`${mode}: test notification was not attempted`,200,100)
   assert.ok(['retry','failed'].includes(delivery.status),`${mode}: plaintext mail server delivery ended as ${delivery.status}`);assert.equal(delivery.lastError,reason,`${mode}: refusal reason`);assert.equal(delivery.deliveredAt,undefined)
   assert.ok(mail.connections>before,`${mode}: fixture mail server was not contacted`)
   const removed=await session.call('channels.delete',{parameters:{id:channel.id},data:{revision:channel.revision,confirm:true}});assert.equal(removed.status(),204,await removed.text())
   await eventually(async()=>{const item=(await deliveries()).find(item=>item.id===delivery.id);return item.status==='failed'&&item.lastError==='notification channel was deleted'},`${mode}: deleting the channel did not end the pending retry`)
  }
  await refusedMail('starttls','SMTP server does not support required STARTTLS')
  assert.ok(mail.lines.length>0&&mail.lines.every(line=>/^EHLO /i.test(line)),'Without STARTTLS only the greeting may be sent: '+JSON.stringify(mail.lines))
  await refusedMail('implicit_tls','SMTP TLS negotiation failed')
  assert.ok(!mail.lines.some(line=>/^(AUTH|MAIL|RCPT|DATA)\b/i.test(line)),'No mail transaction may start without TLS')
  for(const needle of [mailPassword,Buffer.from('\u0000mailer\u0000'+mailPassword).toString('base64'),Buffer.from(mailPassword).toString('base64'),'operator@fixture.invalid'])assert.ok(!mail.bytes.includes(Buffer.from(needle)),'Credentials or recipients reached a plaintext mail server')
  assert.deepEqual(await channels(),[]);assert.equal(hits.length,0)

  const webhook=async(name,route)=>{const created=await session.call('channels.create',{data:{name,kind:'webhook',config:{webhookUrl:`http://127.0.0.1:${receiver.address().port}${route}`,method:'POST'},secret,enabled:true}});assert.equal(created.status(),200,await created.text());return(await created.json()).channel}
  const channel=await webhook('Alert receiver','/alerts'),decoy=await webhook('Unselected receiver','/decoy')
  const feed=async(name,cpuPercent)=>{
   const agent=await protocolAgent({session,displayName:name});await agent.open()
   const node={name,agent,nodeId:agent.nodeId,cpuPercent,reports:0,active:true,failure:null}
   node.loop=(async()=>{while(node.active){try{await agent.report({cpuPercent:node.cpuPercent});node.reports++}catch(error){node.failure=error;return}await sleep(1500)}})()
   node.stop=async()=>{node.active=false;await node.loop;await agent.close().catch(()=>{})}
   nodes.push(node);return node
  }
  const plain=await feed('Alert plain node',12),acked=await feed('Alert acknowledged node',12),silenced=await feed('Alert silenced node',12),windowed=await feed('Alert maintenance node',12)
  const until=(check,message,seconds=30)=>eventually(async()=>{for(const node of nodes)if(node.failure)throw new Error(`${node.name} stopped reporting: ${node.failure.message}`);return check()},message,seconds*5,200)
  await until(()=>nodes.every(node=>node.reports>=1),'Protocol Agents did not report metrics')
  const rule=async(node,name,kind='cpu')=>{const created=await session.call('rules.create',{data:{name,kind,nodeId:node.nodeId,severity:'critical',threshold:80,durationSeconds:0,cooldownSeconds:0,enabled:true,channelIds:[channel.id]}});assert.equal(created.status(),200,await created.text());return(await created.json()).rule}
  const rules={plain:await rule(plain,'Plain processor rule'),acked:await rule(acked,'Acknowledged processor rule'),silenced:await rule(silenced,'Silenced processor rule'),windowed:await rule(windowed,'Maintenance processor rule'),calm:await rule(plain,'Calm memory rule','memory')}
  const now=Date.now(),iso=offset=>new Date(now+offset).toISOString()
  const windowInput=(scopeType,scopeId,startsAt,endsAt,reason,kind='maintenance')=>({kind,scopeType,...(scopeId?{scopeId}:{}),startsAt,endsAt,reason})
  const openWindow=async(...input)=>{const created=await session.call('windows.create',{data:windowInput(...input)});assert.equal(created.status(),201,await created.text());return(await created.json()).window}
  const listedWindows=async()=>(await read('windows.list')).windows.map(item=>item.id).sort()
  for(const [label,input,reason] of [
   ['unknown kind',windowInput('node',windowed.nodeId,iso(-60e3),iso(3600e3),'Fixture','holiday'),'unsupported alert window kind'],
   ['unknown scope',windowInput('account',windowed.nodeId,iso(-60e3),iso(3600e3),'Fixture'),'unsupported alert window scope'],
   ['global scope with a target',windowInput('global',windowed.nodeId,iso(-60e3),iso(3600e3),'Fixture'),'window scope ID does not match scope type'],
   ['node scope without a target',windowInput('node','',iso(-60e3),iso(3600e3),'Fixture'),'window scope ID does not match scope type'],
   ['unknown node',windowInput('node',crypto.randomUUID(),iso(-60e3),iso(3600e3),'Fixture'),'window node does not exist'],
   ['unknown rule',windowInput('rule',crypto.randomUUID(),iso(-60e3),iso(3600e3),'Fixture'),'window rule does not exist'],
   ['reversed range',windowInput('node',windowed.nodeId,iso(3600e3),iso(-60e3),'Fixture'),'alert window must last between zero and 90 days'],
   ['overlong range',windowInput('node',windowed.nodeId,iso(0),iso(91*86400e3),'Fixture'),'alert window must last between zero and 90 days'],
   ['missing reason',windowInput('node',windowed.nodeId,iso(-60e3),iso(3600e3),' '),'window reason must contain 1 to 500 bytes'],
  ])await refused(`Alert window with ${label}`,await session.call('windows.create',{data:input}),400,reason)
  assert.deepEqual(await listedWindows(),[],'Rejected alert windows must not be stored');assert.equal(stored('alert_windows'),0)
  const upcoming=await openWindow('node',plain.nodeId,iso(3600e3),iso(7200e3),'Upcoming maintenance'),expired=await openWindow('node',plain.nodeId,iso(-7200e3),iso(-3600e3),'Finished maintenance'),maintenance=await openWindow('node',windowed.nodeId,iso(-60e3),iso(3600e3),'Planned maintenance')
  assert.equal(maintenance.kind,'maintenance');assert.equal(maintenance.scopeType,'node');assert.equal(maintenance.scopeId,windowed.nodeId);assert.equal(maintenance.reason,'Planned maintenance')
  assert.deepEqual(await listedWindows(),[upcoming.id,maintenance.id].sort(),'Only unfinished windows are listed');assert.ok(!(await listedWindows()).includes(expired.id))

  const active=async()=>(await read('alerts.list')).alerts,history=async()=>(await read('alerts.history')).alerts
  const activeFor=async item=>(await active()).filter(alert=>alert.ruleId===item.id)
  const current=async id=>{const alert=(await active()).find(item=>item.id===id);assert.ok(alert,`Alert ${id} is no longer active`);return alert}
  const eventKinds=async id=>(await read('alerts.events',{query:{alertId:id,limit:500}})).events.map(event=>event.kind).reverse()
  const sentTo=(id,event)=>hits.filter(hit=>hit.path==='/alerts'&&hit.body.alertId===id&&(!event||hit.body.event===event))
  const deliveredFor=async id=>(await deliveries()).filter(item=>item.alertId===id)
  const settled=id=>until(async()=>{const items=await deliveredFor(id);return items.every(item=>!inFlight.includes(item.status))&&items},`Deliveries of alert ${id} did not settle`)
  assert.deepEqual(await active(),[],'Samples below the threshold must not raise alerts')
  for(const node of nodes)node.cpuPercent=95
  const alerts={}
  await until(async()=>{for(const [name,item] of Object.entries(rules))if(name!=='calm')alerts[name]=(await activeFor(item))[0];return ['plain','acked','silenced','windowed'].every(name=>alerts[name])},'Samples above the threshold did not raise alerts')
  for(const name of ['plain','acked','silenced','windowed']){
   const alert=alerts[name],node={plain,acked,silenced,windowed}[name]
   assert.equal(alert.status,'active');assert.equal(alert.ruleId,rules[name].id);assert.equal(alert.ruleName,rules[name].name);assert.equal(alert.nodeId,node.nodeId);assert.equal(alert.nodeName,node.name);assert.equal(alert.severity,'critical')
   assert.equal(alert.currentValue,95);assert.equal(alert.message,`${rules[name].name} is 95.00 (threshold 80.00)`);assert.equal(alert.resolvedAt,undefined);assert.equal(alert.acknowledgedAt,undefined);assert.equal(alert.silencedUntil,undefined)
   assert.equal((await activeFor(rules[name])).length,1,`${name}: one active alert per rule`)
  }
  for(const name of ['plain','acked','silenced']){
   const alert=alerts[name];assert.equal(alert.suppressionReason,undefined,`${name}: alert outside any window must not be suppressed`)
   await until(()=>sentTo(alert.id,'firing').length===1,`${name}: firing notification did not reach the receiver`)
   const hit=sentTo(alert.id,'firing')[0]
   assert.equal(hit.method,'POST');assert.equal(hit.authorization,'Bearer '+secret);assert.equal(hit.contentType,'application/json')
   assert.equal(hit.body.ruleId,rules[name].id);assert.equal(hit.body.ruleName,rules[name].name);assert.equal(hit.body.nodeId,alert.nodeId);assert.equal(hit.body.nodeName,alert.nodeName);assert.equal(hit.body.severity,'critical');assert.equal(hit.body.message,alert.message)
   assert.equal(new Date(hit.body.occurredAt).getTime(),new Date(alert.firstSeenAt).getTime(),`${name}: notification time must match the alert`)
   const first=(await deliveredFor(alert.id)).filter(item=>item.status==='sent').at(-1)
   assert.ok(first,`${name}: delivery history lacks the sent notification`);assert.equal(first.channelId,channel.id);assert.equal(first.channelName,'Alert receiver');assert.equal(first.kind,'webhook');assert.equal(first.testSend,false);assert.equal(first.attempts,1);assert.equal(first.httpStatus,204);assert.ok(new Date(first.deliveredAt).getTime()>0)
   assert.equal((await eventKinds(alert.id))[0],'firing')
  }
  const hushed=await deliveredFor(alerts.windowed.id)
  assert.equal(alerts.windowed.suppressionReason,'window','An alert raised inside a maintenance window must be marked as suppressed')
  assert.equal(hushed.length,1);assert.equal(hushed[0].status,'suppressed');assert.equal(hushed[0].lastError,'silenced or maintenance window');assert.equal(hushed[0].attempts,0);assert.equal(hushed[0].deliveredAt,undefined);assert.equal(hushed[0].channelId,channel.id)
  assert.deepEqual(await eventKinds(alerts.windowed.id),['firing'])

  await refused('Acknowledgement with an overlong note',await session.call('alerts.acknowledge',{parameters:{id:alerts.plain.id},data:{note:'n'.repeat(501)}}),400,'acknowledgement note is too long or invalid')
  await refused('Acknowledgement of an unknown alert',await session.call('alerts.acknowledge',{parameters:{id:crypto.randomUUID()},data:{note:'Fixture'}}),409,'alert is not active or already acknowledged')
  await refused('Acknowledgement of a malformed alert ID',await session.call('alerts.acknowledge',{parameters:{id:'not-an-alert'},data:{note:'Fixture'}}),404)
  for(const [label,body] of [['a past end',{until:iso(-60e3),reason:'Fixture'}],['an end beyond 90 days',{until:iso(91*86400e3),reason:'Fixture'}],['no reason',{until:iso(3600e3),reason:' '}]])await refused(`Silence with ${label}`,await session.call('alerts.silence',{parameters:{id:alerts.plain.id},data:body}),400,'alert silence must end within 90 days and include a short reason')
  await refused('Silence of an unknown alert',await session.call('alerts.silence',{parameters:{id:crypto.randomUUID()},data:{until:iso(3600e3),reason:'Fixture'}}),409,'alert is not active')
  const untouched=await current(alerts.plain.id);assert.equal(untouched.acknowledgedAt,undefined);assert.equal(untouched.silencedUntil,undefined);assert.equal(untouched.suppressionReason,undefined)
  const acknowledged=await session.call('alerts.acknowledge',{parameters:{id:alerts.acked.id},data:{note:'  Operator is investigating  '}});assert.equal(acknowledged.status(),204,await acknowledged.text())
  await refused('Repeated acknowledgement',await session.call('alerts.acknowledge',{parameters:{id:alerts.acked.id},data:{note:'Again'}}),409,'alert is not active or already acknowledged')
  const silenceUntil=iso(3600e3),hush=await session.call('alerts.silence',{parameters:{id:alerts.silenced.id},data:{until:silenceUntil,reason:'Known noisy host'}});assert.equal(hush.status(),204,await hush.text())
  const afterAck=await current(alerts.acked.id),afterSilence=await current(alerts.silenced.id)
  assert.ok(new Date(afterAck.acknowledgedAt).getTime()>=new Date(afterAck.firstSeenAt).getTime());assert.equal(afterAck.acknowledgedBy,'administrator');assert.equal(afterAck.acknowledgedNote,'Operator is investigating');assert.equal(afterAck.status,'active')
  assert.equal(new Date(afterSilence.silencedUntil).getTime(),new Date(silenceUntil).getTime());assert.equal(afterSilence.suppressionReason,'silenced');assert.equal(afterSilence.status,'active');assert.equal(afterSilence.acknowledgedAt,undefined)
  assert.ok((await eventKinds(alerts.acked.id)).includes('acknowledged'));assert.ok((await read('alerts.events',{query:{alertId:alerts.silenced.id}})).events.some(event=>event.kind==='silenced'&&event.message==='Known noisy host'))
  const quiet={}
  for(const name of ['acked','silenced','windowed'])quiet[name]={deliveries:(await settled(alerts[name].id)).map(item=>[item.id,item.status]).sort(),hits:sentTo(alerts[name].id).length}
  assert.equal(quiet.windowed.hits,0);const mark=Date.now(),reminded=sentTo(alerts.plain.id,'reminder').length
  plain.cpuPercent=97
  await until(async()=>{
   if(sentTo(alerts.plain.id,'reminder').filter(hit=>hit.body.message===`${rules.plain.name} is 97.00 (threshold 80.00)`).length<2)return false
   for(const name of ['acked','silenced','windowed'])if(new Date((await current(alerts[name].id)).lastSeenAt).getTime()<=mark)return false
   return true
  },'Unattended alert was not reminded while the others stayed quiet',45)
  assert.ok(sentTo(alerts.plain.id,'reminder').length>=reminded+2);assert.equal((await current(alerts.plain.id)).currentValue,97);assert.ok((await eventKinds(alerts.plain.id)).includes('reminder'))
  for(const name of ['acked','silenced','windowed']){
   assert.deepEqual((await settled(alerts[name].id)).map(item=>[item.id,item.status]).sort(),quiet[name].deliveries,`${name}: evaluation after the operator action queued another notification`)
   assert.equal(sentTo(alerts[name].id).length,quiet[name].hits,`${name}: receiver was notified after the alert was quieted`)
  }
  assert.ok(!(await eventKinds(alerts.acked.id)).slice((await eventKinds(alerts.acked.id)).indexOf('acknowledged')).includes('reminder'));assert.ok(!(await eventKinds(alerts.silenced.id)).slice((await eventKinds(alerts.silenced.id)).indexOf('silenced')).includes('reminder'))
  assert.deepEqual(await eventKinds(alerts.windowed.id),['firing'],'A window keeps every evaluation of the alert quiet');assert.equal((await current(alerts.windowed.id)).suppressionReason,'window')

  await refused('Unconfirmed window deletion',await session.call('windows.delete',{parameters:{id:maintenance.id},data:{confirm:false}}),400,'alert window deletion requires confirmation')
  await refused('Deletion of an unknown window',await session.call('windows.delete',{parameters:{id:crypto.randomUUID()},data:{confirm:true}}),404)
  assert.ok((await listedWindows()).includes(maintenance.id))
  const lifted=await session.call('windows.delete',{parameters:{id:maintenance.id},data:{confirm:true}});assert.equal(lifted.status(),204,await lifted.text());assert.ok(!(await listedWindows()).includes(maintenance.id))
  await refused('Repeated window deletion',await session.call('windows.delete',{parameters:{id:maintenance.id},data:{confirm:true}}),404)
  await until(()=>sentTo(alerts.windowed.id,'reminder').length>=1,'Alert was not notified after its maintenance window was withdrawn')
  assert.equal(sentTo(alerts.windowed.id,'firing').length,0,'The suppressed firing notification must not be delivered later');assert.equal(sentTo(alerts.windowed.id,'reminder')[0].authorization,'Bearer '+secret)
  assert.equal((await current(alerts.windowed.id)).suppressionReason,undefined);assert.ok((await deliveredFor(alerts.windowed.id)).some(item=>item.id===hushed[0].id&&item.status==='suppressed'))

  plain.cpuPercent=12
  await until(()=>sentTo(alerts.plain.id,'recovered').length===1,'Recovery notification did not reach the receiver')
  const recovery=sentTo(alerts.plain.id,'recovered')[0],resolved=(await history()).find(alert=>alert.id===alerts.plain.id)
  assert.ok(resolved,'Recovered alert must move to the alert history');assert.equal(resolved.status,'resolved');assert.equal(resolved.currentValue,12);assert.ok(new Date(resolved.resolvedAt).getTime()>new Date(resolved.firstSeenAt).getTime())
  assert.equal(recovery.authorization,'Bearer '+secret);assert.equal(recovery.body.message,'Alert condition recovered');assert.equal(recovery.body.ruleId,rules.plain.id);assert.equal(recovery.body.nodeId,plain.nodeId);assert.equal(new Date(recovery.body.occurredAt).getTime(),new Date(resolved.resolvedAt).getTime())
  assert.deepEqual(await activeFor(rules.plain),[]);assert.equal((await eventKinds(alerts.plain.id)).at(-1),'recovered');assert.equal(sentTo(alerts.plain.id).at(-1).body.event,'recovered');assert.equal(sentTo(alerts.plain.id)[0].body.event,'firing')
  await refused('Acknowledgement of a resolved alert',await session.call('alerts.acknowledge',{parameters:{id:alerts.plain.id},data:{note:'Late'}}),409,'alert is not active or already acknowledged')
  await refused('Silence of a resolved alert',await session.call('alerts.silence',{parameters:{id:alerts.plain.id},data:{until:iso(3600e3),reason:'Late'}}),409,'alert is not active')

  const scoped=await openWindow('rule',rules.windowed.id,iso(-60e3),iso(3600e3),'Rule maintenance');assert.equal(scoped.scopeType,'rule')
  for(const name of ['acked','silenced','windowed'])quiet[name]={deliveries:(await settled(alerts[name].id)).map(item=>[item.id,item.status]).sort(),hits:sentTo(alerts[name].id).length}
  acked.cpuPercent=12;silenced.cpuPercent=12;windowed.cpuPercent=12
  await until(async()=>{const past=await history();return ['acked','silenced','windowed'].every(name=>past.find(alert=>alert.id===alerts[name].id)?.status==='resolved')},'Quieted alerts did not resolve after their samples recovered')
  await sleep(1500)
  for(const name of ['acked','silenced','windowed']){
   assert.equal((await eventKinds(alerts[name].id)).at(-1),'recovered',`${name}: recovery must be recorded`)
   assert.deepEqual((await settled(alerts[name].id)).map(item=>[item.id,item.status]).sort(),quiet[name].deliveries,`${name}: recovery of a quieted alert queued a notification`)
   assert.equal(sentTo(alerts[name].id).length,quiet[name].hits,`${name}: recovery of a quieted alert reached the receiver`);assert.equal(sentTo(alerts[name].id,'recovered').length,0)
  }
  assert.deepEqual(await active(),[]);const kept=(await history()).find(alert=>alert.id===alerts.acked.id);assert.equal(kept.acknowledgedBy,'administrator');assert.equal(kept.acknowledgedNote,'Operator is investigating')

  const blanket=await openWindow('global','',iso(-60e3),iso(3600e3),'Site maintenance');assert.equal(blanket.scopeId,undefined)
  plain.cpuPercent=95
  const repeated=await until(async()=>(await activeFor(rules.plain))[0],'Rule did not raise a new alert after recovering')
  assert.notEqual(repeated.id,alerts.plain.id,'A recovered rule raises a new alert');assert.equal(repeated.suppressionReason,'window')
  const blanketed=await settled(repeated.id);assert.equal(blanketed.length,1);assert.equal(blanketed[0].status,'suppressed');assert.equal(blanketed[0].lastError,'silenced or maintenance window')
  await until(async()=>new Date((await current(repeated.id)).lastSeenAt).getTime()>new Date(repeated.firstSeenAt).getTime(),'Suppressed alert was not evaluated again')
  assert.equal(sentTo(repeated.id).length,0,'A global maintenance window must keep the receiver quiet');assert.deepEqual((await settled(repeated.id)).map(item=>item.status),['suppressed']);assert.deepEqual(await eventKinds(repeated.id),['firing'])

  const everything=[...(await active()),...(await history())]
  assert.ok(!everything.some(alert=>alert.ruleId===rules.calm.id),'A rule whose samples stay below the threshold must never raise an alert')
  assert.deepEqual([...new Set(everything.map(alert=>alert.ruleId))].sort(),[rules.plain.id,rules.acked.id,rules.silenced.id,rules.windowed.id].sort(),'Only the exceeded rules raised alerts')
  assert.ok(hits.every(hit=>hit.path==='/alerts'),'A channel the rules do not select must not be notified');assert.equal((await deliveries()).filter(item=>item.channelId===decoy.id).length,0)
  assert.ok(hits.every(hit=>hit.authorization==='Bearer '+secret&&Object.values(alerts).some(alert=>alert.id===hit.body.alertId)),'Receiver saw a notification for an unexpected alert')
  assert.equal((await deliveries()).filter(item=>item.alertId&&item.status==='sent').length,hits.length,'Delivery history must match what the receiver saw')
  console.log('audit-alert-delivery passed.')
 }finally{
  for(const node of nodes)await node.stop().catch(()=>{})
  receiver.close();receiver.closeAllConnections();mailbox.close();if(session)await session.close().catch(()=>{})
  await core.stop();fs.rmSync(core.directory,{recursive:true,force:true})
 }
})().catch(error=>{console.error(error);process.exit(1)})
