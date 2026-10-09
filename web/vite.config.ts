import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

const coreTarget = process.env.VITE_CORE_TARGET ?? 'http://127.0.0.1:8180'

export default defineConfig({
  root: '.',
  plugins: [vue()],
  server: {
    proxy: {
      '/api': { target: coreTarget, changeOrigin: true, secure: false },
      '/ws': { target: coreTarget, changeOrigin: true, secure: false, ws: true },
    },
  },
  build: {
    target: 'es2022',
    sourcemap: false,
    emptyOutDir: true,
  },
})
