import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  root: '.',
  plugins: [vue()],
  build: {
    target: 'es2022',
    sourcemap: false,
    emptyOutDir: true,
  },
})
