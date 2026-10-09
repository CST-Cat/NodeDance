import { expect, test } from '@playwright/test'

const projectKey = 'compose-editor-fixture'
const composePath = '/srv/nd-demo/compose.yaml'
const original = 'services:\n  web:\n    image: nginx:locked\n    ports:\n      - 127.0.0.1:18080:80/tcp\n'
const nodeProject = {
  ref: { key: projectKey, name: 'nd-demo', workingDirectory: '/srv/nd-demo', configFiles: [composePath] },
  configAvailable: true,
  services: [{ name: 'web', instances: [{ containerId: 'abc123', containerName: 'nd-demo-web-1', state: 'running', health: 'healthy' }] }],
}

test('opens a project editor, previews a port change, and reports apply and rollback status', async ({ page }) => {
  let applyCount = 0
  await page.route('**/api/v1/nodes/test-node/compose/projects', async (route) => {
    await route.fulfill({ json: { projects: [nodeProject], serverTime: new Date().toISOString() } })
  })
  await page.route('**/api/v1/auth/csrf', async (route) => {
    await route.fulfill({ json: { token: 'test-csrf' } })
  })
  await page.route(`**/api/v1/nodes/test-node/compose/projects/${projectKey}/editor/source`, async (route) => {
    await route.fulfill({ json: { files: [{ path: composePath, content: original, version: 'a'.repeat(64) }] } })
  })
  await page.route(`**/api/v1/nodes/test-node/compose/projects/${projectKey}/editor/preview`, async (route) => {
    const body = route.request().postDataJSON() as { editor: { files: Array<{ path: string; content: string }> } }
    const content = body.editor.files[0]?.content ?? original
    await route.fulfill({ json: {
      files: [{ path: composePath, content, version: 'b'.repeat(64) }],
      diff: '--- compose.yaml\n+++ compose.yaml\n-      - 127.0.0.1:18080:80/tcp\n+      - 127.0.0.1:18081:80/tcp\n',
      resolvedConfig: '{\n  "services": { "web": { "ports": [{ "published": "18081" }] } }\n}',
      affectedServices: ['web'],
      impact: ['仅重建 web。', '应用数据卷不在配置回滚范围内。'],
      dataBackup: false,
      rollbackConfirmed: false,
    } })
  })
  await page.route(`**/api/v1/nodes/test-node/compose/projects/${projectKey}/editor/apply`, async (route) => {
    applyCount += 1
    await route.fulfill({ status: 202, json: {
      operationId: `s11-operation-${applyCount}`,
      status: 'running',
      verified: false,
      dataBackup: false,
    } })
  })
  await page.route('**/api/v1/nodes/test-node/compose/editor/operations/**', async (route) => {
    const status = applyCount === 1
      ? { status: 'succeeded', verified: true, dataBackup: false, affectedServices: ['web'] }
      : { status: 'failed', errorCode: 'health_failed', verified: false, dataBackup: false, rollbackConfirmed: true, affectedServices: ['web'] }
    await route.fulfill({ json: { operationId: `s11-operation-${applyCount}`, ...status } })
  })

  await page.goto('/s11-editor-fixture.html')
  await expect(page.getByRole('heading', { name: 'nd-demo' })).toBeVisible()
  await page.getByRole('button', { name: '编辑配置与端口' }).click()
  await page.getByRole('button', { name: '读取源文件' }).click()
  const editor = page.getByRole('textbox', { name: composePath })
  await expect(editor).toHaveValue(original)
  await editor.fill(original.replace('18080', '18081'))
  await page.getByRole('button', { name: '预览并校验' }).click()
  await expect(page.getByRole('region', { name: '变更预览' })).toContainText('web')
  await expect(page.getByText('未备份；请使用应用自身备份')).toBeVisible()
  page.once('dialog', (dialog) => dialog.accept())
  await page.getByRole('button', { name: '应用已预览的变更' }).click()
  await expect(page.getByText(/最近任务：s11-operation-1 · succeeded/)).toBeVisible()
  await expect(page.getByText('Compose 文件、受影响服务及端口已完成实际状态验证。')).toBeVisible()

  await editor.fill(original.replace('18080', '18082'))
  await page.getByRole('button', { name: '预览并校验' }).click()
  await expect(page.getByRole('region', { name: '变更预览' })).toBeVisible()
  page.once('dialog', (dialog) => dialog.accept())
  await page.getByRole('button', { name: '应用已预览的变更' }).click()
  await expect(page.getByText(/最近任务：s11-operation-2 · failed · health_failed/)).toBeVisible()
  await expect(page.getByText(/配置及服务回滚已确认/)).toBeVisible()
})
