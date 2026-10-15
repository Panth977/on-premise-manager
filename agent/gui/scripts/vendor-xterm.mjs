#!/usr/bin/env node
/**
 * Copies xterm.js (+ fit addon + css) from node_modules into static/assets/vendor/ (round T). They are served by the
 * agent at /_opm/assets/vendor/… (embedded with the GUI, no CDN) and loaded with a dynamic import only on the
 * Terminal page, so the main bundle (bundleStrategy 'single') does not carry them.
 */
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const out = path.join(root, 'static/assets/vendor');
fs.rmSync(out, { recursive: true, force: true });
fs.mkdirSync(out, { recursive: true });
const strip = (s) => s.replace(/\n?\/\/# sourceMappingURL=.*$/m, '\n');
const files = [
  ['node_modules/@xterm/xterm/lib/xterm.mjs', 'xterm.js'],
  ['node_modules/@xterm/addon-fit/lib/addon-fit.mjs', 'xterm-addon-fit.js'],
  ['node_modules/@xterm/xterm/css/xterm.css', 'xterm.css'],
];
for (const [src, dst] of files) fs.writeFileSync(path.join(out, dst), strip(fs.readFileSync(path.join(root, src), 'utf8')));
for (const lic of ['node_modules/@xterm/xterm/LICENSE']) if (fs.existsSync(path.join(root, lic))) fs.copyFileSync(path.join(root, lic), path.join(out, 'LICENSE-xterm.txt'));
console.log('vendor: xterm →', path.relative(root, out));
