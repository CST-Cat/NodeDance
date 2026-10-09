import { createApp, h } from 'vue'
import NodeFiles from '../../src/components/NodeFiles.vue'

createApp({
  setup: () => () => h(NodeFiles, { nodeId: '01234567-89ab-4cde-8fab-0123456789ab' }),
}).mount('#app')
