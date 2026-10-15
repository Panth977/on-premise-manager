// §21 #101 — generates volume-content-config.json: hello-config with tree / single seeds + persist (normalized by
// shared normalizeSiteVolume) and its TS revision hash (sha256 of the JCS). Go: TestVolumeContentRevisionHash.
// Run (after `cd shared && npm run build`): node agent/internal/model/testdata/gen-volume-content-config.mjs
import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
const here = dirname(fileURLToPath(import.meta.url));
const shared = join(here, '../../../../shared');
const { canonicalize } = await import(join(shared, 'dist/canonical.js'));
const { normalizeSiteVolume } = await import(join(shared, 'dist/validate.js'));
const H = (s) => 'sha256:' + createHash('sha256').update(s).digest('hex');
const e = (path, body, mode = '0644', render = 'none') => ({ path, sha256: H(body), size: Buffer.byteLength(body), mode, render });
const cfg = JSON.parse(readFileSync(join(shared, 'testdata/hello-config.json'), 'utf8')).config;
const app = cfg.sites.find((s) => s.siteSlug === 'app');
app.config.volumes.push(
  normalizeSiteVolume({
    volumeSlug: 'nodered-data', label: 'Node-RED', description: '', backup: true, location: { kind: 'auto' }, kind: 'dir',
    seed: { kind: 'tree', entries: [e('settings.js', 'module.exports = { uiPort: 1880 }\n', '0644', 'expressions'), e('flows.json', '[]'), e('lib/ünï/x.sh', '#!/bin/sh\n', '0755')], dirs: ['empty/dir', 'lib'] },
    persist: { exclude: ['node_modules/', '*.log'], maxBytes: 52428800, auto: true },
  }),
  normalizeSiteVolume({ volumeSlug: 'one', label: 'One', description: '', backup: false, location: { kind: 'auto' }, kind: 'file', seed: { kind: 'single', sha256: H('x=1\n'), size: 4, mode: '0600', render: 'none' } }),
);
const hash = H(canonicalize(cfg));
writeFileSync(join(here, 'volume-content-config.json'), JSON.stringify({ hash, config: cfg }, null, 1) + '\n');
console.log(hash);
