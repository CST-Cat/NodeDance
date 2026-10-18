const assert=require('node:assert/strict');
(async()=>{const {describeSessionDevice:parse}=await import(require('node:url').pathToFileURL(require('node:path').resolve(__dirname,'../src/session-device.ts')).href);
const cases=[
['Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/154.0.0.0 Safari/537.36',{kind:'computer',os:'macOS',browser:'Chrome 154'}],
['Mozilla/5.0 (Linux; Android 16; 2602BRT18C Build/BP2A.250605.031.A3) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.8010.39 Mobile Safari/537.36',{kind:'phone',os:'Android 16',browser:'Chrome 153'}],
['Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/153.0.0.0 Safari/537.36 Edg/153.0.0.0',{kind:'computer',os:'Windows',browser:'Edge 153'}],
['Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1',{kind:'phone',os:'iOS 18',browser:'Safari 18'}],
['Mozilla/5.0 (iPad; CPU OS 18_0 like Mac OS X) Version/18.0 Mobile/15E148 Safari/604.1',{kind:'tablet',os:'iPadOS 18',browser:'Safari 18'}],
['Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) CriOS/153.0 Mobile/15E148 Safari/604.1',{kind:'phone',os:'iOS 18',browser:'Chrome 153'}],
['Mozilla/5.0 (Linux; Android 15; Tablet) Chrome/153.0 Safari/537.36',{kind:'tablet',os:'Android 15',browser:'Chrome 153'}],
['Mozilla/5.0 (X11; CrOS x86_64 16267.0.0) Chrome/153.0 Safari/537.36',{kind:'computer',os:'ChromeOS',browser:'Chrome 153'}],
['Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/153.0',{kind:'computer',os:'Linux',browser:'Firefox 153'}],
['NodeDance CLI/TUI',{kind:'terminal',os:'',browser:''}],
['',{kind:'unknown',os:'',browser:''}],
['<img src=x onerror=alert(1)>',{kind:'unknown',os:'',browser:''}],
];for(const [ua,want]of cases)assert.deepEqual(parse(ua),want);console.log('PASS',cases.length,'realistic device summaries');})();
