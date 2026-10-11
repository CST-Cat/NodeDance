const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')
const ts = require('typescript')
const vue = require('vue')
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
    else if (entry.isFile() && /\.(vue|ts)$/.test(file)) {
      const text = fs.readFileSync(file, 'utf8')
      for (const m of text.matchAll(/\bt\(['"]([^'"]+)['"]/g)) assert.ok(Object.hasOwn(en, m[1]), `${file}: unknown key ${m[1]}`)
      // Endonyms are the sole literal Chinese UI strings; all other text lives in catalogs.
      assert.ok(!/\p{Script=Han}/u.test(text.replace('简体中文', '').replace("'语言'", '')), `${file}: literal Chinese outside locale catalog`)
    }
  }
}
scan(source)
async function runtime(saved, language, storageDisabled = false) {
  const document = {documentElement: {lang: ''}}
  const values = new Map(saved ? [['nodedance.language', saved]] : [])
  const module = {exports: {}}
  const code = ts.transpileModule(fs.readFileSync(path.join(source, 'i18n.ts'), 'utf8'), {compilerOptions: {module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true}}).outputText
  vm.runInNewContext(code, {exports: module.exports, module, require: id => id === 'vue' ? vue : id.includes('zh-CN') ? zh : en,
    document, navigator: {language}, localStorage: {getItem: k => {if(storageDisabled) throw Error('disabled');return values.get(k)}, setItem: (k,v) => {if(storageDisabled) throw Error('disabled');values.set(k,v)}}})
  const api = module.exports
  assert.equal(api.locale.value, saved || (language.startsWith('zh') ? 'zh-CN' : 'en'))
  assert.equal(document.documentElement.lang, api.locale.value)
  const key = Object.keys(en).find(k => slots(en[k]).includes('0'))
  api.setLocale('en');await vue.nextTick()
  assert.equal(api.t(key, {'0': '<script>alert(1)</script>'}).includes('<script>alert(1)</script>'), true, 'Parameters must remain plain text')
  assert.equal(document.documentElement.lang, 'en')
  api.setLocale('zh-CN');await vue.nextTick()
  assert.equal(document.documentElement.lang, 'zh-CN')
  assert.equal(api.display('invalid credentials'), '凭据无效')
  assert.equal(api.display('user-data-that-is-not-a-message'), 'user-data-that-is-not-a-message')
  assert.equal(api.display(en[key].replaceAll('{0}', '42')), api.t(key, {'0': '42'}))
  if (!storageDisabled) assert.equal(values.get('nodedance.language'), 'zh-CN')
  api.setLocale('en');await vue.nextTick()
  assert.equal(api.display('凭据无效'), 'invalid credentials')
  const webhookKey = Object.keys(en).find(k => en[k].includes('{{event}}'))
  assert.ok(api.t(webhookKey).includes('{{event}}'), 'Webhook templates must be preserved')
}
Promise.all([runtime('en', 'zh-CN'), runtime(null, 'zh-CN'), runtime(null, 'en-US', true)])
  .then(() => console.log(`i18n: ${Object.keys(en).length} bilingual messages; persistence, fallback, interpolation verified`))
  .catch(e => {console.error(e);process.exitCode=1})
