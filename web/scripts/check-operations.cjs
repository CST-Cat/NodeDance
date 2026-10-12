const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')
const ts = require('typescript')
const source = path.resolve(__dirname, '../src')
const crypto = require('node:crypto').webcrypto
const operation = {name:'future.inspect', description:'New server operation', method:'GET', path:'/api/v1/future/{id}', parameters:['id'], query:['filter'], transport:'json'}
const moduleObject = {exports:{}}
let calls=0
const code=ts.transpileModule(fs.readFileSync(path.join(source,'operations.ts'),'utf8'),{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
vm.runInNewContext(code,{module:moduleObject,exports:moduleObject.exports, URL, URLSearchParams, TextEncoder, Headers, Blob, File, FormData, crypto, window:{location:{origin:'https://core.example'}}, fetch:async url=>{calls++;assert.equal(url,'/api/v1/operations');return {ok:true,json:async()=>({version:1,operations:[operation]})}}})
;(async()=>{
 const api=moduleObject.exports
 const result=await api.resolveOperation('future.inspect',{params:{id:'node'},query:{filter:'a b'}})
 assert.equal(result.path,'/api/v1/future/node?filter=a+b')
 assert.equal((await api.operationURL('future.inspect',{params:{id:'node'}})).href,'https://core.example/api/v1/future/node')
 assert.equal(calls,1,'Catalog should be shared by every request')
 for(const id of ['..','../escape','a/b','a\\b','a\0b']) assert.throws(()=>api.operationPath(operation,{params:{id}}))
 assert.throws(()=>api.operationPath(operation,{params:{id:'node'},query:{unknown:'value'}}))
 assert.throws(()=>api.operationPath(operation,{params:{id:'node',unknown:'value'}}))
 await assert.rejects(api.resolveOperation('missing'))
 const preference={...operation,bodySchema:{type:'object',required:['identity'],additionalProperties:false,properties:{identity:{type:'string'},alias:{type:'string'},enabled:{type:'boolean'},credentials:{type:'object',properties:{password:{type:'string',writeOnly:true}}}}}}
 assert.equal(api.operationBody(preference,{identity:'node:x',alias:'',enabled:'false'}),' {"identity":"node:x","alias":"","enabled":false}'.trim())
 assert.throws(()=>api.operationBody(preference,{identity:'node:x',enabled:'not-json'}))
 assert.ok(api.schemaHasSecrets(preference.bodySchema.properties.credentials))
 assert.throws(()=>api.validateSchema({type:'string',maxBytes:2},'你好'))
 assert.throws(()=>api.validateSchema({type:'integer',minimum:1},0))
 assert.doesNotThrow(()=>api.validateSchema({type:'object',required:['alias'],properties:{alias:{type:'string'}}},{alias:''}))
 assert.throws(()=>api.validateSchema({type:'object',required:['alias']},{}))
 assert.equal(api.operationBody({...operation,bodySchema:{type:'array',items:{type:'integer'}}},{},'[1,2]'),'[1,2]')
 const large={...operation,bodySchema:{type:'object',properties:{bytes:{type:'integer'}}}}
 assert.equal(api.operationBody(large,{bytes:22}),'{"bytes":22}')
 assert.equal(api.operationBody(large,{bytes:'18446744073709551615'}),'{"bytes":18446744073709551615}')
 assert.equal(api.operationBody(large,{},'{"bytes":18446744073709551615}',true),'{"bytes":18446744073709551615}')
 const upload={...operation,transport:'multipart',upload:{field:'attachment',contentType:'multipart/form-data',maxBytes:10}}
 const file=new File(['hello'],'file.txt')
 const form=api.operationUpload(upload,file)
 assert.ok(form.has('attachment'));assert.ok(!form.has('image'));assert.throws(()=>api.operationUpload({...upload,upload:{...upload.upload,maxBytes:2}},file))
 const complete={...operation,completion:{operation:'future.read',params:{id:'data.id',node:'params.node'},query:{component:'body.component'},statusPath:'job.status',terminalStatuses:['done'],successStatuses:['done']}}
 const next=api.completionInput(complete,{params:{node:'n'}},'{"component":"core"}',{id:'task'})
 assert.equal(next.name,'future.read');assert.equal(next.input.params.id,'task');assert.equal(next.input.query.component,'core')
 assert.equal(api.completionStatus(complete,{job:{status:'done'}}),'done')
 const intentModule={exports:{}}
 const intentCode=ts.transpileModule(fs.readFileSync(path.join(source,'operation-intent.ts'),'utf8'),{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
 vm.runInNewContext(intentCode,{module:intentModule,exports:intentModule.exports,crypto,TextEncoder,Blob,File,FormData,Headers})
 const intents=intentModule.exports
 const first=await intents.requestIntent('write',{params:{id:'n'}},'{"a":1,"b":2}')
 const reordered=await intents.requestIntent('write',{params:{id:'n'}},'{ "b":2,"a":1 }')
 assert.equal(first,reordered)
 const key=intents.intentKey(first);assert.equal(key,intents.intentKey(reordered));assert.notEqual(key,intents.intentKey(first,true))
 const selectedFile=await intents.requestIntent('upload',{},file)
 assert.equal(selectedFile,await intents.requestIntent('upload',{},file))
 assert.notEqual(selectedFile,await intents.requestIntent('upload',{},new File(['changed'],'file.txt')))
 console.log('Web discovery, schema presence, secrets, upload metadata, completion and retry intent passed')
})().catch(error=>{console.error(error);process.exit(1)})
