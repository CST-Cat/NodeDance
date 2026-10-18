export function describeSessionDevice(raw: string) {
  const ua = raw.trim();
  const terminal = /^NodeDance CLI\/TUI$/i.test(ua);
  const tablet = /iPad|Tablet|PlayBook|Silk/i.test(ua) || /Android/i.test(ua) && !/Mobile/i.test(ua);
  const phone = /iPhone|iPod|Android|Windows Phone|Mobile/i.test(ua);
  const system = /Android[ /]([\d.]+)/i.exec(ua);
  const ios = /(?:CPU (?:iPhone )?OS|iPhone OS)[ /]([\d_]+)/i.exec(ua);
  const os = /Windows Phone/i.test(ua) ? 'Windows Phone'
    : system ? `Android ${system[1].split('.')[0]}`
    : /Android/i.test(ua) ? 'Android'
    : ios ? `${/iPad/i.test(ua) ? 'iPadOS' : 'iOS'} ${ios[1].split('_')[0]}`
    : /iPad/i.test(ua) ? 'iPadOS'
    : /Windows/i.test(ua) ? 'Windows'
    : /CrOS/i.test(ua) ? 'ChromeOS'
    : /Macintosh|Mac OS X/i.test(ua) ? 'macOS'
    : /Linux/i.test(ua) ? 'Linux' : '';
  const browsers: [RegExp, string][] = [
    [/Edg(?:e|A|iOS)?\/([\d]+)/i, 'Edge'],
    [/(?:OPR|OPiOS|Opera)\/([\d]+)/i, 'Opera'],
    [/SamsungBrowser\/([\d]+)/i, 'Samsung Internet'],
    [/HeadlessChrome\/([\d]+)/i, 'Chrome Headless'],
    [/(?:Chrome|CriOS)\/([\d]+)/i, 'Chrome'],
    [/(?:Firefox|FxiOS)\/([\d]+)/i, 'Firefox'],
    [/Version\/([\d]+).*Safari\//i, 'Safari'],
  ];
  let browser = '';
  for (const [pattern, name] of browsers) {
    const match = pattern.exec(ua);
    if (match) { browser = `${name} ${match[1]}`; break; }
  }
  const kind = terminal ? 'terminal' : tablet ? 'tablet' : phone ? 'phone' : os || browser ? 'computer' : 'unknown';
  return { kind, os, browser };
}
