<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { activeDialog, cancelDialogs, finishDialog } from '../dialogs'
import { t } from '../i18n'

const element = ref<HTMLDialogElement>()
const input = ref<HTMLInputElement>()
const value = ref('')
watch(activeDialog, async request => {
  element.value?.close()
  if (!request) return
  value.value = request.value
  await nextTick()
  if (activeDialog.value !== request) return
  element.value?.showModal()
  if (request.kind === 'prompt') { input.value?.focus(); input.value?.select() }
}, { flush:'post' })
function finish(accepted: boolean) {
  element.value?.close()
  finishDialog(accepted ? (activeDialog.value?.kind === 'prompt' ? value.value : '') : null)
}
onBeforeUnmount(cancelDialogs)
</script>

<template>
  <dialog ref="element" class="ui-dialog" aria-labelledby="ui-dialog-title" aria-describedby="ui-dialog-message" @cancel.prevent="finish(false)">
    <form v-if="activeDialog" @submit.prevent="finish(true)">
      <h2 id="ui-dialog-title">{{ t('dialog.title') }}</h2>
      <p id="ui-dialog-message">{{ activeDialog.message }}</p>
      <input v-if="activeDialog.kind === 'prompt'" ref="input" v-model="value" :aria-label="activeDialog.message" autocomplete="off">
      <footer><button class="secondary-button" type="button" autofocus @click="finish(false)">{{ t('dialog.cancel') }}</button><button class="primary-button" type="submit">{{ t('dialog.confirm') }}</button></footer>
    </form>
  </dialog>
</template>

<style scoped>
.ui-dialog { width:min(480px,calc(100% - 32px)); max-height:calc(100dvh - 32px); overflow:auto; margin:auto; border:1px solid var(--line-strong); border-radius:var(--radius-card); padding:32px; color:var(--text); background:var(--surface); }
.ui-dialog::backdrop { background:rgba(24,23,21,.65); }
h2 { margin:0 0 16px; font-size:28px; }
p { margin:0 0 24px; white-space:pre-wrap; overflow-wrap:anywhere; font-size:14px; line-height:1.55; }
input { width:100%; min-height:40px; border:1px solid var(--line); border-radius:var(--radius-control); padding:10px 12px; color:var(--text); background:var(--canvas); }
footer { display:flex; flex-wrap:wrap; justify-content:flex-end; gap:12px; margin-top:24px; }
@media(max-width:480px) { .ui-dialog { padding:24px; } }
</style>
