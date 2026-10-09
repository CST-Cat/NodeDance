import { createApp } from 'vue'
import ComposeProjects from '../../src/components/ComposeProjects.vue'

createApp(ComposeProjects, { nodeId: 'test-node', nodeName: 'S11 test node' }).mount('#app')
