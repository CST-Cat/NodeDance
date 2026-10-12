// Run before the first paint; external script preserves the Core CSP.
(() => {
  let preference = 'system';
  try {
    const saved = localStorage.getItem('nodedance.theme');
    if (['light', 'dark', 'system'].includes(saved)) preference = saved;
  } catch {}
  const theme = preference === 'system'
    ? (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')
    : preference;
  document.documentElement.dataset.themePreference = preference;
  document.documentElement.dataset.theme = theme;
  document.documentElement.style.colorScheme = theme;
  document.documentElement.style.backgroundColor = theme === 'dark' ? '#181715' : '#faf9f5';
  document.querySelector('meta[name=theme-color]')?.setAttribute('content', theme === 'dark' ? '#181715' : '#faf9f5');
})();
