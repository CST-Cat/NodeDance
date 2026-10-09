<script setup lang="ts">
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
    const next = { ...form, alias: form.alias.trim(), notes: form.notes.trim(), serviceUrl: form.serviceUrl.trim(), group: form.group.trim() }
    await api.saveNodePreference(next.nodeId, next)
    Object.assign(form, next)
    emit('saved', next)
    notice.value = '显示偏好已保存。'
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '偏好保存失败。'
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <form class="preference-editor" @submit.prevent="save">
    <header><strong>{{ title }}</strong><small>只影响 NodeDance 的展示，不会重命名 Docker 资源。</small></header>
    <label>显示名称
      <input v-model="form.alias" maxlength="128" placeholder="留空时使用真实名称">
    </label>
    <label>图标
      <select v-model="form.icon">
        <option value="">默认</option><option value="server">服务器</option><option value="globe">网站</option>
        <option value="database">数据库</option><option value="shield">安全</option><option value="terminal">终端</option>
        <option value="box">容器</option><option value="cloud">云</option><option value="folder">文件</option><option value="activity">监控</option>
      </select>
    </label>
    <label>备注
      <textarea v-model="form.notes" maxlength="2048" rows="2" placeholder="可选说明"></textarea>
    </label>
    <label v-if="form.targetKind === 'node'">VPS 分组
      <input v-model="form.group" maxlength="128" placeholder="未分组">
    </label>
    <label v-if="form.targetKind === 'node'">自定义顺序
      <input v-model.number="form.sortOrder" type="number" min="-1000000" max="1000000" step="1" required>
    </label>
    <label>服务入口
      <input v-model="form.serviceUrl" type="url" maxlength="2048" placeholder="https://service.example.com">
    </label>
    <label class="preference-checkbox"><input v-model="form.visible" type="checkbox"> 在首页显示</label>
    <label class="preference-checkbox"><input v-model="form.pinned" type="checkbox"> 置顶</label>
    <div class="preference-actions"><button type="submit" :disabled="saving">{{ saving ? '保存中…' : '保存偏好' }}</button><span v-if="notice" role="status">{{ notice }}</span><span v-if="error" role="alert">{{ error }}</span></div>
  </form>
</template>

<style scoped>
.preference-editor { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; margin-top: 12px; border: 1px solid rgba(171,196,232,.13); border-radius: 9px; padding: 12px; background: rgba(9,17,29,.6); }
.preference-editor header { grid-column: 1 / -1; display: grid; gap: 4px; }
.preference-editor header strong { font-size: 11px; }
.preference-editor header small { color: #91a2ba; font-size: 9px; }
.preference-editor label { display: grid; min-width: 0; gap: 5px; color: #aebbd0; font-size: 9px; }
.preference-editor input:not([type=checkbox]), .preference-editor select, .preference-editor textarea { width: 100%; min-width: 0; border: 1px solid rgba(171,196,232,.16); border-radius: 6px; padding: 8px; color: #eaf0fa; background: #111b29; font: inherit; font-size: 10px; }
.preference-editor textarea { resize: vertical; }
.preference-editor .preference-checkbox { display: flex; align-items: center; gap: 7px; }
.preference-actions { grid-column: 1 / -1; display: flex; flex-wrap: wrap; align-items: center; gap: 9px; color: #9ce0b7; font-size: 9px; }
.preference-actions button { min-height: 34px; border: 1px solid rgba(141,201,255,.25); border-radius: 7px; padding: 6px 10px; color: #cce6ff; background: rgba(62,119,170,.16); font: inherit; cursor: pointer; }
.preference-actions span[role=alert] { color: #ffc1b8; }
.preference-actions button:disabled { opacity: .5; }
@media (max-width: 480px) { .preference-editor { grid-template-columns: minmax(0, 1fr); } }
</style>
