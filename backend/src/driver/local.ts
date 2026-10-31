/**
 * The local adapter set (SPEC §21 #108: OPM on a VM without Firebase) — SQLite DocStore, filesystem BlobStore,
 * built-in Auth. Layout + semantics: README.md in this directory.
 */
import { existsSync, mkdirSync, readdirSync } from 'node:fs';
import { join, relative } from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import type { Ports } from './index.js';
import { TRIGGER_SPECS } from '../core/trigger-specs.js';
import { LocalDocStore, type OutboxTrigger } from './docstore.js';
import { LocalBlobStore } from './blobstore.js';
import { LocalAuth } from './auth.js';
import { INDEX_REGISTRY, mergeRegistry, type IndexRegistry } from './registry.js';

export { LocalDocStore, LocalBlobStore, LocalAuth, INDEX_REGISTRY, type IndexRegistry, type OutboxTrigger };
export { startOutboxDispatcher, sweepDocTtl } from './outbox.js';
export { handleBlobHttp, type BlobHttpResponse } from './blobstore.js';
export { handleAuthHttp, type AuthHttpOptions, type AuthHttpRequest, type AuthHttpResponse, type OidcConfig } from './auth-http.js';

export interface LocalConfig {
  dataDir: string;
  /** origin (+ base path) of opm-cloud: signed blob URLs point at `${publicUrl}/_blob/…` */
  publicUrl: string;
  /** console origin for reset links / OIDC redirects (default publicUrl) */
  consoleUrl?: string;
  /** outbox trigger patterns (default TRIGGER_SPECS) */
  triggers?: OutboxTrigger[];
  /** throw on queries over fields without an index column (default env OPM_LOCAL_STRICT=1) */
  strict?: boolean;
  /** extra index-registry entries (tests, new collections) */
  indexes?: IndexRegistry;
}

export function localPorts(cfg: LocalConfig): Ports & { store: LocalDocStore; blobs: LocalBlobStore; auth: LocalAuth; close(): Promise<void> } {
  mkdirSync(cfg.dataDir, { recursive: true });
  const publicUrl = cfg.publicUrl.replace(/\/+$/, '');
  const store = new LocalDocStore({
    dataDir: cfg.dataDir,
    registry: mergeRegistry(INDEX_REGISTRY, cfg.indexes),
    strict: cfg.strict ?? process.env.OPM_LOCAL_STRICT === '1',
    triggers: (cfg.triggers ?? TRIGGER_SPECS).map((t) => ({ name: t.name, kind: t.kind, pattern: t.pattern })),
  });
  const blobs = new LocalBlobStore(cfg.dataDir, publicUrl);
  const auth = new LocalAuth(store, cfg.dataDir, (cfg.consoleUrl ?? publicUrl).replace(/\/+$/, ''));
  return { store, blobs, auth, close: () => store.close() };
}

/**
 * Consistent online copy of every .sqlite file under <dataDir>/db (`VACUUM INTO`, safe while opm-cloud runs) into
 * outDir with the same relative layout. Returns the written files. Existing targets are an error.
 */
export async function backupSqlite(dataDir: string, outDir: string): Promise<string[]> {
  const src = join(dataDir, 'db');
  const files: string[] = [];
  const walk = (d: string) => {
    if (!existsSync(d)) return;
    for (const e of readdirSync(d, { withFileTypes: true })) {
      const p = join(d, e.name);
      if (e.isDirectory()) walk(p);
      else if (e.isFile() && e.name.endsWith('.sqlite')) files.push(p);
    }
  };
  walk(src);
  const out: string[] = [];
  for (const f of files.sort()) {
    const dst = join(outDir, relative(src, f));
    if (existsSync(dst)) throw new Error(`backup target exists: ${dst}`);
    mkdirSync(join(dst, '..'), { recursive: true });
    const db = new DatabaseSync(f);
    try {
      db.exec('PRAGMA busy_timeout = 5000');
      db.prepare('VACUUM INTO ?').run(dst);
    } finally {
      db.close();
    }
    out.push(dst);
  }
  return out;
}
