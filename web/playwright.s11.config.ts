import { defineConfig } from '@playwright/test'

const baseURL = 'http://127.0.0.1:4177'

export default defineConfig({
  testDir: './tests/s11',
  fullyParallel: false,
  forbidOnly: true,
  retries: 0,
  timeout: 60_000,
  reporter: 'line',
  use: { baseURL, browserName: 'chromium', viewport: { width: 390, height: 844 }, headless: true },
  webServer: {
    command: 'pnpm exec vite --host 127.0.0.1 --port 4177 --strictPort',
    url: baseURL,
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
})
