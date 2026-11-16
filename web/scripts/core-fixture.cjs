const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),net=require('node:net')
const {spawn,spawnSync}=require('node:child_process'),{request}=require('playwright')
const root=path.resolve(__dirname,'../..'),binary=path.join(root,'.build/nodedance'),agentBinary=path.join(root,'.build/nodedance-agent')
const sleep=milliseconds=>new Promise(resolve=>setTimeout(resolve,milliseconds))
async function freePort(){const listener=net.createServer();await new Promise(resolve=>listener.listen(0,'127.0.0.1',resolve));const port=listener.address().port;await new Promise(resolve=>listener.close(resolve));return port}
async function stop(child){if(!child||child.exitCode!==null||child.signalCode!==null)return;child.kill('SIGTERM');await Promise.race([new Promise(resolve=>child.once('exit',resolve)),sleep(8000)]);if(child.exitCode===null&&child.signalCode===null){child.kill('SIGKILL');await new Promise(resolve=>child.once('exit',resolve))}}
async function eventually(check,message,attempts=200,interval=50){let last;for(let attempt=0;attempt<attempts;attempt++){try{const value=await check();if(value)return value}catch(error){last=error}await sleep(interval)}throw new Error(message+(last?': '+last.message:''))}
async function startCore({directory,port,args=[],env={}}={}){
 const dataDir=directory??fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-e2e-core-')),base=`http://127.0.0.1:${port??await freePort()}`
 const child=spawn(binary,['serve','--dev','--listen',new URL(base).host,'--data-dir',dataDir,...args],{cwd:root,env:{...process.env,...env},stdio:['ignore','pipe','pipe']})
 const core={base,directory:dataDir,port:Number(new URL(base).port),process:child,logs:'',database:path.join(dataDir,'nodedance.sqlite')}
 child.stdout.on('data',data=>core.logs+=data);child.stderr.on('data',data=>core.logs+=data)
 core.stop=()=>stop(child)
 core.credentials=()=>JSON.parse(fs.readFileSync(path.join(dataDir,'initial-admin.json'),'utf8'))
 let ready=false
 for(let attempt=0;attempt<200&&!ready;attempt++){
  if(child.exitCode!==null)throw new Error('Isolated Core exited: '+core.logs)
  try{ready=(await fetch(base+'/api/v1/health')).ok}catch{}
  if(!ready)await sleep(100)
 }
 if(!ready){await stop(child);throw new Error('Isolated Core failed to start: '+core.logs)}
 return core
}
function runCore(args,{env={},input}={}){return spawnSync(binary,args,{cwd:root,env:{...process.env,...env},input,encoding:'utf8',timeout:60000})}
async function signIn(base,credentials,extraHeaders={}){
 const api=await request.newContext({extraHTTPHeaders:extraHeaders})
 const csrf=await(await api.get(base+'/api/v1/auth/csrf')).json()
 const login=await api.post(base+'/api/v1/auth/login',{headers:{Origin:base,'X-CSRF-Token':csrf.token},data:{username:credentials.username,password:credentials.password}})
 assert.equal(login.status(),200,await login.text())
 const body=await login.json(),session={api,base,user:body.user,headers:{Origin:base,'X-CSRF-Token':body.csrfToken}}
 session.catalog=await(await api.get(base+'/api/v1/operations')).json()
 session.operation=name=>{const value=session.catalog.operations.find(item=>item.name===name);assert.ok(value,`Missing operation ${name}`);return value}
 session.url=(name,parameters={},query={})=>{const search=new URLSearchParams(query).toString();return base+session.operation(name).path.replace(/\{([^}]+)\}/g,(_,key)=>{assert.ok(parameters[key]!==undefined,`Missing ${key} for ${name}`);return encodeURIComponent(parameters[key])})+(search?'?'+search:'')}
 session.call=(name,{parameters={},query={},data,headers={}}={})=>api.fetch(session.url(name,parameters,query),{method:session.operation(name).method,headers:{...session.headers,...headers},data})
 session.refresh=async()=>{session.headers['X-CSRF-Token']=(await(await api.get(base+'/api/v1/auth/csrf')).json()).token}
 session.close=()=>api.dispose()
 return session
}
function sqlite(database,statement,parameters=[]){
 const result=spawnSync('python3',['-c',`import sqlite3,sys,json
c=sqlite3.connect(sys.argv[1],timeout=30)
r=c.execute(sys.argv[2],json.loads(sys.argv[3]))
rows=[[v.hex() if isinstance(v,(bytes,bytearray)) else v for v in row] for row in r.fetchall()]
c.commit();c.close()
print(json.dumps(rows))`,database,statement,JSON.stringify(parameters)],{encoding:'utf8'})
 assert.equal(result.status,0,result.stderr)
 return JSON.parse(result.stdout)
}
function fileContains(file,needle){
 const data=fs.readFileSync(file)
 return data.includes(Buffer.from(needle,'utf8'))
}
function directoryContains(directory,needle){
 const found=[]
 for(const entry of fs.readdirSync(directory,{withFileTypes:true,recursive:true})){
  if(!entry.isFile())continue
  const file=path.join(entry.parentPath??entry.path,entry.name)
  if(fileContains(file,needle))found.push(path.relative(directory,file))
 }
 return found
}
module.exports={root,binary,agentBinary,sleep,freePort,stop,eventually,startCore,runCore,signIn,sqlite,fileContains,directoryContains}
