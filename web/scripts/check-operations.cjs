const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')
const ts = require('typescript')
const source = path.resolve(__dirname, '../src')
const operation = {name:'future.inspect', description:'New server operation', method:'GET', path:'/api/v1/future/{id}', parameters:['id'], query:['filter'], transport:'json'}
const moduleObject = {exports:{}}
let calls=0
const code=ts.transpileModule(fs.readFileSync(path.join(source,'operations.ts'),'utf8'),{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
vm.runInNewContext(code,{module:moduleObject,exports:moduleObject.exports, URL, URLSearchParams, window:{location:{origin:'https://core.example'}}, fetch:async url=>{calls++;assert.equal(url,'/api/v1/operations');return {ok:true,json:async()=>({version:1,operations:[operation]})}}})
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
 console.log('Web operation discovery and path validation passed')
})().catch(error=>{console.error(error);process.exit(1)})
