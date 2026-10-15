// Edge-parity helper (round E): runs the shared nginx importer on the client nginx config and writes the imported edge
// (replace mode, certificate bound to the OPM file "client-crt" + secret "client-key") as JSON.
//
//   node e2e/edge-parity/import.mjs <nginx.conf> <out.json>
//
// Containers are named like the fixture upstreams (ez-integration:8080 … nodered:1880, northwind-web:80), one port
// "http" each. Exit code 3 = the importer is not implemented yet (the Go test then falls back to edge-vectors.json).
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const [, , confPath, outPath] = process.argv;
if (!confPath || !outPath) {
  console.error('usage: node import.mjs <nginx.conf> <out.json>');
  process.exit(2);
}
const dist = new URL('../../shared/dist/nginx-import.js', import.meta.url);
let mod;
try {
  mod = await import(fileURLToPath(dist));
} catch (e) {
  console.error(`shared/dist/nginx-import.js not found (run "cd shared && npm test"): ${e.message}`);
  process.exit(3);
}
export const CONTAINERS = [
  ['ez-integration', 8080], ['ez-dc', 8080], ['ez-worker', 8080], ['ez-pg-listen', 8080], ['ez-terminal', 8080], ['ez-migrate', 8080],
  ['ez-admin', 8080], ['northwind-admin', 3000], ['ez-heatmap', 8080], ['pg-admin', 80], ['nodered', 1880], ['northwind-web', 80],
].map(([containerSlug, port]) => ({ containerSlug, ports: [{ portName: 'http', port, edge: true }] }));
let res;
try {
  res = mod.importNginx(readFileSync(confPath, 'utf8'), { containers: CONTAINERS });
} catch (e) {
  console.error(`importer failed: ${e.message}`);
  process.exit(/not implemented/i.test(String(e.message)) ? 3 : 1);
}
const certs = Object.fromEntries((res.certs ?? []).map((c) => [c.hostname, { certFileSlug: 'client-crt', keySecretSlug: 'client-key' }]));
const empty = { httpPort: 80, httpsPort: 443, maxBodyBytes: null, hosts: [], routes: [] };
const edge = mod.applyNginxImport(empty, res, 'replace', certs);
writeFileSync(outPath, JSON.stringify({ edge, summary: res.summary, rows: res.rows.length, parseErrors: res.parseErrors }, null, 2) + '\n');
console.log(JSON.stringify(res.summary));
