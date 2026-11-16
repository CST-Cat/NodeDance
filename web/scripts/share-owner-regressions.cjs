const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),crypto=require('node:crypto')
const {spawnSync}=require('node:child_process'),{request}=require('playwright')
const {startCore,signIn}=require('./core-fixture.cjs'),rootAgent=require('./agent-fixture.cjs')
const nodeBody='node share body\n',uploadBody='uploaded share body'
module.exports=async function shareOwner(){
 const files=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-share-owner-'));fs.writeFileSync(path.join(files,'shared.txt'),nodeBody)
 const core=await startCore(),visitor=await request.newContext(),opened=[];let session,agent
 const open=async credentials=>{const value=await signIn(core.base,credentials);opened.push(value);return value}
 try{
  session=await open(core.credentials())
  agent=await rootAgent({session,displayName:'Share owner fixture',files});await agent.start()
  const account={username:'share-owner',displayName:'Share Owner',password:'Share-owner-password-7319',role:'administrator'}
  const created=await session.call('accounts.create',{data:account});assert.equal(created.status(),201,await created.text())
  const ownerId=(await created.json()).account.id,intent=()=>crypto.randomBytes(32).toString('hex')
  let owner=await open(account)
  const shared=await owner.call('shares.create',{data:{requestId:intent(),nodeId:agent.nodeId,paths:['/shared.txt'],name:'node-share',expiryDays:7,maxDownloads:0}});assert.equal(shared.status(),201,await shared.text())
  const nodeShare=await shared.json()
  const upload=await owner.api.put(owner.url('shares.upload',{},{name:'uploaded.txt',expiryDays:7,maxDownloads:0,requestId:intent()}),{headers:{...owner.headers,'Content-Type':'application/octet-stream'},data:uploadBody});assert.equal(upload.status(),201,await upload.text())
  const uploadShare=await upload.json()
  const invited=await owner.call('share-requests.create',{data:{requestId:intent(),name:'Owner collection',notes:'',expiryDays:1,maxFiles:8,maxBytes:4096}});assert.equal(invited.status(),200,await invited.text())
  const invitation=await invited.json()
  const links={
   nodeShare:()=>visitor.get(session.url('shares.public',{id:nodeShare.id})),
   nodeFile:()=>visitor.get(session.url('shares.hotlink',{id:nodeShare.id},{path:'shared.txt'})),
   uploadShare:()=>visitor.get(session.url('shares.public',{id:uploadShare.id})),
   uploadFile:()=>visitor.get(session.url('shares.hotlink',{id:uploadShare.id},{path:'uploaded.txt'})),
   invitation:()=>visitor.get(session.url('share-requests.public',{id:invitation.id})),
  }
  const collect=name=>visitor.put(session.url('share-requests.upload',{id:invitation.id},{name}),{headers:{Origin:core.base,'Content-Type':'application/octet-stream'},data:'collected'})
  const published=async(label,expected)=>{for(const [name,read] of Object.entries(links)){const response=await read();assert.equal(response.status(),expected[name]??expected.all,`${label}: ${name} answered ${response.status()}`)}}
  const setAccount=async(label,changes)=>{const response=await session.call('accounts.update',{parameters:{id:ownerId},data:{username:account.username,displayName:account.displayName,role:'administrator',enabled:true,confirmed:true,...changes}});assert.equal(response.status(),204,`${label}: ${await response.text()}`)}
  const downloads=async id=>(await(await owner.call('shares.get',{parameters:{id}})).json()).downloads

  await published('Enabled owner',{all:200})
  assert.equal(await(await links.nodeFile()).text(),nodeBody);assert.equal(await(await links.uploadFile()).text(),uploadBody)
  assert.equal((await collect('before.txt')).status(),201,'An enabled owner collects uploads')
  const counted={node:await downloads(nodeShare.id),upload:await downloads(uploadShare.id)};assert.deepEqual(counted,{node:2,upload:2},'Each anonymous file download is counted once')
  const foreign=await session.call('shares.get',{parameters:{id:nodeShare.id}});assert.equal(foreign.status(),404,'Another administrator cannot read the owner share record')

  await setAccount('Disabling the owner',{enabled:false})
  await published('Disabled owner',{all:404})
  assert.equal((await collect('disabled.txt')).status(),404,'A disabled owner must not collect uploads')
  assert.equal((await visitor.post(session.url('shares.unlock',{id:nodeShare.id}),{headers:{Origin:core.base},data:{password:''}})).status(),404,'A withdrawn share must not answer unlock attempts')
  assert.equal((await owner.api.get(core.base+'/api/v1/auth/me')).status(),401,'Disabling an account revokes its sessions')

  await setAccount('Enabling the owner again',{enabled:true})
  await published('Re-enabled owner',{all:200})
  owner=await open(account)
  assert.deepEqual({node:await downloads(nodeShare.id),upload:await downloads(uploadShare.id)},{node:counted.node+1,upload:counted.upload+1},'Requests refused while the owner was disabled must not consume downloads')
  const collected=(await(await owner.call('share-requests.get',{parameters:{id:invitation.id}})).json()).entries.map(entry=>entry.path);assert.equal(collected.length,1,'Only the upload accepted while the owner was enabled is stored: '+JSON.stringify(collected))

  await setAccount('Demoting the owner',{role:'viewer'})
  await published('Viewer owner',{all:404})
  assert.equal((await collect('viewer.txt')).status(),404,'A viewer owner must not collect uploads')
  await setAccount('Promoting the owner again',{role:'administrator'})
  await published('Restored owner',{all:200})

  const removedNode=await session.call('nodes.delete',{parameters:{nodeId:agent.nodeId},data:{confirm:true}});assert.ok(removedNode.ok(),await removedNode.text())
  await published('Deleted source node',{all:200,nodeShare:404,nodeFile:404})

  const removedOwner=await session.call('accounts.delete',{parameters:{id:ownerId},data:{confirmed:true}});assert.ok(removedOwner.ok(),await removedOwner.text())
  await published('Deleted owner',{all:404})
  assert.equal((await collect('deleted.txt')).status(),404,'A deleted owner must not collect uploads')
  console.log('share-owner-regressions passed.')
 }finally{
  if(agent)await agent.remove().catch(()=>{});for(const value of opened)await value.close().catch(()=>{})
  await visitor.dispose();await core.stop();fs.rmSync(core.directory,{recursive:true,force:true});spawnSync('sudo',['rm','-rf',files])
 }
}
