const cases={entry:'./agent-entry-regressions.cjs',safety:'./file-safety-regressions.cjs',transfer:'./peer-transfer-regressions.cjs',commands:'./host-command-regressions.cjs',terminal:'./terminal-regressions.cjs',shares:'./share-owner-regressions.cjs'}
const only=process.env.NODEDANCE_AUDIT_AGENT_ONLY
;(async()=>{
 if(only&&!cases[only])throw new Error(`Unknown NODEDANCE_AUDIT_AGENT_ONLY case ${only}; expected one of ${Object.keys(cases).join(', ')}`)
 for(const [name,file] of Object.entries(cases)){
  if(only&&only!==name)continue
  await require(file)()
 }
 console.log('audit-agent passed.')
})().catch(error=>{console.error(error);process.exit(1)})
