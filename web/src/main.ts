import { createApp, defineComponent, h } from 'vue'
import { NConfigProvider, NCard, NTag } from 'naive-ui'
import './style.css'

const App = defineComponent({
  setup() {
    return () => h(NConfigProvider, null, {
      default: () => h('main', { class: 'shell' }, [
        h(NCard, { class: 'card', bordered: false }, {
          default: () => [
            h('div', { class: 'brand' }, 'NodeDance'),
            h(NTag, { type: 'info', bordered: false }, { default: () => 'S00 基础阶段' }),
            h('h1', '服务器与容器统一管理'),
            h('p', 'Core 服务和前端资源已打包在同一个程序中。管理功能将在对应阶段通过验收后开放。'),
          ],
        }),
      ]),
    })
  },
})

createApp(App).mount('#app')
