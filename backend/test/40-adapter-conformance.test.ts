/**
 * §21 #108 conformance: the DocStore / BlobStore suite against the local adapter (fresh temp data dir, strict index
 * registry).
 */
import { mkdtempSync, rmSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { conformance, CONFORMANCE_INDEXES } from './conformance.ts';

const TMP = join(import.meta.dirname, '.tmp');

conformance('local', async () => {
  mkdirSync(TMP, { recursive: true });
  const dir = mkdtempSync(join(TMP, 'conf-'));
  const { localPorts } = await import('../functions/src/adapters/local/index.js');
  const p = localPorts({ dataDir: dir, publicUrl: 'http://127.0.0.1:1', strict: true, indexes: CONFORMANCE_INDEXES });
  return {
    store: p.store,
    blobs: p.blobs,
    done: async () => {
      await p.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
});
