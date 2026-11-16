const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http')
const {execFileSync}=require('node:child_process')
const {startCore,signIn,sqlite,directoryContains,eventually}=require('./core-fixture.cjs')
module.exports=async function secretStorage(){
 const hits=[],receiver=http.createServer((request,response)=>{let body='';request.on('data',chunk=>body+=chunk);request.on('end',()=>{hits.push({authorization:request.headers.authorization,header:request.headers['x-private-header'],body});response.end('ok')})})
 await new Promise(resolve=>receiver.listen(0,'127.0.0.1',resolve))
 const secrets={bearer:'bearer-'+'plaintext-webhook-credential',header:'header-'+'plaintext-custom-value',smtp:'smtp-'+'plaintext-mail-password'}
 const scratch=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-secret-storage-')),keyFile=path.join(scratch,'key.pem'),certificateFile=path.join(scratch,'certificate.pem')
 execFileSync('openssl',['req','-x509','-newkey','ec','-pkeyopt','ec_paramgen_curve:P-256','-nodes','-subj','/CN=sealed.example','-addext','subjectAltName=DNS:sealed.example','-addext','basicConstraints=critical,CA:FALSE','-days','2','-keyout',keyFile,'-out',certificateFile],{stdio:'ignore'})
 const privateKey=fs.readFileSync(keyFile,'utf8'),certificate=fs.readFileSync(certificateFile,'utf8'),keyLines=privateKey.trim().split('\n').slice(1,-1)
 assert.ok(keyLines.length>0&&keyLines[0].length>=32)
 let core=await startCore(),session
 try{
  const credentials=core.credentials();session=await signIn(core.base,credentials)
  const webhook=await session.call('channels.create',{data:{name:'Sealed webhook',kind:'webhook',config:{webhookUrl:`http://127.0.0.1:${receiver.address().port}/sealed`,method:'POST'},secret:secrets.bearer,headers:{'X-Private-Header':secrets.header},enabled:true}})
  assert.equal(webhook.status(),200,await webhook.text());const channel=(await webhook.json()).channel
  assert.equal(channel.hasSecret,true);assert.equal(channel.hasHeaders,true);assert.ok(!JSON.stringify(channel).includes(secrets.bearer)&&!JSON.stringify(channel).includes(secrets.header))
  const mail=await session.call('channels.create',{data:{name:'Sealed mail',kind:'smtp',config:{smtpHost:'127.0.0.1',smtpPort:2525,smtpFrom:'sender@example.org',smtpTo:'receiver@example.org',smtpSecurityMode:'starttls',smtpUsername:'mailer'},secret:secrets.smtp,enabled:false}})
  assert.equal(mail.status(),200,await mail.text());assert.ok(!(await mail.text()).includes(secrets.smtp))
  const imported=await session.call('certificates.import',{data:{name:'Sealed certificate',certificatePem:certificate,privateKeyPem:privateKey,domainIds:[],channelIds:[]}})
  assert.equal(imported.status(),201,await imported.text());const managed=await imported.json()
  const listed=await(await session.call('channels.list')).text();for(const value of Object.values(secrets))assert.ok(!listed.includes(value),'Channel list must not return stored credentials')
  const certificates=await(await session.call('certificates.list')).text();assert.ok(!certificates.includes(keyLines[0]),'Certificate list must not return the private key')
  const sealed=()=>{
   assert.ok(directoryContains(core.directory,'Sealed webhook').length>0,'Storage scan must reach live Core records')
   for(const [name,value] of Object.entries(secrets))assert.deepEqual(directoryContains(core.directory,value),[],`Plaintext ${name} credential reached the Core data directory`)
   for(const line of keyLines)assert.deepEqual(directoryContains(core.directory,line),[],'Plaintext certificate private key reached the Core data directory')
   assert.deepEqual(directoryContains(core.directory,'BEGIN EC PRIVATE KEY'),[]);assert.deepEqual(directoryContains(core.directory,'BEGIN PRIVATE KEY'),[])
   const channelRows=sqlite(core.database,'SELECT name,config_json,secret_ciphertext FROM alert_channels ORDER BY name')
   assert.equal(channelRows.length,2);for(const row of channelRows){assert.ok(row[2]&&row[2].length>0,`${row[0]} keeps a sealed credential`);for(const value of Object.values(secrets)){assert.ok(!row[1].includes(value));assert.ok(!Buffer.from(row[2],'hex').includes(Buffer.from(value)))}}
   const headerRows=sqlite(core.database,'SELECT ciphertext FROM alert_channel_credentials');assert.ok(headerRows.length>0);for(const row of headerRows)for(const value of Object.values(secrets))assert.ok(!Buffer.from(row[0],'hex').includes(Buffer.from(value)))
   const certificateRows=sqlite(core.database,'SELECT key_pem FROM managed_certificates');assert.equal(certificateRows.length,1);const storedKey=Buffer.from(certificateRows[0][0],'hex');assert.ok(storedKey.length>0);assert.ok(!storedKey.includes(Buffer.from('PRIVATE KEY')),'Stored certificate key must be sealed')
   for(const name of ['alert-channel-encryption.key','certificate-encryption.key','csrf-signing.key','nodedance.sqlite'])assert.equal(fs.statSync(path.join(core.directory,name)).mode&0o077,0,`${name} must stay owner-only`)
   assert.equal(fs.statSync(core.directory).mode&0o077,0,'Core data directory must stay owner-only')
  }
  sealed()
  const delivered=async()=>{
   hits.length=0;const tested=await session.call('channels.test',{parameters:{id:channel.id},data:{}});assert.ok(tested.ok(),await tested.text())
   await eventually(()=>hits.length>0,'Sealed webhook credential did not reach the local receiver')
   assert.equal(hits[0].authorization,'Bearer '+secrets.bearer);assert.equal(hits[0].header,secrets.header)
  }
  await delivered()
  const exported=async()=>{
   const pem=await session.call('certificates.pem',{parameters:{id:managed.id}});assert.equal(pem.status(),200);assert.ok(!(await pem.text()).includes(keyLines[0]),'Public certificate download must not contain the private key')
   const archive=await session.call('certificates.export',{parameters:{id:managed.id},data:{confirm:true,revision:managed.revision}});assert.equal(archive.status(),200,await archive.text())
   const file=path.join(scratch,'export.zip');fs.writeFileSync(file,await archive.body());const names=execFileSync('python3',['-c','import sys,zipfile\nz=zipfile.ZipFile(sys.argv[1])\nprint("\\n".join(n for n in z.namelist() if z.read(n).decode("utf8","replace").strip()==open(sys.argv[2]).read().strip()))',file,keyFile],{encoding:'utf8'}).trim()
   assert.ok(names.length>0,'Private export must return the original private key')
  }
  await exported()
  await session.close();await core.stop()
  core=await startCore({directory:core.directory});session=await signIn(core.base,credentials)
  sealed();await delivered();await exported()
  const dump=execFileSync('sqlite3',[core.database,'.dump'],{encoding:'latin1',maxBuffer:256*1024*1024})
  for(const value of [...Object.values(secrets),keyLines[0]])assert.ok(!dump.includes(value),'SQL dump must not expose stored secrets')
  console.log('secret-storage-regressions passed.')
 }finally{
  receiver.close();if(session)await session.close().catch(()=>{});await core.stop();fs.rmSync(core.directory,{recursive:true,force:true});fs.rmSync(scratch,{recursive:true,force:true})
 }
}
