<script setup lang="ts">
import {computed,nextTick,onBeforeUnmount,onMounted,ref,watch} from 'vue'
import {operationRequest,ApiError,api,type AgentNode,type NodeStatusResponse} from '../api'
import type {MetricsView} from '../metrics-contract'
import {t,display,locale} from '../i18n'
import UiIcon from './UiIcon.vue'
const props=defineProps<{node?:AgentNode;metrics?:MetricsView;online?:boolean}>()
const emit=defineEmits<{core:[];metrics:[value:MetricsView|NodeStatusResponse]}>()
interface CoreInfo {hostname:string;platform:string;platformVersion:string;kernelVersion:string;architecture:string;version:string;bootTime:number;uptime:number;environment:string;addresses:string[];sampledAt:string}
interface Memo {content:string;revision:number;updatedAt:string}
const fieldKeys=['hostname','os','kernel','arch','address','boot','uptime','version'] as const
type FieldKey=typeof fieldKeys[number]
const fieldLabels={hostname:'overview.hostname',os:'overview.os',kernel:'overview.kernel',arch:'overview.arch',address:'overview.address',boot:'overview.boot',uptime:'overview.uptime',version:'overview.version'} as const
const presentationKey='nodedance.overview.presentation'
const visibleFields=ref<FieldKey[]>([...fieldKeys]),memoVisible=ref(true),sensitiveHidden=ref(false)
try{
 const saved=JSON.parse(localStorage.getItem(presentationKey)??'null')
 if(saved&&typeof saved==='object'){
  if(Array.isArray(saved.fields))visibleFields.value=fieldKeys.filter(key=>saved.fields.includes(key))
  if(typeof saved.memoVisible==='boolean')memoVisible.value=saved.memoVisible
  if(typeof saved.sensitiveHidden==='boolean')sensitiveHidden.value=saved.sensitiveHidden
 }
}catch{/* Browser-local display settings are optional. */}
watch([visibleFields,memoVisible,sensitiveHidden],()=>{try{localStorage.setItem(presentationKey,JSON.stringify({fields:visibleFields.value,memoVisible:memoVisible.value,sensitiveHidden:sensitiveHidden.value}))}catch{}},{deep:true})
const core=ref<CoreInfo>(),coreError=ref(''),coreBusy=ref(false),refreshBusy=ref(false)
const memo=ref<Memo>(),draft=ref(''),editing=ref(false),saving=ref(false),memoError=ref(''),conflict=ref(false)
const hostOpen=ref(false),memoOpen=ref(false),copyStatus=ref(''),refreshError=ref('')
const settingsDialog=ref<HTMLDialogElement>(),settingsButton=ref<HTMLButtonElement>(),memoInput=ref<HTMLTextAreaElement>()
let query:MediaQueryList|undefined,timer:ReturnType<typeof setInterval>|undefined,disposed=false
const resize=()=>{hostOpen.value=query?.matches??false;memoOpen.value=(query?.matches??false)||editing.value}
async function loadCore(){
 if(coreBusy.value)return
 coreBusy.value=true
 try{const value=await operationRequest<CoreInfo>('dashboard.core-info');if(!disposed){core.value=value;coreError.value=''}}
 catch(e){if(!disposed)coreError.value=e instanceof Error?e.message:String(e)}
 finally{coreBusy.value=false}
}
async function refreshInfo(){
 if(refreshBusy.value||coreBusy.value)return
 refreshBusy.value=true;refreshError.value='';copyStatus.value=''
 const id=props.node?.nodeId
 try{
  if(id)emit('metrics',await api.nodeMetrics(id))
  else await loadCore()
 }catch(e){if(!disposed&&props.node?.nodeId===id)refreshError.value=e instanceof Error?e.message:String(e)}
 finally{refreshBusy.value=false}
}
async function loadMemo(){
 try{const value=await operationRequest<Memo>('dashboard.memo');if(!disposed){memo.value=value;draft.value=value.content;memoError.value='';conflict.value=false}}
 catch(e){if(!disposed)memoError.value=e instanceof Error?e.message:String(e)}
}
async function startMemoEdit(){memoOpen.value=true;editing.value=true;await nextTick();memoInput.value?.focus()}
function cancelMemoEdit(){draft.value=memo.value?.content??'';editing.value=false;memoError.value='';conflict.value=false}
async function saveMemo(){
 if(!memo.value||saving.value)return
 saving.value=true;memoError.value=''
 try{const value=await operationRequest<Memo>('dashboard.save-memo',{}, {body:JSON.stringify({content:draft.value,revision:memo.value.revision})});if(!disposed){memo.value=value;draft.value=value.content;editing.value=false;conflict.value=false}}
 catch(e){if(!disposed){conflict.value=e instanceof ApiError&&e.status===409;memoError.value=conflict.value?t('overview.memoConflict'):e instanceof Error?e.message:String(e)}}
 finally{saving.value=false}
}
function duration(value?:number){if(value==null)return '—';const days=Math.floor(value/86400),hours=Math.floor(value%86400/3600);return `${days} ${t('overview.days')} ${hours} ${t('overview.hours')}`}
function date(seconds?:number){return seconds?new Date(seconds*1000).toLocaleString(locale.value):'—'}
const title=computed(()=>props.node?(sensitiveHidden.value?'Agent':props.node.displayName):'Core')
const allRows=computed(()=>{
 const m=props.metrics?.metrics,info=core.value
 const get=(value:unknown)=>value==null||value===''?'—':String(value)
 const values=props.node?[
  get(m?.system.hostname.value),[m?.system.platform.value,m?.system.platformVersion.value].filter(Boolean).join(' ')||'—',get(m?.system.kernelVersion.value),get(m?.system.architecture.value),m?.host?.addresses.value?.map(a=>a.address).join(' · ')||'—',date(m?.uptime.value?.bootTimeUnix),duration(m?.uptime.value?.seconds),props.node.agentVersion||'—'
 ]:[get(info?.hostname),[info?.platform,info?.platformVersion].filter(Boolean).join(' ')||'—',get(info?.kernelVersion),get(info?.architecture),info?.addresses?.join(' · ')||'—',date(info?.bootTime),duration(info?.uptime),get(info?.version)]
 return fieldKeys.map((key,index)=>({key,label:t(fieldLabels[key]),value:sensitiveHidden.value&&(key==='hostname'||key==='address')?'••••••':values[index]!}))
})
const rows=computed(()=>allRows.value.filter(row=>visibleFields.value.includes(row.key)))
async function copyInfo(){
 const text=[title.value,...rows.value.map(row=>`${row.label}: ${row.value}`)].join('\n')
 copyStatus.value=''
 try{
  let copied=false
  if(navigator.clipboard){try{await navigator.clipboard.writeText(text);copied=true}catch{}}
  if(!copied){
   const active=document.activeElement as HTMLElement|null,input=document.createElement('textarea')
   input.value=text;input.style.cssText='position:fixed;left:-9999px;top:0';document.body.append(input)
   try{input.select();copied=document.execCommand('copy')}finally{input.remove();active?.focus({preventScroll:true})}
  }
  copyStatus.value=t(copied?'overview.copied':'overview.copyFailed')
 }catch{copyStatus.value=t('overview.copyFailed')}
}
function openSettings(){settingsDialog.value?.showModal()}
function closeSettings(){settingsDialog.value?.close();settingsButton.value?.focus({preventScroll:true})}
watch(()=>props.node?.nodeId,()=>{copyStatus.value='';refreshError.value=''})
onMounted(()=>{query=matchMedia('(min-width:1101px)');resize();query.addEventListener('change',resize);void loadCore();void loadMemo();timer=setInterval(()=>{if(!props.node)void loadCore()},30000)})
onBeforeUnmount(()=>{disposed=true;query?.removeEventListener('change',resize);if(timer)clearInterval(timer)})
</script>
<template>
 <aside class="overview-context" :aria-label="t('overview.context')">
  <section class="context-section" data-testid="overview-host-info">
   <header class="context-heading">
    <h2><button class="section-toggle" type="button" :aria-expanded="hostOpen" @click="hostOpen=!hostOpen">{{t('overview.hostInfo')}}</button></h2>
    <div class="header-actions" :aria-label="t('overview.hostActions')">
     <button ref="settingsButton" type="button" :aria-label="t('overview.displaySettings')" :title="t('overview.displaySettings')" @click="openSettings"><UiIcon name="cog"/></button>
     <button type="button" :disabled="coreBusy||refreshBusy" :aria-label="t('overview.refresh')" :title="t('overview.refresh')" @click="refreshInfo"><UiIcon name="refresh"/></button>
     <button type="button" :aria-label="t(sensitiveHidden?'overview.showSensitive':'overview.hideSensitive')" :title="t(sensitiveHidden?'overview.showSensitive':'overview.hideSensitive')" :aria-pressed="sensitiveHidden" @click="sensitiveHidden=!sensitiveHidden;copyStatus='' "><UiIcon :name="sensitiveHidden?'eyeOff':'eye'"/></button>
     <button type="button" :disabled="!rows.length||(!node&&!core)||(!!node&&!metrics)" :aria-label="t('overview.copyInfo')" :title="t('overview.copyInfo')" @click="copyInfo"><UiIcon name="copy"/></button>
    </div>
   </header>
   <div v-show="hostOpen" class="context-body">
    <div class="context-target"><span v-if="node" :title="title">{{title}}</span><div class="context-actions"><button type="button" :aria-pressed="!node" @click="emit('core')">Core</button><span v-if="node" class="context-status">{{t(online?'overview.online':'overview.lastKnown')}}</span></div></div>
    <p v-if="!node&&core?.environment==='container'" class="context-note">{{t('overview.containerInfo')}}</p>
    <p v-if="node&&!metrics" class="context-note">{{t('overview.noData')}}</p>
    <dl><template v-for="row in rows" :key="row.key"><dt>{{row.label}}</dt><dd :data-field="row.key">{{row.value}}</dd></template></dl>
    <p v-if="!rows.length" class="context-note">{{t('overview.noFields')}}</p>
    <p v-if="!node&&coreError||refreshError" role="alert" class="context-error">{{display(refreshError||coreError)}}</p>
    <p v-if="copyStatus" class="context-note copy-status" role="status">{{copyStatus}}</p>
   </div>
  </section>
  <section v-show="memoVisible||editing" class="context-section memo-section" data-testid="overview-memo">
   <header class="context-heading">
    <h2><button class="section-toggle" type="button" :aria-expanded="memoOpen" @click="memoOpen=!memoOpen">{{t('overview.memo')}}</button></h2>
    <div class="header-actions" :aria-label="t('overview.memoActions')">
     <button v-if="!editing" type="button" :disabled="!memo" :aria-label="t('overview.edit')" :title="t('overview.edit')" @click="startMemoEdit"><UiIcon name="edit"/></button>
     <template v-else><button type="button" :disabled="saving" :aria-label="t('overview.save')" :title="t('overview.save')" @click="saveMemo"><UiIcon name="check"/></button><button type="button" :disabled="saving" :aria-label="t('overview.cancel')" :title="t('overview.cancel')" @click="cancelMemoEdit"><UiIcon name="close"/></button></template>
    </div>
   </header>
   <div v-show="memoOpen" class="context-body"><p class="context-note">{{t('overview.memoShared')}}</p>
    <textarea ref="memoInput" v-model="draft" class="memo-input" :aria-label="t('overview.memo')" :readonly="!editing" :disabled="saving||!memo" rows="7" :placeholder="t('overview.memoPlaceholder')" />
    <div v-if="conflict||!memo" class="memo-actions"><button type="button" :disabled="saving" @click="loadMemo">{{t('overview.reloadMemo')}}</button></div>
    <p v-if="memoError" role="alert" class="context-error">{{display(memoError)}}</p>
   </div>
  </section>
  <dialog ref="settingsDialog" class="context-settings" aria-labelledby="overview-settings-title" @cancel.prevent="closeSettings" @click="event=>{if(event.target===settingsDialog)closeSettings()}">
   <header><h2 id="overview-settings-title">{{t('overview.displaySettings')}}</h2><button type="button" :aria-label="t('overview.close')" @click="closeSettings"><UiIcon name="close"/></button></header>
   <p>{{t('overview.browserLocal')}}</p>
   <label class="settings-option"><input v-model="memoVisible" type="checkbox">{{t('overview.showMemo')}}</label>
   <fieldset><legend>{{t('overview.infoFields')}}</legend><label v-for="key in fieldKeys" :key="key" class="settings-option"><input v-model="visibleFields" :value="key" type="checkbox">{{t(fieldLabels[key])}}</label></fieldset>
   <footer><button type="button" @click="visibleFields=[...fieldKeys];memoVisible=true">{{t('overview.resetDisplay')}}</button><button type="button" @click="closeSettings">{{t('overview.done')}}</button></footer>
  </dialog>
 </aside>
</template>
<style scoped>
.overview-context{position:sticky;top:20px;display:grid;gap:16px;min-width:0;max-height:calc(100dvh - 40px);overflow:auto;scrollbar-width:thin}
.context-section{border:1px solid var(--line);border-radius:var(--radius-card);background:var(--surface);min-width:0}
.context-heading{display:flex;align-items:center;justify-content:space-between;gap:4px;padding:10px 12px}
.context-heading h2{margin:0;min-width:0;font-size:14px;font-weight:500}
.context-heading .section-toggle{font:inherit;padding:4px 0;border:0;background:transparent;color:var(--text);text-align:left;cursor:pointer}
.section-toggle::after{content:'⌄';display:inline-block;margin-left:6px;font-size:12px;color:var(--muted);transform:rotate(-90deg)}.section-toggle[aria-expanded=true]::after{transform:none}
.header-actions{display:flex;align-items:center;gap:2px;flex-shrink:0}
.header-actions button{display:grid;place-items:center;width:28px;height:32px;padding:4px;border:0;border-radius:var(--radius-control);background:transparent;color:var(--muted-strong);cursor:pointer}
.header-actions svg{width:17px;height:17px}.header-actions button[aria-pressed=true]{color:var(--accent);background:var(--accent-soft)}
.context-body{padding:0 16px 16px;min-width:0}.context-target{display:flex;align-items:center;justify-content:space-between;gap:8px;margin-bottom:12px}.context-target>span{font-size:12px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.context-actions,.memo-actions{display:flex;flex-wrap:wrap;align-items:center;gap:8px}.memo-actions{margin-top:10px}
.context-body button,.context-settings button{font:inherit;font-size:12px;min-height:32px;padding:5px 9px;background:var(--control-bg);border:1px solid var(--control-border);border-radius:var(--radius-control);color:var(--text);cursor:pointer}
button:hover:not(:disabled){background:var(--surface-hover)}button:focus-visible{outline:2px solid var(--focus);outline-offset:2px}button:disabled{color:var(--muted);cursor:not-allowed;opacity:.55}.context-body button[aria-pressed=true]{color:var(--accent);background:var(--accent-soft)}
dl{display:grid;grid-template-columns:70px minmax(0,1fr);gap:11px 10px;font-size:12px;margin:0}dt{color:var(--muted)}dd{margin:0;overflow-wrap:anywhere;font-variant-numeric:tabular-nums}
.context-note,.context-status{color:var(--muted);font-size:11px;line-height:1.6}.context-note{margin:0 0 10px}.copy-status{margin:10px 0 0}.context-error{font-size:12px;color:var(--danger);overflow-wrap:anywhere}
.memo-input{width:100%;min-width:0;min-height:150px;resize:vertical;border:1px solid var(--control-border);border-radius:var(--radius-control);padding:9px;background:var(--control-bg);color:var(--text);font:inherit;font-size:12px;line-height:1.6}.memo-input[readonly]{cursor:default}.memo-input:focus-visible{outline:2px solid var(--focus);outline-offset:2px}
.context-settings{width:min(380px,calc(100% - 32px));max-height:calc(100dvh - 32px);overflow:auto;padding:20px;border:1px solid var(--line-strong);border-radius:var(--radius-card);background:var(--surface);color:var(--text);margin:auto}.context-settings::backdrop{background:#0008}.context-settings header{display:flex;align-items:center;justify-content:space-between;gap:16px}.context-settings h2{font-size:18px;margin:0}.context-settings p{color:var(--muted-strong);font-size:12px;line-height:1.6}.settings-option{display:flex;align-items:center;gap:8px;font-size:13px;min-height:36px}.settings-option input{width:16px;height:16px;margin:0;accent-color:var(--accent)}.settings-option input:focus-visible{outline:2px solid var(--focus);outline-offset:3px}.context-settings fieldset{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:0 12px;padding:8px 0;border:0;margin:10px 0}.context-settings legend{font-size:12px;color:var(--muted-strong);padding:0}.context-settings footer{display:flex;justify-content:space-between;gap:10px;margin-top:12px}
@media(max-width:1100px){.overview-context{position:static;max-height:none;overflow:visible;grid-template-columns:repeat(2,minmax(0,1fr))}}@media(max-width:600px){.overview-context{grid-template-columns:minmax(0,1fr);gap:8px}.header-actions button{width:32px;height:36px}}
</style>
