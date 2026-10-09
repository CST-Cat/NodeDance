import fs from 'node:fs'
import readline from 'node:readline'
import { chromium } from '@playwright/test'

const config = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'))
let browser
let context
let page

function containerRow() {
  return page.locator('.docker-row').filter({
    has: page.locator('.docker-container-copy strong', { hasText: config.containerName }),
  }).first()
}

async function open() {
  browser = await chromium.launch({ headless: true })
  context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1280, height: 900 } })
  await context.addCookies([{
    name: 'nodedance_session', value: config.session, url: config.url,
    secure: true, httpOnly: true, sameSite: 'Strict',
  }])
  page = await context.newPage()
  await page.goto(config.url, { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: '节点监控' }).waitFor({ timeout: 15_000 })
  await page.getByRole('button', { name: '节点监控' }).click()
  await containerRow().waitFor({ state: 'visible', timeout: 25_000 })
  const row = containerRow()
  await row.getByRole('button', { name: '重启容器' }).waitFor({ state: 'visible' })
  return { containerFound: await row.count() === 1 }
}

async function clickRestart() {
  const row = containerRow()
  let resolveKey
  let rejectKey
  let csrfToken = ''
  const keyPromise = new Promise((resolve, reject) => {
    resolveKey = resolve
    rejectKey = reject
  })
  const timeout = setTimeout(() => rejectKey(new Error('browser action did not issue its Core task request')), 10_000)
  const listener = (request) => {
    if (request.method() !== 'POST' || !request.url().includes(`/containers/${config.containerId}/actions`)) return
    const headers = request.headers()
    const key = headers['idempotency-key']
    csrfToken = headers['x-csrf-token'] ?? ''
    if (key) resolveKey(key)
  }
  page.on('request', listener)
  await row.getByRole('button', { name: '重启容器' }).click()
  const idempotencyKey = await keyPromise
  clearTimeout(timeout)
  page.off('request', listener)
  await page.waitForFunction((containerName) => {
    const row = [...document.querySelectorAll('.docker-row')].find((item) =>
      item.querySelector('.docker-container-copy strong')?.textContent?.includes(containerName))
    const status = row?.querySelector('.container-task-status')
    return status?.getAttribute('data-status') === 'running' && status.textContent?.includes('正在执行')
  }, config.containerName, { timeout: 15_000 })
  const status = await row.locator('.container-task-status').innerText()
  return { idempotencyKey, csrfToken, runningVisible: status.includes('正在执行') }
}

async function waitSucceeded() {
  const row = containerRow()
  await page.waitForFunction((containerName) => {
    const row = [...document.querySelectorAll('.docker-row')].find((item) =>
      item.querySelector('.docker-container-copy strong')?.textContent?.includes(containerName))
    const status = row?.querySelector('.container-task-status')
    return status?.getAttribute('data-status') === 'succeeded' && status.textContent?.includes('已验证完成')
  }, config.containerName, { timeout: 30_000 })
  return {
    taskStatus: await row.locator('.container-task-status').getAttribute('data-status'),
    statusText: await row.locator('.container-task-status').innerText(),
  }
}

async function handle(action) {
  switch (action) {
    case 'open': return await open()
    case 'clickRestart': return await clickRestart()
    case 'waitSucceeded': return await waitSucceeded()
    case 'close':
      await browser?.close()
      browser = undefined
      return { closed: true }
    default: throw new Error(`unknown S05 browser action: ${action}`)
  }
}

const lines = readline.createInterface({ input: process.stdin, crlfDelay: Infinity })
for await (const line of lines) {
  if (!line) continue
  let request
  try {
    request = JSON.parse(line)
    const result = await handle(request.action)
    process.stdout.write(`${JSON.stringify({ id: request.id, result })}\n`)
  } catch (error) {
    process.stdout.write(`${JSON.stringify({ id: request?.id ?? 0, error: String(error?.stack ?? error) })}\n`)
  }
}

await browser?.close()
