/**
 * Container form helpers (SPEC §21 #52/#53, §21 #102) — the rules of the ONE container form of both GUIs:
 * a fresh container of a service + version, a version change, source values of a mount picker, env bulk paste.
 */
import type { Container, ContainerEnv, ImageEnv, VolumeSource } from '@opm/shared/types';
import { defaultContainer, normalizeImageEnv, type CatalogImage } from '@opm/shared/validate';
import type { ImageLike } from '../host';

export function catalogImage(img: ImageLike): CatalogImage {
  return { kind: img.kind, ports: img.ports, env: img.env, volumes: img.volumes, healthcheck: img.healthcheck };
}
/** A new container of service + version; required secret env gets the first secret of the config (instead of ""). */
export function freshContainer(serviceSlug: string, version: string, img: ImageLike, name: string, secrets: readonly string[]): Container {
  const c = defaultContainer(serviceSlug, version, catalogImage(img), name);
  for (const en of c.env) if (en.secretRef === '' && secrets.length) en.secretRef = secrets[0]!;
  return c;
}
/** A new version: keep port rows of names that still exist, add new ones; drop image-volume rows the image no longer has. */
export function applyVersion(c: Container, version: string, img: ImageLike | undefined) {
  c.version = version;
  if (!img) return;
  c.ports = c.ports.filter((p) => img.ports.some((x) => x.name === p.portName));
  for (const p of img.ports) if (!c.ports.some((x) => x.portName === p.name)) c.ports.push({ portName: p.name, hostPort: null, hostIp: '0.0.0.0', edge: true });
  c.volumes = c.volumes.filter((v) => v.imageVolume === null || img.volumes.some((x) => x.name === v.imageVolume));
  const hc = c.healthcheck;
  if (hc && (hc.type === 'http' || hc.type === 'tcp') && !img.ports.some((p) => p.name === hc.portName)) c.healthcheck = null;
}
/** the picker value of a mount source */
export function sourceValue(src: VolumeSource | null | undefined): string | null {
  if (!src) return null;
  if (src.kind === 'volume') return src.volumeSlug ? `volume:${src.volumeSlug}` : null;
  if (src.kind === 'file') return `file:${src.fileSlug}`;
  if (src.kind === 'secret') return `secret:${src.secretSlug}`;
  return src.kind;
}
export function sourceOf(v: string, prev: VolumeSource): VolumeSource {
  if (v.startsWith('volume:')) return { kind: 'volume', volumeSlug: v.slice(7) };
  if (v.startsWith('file:')) return { kind: 'file', fileSlug: v.slice(5) };
  if (v.startsWith('secret:')) return { kind: 'secret', secretSlug: v.slice(7) };
  if (v === 'host') return { kind: 'host', path: prev.kind === 'host' ? prev.path : '' };
  return { kind: 'none' };
}
/**
 * Bulk paste into an env name cell (Rancher-style): lines `KEY=VALUE` (or `export KEY=VALUE`, `KEY: VALUE`; `#` comments
 * and blank lines ignored; surrounding quotes stripped) → rows. Returns null when the text is not a multi-line block.
 */
export function parseEnvBlock(text: string): { name: string; value: string }[] | null {
  const lines = text.split(/\r?\n/).filter((l) => l.trim() && !l.trim().startsWith('#'));
  if (lines.length < 2 && !/^\s*(export\s+)?[A-Za-z_][A-Za-z0-9_]*\s*=/.test(text)) return null;
  if (lines.length < 2 && !text.includes('=')) return null;
  const out: { name: string; value: string }[] = [];
  for (const l of lines) {
    const m = /^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*[=:]\s?(.*)$/.exec(l);
    if (!m) continue;
    let v = m[2]!.trim();
    if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'"))) v = v.slice(1, -1);
    out.push({ name: m[1]!.toUpperCase(), value: v });
  }
  return out.length ? out : null;
}
/** Merge pasted rows into the container env: existing names get the new value (unless they are secret refs), new names are appended. */
export function mergeEnv(c: Container, rows: { name: string; value: string }[]): number {
  let n = 0;
  for (const r of rows) {
    const cur = c.env.find((e) => e.name === r.name);
    if (cur) {
      if (cur.secretRef !== null) continue;
      cur.value = r.value;
    } else c.env.push({ name: r.name, value: r.value, secretRef: null });
    n++;
  }
  return n;
}
export function imageEnvList(img: ImageLike | undefined): ImageEnv[] {
  return (img?.env ?? []).map((e) => normalizeImageEnv(e));
}
export function envDefault(d: ImageEnv | null | undefined): string {
  return d?.value ?? d?.options?.[0] ?? '';
}
export function setEnvKind(en: ContainerEnv, k: 'value' | 'secret', d: ImageEnv | null, firstSecret: string) {
  if (k === 'value') {
    en.value = en.value ?? envDefault(d);
    en.secretRef = null;
  } else {
    en.secretRef = en.secretRef ?? firstSecret;
    en.value = null;
  }
}
