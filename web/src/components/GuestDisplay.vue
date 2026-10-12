<script setup lang="ts">
import { defineAsyncComponent, computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, operationRequest } from '../api'
import { t } from '../i18n'
import type { GuestDisplay } from '../guest'
const HistoricalMetrics=defineAsyncComponent(()=>import('./HistoricalMetrics.vue'))
const props=defineProps<{preview?:GuestDisplay}>()
const emit=defineEmits<{back:[]}>()
const snapshot=ref<GuestDisplay>(),error=ref(''),loading=ref(false),filter=ref('')
const data=computed(()=>props.preview??snapshot.value)
const nodes=computed(()=>data.value?.nodes.filter(n=>n.name.toLowerCase().includes(filter.value.toLowerCase()))??[])
let timer:ReturnType<typeof setTimeout>|undefined,controller:AbortController|undefined,disposed=false
async function load(){if(loading.value)return;loading.value=true;error.value='';controller=new AbortController()
 try{snapshot.value=await operationRequest<GuestDisplay>('guest.display',{}, {signal:controller.signal})}
 catch(reason){if(!disposed){snapshot.value=undefined;error.value=reason instanceof ApiError&&reason.status===404?t('guest.unavailable'):reason instanceof Error?reason.message:String(reason)}}
 finally{loading.value=false;if(!disposed&&!props.preview)timer=setTimeout(load,30000)}
}
function refresh(){if(timer)clearTimeout(timer);void load()}
function value(v:number|undefined,unit:string){if(v===undefined)return '—';if(unit==='B/s')return (v/1048576).toFixed(2)+' MiB/s';if(unit==='s')return (v/86400).toFixed(1)+' '+t('guest.days');return v.toFixed(1)+' '+unit}
onMounted(()=>{if(!props.preview)void load()})
onBeforeUnmount(()=>{disposed=true;controller?.abort();if(timer)clearTimeout(timer)})
</script>
<template>
 <section class="guest-page" data-testid="guest-display">
  <header><div><p class="eyebrow">{{t('guest.mode')}}</p><h1>{{data?.title??t('guest.mode')}}</h1><p class="muted">{{t('guest.readOnly')}}</p></div><div class="guest-actions"><button v-if="!preview" type="button" @click="emit('back')">{{t('guest.back')}}</button><button v-if="!preview" type="button" :disabled="loading" @click="refresh">{{t('guest.refresh')}}</button></div></header>
  <p v-if="error" role="alert" class="guest-error">{{error}}</p><p v-if="loading&&!data" role="status">{{t('guest.loading')}}</p>
  <template v-if="data"><p class="muted">{{t('guest.updated')}} {{new Date(data.generatedAt).toLocaleString()}}</p>
   <label v-if="data.nodes.length" class="guest-search">{{t('guest.search')}}<input v-model="filter" type="search" /></label>
   <div class="guest-grid"><article v-for="node in nodes" :key="node.id" class="guest-card"><h2>{{node.name}}</h2><p v-if="node.status" class="guest-state">{{t(`guest.state.${node.status}`)}}</p><dl><div v-for="(metric,key) in node.metrics" :key="key"><dt>{{t(`guest.metric.${key}`)}}</dt><dd>{{metric.status==='known'?value(metric.value,metric.unit):'—'}}<small v-if="metric.status!=='known'">{{t(`guest.state.${metric.status}`)}}</small></dd></div></dl><HistoricalMetrics v-if="node.history" :history="{...node.history,nodeId:node.id}" :loading="false" error="" :node-online="node.status!=='offline'" :available-only="true" /></article></div>
   <section v-if="data.probes.length" class="guest-services"><h2>{{t('guest.services')}}</h2><div class="guest-grid"><article v-for="probe in data.probes" :key="probe.id" class="guest-card"><h3>{{probe.name}}</h3><p>{{t(`guest.state.${probe.status}`)}}</p><p v-if="probe.latencyMs!==undefined">{{probe.latencyMs}} ms</p><p v-if="probe.checkedAt" class="muted">{{new Date(probe.checkedAt).toLocaleString()}}</p></article></div></section>
   <p v-if="!data.nodes.length&&!data.probes.length" class="muted">{{t('guest.empty')}}</p>
  </template>
 </section>
</template>
<style scoped>
.guest-page{width:min(1280px,calc(100% - 48px));margin:32px auto 64px;color:var(--text)}header{display:flex;justify-content:space-between;gap:20px;flex-wrap:wrap}h1{font-family:var(--display-font);font-size:clamp(28px,4vw,42px);margin:0 0 12px;overflow-wrap:anywhere}.guest-actions{display:flex;align-items:start;gap:8px;flex-wrap:wrap}.guest-search{display:flex;flex-direction:column;gap:6px;margin:20px 0;max-width:400px}.guest-search input{width:100%;padding:10px;background:var(--surface);color:var(--text);border:1px solid var(--line);border-radius:var(--radius-control);font:inherit}.guest-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,340px),1fr));gap:18px;margin-top:20px}.guest-card{min-width:0;border:1px solid var(--line);border-radius:var(--radius-card);background:var(--surface-soft);padding:22px;overflow-wrap:anywhere}.guest-card h2,.guest-card h3{margin-top:0}.guest-card dl{display:grid;gap:12px}.guest-card dl>div{display:flex;justify-content:space-between;gap:12px;flex-wrap:wrap}.guest-card dt{color:var(--muted)}.guest-card dd{margin:0;text-align:end}.guest-card small{display:block;color:var(--muted)}.guest-error{color:var(--danger)}.guest-services{margin-top:30px}.guest-page button{font:inherit;background:var(--surface);color:var(--text);border:1px solid var(--line);border-radius:var(--radius-control);padding:10px 14px;cursor:pointer}.guest-page button:hover:not(:disabled){border-color:var(--accent)}.guest-page button:active:not(:disabled){background:var(--accent-soft);color:var(--accent)}.guest-page button:disabled{opacity:.5;cursor:not-allowed}@media(max-width:700px){.guest-page{width:calc(100% - 24px);margin:20px auto}.guest-card{padding:16px}}
</style>
