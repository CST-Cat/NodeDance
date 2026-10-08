import { env } from 'node:process'
import { defineConfig } from '@playwright/test'

const baseURL = env.PLAYWRIGHT_BASE_URL
if (!baseURL) {
  throw new Error('Set PLAYWRIGHT_BASE_URL to the real NodeDance Core URL before running browser tests.')
}

export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  forbidOnly: Boolean(env.CI),
  retries: 0,
  timeout: 120_000,
  reporter: 'line',
  use: {
    baseURL,
    viewport: { width: 1280, height: 720 },
    headless: true,
    trace: 'off',
  },
  projects: [
    { name: 'chromium', use: { browserName: 'chromium' } },
    { name: 'webkit', use: { browserName: 'webkit' } },
    { name: 'firefox', use: { browserName: 'firefox' } },
  ],
})
