/**
 * §21 #106 the exporter: ExportRequest assembly (never a secret value) and the run of the Go exporter
 * (`opm-export.wasm`, GOOS=wasip1, built by backend/build.mjs from agent/cmd/opm-export) through node:wasi:
 * stdin = ExportRequest JSON, stdout = ExportResult JSON. The compiled module is cached per instance.
 *
 * Tool binaries put into every tree (opm/bin): functions/lib/opm-render-linux-{amd64,arm64} (+ opm-tools.json).
 * EMULATOR ONLY: githubMirror/config._stubExporter = true uses a small TS stand-in (queue / push tests without Go).
 */
import { createHash } from 'node:crypto';
import { existsSync, readFileSync, mkdtempSync, writeFileSync, openSync, closeSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import {
  canonicalize,
  compareUtf16,
  imageKey,
  MIRROR_EXPORT_FORMAT,
  type Commit,
  type CommitAuthor,
  type Config,
  type ExportFile,
  type ExportImageRef,
  type ExportPersistSnapshot,
  type ExportRequest,
  type ExportResult,
  type Image,
  type RevisionCatalog,
} from '@opm/shared';
import { col } from './fire.js';
import { readText } from './storage.js';
import { hexOf } from './util.js';
import { imageSpec, loadCatalog, type Catalog } from './catalog.js';
import { env } from './env.js';

// ───────────────────────────────────────── artefacts
function libDirs(): string[] {
  const out: string[] = [];
  if (process.env.OPM_EXPORT_DIR) out.push(process.env.OPM_EXPORT_DIR);
  if (typeof __dirname !== 'undefined') out.push(__dirname, path.resolve(__dirname, '../lib'), path.resolve(__dirname, '../../lib'));
  out.push(path.resolve(process.cwd(), 'functions/lib'), path.resolve(process.cwd(), 'lib'), path.resolve(process.cwd(), 'backend/functions/lib'));
  return out;
}
export function artefactPath(name: string): string | null {
  for (const d of libDirs()) {
    const p = path.join(d, name);
    if (existsSync(p)) return p;
  }
  return null;
}
export interface ToolInfo {
  version: string;
  amd64: { sha256: string; size: number } | null;
  arm64: { sha256: string; size: number } | null;
}
let tool: ToolInfo | null = null;
export function toolInfo(): ToolInfo {
  if (tool) return tool;
  const one = (arch: string) => {
    const p = artefactPath(`opm-render-linux-${arch}`);
    if (!p) return null;
    const b = readFileSync(p);
    return { sha256: createHash('sha256').update(b).digest('hex'), size: b.length };
  };
  let version = 'none';
  const meta = artefactPath('opm-tools.json');
  if (meta)
    try {
      version = String((JSON.parse(readFileSync(meta, 'utf8')) as { version?: string }).version ?? 'unknown');
    } catch {
      /* keep */
    }
  tool = { version, amd64: one('amd64'), arm64: one('arm64') };
  return tool;
}
export function toolBytes(arch: 'linux-amd64' | 'linux-arm64'): Buffer {
  const p = artefactPath(`opm-render-${arch}`);
  if (!p) throw new Error(`opm-render-${arch} is not built (backend/build.mjs needs Go and agent/cmd/opm-render)`);
  return readFileSync(p);
}
export const exporterAvailable = () => artefactPath('opm-export.wasm') !== null;

// ───────────────────────────────────────── run the WASM exporter
let compiled: Promise<WebAssembly.Module> | null = null;
export async function runExporter(req: ExportRequest): Promise<ExportResult> {
  const wasmPath = artefactPath('opm-export.wasm');
  if (!wasmPath) throw new Error('the exporter is not built (functions/lib/opm-export.wasm missing: run the backend build with Go installed)');
  compiled ??= WebAssembly.compile(readFileSync(wasmPath));
  const mod = await compiled;
  const { WASI } = await import('node:wasi');
  const dir = mkdtempSync(path.join(tmpdir(), 'opm-export-'));
  const inP = path.join(dir, 'in.json');
  const outP = path.join(dir, 'out.json');
  const errP = path.join(dir, 'err.txt');
  writeFileSync(inP, JSON.stringify(req));
  const fin = openSync(inP, 'r');
  const fout = openSync(outP, 'w');
  const ferr = openSync(errP, 'w');
  try {
    const wasi = new WASI({ version: 'preview1', args: ['opm-export'], env: {}, stdin: fin, stdout: fout, stderr: ferr, returnOnExit: true });
    const inst = await WebAssembly.instantiate(mod, wasi.getImportObject() as WebAssembly.Imports);
    const code = wasi.start(inst);
    closeSync(fout);
    closeSync(ferr);
    if (code !== 0) throw new Error(`exporter failed (exit ${code}): ${readFileSync(errP, 'utf8').slice(0, 2000)}`);
    const res = JSON.parse(readFileSync(outP, 'utf8')) as ExportResult;
    if (res.format !== MIRROR_EXPORT_FORMAT) throw new Error(`exporter format ${res.format} ≠ ${MIRROR_EXPORT_FORMAT}`);
    return res;
  } finally {
    for (const fd of [fin, fout, ferr])
      try {
        closeSync(fd);
      } catch {
        /* closed */
      }
    rmSync(dir, { recursive: true, force: true });
  }
}

/** EMULATOR ONLY stand-in: a deterministic minimal tree (README, opm/config.json, opm/export.json, Files, seeds, snapshots). */
export function stubExport(req: ExportRequest): ExportResult {
  if (!env.emulator) throw new Error('the stub exporter exists only in the emulator');
  const files: ExportFile[] = [];
  const text = (p: string, t: string) => files.push({ path: p, mode: '100644', text: t, size: Buffer.byteLength(t) });
  const pretty = (v: unknown) => JSON.stringify(JSON.parse(canonicalize(v)), null, 2) + '\n';
  text('README.md', `# ${req.branch.label}\n\nOPM ${req.branch.kind} branch ${req.branch.gitBranch} (stub export)\n`);
  text('.gitignore', '**/.env\n**/.opm-runtime/\n**/.opm-data/\n');
  text('opm/config.json', pretty({ format: req.format, branch: req.branch, commit: req.commit, config: req.config }));
  text('opm/export.json', pretty(req));
  if (req.tool.amd64) files.push({ path: 'opm/bin/opm-render-linux-amd64', mode: '100755', tool: 'linux-amd64', size: req.tool.amd64.size });
  for (const site of req.sites) {
    const s = req.config.sites.find((x) => x.siteSlug === site);
    if (!s) continue;
    text(`sites/${site}/docker-compose.yml`, `name: opm-${req.branch.entitySlug}-${site}\nservices:\n${s.config.containers.map((c) => `  ${c.containerSlug}:\n    image: ${req.images[imageKey(c.serviceSlug, c.version)]?.pull ?? `${c.serviceSlug}:${c.version}`}\n`).join('')}`);
    text(`sites/${site}/.env.example`, req.config.secrets.map((x) => `OPM_SECRET_${x.secretSlug.toUpperCase().replace(/-/g, '_')}=\n`).join(''));
    for (const f of req.config.files)
      if (f.content) {
        const hex = hexOf(f.content.sha256);
        if (f.render === 'expressions') text(`sites/${site}/files/${f.fileSlug}.opm-template`, req.texts[hex] ?? '');
        else files.push({ path: `sites/${site}/files/${f.fileSlug}`, mode: '100644', blob: hex, size: f.content.sizeBytes });
      }
    for (const v of s.config.volumes ?? []) {
      const snap = req.persist[site]?.[v.volumeSlug];
      if (snap) for (const e of snap.tree.entries) files.push({ path: `sites/${site}/volumes/${v.volumeSlug}/${e.path}`, mode: e.mode === '0755' ? '100755' : '100644', blob: hexOf(e.sha256), size: e.size });
      else if (v.seed?.kind === 'tree') for (const e of v.seed.entries) files.push({ path: `sites/${site}/volumes/${v.volumeSlug}/${e.path}`, mode: '100644', blob: hexOf(e.sha256), size: e.size });
    }
  }
  files.sort((a, b) => compareUtf16(a.path, b.path));
  return { format: MIRROR_EXPORT_FORMAT, files, notes: [{ code: 'STUB', site: null, message: 'stub exporter (emulator test)' }], env: [] };
}

// ───────────────────────────────────────── request assembly
export function personOf(a: CommitAuthor | null | undefined, siteSlug: string | null): { name: string; email: string } {
  // §21 #113 an agent auto-commit of changes made in Docker outside OPM
  if (a?.kind === 'unknown') return { name: a.name?.trim() || 'UNKNOWN (changed in Docker)', email: `opm-site-${a.siteSlug ?? siteSlug ?? 'unknown'}@users.noreply.opm` };
  if (!a || a.kind === 'system' || !a.email || a.email === 'system') return siteSlug && a?.siteSlug ? { name: `OPM site ${siteSlug}`, email: `opm-site-${siteSlug}@users.noreply.opm` } : { name: 'OPM', email: 'opm@users.noreply.opm' };
  const email = a.email.includes('@') ? a.email : `${a.email}@users.noreply.opm`;
  return { name: (a.name ?? '').trim() || a.email, email };
}
/** config without any secret value + the list of secrets that have one */
export function stripSecrets(c: Config): { config: Config; secretsSet: string[] } {
  const secretsSet: string[] = [];
  const config: Config = { ...c, secrets: c.secrets.map((s) => {
    if (s.value !== null && s.value !== undefined && s.value !== '') secretsSet.push(s.secretSlug);
    return { ...s, value: null };
  }) };
  return { config, secretsSet: secretsSet.sort() };
}

/** a revision catalog from the live catalog that tolerates missing / not-ready images (they are skipped; exporter notes) */
export function liveCatalogFor(config: Config, cat: Catalog): RevisionCatalog {
  const out: RevisionCatalog = { services: {}, images: {}, notFoundPages: {} };
  for (const site of config.sites)
    for (const c of site.config.containers) {
      const s = cat.services[c.serviceSlug];
      const i = cat.images[imageKey(c.serviceSlug, c.version)];
      if (s) out.services[s.serviceSlug] = { serviceSlug: s.serviceSlug, label: s.label, icon: null };
      if (i)
        try {
          out.images[imageKey(c.serviceSlug, c.version)] = imageSpec(i);
        } catch {
          /* not imported: skipped */
        }
    }
  return out;
}
async function notFoundPagesFor(config: Config, base: RevisionCatalog): Promise<RevisionCatalog['notFoundPages']> {
  const out = { ...base.notFoundPages };
  for (const s of config.sites) {
    const id = s.config.notFoundPageId;
    if (!id || out[id]) continue;
    const d = await col.notFoundPages().doc(id).get();
    if (d.exists) out[id] = { notFoundPageId: id, label: String(d.data()!.label ?? ''), htmlCode: String(d.data()!.htmlCode ?? '') };
  }
  return out;
}

export interface ExportCtx {
  cat?: Catalog;
  registriesPrivate?: Map<string, boolean>;
  texts: Map<string, string>;
}
export const newExportCtx = (): ExportCtx => ({ texts: new Map() });
async function privateRegistries(x: ExportCtx): Promise<Map<string, boolean>> {
  if (x.registriesPrivate) return x.registriesPrivate;
  const m = new Map<string, boolean>();
  for (const d of (await col.registries().select('username').get()).docs) m.set(d.id, !!d.data().username);
  x.registriesPrivate = m;
  return m;
}
export function pullRef(i: Image | undefined, fallback: string): Omit<ExportImageRef, 'private'> {
  if (!i || !i.source?.repository) return { pull: fallback, registryHost: null, platform: (i?.platform as string) ?? 'linux/amd64' };
  const host = i.source.host || 'docker.io';
  const digest = i.digest ?? i.source.digest;
  const tag = i.source.tag;
  return { pull: `${host}/${i.source.repository}${tag ? `:${tag}` : ''}${digest ? `@${digest}` : ''}`, registryHost: host, platform: (i.platform as string) ?? 'linux/amd64' };
}

export interface ExportInput {
  branch: ExportRequest['branch'];
  commit: ExportRequest['commit'];
  deploymentId: string;
  config: Config;
  catalog: RevisionCatalog | null;
  sites: string[];
  persist: Record<string, Record<string, ExportPersistSnapshot>>;
}
export async function buildRequest(x: ExportCtx, i: ExportInput): Promise<ExportRequest> {
  x.cat ??= await loadCatalog();
  const { config, secretsSet } = stripSecrets(i.config);
  const catalog0 = i.catalog ?? liveCatalogFor(config, x.cat);
  const catalog: RevisionCatalog = { ...catalog0, notFoundPages: await notFoundPagesFor(config, catalog0) };
  const priv = await privateRegistries(x);
  const images: Record<string, ExportImageRef> = {};
  for (const key of Object.keys(catalog.images).sort()) {
    const live = x.cat.images[key];
    const spec = catalog.images[key]!;
    // §21 #113 a site image with a pullable reference: the compose file pulls that reference
    if (!live && !spec.digest && spec.reference) {
      const first = spec.reference.split('/')[0]!;
      const host = spec.reference.includes('/') && (first.includes('.') || first.includes(':') || first === 'localhost') ? first : 'docker.io';
      images[key] = { pull: spec.reference, registryHost: host, private: false, platform: spec.platform || 'linux/amd64' };
      continue;
    }
    const r = pullRef(live, key);
    images[key] = { ...r, private: !!(live?.registryId && priv.get(live.registryId)) };
  }
  // texts: every render-expressions blob of the exported sites
  const want = new Set<string>();
  for (const f of config.files) if (f.render === 'expressions' && f.content) want.add(hexOf(f.content.sha256));
  // files-mode TLS certificates (any render): the exporter reads every blob it needs from texts
  for (const s of config.sites)
    if (i.sites.includes(s.siteSlug))
      for (const h of s.config.edge?.hosts ?? []) {
        const tls = h.tls;
        if (tls?.mode !== 'files') continue;
        const f = config.files.find((x) => x.fileSlug === tls.certFileSlug);
        if (f?.content) want.add(hexOf(f.content.sha256));
      }
  for (const s of config.sites)
    if (i.sites.includes(s.siteSlug))
      for (const v of s.config.volumes ?? []) {
        const seed = v.seed as { kind: string; entries?: { sha256: string; render?: string }[]; sha256?: string; render?: string } | null;
        if (seed?.kind === 'tree') for (const e of seed.entries ?? []) if (e.render === 'expressions') want.add(hexOf(e.sha256));
        if (seed?.kind === 'single' && seed.render === 'expressions' && seed.sha256) want.add(hexOf(seed.sha256));
      }
  const texts: Record<string, string> = {};
  for (const hex of [...want].sort()) {
    if (!x.texts.has(hex)) x.texts.set(hex, (await readText(`files/${hex}`)) ?? '');
    texts[hex] = x.texts.get(hex)!;
  }
  const t = toolInfo();
  return {
    format: MIRROR_EXPORT_FORMAT,
    branch: i.branch,
    commit: i.commit,
    deploymentId: i.deploymentId,
    config,
    secretsSet,
    catalog,
    images,
    texts,
    sites: i.sites,
    persist: i.persist,
    tool: { version: t.version, amd64: t.amd64 ? { sha256: `sha256:${t.amd64.sha256}`, size: t.amd64.size } : null, arm64: t.arm64 ? { sha256: `sha256:${t.arm64.sha256}`, size: t.arm64.size } : null },
  };
}
export async function exportTree(req: ExportRequest, stub: boolean): Promise<ExportResult> {
  if (stub && env.emulator) return stubExport(req);
  return runExporter(req);
}
export type { Commit };
