<script setup lang="ts">
import {computed,ref,watch} from 'vue'
import {t,locale} from '../i18n'
import type {Metric,MetricsView} from '../metrics-contract'
const props=defineProps<{metrics?:MetricsView;online:boolean;currentTime:number}>()
const mode=ref<'network'|'io'>('network')
const loadOpen=ref(false)
type Point={at:number;network:[number|null,number|null];io:[number|null,number|null]}
const samples=ref<Point[]>([]);let identity='',last=0
function number(value:unknown){return typeof value==='number'&&Number.isFinite(value)&&value>=0?value:null}
function pair(metric:Metric<any>|undefined,keys:[string,string]):[number|null,number|null]{return usable(metric)&&metric?.value?[number(metric.value[keys[0]]),number(metric.value[keys[1]])]:[null,null]}
watch(()=>props.metrics,value=>{
 if(!value)return
 const next=[value.generation,value.metrics.uptime.value?.bootId??value.bootId??''].join(':')
 if(identity!==next){identity=next;samples.value=[];last=0}
 const at=Date.parse(value.collectedAt);if(!Number.isFinite(at)||at<=last)return;last=at
 samples.value=[...samples.value,{at,network:pair(value.metrics.network.summary,['receivedBytesPerSecond','sentBytesPerSecond']),io:pair(value.metrics.host?.diskIO,['readBytesPerSecond','writeBytesPerSecond'])}].slice(-60)
},{immediate:true})
const mount=computed(()=>props.metrics?.metrics.disk.mounts.find(m=>m.mountpoint==='/')??props.metrics?.metrics.disk.mounts[0])
function usable(metric:Metric<any>|undefined,ttl=(props.metrics?.metrics.host?.fastIntervalSeconds??5)*3000){const received=Date.parse(props.metrics?.receivedAt??'');const age=(metric?.sampleAgeMillis??0)+(Number.isFinite(received)?Math.max(0,props.currentTime-received):0);return props.online&&metric?.status==='known'&&metric.value!=null&&age<=ttl}
function bytes(n:number|undefined|null){if(n==null||!Number.isFinite(n))return '—';const units=['B','KiB','MiB','GiB','TiB'];let i=0;while(n>=1024&&i<4){n/=1024;i++}return `${n.toFixed(i?1:0)} ${units[i]}`}
const loadPercent=computed(()=>{
 const load=props.metrics?.metrics.host?.load,cores=props.metrics?.metrics.cpu.logicalCores
 if(!usable(load)||!usable(cores))return null
 const one=number(load?.value?.one),count=number(cores?.value)
 return one===null||!count?null:one/(count*1.5)*100
})
const loadAverages=computed(()=>{
 const load=props.metrics?.metrics.host?.load
 return ['one','five','fifteen'].map(key=>usable(load)?number(load?.value?.[key as 'one'|'five'|'fifteen']):null)
})
const rings=computed(()=>[
 {key:'load',label:t('overview.load'),value:loadPercent.value,detail:loadPercent.value===null?'—':loadPercent.value<30?t('overview.loadLow'):loadPercent.value<70?t('overview.loadNormal'):loadPercent.value<80?t('overview.loadMedium'):t('overview.loadHigh')},
 {key:'cpu',label:'CPU',value:usable(props.metrics?.metrics.cpu.usagePercent)?number(props.metrics?.metrics.cpu.usagePercent.value):null,detail:props.metrics?.metrics.cpu.logicalCores.value?`${props.metrics.metrics.cpu.logicalCores.value} ${t('overview.cores')}`:'—'},
 {key:'memory',label:t('overview.memory'),value:usable(props.metrics?.metrics.memory)?number(props.metrics?.metrics.memory.value?.usedPercent):null,detail:`${bytes(props.metrics?.metrics.memory.value?.usedBytes)} / ${bytes(props.metrics?.metrics.memory.value?.totalBytes)}`},
 {key:'disk',label:t('overview.disk'),value:usable(mount.value?.usage,(props.metrics?.metrics.host?.diskIntervalSeconds??30)*3000)?number(mount.value?.usage.value?.usedPercent):null,detail:`${bytes(mount.value?.usage.value?.usedBytes)} / ${bytes(mount.value?.usage.value?.totalBytes)}`}
])
const values=computed(()=>samples.value.map(p=>p[mode.value]))
const max=computed(()=>Math.max(1,...values.value.flatMap(p=>p.filter((n):n is number=>n!==null))))
const fresh=computed(()=>usable(mode.value==='network'?props.metrics?.metrics.network.summary:props.metrics?.metrics.host?.diskIO,(props.metrics?.metrics.host?.fastIntervalSeconds??5)*3000))
const latest=computed(()=>fresh.value?values.value.at(-1)??[null,null]:[null,null])
const labels=computed(()=>mode.value==='network'?[t('overview.download'),t('overview.upload')]:[t('overview.read'),t('overview.write')])
function point(index:number,value:number){const first=samples.value[0]?.at??0,last=samples.value.at(-1)?.at??0;return [last>first?8+(samples.value[index]!.at-first)/(last-first)*484:8,88-value/max.value*76]}
function path(series:number){let output='',active=false;values.value.forEach((p,index)=>{const value=p[series];if(value==null){active=false;return}const [x,y]=point(index,value);output+=`${active?'L':'M'}${x!.toFixed(1)},${y!.toFixed(1)} `;active=true});return output}
function clock(at?:number){return at?new Date(at).toLocaleTimeString(locale.value,{hour12:false,hour:'2-digit',minute:'2-digit',second:'2-digit'}):'—'}
</script>
<template>
 <div class="vps-telemetry" :data-state="fresh?'live':'offline'">
  <div class="resource-rings" :aria-label="t('overview.resources')">
   <div v-for="ring in rings" :key="ring.key" class="resource-reading" :data-resource="ring.key">
    <div class="ring" role="img" :aria-label="`${ring.label} ${ring.value===null?t('overview.noData'):ring.value.toFixed(1)+'%'}`">
     <svg viewBox="0 0 80 80" aria-hidden="true"><circle class="ring-track" cx="40" cy="40" r="34"/><circle v-if="ring.value!==null" class="ring-value" :class="{'is-high':ring.value>=(ring.key==='load'?80:90)}" cx="40" cy="40" r="34" pathLength="100" :stroke-dasharray="`${Math.min(100,ring.value)} 100`"/></svg>
     <strong>{{ring.value===null?'—':ring.value.toFixed(1)+'%'}}</strong>
    </div>
    <button v-if="ring.key==='load'" class="load-trigger" type="button" :aria-expanded="loadOpen" :aria-label="t('overview.loadDetails')" @click="loadOpen=!loadOpen" @keydown.esc.stop.prevent="loadOpen=false">{{ring.label}}</button><span v-else>{{ring.label}}</span><small>{{ring.detail}}</small>
    <div v-if="ring.key==='load'&&loadOpen" class="load-details" role="region" :aria-label="t('overview.loadDetails')" @keydown.esc.stop.prevent="loadOpen=false">
     <dl><template v-for="(value,index) in loadAverages" :key="index"><dt>{{t('overview.loadAverage',{'0':[1,5,15][index]})}}</dt><dd>{{value===null?'—':value.toFixed(2)}}</dd></template></dl><p>{{t('overview.loadFormula')}}</p><button type="button" @click="loadOpen=false">{{t('overview.close')}}</button>
    </div>
   </div>
  </div>
  <div class="live-monitor">
   <header><div class="monitor-tabs" :aria-label="t('overview.monitor')"><button type="button" :aria-pressed="mode==='network'" @click="mode='network'">{{t('overview.network')}}</button><button type="button" :aria-pressed="mode==='io'" @click="mode='io'">{{t('overview.io')}}</button></div><span v-if="!online" class="monitor-state">{{t('overview.offline')}}</span><span v-else-if="!fresh&&values.some(p=>p.some(v=>v!==null))" class="monitor-state">{{t('overview.stale')}}</span><span v-else-if="!values.some(p=>p.some(v=>v!==null))" class="monitor-state">{{t('overview.waitSamples')}}</span></header>
   <div class="monitor-values"><span v-for="(label,index) in labels" :key="label" :data-series="index">{{label}} <strong>{{latest[index]==null?'—':bytes(latest[index])+'/s'}}</strong></span></div>
   <svg class="rate-chart" viewBox="0 0 500 100" preserveAspectRatio="none" role="img" :aria-label="`${t('overview.monitor')} · ${labels.join(' / ')}`" :data-points="samples.length"><title>{{t('overview.liveOnly')}}</title><path class="chart-grid" d="M8 12H492 M8 50H492 M8 88H492"/><path v-for="index in [0,1]" :key="index" :d="path(index)" :data-series="index" class="chart-line"/><template v-if="samples.length===1"><circle v-for="index in [0,1]" v-show="latest[index]!=null" :key="index" :cx="8" :cy="latest[index]==null?88:point(0,latest[index]!)[1]" r="2.5" :data-series="index" class="chart-dot"/></template></svg>
   <footer><span>{{clock(samples[0]?.at)}}</span><span>{{t('overview.liveOnly')}}</span><span>{{clock(samples.at(-1)?.at)}}</span></footer>
  </div>
 </div>
</template>
<style scoped>
.vps-telemetry{display:grid;grid-template-columns:minmax(340px,1.2fr) minmax(220px,1fr);align-items:center;gap:20px;margin:12px 0}.resource-rings{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:8px}.resource-reading{position:relative;display:flex;flex-direction:column;align-items:center;text-align:center;gap:4px;min-width:0;font-size:12px}.ring{width:76px;height:76px;position:relative}.ring svg{width:100%;height:100%;transform:rotate(-90deg)}.ring circle{fill:none;stroke-width:5}.ring-track{stroke:var(--line)}.ring-value{stroke:var(--accent);stroke-linecap:round;transition:stroke-dasharray .4s ease}.ring-value.is-high{stroke:var(--warning)}.ring strong{position:absolute;inset:0;display:grid;place-items:center;font-size:16px;font-weight:500;font-variant-numeric:tabular-nums}.resource-reading small{max-width:100%;text-wrap:balance;font-size:10px;color:var(--muted);line-height:1.5;overflow-wrap:anywhere}.live-monitor{min-width:0}.live-monitor header{display:flex;justify-content:space-between;align-items:center;gap:8px}.monitor-tabs{display:flex;gap:4px}.monitor-tabs button{font:inherit;font-size:12px;padding:4px 8px;min-height:30px;background:transparent;border:1px solid transparent;color:var(--muted-strong);border-radius:var(--radius-control)}.monitor-tabs button[aria-pressed=true]{background:var(--accent-soft);color:var(--accent)}.monitor-tabs button:hover{background:var(--surface-hover)}.monitor-tabs button:focus-visible{outline:2px solid var(--focus);outline-offset:2px}.monitor-state{font-size:11px;color:var(--muted)}.monitor-values{display:flex;flex-wrap:wrap;gap:6px 16px;font-size:11px;margin-top:5px;color:var(--muted-strong)}.monitor-values strong{font-weight:500;font-variant-numeric:tabular-nums}.monitor-values span::before{content:'';display:inline-block;width:6px;height:6px;border-radius:50%;background:var(--accent);margin-right:5px}.monitor-values span[data-series='1']::before{background:var(--success)}.rate-chart{width:100%;height:76px;display:block;overflow:visible}.chart-grid{stroke:var(--line);stroke-width:.6;fill:none;stroke-dasharray:3 4}.chart-line{fill:none;stroke:var(--accent);stroke-width:1.8;vector-effect:non-scaling-stroke;stroke-linejoin:round}.chart-line[data-series='1']{stroke:var(--success)}.chart-dot{fill:var(--accent)}.chart-dot[data-series='1']{fill:var(--success)}.live-monitor footer{display:flex;justify-content:space-between;gap:8px;color:var(--muted);font-size:10px;font-variant-numeric:tabular-nums}.vps-telemetry[data-state=offline] .chart-line{opacity:.4}@container vps-card (max-width:620px){.vps-telemetry{grid-template-columns:minmax(0,1fr);gap:12px}.ring{width:62px;height:62px}.ring strong{font-size:14px}.rate-chart{height:68px}}.vps-telemetry .resource-rings .resource-reading[data-resource='load'] .load-trigger{min-height:18px;height:18px;border:0;background:transparent;color:var(--text);font:inherit;line-height:18px;padding:0 3px;cursor:pointer;text-decoration:none}.load-trigger::before{content:'';position:absolute;inset:0}.load-trigger:focus-visible{outline:2px solid var(--focus);outline-offset:3px}.load-details{position:absolute;left:0;top:calc(100% + 8px);z-index:10;width:280px;max-width:calc(100vw - 64px);padding:14px;background:var(--surface);border:1px solid var(--line-strong);border-radius:var(--radius-card);box-shadow:0 8px 24px #0002;text-align:left}.load-details dl{display:grid;grid-template-columns:1fr auto;gap:8px;margin:0;font-size:12px}.load-details dd{margin:0;font-variant-numeric:tabular-nums}.load-details p{font-size:11px;color:var(--muted-strong);line-height:1.6}.load-details button{font:inherit;font-size:12px;background:var(--control-bg);color:var(--text);border:1px solid var(--control-border);border-radius:var(--radius-control);padding:5px 9px}.load-details button:focus-visible{outline:2px solid var(--focus);outline-offset:2px}@container vps-card (max-width:340px){.ring{width:54px;height:54px}.resource-rings{gap:4px}.ring strong{font-size:12px}}@media(prefers-reduced-motion:reduce){.ring-value{transition:none}}
</style>
