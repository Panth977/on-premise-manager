// Bundles functions/src/index.ts → functions/lib/index.js (CJS, node22).
// @opm/shared is resolved to ../shared/src and INLINED, so Cloud Build never needs the local package.
// firebase-admin / firebase-functions stay external (installed from functions/package.json at deploy).
import { build } from 'esbuild';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const here = path.dirname(fileURLToPath(import.meta.url));
await build({
  entryPoints: [path.join(here, 'functions/src/index.ts')],
  outfile: path.join(here, 'functions/lib/index.js'),
  bundle: true,
  platform: 'node',
  target: 'node22',
  format: 'cjs',
  sourcemap: 'linked',
  external: ['firebase-admin', 'firebase-admin/*', 'firebase-functions', 'firebase-functions/*'],
  alias: { '@opm/shared': path.join(here, '../shared/src/index.ts') },
  logLevel: 'info',
});

// ───────────────────────────────────────── §21 #106 Go artefacts of the GitHub mirror (built from ../agent)
//   functions/lib/opm-export.wasm               the exporter (GOOS=wasip1), run by Cloud Functions through node:wasi
//   functions/lib/opm-render-linux-{amd64,arm64} the runtime helper committed into every branch (opm/bin)
//   functions/lib/opm-tools.json                { version, sha256 by artefact }
// Reproducible: -trimpath, no build id, no VCS stamp. Missing Go / commands → a warning (the mirror then refuses to export).
{
  const { execFileSync } = await import('node:child_process');
  const { existsSync, readFileSync, writeFileSync, mkdirSync } = await import('node:fs');
  const { createHash } = await import('node:crypto');
  const agent = path.join(here, '../agent');
  const lib = path.join(here, 'functions/lib');
  mkdirSync(lib, { recursive: true });
  const flags = ['build', '-trimpath', '-buildvcs=false', '-ldflags=-s -w -buildid='];
  const jobs = [
    { cmd: 'cmd/opm-export', out: 'opm-export.wasm', env: { GOOS: 'wasip1', GOARCH: 'wasm', CGO_ENABLED: '0' } },
    { cmd: 'cmd/opm-render', out: 'opm-render-linux-amd64', env: { GOOS: 'linux', GOARCH: 'amd64', CGO_ENABLED: '0' } },
    { cmd: 'cmd/opm-render', out: 'opm-render-linux-arm64', env: { GOOS: 'linux', GOARCH: 'arm64', CGO_ENABLED: '0' } },
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
  meta.version = meta.artefacts['opm-render-linux-amd64'] ? `render-${meta.artefacts['opm-render-linux-amd64'].slice(0, 12)}` : 'none';
  writeFileSync(path.join(lib, 'opm-tools.json'), JSON.stringify(meta, null, 2) + '\n');
}
