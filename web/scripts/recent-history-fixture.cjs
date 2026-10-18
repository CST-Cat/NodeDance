// Persisted five-second response fixture, independent of dashboard samples.
const assert=require('node:assert/strict')
module.exports=function(url){
 const query=new URL(url).searchParams,from=Date.parse(query.get('from')),to=Date.parse(query.get('to')),step=5000;
 assert.equal(query.get('resolution'),'5s');assert.ok(to-from>0&&to-from<=4*60*60*1000);
 const start=Math.ceil(from/step)*step;
 const keys=['network.received_bytes_per_second','network.sent_bytes_per_second','disk.read_bytes_per_second','disk.write_bytes_per_second'];
 return {nodeId:'history-fixture',resolution:'5s',from:new Date(from).toISOString(),to:new Date(to).toISOString(),series:keys.map((key,index)=>({key,points:Array.from({length:Math.max(0,Math.ceil((to-start)/step))},(_,i)=>{const at=start+i*step;return {bucketAt:new Date(at).toISOString(),value:(index+1)*1024+(Math.floor(at/step)%17)*100,samples:1,minimum:0,maximum:8192}}).filter(p=>Math.floor(Date.parse(p.bucketAt)/step)%120!==0)}))};
}
