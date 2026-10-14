<!-- Lucian UX/UI Prompt #031 · MIT License (c) 2026 Seokwoooo · https://lucianlabs.dev/uxui-prompt/#031 -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useAttrs, useId, watch } from 'vue'
import { t } from '../i18n'
defineOptions({inheritAttrs:false})
const props=defineProps<{modelValue?:unknown;value?:unknown;modelModifiers?:{number?:boolean}}>()
const emit=defineEmits<{ 'update:modelValue':[value:any];change:[event:Event] }>()
const attrs=useAttrs(),id='select-'+useId(),source=ref<HTMLSelectElement>(),button=ref<HTMLButtonElement>(),list=ref<HTMLUListElement>()
const options=ref<{value:string;label:string;disabled:boolean;selected:boolean}[]>([]),open=ref(false),active=ref(-1),label=ref(''),position=ref<Record<string,string>>({}),invalid=ref(false)
const inheritedDisabled=ref(false)
const inline=computed(()=>Number(attrs.size)>1),value=computed(()=>props.modelValue!==undefined?props.modelValue:props.value)
const disabled=computed(()=>Boolean(attrs.disabled)||inheritedDisabled.value||!options.value.some(option=>!option.disabled))
const caption=computed(()=>options.value.find(option=>option.selected)?.label||t('select.placeholder'))
let observer:MutationObserver|undefined,typed='',typingTimer:ReturnType<typeof setTimeout>|undefined,frame=0,disposed=false
function sync(){
 if(!source.value)return
 inheritedDisabled.value=source.value.matches(':disabled')
 options.value=Array.from(source.value.options).map(option=>({value:option.value,label:option.label,disabled:option.disabled||!!(option.parentElement instanceof HTMLOptGroupElement&&option.parentElement.disabled),selected:option.selected}))
 if((open.value||inline.value)&&(active.value<0||active.value>=options.value.length))active.value=options.value.findIndex(option=>option.selected&&!option.disabled)
 if((open.value||inline.value)&&active.value<0)active.value=options.value.findIndex(option=>!option.disabled)
 const parent=source.value.closest('label')?.cloneNode(true) as HTMLElement|undefined
 parent?.querySelectorAll('.ui-select').forEach(element=>element.remove())
 label.value=String(attrs['aria-label']||parent?.textContent?.trim()||caption.value)
 if(disabled.value)close(false)
}
function place(){
 if(!button.value)return
 const rect=button.value.getBoundingClientRect(),below=innerHeight-rect.bottom-16,above=rect.top-16,up=below<180&&above>below,height=Math.max(46,Math.min(320,up?above:below))
 position.value={position:'fixed',left:Math.max(8,Math.min(rect.left,innerWidth-Math.min(rect.width,innerWidth-16)-8))+'px',width:Math.min(rect.width,innerWidth-16)+'px',maxHeight:height+'px',...(up?{bottom:innerHeight-rect.top+8+'px',transformOrigin:'50% 100%'}:{top:rect.bottom+8+'px',transformOrigin:'50% 0'})}
}
function scrollActive(){void nextTick(()=>list.value?.querySelector<HTMLElement>(`[data-index="${active.value}"]`)?.scrollIntoView({block:'nearest'}))}
async function show(){
 if(disabled.value)return
 sync();active.value=options.value.findIndex(option=>option.selected&&!option.disabled)
 if(active.value<0)active.value=options.value.findIndex(option=>!option.disabled)
 place();open.value=true;await nextTick();list.value?.focus({preventScroll:true});scrollActive()
}
function close(focus=true){open.value=false;if(focus&&!inline.value)button.value?.focus({preventScroll:true})}
function changed(event:Event){
 const option=source.value?.selectedOptions[0] as (HTMLOptionElement&{_value?:unknown})|undefined
 let selected=option&&'_value' in option?option._value:source.value?.value
 if(props.modelModifiers?.number&&typeof selected==='string'&&selected!==''&&!Number.isNaN(Number(selected)))selected=Number(selected)
 invalid.value=false;emit('update:modelValue',selected);emit('change',event);sync()
}
function choose(index:number){
 if(disabled.value||!options.value[index]||options.value[index].disabled||!source.value||source.value.matches(':disabled')){close(false);return}
 source.value.selectedIndex=index;source.value.dispatchEvent(new Event('change',{bubbles:true}));close();if(inline.value)list.value?.focus({preventScroll:true})
}
function move(direction:number){
 let next=active.value+direction
 while(next>=0&&next<options.value.length){if(!options.value[next].disabled){active.value=next;scrollActive();return}next+=direction}
}
function keys(event:KeyboardEvent){
 if(!inline.value&&!open.value){if(['ArrowDown','ArrowUp','Enter',' '].includes(event.key)){event.preventDefault();void show()}return}
 if(['ArrowDown','ArrowUp','Home','End','Enter',' ','Escape'].includes(event.key))event.preventDefault()
 if(event.key==='ArrowDown')move(1)
 else if(event.key==='ArrowUp')move(-1)
 else if(event.key==='Home'){active.value=-1;move(1)}
 else if(event.key==='End'){active.value=options.value.length;move(-1)}
 else if(event.key==='Enter'||event.key===' ')choose(active.value)
 else if(event.key==='Escape'){event.stopPropagation();close()}
 else if(event.key==='Tab')close(false)
 else if(event.key.length===1&&!event.ctrlKey&&!event.metaKey){typed+=event.key.toLocaleLowerCase();clearTimeout(typingTimer);typingTimer=setTimeout(()=>{typed=''},600);const hit=options.value.findIndex(option=>!option.disabled&&option.label.toLocaleLowerCase().startsWith(typed));if(hit>=0){active.value=hit;scrollActive()}}
}
function outside(event:PointerEvent){if(open.value&&!button.value?.contains(event.target as Node)&&!list.value?.contains(event.target as Node))close(false)}
function viewport(event:Event){if(open.value&&!(event.target instanceof Node&&list.value?.contains(event.target)))close(false)}
watch(value,()=>void nextTick(sync),{flush:'post'})
onMounted(()=>{
 sync();active.value=options.value.findIndex(option=>option.selected)
 observer=new MutationObserver(()=>{cancelAnimationFrame(frame);frame=requestAnimationFrame(()=>{if(!disposed)sync()})});if(source.value){observer.observe(source.value,{subtree:true,childList:true,characterData:true,attributes:true});const fieldset=source.value.closest('fieldset');if(fieldset)observer.observe(fieldset,{attributes:true,attributeFilter:['disabled']})}
 document.addEventListener('pointerdown',outside);window.addEventListener('resize',viewport);window.addEventListener('scroll',viewport,true)
})
onBeforeUnmount(()=>{disposed=true;list.value?.remove();observer?.disconnect();clearTimeout(typingTimer);cancelAnimationFrame(frame);document.removeEventListener('pointerdown',outside);window.removeEventListener('resize',viewport);window.removeEventListener('scroll',viewport,true)})
</script>
<template>
 <span class="ux ui-select u031" :class="{'is-open':open,'is-inline':inline}" :style="attrs.style as any">
  <button v-if="!inline" ref="button" class="u031-btn" type="button" role="combobox" :aria-label="label" aria-haspopup="listbox" :aria-expanded="open" :aria-controls="id" :aria-invalid="invalid||undefined" :aria-describedby="attrs['aria-describedby'] as string" :disabled="disabled" @click="open?close():show()" @keydown="keys"><span class="u031-v">{{ caption }}</span><svg class="u031-car" viewBox="0 0 18 18" aria-hidden="true"><path d="M4.5 7l4.5 4.5L13.5 7"/></svg></button>
  <select ref="source" v-bind="attrs" class="ui-select-source" :value="value" tabindex="-1" aria-hidden="true" @change="changed" @invalid.prevent="invalid=true;button?.focus()"><slot /></select>
  <Teleport to="body" :disabled="inline">
   <Transition name="select-popover">
    <ul v-if="open||inline" :id="id" ref="list" class="ux u031-list" :class="{'is-inline':inline}" :style="inline?undefined:position" role="listbox" tabindex="0" :aria-label="label" :aria-activedescendant="active>=0?id+'-'+active:undefined" @keydown="keys" @focus="inline&&sync()" @blur="!list?.contains($event.relatedTarget as Node)&&close(false)">
     <li v-for="(option,index) in options" :id="id+'-'+index" :key="index" class="u031-o" :data-index="index" :class="{'is-active':active===index}" role="option" :aria-selected="option.selected" :aria-disabled="option.disabled||undefined" @pointermove="!option.disabled&&(active=index)" @mousedown.prevent @click.stop="choose(index)"><span>{{ option.label }}</span><svg class="u031-ck" viewBox="0 0 18 18" aria-hidden="true"><path d="M4 9.5l3.5 3.5L14 6"/></svg></li>
    </ul>
   </Transition>
  </Teleport>
 </span>
</template>
<style>
.ui-select.u031{position:relative;display:block;min-width:0;width:100%;font:inherit;text-align:left}
.ui-select .ui-select-source{position:absolute!important;inset:0!important;width:100%!important;height:100%!important;opacity:0!important;pointer-events:none!important;margin:0!important}
.ui-select .u031-btn{display:flex;align-items:center;gap:12px;width:100%;min-height:44px;padding:9px 12px;border:1px solid var(--control-border);border-radius:var(--radius-control);background:var(--control-bg);color:var(--ux-ink);font:inherit;font-size:13px;text-align:left;transition:box-shadow .25s var(--ux-ease),background-color .25s}
.ui-select .u031-btn:focus-visible,.ui-select.is-open .u031-btn{outline:2px solid var(--focus);outline-offset:3px;background:var(--control-bg);box-shadow:none}
.ui-select .u031-btn[aria-invalid=true]{border-color:var(--danger)}
.ui-select .u031-btn:disabled{cursor:not-allowed;background:var(--control-disabled);color:var(--muted);opacity:1}
.u031-v{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.u031-car{width:18px;height:18px;flex:none;fill:none;stroke:var(--ux-ink-3);stroke-width:2.2;stroke-linecap:round;stroke-linejoin:round;transition:transform .45s var(--ux-spring)}
.u031-btn[aria-expanded=true] .u031-car{transform:rotate(180deg)}
.u031-list{z-index:11000;margin:0;padding:6px;list-style:none;border:1px solid var(--ux-line-2);border-radius:var(--radius-card);background:var(--ux-surface);color:var(--ux-ink);box-shadow:var(--overlay-shadow);outline:none;overflow:auto;overscroll-behavior:contain;font-size:13px}
.u031-list.is-inline{position:relative;max-height:26rem;min-height:12rem;box-shadow:none;width:100%}
.u031-list:focus-visible{outline:2px solid var(--focus);outline-offset:3px}
.u031-o{position:relative;display:flex;align-items:center;gap:12px;min-height:46px;padding:8px 10px;border-radius:var(--radius-control);cursor:pointer;transition:background-color .2s var(--ux-ease)}
.u031-o>span{min-width:0;flex:1;overflow-wrap:anywhere}
.u031-o.is-active{background:var(--surface-hover)}
.u031-o[aria-selected=true]{color:var(--ux-accent);background:var(--accent-soft)}
.u031-o[aria-disabled=true]{opacity:.45;cursor:not-allowed}
.u031-ck{margin-left:auto;width:18px;height:18px;flex:none;fill:none;stroke:var(--ux-accent);stroke-width:2.6;stroke-linecap:round;stroke-linejoin:round;opacity:0;transform:scale(.4);transition:opacity .2s,transform .4s var(--ux-spring)}
.u031-o[aria-selected=true] .u031-ck{opacity:1;transform:none}
.select-popover-enter-active{transition:opacity .2s var(--ux-ease),transform .45s var(--ux-spring)}
.select-popover-leave-active{transition:opacity .2s var(--ux-ease),transform .4s var(--ux-spring)}
.select-popover-enter-from,.select-popover-leave-to{opacity:0;transform:translateY(-6px) scaleY(.92)}
@media(prefers-reduced-motion:reduce){.ui-select *, .u031-list, .u031-list *{transition:none!important}}
</style>
