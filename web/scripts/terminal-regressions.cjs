const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),crypto=require('node:crypto')
const {spawnSync}=require('node:child_process'),{request}=require('playwright')
const {startCore,signIn,eventually,sleep,sqlite}=require('./core-fixture.cjs'),rootAgent=require('./agent-fixture.cjs')
const capabilities={colorDepth:0,hyperlinks:false},rejectedTicket={code:1008,reason:'terminal authorization rejected'}
function attach(session,granted){
 const socket=new WebSocket(session.base.replace(/^http/,'ws')+session.operation('terminal.connect').path+'?ticket='+encodeURIComponent(granted.ticket),{headers:{Cookie:session.cookie,Origin:session.base}})
 const terminal={output:'',frames:[],unexpected:[],closed:null}
 socket.addEventListener('message',event=>{
  let message;try{message=JSON.parse(event.data)}catch{terminal.unexpected.push(String(event.data).slice(0,120));return}
  if(message.type!=='terminal'||message.frame?.streamId!==granted.streamId){terminal.unexpected.push(JSON.stringify(message).slice(0,120));return}
  if(message.frame.action==='output')terminal.output+=Buffer.from(message.frame.data,'base64').toString('utf8');else terminal.frames.push(message.frame)
 })
 socket.addEventListener('close',event=>{terminal.closed={code:event.code,reason:event.reason}})
 socket.addEventListener('error',()=>{terminal.closed??={code:0,reason:'connection failed'}})
 terminal.frame=(action,fields={})=>JSON.stringify({streamId:granted.streamId,action,...fields})
 terminal.input=text=>terminal.frame('input',{data:Buffer.from(text).toString('base64')})
 terminal.send=payload=>socket.send(payload)
 terminal.type=text=>socket.send(terminal.input(text))
 terminal.ready=async()=>{await eventually(()=>terminal.closed||terminal.frames.some(frame=>frame.action==='ready'),'Terminal did not become ready',200,50);assert.equal(terminal.closed,null,'Terminal closed before it was ready')}
 terminal.shows=text=>eventually(()=>terminal.output.includes(text),`Terminal never printed ${text}; last output ${JSON.stringify(terminal.output.slice(-300))}`,200,50)
 terminal.ended=()=>eventually(()=>terminal.closed,'Terminal connection stayed open',200,50)
 terminal.drop=()=>{socket.close();return terminal.ended()}
 return terminal
}
module.exports=async function terminals(){
 const files=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-terminal-')),marker=name=>path.join(files,name)
 const unique=()=>String(700000+crypto.randomInt(200000)),alive=duration=>spawnSync('pgrep',['-f',`^sleep ${duration}$`],{encoding:'utf8'}).stdout.split('\n').filter(Boolean)
 const runs=duration=>eventually(()=>alive(duration).length===1,`sleep ${duration} did not start`,200,50),gone=duration=>eventually(()=>alive(duration).length===0,`sleep ${duration} survived its terminal`,200,50)
 const core=await startCore(),anonymous=await request.newContext(),opened=[],sockets=[];let session,agent,started=0
 const open=async credentials=>{const value=await signIn(core.base,credentials);opened.push(value);value.cookie=(await value.api.storageState()).cookies.map(cookie=>`${cookie.name}=${cookie.value}`).join('; ');return value}
 const connect=(actor,granted)=>{const terminal=attach(actor,granted);sockets.push(terminal);return terminal}
 try{
  const credentials=core.credentials();session=await open(credentials)
  agent=await rootAgent({session,displayName:'Terminal fixture',files,env:{SHELL:'/bin/sh'}});await agent.start()
  const node={nodeId:agent.nodeId}
  const create=(actor=session,data={targetKind:'host',capabilities})=>actor.call('terminal.create',{parameters:node,data})
  const grant=async(actor=session)=>{const response=await create(actor);assert.equal(response.status(),201,await response.text());const body=await response.json();assert.match(body.streamId,/^[A-Za-z0-9_-]{32,64}$/);assert.ok(Date.parse(body.expiresAt)>Date.now());return body}
  const resume=(streamId,actor=session)=>actor.call('terminal.resume',{parameters:{...node,streamId}})
  const cancel=(streamId,actor=session)=>actor.call('terminal.cancel',{parameters:{...node,streamId}})
  const reattachable=(streamId,actor=session)=>eventually(async()=>{const response=await resume(streamId,actor);if(response.status()===409)return null;assert.equal(response.status(),201,await response.text());return response.json()},'Detached terminal could not be resumed',100,50)
  const unavailable=(streamId,actor=session)=>eventually(async()=>{const response=await resume(streamId,actor);return response.status()===410&&(await response.text()).includes('terminal session is no longer available')},`Terminal ${streamId} stayed resumable`,100,50)
  const refused=async(label,response,status,text)=>{const body=await response.text();assert.equal(response.status(),status,`${label}: ${response.status()} ${body}`);if(text)assert.ok(body.includes(text),`${label}: ${body}`)}
  const live=async(actor=session)=>{const granted=await eventually(async()=>{const response=await create(actor);return response.status()===201?response.json():null},'Terminal slot did not become available',100,100),terminal=connect(actor,granted);await terminal.ready();started++;return {granted,terminal}}

  const {granted:first,terminal:one}=await live()
  one.type('echo uid-$(id -u)-end\n');await one.shows('uid-0-end')
  one.type(`touch ${marker('typed')}\n`);await eventually(()=>fs.existsSync(marker('typed')),'Typed input did not reach the shell',200,50)
  const kept='kept'+crypto.randomBytes(4).toString('hex'),background=unique()
  one.type(`KEEP=${kept}; sleep ${background} &\n`);await runs(background)
  await refused('Resuming an attached terminal',await resume(first.streamId),409,'terminal connection is still attached')
  await one.drop();assert.deepEqual(one.unexpected,[])
  assert.deepEqual(await connect(session,first).ended(),rejectedTicket,'A consumed ticket cannot attach again')
  const second=await reattachable(first.streamId);assert.equal(second.streamId,first.streamId);assert.notEqual(second.ticket,first.ticket)
  assert.equal(alive(background).length,1,'A background job survives the detached terminal')
  const two=connect(session,second);await two.ready()
  two.type('echo keep-$KEEP-end\n');await two.shows(`keep-${kept}-end`)
  two.type('kill %1 && echo killed-$((6*7))-end\n');await two.shows('killed-42-end');await gone(background)

  const other=await open(credentials)
  await refused('Resuming from another session',await resume(first.streamId,other),410,'terminal session is no longer available')
  assert.equal((await cancel(first.streamId,other)).status(),204)
  two.type('echo owned-$((6*7+1))-end\n');await two.shows('owned-43-end')
  const pending=await grant()
  await refused('Third terminal on one node',await create(),429,'node already has two active terminals')
  assert.deepEqual(await connect(other,pending).ended(),rejectedTicket,'A ticket is bound to the session that requested it')
  assert.equal((await cancel(pending.streamId)).status(),204)
  assert.deepEqual(await connect(session,pending).ended(),rejectedTicket,'A cancelled ticket cannot attach')

  const hostile=[
   ['trailing JSON',terminal=>terminal.input(`touch ${marker('hostile')}\n`)+'{}'],
   ['another stream ID',terminal=>JSON.stringify({streamId:first.streamId,action:'input',data:Buffer.from(`touch ${marker('hostile')}\n`).toString('base64')})],
   ['an Agent-only action',terminal=>terminal.frame('attach')],
   ['an unknown field',terminal=>JSON.stringify({...JSON.parse(terminal.input(`touch ${marker('hostile')}\n`)),extra:true})],
   ['a binary message',terminal=>Buffer.from(terminal.input(`touch ${marker('hostile')}\n`))],
  ]
  for(const [label,build] of hostile){
   const {granted,terminal}=await live()
   terminal.send(build(terminal));await terminal.ended();await unavailable(granted.streamId)
   assert.ok(!fs.existsSync(marker('hostile')),`A frame with ${label} must not reach a shell`)
  }
  two.type('echo intact-$((6*7+2))-end\n');await two.shows('intact-44-end');assert.ok(!fs.existsSync(marker('hostile')),'Rejected frames must not reach another terminal')

  const foreground=unique();two.type(`sleep ${foreground}\n`);await runs(foreground)
  await two.drop();assert.deepEqual(two.unexpected,[]);await sleep(300);assert.equal(alive(foreground).length,1,'A foreground command survives the detached terminal')
  assert.equal((await cancel(first.streamId)).status(),204)
  await unavailable(first.streamId);await gone(foreground)

  const revocable=await open(credentials),{granted:third,terminal:three}=await live(revocable)
  const held=unique();three.type(`sleep ${held}\n`);await runs(held);await three.drop()
  const reissued=await reattachable(third.streamId,revocable)
  const target=(await(await revocable.call('sessions.list')).json()).sessions.find(item=>item.current);assert.ok(target,'Revocable session is listed')
  const revoked=await session.call('sessions.revoke',{parameters:{id:target.id}});assert.ok(revoked.ok(),await revoked.text())
  await gone(held)
  assert.equal((await resume(third.streamId,revocable)).status(),401,'A revoked session cannot resume its terminal');await unavailable(third.streamId)
  const late=connect(revocable,reissued);await late.ended();assert.ok(!late.frames.some(frame=>frame.action==='ready'),'A revoked session cannot attach with an issued ticket')

  const leaving=await open(credentials),{terminal:four}=await live(leaving)
  const busy=unique();four.type(`sleep ${busy}\n`);await runs(busy)
  const signedOut=await leaving.call('auth.logout');assert.ok(signedOut.ok(),await signedOut.text())
  await four.ended();await gone(busy)

  for(const [label,data,status] of [['unknown target',{targetKind:'pod',capabilities},400],['host target with a container',{targetKind:'host',containerId:'a'.repeat(64),capabilities},400],['short container ID',{targetKind:'container',containerId:'abc',capabilities},400],['unsupported colour depth',{targetKind:'host',capabilities:{colorDepth:3,hyperlinks:false}},400],['container without Docker inventory',{targetKind:'container',containerId:'a'.repeat(64),capabilities},409]])await refused(`Terminal for ${label}`,await create(session,data),status)
  await refused('Anonymous terminal',await anonymous.post(session.url('terminal.create',node),{headers:{Origin:core.base},data:{targetKind:'host',capabilities}}),401)
  assert.equal((await anonymous.get(core.base+session.operation('terminal.connect').path+'?ticket='+first.ticket)).status(),401,'Anonymous clients cannot reach the terminal stream')
  const viewerAccount={username:'terminal-viewer',displayName:'Terminal Viewer',password:'Terminal-viewer-password-8846',role:'viewer'}
  const createdViewer=await session.call('accounts.create',{data:viewerAccount});assert.equal(createdViewer.status(),201,await createdViewer.text())
  await refused('Viewer terminal',await create(await open(viewerAccount)),403)

  const {granted:last,terminal:five}=await live()
  const orphan=unique();five.type(`sleep ${orphan}\n`);await runs(orphan);await five.drop()
  await agent.stop();await gone(orphan);await unavailable(last.streamId)
  await eventually(async()=>(await create()).status()===409,'A stopped Agent must not accept terminals',100,100)
  await eventually(()=>{const rows=sqlite(core.database,"SELECT action,COUNT(*) FROM audit_entries WHERE action IN ('terminal_start','terminal_end') GROUP BY action ORDER BY action");return JSON.stringify(rows)===JSON.stringify([['terminal_end',started],['terminal_start',started]])},`Every started terminal must be audited as started and ended (${started})`,60,100)
  console.log('terminal-regressions passed.')
 }finally{
  for(const terminal of sockets)if(!terminal.closed)await terminal.drop().catch(()=>{})
  if(agent)await agent.remove().catch(()=>{});for(const value of opened)await value.close().catch(()=>{})
  await anonymous.dispose();await core.stop();fs.rmSync(core.directory,{recursive:true,force:true});spawnSync('sudo',['rm','-rf',files])
 }
}
