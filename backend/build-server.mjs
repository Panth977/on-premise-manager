// opm-cloud bundle (§21 #108): functions/src/server.ts → dist/opm-cloud.cjs (Node 22, CJS). @opm/shared + the business
// code are inlined. The GitHub mirror's Go artefacts go next to the bundle.
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
  alias: { '@opm/shared': path.join(here, '../shared/src/index.ts') },
  define: { 'process.env.OPM_CLOUD_VERSION': JSON.stringify(version) },
  logLevel: 'info',
});

// ───────────────────────────────────────── §21 #106 Go artefacts of the GitHub mirror (built from ../agent)
//   dist/opm-export.wasm               the exporter (GOOS=wasip1), run through node:wasi
//   dist/opm-tools.json                { version, sha256 by artefact }
// (the exported trees run with Docker alone: no binary goes into them, §21 #115)
// Reproducible: -trimpath, no build id, no VCS stamp. Missing Go / commands → a warning (the mirror then refuses to export).
{
  const { execFileSync } = await import('node:child_process');
  const { existsSync, readFileSync, writeFileSync } = await import('node:fs');
  const { createHash } = await import('node:crypto');
  const agent = path.join(here, '../agent');
  const lib = out;
  const flags = ['build', '-trimpath', '-buildvcs=false', '-ldflags=-s -w -buildid='];
  const jobs = [
    { cmd: 'cmd/opm-export', out: 'opm-export.wasm', env: { GOOS: 'wasip1', GOARCH: 'wasm', CGO_ENABLED: '0' } },
  ];
  const meta = { version: '', artefacts: {} };
  if (process.env.OPM_SKIP_GO === '1') console.warn('build: OPM_SKIP_GO=1 — Go artefacts of the GitHub mirror not rebuilt');
  else
    for (const j of jobs) {
      if (!existsSync(path.join(agent, j.cmd))) {
        console.warn(`build: WARNING agent/${j.cmd} does not exist yet — ${j.out} not built (the GitHub mirror cannot export without it)`);
        continue;
      }
      try {
        execFileSync('go', [...flags, '-o', path.join(lib, j.out), `./${j.cmd}`], { cwd: agent, stdio: ['ignore', 'inherit', 'inherit'], env: { ...process.env, GOTOOLCHAIN: process.env.GOTOOLCHAIN ?? 'auto', GOFLAGS: '', ...j.env } });
      } catch (e) {
        console.warn(`build: WARNING go build ${j.cmd} (${j.out}) failed: ${e.message}`);
      }
    }
  for (const j of jobs) {
    const p = path.join(lib, j.out);
    if (existsSync(p)) meta.artefacts[j.out] = createHash('sha256').update(readFileSync(p)).digest('hex');
  }
  meta.version = meta.artefacts['opm-export.wasm'] ? `export-${meta.artefacts['opm-export.wasm'].slice(0, 12)}` : 'none';
  writeFileSync(path.join(lib, 'opm-tools.json'), JSON.stringify(meta, null, 2) + '\n');
}
