const assert = require('node:assert/strict')

module.exports = async function ({page, browserUI, listPath}) {
  const tree = browserUI.getByRole('tree', {name:'Directories', exact:true})
  const parent = tree.getByRole('treeitem', {name:'tree-parent', exact:true})
  const child = tree.getByRole('treeitem', {name:'tree-child', exact:true})
  const file = name => browserUI.locator('[data-file-name]').and(browserUI.getByRole('button', {name, exact:true}))
  const toolbar = browserUI.getByRole('toolbar')
  const pattern = url => url.pathname === listPath && url.searchParams.get('path') === '/tree-parent'
  let release, arrived, reads = 0
  const gate = new Promise(resolve => { release = resolve })
  const held = new Promise(resolve => { arrived = resolve })
  const delayed = async route => {
    reads++
    const response = await route.fetch()
    arrived()
    await gate
    await route.fulfill({response})
  }
  await parent.waitFor()
  await page.route(pattern, delayed)
  try {
    await parent.click()
    await held
    assert.equal(await parent.getAttribute('aria-expanded'), 'true')
    await parent.press('ArrowLeft')
    assert.equal(await parent.getAttribute('aria-expanded'), 'false')
    release()
    await file('tree-child').waitFor()
    assert.equal(await parent.getAttribute('aria-expanded'), 'false', 'Late navigation and tree responses preserve manual collapse')
    await child.waitFor({state:'detached'})
    await toolbar.getByRole('button', {name:'Refresh', exact:true}).click()
    await browserUI.getByTestId('file-loading').waitFor({state:'detached'})
    assert.equal(await parent.getAttribute('aria-expanded'), 'false', 'Refreshing the listing preserves manual collapse')
    const previous = reads
    await parent.click()
    await child.waitFor()
    await parent.click()
    await child.waitFor({state:'detached'})
    assert.equal(await parent.getAttribute('aria-expanded'), 'false', 'Clicking the current directory toggles its tree without reopening it')
    assert.equal(reads, previous, 'Tree toggles on the current directory do not reload the listing')
    await file('tree-child').click()
    await file('leaf.txt').waitFor()
    assert.equal(await parent.getAttribute('aria-expanded'), 'true', 'Explicit child navigation reveals its ancestors')
    await toolbar.getByRole('button', {name:'Back', exact:true}).click()
    await file('tree-child').waitFor()
    await toolbar.getByRole('button', {name:'Parent directory', exact:true}).click()
    await file('hello.txt').waitFor()
  } finally {
    release()
    await page.unroute(pattern, delayed)
  }
}
