// Preserve license notices for the file-manager package and its dependency graph.
const fs=require('node:fs'),path=require('node:path')
const packages=new Map(), supplemental=JSON.parse(fs.readFileSync(path.join(__dirname,'file-license-notices.json'),'utf8'))
function visit(directory){
 directory=fs.realpathSync(directory)
 const info=JSON.parse(fs.readFileSync(path.join(directory,'package.json'),'utf8')),key=info.name+'@'+info.version
 if(packages.has(key))return
 const notices=fs.readdirSync(directory).filter(n=>/^(license|licence|copying|notice)([.-]|$)/i.test(n)).filter(n=>fs.statSync(path.join(directory,n)).isFile()).map(n=>fs.readFileSync(path.join(directory,n),'utf8'))
 if(!notices.length&&supplemental[key])notices.push(supplemental[key])
 if(!notices.length)throw Error(`Missing license notice for ${key}`)
 packages.set(key,{info,notices})
 for(const name of Object.keys({...info.dependencies,...info.peerDependencies})){
  const modules=info.name.startsWith('@')?path.dirname(path.dirname(directory)):path.dirname(directory)
  const candidate=path.join(modules,name)
  if(fs.existsSync(path.join(candidate,'package.json')))visit(candidate)
 }
}
visit(path.resolve(__dirname,'../node_modules/vuefinder'))
const text='NodeDance file-manager third-party notices\nGenerated from installed, locked dependencies.\n\n'+[...packages].sort(([a],[b])=>a.localeCompare(b)).map(([name,{info,notices}])=>name+'\nLicense: '+JSON.stringify(info.license)+'\n'+(notices.join('\n')||'See the package source for its license notice.')+'\n').join('\n')
const output=path.resolve(__dirname,'../public/licenses/file-manager-notices.txt')
if(process.argv.includes('--check')){if(!fs.existsSync(output)||fs.readFileSync(output,'utf8')!==text)throw Error('File manager license notices need regeneration')}
else{fs.mkdirSync(path.dirname(output),{recursive:true});fs.writeFileSync(output,text)}
console.log(`File manager notices: ${packages.size} packages`)
