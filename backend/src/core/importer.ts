/**
 * Image import (SPEC-final §6.1 Images): registry → Storage, content-addressed (blobs/sha256/<hex>), sha256-verified.
 * copy mode copies manifest + config + every layer; gateway mode copies manifest + config (layers on first agent fetch).
 */
import { registryHost, type Image, type ImageRefParts, type Registry } from '@opm/shared';
import { col, db, nowIso } from './fire.js';
import { exists, paths, streamToStorage, writeBytes, sizeOf } from './storage.js';
import { hexOf, sha256Hex } from './util.js';
import { RegistryClient, RegistryError, type OciConfig, type RegistryCreds } from './registry.js';
import { withAccessHint } from './registry-browse.js';
import { logger } from '../driver/index.js';
import { auditSystem, readAuditDoc } from './audit.js';

/** Registry base URL for a reference host (docker.io → https://registry-1.docker.io). */
export const hostUrl = (host: string) => (host === 'docker.io' ? 'https://registry-1.docker.io' : `https://${host}`);
/**
 * Credentials for pulling `source`: the registry's (its URL host must equal the reference host) or anonymous on the
 * reference host (registryId null = public). Throws RegistryError('host mismatch') / ('registry … no longer exists').
 */
export function sourceCreds(source: ImageRefParts, reg: Registry | null): RegistryCreds {
  if (!reg) return { url: hostUrl(source.host), username: '', password: '' };
  if (registryHost(reg.url) !== source.host) throw new RegistryError(`the registry "${reg.label}" is for ${registryHost(reg.url)}, but the reference is on ${source.host}`);
  return { url: reg.url, username: reg.username, password: reg.password };
}
/** What to ask the registry for: the digest when pinned, else the tag. */
export const sourceRef = (source: ImageRefParts) => source.digest ?? source.tag ?? 'latest';

async function context(img: Pick<Image, 'registryId' | 'source'>) {
  if (!img.source?.repository) throw new Error('the image has no source reference (fix it with PATCH reference while the import is failed)');
  let reg: Registry | null = null;
  if (img.registryId) {
    const r = await col.registries().doc(img.registryId).get();
    if (!r.exists) throw new Error('the registry no longer exists');
    reg = r.data() as Registry;
  }
  const creds = sourceCreds(img.source, reg);
  return { creds, client: new RegistryClient(creds), repo: img.source.repository, ref: sourceRef(img.source) };
}

/** Claim a pending import (pending → copying) in a transaction; false when someone else has it. */
async function claim(serviceSlug: string, version: string): Promise<Image | null> {
  const ref = col.images(serviceSlug).doc(version);
  return db().runTransaction(async (tx) => {
    const s = await tx.get(ref);
    if (!s.exists) return null;
    const img = s.data() as Image;
    if (img.import.status !== 'pending') return null;
    const startedAt = nowIso();
    tx.update(ref, { 'import.status': 'copying', 'import.startedAt': startedAt, 'import.error': null, 'import.finishedAt': null });
    return { ...img, import: { ...img.import, status: 'copying', startedAt } };
  });
}

export async function runImport(serviceSlug: string, version: string): Promise<void> {
  const img = await claim(serviceSlug, version);
  if (!img) return;
  const ref = col.images(serviceSlug).doc(version);
  const target = `services/${serviceSlug}/images/${version}`;
  const before = await readAuditDoc(target).catch(() => null);
  try {
    const { client, repo, ref: pullRef, creds } = await context(img);
    const pm = await withAccessHint(creds, () => client.platformManifest(repo, pullRef, img.platform));
    if (img.digest && img.digest !== pm.digest) throw new Error(`the tag now points to ${pm.digest} (registered digest ${img.digest}); register a new version`);
    const mHex = hexOf(pm.digest);
    if (!(await exists(paths.blob(mHex)))) await writeBytes(paths.blob(mHex), pm.bytes);
    // config (both modes) — verify + check the platform
    const cfgDigest = pm.manifest.config.digest;
    const cfgBytes = await client.blobBytes(repo, cfgDigest);
    if ('sha256:' + sha256Hex(cfgBytes) !== cfgDigest) throw new Error(`config blob sha256 mismatch (${cfgDigest})`);
    const cfg = JSON.parse(cfgBytes.toString('utf8')) as OciConfig;
    const arch = img.platform.split('/')[1];
    if (cfg.architecture && cfg.architecture !== arch) throw new Error(`platform ${img.platform} not in the image (it is ${cfg.os ?? 'linux'}/${cfg.architecture})`);
    if (!(await exists(paths.blob(hexOf(cfgDigest))))) await writeBytes(paths.blob(hexOf(cfgDigest)), cfgBytes);
    const layers = pm.manifest.layers.map((l) => ({ digest: l.digest, sizeBytes: l.size, mediaType: l.mediaType }));
    const total = layers.reduce((a, l) => a + l.sizeBytes, 0);
    await ref.update({ _progress: { bytesDone: 0, bytesTotal: total } });
    if (img.import.mode === 'copy') {
      let done = 0;
      for (const l of layers) {
        const hex = hexOf(l.digest);
        const have = await sizeOf(paths.blob(hex));
        if (have !== l.sizeBytes) {
          const { stream } = await client.blobStream(repo, l.digest);
          await streamToStorage(paths.blob(hex), stream, hex);
        }
        done += l.sizeBytes;
        await ref.update({ _progress: { bytesDone: done, bytesTotal: total } });
      }
    }
    await ref.update({
      digest: pm.digest,
      'import.status': 'ready',
      'import.error': null,
      'import.layers': layers,
      'import.sizeBytes': total,
      'import.configDigest': cfgDigest,
      'import.manifestMediaType': pm.mediaType,
      'import.finishedAt': nowIso(),
      _configSizeBytes: cfgBytes.length,
      _manifestSizeBytes: pm.bytes.length,
      updatedAt: nowIso(),
      updatedBy: 'system',
    });
    logger.info('image imported', { serviceSlug, version, digest: pm.digest, layers: layers.length, total });
    await auditSystem({ job: 'importImage', action: 'image.import-ready', targetType: 'image', target, ok: true, before, after: await readAuditDoc(target).catch(() => null), data: { serviceSlug, version, reference: img.reference, mode: img.import.mode, digest: pm.digest, layers: layers.length, sizeBytes: total } });
  } catch (e) {
    const msg = (e as Error).message?.slice(0, 1000) ?? String(e);
    logger.warn('image import failed', { serviceSlug, version, error: msg });
    await ref.update({ 'import.status': 'failed', 'import.error': msg, 'import.finishedAt': nowIso(), updatedAt: nowIso(), updatedBy: 'system' }).catch(() => undefined);
    await auditSystem({ job: 'importImage', action: 'image.import-failed', targetType: 'image', target, ok: false, errorMessage: msg, before, after: await readAuditDoc(target).catch(() => null), data: { serviceSlug, version, reference: img.reference, mode: img.import.mode, error: msg } });
  }
}

/** Gateway mode: stream a missing layer from the registry into Storage (first agent fetch). */
export async function fetchLayer(img: Image, digest: string): Promise<void> {
  const hex = hexOf(digest);
  if (await exists(paths.blob(hex))) return;
  const { client, repo } = await context(img);
  const { stream } = await client.blobStream(repo, digest);
  await streamToStorage(paths.blob(hex), stream, hex);
}
