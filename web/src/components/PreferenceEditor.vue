<script setup lang="ts">
import { t, display, locale } from "../i18n"
import { reactive, ref, watch } from 'vue'
import { api, type DashboardPreference } from '../api'

const props = defineProps<{ preference: DashboardPreference; title: string }>()
const emit = defineEmits<{ saved: [preference: DashboardPreference] }>()
const form = reactive<DashboardPreference>({ ...props.preference })
const saving = ref(false)
const error = ref('')
const notice = ref('')

watch(() => props.preference, (value) => Object.assign(form, value), { deep: true })

async function save() {
  error.value = ''
  notice.value = ''
  saving.value = true
  try {
    const next = { ...form, alias: form.alias.trim(), notes: form.notes.trim(), serviceUrl: form.serviceUrl.trim(),
      sshHost: form.sshHost.trim(), sshUser: form.sshUser.trim(), sshPort: Number(form.sshPort) || 22, group: form.group.trim() }
    await api.saveNodePreference(next.nodeId, next)
    Object.assign(form, next)
    emit('saved', next)
    notice.value = t('m.2f1a6d364d44')
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : t('m.4c4b8b19ae6a')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <form class="preference-editor" @submit.prevent="save">
    <header><strong>{{ title }}</strong><small>{{ t('m.741981bd96aa') }}</small></header>
    <label>{{ t('m.a98585871c53') }}<input v-model="form.alias" maxlength="128" :placeholder="t('m.0496871244c3')">
    </label>
    <label>{{ t('m.0d720eeea264') }}<UiSelect v-model="form.icon">
        <option value="">{{ t('m.844b8cc8dff7') }}</option><option value="server">{{ t('m.2388cbc6a486') }}</option><option value="globe">{{ t('m.fb8d739d08b9') }}</option>
        <option value="database">{{ t('m.263200d7d529') }}</option><option value="shield">{{ t('m.afb63a620bdc') }}</option><option value="terminal">{{ t('m.e2a76ef1f12e') }}</option>
        <option value="box">{{ t('m.6d23f04b2696') }}</option><option value="cloud">{{ t('m.32d42b97294a') }}</option><option value="folder">{{ t('m.39932f24fe11') }}</option><option value="activity">{{ t('m.fdc390061698') }}</option>
      </UiSelect>
    </label>
    <label>{{ t('m.daede9881787') }}<textarea v-model="form.notes" maxlength="2048" rows="2" :placeholder="t('m.9ad5244a4b67')"></textarea>
    </label>
    <label v-if="form.targetKind === 'node'">{{ t('m.889b28078e5d') }}<input v-model="form.group" maxlength="128" :placeholder="t('m.e86ea3f280d9')">
    </label>
    <label v-if="form.targetKind === 'node'">{{ t('m.2290354d1f88') }}<input v-model="form.sshHost" maxlength="255" autocomplete="off" :placeholder="t('m.5f4aaae405f3')">
    </label>
    <label v-if="form.targetKind === 'node'">{{ t('m.1c22cc57aee8') }}<input v-model.number="form.sshPort" type="number" min="1" max="65535" step="1" inputmode="numeric" required>
    </label>
    <label v-if="form.targetKind === 'node'">{{ t('m.4249329ab914') }}<input v-model="form.sshUser" maxlength="64" autocomplete="off" :placeholder="t('m.c650b8295c49')">
    </label>
    <small v-if="form.targetKind === 'node'" class="ssh-preference-note">{{ t('m.a5d21b75a337') }}</small>
    <label v-if="form.targetKind === 'node'">{{ t('m.0f59d3742e06') }}<input v-model.number="form.sortOrder" type="number" min="-1000000" max="1000000" step="1" required>
    </label>
    <label>{{ t('m.5d52d4478187') }}<input v-model="form.serviceUrl" type="url" maxlength="2048" placeholder="https://service.example.com">
    </label>
    <label class="preference-checkbox"><input v-model="form.visible" type="checkbox">{{ t('m.0264c56e530a') }}</label>
    <label class="preference-checkbox"><input v-model="form.pinned" type="checkbox">{{ t('m.173f88d28ebf') }}</label>
    <div class="preference-actions"><button type="submit" :disabled="saving">{{ saving ? t('m.ff509c9ba052') : t('m.f96c9903175e') }}</button><span v-if="notice" role="status">{{ display(notice) }}</span><span v-if="error" role="alert">{{ display(error) }}</span></div>
  </form>
</template>

<style scoped>
.preference-editor { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; margin-top: 12px; border: 1px solid var(--line); border-radius:var(--radius-card); padding: 12px; background: var(--surface-soft); }
.preference-editor header { grid-column: 1 / -1; display: grid; gap: 4px; }
.ssh-preference-note { grid-column: 1 / -1; color: var(--muted); font-size: 12px; }
.preference-editor header strong { font-size: 12px; }
.preference-editor header small { color: var(--muted); font-size: 12px; }
.preference-editor label { display: grid; min-width: 0; gap: 5px; color: var(--muted-strong); font-size: 12px; }
.preference-editor input:not([type=checkbox]), .preference-editor select, .preference-editor textarea { width: 100%; min-width: 0; border: 1px solid var(--line); border-radius:var(--radius-control); padding: 8px; color: var(--text); background: var(--surface-soft); font: inherit; font-size: 12px; }
.preference-editor textarea { resize: vertical; }
.preference-editor .preference-checkbox { display: flex; align-items: center; gap: 7px; }
.preference-actions { grid-column: 1 / -1; display: flex; flex-wrap: wrap; align-items: center; gap: 9px; color: var(--success); font-size: 12px; }
.preference-actions button { min-height:40px; border: 1px solid var(--line); border-radius:var(--radius-control); padding: 6px 10px; color: var(--accent); background: var(--line); font: inherit; cursor: pointer; }
.preference-actions span[role=alert] { color: var(--danger); }
.preference-actions button:disabled { opacity: .5; }
@media (max-width: 480px) { .preference-editor { grid-template-columns: minmax(0, 1fr); } }
</style>
