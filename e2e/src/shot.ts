// Usage: tsx src/shot.ts <url> <out.png> [clicks]  — screenshot a page (optionally click #inc N times first).
import { chromium } from 'playwright';
const [url, out, clicks = '0'] = process.argv.slice(2);
const b = await chromium.launch();
const p = await b.newPage({ viewport: { width: 1000, height: 640 } });
await p.goto(url);
await p.waitForSelector('#count[data-loaded="1"]', { timeout: 10000 }).catch(() => {});
for (let i = 0; i < Number(clicks); i++) { const before = await p.textContent('#count'); await p.click('#inc'); await p.waitForFunction((b) => document.querySelector('#count')?.textContent !== b, before); }
console.log(JSON.stringify({ theme: await p.getAttribute('body', 'data-theme'), bg: await p.evaluate(() => getComputedStyle(document.body).backgroundColor), count: await p.textContent('#count') }));
await p.screenshot({ path: out });
await b.close();
