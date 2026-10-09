import { readFileSync } from 'node:fs'
import https from 'node:https'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

const s03CoreTarget = process.env.NODEDANCE_S03_CORE_TARGET
const s03CoreCA = process.env.NODEDANCE_S03_CORE_CA
const s03CoreAgent = s03CoreCA ? new https.Agent({ ca: readFileSync(s03CoreCA) }) : undefined

function s03ProxyOptions(websocket = false) {
  return {
    target: s03CoreTarget,
    changeOrigin: true,
    secure: true,
    agent: s03CoreAgent,
    ...(websocket ? { ws: true } : {}),
  }
}

export default defineConfig({
  root: '.',
  plugins: [vue()],
  server: s03CoreTarget ? {
    proxy: {
      '/api': s03ProxyOptions(),
      '/ws': s03ProxyOptions(true),
    },
  } : undefined,
  build: {
    target: 'es2022',
    sourcemap: false,
    emptyOutDir: true,
  },
})
