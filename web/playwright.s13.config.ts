import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './tests-s13',
  fullyParallel: false,
  forbidOnly: true,
  retries: 0,
  timeout: 30_000,
  reporter: 'line',
  use: {
    baseURL: 'http://127.0.0.1:4180',
    browserName: 'chromium',
    viewport: { width: 1280, height: 900 },
    headless: true,
  },
  webServer: {
    command: 'pnpm exec vite --host 127.0.0.1 --port 4180 --strictPort',
    url: 'http://127.0.0.1:4180',
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
})
