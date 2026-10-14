<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { t, display } from '../i18n'
import { operationRequest } from '../api'
import { resolveOperation, exactJSON, type BodySchema } from '../operations'
import NodeAssetFields from './NodeAssetFields.vue'
const props = defineProps<{ nodeId: string }>()
const asset = ref<Record<string, unknown>>({}), schema = ref<BodySchema>(), loading = ref(true), saving = ref(false), error = ref(''), saved = ref(false)
let epoch = 0
watch(() => props.nodeId, async nodeId => {
 const current = ++epoch; loading.value = true; error.value = ''; saved.value = false
 try {
  const [contract, value] = await Promise.all([resolveOperation('nodes.save-asset', {params:{nodeId}}), operationRequest<Record<string, unknown>>('nodes.asset', {params:{nodeId}})])
  if (current !== epoch) return
  schema.value = contract.operation.bodySchema; asset.value = value
 } catch(reason) { if(current === epoch) error.value = String(reason instanceof Error ? reason.message : reason) }
 finally { if(current === epoch) loading.value = false }
}, {immediate:true})
async function save() {
 const current = epoch, nodeId = props.nodeId; saving.value = true; error.value = ''; saved.value = false
 try {
  const value = await operationRequest<Record<string, unknown>>('nodes.save-asset', {params:{nodeId}}, {body:exactJSON(asset.value)})
  if(current === epoch) { asset.value = value; await nextTick(); if(current === epoch) saved.value = true }
 } catch(reason) { if(current === epoch) error.value = String(reason instanceof Error ? reason.message : reason) }
 finally { saving.value = false }
}
watch(asset, () => { saved.value = false }, {deep:true})
</script>
<template>
 <form class="asset-settings" data-testid="node-asset-settings" @submit.prevent="save">
  <NodeAssetFields v-if="!loading && schema" :values="asset" :schema="schema" :disabled="saving" />
  <p v-if="loading">{{ t('asset.loading') }}</p>
  <p v-if="error" class="alert alert-error" role="alert">{{ display(error) }}</p>
  <p v-if="saved" class="alert alert-success" role="status">{{ t('asset.saved') }}</p>
  <button v-if="!loading && schema" class="secondary-button" type="submit" :disabled="saving" data-testid="asset-save">{{ t(saving ? 'asset.saving' : 'asset.save') }}</button>
 </form>
</template>
<style scoped>.asset-settings{margin-top:20px;min-width:0}.asset-settings>p{font-size:13px;color:var(--muted-strong)}</style>
