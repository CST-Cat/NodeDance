<script setup lang="ts">
import { computed } from 'vue'
import { t } from '../i18n'
export type DockerPrimaryAction = 'start' | 'stop' | 'restart' | 'rename' | 'delete'
const props = defineProps<{ can: (action: DockerPrimaryAction) => boolean; renameLabel?: string }>()
const emit = defineEmits<{ action: [action: DockerPrimaryAction] }>()
const actions = computed(() => [
 { action: 'start' as const, label: t('m.56410fc65314') },
 { action: 'stop' as const, label: t('m.ca4d973c0b00') },
 { action: 'restart' as const, label: t('m.562822892865') },
 { action: 'rename' as const, label: props.renameLabel || t('m.0d0cbac2eee5') },
 { action: 'delete' as const, label: t('m.2f9daa828907') },
])
</script>
<template>
 <div class="docker-primary-actions">
  <button v-for="{action,label} in actions" :key="action" class="container-action" :class="{'container-action-danger':action==='delete'}" type="button" :data-action="action" :disabled="!can(action)" :title="action==='rename'?t('docker.singleOnly'):t('docker.allApplicable')" @click="emit('action',action)">{{ label }}</button>
 </div>
</template>
<style scoped>
.docker-primary-actions { display:flex; flex-wrap:wrap; gap:8px; }
</style>
