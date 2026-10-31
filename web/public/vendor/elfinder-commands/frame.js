(() => {
  const channel = 'nodedance-elfinder-commands';
  let state, finder;
  const emit = (type, value) => parent.postMessage({ channel, type, ...value }, location.origin);
  const hash = path => 'r_' + btoa(String.fromCharCode(...new TextEncoder().encode(path))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
  const parentPath = path => path.substring(0, path.lastIndexOf('/')) || '/';
  const paths = hashes => (Array.isArray(hashes) ? hashes : hashes ? [hashes] : finder.selected()).map(value => finder.file(value)?.ndpath).filter(Boolean);
  const mime = name => { const extension = name.toLowerCase().split('.').pop(); const types={png:'image/png',jpg:'image/jpeg',jpeg:'image/jpeg',gif:'image/gif',webp:'image/webp',bmp:'image/bmp',pdf:'application/pdf',zip:'application/zip',gz:'application/x-gzip',tar:'application/x-tar',bz2:'application/x-bzip2',mp3:'audio/mpeg',mp4:'video/mp4',webm:'video/webm',txt:'text/plain',md:'text/plain',json:'application/json',js:'application/javascript',html:'text/plain',svg:'text/plain'}; return Object.hasOwn(types,extension)?types[extension]:'application/octet-stream'; };
  const file = row => ({hash:hash(row.path),phash:row.path==='/'?undefined:hash(parentPath(row.path)),name:row.name,mime:row.kind==='directory'?'directory':mime(row.name),size:row.size,ts:Math.floor(Number(row.modifiedAt)/1e9),read:1,write:1,dirs:row.kind==='directory'?1:0,ndpath:row.path,volumeid:'r_'});
  const snapshot = () => {
    const ancestors=[];for(let path=state.path;;path=parentPath(path)){ancestors.push(file({path,name:path==='/'?'/':path.split('/').pop(),kind:'directory',size:0,modifiedAt:0}));if(path==='/')break}
    return {api:'2.1',cwd:ancestors[0],files:[...ancestors,...state.rows.map(file)],options:{path:state.path,separator:'/',disabled:[],copyOverwrite:false,uploadOverwrite:false,archivers:{create:['application/zip'],extract:['application/zip','application/x-tar','application/x-gzip','application/x-bzip2']},syncMinMs:0,url:'',tmbUrl:''},uplMaxSize:'0'};
  };
  const initialize = () => {
    if(finder) finder.destroy();
    document.getElementById('commands').replaceChildren();
    elFinder.prototype.commands.share=function(){this.getstate=()=>0;this.exec=()=>jQuery.Deferred().resolve()};
    elFinder.prototype.commands.transfer=function(){this.getstate=()=>0;this.exec=()=>jQuery.Deferred().resolve()};
    const commands=['open','edit','download','share','transfer','info','copy','cut','paste','rename','rm','archive','extract','reload'];
    finder=new elFinder(document.getElementById('commands'),{url:'/',baseUrl:'/vendor/elfinder/',cssAutoLoad:false,lang:state.language==='zh-CN'?'zh_CN':'en',ui:[],commands,rememberLastDir:false,useBrowserHistory:false,resizable:false,allowShortcuts:false,dragUploadAllow:'none',contextmenu:{navbar:[],cwd:[],files:[]},transport:{send:options=>{const pending=jQuery.Deferred();pending.abort=()=>pending.reject({status:0,statusText:'abort'});queueMicrotask(()=>options.data.cmd==='open'?pending.resolve(snapshot()):options.data.cmd==='ls'?pending.resolve({list:{}}):pending.reject(['errUnknownCmd']));return pending}},handlers:{load:()=>emit('ready',{})}});
    for(const name of commands){const command=finder.getCommand(name);if(command)command.exec=targets=>{emit('action',{action:name,paths:paths(targets)});return jQuery.Deferred().resolve()}}
  };
  addEventListener('message',event=>{
    if(event.source!==parent||event.origin!==location.origin||event.data?.channel!==channel)return;
    const message=event.data;
    if(message.type==='data'){const rebuild=!state||state.path!==message.value.path||state.language!==message.value.language;state=message.value;rebuild?initialize():finder.sync().always(()=>emit('ready',{}))}
    else if(message.type==='command'&&finder)finder.getCommand(message.action)?.exec(message.paths.map(hash));
  });
  emit('mounted',{});
})();
