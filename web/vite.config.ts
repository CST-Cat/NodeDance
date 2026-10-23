import { fileURLToPath } from "node:url"
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const coreTarget = process.env.VITE_CORE_TARGET ?? 'http://127.0.0.1:8180'

export default defineConfig({
  root: '.',
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
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
