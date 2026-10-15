// Preserve license notices for the file-manager package and its dependency graph.
const fs=require('node:fs'),path=require('node:path')
const packages=new Map()
function visit(directory){
 directory=fs.realpathSync(directory)
 const info=JSON.parse(fs.readFileSync(path.join(directory,'package.json'),'utf8')),key=info.name+'@'+info.version
 if(packages.has(key))return
 const notices=fs.readdirSync(directory).filter(n=>/^(license|licence|copying|notice)([.-]|$)/i.test(n)).filter(n=>fs.statSync(path.join(directory,n)).isFile()).map(n=>fs.readFileSync(path.join(directory,n),'utf8'))
 if(!notices.length)throw Error(`Missing license notice for ${key}`)
 packages.set(key,{info,notices})
 for(const name of Object.keys({...info.dependencies,...info.peerDependencies})){
  const modules=info.name.startsWith('@')?path.dirname(path.dirname(directory)):path.dirname(directory)
  const candidate=path.join(modules,name)
  if(fs.existsSync(path.join(candidate,'package.json')))visit(candidate)
 }
}
for(const name of ['jquery','jquery-ui','react','react-dom','@preact/signals-react','framer-motion'])visit(path.resolve(__dirname,'../node_modules/'+name))
const vendor=path.resolve(__dirname,'../public/vendor/elfinder'),manifest=JSON.parse(fs.readFileSync(path.join(vendor,'manifest.json'),'utf8'))
const crypto=require('node:crypto')
for(const [name,hash]of Object.entries(manifest.files)){if(crypto.createHash('sha256').update(fs.readFileSync(path.join(vendor,name))).digest('hex')!==hash)throw Error('Vendored elFinder integrity: '+name)}
packages.set('elFinder@'+manifest.version,{info:manifest,notices:[fs.readFileSync(path.join(vendor,'LICENSE.md'),'utf8')]})
const editors=path.resolve(__dirname,'../public/vendor/file-editors'),editorManifest=JSON.parse(fs.readFileSync(path.join(editors,'manifest.json'),'utf8'))
for(const [name,hash]of Object.entries(editorManifest.files))if(crypto.createHash('sha256').update(fs.readFileSync(path.join(editors,name))).digest('hex')!==hash)throw Error('Vendored editor integrity: '+name)
for(const info of editorManifest.packages)packages.set(info.name+'@'+info.version,{info,notices:[fs.readFileSync(path.join(editors,info.name+'-LICENSE'),'utf8')]})
const text='NodeDance file-manager third-party notices\nGenerated from installed, locked dependencies.\n\n'+[...packages].sort(([a],[b])=>a.localeCompare(b)).map(([name,{info,notices}])=>name+'\nLicense: '+JSON.stringify(info.license)+'\n'+(notices.join('\n').replace(/\r\n/g,'\n')||'See the package source for its license notice.')+'\n').join('\n')
const output=path.resolve(__dirname,'../public/licenses/file-manager-notices.txt')
if(process.argv.includes('--check')){if(!fs.existsSync(output)||fs.readFileSync(output,'utf8')!==text)throw Error('File manager license notices need regeneration')}
else{fs.mkdirSync(path.dirname(output),{recursive:true});fs.writeFileSync(output,text)}
console.log(`File manager notices: ${packages.size} packages`)
