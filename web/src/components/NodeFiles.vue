<script setup lang="ts">
import { computed, getCurrentInstance, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import VueFinderPlugin, { VueFinder, type Driver } from 'vuefinder'
import upstreamEn from 'vuefinder/dist/locales/en.js'
import upstreamZhCN from 'vuefinder/dist/locales/zhCN.js'
import 'vuefinder/dist/style.css'
import enMessages from '../locales/en.json'
import zhMessages from '../locales/zh-CN.json'
import { useViewNavigation } from '../navigation'
import { useVueFinder } from 'vuefinder'
import { locale, t } from '../i18n'
import { createFileManager, type FileObservation } from '../file-manager'
const archivePrompt='Archive name. (.zip file will be created)'
const en={...upstreamEn,[archivePrompt]:enMessages['files.archiveName']}
const zhCN={...upstreamZhCN,[archivePrompt]:zhMessages['files.archiveName']}
const app=getCurrentInstance()!.appContext.app
if(!app.component('vue-finder'))app.use(VueFinderPlugin,{i18n:{en,zhCN}})
const props=defineProps<{nodeId:string;nodeName:string;directoryPicker?:boolean}>()
const emit=defineEmits<{directoryReady:[path:string]}>()
const navigation=useViewNavigation(),finderId=`${props.directoryPicker?'compose-directory':'node-files'}-${props.nodeId}`
const ready=ref(false)
function pathChanged(value:string){if(props.directoryPicker)return;const filePath=value.startsWith('node://')?value.slice(7)||'/':value;if(filePath!==(navigation.route.value.filePath??'/'))navigation.go({...navigation.route.value,filePath})}
watch(()=>navigation.route.value.filePath,value=>{if(ready.value&&!props.directoryPicker){const ui=useVueFinder(finderId);const target='node://'+(value??'/');if(ui.getPath()!==target)void ui.open(target)}})
const host=ref<HTMLElement>()
let controls:MutationObserver|undefined,dialog:HTMLElement|null=null,returnFocus:HTMLElement|null=null
function normalizeControls(){const root=document.querySelector<HTMLElement>(`[data-nodedance-files="${finderId}"]`);const current=document.querySelector<HTMLElement>('.vuefinder__modal-layout');if(current!==dialog){if(current){returnFocus=document.activeElement instanceof HTMLElement?document.activeElement:null;current.focus()}else {manager.value?.endPreview();if(returnFocus?.isConnected)returnFocus.focus()}dialog=current}for(const item of root?.querySelectorAll<HTMLElement>('.vuefinder__toolbar [title], .vuefinder__breadcrumb [title]')??[]){if(['BUTTON','A','INPUT','SELECT'].includes(item.tagName))continue;item.setAttribute('role','button');item.setAttribute('aria-label',item.title);const disabled=/disabled|opacity-50/.test(item.className);item.setAttribute('aria-disabled',String(disabled));item.tabIndex=disabled?-1:0;item.dataset.nodedanceControl='true'}}
function activateControl(event:KeyboardEvent){if(props.directoryPicker&&host.value?.contains(event.target as Node)&&(event.code==='Delete'||(event.ctrlKey||event.metaKey)&&['Enter','KeyS','Backslash'].includes(event.code))){event.preventDefault();event.stopImmediatePropagation();return}if(event.key==='Tab'&&dialog){const items=[...dialog.querySelectorAll<HTMLElement>('button:not(:disabled),a[href],input:not(:disabled),select:not(:disabled),textarea:not(:disabled),[tabindex="0"],[contenteditable="true"]')].filter(item=>item.getClientRects().length&&item.tabIndex>=0);const first=items[0],last=items.at(-1);if(!first){event.preventDefault();dialog.focus();return}if(!dialog.contains(document.activeElement)||document.activeElement===dialog||event.shiftKey&&document.activeElement===first||!event.shiftKey&&document.activeElement===last){event.preventDefault();(event.shiftKey?last:first)?.focus()}return}const target=event.target as HTMLElement;if(['Enter',' '].includes(event.key)&&target.dataset.nodedanceControl==='true'){event.preventDefault();event.stopPropagation();if(target.getAttribute('aria-disabled')!=='true')target.click()}}
const manager=ref<Awaited<ReturnType<typeof createFileManager>>>(),error=ref(''),observations=ref<FileObservation[]>([]),theme=ref(document.documentElement.dataset.theme??'light')
const config=computed(()=>({initialPath:'node://'+(props.directoryPicker?'/':navigation.route.value.filePath??'/'),path:'node://'+(props.directoryPicker?'/':navigation.route.value.filePath??'/'),persist:!props.directoryPicker,...(props.directoryPicker?{showMenuBar:false}:{}),maxFileSize:manager.value?.maxFileSize,theme:theme.value==='dark'?'midnight':'latte',showHiddenFiles:true,showThumbnails:false}))

const features=computed(()=>props.directoryPicker?{edit:false,newfile:false,newfolder:true,preview:false,archive:false,unarchive:false,search:false,rename:false,upload:false,delete:false,fullscreen:false,download:false,language:false,move:false,copy:false,history:false,theme:false,pinned:false}:{language:false,theme:false})
const finderDriver=computed(()=>{
 const driver=manager.value?.driver
 if(!driver||!props.directoryPicker)return driver
 const restricted={...driver}
 for(const key of Object.keys(restricted))if(key!=='list'&&key!=='createFolder')Reflect.set(restricted,key,()=>{throw Error(t('compose.directoryOnly'))})
 const completed=(result:Awaited<ReturnType<Driver['list']>>)=>{
  emit('directoryReady',result.dirname.startsWith('node://')?result.dirname.slice(7)||'/':result.dirname)
  return {...result,read_only:result.read_only??false,files:result.files.filter(item=>item.type==='dir')}
 }
 restricted.list=async options=>{emit('directoryReady','');return completed(await driver.list(options))}
 restricted.createFolder=async options=>{emit('directoryReady','');return completed(await driver.createFolder!(options))}
 return restricted
})
let disposed=false,observer:MutationObserver|undefined
onMounted(async()=>{
 document.addEventListener('keydown',activateControl,true)
 controls=new MutationObserver(normalizeControls);if(host.value)controls.observe(document.body,{childList:true,subtree:true,attributes:true,attributeFilter:['class','title']})
 observer=new MutationObserver(()=>{theme.value=document.documentElement.dataset.theme??'light'});observer.observe(document.documentElement,{attributes:true,attributeFilter:['data-theme']})
 try{const value=await createFileManager(props.nodeId,items=>{if(!disposed)observations.value=items});if(disposed)value.dispose();else manager.value=value}catch(reason){if(!disposed)error.value=String(reason)}
})
onBeforeUnmount(()=>{disposed=true;document.removeEventListener('keydown',activateControl,true);observer?.disconnect();controls?.disconnect();manager.value?.dispose()})
async function observe(id:string){try{await manager.value?.observe(id)}catch(reason){if(!disposed)error.value=String(reason)}}
</script>
<template>
 <section ref="host" class="node-files" data-testid="node-files" :aria-label="nodeName">
  <p v-if="error" role="alert">{{ error }}</p>
  <VueFinder v-if="manager" :id="finderId" :data-nodedance-files="finderId" :driver="finderDriver!" :locale="locale==='zh-CN'?'zhCN':'en'" :config="config" :features="features" @notify="$event.type==='success' && (error='')" @error="error=String($event)" @ready="ready=true" @path-change="pathChanged" />
  <p v-else role="status">{{ t('guest.loading') }}</p>
  <section v-if="observations.length && !directoryPicker" class="file-observations" :aria-label="t('host.history')"><article v-for="item in observations" :key="item.taskId"><span>{{ item.path }} · {{ item.status }}</span><button type="button" @click="observe(item.taskId)">{{ t('host.refresh') }}</button><button type="button" @click="manager?.forget(item.taskId)">{{ t('operations.forget') }}</button></article></section>
 </section>
</template>
<style scoped>
.node-files{min-width:0;color:var(--text);border:1px solid var(--line);border-radius:var(--radius-card);overflow:hidden}.node-files>p{padding:16px}.node-files>p[role=alert]{color:var(--danger)}.file-observations{padding:16px;overflow-wrap:anywhere}.file-observations article{display:flex;gap:12px;flex-wrap:wrap;align-items:center;margin:8px 0}.file-observations button{background:var(--surface);color:var(--text);border:1px solid var(--line);border-radius:var(--radius-control);padding:8px;cursor:pointer}
</style>

<style>
.vuefinder__themer{
 --vf-bg-primary:var(--surface)!important;--vf-bg-secondary:var(--surface-soft)!important;--vf-bg-tertiary:var(--surface)!important;--vf-bg-hover:var(--accent-soft)!important;--vf-bg-selected:var(--accent-soft)!important;--vf-bg-disabled:var(--surface-soft)!important;
 --vf-text-primary:var(--text)!important;--vf-text-secondary:var(--muted)!important;--vf-text-muted:var(--muted)!important;--vf-border-primary:var(--line)!important;--vf-border-secondary:var(--line)!important;
 --vf-accent-primary:var(--accent)!important;--vf-accent-hover:var(--accent)!important;--vf-selection-bg:var(--accent-soft)!important;--vf-selection-border:var(--accent)!important;
 --color-blue-500:var(--accent);--color-blue-900:var(--accent);--color-sky-400:var(--accent);--vf-ring:var(--accent)!important;color:var(--text);accent-color:var(--accent)
}
/* Teleported dialogs inherit the shared root theme, above the application shell. */
.vuefinder__modal-layout{z-index:10000}
.vuefinder__modal-layout__overlay{background:rgb(20 20 19 / 55%)}
.vuefinder__modal-layout__body{border:1px solid var(--line);background:var(--canvas);color:var(--text);border-radius:var(--radius-card)}
.vuefinder__modal-layout__content{background:var(--canvas);color:var(--text)}
.vuefinder__codemirror-editor,.vuefinder__codemirror-editor .cm-editor,.vuefinder__codemirror-editor .cm-scroller,.vuefinder__codemirror-editor .cm-content{background:var(--canvas);color:var(--text)}
.vuefinder__codemirror-editor .cm-gutters{background:var(--surface-soft);color:var(--muted-strong)}
.vuefinder__codemirror-editor .cm-activeLineGutter{background:var(--accent-soft);color:var(--text)}
.vuefinder__codemirror-editor--readonly .cm-content{color:var(--text)}
.vuefinder__codemirror-wrap-toggle{background:var(--surface);opacity:1}
@media(max-width:640px){.vuefinder__modal-layout__wrapper{align-items:center;padding:12px}.vuefinder__modal-layout__body{max-height:calc(100dvh - 24px);overflow:auto;margin:0}.vuefinder__codemirror-editor{max-height:65dvh}.vuefinder__modal-layout__footer .vf-btn{min-height:44px}}
.vuefinder__themer a,.vuefinder__themer a:visited{color:var(--accent)}
.vuefinder__themer :focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.vuefinder__themer ::selection{background:var(--accent-soft);color:var(--text)}
</style>
