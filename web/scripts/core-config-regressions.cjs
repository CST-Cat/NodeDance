const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path')
const {spawnSync}=require('node:child_process')
const {binary,root,freePort}=require('./core-fixture.cjs')
module.exports=async function coreConfig(){
 const scratch=fs.mkdtempSync(path.join(os.tmpdir(),'nodedance-core-config-')),listen=`127.0.0.1:${await freePort()}`
 try{
  let index=0
  const refused=(label,{config,args=[],dev=false,pattern})=>{
   const dataDir=path.join(scratch,'data-'+(++index)),configFile=path.join(scratch,`config-${index}.json`)
   const flags=['serve','--data-dir',dataDir]
   if(config!==undefined){fs.writeFileSync(configFile,config);flags.push('--config',configFile)}else flags.push('--config',path.join(scratch,'absent.json'))
   if(!args.includes('--listen'))flags.push('--listen',listen)
   if(dev)flags.push('--dev')
   const result=spawnSync(binary,[...flags,...args],{cwd:root,encoding:'utf8',timeout:20000,env:{...process.env,NODEDANCE_CONFIG:'',NODEDANCE_LISTEN:'',NODEDANCE_DATA_DIR:'',NODEDANCE_PUBLIC_ORIGIN:'',NODEDANCE_TRUSTED_PROXIES:'',NODEDANCE_MAX_FILE_BYTES:'',NODEDANCE_HISTORY_RETENTION_DAYS:''}})
   assert.equal(result.error,undefined,`${label}: Core kept running with a rejected configuration`)
   assert.notEqual(result.status,0,`${label}: Core accepted the configuration`)
   assert.match(result.stderr+result.stdout,pattern,`${label}: ${result.stderr}`)
   assert.ok(!(result.stdout+result.stderr).includes('listening on'),`${label}: Core started listening`)
   assert.ok(!fs.existsSync(path.join(dataDir,'nodedance.sqlite')),`${label}: Core initialized storage before rejecting the configuration`)
  }
  refused('trailing JSON value',{config:`{"listen":"${listen}"} {"listen":"${listen}"}`,dev:true,pattern:/exactly one JSON value/})
  refused('trailing garbage',{config:`{"listen":"${listen}"} trailing`,dev:true,pattern:/exactly one JSON value|parse config/})
  refused('unknown field',{config:JSON.stringify({listen,administrator:'forged'}),dev:true,pattern:/parse config/})
  refused('array document',{config:'[]',dev:true,pattern:/parse config/})
  refused('hostname listen in file',{config:JSON.stringify({listen:'localhost:8180'}),dev:true,pattern:/must be an IP address/})
  refused('origin with path in file',{config:JSON.stringify({public_origin:'https://core.example/admin'}),dev:true,pattern:/only scheme and authority/})
  refused('origin with credentials in file',{config:JSON.stringify({public_origin:'https://user:secret@core.example'}),dev:true,pattern:/only scheme and authority/})
  refused('origin with query',{args:['--public-origin','https://core.example?next=1'],dev:true,pattern:/only scheme and authority/})
  refused('plain HTTP origin outside development',{args:['--public-origin','http://core.example'],pattern:/https outside --dev/})
  refused('origin without scheme',{args:['--public-origin','core.example'],dev:true,pattern:/only scheme and authority/})
  refused('origin with invalid port',{args:['--public-origin','https://core.example:70000'],dev:true,pattern:/invalid port|only scheme and authority/})
  refused('invalid trusted proxy',{args:['--trusted-proxies','203.0.113.0/24,not-a-network'],dev:true,pattern:/trusted|prox/i})
  refused('invalid trusted proxy in file',{config:JSON.stringify({trusted_proxies:['203.0.113.0/40']}),dev:true,pattern:/trusted|prox/i})
  refused('zero retention days',{args:['--history-retention-days','0'],dev:true,pattern:/between 1 and 3650/})
  refused('excess retention days',{args:['--history-retention-days','3651'],dev:true,pattern:/between 1 and 3650/})
  refused('non-numeric retention days',{args:['--history-retention-days','forever'],dev:true,pattern:/retention/i})
  refused('retention days in file',{config:JSON.stringify({non_metric_history_retention_days:-1}),dev:true,pattern:/between 1 and 3650/})
  refused('development mode off loopback',{args:['--listen','0.0.0.0:'+listen.split(':')[1]],dev:true,pattern:/loopback/})
  refused('hostname listen flag',{args:['--listen','localhost:'+listen.split(':')[1]],dev:true,pattern:/must be an IP address/})
  refused('listen without host',{args:['--listen',':'+listen.split(':')[1]],dev:true,pattern:/must name an IP address/})
  refused('listen port out of range',{args:['--listen','127.0.0.1:70000'],dev:true,pattern:/between 1 and 65535/})
  refused('TLS certificate without key',{args:['--tls-cert',path.join(scratch,'missing.pem')],dev:true,pattern:/provided together/})
  refused('unexpected positional argument',{args:['extra'],dev:true,pattern:/unexpected arguments/})
  refused('negative transfer limit',{args:['--max-file-bytes','-1'],dev:true,pattern:/file|bytes/i})
  console.log('core-config-regressions passed.')
 }finally{fs.rmSync(scratch,{recursive:true,force:true})}
}
