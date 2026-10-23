const assert = require('node:assert/strict');
module.exports = async function assertSettingsLayout(page) {
  const root = page.locator('.personal-settings');
  await root.waitFor();
  await page.evaluate(() => document.fonts.ready);
  const controls = await root.evaluate(root => ['.theme-options [role="group"]', '#personalsettings-field-1', '.parameter-select button', '.color-field .parameter-control > div > button'].map(selector => {
    const element = root.querySelector(selector);
    if (!element) throw Error('Missing appearance control: ' + selector);
    const rect = element.getBoundingClientRect();
    return {width: rect.width, right: rect.right};
  }));
  for (const control of controls) {
    assert.ok(control.width > 0);
    assert.ok(Math.abs(control.width - controls[0].width) < 1 && Math.abs(control.right - controls[0].right) < 1, 'Appearance controls align');
  }
  const fields = root.locator('.password-form input');
  assert.equal(await fields.count(), 3);
  for (const field of await fields.all()) {
    await field.focus();
    const style = await field.evaluate(el => {
      const input = getComputedStyle(el), shell = getComputedStyle(el.parentElement), eye = el.nextElementSibling.getBoundingClientRect();
      return {border: input.borderWidth, shadow: input.boxShadow, radius: shell.borderRadius, reference: getComputedStyle(document.querySelector('#personalsettings-field-1')).borderRadius, eyeWidth: eye.width, eyeHeight: eye.height};
    });
    assert.equal(style.border, '0px');assert.equal(style.shadow, 'none');assert.equal(style.radius, style.reference);
    assert.equal(style.eyeWidth, 30);assert.equal(style.eyeHeight, 30);
  }
};
