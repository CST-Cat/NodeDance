<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useViewNavigation, useViewTab } from '../navigation'
import { requestIntent, intentKey, finishIntent } from '../operation-intent'
import { t } from '../i18n'
import { api, operationRequest, type AgentNode } from '../api'
import { operationCatalog, exactJSON, type CoreOperation } from '../operations'
import ContractFields from './ContractFields.vue'
const nodes = ref<AgentNode[]>([]), selected = ref<string[]>([]), nodeId = ref(''), operations = ref<CoreOperation[]>([])
const viewNavigation = useViewNavigation()
const hostTabs = ['settings','assets','commands','probes','enrollment'] as const
const tab = useViewTab('tab', 'settings', () => hostTabs)
const busy = ref(false), error = ref(''), notice = ref('')
const settings = reactive<Record<string,unknown>>({}), asset = reactive<Record<string,unknown>>({})
const command = reactive<Record<string,unknown>>({ script: '', timeoutSeconds: 60, confirm: false })
const probe = reactive<Record<string,unknown>>({ name:'', kind:'icmp', target:'icmp://', intervalSeconds:30, timeoutSeconds:3, enabled:true })
const grant = reactive<Record<string,unknown>>({displayPrefix:'node',maxUses:10,lifetimeSeconds:600,coreUrl:window.location.origin})
interface Job {id:string;status:string;output?:string;exitCode?:number;error?:string;truncated?:boolean}
interface Batch {id:string;results:Array<{nodeId:string;jobId?:string;probeId?:string;error?:string;stale:boolean;job?:Job}>}
interface Grant {id:string;displayPrefix:string;maxUses:number;uses:number;expiresAt:string;revoked:boolean;token?:string}
const batches = ref<Batch[]>([]), currentBatch = ref<Batch>(), grantList = ref<Grant[]>([]), grantResult = ref<{grant:Grant;installCommand:string;dockerInstallCommand:string}>()
const diagnostics = ref('')
function schema(name:string) {return operations.value.find(o=>o.name===name)?.bodySchema}
const settingsSchema = computed(()=>schema('agents.configure')), assetSchema=computed(()=>schema('nodes.save-asset'))
let disposed = false
onBeforeUnmount(()=>{disposed=true})
async function perform(work:()=>Promise<void>) {if(disposed||busy.value)return;busy.value=true;error.value='';notice.value='';try{await work()}catch(e){error.value=e instanceof Error?e.message:String(e)}finally{busy.value=false}}
async function loadNode() {await perform(async()=>{
 const id=nodeId.value; for(const value of [settings,asset])for(const k of Object.keys(value))delete value[k]
 const results=await Promise.allSettled([operationRequest<{settings:Record<string,unknown>}>('agents.settings',{params:{nodeId:id}}),operationRequest<Record<string,unknown>>('nodes.asset',{params:{nodeId:id}})])
 if(results[0].status==='fulfilled')Object.assign(settings,results[0].value.settings)
 if(results[1].status==='fulfilled')Object.assign(asset,results[1].value)
 const failed=results.find(r=>r.status==='rejected');if(failed?.status==='rejected')throw failed.reason
})}
async function save(name:string,body:Record<string,unknown>){await perform(async()=>{const data={...body};if(data.expiresAt==='')delete data.expiresAt;await operationRequest(name,{params:{nodeId:nodeId.value}},{body:exactJSON(data)});notice.value=t('host.saved')})}
async function submitBatch(name: 'commands.batch' | 'probes.batch', body: Record<string,unknown>) {await perform(async()=>{
 const payload=exactJSON(body), intent=await requestIntent(name,{},payload)
 if(disposed)return
 const result=await operationRequest<Batch>(name,{}, {headers:{'Idempotency-Key':intentKey(intent)},body:payload})
 finishIntent(intent)
 if(disposed)return
 currentBatch.value=result
 if(name==='commands.batch')batches.value=(await operationRequest<{batches:Batch[]}>('commands.batches')).batches
 else notice.value=t('host.saved')
})}
async function submitCommand(){await submitBatch('commands.batch',{...command,nodeIds:[...selected.value]})}
async function submitProbe(){const body:Record<string,unknown>={...probe,nodeIds:[...selected.value]};if(!['http','https'].includes(String(probe.kind)))delete body.expectedHttpStatus;await submitBatch('probes.batch',body)}
async function inspectBatch(id:string,cancel=false){await perform(async()=>{currentBatch.value=await operationRequest<Batch>(cancel?'commands.batch-cancel':'commands.batch-status',{params:{batchId:id}})})}
async function createGrant(){await perform(async()=>{const result=await operationRequest<typeof grantResult.value>('agents.create-grant',{}, {body:exactJSON(grant)});if(disposed)return;grantResult.value=result;grantList.value=(await operationRequest<{grants:Grant[]}>('agents.grants')).grants})}
async function revokeGrant(id:string){await perform(async()=>{await operationRequest('agents.revoke-grant',{params:{grantId:id}});if(disposed)return;grantList.value=(await operationRequest<{grants:Grant[]}>('agents.grants')).grants})}
function selectNode(){viewNavigation.go({...viewNavigation.route.value,nodeId:nodeId.value||undefined});void loadNode()}
watch(()=>viewNavigation.route.value.nodeId,id=>{
 if(!nodes.value.length)return
 const next=nodes.value.find(n=>n.nodeId===id)?.nodeId??nodes.value[0]!.nodeId
 if(next!==nodeId.value){nodeId.value=next;void loadNode()}
})
onMounted(async()=>{await perform(async()=>{const [list,catalog,history,grants]=await Promise.all([api.nodes(),operationCatalog(),operationRequest<{batches:Batch[]}>('commands.batches'),operationRequest<{grants:Grant[]}>('agents.grants')]);if(disposed)return;nodes.value=list.nodes.filter(n=>!['pending','revoked'].includes(n.status));nodeId.value=nodes.value.find(n=>n.nodeId===viewNavigation.route.value.nodeId)?.nodeId??nodes.value[0]?.nodeId??'';operations.value=catalog.operations;batches.value=history.batches;grantList.value=grants.grants});if(nodeId.value)await loadNode()})
</script>
<template>
 <section class="host-page">
  <header><div><h1>{{ t('host.title') }}</h1><p>{{ t('host.subtitle') }}</p></div></header>
  <nav class="host-tabs" :aria-label="t('host.title')"><button v-for="item in hostTabs" :key="item" :aria-pressed="tab===item" @click="tab=item">{{ t(`host.tab.${item}`) }}</button></nav>
  <p v-if="error" role="alert" class="host-error">{{ error }}</p><p v-if="notice" role="status">{{ notice }}</p>
  <section v-if="tab==='settings'||tab==='assets'" class="host-card">
   <label class="host-node">{{ t('host.node') }}<select v-model="nodeId" :disabled="busy" @change="selectNode"><option v-for="node in nodes" :key="node.nodeId" :value="node.nodeId">{{ node.displayName }} · {{ node.status }}</option></select></label>
   <template v-if="nodeId">
    <form v-if="tab==='settings'" @submit.prevent="save('agents.configure',settings)"><p>{{ t('host.settingsHint') }}</p><ContractFields :schema="settingsSchema" :values="settings" /><div class="host-actions"><button class="primary-button" :disabled="busy || !Object.keys(settings).length">{{ t('host.save') }}</button><button type="button" :disabled="busy" @click="perform(async()=>{diagnostics=JSON.stringify(await operationRequest('agents.diagnostics',{params:{nodeId}}),null,2)})">{{ t('host.diagnostics') }}</button></div><pre v-if="diagnostics">{{ diagnostics }}</pre></form>
    <form v-else @submit.prevent="save('nodes.save-asset',asset)"><p>{{ t('host.assetsHint') }}</p><ContractFields :schema="assetSchema" :values="asset" /><button class="primary-button" :disabled="busy || !Object.keys(asset).length">{{ t('host.save') }}</button></form>
   </template><p v-else>{{ t('host.noNodes') }}</p>
  </section>
  <template v-else-if="tab==='commands'||tab==='probes'">
   <div class="host-grid"><section class="host-card"><h2>{{ t('host.nodes') }}</h2><p>{{ t('host.selectionHint') }}</p><label v-for="node in nodes" :key="node.nodeId" class="host-check"><input v-model="selected" type="checkbox" :value="node.nodeId" />{{ node.displayName }} · {{ node.status }}</label></section>
   <form class="host-card" @submit.prevent="tab==='commands'?submitCommand():submitProbe()"><h2>{{ t(`host.tab.${tab}`) }}</h2><p>{{ tab==='commands'?t('host.commandHint'):t('host.probeHint') }}</p><ContractFields :schema="schema(tab==='commands'?'commands.batch':'probes.batch')" :values="tab==='commands'?command:probe" :omit="tab==='commands'?['nodeIds']:['nodeIds', ...(['http','https'].includes(String(probe.kind))?[]:['expectedHttpStatus'])]" /><button class="primary-button" :disabled="busy || !selected.length">{{ t('host.submit') }} · {{ selected.length }}</button></form></div>
   <section v-if="currentBatch" class="host-card"><div class="host-actions"><h2>{{ t('host.results') }}</h2><button v-if="currentBatch.results.some(r=>r.jobId)" :disabled="busy" @click="inspectBatch(currentBatch.id)">{{ t('host.refresh') }}</button><button v-if="currentBatch.results.some(r=>r.jobId)" :disabled="busy" @click="inspectBatch(currentBatch.id,true)">{{ t('host.cancel') }}</button></div><article v-for="result in currentBatch.results" :key="result.nodeId" class="host-result"><strong>{{ nodes.find(n=>n.nodeId===result.nodeId)?.displayName??result.nodeId }}</strong><p>{{ result.job?.status || result.probeId || result.jobId || '—' }} <span v-if="result.stale">· {{ t('host.stale') }}</span></p><p v-if="result.error" class="host-error">{{ result.error }}</p><p v-if="result.job?.exitCode!==undefined">{{ t('host.exit') }}: {{ result.job.exitCode }}</p><pre v-if="result.job?.output">{{ result.job.output }}</pre><p v-if="result.job?.truncated">{{ t('host.truncated') }}</p></article></section>
   <section v-if="tab==='commands'" class="host-card"><h2>{{ t('host.history') }}</h2><button v-for="batch in batches" :key="batch.id" :disabled="busy" @click="inspectBatch(batch.id)">{{ batch.id.slice(0,12) }} · {{ batch.results.length }}</button></section>
  </template>
  <template v-else><form class="host-card" @submit.prevent="createGrant"><h2>{{ t('host.tab.enrollment') }}</h2><p>{{ t('host.enrollmentHint') }}</p><ContractFields :schema="schema('agents.create-grant')" :values="grant" /><button class="primary-button" :disabled="busy">{{ t('host.submit') }}</button><section v-if="grantResult"><p>{{ t('host.secretHint') }}</p><pre>{{ grantResult.grant.token }}</pre><pre>{{ grantResult.installCommand }}</pre><pre>{{ grantResult.dockerInstallCommand }}</pre></section></form><section class="host-card"><article v-for="g in grantList" :key="g.id" class="host-result"><strong>{{ g.displayPrefix }}</strong><p>{{ g.uses }} / {{ g.maxUses }} · {{ new Date(g.expiresAt).toLocaleString() }}</p><button :disabled="busy||g.revoked" @click="revokeGrant(g.id)">{{ t('host.revoke') }}</button></article></section></template>
 </section>
</template>
<style scoped>
.host-page{width:min(1280px,calc(100% - 48px));margin:32px auto 64px;color:var(--text)}header h1{font-size:clamp(28px,4vw,42px);margin:0 0 10px}header p,.host-card>p,.host-card form>p{color:var(--muted)}.host-tabs button{flex:0 0 auto;white-space:nowrap}.host-tabs{display:flex;gap:8px;overflow:auto;margin:24px 0}.host-page button{font:inherit;cursor:pointer;border:1px solid var(--line);background:var(--surface);color:var(--text);border-radius:var(--radius-control);padding:9px 14px}.host-page button[aria-pressed=true]{color:var(--accent);background:var(--accent-soft);border-color:var(--accent)}.host-page button.primary-button{background:var(--accent-soft);color:var(--accent);margin-top:16px}.host-page button:hover:not(:disabled){border-color:var(--accent)}.host-page button:active:not(:disabled){background:var(--accent-soft);color:var(--accent)}.host-page button:disabled{opacity:.5;cursor:not-allowed}.host-card{border:1px solid var(--line);background:var(--surface-soft);border-radius:var(--radius-card);padding:22px;margin-bottom:18px;min-width:0}.host-grid{display:grid;grid-template-columns:minmax(220px,.6fr) minmax(0,1.4fr);gap:18px}.host-check{display:flex;gap:9px;align-items:center;margin:14px 0;overflow-wrap:anywhere}.host-actions{display:flex;align-items:center;gap:12px;flex-wrap:wrap;margin-top:16px}.host-node{display:flex;flex-direction:column;gap:8px;max-width:440px}.host-node select{font:inherit;color:var(--text);background:var(--surface);border:1px solid var(--line);padding:10px;border-radius:var(--radius-control);max-width:100%}.host-result{border-top:1px solid var(--line);padding:16px 0}.host-error{color:var(--danger);overflow-wrap:anywhere}pre{white-space:pre-wrap;overflow-wrap:anywhere;max-height:400px;overflow:auto;background:var(--surface);border:1px solid var(--line);border-radius:var(--radius-control);padding:12px;font-size:12px}h2{font-size:20px}.host-check input{flex:none;width:17px;height:17px}@media(max-width:700px){.host-page{width:calc(100% - 24px);margin:20px auto}.host-grid{grid-template-columns:1fr}.host-card{padding:16px}}
</style>
