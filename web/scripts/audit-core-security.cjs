const cases={secrets:'./secret-storage-regressions.cjs',forwarded:'./forwarded-address-regressions.cjs',channel:'./agent-channel-regressions.cjs',config:'./core-config-regressions.cjs'}
const only=process.env.NODEDANCE_AUDIT_SECURITY_ONLY
;(async()=>{
 for(const [name,file] of Object.entries(cases)){
  if(only&&only!==name)continue
  await require(file)()
 }
 console.log('audit-core-security passed.')
})().catch(error=>{console.error(error);process.exit(1)})
