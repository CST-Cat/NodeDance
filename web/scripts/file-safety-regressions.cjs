const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),crypto=require('node:crypto')
const {spawnSync}=require('node:child_process')
const {startCore,signIn,eventually}=require('./core-fixture.cjs'),rootAgent=require('./agent-fixture.cjs')
const limit=1048576
const archiveBuilder=`
import gzip,io,os,sys,tarfile,zipfile
directory,marker=sys.argv[1],sys.argv[2]
def tar(name,members,mode='w'):
    with tarfile.open(os.path.join(directory,name),mode) as archive:
        for member,data in members:
            archive.addfile(member,io.BytesIO(data) if data is not None else None)
def regular(name,data=b'data'):
    member=tarfile.TarInfo(name);member.size=len(data);member.mode=0o600;return (member,data)
def special(name,kind,link=''):
    member=tarfile.TarInfo(name);member.type=kind;member.linkname=link;member.mode=0o600;return (member,None)
def archive(name,members,method=zipfile.ZIP_STORED):
    with zipfile.ZipFile(os.path.join(directory,name),'w',method) as bundle:
        for member,data in members:
            bundle.writestr(member,data)
tar('control.tar',[regular('control.txt',b'control')])
tar('traversal.tar',[regular('../'+marker)])
tar('late-traversal.tar.gz',[regular('published-first.txt'),regular('nested/../../'+marker)],'w:gz')
tar('absolute.tar',[regular('/'+marker)])
tar('symlink.tar',[special('link',tarfile.SYMTYPE,'/etc/passwd')])
tar('symlink-then-write.tar',[special('hop',tarfile.SYMTYPE,'..'),regular('hop/'+marker)])
tar('hardlink.tar',[special('link',tarfile.LNKTYPE,'/etc/passwd')])
tar('device.tar',[special('device',tarfile.CHRTYPE)])
tar('fifo.tar',[special('pipe',tarfile.FIFOTYPE)])
tar('duplicate.tar',[regular('same'),regular('same')])
tar('file-parent.tar',[regular('parent'),regular('parent/child')])
tar('backslash.tar',[regular('..\\\\'+marker)])
tar('oversize.tar',[regular('big',bytes(${limit}+1))])
tar('sum-oversize.tar',[regular('first',bytes(${limit}//2+1)),regular('second',bytes(${limit}//2+1))])
tar('bomb.tar.gz',[regular('bomb',bytes(8*${limit}))],'w:gz')
tar('empty.tar',[])
damaged=bytearray(gzip.compress(open(os.path.join(directory,'control.tar'),'rb').read()));damaged[-8]^=0xff
open(os.path.join(directory,'checksum.tar.gz'),'wb').write(damaged)
archive('control.zip',[('zip-control.txt','control')])
archive('traversal.zip',[('../'+marker,'data')])
archive('absolute.zip',[('/'+marker,'data')])
archive('backslash.zip',[('..\\\\'+marker,'data')])
archive('duplicate.zip',[('same','a'),('same','b')])
archive('file-parent.zip',[('parent','a'),('parent/child','b')])
archive('bomb.zip',[('bomb',bytes(8*${limit}))],zipfile.ZIP_DEFLATED)
link=zipfile.ZipInfo('link');link.external_attr=0o120777<<16
archive('symlink.zip',[(link,'/etc/passwd')])
`
const hostileArchives={
 'traversal.tar':'invalid_path','late-traversal.tar.gz':'invalid_path','absolute.tar':'invalid_path','symlink.tar':'invalid_path','symlink-then-write.tar':'invalid_path','hardlink.tar':'invalid_path','device.tar':'invalid_path','fifo.tar':'invalid_path','duplicate.tar':'invalid_path','file-parent.tar':'invalid_path','backslash.tar':'invalid_path','empty.tar':'invalid_path',
 'oversize.tar':'limit_exceeded','sum-oversize.tar':'limit_exceeded','bomb.tar.gz':'limit_exceeded','checksum.tar.gz':'operation_rejected',
 'traversal.zip':'invalid_path','absolute.zip':'invalid_path','backslash.zip':'invalid_path','duplicate.zip':'invalid_path','file-parent.zip':'invalid_path','symlink.zip':'invalid_path','bomb.zip':'limit_exceeded',
}
module.exports=async function fileSafety(){
 const outer=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-file-safety-')),files=path.join(outer,'root'),marker=`nodedance-e2e-escape-${crypto.randomBytes(8).toString('hex')}.txt`
 fs.mkdirSync(files);for(const name of ['data','out','archives'])fs.mkdirSync(path.join(files,name))
 fs.writeFileSync(path.join(files,'data','file.txt'),'inside\n');fs.writeFileSync(path.join(outer,'outside.txt'),'outside\n');fs.mkdirSync(path.join(outer,'outside-dir'));fs.writeFileSync(path.join(outer,'outside-dir','kept.txt'),'kept\n')
 fs.symlinkSync(path.join(outer,'outside.txt'),path.join(files,'escape-link'));fs.symlinkSync(outer,path.join(files,'escape-dir'));fs.symlinkSync('data/file.txt',path.join(files,'inner-link'));fs.symlinkSync('/etc/hostname',path.join(files,'data','host-link'))
 const built=spawnSync('python3',['-W','ignore','-c',archiveBuilder,path.join(files,'archives'),marker],{encoding:'utf8'});assert.equal(built.status,0,built.stderr)
 assert.deepEqual(fs.readdirSync(path.join(files,'archives')).filter(name=>!name.startsWith('control.')).sort(),Object.keys(hostileArchives).sort(),'Archive fixture is incomplete')
 const snapshot=(directory,prefix='')=>fs.readdirSync(directory,{withFileTypes:true}).flatMap(entry=>entry.isSymbolicLink()?[prefix+entry.name+'@']:entry.isDirectory()?[prefix+entry.name+'/',...snapshot(path.join(directory,entry.name),prefix+entry.name+'/')]:[prefix+entry.name]).sort()
 const outside=()=>({listing:snapshot(outer).filter(name=>!name.startsWith('root/')),file:fs.readFileSync(path.join(outer,'outside.txt'),'utf8'),kept:fs.readFileSync(path.join(outer,'outside-dir','kept.txt'),'utf8'),host:fs.existsSync('/'+marker),temp:fs.existsSync(path.join(os.tmpdir(),marker))})
 const core=await startCore();let session,agent
 try{
  session=await signIn(core.base,core.credentials())
  agent=await rootAgent({session,displayName:'File safety fixture',files,env:{NODEDANCE_AGENT_MAX_FILE_BYTES:String(limit)}});await agent.start()
  const node={nodeId:agent.nodeId},untouched=outside()
  const settle=taskId=>eventually(async()=>{const task=await(await session.call('tasks.get',{parameters:{...node,taskId}})).json();return ['queued','running'].includes(task.status)?null:task},`File task ${taskId} did not finish`,600,50)
  const submit=(data,key=crypto.randomUUID())=>session.call('files.action',{parameters:node,headers:{'Idempotency-Key':key},data})
  const run=async(data,key)=>{const response=await submit(data,key);assert.equal(response.status(),202,await response.text());return settle((await response.json()).taskId)}
  const failed=async(label,data,code)=>{const task=await run(data);assert.equal(task.status,'failed',`${label}: ${JSON.stringify(task.result)}`);assert.equal(task.result.observedState,'unchanged',`${label}: state`);assert.equal(task.result.resourceRevision,code,`${label}: reason`);assert.deepEqual(outside(),untouched,`${label}: reached outside the file root`);return task}
  const refused=async(label,data,text)=>{const response=await submit(data),body=await response.text();assert.equal(response.status(),400,`${label}: ${response.status()} ${body}`);assert.ok(body.includes(text),`${label}: ${body}`);assert.deepEqual(outside(),untouched,`${label}: reached outside the file root`)}

  const control=await run({action:'extract',path:'/archives/control.tar',newPath:'/out'});assert.equal(control.status,'succeeded',JSON.stringify(control.result));assert.equal(fs.readFileSync(path.join(files,'out','control.txt'),'utf8'),'control')
  const zipControl=await run({action:'extract',path:'/archives/control.zip',newPath:'/out'});assert.equal(zipControl.status,'succeeded',JSON.stringify(zipControl.result));assert.equal(fs.readFileSync(path.join(files,'out','zip-control.txt'),'utf8'),'control')
  const inside=snapshot(files)
  for(const [name,code] of Object.entries(hostileArchives)){
   await failed(`Extracting ${name}`,{action:'extract',path:'/archives/'+name,newPath:'/out'},code)
   assert.deepEqual(snapshot(files),inside,`Extracting ${name} left files or staging directories behind`)
  }
  assert.deepEqual(fs.readdirSync(path.join(files,'out')).sort(),['control.txt','zip-control.txt'],'Rejected archives must not publish any member')
  await failed('Extracting over existing entries',{action:'extract',path:'/archives/control.tar',newPath:'/out'},'destination_exists');assert.equal(fs.readFileSync(path.join(files,'out','control.txt'),'utf8'),'control')
  await failed('Extracting through a symlinked directory',{action:'extract',path:'/archives/control.tar',newPath:'/escape-dir'},'invalid_path')
  await failed('Extracting a symlinked archive',{action:'extract',path:'/inner-link',newPath:'/data'},'invalid_path')
  assert.deepEqual(snapshot(files),inside)

  const reads=['files.stat','files.list','files.text','files.download','files.preview','files.search']
  const status=async(name,value)=>(await session.call(name,{parameters:node,query:name==='files.search'?{path:value,filter:'file',recursive:'true'}:{path:value}})).status()
  for(const value of ['/data/../escape','/../escape','/data/../../etc/passwd','/data/..','/..','/data/file.txt/..','/escape-dir/../outside.txt','/data/../data/file.txt'])for(const name of reads)assert.equal(await status(name,value),400,`${name} accepted traversal path ${value}`)
  for(const value of ['relative/path','data/file.txt','/data\u0000file','/'+'a/'.repeat(2500)+'deep','/escape-dir/outside.txt','/escape-dir/outside-dir/kept.txt','/escape-dir/root/data/file.txt'])for(const name of reads){const answered=await status(name,value);assert.ok(answered>=400,`${name} answered ${answered} for hostile path ${JSON.stringify(value.slice(0,48))}`)}
  for(const value of ['/escape-link','/escape-dir','/inner-link','/data/host-link'])for(const name of ['files.list','files.text','files.download','files.preview']){const answered=await status(name,value);assert.ok(answered>=400,`${name} followed symlink ${value}: ${answered}`)}
  for(const [value,name] of [['/escape-link','escape-link'],['/escape-dir','escape-dir'],['/data/host-link','host-link']]){const stated=await session.call('files.stat',{parameters:node,query:{path:value}});assert.equal(stated.status(),200,await stated.text());const body=await stated.text(),entry=JSON.parse(body);assert.equal(entry.name,name);assert.equal(entry.path,value);assert.equal(entry.kind,'symlink',`${value} must be reported as a link, not as its target`);assert.equal(entry.version,undefined,`${value}: a link must not expose a content version`);assert.ok(!body.includes('outside')&&!body.includes('hostname'),`${value}: stat disclosed the link target`)}
  const listed=await(await session.call('files.list',{parameters:node,query:{path:'/'}})).json();assert.deepEqual(listed.entries.map(entry=>[entry.name,entry.kind]).sort(),[['archives','directory'],['data','directory'],['out','directory']],'Directory listings must not offer symlinks')
  assert.deepEqual((await(await session.call('files.list',{parameters:node,query:{path:'/data'}})).json()).entries.map(entry=>entry.name),['file.txt'])
  const found=await(await session.call('files.search',{parameters:node,query:{path:'/',filter:'kept',recursive:'true'}})).json();assert.deepEqual(found.entries,[],'Recursive search must not descend through a symlinked directory')
  for(const [value,canonical] of [['/./data','/data'],['/data/','/data'],['/.','/']]){
   const dotted=await session.call('files.list',{parameters:node,query:{path:value}}),cleaned=await session.call('files.list',{parameters:node,query:{path:canonical}});assert.equal(dotted.status(),200,`${value}: ${await dotted.text()}`)
   assert.deepEqual((await dotted.json()).entries.map(entry=>entry.path),(await cleaned.json()).entries.map(entry=>entry.path),`${value} must resolve to ${canonical} inside the file root`)
  }
  const plain=await session.call('files.text',{parameters:node,query:{path:'/data/file.txt'}});assert.equal(plain.status(),200);assert.equal((await plain.json()).text,'inside\n')

  await refused('Creating a directory through ..',{action:'mkdir',path:'/data/../made'},'invalid file')
  await refused('Copying to a .. destination',{action:'copy',path:'/data/file.txt',newPath:'/../copied.txt'},'invalid file')
  await refused('Moving to a .. destination',{action:'move',path:'/data/file.txt',newPath:'/data/../../moved.txt'},'invalid file')
  await refused('Archiving a .. source',{action:'archive',path:'/data/bundle.zip',paths:['/../'+path.basename(outer)]},'invalid file')
  await refused('Extracting to a .. destination',{action:'extract',path:'/archives/control.tar',newPath:'/out/../..'},'invalid file')
  await failed('Creating a directory through a symlinked directory',{action:'mkdir',path:'/escape-dir/made'},'invalid_path')
  await failed('Copying into a symlinked directory',{action:'copy',path:'/data/file.txt',newPath:'/escape-dir/copied.txt'},'invalid_path')
  await failed('Moving into a symlinked directory',{action:'move',path:'/data/file.txt',newPath:'/escape-dir/moved.txt'},'invalid_path')
  await failed('Archiving a symlinked directory',{action:'archive',path:'/data/bundle.zip',paths:['/escape-dir']},'invalid_path')
  await failed('Archiving into a symlinked directory',{action:'archive',path:'/escape-dir/bundle.zip',paths:['/data/file.txt']},'invalid_path')
  const targetVersion=(await(await session.call('files.text',{parameters:node,query:{path:'/data/file.txt'}})).json()).version
  for(const expectedVersion of ['',targetVersion])await failed('Saving text through a symlink',{action:'save_text',path:'/escape-link',content:'overwritten through link',expectedVersion},'operation_rejected')
  await failed('Saving text through an inner symlink',{action:'save_text',path:'/inner-link',content:'overwritten through link',expectedVersion:targetVersion},'operation_rejected')
  await failed('Copying a symlink',{action:'copy',path:'/escape-link',newPath:'/data/copied-link.txt'},'invalid_path');assert.ok(!fs.existsSync(path.join(files,'data','copied-link.txt')))
  await failed('Archiving a symlink',{action:'archive',path:'/data/link.zip',paths:['/escape-link']},'invalid_path');assert.ok(!fs.existsSync(path.join(files,'data','link.zip')))
  await refused('Deleting without confirmation',{action:'delete',path:'/escape-dir'},'file deletion requires exact target confirmation')
  await refused('Deleting with another confirmed path',{action:'delete',path:'/escape-dir',deleteConfirmed:true,confirmationPath:'/data'},'file deletion requires exact target confirmation')
  const unlinked=await run({action:'delete',path:'/escape-dir',deleteConfirmed:true,confirmationPath:'/escape-dir'});assert.equal(unlinked.status,'succeeded',JSON.stringify(unlinked.result));assert.throws(()=>fs.lstatSync(path.join(files,'escape-dir')),'Deleting a symlink removes the link');assert.deepEqual(outside(),untouched,'Deleting a symlinked directory must not delete through the link')
  assert.deepEqual(outside(),untouched);assert.equal(fs.readFileSync(path.join(files,'data','file.txt'),'utf8'),'inside\n');assert.ok(fs.lstatSync(path.join(files,'escape-link')).isSymbolicLink())

  const key=crypto.randomUUID(),first=await run({action:'mkdir',path:'/idempotent-a'},key);assert.equal(first.status,'succeeded',JSON.stringify(first.result))
  const replay=await submit({action:'mkdir',path:'/idempotent-a'},key);assert.equal(replay.status(),200,await replay.text());assert.equal((await replay.json()).taskId,first.taskId,'The same key and body return the original task')
  for(const [label,data] of [['another path',{action:'mkdir',path:'/idempotent-b'}],['another action',{action:'delete',path:'/idempotent-a',deleteConfirmed:true,confirmationPath:'/idempotent-a'}],['another source',{action:'copy',path:'/data/file.txt',newPath:'/idempotent-c'}]]){
   const conflict=await submit(data,key),body=await conflict.text();assert.equal(conflict.status(),409,`Reusing an idempotency key with ${label}: ${conflict.status()} ${body}`);assert.ok(body.includes('task conflicts with existing state'),body)
  }
  assert.ok(fs.statSync(path.join(files,'idempotent-a')).isDirectory());assert.ok(!fs.existsSync(path.join(files,'idempotent-b'))&&!fs.existsSync(path.join(files,'idempotent-c')),'A conflicting replay must not run')
  const tasks=(await(await session.call('tasks.list',{parameters:node})).json()).tasks.filter(task=>task.taskId===first.taskId);assert.equal(tasks.length,1,'A replayed key must not create another task')
  const missing=await session.call('files.action',{parameters:node,data:{action:'mkdir',path:'/idempotent-d'}});assert.equal(missing.status(),400);assert.ok((await missing.text()).includes('Idempotency-Key is required'));assert.ok(!fs.existsSync(path.join(files,'idempotent-d')))
  assert.deepEqual(outside(),untouched)
  console.log('file-safety-regressions passed.')
 }finally{
  if(agent)await agent.remove().catch(()=>{});if(session)await session.close().catch(()=>{})
  await core.stop();fs.rmSync(core.directory,{recursive:true,force:true});spawnSync('sudo',['rm','-rf',outer])
 }
}
