import { t } from './i18n'
import type { Driver, DirEntry, FileOperationResult } from 'vuefinder'
import { ApiError, operationRequest, type ContainerTask, type NodeFileEntry } from './api'
import { operationCatalog, operationPath, exactJSON } from './operations'
import { requestIntent, intentKey, finishIntent } from './operation-intent'

export interface FileObservation { taskId: string; intent: string; path: string; status: string }
export async function createFileManager(nodeId: string, changed: (items: FileObservation[]) => void) {
 const catalog=await operationCatalog(), controller=new AbortController(), versions=new Map<string,string>()
 const storage=`nodedance.file-observations.${nodeId}`
 let observations:FileObservation[]=[]
 try {const saved=JSON.parse(sessionStorage.getItem(storage)??'[]');if(Array.isArray(saved))observations=saved.filter(v=>v&&typeof v.taskId==='string'&&typeof v.intent==='string'&&typeof v.path==='string'&&typeof v.status==='string').slice(-100)}catch{}
 const persist=()=>{try{sessionStorage.setItem(storage,JSON.stringify(observations))}catch{};changed([...observations])}
 const active=()=>{controller.signal.throwIfAborted()}
 const signal=(other?:AbortSignal)=>other?AbortSignal.any([controller.signal,other]):controller.signal
 const params={nodeId}
 const read=<T>(name:string,query:Record<string,string>,other?:AbortSignal)=>{active();return operationRequest<T>(name,{params,query},{signal:signal(other)})}
 const url=(name:string,p:string)=>{active();const operation=catalog.operations.find(o=>o.name===name);if(!operation)throw Error(`Missing Core operation: ${name}`);return operationPath(operation,{params,query:{path:p}})}
 const virtual=(p:string)=>p.startsWith('node://')?p.slice(7)||'/':p
 const joined=(dir:string,name:string)=>virtual(dir).replace(/\/$/,'')+'/'+name
 function entry(value:NodeFileEntry):DirEntry {
  const extension=value.name.includes('.')?value.name.split('.').pop()!.toLowerCase():''
  return {dir:'node://'+(value.path.slice(0,value.path.lastIndexOf('/'))||'/'),basename:value.name,extension,path:'node://'+value.path,storage:'node',type:value.kind==='directory'?'dir':'file',file_size:value.size,last_modified:value.modifiedAt/1e9,mime_type:mime(extension),visibility:value.name.startsWith('.')?'hidden':'public',read_only:value.kind!=='file'&&value.kind!=='directory'}
 }
 async function pages(name:string,query:Record<string,string>,other?:AbortSignal){
  const result:DirEntry[]=[];let after=''
  do {const data=await read<{entries:NodeFileEntry[];nextAfter:string}>(name,{...query,...(after?{after}:{})},other);active();for(const item of data.entries){result.push(entry(item));if(item.version)versions.set(item.path,item.version)};if(data.nextAfter===after&&after)throw Error(t('files.invalidContinuation'));after=data.nextAfter;if(result.length>100000)throw Error(t('files.directoryLimit'))}while(after)
  return result
 }
 const list:Driver['list']=async(options={})=>{const path=virtual(options.path??'/');return {storages:['node'],dirname:'node://'+path,files:await pages('files.list',{path},options.signal),read_only:false}}
 const result=async(path:string):Promise<FileOperationResult>=>({...await list({path}),read_only:false})
 async function observe(item:FileObservation,other?:AbortSignal){
  for(;;){active();const task=await operationRequest<ContainerTask>('tasks.get',{params:{...params,taskId:item.taskId}},{signal:signal(other)});active();item.status=task.status;persist()
   if(['queued','running'].includes(task.status)){await new Promise<void>((resolve,reject)=>{const s=signal(other);const finish=()=>{s.removeEventListener('abort',abort);resolve()};const timer=setTimeout(finish,500);const abort=()=>{clearTimeout(timer);s.removeEventListener('abort',abort);reject(s.reason)};s.addEventListener('abort',abort,{once:true});if(s.aborted)abort()});continue}
   if(task.status==='unknown')throw Error(t('files.unknown'))
   finishIntent(item.intent);observations=observations.filter(v=>v!==item);persist()
   if(task.status!=='succeeded')throw Error(t('files.failed',{0:task.result?.code||task.status}))
   return task
  }
 }
 async function action(body:Record<string,unknown>,other?:AbortSignal){
  active();const payload=exactJSON(body),intent=await requestIntent('files.action',{params},payload);active()
  let item=observations.find(v=>v.intent===intent)
  if(!item){if(observations.length>=100)throw Error(t('files.observationLimit'));const accepted=await operationRequest<{taskId:string;status:string}>('files.action',{params},{headers:{'Idempotency-Key':intentKey(intent)},body:payload,signal:signal(other)});active();item={taskId:accepted.taskId,intent,path:String(body.path),status:accepted.status};observations.push(item);persist()}
  await observe(item,other)
 }
 async function ensureUploadDirectory(path:string,other?:AbortSignal):Promise<void>{
  if(path==='/')return
  try{const info=await read<NodeFileEntry>('files.stat',{path},other);if(info.kind!=='directory')throw Error(t('files.notDirectory'));return}catch(reason){if(!(reason instanceof ApiError)||reason.status!==404)throw reason}
  await ensureUploadDirectory(path.slice(0,path.lastIndexOf('/'))||'/',other)
  try{await action({action:'mkdir',path},other)}catch(reason){const info=await read<NodeFileEntry>('files.stat',{path},other);if(info.kind!=='directory')throw reason}
 }
 async function upload(path:string,file:File,expectedVersion='',other?:AbortSignal){
  active();const query={path:virtual(path)},headers={'Content-Type':'application/octet-stream','X-File-Version':expectedVersion}
  const intent=await requestIntent('files.upload',{params,query},exactJSON({name:file.name,size:file.size,modifiedAt:file.lastModified,type:file.type,expectedVersion}));active()
  const task=await operationRequest<ContainerTask>('files.upload',{params,query},{headers:{...headers,'Idempotency-Key':intentKey(intent)},body:file,signal:signal(other)},30*60_000);active()
  if(task.status==='succeeded'){finishIntent(intent);return task}
  const item={taskId:task.taskId,intent,path:query.path,status:task.status};observations.push(item);persist();return observe(item,other)
 }
 const driver:Driver={
  list,
  async delete(options){for(const item of options.items){const path=virtual(item.path);await action({action:'delete',path,deleteConfirmed:true,confirmationPath:path})};return result(virtual(options.path))},
  async rename(options){await action({action:'rename',path:virtual(options.item),newPath:joined(options.path,options.name)});return result(virtual(options.path))},
  async copy(options){for(const source of options.sources)await action({action:'copy',path:virtual(source),newPath:joined(options.destination,virtual(source).split('/').pop()!)});return result(virtual(options.path??options.destination))},
  async move(options){for(const source of options.sources)await action({action:'move',path:virtual(source),newPath:joined(options.destination,virtual(source).split('/').pop()!)});return result(virtual(options.path??options.destination))},
  async archive(options){await action({action:'archive',path:joined(options.destination??options.path,options.name),paths:options.items.map(item=>virtual(item.path))});return result(virtual(options.path))},
  async unarchive(options){await action({action:'extract',path:virtual(options.item),newPath:virtual(options.destination??options.path)});return result(virtual(options.path))},
  async createFile(options){await action({action:'save_text',path:joined(options.path,options.name),content:''});return result(virtual(options.path))},
  async createFolder(options){await action({action:'mkdir',path:joined(options.path,options.name)});return result(virtual(options.path))},
  async getContent(options){const path=virtual(options.path),data=await read<{text:string;version:string}>('files.text',{path},options.signal);active();versions.set(path,data.version);return {content:data.text,mimeType:mime(path.split('.').pop()??'')}},
  getPreviewUrl:options=>url('files.preview',virtual(options.path)),
  getDownloadUrl:options=>url('files.download',virtual(options.path)),
  async search(options){const sizes:Record<string,Record<string,string>>={small:{maxSize:String(1024*1024)},medium:{minSize:String(1024*1024),maxSize:String(100*1024*1024)},large:{minSize:String(100*1024*1024)}};return pages('files.search',{path:virtual(options.path??'/'),filter:options.filter,recursive:String(options.deep??false),...sizes[options.size??'all']},options.signal)},
  async save(options){const path=virtual(options.path);let version=versions.get(path);if(!version){const meta=await read<NodeFileEntry>('files.stat',{path},options.signal);version=meta.version;active()}
   if(/^data:image\/(png|jpeg|webp|gif);base64,/.test(options.content)){const [header,data]=options.content.split(',',2),bytes=Uint8Array.from(atob(data!),c=>c.charCodeAt(0));await upload(path,new File([bytes],path.split('/').pop()!,{type:header!.slice(5,header!.indexOf(';')),lastModified:0}),version,options.signal)}
   else await action({action:'save_text',path,content:options.content,expectedVersion:version??''},options.signal)
   const meta=await read<NodeFileEntry>('files.stat',{path},options.signal);active();if(meta.version)versions.set(path,meta.version);return options.content
  },
  configureUploader(uppy,context){
   const uploads=new Map<string,AbortController>()
   uppy.on('cancel-all',()=>{for(const controller of uploads.values())controller.abort()})
   uppy.on('file-removed',(file:{id:string})=>uploads.get(file.id)?.abort())
   uppy.addUploader(async(ids:string[])=>{uppy.emit('upload-start',ids.map(id=>uppy.getFile(id)).filter(Boolean));for(const id of ids){active();const item=uppy.getFile(id);if(!item)continue;const transfer=new AbortController();uploads.set(id,transfer);try{const file=item.data instanceof File?item.data:new File([item.data],item.name,{type:item.type,lastModified:0});const target=joined(context.getTargetPath(),item.name);await ensureUploadDirectory(target.slice(0,target.lastIndexOf('/'))||'/',transfer.signal);const done=await upload(target,file,'',transfer.signal);active();uppy.emit('upload-progress',item,{uploader:uppy,bytesUploaded:file.size,bytesTotal:file.size});uppy.emit('upload-success',item,{status:200,body:done})}catch(error){if(uppy.getFile(id))uppy.emit('upload-error',item,error);throw error}finally{uploads.delete(id)}}})
  },
 }
 persist()
 return {driver,dispose(){controller.abort();versions.clear()},observations:()=>[...observations],observe:async(id:string)=>{const item=observations.find(v=>v.taskId===id);if(item)await observe(item)},forget(id:string){observations=observations.filter(v=>v.taskId!==id);persist()}}
}
function mime(extension:string){return ({png:'image/png',jpg:'image/jpeg',jpeg:'image/jpeg',gif:'image/gif',webp:'image/webp',bmp:'image/bmp',avif:'image/avif',pdf:'application/pdf',mp4:'video/mp4',webm:'video/webm',mp3:'audio/mpeg',ogg:'audio/ogg',wav:'audio/wav',csv:'text/csv',tsv:'text/tab-separated-values',txt:'text/plain',svg:'text/plain',html:'text/plain',htm:'text/plain',zip:'application/zip'} as Record<string,string>)[extension.toLowerCase()]??'text/plain'}
