import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { env } from 'node:process'
import { expect, request as playwrightRequest, test, type Locator } from '@playwright/test'

const dataDir = env.NODEDANCE_TEST_DATA_DIR

async function renderedPixel(image: Locator): Promise<number[]> {
  return image.evaluate(async (element) => {
    const source = element as HTMLImageElement
    await source.decode()
    const canvas = document.createElement('canvas')
    canvas.width = 1
    canvas.height = 1
    const context = canvas.getContext('2d')
    if (!context) throw new Error('Canvas is unavailable in the browser.')
    context.drawImage(source, 0, 0, 1, 1)
    return Array.from(context.getImageData(0, 0, 1, 1).data)
  })
}

test('real Core setup, login, appearance, sessions, password, and logout', async ({ page, browser }) => {
  if (!dataDir) throw new Error('Set NODEDANCE_TEST_DATA_DIR to the temporary Core data directory.')
  const credential = readFileSync(join(dataDir, 'setup-credential.txt'), 'utf8').trim()
  if (!credential) throw new Error('The temporary Core setup credential file is empty.')

  const initialPassword = env.NODEDANCE_TEST_INITIAL_PASSWORD
  const changedPassword = env.NODEDANCE_TEST_CHANGED_PASSWORD
  if (!initialPassword || !changedPassword) {
    throw new Error('Set NODEDANCE_TEST_INITIAL_PASSWORD and NODEDANCE_TEST_CHANGED_PASSWORD for S01 browser tests.')
  }
  if (initialPassword === changedPassword) throw new Error('S01 test passwords must be different.')
  const unsafeDisplayName = '<img src=x onerror="window.xssExecuted=true">'

  await page.goto('/')
  await expect(page.getByRole('heading', { name: '创建管理员账户' })).toBeVisible()
  await page.getByLabel('初始化凭据').fill(credential)
  await page.getByLabel('展示名称').fill('NodeDance browser test')
  await page.getByLabel('管理员密码').fill(initialPassword)
  await page.getByRole('button', { name: '完成初始化' }).click()
  await expect(page.getByRole('heading', { name: '账户与外观' })).toBeVisible()

  await page.locator('input[name="profileDisplayName"]').fill(unsafeDisplayName)
  await page.getByRole('button', { name: '浅色' }).click()
  const workspaceColor = '#c9d7f1'
  await page.getByLabel('背景颜色').evaluate((element, color) => {
    const input = element as HTMLInputElement
    input.value = color
    input.dispatchEvent(new Event('input', { bubbles: true }))
    input.dispatchEvent(new Event('change', { bubbles: true }))
  }, workspaceColor)
  await page.getByRole('button', { name: '保存外观' }).click()
  await expect(page.locator('.profile-summary strong')).toHaveText(unsafeDisplayName)
  await expect(page.locator('img[src="x"]')).toHaveCount(0)
  expect(await page.evaluate(() => (window as Window & { xssExecuted?: boolean }).xssExecuted)).toBeUndefined()

  const redPixel = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==', 'base64')
  const bluePixel = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYPj/HwADAgH/5ncLrgAAAABJRU5ErkJggg==', 'base64')
  const coreURL = new URL(env.PLAYWRIGHT_BASE_URL!)
  const imagePosts: string[] = []
  const imageGets: Record<'avatar' | 'background', string[]> = { avatar: [], background: [] }
  page.on('request', (request) => {
    const url = new URL(request.url())
    const kind = url.pathname.match(/^\/api\/v1\/public\/appearance\/(avatar|background)$/)?.[1]
    if (request.method() === 'GET' && (kind === 'avatar' || kind === 'background')) {
      imageGets[kind].push(url.href)
    }
    if (request.method() === 'POST' && /\/api\/v1\/settings\/appearance\/(avatar|background)$/.test(url.pathname)) {
      imagePosts.push(url.href)
    }
  })

  const avatarImage = page.locator('.large-avatar img')
  await page.locator('input[aria-label="上传头像"]').setInputFiles({
    name: 'avatar.png',
    mimeType: 'image/png',
    buffer: redPixel,
  })
  await expect(page.locator('.page-alert')).toHaveText('头像已更新。')
  await expect(avatarImage).toBeVisible()
  await expect.poll(() => renderedPixel(avatarImage)).toEqual([255, 0, 0, 255])
  const firstAvatarSrc = new URL((await avatarImage.getAttribute('src'))!, coreURL)
  expect(firstAvatarSrc.pathname).toBe('/api/v1/public/appearance/avatar')
  expect(firstAvatarSrc.searchParams.get('rev')).toBe('1')
  const firstAvatarGetCount = imageGets.avatar.length

  await page.locator('input[aria-label="上传头像"]').setInputFiles({
    name: 'avatar-replacement.png',
    mimeType: 'image/png',
    buffer: bluePixel,
  })
  await expect(page.locator('.page-alert')).toHaveText('头像已更新。')
  await expect.poll(() => renderedPixel(avatarImage)).toEqual([0, 0, 255, 255])
  expect(imageGets.avatar.length).toBeGreaterThan(firstAvatarGetCount)
  const secondAvatarSrc = new URL((await avatarImage.getAttribute('src'))!, coreURL)
  expect(secondAvatarSrc.pathname).toBe('/api/v1/public/appearance/avatar')
  expect(secondAvatarSrc.searchParams.get('rev')).toBe('2')
  const secondAvatarGet = new URL(imageGets.avatar.at(-1)!, coreURL)
  expect(secondAvatarGet.pathname).toBe('/api/v1/public/appearance/avatar')
  expect(secondAvatarGet.searchParams.get('rev')).toBe('2')

  const backgroundPreview = page.locator('.background-preview img')
  await page.locator('input[aria-label="上传背景图片"]').setInputFiles({
    name: 'background.png',
    mimeType: 'image/png',
    buffer: redPixel,
  })
  await expect(page.locator('.page-alert')).toHaveText('背景图片已更新。')
  await expect(backgroundPreview).toBeVisible()
  await expect.poll(() => renderedPixel(backgroundPreview)).toEqual([255, 0, 0, 255])
  const firstBackgroundSrc = new URL((await backgroundPreview.getAttribute('src'))!, coreURL)
  expect(firstBackgroundSrc.pathname).toBe('/api/v1/public/appearance/background')
  expect(firstBackgroundSrc.searchParams.get('rev')).toBe('1')
  const firstBackgroundGetCount = imageGets.background.length

  await page.locator('input[aria-label="上传背景图片"]').setInputFiles({
    name: 'background-replacement.png',
    mimeType: 'image/png',
    buffer: bluePixel,
  })
  await expect(page.locator('.page-alert')).toHaveText('背景图片已更新。')
  await expect.poll(() => renderedPixel(backgroundPreview)).toEqual([0, 0, 255, 255])
  expect(imageGets.background.length).toBeGreaterThan(firstBackgroundGetCount)
  const secondBackgroundSrc = new URL((await backgroundPreview.getAttribute('src'))!, coreURL)
  expect(secondBackgroundSrc.pathname).toBe('/api/v1/public/appearance/background')
  expect(secondBackgroundSrc.searchParams.get('rev')).toBe('2')
  const secondBackgroundGet = new URL(imageGets.background.at(-1)!, coreURL)
  expect(secondBackgroundGet.pathname).toBe('/api/v1/public/appearance/background')
  expect(secondBackgroundGet.searchParams.get('rev')).toBe('2')
  expect(imagePosts).toHaveLength(4)

  const coreOrigin = coreURL.origin
  const authCookies = await page.context().cookies(coreOrigin)
  const uploadSessionCookie = authCookies.find((cookie) => cookie.name === 'nodedance_session')
  const csrfCookie = authCookies.find((cookie) => cookie.name === 'nodedance_csrf')
  expect(uploadSessionCookie?.value).toBeTruthy()
  expect(csrfCookie?.value).toBeTruthy()
  const writeHeaders = {
    Cookie: `nodedance_session=${uploadSessionCookie!.value}; nodedance_csrf=${csrfCookie!.value}`,
    'X-CSRF-Token': csrfCookie!.value,
    Origin: coreOrigin,
  }

  const anonymousCore = await playwrightRequest.newContext({ baseURL: env.PLAYWRIGHT_BASE_URL })
  try {
    const beforeAppearanceResponse = await anonymousCore.get('/api/v1/public/appearance')
    expect(beforeAppearanceResponse.status()).toBe(200)
    const beforeAppearance = await beforeAppearanceResponse.json() as {
      avatarUrl: string
      backgroundUrl: string
    }
    expect(beforeAppearance.avatarUrl).toBe('/api/v1/public/appearance/avatar')
    expect(beforeAppearance.backgroundUrl).toBe('/api/v1/public/appearance/background')
    const beforeAvatarResponse = await anonymousCore.get(beforeAppearance.avatarUrl)
    const beforeBackgroundResponse = await anonymousCore.get(beforeAppearance.backgroundUrl)
    expect(beforeAvatarResponse.status()).toBe(200)
    expect(beforeBackgroundResponse.status()).toBe(200)
    const beforeAvatar = await beforeAvatarResponse.body()
    const beforeBackground = await beforeBackgroundResponse.body()

    const svgMarker = `S01-svg-${Date.now()}`
    const htmlMarker = `S01-html-${Date.now()}`
    const rejectedSVG = await page.context().request.post(
      new URL('/api/v1/settings/appearance/avatar', coreURL).toString(),
      {
        headers: writeHeaders,
        multipart: {
          image: {
            name: 'direct-upload.svg',
            mimeType: 'image/svg+xml',
            buffer: Buffer.from(`<svg xmlns="http://www.w3.org/2000/svg"><script>${svgMarker}</script></svg>`),
          },
        },
      },
    )
    expect(rejectedSVG.status()).toBe(400)
    expect(await rejectedSVG.text()).not.toContain(svgMarker)

    const rejectedHTML = await page.context().request.post(
      new URL('/api/v1/settings/appearance/background', coreURL).toString(),
      {
        headers: writeHeaders,
        multipart: {
          image: {
            name: 'direct-upload.html',
            mimeType: 'text/html',
            buffer: Buffer.from(`<html><body>${htmlMarker}</body></html>`),
          },
        },
      },
    )
    expect(rejectedHTML.status()).toBe(400)
    expect(await rejectedHTML.text()).not.toContain(htmlMarker)

    const afterAppearanceResponse = await anonymousCore.get('/api/v1/public/appearance')
    expect(afterAppearanceResponse.status()).toBe(200)
    const afterAppearance = await afterAppearanceResponse.json() as {
      avatarUrl: string
      backgroundUrl: string
    }
    expect(afterAppearance.avatarUrl).toBe(beforeAppearance.avatarUrl)
    expect(afterAppearance.backgroundUrl).toBe(beforeAppearance.backgroundUrl)
    const afterAvatarResponse = await anonymousCore.get(afterAppearance.avatarUrl)
    const afterBackgroundResponse = await anonymousCore.get(afterAppearance.backgroundUrl)
    expect(afterAvatarResponse.status()).toBe(200)
    expect(afterBackgroundResponse.status()).toBe(200)
    expect((await afterAvatarResponse.body()).equals(beforeAvatar)).toBe(true)
    expect((await afterBackgroundResponse.body()).equals(beforeBackground)).toBe(true)

    const svgLeak = await anonymousCore.get(`/api/v1/public/appearance/${svgMarker}.svg`)
    const htmlLeak = await anonymousCore.get(`/api/v1/public/appearance/${htmlMarker}.html`)
    expect(svgLeak.status()).toBe(401)
    expect(htmlLeak.status()).toBe(401)
  } finally {
    await anonymousCore.dispose()
  }

  const postsBeforeClientSVG = imagePosts.length
  await page.locator('input[aria-label="上传头像"]').setInputFiles({
    name: 'unsafe.svg',
    mimeType: 'image/svg+xml',
    buffer: Buffer.from('<svg xmlns="http://www.w3.org/2000/svg"><script>window.xssExecuted=true</script></svg>'),
  })
  await expect(page.getByRole('alert')).toContainText('SVG 图片不受支持')
  expect(imagePosts).toHaveLength(postsBeforeClientSVG)
  expect(await page.evaluate(() => (window as Window & { xssExecuted?: boolean }).xssExecuted)).toBeUndefined()

  const secondaryContext = await browser.newContext({ baseURL: env.PLAYWRIGHT_BASE_URL })
  try {
    const secondaryPage = await secondaryContext.newPage()
    await secondaryPage.goto('/')
    await expect(secondaryPage.getByRole('heading', { name: '管理员登录' })).toBeVisible()
    await secondaryPage.getByLabel('管理员密码').fill(initialPassword)
    await secondaryPage.getByRole('button', { name: '登录控制台' }).click()
    await expect(secondaryPage.getByRole('heading', { name: '账户与外观' })).toBeVisible()

    await page.reload()
    await expect(page.getByRole('heading', { name: '账户与外观' })).toBeVisible()
    await expect(page.locator('.profile-summary strong')).toHaveText(unsafeDisplayName)
    await expect(page.getByLabel('背景颜色')).toHaveValue(workspaceColor)
    await expect(page.locator('.session-count')).toHaveText('2 个')
    const revokeOtherSession = page.getByRole('button', { name: /撤销 .* 会话/ })
    await expect(revokeOtherSession).toHaveCount(1)
    await revokeOtherSession.click()
    await expect(page.locator('.session-count')).toHaveText('1 个')

    await secondaryPage.reload()
    await expect(secondaryPage.getByRole('heading', { name: '管理员登录' })).toBeVisible()
  } finally {
    await secondaryContext.close()
  }

  await page.getByRole('button', { name: '退出登录' }).click()
  await expect(page.getByRole('heading', { name: '管理员登录' })).toBeVisible()
  await expect(page.locator('.login-identity strong')).toHaveText(unsafeDisplayName)
  await expect(page.locator('img[src="x"]')).toHaveCount(0)
  const loginBackdrop = page.locator('.login-background > img')
  await expect(loginBackdrop).toBeVisible()
  const loginBackdropURL = new URL((await loginBackdrop.getAttribute('src'))!, coreURL)
  expect(loginBackdropURL.pathname).toBe('/api/v1/public/appearance/background')
  await expect.poll(() => renderedPixel(loginBackdrop)).toEqual([0, 0, 255, 255])
  await expect.poll(() => loginBackdrop.evaluate((image) => (image as HTMLImageElement).naturalWidth)).toBeGreaterThan(0)
  await page.setViewportSize({ width: 390, height: 844 })
  const backdropLayout = await loginBackdrop.evaluate((image) => {
    const element = image as HTMLImageElement
    const imageStyle = getComputedStyle(element)
    const wrapperStyle = getComputedStyle(element.parentElement!)
    const overlayStyle = getComputedStyle(element.nextElementSibling!)
    const rect = element.getBoundingClientRect()
    return {
      objectFit: imageStyle.objectFit,
      wrapperPosition: wrapperStyle.position,
      width: rect.width,
      height: rect.height,
      overlay: overlayStyle.backgroundImage,
    }
  })
  expect(backdropLayout.objectFit).toBe('cover')
  expect(backdropLayout.wrapperPosition).toBe('absolute')
  expect(backdropLayout.width).toBe(390)
  expect(backdropLayout.height).toBeGreaterThanOrEqual(844)
  expect(backdropLayout.overlay).toContain('linear-gradient')
  await page.setViewportSize({ width: 1280, height: 900 })

  await page.getByLabel('管理员密码').fill(initialPassword)
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByRole('heading', { name: '账户与外观' })).toBeVisible()

  const sessionCookie = (await page.context().cookies()).find((cookie) => cookie.name === 'nodedance_session')
  expect(sessionCookie?.value).toBeTruthy()
  await page.getByLabel('当前密码').fill(initialPassword)
  await page.getByLabel('新密码', { exact: true }).fill(changedPassword)
  await page.getByLabel('确认新密码').fill(changedPassword)
  await page.getByRole('button', { name: '更新密码' }).click()
  await expect(page.getByRole('heading', { name: '管理员登录' })).toBeVisible()

  const staleSessionClient = await playwrightRequest.newContext({
    baseURL: env.PLAYWRIGHT_BASE_URL,
    extraHTTPHeaders: { Cookie: `nodedance_session=${sessionCookie!.value}` },
  })
  try {
    const staleSessionResponse = await staleSessionClient.get('/api/v1/auth/me')
    expect(staleSessionResponse.status()).toBe(401)
  } finally {
    await staleSessionClient.dispose()
  }

  await page.getByLabel('管理员密码').fill(initialPassword)
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByRole('alert')).toBeVisible()
  await page.getByLabel('管理员密码').fill(changedPassword)
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByRole('heading', { name: '账户与外观' })).toBeVisible()
  await page.getByRole('button', { name: '退出登录' }).click()
  await expect(page.getByRole('heading', { name: '管理员登录' })).toBeVisible()
})
