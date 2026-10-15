import { test, expect } from '@playwright/test';
import { existsSync } from 'node:fs';
import { resolve } from 'node:path';
import { scanBundle } from '../scripts/check-bundle-text.mjs';

/**
 * SPEC-final §21 #84 (g): the built console never says "shown once" / "only once" / "one-time" / "I saved".
 * Scans frontend/build (run `npm run build` first, or `npm run test:bundle` = build + this scan as a node script).
 */
test('built bundle has no show-once text', () => {
  const dir = resolve(process.cwd(), 'build');
  test.skip(!existsSync(dir), 'no build/ — run npm run build');
  const { files, hits } = scanBundle(dir);
  expect(files).toBeGreaterThan(0);
  expect(hits.map((h) => `${h.file}: ${h.match} … ${h.context}`)).toEqual([]);
});
