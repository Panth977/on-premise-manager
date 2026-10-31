/**
 * Agent release + install dependencies in object storage (SPEC §2.3, §19.1) — shared by scripts/upload-agent.ts,
 * scripts/upload-deps.ts (Firebase or --adapter local) and `opm-cloud upload-agent | upload-deps` on a VM (§21 #108).
 * Written against the Bucket-shaped shim so the objects are byte-for-byte what the scripts always uploaded.
 */
import { createHash, createPrivateKey, sign } from 'node:crypto';
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import type { AgentBuildMeta, DepsFile, DepsManifest } from '@opm/shared';
import type { BucketShim } from '../driver/bucket-shim.js';

const OS = ['linux', 'darwin'];
const ARCH = ['amd64', 'arm64'];

export function signHex(hex: string, seedB64: string): string {
  const seed = Buffer.from(seedB64, 'base64');
  // PKCS#8 wrapper for an Ed25519 seed
  const der = Buffer.concat([Buffer.from('302e020100300506032b657004220420', 'hex'), seed]);
  const key = createPrivateKey({ key: der, format: 'der', type: 'pkcs8' });
  return sign(null, Buffer.from(hex, 'utf8'), key).toString('base64url');
}
/** §21 #91 <dir>/release.json → the AgentBuildMeta to store (null when absent); throws on a bad / mismatching file. */
export function readReleaseMeta(dir: string, version: string): AgentBuildMeta | null {
  const f = join(dir, 'release.json');
  if (!existsSync(f)) return null;
  const j = JSON.parse(readFileSync(f, 'utf8')) as Partial<AgentBuildMeta>;
  if (j.version !== version) throw new Error(`${f}: version ${String(j.version)} != --version ${version}`);
  const int = (n: unknown) => Number.isInteger(n) && (n as number) >= 0;
  if (!int(j.schema) || !int(j.minSchema) || (j.minSchema as number) > (j.schema as number)) throw new Error(`${f}: schema / minSchema must be integers with minSchema <= schema`);
  return { version, schema: j.schema!, minSchema: j.minSchema!, commit: typeof j.commit === 'string' ? j.commit : null, builtAt: typeof j.builtAt === 'string' ? j.builtAt : null };
}
export function findBinaries(dir: string): { os: string; arch: string; file: string }[] {
  const out: { os: string; arch: string; file: string }[] = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    const m = /^opm-agent[-_](linux|darwin)[-_](amd64|arm64)$/.exec(name);
    if (m && statSync(p).isFile()) out.push({ os: m[1]!, arch: m[2]!, file: p });
    const d = /^(linux|darwin)-(amd64|arm64)$/.exec(name);
    if (d && statSync(p).isDirectory() && existsSync(join(p, 'opm-agent'))) out.push({ os: d[1]!, arch: d[2]!, file: join(p, 'opm-agent') });
  }
  return out.filter((b) => OS.includes(b.os) && ARCH.includes(b.arch));
}


/** upload agent/<v>/<os>-<arch>/opm-agent + .sha256 + .sig (+ release.json, + agent/latest) — the body of upload-agent */
export async function uploadAgentDir(o: { bucket: BucketShim; dir: string; version: string; latest: boolean; seedB64: string; log?: (s: string) => void }): Promise<{ uploaded: number }> {
  const log = o.log ?? console.log;
  const bucket = o.bucket;
  const bins = findBinaries(o.dir);
  if (!bins.length) throw new Error(`no binaries found in ${o.dir} (opm-agent-<os>-<arch> or <os>-<arch>/opm-agent)`);
  const meta = readReleaseMeta(o.dir, o.version);
  for (const b of bins) {
    const data = readFileSync(b.file);
    const hex = createHash('sha256').update(data).digest('hex');
    const sig = signHex(hex, o.seedB64);
    const base = `agent/${o.version}/${b.os}-${b.arch}/opm-agent`;
    // resumable uploads fail with the gcloud-token OAuth2 client (gaxios "URL is required"); one multipart request handles ~60 MB fine
    await bucket.file(base).save(data, { resumable: false, contentType: 'application/octet-stream' });
    await bucket.file(`${base}.sha256`).save(hex + '\n', { resumable: false, contentType: 'text/plain' });
    await bucket.file(`${base}.sig`).save(sig + '\n', { resumable: false, contentType: 'text/plain' });
    log(`uploaded ${base} (${data.length} bytes, sha256 ${hex})`);
  }
  if (meta) {
    await bucket.file(`agent/${o.version}/release.json`).save(JSON.stringify(meta, null, 2) + '\n', { resumable: false, contentType: 'application/json' });
    log(`uploaded agent/${o.version}/release.json (schema ${meta.schema}, minSchema ${meta.minSchema})`);
  } else log(`no ${join(o.dir, 'release.json')}: schema unknown (legacy 1)`);
  if (o.latest) {
    await bucket.file('agent/latest').save(o.version + '\n', { resumable: false, contentType: 'text/plain' });
    log(`agent/latest = ${o.version}`);
  }
  return { uploaded: bins.length };
}

/** Pinned Docker Engine (static bundle) installed only when Docker is missing; existing Docker 20.10+ is used as is. */
export const DOCKER_VERSION = '28.5.2';
export const MIN_DOCKER_API = '1.41';
const UPSTREAM_ARCH = { amd64: 'x86_64', arm64: 'aarch64' } as const;
/** = RELEASE_NOTES_MAX of src/routes/agent-upgrade.ts */
export const AGENT_NOTES_MAX = 8000;

/** notes to write: given (string; "" = none) wins; undefined → keep the previous notes when the agent version is unchanged. */
export function resolveAgentNotes(given: string | undefined, agentVersion: string | null, prev: DepsManifest | null): string | null {
  if (!agentVersion) return null;
  if (given !== undefined) {
    const t = given.replace(/\r\n/g, '\n').trim();
    if (t.length > AGENT_NOTES_MAX) throw new Error(`agent notes are ${t.length} characters; at most ${AGENT_NOTES_MAX}`);
    return t || null;
  }
  return prev?.agent?.version === agentVersion && typeof prev.agent.notes === 'string' && prev.agent.notes ? prev.agent.notes : null;
}

export const depsPath = (version: string, arch: 'amd64' | 'arm64') => `deps/docker/${version}/${arch}/docker-${version}.tgz`;

async function fetchBundle(version: string, arch: 'amd64' | 'arm64', fromDir?: string): Promise<Buffer> {
  if (fromDir) {
    const f = join(fromDir, `docker-${version}-${arch}.tgz`);
    if (!existsSync(f)) throw new Error(`missing ${f}`);
    return readFileSync(f);
  }
  const url = `https://download.docker.com/linux/static/stable/${UPSTREAM_ARCH[arch]}/docker-${version}.tgz`;
  const r = await fetch(url);
  if (!r.ok) throw new Error(`GET ${url}: ${r.status}`);
  return Buffer.from(await r.arrayBuffer());
}

export async function uploadDeps(o: { bucket: BucketShim; dockerVersion: string; agentVersion: string | null; agentNotes?: string; fromDir?: string; log?: (s: string) => void }): Promise<DepsManifest> {
  const log = o.log ?? console.log;
  // §21 #90 release notes: validated (and the previous manifest read) before anything is uploaded
  const mf = o.bucket.file('deps/manifest.json');
  let prev: DepsManifest | null = null;
  if ((await mf.exists())[0]) {
    try {
      prev = JSON.parse((await mf.download())[0].toString('utf8')) as DepsManifest;
    } catch {
      prev = null;
    }
  }
  const notes = resolveAgentNotes(o.agentNotes, o.agentVersion, prev);
  // §21 #91 schema metadata of the pinned agent (agent/<v>/release.json, written by upload-agent.ts; absent = legacy)
  let meta: { schema?: number; minSchema?: number } | null = null;
  if (o.agentVersion) {
    const rf = o.bucket.file(`agent/${o.agentVersion}/release.json`);
    if ((await rf.exists())[0]) meta = JSON.parse((await rf.download())[0].toString('utf8')) as { schema?: number; minSchema?: number };
  }
  const files = {} as Record<'amd64' | 'arm64', DepsFile>;
  for (const arch of ['amd64', 'arm64'] as const) {
    const path = depsPath(o.dockerVersion, arch);
    const data = await fetchBundle(o.dockerVersion, arch, o.fromDir);
    const sha256 = createHash('sha256').update(data).digest('hex');
    const f = o.bucket.file(path);
    const [ex] = await f.exists();
    const same = ex && (await f.getMetadata())[0]?.metadata?.sha256 === sha256;
    if (!same) await f.save(data, { resumable: false, contentType: 'application/gzip', metadata: { metadata: { sha256 } } });
    files[arch] = { path, sha256, size: data.length };
    log(`${same ? 'kept' : 'uploaded'} ${path} (${data.length} bytes, sha256 ${sha256})`);
  }
  const manifest: DepsManifest = {
    docker: { version: o.dockerVersion, files },
    minDockerApi: MIN_DOCKER_API,
    agent: o.agentVersion ? { version: o.agentVersion, notes, schema: meta?.schema ?? null, minSchema: meta?.minSchema ?? null } : null,
    updatedAt: new Date().toISOString(),
  };
  await o.bucket.file('deps/manifest.json').save(JSON.stringify(manifest, null, 2) + '\n', { resumable: false, contentType: 'application/json' });
  log(`deps/manifest.json written: docker ${o.dockerVersion}, minDockerApi ${MIN_DOCKER_API}, agent ${o.agentVersion ?? '(none)'}, notes ${notes ? `${notes.length} chars${o.agentNotes === undefined ? ' (kept)' : ''}` : '(none)'}`);
  return manifest;
}

