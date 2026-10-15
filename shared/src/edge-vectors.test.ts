import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { resolveEdgeRequest, edgeVectorView } from './edge-resolve.js';
import { validateEdge, Issues, normalizeEdge } from './validate.js';
import type { Edge } from './types.js';

interface Vec {
  edges: Record<string, Edge>;
  ports: Record<string, Record<string, Record<string, number>>>;
  cases: { edge: string; url: string; method?: string; bodyBytes?: number; upgrade?: boolean; note?: string; expect: unknown }[];
}
const V = JSON.parse(readFileSync(fileURLToPath(new URL('../testdata/edge-vectors.json', import.meta.url)), 'utf8')) as Vec;

test('edge vectors: edges are valid and normalized', () => {
  for (const [name, e] of Object.entries(V.edges)) {
    assert.deepEqual(normalizeEdge(e as never), e, `${name} is in the round E shape`);
    const is = new Issues();
    validateEdge(e, { agentPort: 7080, agentGuiAliasPort: 8080 }, is);
    assert.deepEqual(is.list.filter((x) => x.severity === 'error'), [], name);
  }
});

for (const c of V.cases) {
  test(`edge vector ${c.edge} ${c.method ?? 'GET'} ${c.url}${c.bodyBytes ? ` body ${c.bodyBytes}` : ''}${c.upgrade ? ' upgrade' : ''}`, () => {
    const r = resolveEdgeRequest(V.edges[c.edge]!, c.url, { method: c.method, ports: V.ports[c.edge], bodyBytes: c.bodyBytes, upgrade: c.upgrade });
    assert.deepEqual(edgeVectorView(r), c.expect);
  });
}
