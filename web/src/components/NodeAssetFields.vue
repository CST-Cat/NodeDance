<script setup lang="ts">
import { computed } from 'vue'
import { t } from '../i18n'
import type { BodySchema } from '../operations'
const props = defineProps<{ values: Record<string, unknown>; schema?: BodySchema; disabled?: boolean }>()
const unitLabels: Record<string,string> = {year:'asset.unit.year',month:'asset.unit.month',hour:'asset.unit.hour',minute:'asset.unit.minute',day:'asset.unit.day'}
const cycle = computed({get: () => String(props.values.billingCycleUnit || 'day'), set: value => { props.values.billingCycleUnit = value }})
const units = computed(() => (props.schema?.properties?.billingCycleUnit?.enum ?? []).filter(value => value !== ''))
const purchase = computed({get: () => props.values.purchasedAt ? String(props.values.purchasedAt).slice(0,10) : '', set: value => { props.values.purchasedAt = value ? value + 'T00:00:00Z' : null }})
const expiry = computed({
 get: () => { if (!props.values.expiresAt) return ''; const date = new Date(String(props.values.expiresAt)); return Number.isNaN(date.getTime()) ? '' : new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16) },
 set: value => { props.values.expiresAt = value ? new Date(value).toISOString() : null },
})
const traffic = computed({get: () => Number(props.values.trafficLimitBytes ?? 0) / 1000000000, set: value => { props.values.trafficLimitBytes = Math.round(Number(value) * 1000000000) }})
</script>
<template>
 <fieldset class="asset-fields" :disabled="disabled" data-testid="node-asset-fields">
  <legend>{{ t('asset.title') }}</legend>
  <div class="asset-grid">
   <label><span>{{ t('asset.monthlyTraffic') }}</span><input v-model="traffic" type="number" min="0" step="any" data-testid="asset-traffic" /><small>{{ t('asset.trafficHint') }}</small></label>
   <label><span>{{ t('asset.purchase') }}</span><input v-model="purchase" type="date" data-testid="asset-purchase" /><small>{{ t('asset.purchaseHint') }}</small></label>
   <label><span>{{ t('asset.expiry') }}</span><input v-model="expiry" type="datetime-local" data-testid="asset-expiry" /><small>{{ t('asset.expiryHint') }}</small></label>
   <label><span>{{ t('asset.price') }}</span><input v-model.number="values.price" type="number" min="0" step="any" data-testid="asset-price" /></label>
   <label><span>{{ t('host.field.currency') }}</span><input v-model="values.currency" type="text" maxlength="8" data-testid="asset-currency" /></label>
   <label><span>{{ t('asset.cycle') }}</span><UiSelect v-model="cycle" data-testid="asset-cycle"><option v-for="unit in units" :key="String(unit)" :value="unit">{{ t(unitLabels[String(unit)] ?? String(unit)) }}</option></UiSelect></label>
   <label v-if="cycle === 'day'"><span>{{ t('host.field.billingCycleDays') }}</span><input v-model.number="values.billingCycleDays" type="number" min="1" max="3660" /></label>
  </div>
 </fieldset>
</template>
<style scoped>
.asset-fields{border:0;border-top:1px solid var(--line);padding:18px 0 0;margin:10px 0;min-width:0;color:var(--text)}.asset-fields legend{padding:0 12px 0 0;font-weight:500}.asset-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px}.asset-grid label{display:flex;flex-direction:column;gap:7px;font-size:13px;min-width:0}.asset-grid input,.asset-grid select{box-sizing:border-box;width:100%;min-width:0;min-height:44px;padding:10px;border:1px solid var(--line);border-radius:var(--radius-control);background:var(--surface);color:var(--text);font:inherit;color-scheme:inherit}.asset-grid small{color:var(--muted-strong);line-height:1.5}.asset-grid input:focus-visible,.asset-grid select:focus-visible{outline:2px solid var(--accent);outline-offset:2px}.asset-fields:disabled{opacity:.65}.asset-grid select:active{background:var(--surface-soft)}@media(max-width:640px){.asset-grid{grid-template-columns:minmax(0,1fr)}}
</style>
