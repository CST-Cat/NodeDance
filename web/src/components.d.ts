export {}
declare module 'vue' {
 export interface GlobalComponents { UiSelect: typeof import('./components/UiSelect.vue')['default'] }
}
