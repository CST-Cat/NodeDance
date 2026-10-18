// Verify the settings spacing contract using rendered geometry, not screenshots.
const assert = require('node:assert/strict');
module.exports = async function assertSettingsSpacing(page) {
  await page.evaluate(() => document.fonts.ready);
  const cards = await page.locator('.settings-card').evaluateAll(elements => elements.map(card => {
    const heading = card.querySelector(':scope > .section-heading, :scope > .settings-list-heading');
    const body = card.querySelector(':scope > .profile-form, :scope > .password-form, :scope > .settings-list-body');
    const list = card.querySelector('.settings-record-list');
    const rows = [...card.querySelectorAll('.settings-record-row')];
    return {
      padding: getComputedStyle(card).padding,
      gap: heading && body ? body.getBoundingClientRect().top - heading.getBoundingClientRect().bottom : null,
      listGap: list ? getComputedStyle(list).rowGap : null,
      rowMinimums: rows.map(row => getComputedStyle(row).minHeight),
      detailGaps: [...card.querySelectorAll(".settings-record-info")].map(info => getComputedStyle(info).rowGap),
      title: heading?.querySelector('h3')?.getBoundingClientRect().toJSON(),
      listCard: card.classList.contains('settings-list-card'),
    };
  }));
  assert.ok(cards.length >= 3, 'Settings cards must be rendered');
  for (const card of cards) {
    assert.equal(card.padding, '16px', 'Every settings card shares the same inset');
    assert.notEqual(card.gap, null, 'Every settings card has a heading and body');
    assert.ok(Math.abs(card.gap - 16) < 1, JSON.stringify(card));
    for (const gap of card.detailGaps) assert.equal(gap, "0px", "Same-group lines must not inherit record spacing");
    if (card.listGap !== null) assert.equal(card.listGap, '16px');
    for (const minimum of card.rowMinimums) assert.ok(['0px', 'auto'].includes(minimum), 'Content rows must not be padded with minimum height');
  }
  const paired = cards.filter(card => card.listCard);
  if (paired.length === 2 && paired[1].title.x > paired[0].title.right) {
    assert.ok(Math.abs(paired[0].title.y - paired[1].title.y) < 1, 'Paired titles share an intrinsic header row');
  }
};
