import { createApp } from 'vue'
import App from './App.vue'
import UiSelect from './components/UiSelect.vue'
import './style.css'

createApp(App).component('UiSelect',UiSelect).mount('#app')
