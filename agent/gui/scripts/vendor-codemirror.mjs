#!/usr/bin/env node
/**
 * Builds the dev tools CodeMirror 6 bundle (§21 #97) into static/assets/vendor/codemirror.js with esbuild. Served by
 * the agent at /_opm/assets/vendor/codemirror.js (embedded with the GUI, no CDN) and imported at run time only by the
 * dev tools pages (lib/devtools/cm/load.ts), so the single main bundle (bundleStrategy 'single') does not carry it.
 * Run after vendor-xterm.mjs (which recreates the vendor folder).
 */
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { build } from 'esbuild';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const out = path.join(root, 'static/assets/vendor');
fs.mkdirSync(out, { recursive: true });
const r = await build({
  entryPoints: [path.join(root, 'src/lib/devtools/cm/bundle.ts')],
  bundle: true,
  format: 'esm',
  target: 'es2022',
  minify: true,
  legalComments: 'none',
  outfile: path.join(out, 'codemirror.js'),
  metafile: true,
  logLevel: 'warning',
});
const lic = 'CodeMirror 6 (@codemirror/*, @lezer/*) — MIT License, Copyright (C) 2018-2021 by Marijn Haverbeke <marijn@haverbeke.berlin> and others\n';
fs.writeFileSync(path.join(out, 'LICENSE-codemirror.txt'), lic + (fs.existsSync(path.join(root, 'node_modules/@codemirror/state/LICENSE')) ? '\n' + fs.readFileSync(path.join(root, 'node_modules/@codemirror/state/LICENSE'), 'utf8') : ''));
const bytes = Object.values(r.metafile.outputs)[0]?.bytes ?? 0;
console.log('vendor: codemirror →', path.relative(root, out), `(${bytes} B)`);
