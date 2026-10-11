<script setup lang="ts">
import { t } from '../i18n'
import type { BodySchema } from '../operations'
const props = defineProps<{ schema?: BodySchema; values: Record<string, unknown>; omit?: string[] }>()
function update(key: string, schema: BodySchema, event: Event) {
 const element = event.target as HTMLInputElement
 props.values[key] = schema.type === 'boolean' ? element.checked : schema.type === 'integer' || schema.type === 'number' ? Number(element.value) : schema.type === 'array' ? element.value.split('\n').map(x => x.trim()).filter(Boolean) : element.value
}
function label(key: string) { return t(`host.field.${key}`) }
</script>
<template>
 <div class="contract-fields">
  <label v-for="[key, field] in Object.entries(schema?.properties ?? {}).filter(([key]) => !omit?.includes(key))" :key="key" :class="{ 'check-row': field.type === 'boolean' }">
   <span>{{ label(key) }}</span>
   <select v-if="field.enum" :value="values[key] ?? ''" @change="update(key, field, $event)"><option v-for="option in field.enum" :key="option" :value="option">{{ option || '—' }}</option></select>
   <input v-else-if="field.type === 'boolean'" type="checkbox" :checked="!!values[key]" @change="update(key, field, $event)" />
   <textarea v-else-if="field.type === 'array'" rows="3" :value="Array.isArray(values[key]) ? (values[key] as string[]).join('\n') : ''" @input="update(key, field, $event)"></textarea>
   <textarea v-else-if="key === 'script'" rows="6" :value="String(values[key] ?? '')" spellcheck="false" @input="update(key, field, $event)"></textarea>
   <input v-else :type="field.type === 'integer' || field.type === 'number' ? 'number' : 'text'" :step="field.type === 'number' ? 'any' : undefined" :value="values[key] ?? ''" :placeholder="field.format === 'date-time' ? '2026-12-01T00:00:00Z' : undefined" @input="update(key, field, $event)" />
  </label>
 </div>
</template>
<style scoped>
.contract-fields{display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:14px}.contract-fields label{display:flex;flex-direction:column;gap:6px;color:var(--muted);font-size:13px;min-width:0}.contract-fields input,.contract-fields textarea,.contract-fields select{width:100%;min-width:0;color:var(--text);background:var(--surface);border:1px solid var(--line);border-radius:var(--radius-control);padding:9px;font:inherit}.contract-fields .check-row{flex-direction:row;align-items:center;justify-content:space-between}.check-row input{width:18px;height:18px}.contract-fields textarea{resize:vertical}.contract-fields label:has(textarea){grid-column:1/-1}
</style>
