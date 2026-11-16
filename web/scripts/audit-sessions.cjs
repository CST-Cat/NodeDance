const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path')
const {spawnSync}=require('node:child_process'),{request}=require('playwright')
const {root,binary,startCore,signIn,sqlite}=require('./core-fixture.cjs')
;(async()=>{
 let core=await startCore();const scratch=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-sessions-')),opened=[]
 const open=async(credentials)=>{const session=await signIn(core.base,credentials);opened.push(session);return session}
 const status=async session=>(await session.api.get(core.base+'/api/v1/auth/me')).status()
 try{
  const initialPath=path.join(core.directory,'initial-admin.json')
  assert.equal(fs.statSync(initialPath).mode&0o777,0o600,'Initial administrator credentials must be owner-only')
  assert.equal(fs.statSync(core.directory).mode&0o777,0o700,'Core data directory must be owner-only')
  const administrator=core.credentials()
  const anonymous=await request.newContext()
  const setupStatus=await(await anonymous.get(core.base+'/api/v1/auth/setup/status')).json();assert.equal(setupStatus.initialized,true)
  const guestToken=(await(await anonymous.get(core.base+'/api/v1/auth/csrf')).json()).token
  const usersBefore=sqlite(core.database,'SELECT id,username,role FROM users')
  for(const [label,body,headers] of [
   ['forged credential',{credential:'f'.repeat(64),username:'intruder',password:'intruder-password-long'},{Origin:core.base,'X-CSRF-Token':guestToken}],
   ['short credential',{credential:'short',username:'intruder',password:'intruder-password-long'},{Origin:core.base,'X-CSRF-Token':guestToken}],
   ['oversized credential',{credential:'f'.repeat(300),username:'intruder',password:'intruder-password-long'},{Origin:core.base,'X-CSRF-Token':guestToken}],
   ['invalid display name',{credential:'f'.repeat(64),displayName:'\u0000',username:'intruder',password:'intruder-password-long'},{Origin:core.base,'X-CSRF-Token':guestToken}],
   ['cross origin',{credential:'f'.repeat(64),username:'intruder',password:'intruder-password-long'},{Origin:'https://attacker.invalid','X-CSRF-Token':guestToken}],
   ['missing token',{credential:'f'.repeat(64),username:'intruder',password:'intruder-password-long'},{Origin:core.base}],
  ]){
   const response=await anonymous.post(core.base+'/api/v1/auth/setup',{headers,data:body})
   assert.ok([400,401,403,409].includes(response.status()),`${label}: setup answered ${response.status()}`)
   assert.equal(response.headers()['set-cookie']?.includes('nodedance_session')??false,false,`${label}: setup issued a session`)
  }
  assert.deepEqual(sqlite(core.database,'SELECT id,username,role FROM users'),usersBefore,'Rejected setup requests must not change accounts')
  assert.ok(fs.existsSync(initialPath),'Rejected setup requests must keep the pending first-login credentials')
  const intruder=await anonymous.post(core.base+'/api/v1/auth/login',{headers:{Origin:core.base,'X-CSRF-Token':guestToken},data:{username:'intruder',password:'intruder-password-long'}});assert.equal(intruder.status(),401)
  assert.ok(fs.existsSync(initialPath),'A rejected login must not consume the first-login credentials')
  const first=await open(administrator)
  assert.ok(!fs.existsSync(initialPath),'First successful login removes the initial credential file')
  const afterLogin=await anonymous.post(core.base+'/api/v1/auth/setup',{headers:{Origin:core.base,'X-CSRF-Token':guestToken},data:{credential:'f'.repeat(64),username:'intruder',password:'intruder-password-long'}});assert.ok([401,409].includes(afterLogin.status()))
  await anonymous.dispose()

  const memberPassword='member-password-long-1',created=await first.call('accounts.create',{data:{username:'second-admin',displayName:'Second administrator',password:memberPassword,role:'administrator'}});assert.ok(created.ok(),await created.text())
  const viewerPassword='viewer-password-long-1',viewerCreated=await first.call('accounts.create',{data:{username:'reader',displayName:'Reader',password:viewerPassword,role:'viewer'}});assert.ok(viewerCreated.ok(),await viewerCreated.text())
  const second=await open({username:'second-admin',password:memberPassword}),secondOther=await open({username:'second-admin',password:memberPassword}),reader=await open({username:'reader',password:viewerPassword}),firstOther=await open(administrator)
  for(const session of [first,firstOther,second,secondOther,reader])assert.equal(await status(session),200)

  const wrongCurrent=await first.call('auth.password',{data:{currentPassword:'not-the-current-password',newPassword:'replacement-password-long-1'}});assert.ok([400,401,403].includes(wrongCurrent.status()),`Wrong current password answered ${wrongCurrent.status()}`)
  const weak=await first.call('auth.password',{data:{currentPassword:administrator.password,newPassword:'short'}});assert.equal(weak.status(),400)
  for(const session of [first,firstOther,second,secondOther,reader])assert.equal(await status(session),200,'Rejected password changes must not revoke any session')
  const replacement='replacement-password-long-1',changed=await first.call('auth.password',{data:{currentPassword:administrator.password,newPassword:replacement}});assert.equal(changed.status(),204,await changed.text())
  assert.equal(await status(first),401,'Changing a password revokes the session that changed it')
  assert.equal(await status(firstOther),401,'Changing a password revokes the other sessions of the same account')
  for(const session of [second,secondOther,reader])assert.equal(await status(session),200,'Changing a password must not revoke sessions of other accounts')
  const stale=await request.newContext(),staleToken=(await(await stale.get(core.base+'/api/v1/auth/csrf')).json()).token
  assert.equal((await stale.post(core.base+'/api/v1/auth/login',{headers:{Origin:core.base,'X-CSRF-Token':staleToken},data:administrator})).status(),401,'The replaced password must stop working');await stale.dispose()
  const renewed=await open({username:administrator.username,password:replacement});assert.equal(await status(renewed),200)

  const readerPassword=await reader.call('auth.password',{data:{currentPassword:viewerPassword,newPassword:'viewer-password-long-2'}});assert.equal(readerPassword.status(),204)
  assert.equal(await status(reader),401);for(const session of [second,secondOther,renewed])assert.equal(await status(session),200,'A viewer password change must not revoke administrator sessions')

  const sessionsBefore=(await(await second.call('sessions.list')).json()).sessions;assert.equal(sessionsBefore.length,2);assert.equal(sessionsBefore.filter(item=>item.current).length,1)
  const cookiesBefore=await second.api.storageState()
  const badSwitch=await second.call('auth.switch',{data:{username:'second-admin',password:'wrong-password-value'}});assert.equal(badSwitch.status(),401)
  const badTarget=await second.call('auth.switch',{data:{username:administrator.username,password:memberPassword}});assert.equal(badTarget.status(),401,'Switching accounts requires the target account password')
  assert.equal(await status(second),200,'A failed sign-in keeps the existing session');assert.deepEqual((await(await second.call('sessions.list')).json()).sessions.map(item=>item.id).sort(),sessionsBefore.map(item=>item.id).sort(),'A failed sign-in must not rotate sessions')
  const loginAgain=await second.api.post(core.base+'/api/v1/auth/login',{headers:second.headers,data:{username:'second-admin',password:'wrong-password-value'}});assert.equal(loginAgain.status(),401);assert.equal(await status(second),200)
  const goodSwitch=await second.call('auth.switch',{data:{username:'second-admin',password:memberPassword}});assert.equal(goodSwitch.status(),200,await goodSwitch.text());second.headers['X-CSRF-Token']=(await goodSwitch.json()).csrfToken
  assert.equal(await status(second),200)
  const replayed=await request.newContext({storageState:cookiesBefore});assert.equal((await replayed.get(core.base+'/api/v1/auth/me')).status(),401,'The session replaced by a verified sign-in must stop working');await replayed.dispose()
  const sessionsAfter=(await(await second.call('sessions.list')).json()).sessions;assert.equal(sessionsAfter.length,2);assert.ok(!sessionsAfter.some(item=>item.id===sessionsBefore.find(previous=>previous.current).id))
  assert.equal(await status(secondOther),200,'Rotating one session keeps the other sessions of the account')

  const remote=sessionsAfter.find(item=>!item.current),foreign=(await(await renewed.call('sessions.list')).json()).sessions[0]
  const crossRevoke=await second.call('sessions.revoke',{parameters:{id:foreign.id}});assert.equal(crossRevoke.status(),404,'An account cannot revoke the session of another account');assert.equal(await status(renewed),200)
  const revoke=await second.call('sessions.revoke',{parameters:{id:remote.id}});assert.ok(revoke.ok(),await revoke.text());assert.equal(await status(secondOther),401);assert.equal(await status(second),200)

  const stateDir=path.join(scratch,'cli-state'),cli=(args,input)=>spawnSync(binary,[...args,'--server',core.base,'--dev','--state-dir',stateDir],{cwd:root,input:input?JSON.stringify(input):undefined,encoding:'utf8',timeout:30000,env:{...process.env,NODEDANCE_CLIENT_STATE_DIR:''}})
  const cliLogin=cli(['auth','login','--body','-'],{username:'second-admin',password:memberPassword});assert.equal(cliLogin.status,0,cliLogin.stderr);assert.equal(JSON.parse(cliLogin.stdout).status,200)
  assert.ok(!cliLogin.stdout.includes(memberPassword)&&!cliLogin.stderr.includes(memberPassword),'CLI must not echo the password')
  assert.equal(fs.statSync(stateDir).mode&0o777,0o700,'CLI state directory must be owner-only')
  const sessionFile=path.join(stateDir,'session.json');assert.equal(fs.statSync(sessionFile).mode&0o777,0o600,'CLI session file must be owner-only');assert.ok(fs.lstatSync(sessionFile).isFile())
  assert.ok(!fs.readFileSync(sessionFile,'utf8').includes(memberPassword),'CLI session file must not store the password')
  for(const name of fs.readdirSync(stateDir))assert.equal(fs.statSync(path.join(stateDir,name)).mode&0o077,0,`CLI state file ${name} must be owner-only`)
  const me=cli(['call','auth.me']);assert.equal(me.status,0,me.stderr);assert.equal(JSON.parse(me.stdout).data.user.username,'second-admin')
  const cliSessions=(await(await second.call('sessions.list')).json()).sessions;assert.equal(cliSessions.length,2,'CLI login is a regular revocable session')
  const savedSession=fs.readFileSync(sessionFile)
  fs.chmodSync(sessionFile,0o644);const exposed=cli(['call','auth.me']);assert.notEqual(exposed.status,0,'CLI must refuse a session file readable by other users');fs.chmodSync(sessionFile,0o600)
  fs.renameSync(sessionFile,sessionFile+'.real');fs.symlinkSync(sessionFile+'.real',sessionFile);const linked=cli(['call','auth.me']);assert.notEqual(linked.status,0,'CLI must refuse a symlinked session file');fs.unlinkSync(sessionFile);fs.renameSync(sessionFile+'.real',sessionFile)
  assert.equal(cli(['call','auth.me']).status,0)
  const other=fs.mkdtempSync(path.join(scratch,'cli-other-'));const copy=spawnSync(binary,['call','auth.me','--server','http://127.0.0.1:1','--dev','--state-dir',stateDir],{cwd:root,encoding:'utf8',timeout:30000});assert.notEqual(copy.status,0,'A saved session must not be sent to a different server');assert.match(copy.stderr,/different server/)
  const cliLogout=cli(['auth','logout']);assert.equal(cliLogout.status,0,cliLogout.stderr)
  const afterLogout=cli(['call','auth.me']);assert.ok(afterLogout.status!==0||JSON.parse(afterLogout.stdout).status===401,'CLI session must not work after logout')
  fs.writeFileSync(sessionFile,savedSession,{mode:0o600});fs.chmodSync(sessionFile,0o600)
  const resurrected=cli(['call','auth.me']);assert.ok(resurrected.status!==0||JSON.parse(resurrected.stdout).status===401,'A copied CLI session file must not work after logout')
  assert.equal((await(await second.call('sessions.list')).json()).sessions.length,1,'CLI logout revokes the session on the Core')

  const signedOut=await second.call('auth.logout',{data:{}});assert.ok(signedOut.ok(),await signedOut.text());assert.equal(await status(second),401);assert.equal(await status(renewed),200)
  await core.stop();core=await startCore({directory:core.directory})
  assert.ok(!fs.existsSync(initialPath),'Restart must not regenerate first-login credentials');assert.equal(sqlite(core.database,'SELECT count(*) FROM users')[0][0],3)
  assert.equal(await status(renewed),200,'Sessions survive a Core restart');assert.equal(await status(second),401,'Revoked sessions stay revoked after restart');assert.equal(await status(first),401)
  console.log('audit-sessions passed.')
 }finally{for(const session of opened)await session.close().catch(()=>{});await core.stop();fs.rmSync(core.directory,{recursive:true,force:true});fs.rmSync(scratch,{recursive:true,force:true})}
})().catch(error=>{console.error(error);process.exit(1)})
