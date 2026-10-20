const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')
const ts = require('typescript')
const signals = require('@preact/signals-react')
const source = path.resolve(__dirname, '../src')
const en = require('../src/locales/en.json')
const zh = require('../src/locales/zh-CN.json')
assert.deepEqual(Object.keys(en).sort(), Object.keys(zh).sort(), 'Locale keys differ')
const slots = value => [...value.matchAll(/(?<!\{)\{(\d+)\}(?!\})/g)].map(m => m[1]).sort()
for (const key of Object.keys(en)) {
  assert.ok(en[key].trim() && zh[key].trim(), `Empty translation: ${key}`)
  assert.deepEqual(slots(en[key]), slots(zh[key]), `Interpolation mismatch: ${key}`)
  assert.ok(!/\p{Script=Han}/u.test(en[key]), `Untranslated English: ${key}`)
}
function scan(dir) {
  for (const entry of fs.readdirSync(dir, {withFileTypes: true})) {
    const file = path.join(dir, entry.name)
    if (entry.isDirectory() && entry.name !== 'locales') scan(file)
    else if (entry.isFile() && /\.(tsx?|ts)$/.test(file)) {
      const text = fs.readFileSync(file, 'utf8')
      for (const m of text.matchAll(/\bt\(['"]([^'"]+)['"]/g)) assert.ok(Object.hasOwn(en, m[1]), `${file}: unknown key ${m[1]}`)
      // Endonyms are the sole literal Chinese UI strings; all other text lives in catalogs.
      assert.ok(!/\p{Script=Han}/u.test(text.replace('简体中文', '').replace(/['"]语言['"]/g, '')), `${file}: literal Chinese outside locale catalog`)
    }
  }
}
scan(source)
async function runtime(saved, languages, storageDisabled = false) {
  const document = {documentElement: {lang: ''}}, navigator = {language: languages[0], languages}
  const values = new Map(saved ? [['nodedance.locale', saved]] : []), listeners = new Map()
  const module = {exports: {}}
  const code = ts.transpileModule(fs.readFileSync(path.join(source, 'i18n.ts'), 'utf8'), {compilerOptions: {module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true}}).outputText
  vm.runInNewContext(code, {exports: module.exports, module, require: id => id === '@preact/signals-react' ? signals : id.includes('zh-CN') ? zh : en,
    document, navigator, window: {addEventListener: (name,listener)=>listeners.set(name,listener)}, localStorage: {getItem: k => {if(storageDisabled) throw Error('disabled');return values.get(k)}, setItem: (key,value) => {if(storageDisabled)throw Error('disabled');assert.equal(key,'nodedance.locale');values.set(key,value)}}})
  const api = module.exports
  const supported=languages.find(language=>/^(zh|en)(-|$)/i.test(language))
  const browserExpected=supported?.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en'
  assert.equal(api.locale.value,!storageDisabled&&['en','zh-CN'].includes(saved)?saved:browserExpected)
  assert.equal(document.documentElement.lang, api.locale.value)
  async function browserLanguage(language){navigator.language=language;navigator.languages=[language];listeners.get('languagechange')();await Promise.resolve()}
  api.setLanguagePreference('system');await Promise.resolve()
  const key = Object.keys(en).find(k => slots(en[k]).includes('0'))
  await browserLanguage('en-US')
  assert.equal(api.t(key, {'0': '<script>alert(1)</script>'}).includes('<script>alert(1)</script>'), true, 'Parameters must remain plain text')
  assert.equal(document.documentElement.lang, 'en')
  await browserLanguage('zh-TW')
  assert.equal(document.documentElement.lang, 'zh-CN')
  assert.equal(api.display('invalid credentials'), '凭据无效')
  assert.equal(api.display('user-data-that-is-not-a-message'), 'user-data-that-is-not-a-message')
  assert.equal(api.display(en[key].replaceAll('{0}', '42')), api.t(key, {'0': '42'}))
  await browserLanguage('en')
  assert.equal(api.display('凭据无效'), 'invalid credentials')
  api.setLanguagePreference('zh-CN');await Promise.resolve()
  assert.equal(api.locale.value,'zh-CN')
  await browserLanguage('en-US');assert.equal(api.locale.value,'zh-CN','Explicit preference survives browser language changes')
  if(!storageDisabled)assert.equal(values.get('nodedance.locale'),'zh-CN')
  api.setLanguagePreference('system');await Promise.resolve()
  assert.equal(api.locale.value,'en')

  const webhookKey = Object.keys(en).find(k => en[k].includes('{{event}}'))
  assert.ok(api.t(webhookKey).includes('{{event}}'), 'Webhook templates must be preserved')
}
Promise.all([runtime('en', ['zh-CN']),runtime('zh-CN',['en-US']),runtime(null,['fr-FR','zh-HK','en']),runtime(null,['de-DE']),runtime(null,[],true)])
  .then(() => console.log(`i18n: ${Object.keys(en).length} bilingual messages; browser defaults, saved preferences, languagechange, storage fallback and interpolation verified`))
  .catch(e => {console.error(e);process.exitCode=1})
