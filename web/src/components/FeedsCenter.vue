<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, watch } from 'vue'
import { operationRequest } from '../api'
import { t, display } from '../i18n'
interface Feed { id:string; name:string; url:string; intervalMinutes:number; enabled:boolean; keywords:string[]; channelIds:string[]; notificationMode:string; digestMinutes:number; revision:number; status:string; error:string; lastSuccess:string; nextCheck:string }
interface Item { id:string; title:string; summary:string; url:string; published?:string; firstSeen:string; read:boolean }
interface Delivery { id:string; channelId:string; status:string; attempts:number; error:string; created:string }
interface Channel { id:string; name:string; enabled:boolean }
interface Options { intervalMinutes:number[]; defaultIntervalMinutes:number; minimumIntervalMinutes:number; maximumIntervalMinutes:number }
const feeds=ref<Feed[]>([]),items=ref<Item[]>([]),deliveries=ref<Delivery[]>([]),channels=ref<Channel[]>([]),options=ref<Options>()
const selected=ref(''),editing=ref(''),draft=ref({name:'',url:'',intervalMinutes:30,enabled:true,keywords:[] as string[],channelIds:[] as string[],notificationMode:'off',digestMinutes:60,revision:0})
const keywordText=ref(''),search=ref(''),unread=ref(false),offset=ref(0),error=ref(''),busy=ref(false),removeConfirmed=ref(false)
let stopped=false,epoch=0,timer:ReturnType<typeof setTimeout>|undefined
const statusKeys:Record<string,string>={queued:'feeds.queued',running:'feeds.running',succeeded:'feeds.succeeded',failed:'feeds.failed',canceled:'feeds.canceled',inactive:'feeds.inactive',retry:'feeds.retrying',sending:'feeds.sending'}
function statusLabel(value:string){return statusKeys[value]?t(statusKeys[value]!):value}
function message(e:unknown){return e instanceof Error?e.message:String(e)}
function reset(){editing.value='';keywordText.value='';draft.value={name:'',url:'',intervalMinutes:options.value?.defaultIntervalMinutes??30,enabled:true,keywords:[],channelIds:[],notificationMode:'off',digestMinutes:60,revision:0}}
function edit(feed:Feed){editing.value=feed.id;draft.value={name:feed.name,url:feed.url,intervalMinutes:feed.intervalMinutes,enabled:feed.enabled,keywords:[...feed.keywords],channelIds:[...feed.channelIds],notificationMode:feed.notificationMode,digestMinutes:feed.digestMinutes,revision:feed.revision};keywordText.value=feed.keywords.join('\n')}
async function reload(){const current=epoch;try{const result=await operationRequest<{subscriptions:Feed[]}>('feeds.list');if(stopped||current!==epoch)return;feeds.value=result.subscriptions;if(selected.value&&!feeds.value.some(f=>f.id===selected.value)){selected.value='';items.value=[];deliveries.value=[]};if(!selected.value&&feeds.value.length)selected.value=feeds.value[0]!.id}catch(e){if(!stopped&&current===epoch)error.value=message(e)}}
async function page(){if(!selected.value)return;const current=epoch,id=selected.value;try{const [entries,history]=await Promise.all([operationRequest<{items:Item[]}>('feeds.items',{params:{id},query:{offset:String(offset.value),limit:"50",search:search.value,unread:String(unread.value)}}),operationRequest<{deliveries:Delivery[]}>('feeds.deliveries',{params:{id}})]);if(stopped||current!==epoch||id!==selected.value)return;items.value=entries.items;deliveries.value=history.deliveries}catch(e){if(current===epoch&&!stopped)error.value=message(e)}}
async function poll(){await reload();await page();if(!stopped)timer=setTimeout(poll,5000)}
async function perform(call:()=>Promise<unknown>){if(busy.value)return;busy.value=true;epoch++;error.value='';try{await call();await reload();await page()}catch(e){error.value=message(e)}finally{busy.value=false}}
async function save(){await perform(async()=>{const result=await operationRequest<Feed>(editing.value?'feeds.update':'feeds.create',editing.value?{params:{id:editing.value}}:{},{body:JSON.stringify({...draft.value,keywords:keywordText.value.split('\n').map(v=>v.trim()).filter(Boolean)})});selected.value=result.id;reset()})}
async function action(feed:Feed,verb:string){await perform(()=>operationRequest('feeds.'+verb,{params:{id:feed.id}}))}
async function toggle(feed:Feed){await perform(()=>operationRequest('feeds.update',{params:{id:feed.id}},{body:JSON.stringify({name:feed.name,url:feed.url,intervalMinutes:feed.intervalMinutes,enabled:!feed.enabled,keywords:feed.keywords,channelIds:feed.channelIds,notificationMode:feed.notificationMode,digestMinutes:feed.digestMinutes,revision:feed.revision})}))}
async function remove(feed:Feed){await perform(async()=>{await operationRequest('feeds.delete',{params:{id:feed.id}},{body:JSON.stringify({revision:feed.revision,confirm:true})});removeConfirmed.value=false;if(editing.value===feed.id)reset()})}
async function read(item:Item){await perform(()=>operationRequest('feeds.read',{params:{id:selected.value,itemId:item.id}},{body:JSON.stringify({read:!item.read})}))}
watch([selected,unread],()=>{epoch++;offset.value=0;removeConfirmed.value=false;void page()})
onMounted(async()=>{try{options.value=await operationRequest<Options>('feeds.options');draft.value.intervalMinutes=options.value.defaultIntervalMinutes;channels.value=(await operationRequest<{channels:Channel[]}>('channels.list')).channels}catch(e){error.value=message(e)};if(!stopped)void poll()})
onBeforeUnmount(()=>{stopped=true;epoch++;if(timer)clearTimeout(timer)})
</script>
<template>
<section class="feeds-center" data-testid="feeds-center">
 <header><p class="eyebrow">NODEDANCE</p><h1>{{t('feeds.title')}}</h1><p class="muted">{{t('feeds.description')}}</p></header>
 <p v-if="error" role="alert" class="feed-error">{{display(error)}}</p>
 <form class="feed-card feed-form" @submit.prevent="save">
  <h2>{{t(editing?'feeds.edit':'feeds.add')}}</h2>
  <label>{{t('feeds.name')}}<input v-model="draft.name" name="feedName" required></label>
  <label>{{t('feeds.url')}}<input v-model="draft.url" name="feedUrl" type="url" required><small class="muted">{{t('feeds.publicOnly')}}</small></label>
  <label>{{t('feeds.interval')}}<UiSelect v-model="draft.intervalMinutes" name="feedInterval"><option v-for="minutes in options?.intervalMinutes" :key="minutes" :value="minutes">{{minutes}} {{t('feeds.minutes')}}</option><option v-if="options&&!options.intervalMinutes.includes(draft.intervalMinutes)" :value="draft.intervalMinutes">{{draft.intervalMinutes}} {{t('feeds.minutes')}}</option></UiSelect><input v-model.number="draft.intervalMinutes" name="feedCustomInterval" type="number" :min="options?.minimumIntervalMinutes" :max="options?.maximumIntervalMinutes" :aria-label="t('feeds.customInterval')"></label>
  <label class="check"><input v-model="draft.enabled" type="checkbox">{{t('feeds.enabled')}}</label>
  <label>{{t('feeds.keywords')}}<textarea v-model="keywordText" name="feedKeywords" rows="3"/><small class="muted">{{t('feeds.keywordHint')}}</small></label>
  <label>{{t('feeds.notifications')}}<UiSelect v-model="draft.notificationMode" name="feedNotificationMode"><option value="off">{{t('feeds.off')}}</option><option value="immediate">{{t('feeds.immediate')}}</option><option value="digest">{{t('feeds.digest')}}</option></UiSelect></label>
  <label v-if="draft.notificationMode==='digest'">{{t('feeds.digestInterval')}}<input v-model.number="draft.digestMinutes" type="number" :min="options?.minimumIntervalMinutes" :max="options?.maximumIntervalMinutes"></label>
  <fieldset v-if="draft.notificationMode!=='off'"><legend>{{t('feeds.channels')}}</legend><label v-for="channel in channels.filter(c=>c.enabled)" :key="channel.id" class="check"><input v-model="draft.channelIds" type="checkbox" :value="channel.id">{{channel.name}}</label><p v-if="!channels.some(c=>c.enabled)" class="muted">{{t('feeds.noChannels')}}</p></fieldset>
  <div class="feed-actions"><button class="primary-button" :disabled="busy||!options">{{t('feeds.save')}}</button><button v-if="editing" type="button" class="secondary-button" @click="reset">{{t('feeds.cancelEdit')}}</button></div>
 </form>
 <div class="feed-columns">
  <aside class="feed-card"><h2>{{t('feeds.subscriptions')}}</h2><p v-if="!feeds.length" class="muted">{{t('feeds.empty')}}</p>
   <article v-for="feed in feeds" :key="feed.id" class="feed-subscription">
    <button class="feed-select" :aria-pressed="selected===feed.id" @click="selected=feed.id">{{feed.name}}</button>
    <p class="muted">{{feed.intervalMinutes}} {{t('feeds.minutes')}} · {{statusLabel(feed.status)}}</p><p v-if="feed.error" class="feed-error">{{display(feed.error)}}</p>
    <p class="muted">{{t('feeds.lastSuccess')}}: {{feed.lastSuccess.startsWith('0001')?'—':new Date(feed.lastSuccess).toLocaleString()}}</p><p class="muted">{{t('feeds.nextCheck')}}: {{new Date(feed.nextCheck).toLocaleString()}}</p>
    <div class="feed-actions"><button class="secondary-button" :disabled="busy" @click="edit(feed)">{{t('feeds.edit')}}</button><button class="secondary-button" :disabled="busy||!feed.enabled" @click="action(feed,'refresh')">{{t('feeds.refresh')}}</button><button v-if="['queued','running'].includes(feed.status)" class="secondary-button" :disabled="busy" @click="action(feed,'cancel')">{{t('feeds.cancelRefresh')}}</button><button class="secondary-button" :disabled="busy" @click="toggle(feed)">{{t(feed.enabled?'feeds.disable':'feeds.enable')}}</button></div>
    <template v-if="selected===feed.id"><label class="check"><input v-model="removeConfirmed" type="checkbox">{{t('feeds.deleteConfirm')}}</label><button class="secondary-button feed-delete" :disabled="busy||!removeConfirmed" @click="remove(feed)">{{t('feeds.delete')}}</button></template>
   </article>
  </aside>
  <div class="feed-card"><h2>{{t('feeds.entries')}}</h2>
   <form class="feed-actions" @submit.prevent="offset=0;epoch++;page()"><input v-model="search" type="search" :aria-label="t('feeds.search')"><button class="secondary-button">{{t('feeds.search')}}</button><label class="check"><input v-model="unread" type="checkbox">{{t('feeds.unread')}}</label></form>
   <p v-if="!items.length" class="muted">{{t('feeds.noEntries')}}</p>
   <article v-for="item in items" :key="item.id" class="feed-entry"><h3>{{item.title}}</h3><p class="muted">{{t(item.published?'feeds.published':'feeds.observed')}}: {{new Date(item.published||item.firstSeen).toLocaleString()}}</p><p>{{item.summary}}</p><div class="feed-actions"><a v-if="item.url" :href="item.url" target="_blank" rel="noopener noreferrer" referrerpolicy="no-referrer">{{t('feeds.original')}}</a><button class="secondary-button" :disabled="busy" @click="read(item)">{{t(item.read?'feeds.markUnread':'feeds.markRead')}}</button></div></article>
   <div class="feed-actions"><button class="secondary-button" :disabled="offset===0" @click="offset=Math.max(0,offset-50);epoch++;page()">{{t('feeds.previous')}}</button><button class="secondary-button" :disabled="items.length<50" @click="offset+=50;epoch++;page()">{{t('feeds.next')}}</button></div>
   <h2 class="delivery-title">{{t('feeds.deliveries')}}</h2><article v-for="delivery in deliveries" :key="delivery.id" class="feed-entry"><p>{{statusLabel(delivery.status)}} · {{delivery.attempts}} · {{new Date(delivery.created).toLocaleString()}}</p><p v-if="delivery.error" class="feed-error">{{display(delivery.error)}}</p><button v-if="delivery.status==='failed'" class="secondary-button" :disabled="busy" @click="perform(()=>operationRequest('feeds.retry-delivery',{params:{id:selected,deliveryId:delivery.id}}))">{{t('feeds.retry')}}</button></article>
  </div>
 </div>
</section>
</template>
<style scoped>
.feeds-center{display:grid;gap:24px;max-width:1200px;margin:auto;padding:32px}.feeds-center h1{font-family:var(--display-font);font-weight:400;font-size:36px;margin:8px 0 16px}.feeds-center h2{font-family:var(--display-font);font-weight:400;margin:0 0 20px}.feed-card{padding:24px;border:1px solid var(--line);border-radius:var(--radius-card);background:var(--surface);min-width:0}.feed-form{display:grid;grid-template-columns:1fr 1fr;gap:16px}.feed-form h2,.feed-form>.feed-actions{grid-column:1/-1}.feed-form label{display:grid;gap:8px}.feed-form fieldset{border:1px solid var(--line);border-radius:var(--radius-control)}.feed-columns{display:grid;align-items:start;grid-template-columns:minmax(260px,1fr) minmax(0,2fr);gap:24px}.feed-actions{display:flex;flex-wrap:wrap;align-items:center;gap:12px}.feeds-center :is(input:not([type=checkbox]),select,textarea){width:100%;min-height:40px;padding:10px 12px;background:var(--canvas);color:var(--text);border:1px solid var(--line);border-radius:var(--radius-control);font:inherit}.feed-actions>input{flex:1;min-width:120px}.feeds-center .check{display:flex;align-items:center;gap:8px;min-height:40px}.feeds-center input[type=checkbox]{accent-color:var(--accent)}.feeds-center :is(input,select,textarea,button,a):focus-visible{outline:2px solid var(--accent);outline-offset:3px}.feeds-center :disabled{opacity:.5;cursor:not-allowed}.feeds-center input:read-only{background:var(--surface);color:var(--muted)}.feed-select{border:0;background:transparent;color:var(--text);font:inherit;font-weight:600;text-align:left;padding:8px;cursor:pointer;border-radius:var(--radius-control)}.feed-select[aria-pressed=true]{background:var(--accent-soft);color:var(--accent)}.feed-select:active{background:var(--accent-soft)}.feed-subscription,.feed-entry{border-top:1px solid var(--line);padding:20px 0;overflow-wrap:anywhere}.feed-entry h3{margin:0;font-size:18px}.feed-entry p{line-height:1.6}.feeds-center a{color:var(--accent);text-decoration:underline;text-underline-offset:3px}.feeds-center a:visited{color:var(--muted)}.feeds-center a:active{color:var(--text)}.feeds-center ::selection{background:var(--accent-soft);color:var(--text)}.muted{color:var(--muted);font-size:14px}.feed-error,.feed-delete{color:var(--danger)}.delivery-title{margin-top:32px!important}@media(max-width:768px){.feeds-center{padding:16px}.feed-columns,.feed-form{grid-template-columns:1fr}.feed-card{padding:20px}.feeds-center h1{font-size:32px}}
</style>
