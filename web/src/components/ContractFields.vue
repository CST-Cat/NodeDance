<script setup lang="ts">
import { t } from '../i18n'
import type { BodySchema } from '../operations'
const props = defineProps<{ schema?: BodySchema; values: Record<string, unknown>; omit?: string[] }>()
function update(key: string, schema: BodySchema, event: Event) {
 const element = event.target as HTMLInputElement
 props.values[key] = schema.type === 'boolean' ? element instanceof HTMLInputElement && element.type === 'checkbox' ? element.checked : element.value === 'true' : schema.type === 'integer' ? /^-?\d+$/.test(element.value) ? BigInt(element.value) : element.value : schema.type === 'number' ? Number(element.value) : schema.type === 'array' ? element.value.split('\n').map(x => x.trim()).filter(Boolean) : element.value
}
function label(key: string) { return t(`host.field.${key}`) }
</script>
<template>
 <div class="contract-fields">
  <label v-for="[key, field] in Object.entries(schema?.properties ?? {}).filter(([key]) => !omit?.includes(key))" :key="key" :data-field-type="field.enum ? 'enum' : field.type" :class="{ 'check-row': field.type === 'boolean' }">
   <span>{{ label(key) }}</span>
   <input v-if="field.type === 'boolean'" type="checkbox" :checked="!!values[key]" @change="update(key, field, $event)" />
   <UiSelect v-else-if="field.enum" :value="String(values[key] ?? '')" @change="update(key, field, $event)"><option v-for="option in field.enum" :key="String(option)" :value="String(option)">{{ option === '' ? '—' : option }}</option></UiSelect>
   <textarea v-else-if="field.type === 'array'" rows="3" :value="Array.isArray(values[key]) ? (values[key] as string[]).join('\n') : ''" @input="update(key, field, $event)"></textarea>
   <textarea v-else-if="key === 'script'" rows="6" :value="String(values[key] ?? '')" spellcheck="false" @input="update(key, field, $event)"></textarea>
   <input v-else :type="field.type === 'number' ? 'number' : 'text'" :inputmode="field.type === 'integer' ? 'numeric' : undefined" :step="field.type === 'number' ? 'any' : undefined" :value="values[key] ?? ''" :placeholder="field.format === 'date-time' ? '2026-12-01T00:00:00Z' : undefined" @input="update(key, field, $event)" />
  </label>
 </div>
</template>
<style scoped>
.contract-fields{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,220px),320px));gap:14px}.contract-fields label{display:flex;flex-direction:column;gap:6px;color:var(--muted);font-size:13px;min-width:0}.contract-fields input,.contract-fields textarea,.contract-fields select{width:100%;min-width:0;color:var(--text);background:var(--surface);border:1px solid var(--line);border-radius:var(--radius-control);padding:9px;font:inherit}.contract-fields .check-row{flex-direction:row-reverse;align-items:center;justify-content:flex-end;gap:8px}.check-row input{width:18px;height:18px}.contract-fields textarea{resize:vertical}.contract-fields label[data-field-type=number]>input,.contract-fields label[data-field-type=integer]>input{max-width:160px}.contract-fields label[data-field-type=enum]>.ui-select{max-width:240px}.contract-fields label:has(textarea){max-width:760px}@media(max-width:560px){.contract-fields{grid-template-columns:minmax(0,1fr)}.contract-fields label>input,.contract-fields label>.ui-select{max-width:none}}.contract-fields label:has(textarea){grid-column:1/-1}
</style>
