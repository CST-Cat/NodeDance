const assert=require('node:assert/strict'),crypto=require('node:crypto')
module.exports=async({base,browser,context,page,node,headers,catalog})=>{
 const endpoint=(name,params={},query={})=>{const o=catalog.find(v=>v.name===name);assert.ok(o,`Missing ${name}`);let p=o.path;for(const [key,value]of Object.entries(params))p=p.replace(`{${key}}`,encodeURIComponent(value));return base+p+(Object.keys(query).length?'?'+new URLSearchParams(query):'')}
 const intent=()=>crypto.randomBytes(32).toString('hex')
 const visitor=await browser.newContext({locale:'en-US'}),receive=await visitor.newPage()
 try{
  const listView=page.getByTestId('node-files').getByRole('button',{name:'List',exact:true});assert.equal(await listView.count(),1);assert.equal(await listView.getAttribute('aria-pressed'),'true')
  await page.locator('[data-file-name="hello.txt"]').click({button:'right'});await page.getByRole('menuitem',{name:'Share',exact:true}).click()
  const dialog=page.getByRole('dialog',{name:'File sharing',exact:true});await dialog.getByRole('button',{name:'Create link',exact:true}).waitFor()
  const shareTab=dialog.getByRole('tab',{name:'File sharing',exact:true});assert.equal(await shareTab.count(),1)
  if(process.env.NODEDANCE_FILE_GRID_REVIEW)await dialog.locator('[role=tablist]').screenshot({path:require('node:path').resolve(__dirname,'../../.build/file-audit/share-tabs-review.png')});
  await dialog.getByRole('tab',{name:'File requests',exact:true}).click();await dialog.getByRole('button',{name:'Create upload link',exact:true}).waitFor();await shareTab.click();await dialog.getByRole('button',{name:'Create link',exact:true}).waitFor()

  await dialog.getByRole('textbox',{name:'Password',exact:true}).fill('test-share-password')
  await dialog.getByRole('button',{name:'Create link',exact:true}).click();const input=dialog.getByRole('textbox',{name:'Share link',exact:true});await input.waitFor();const shareURL=await input.inputValue(),id=shareURL.split('/').pop()
  await page.setViewportSize({width:390,height:844});const labels=dialog.locator('.share-setting-row label');assert.ok(await labels.count()>0);assert.ok(await labels.evaluateAll(labels=>labels.every(label=>!!label.htmlFor&&!!document.getElementById(label.htmlFor))),'Settings labels identify installed controls');assert.ok(await dialog.locator('.file-shares').evaluate(element=>element.scrollWidth<=element.clientWidth+1),'Sharing controls fit a narrow dialog');await page.setViewportSize({width:1280,height:720});
  assert.match(id,/^[0-9a-f]{64}$/);await dialog.getByRole('button',{name:'QR code',exact:true}).click();await dialog.getByRole('img',{name:'QR code',exact:true}).waitFor()
  const publicURL=endpoint('shares.public',{id}),anonymous=await visitor.request.get(publicURL),admin=await context.request.get(publicURL);assert.equal(anonymous.status(),200);assert.deepEqual(await anonymous.json(),await admin.json());const locked=await anonymous.json();assert.equal(locked.name,'');assert.deepEqual(locked.entries,[]);assert.ok(!('paths'in locked)&&!('nodeId'in locked)&&!('passwordHash'in locked))
  assert.equal((await visitor.request.get(endpoint('shares.hotlink',{id},{path:'hello.txt'}))).status(),405)
  assert.equal((await visitor.request.post(endpoint('shares.unlock',{id}),{headers:{Origin:base},data:{password:'bad'}})).status(),403)
  await receive.goto(shareURL);await receive.getByRole('textbox',{name:'Password',exact:true}).fill('test-share-password');await receive.getByRole('button',{name:'Unlock',exact:true}).click();await receive.getByRole('button',{name:'Download',exact:true}).waitFor()
  const downloaded=receive.waitForEvent('download');await receive.getByRole('button',{name:'Download',exact:true}).click();assert.equal((await downloaded).suggestedFilename(),'hello.txt')
  let share=await(await context.request.get(endpoint('shares.get',{id}))).json();assert.equal(share.downloads,1)
  const owner=await context.request.get(endpoint('shares.download',{id},{path:'hello.txt'}));assert.equal(owner.status(),200);assert.equal(await owner.text(),'edited fixture\n');assert.equal((await(await context.request.get(endpoint('shares.get',{id}))).json()).downloads,1)
  const badCSRF=await context.request.delete(endpoint('shares.revoke',{id},{revision:share.revision}),{headers:{Origin:base}});assert.equal(badCSRF.status(),403)
  assert.equal((await visitor.request.get(endpoint('shares.list'))).status(),401)
  const changed={requestId:intent(),nodeId:node,paths:[],name:'changed',expiryDays:1,maxDownloads:2,password:'',revision:share.revision};let edit=await context.request.put(endpoint('shares.update',{id}),{headers,data:changed});assert.equal(edit.status(),200);share=await edit.json();assert.equal((await context.request.put(endpoint('shares.update',{id}),{headers,data:changed})).status(),409)
  const responses=await Promise.all(Array.from({length:8},()=>visitor.request.get(endpoint('shares.hotlink',{id},{path:'hello.txt'}))));assert.equal(responses.filter(r=>r.status()===200).length,1,'Concurrent downloads cannot exceed the remaining count');assert.equal((await visitor.request.get(publicURL)).status(),404)
  assert.equal((await context.request.delete(endpoint('shares.revoke',{id},{revision:share.revision}),{headers})).status(),204)
  await dialog.getByRole('button',{name:'Close dialog',exact:true}).click()
  const create=async(paths,extra={})=>{const inBody={requestId:intent(),nodeId:node,paths,name:'folder-share',expiryDays:7,maxDownloads:0,...extra};const response=await context.request.post(endpoint('shares.create'),{headers,data:inBody});assert.equal(response.status(),201,await response.text());return {v:await response.json(),inBody}}
  const {v:folder,inBody}=await create(['/single']);const retry=await context.request.post(endpoint('shares.create'),{headers,data:inBody});assert.equal((await retry.json()).id,folder.id)
  assert.ok(folder.entries.some(e=>e.path==='single'&&e.directory));assert.ok(folder.entries.some(e=>e.path==='single/only.txt'))
  const folderPage=await visitor.newPage();await folderPage.goto(base+'/s/'+folder.id);await folderPage.getByRole('button',{name:'Open',exact:true}).click();await folderPage.getByRole('button',{name:'Download',exact:true}).waitFor();assert.equal(await folderPage.locator('img[onerror]').count(),0)
  const invalidPaths=await visitor.request.get(endpoint('shares.hotlink',{id:folder.id},{path:'../hello.txt'}));assert.equal(invalidPaths.status(),404)
  for(const source of ['/link','/../etc/shadow']){const r=await context.request.post(endpoint('shares.create'),{headers,data:{...inBody,requestId:intent(),paths:[source]}});assert.equal(r.status(),400)}
  const uploadURL=endpoint('shares.upload',{}, {name:'uploaded.txt',expiryDays:7,maxDownloads:0,requestId:intent()});const uploaded=await context.request.put(uploadURL,{headers:{...headers,'Content-Type':'application/octet-stream'},data:'direct share upload'});assert.equal(uploaded.status(),201);const uploadedShare=await uploaded.json();assert.equal(await(await visitor.request.get(endpoint('shares.hotlink',{id:uploadedShare.id},{path:'uploaded.txt'}))).text(),'direct share upload')
  const request=await context.request.post(endpoint('share-requests.create'),{headers,data:{requestId:intent(),name:'Collect fixture',notes:'<img src=x onerror=window.collectionXSS=1>',expiryDays:1,maxFiles:1,maxBytes:1024}});assert.equal(request.status(),200);let invitation=await request.json()
  const collectionPage=await visitor.newPage();await collectionPage.goto(base+'/r/'+invitation.id);await collectionPage.getByText('Collect fixture',{exact:true}).waitFor();assert.equal(await collectionPage.locator('img[onerror]').count(),0)
  const publicRequest=endpoint('share-requests.public',{id:invitation.id}),publicMetadata=await(await visitor.request.get(publicRequest)).json();assert.ok(!('entries'in publicMetadata));assert.deepEqual(publicMetadata,await(await context.request.get(publicRequest)).json())
  const upload=(name)=>visitor.request.put(endpoint('share-requests.upload',{id:invitation.id},{name}),{headers:{Origin:base,'Content-Type':'application/octet-stream'},data:'private collected content'})
  assert.equal((await upload('../escape')).status(),400)
  const concurrent=await Promise.all([upload('one.txt'),upload('two.txt')]);assert.equal(concurrent.filter(r=>r.status()===201).length,1)
  invitation=await(await context.request.get(endpoint('share-requests.get',{id:invitation.id}))).json();assert.equal(invitation.entries.length,1);assert.equal((await visitor.request.get(endpoint('share-requests.download',{id:invitation.id},{path:invitation.entries[0].path}))).status(),401)
  const collected=await context.request.get(endpoint('share-requests.download',{id:invitation.id},{path:invitation.entries[0].path}));assert.equal(await collected.text(),'private collected content')
  assert.equal((await context.request.delete(endpoint('share-requests.revoke',{id:invitation.id},{revision:invitation.revision}),{headers})).status(),204);assert.equal((await visitor.request.get(publicRequest)).status(),404);assert.equal((await upload('after.txt')).status(),404)
  for(const v of [folder,uploadedShare]){assert.equal((await context.request.delete(endpoint('shares.revoke',{id:v.id},{revision:v.revision}),{headers})).status(),204);assert.equal((await visitor.request.get(endpoint('shares.public',{id:v.id}))).status(),404)}
  console.log('File sharing: actual right-click dialog, password, QR, public folder browsing, downloads, owner count, atomic limits, revision conflicts, withdrawal, upload, private collection and concurrent collection bounds passed')
 }finally{await visitor.close()}
}
