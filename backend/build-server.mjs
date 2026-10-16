// opm-cloud bundle (§21 #108): functions/src/server.ts → dist/opm-cloud.cjs (Node 22, CJS). @opm/shared + the business
// code are inlined; firebase-admin / @google-cloud/* stay external (only loaded for adapter=firebase or migrate).
// The GitHub mirror's Go artefacts (built by build.mjs into functions/lib) are copied next to the bundle.
import { build } from 'esbuild';
import { fileURLToPath } from 'node:url';
import { copyFileSync, existsSync, mkdirSync, readFileSync } from 'node:fs';
import path from 'node:path';

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.join(here, 'dist');
mkdirSync(out, { recursive: true });
const version = process.env.OPM_CLOUD_VERSION || JSON.parse(readFileSync(path.join(here, 'package.json'), 'utf8')).version || 'dev';
await build({
  entryPoints: [path.join(here, 'functions/src/server.ts')],
  outfile: path.join(out, 'opm-cloud.cjs'),
  bundle: true,
  platform: 'node',
  target: 'node22',
  format: 'cjs',
  sourcemap: 'linked',
  external: ['firebase-admin', 'firebase-admin/*', 'firebase-functions', 'firebase-functions/*', '@google-cloud/*', 'google-auth-library'],
  alias: { '@opm/shared': path.join(here, '../shared/src/index.ts') },
  define: { 'process.env.OPM_CLOUD_VERSION': JSON.stringify(version) },
  logLevel: 'info',
});
for (const f of ['opm-export.wasm', 'opm-tools.json']) {
  const src = path.join(here, 'functions/lib', f);
  if (existsSync(src)) copyFileSync(src, path.join(out, f));
  else console.warn(`build-server: functions/lib/${f} missing (run npm run build first) — the GitHub mirror cannot export without it`);
}
