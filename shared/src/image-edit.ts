/**
 * Image editing (SPEC §2.2 Image, §6.1 patchImage, §21 #82): which image names change (rename / remove / add), where a
 * port / env / volume name of an image is referenced by containers of templates + deployment drafts, and the
 * "rename everywhere" rewrite. Pure functions — used by the backend (patchImage), the frontend mock and the image page.
 *
 * References of an image name (containers of the image = serviceSlug + version):
 *   port   — ContainerPort.portName (`ports[<name>]`), Container.healthcheck.portName (http / tcp, `healthcheck.portName`),
 *            the site's edge proxy routes to that container (`edge.routes[<routeId>].portName`), and expressions
 *            `site.<siteSlug>.port.<containerSlug>.<name>` in container env values of any site (the use names the
 *            REFERENCING container: `env[<NAME>].value (site.<s>.port.<c>.<name>)`)
 *   env    — ContainerEnv rows (`env[<NAME>]`) and `container.env.<NAME>` in that container's env values / mount paths
 *            (`env[<X>].value`, `volumes[<i>].mountPath`)
 *   volume — ContainerMount.imageVolume (`volumes[<name>]`)
 * File contents (blobs) are not scanned; a stale reference there is reported by validation at publish (E_UNKNOWN_PATH).
 */
import type { Config, Container, Image, ImageEnv, ImageNameKind, ImageNameUse, ImagePort, ImageRename, ImageVolume, Site, ValidationIssue } from './types.js';

/** Image fields that never change through patchImage (identity). reference / registryId: only while the import failed. */
export const IMAGE_IDENTITY_FIELDS = ['version', 'serviceSlug', 'source', 'digest', 'platform', 'import', 'createdAt', 'createdBy', 'updatedAt', 'updatedBy'] as const;
/** Editable image fields (everything else is identity). */
export const IMAGE_EDITABLE_FIELDS = ['kind', 'entrypoint', 'command', 'workdir', 'user', 'ports', 'env', 'volumes', 'healthcheck', 'stopTimeoutSec', 'description'] as const;

export interface ImageNames {
  ports: Pick<ImagePort, 'name' | 'port' | 'protocol'>[];
  env: Pick<ImageEnv, 'name'>[];
  volumes: Pick<ImageVolume, 'name'>[];
}
export interface ImageNameChanges {
  /** valid renames (old → new) */
  renamed: ImageRename[];
  /** old names that are neither kept nor renamed */
  removed: { kind: ImageNameKind; name: string }[];
  /** new names that have no origin */
  added: { kind: ImageNameKind; name: string }[];
  /** ports (new name) whose number or protocol changed (kept or renamed) */
  portRetargeted: { name: string; from: string; to: string }[];
  issues: ValidationIssue[];
}
const LIST: Record<ImageNameKind, keyof ImageNames> = { port: 'ports', env: 'env', volume: 'volumes' };
export const IMAGE_NAME_KINDS: ImageNameKind[] = ['port', 'env', 'volume'];

/**
 * Compare the stored names with the new lists. Each new row's origin = the rename whose `to` is its name, else the same
 * name when it existed, else none (added). Old names without a row pointing at them are removed. Invalid renames
 * (unknown kind, `from` not in the old list, `to` not in the new list, duplicate from / to, two rows with one origin)
 * → issues V_RENAME at `renames[<i>]`.
 */
export function imageNameChanges(before: ImageNames, after: ImageNames, renames: ImageRename[] = []): ImageNameChanges {
  const issues: ValidationIssue[] = [];
  const err = (path: string, message: string) => issues.push({ path, code: 'V_RENAME', message, severity: 'error' });
  const out: ImageNameChanges = { renamed: [], removed: [], added: [], portRetargeted: [], issues };
  if (!Array.isArray(renames)) {
    err('renames', 'renames must be a list of { kind, from, to }');
    renames = [];
  }
  renames.forEach((r, i) => {
    if (!r || typeof r !== 'object' || !IMAGE_NAME_KINDS.includes(r.kind) || typeof r.from !== 'string' || typeof r.to !== 'string') return err(`renames[${i}]`, 'a rename is { kind: port | env | volume, from, to }');
    const oldNames = before[LIST[r.kind]].map((x) => x.name);
    const newNames = after[LIST[r.kind]].map((x) => x.name);
    if (r.from === r.to) return err(`renames[${i}]`, `${r.kind} ${r.from}: from and to are the same`);
    if (!oldNames.includes(r.from)) return err(`renames[${i}]`, `${r.kind} "${r.from}" is not a ${r.kind} of the image`);
    if (!newNames.includes(r.to)) return err(`renames[${i}]`, `${r.kind} "${r.to}" is not in the new ${LIST[r.kind]} list`);
    if (renames.some((x, j) => j < i && x?.kind === r.kind && (x.from === r.from || x.to === r.to))) return err(`renames[${i}]`, `${r.kind} ${r.from} → ${r.to}: renamed twice`);
    out.renamed.push({ kind: r.kind, from: r.from, to: r.to });
  });
  for (const kind of IMAGE_NAME_KINDS) {
    const oldNames = before[LIST[kind]].map((x) => x.name);
    const used = new Set<string>();
    for (const n of after[LIST[kind]].map((x) => x.name)) {
      const rn = out.renamed.find((r) => r.kind === kind && r.to === n);
      const origin = rn ? rn.from : oldNames.includes(n) ? n : null;
      if (origin === null) {
        out.added.push({ kind, name: n });
        continue;
      }
      if (used.has(origin)) err('renames', `${kind} "${origin}" is both kept and renamed — rename it or keep it, not both`);
      used.add(origin);
      if (kind === 'port') {
        const a = before.ports.find((p) => p.name === origin)!;
        const b = after.ports.find((p) => p.name === n)!;
        if (String(a.port) !== String(b.port) || a.protocol !== b.protocol) out.portRetargeted.push({ name: n, from: `${a.port}/${a.protocol}`, to: `${b.port}/${b.protocol}` });
      }
    }
    for (const o of oldNames) if (!used.has(o)) out.removed.push({ kind, name: o });
  }
  return out;
}

// ───────────────────────────────────────── expressions
const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
/** Replace inside `{{ … }}` blocks only (text outside blocks is literal). */
function mapBlocks(text: string, f: (block: string) => string): string {
  return text.replace(/\{\{[^\n]*?\}\}/g, (b) => f(b));
}
const envRe = (name: string) => new RegExp(`(?<![A-Za-z0-9_.-])container\\.env\\.${esc(name)}(?![A-Za-z0-9_-])`, 'g');
const portRe = (siteSlug: string, containerSlug: string, port: string) => new RegExp(`(?<![A-Za-z0-9_.-])site\\.${esc(siteSlug)}\\.port\\.${esc(containerSlug)}\\.${esc(port)}(?![A-Za-z0-9_-])`, 'g');
const hasRef = (text: unknown, re: RegExp): boolean => typeof text === 'string' && text.includes('{{') && (text.match(/\{\{[^\n]*?\}\}/g) ?? []).some((b) => new RegExp(re.source).test(b));

const isOf = (c: Container, serviceSlug: string, version: string) => c.serviceSlug === serviceSlug && c.version === version;
const slugOf = (c: Container) => c.containerSlug ?? c.serviceSlug;

/** Where `name` (a port / env / volume of serviceSlug:version) is referenced in a config. One entry per place. */
export function imageNameUses(cfg: Pick<Config, 'sites'>, serviceSlug: string, version: string, kind: ImageNameKind, name: string): Pick<ImageNameUse, 'siteSlug' | 'containerSlug' | 'field'>[] {
  const out: Pick<ImageNameUse, 'siteSlug' | 'containerSlug' | 'field'>[] = [];
  const sites: Site[] = cfg.sites ?? [];
  for (const site of sites) {
    const sc = site.config;
    for (const c of sc?.containers ?? []) {
      if (!isOf(c, serviceSlug, version)) continue;
      const cs = slugOf(c);
      const at = (field: string) => out.push({ siteSlug: site.siteSlug, containerSlug: cs, field });
      if (kind === 'port') {
        if ((c.ports ?? []).some((p) => p.portName === name)) at(`ports[${name}]`);
        const hc = c.healthcheck as { type?: string; portName?: string } | null;
        if (hc && (hc.type === 'http' || hc.type === 'tcp') && hc.portName === name) at('healthcheck.portName');
        for (const r of sc.edge?.routes ?? []) if (r.action?.type === 'proxy' && r.action.containerSlug === cs && r.action.portName === name) at(`edge.routes[${r.routeId}].portName`);
        // site.<thisSite>.port.<cs>.<name> from any container of any site
        const re = portRe(site.siteSlug, cs, name);
        for (const s2 of sites)
          for (const c2 of s2.config?.containers ?? [])
            for (const e of c2.env ?? [])
              if (hasRef(e.value, re)) out.push({ siteSlug: s2.siteSlug, containerSlug: slugOf(c2), field: `env[${e.name}].value (site.${site.siteSlug}.port.${cs}.${name})` });
      } else if (kind === 'env') {
        if ((c.env ?? []).some((e) => e.name === name)) at(`env[${name}]`);
        const re = envRe(name);
        for (const e of c.env ?? []) if (hasRef(e.value, re)) at(`env[${e.name}].value`);
        (c.volumes ?? []).forEach((m, i) => hasRef(m.mountPath, re) && at(`volumes[${m.imageVolume ?? i}].mountPath`));
      } else if ((c.volumes ?? []).some((m) => m.imageVolume === name)) at(`volumes[${name}]`);
    }
  }
  return out;
}

/**
 * Rewrite every reference of the renamed names (same places as imageNameUses). Pure: returns a new config + the number
 * of rewritten places. Renames apply simultaneously (a swap a ↔ b works). A container env row that would collide with an
 * existing row of the new name → issue V_DUPLICATE (nothing is rewritten for that row).
 */
export function renameImageNames<T extends Pick<Config, 'sites'>>(cfg: T, serviceSlug: string, version: string, renames: ImageRename[]): { config: T; count: number; issues: ValidationIssue[] } {
  const next = structuredClone(cfg) as T;
  const issues: ValidationIssue[] = [];
  let count = 0;
  const map = (kind: ImageNameKind) => new Map(renames.filter((r) => r.kind === kind).map((r) => [r.from, r.to]));
  const ports = map('port');
  const envs = map('env');
  const vols = map('volume');
  if (!ports.size && !envs.size && !vols.size) return { config: next, count, issues };
  const sites: Site[] = next.sites ?? [];
  // expression rewrites are computed against the ORIGINAL text, so a swap does not rewrite twice
  for (const site of sites) {
    const sc = site.config;
    for (const c of sc?.containers ?? []) {
      if (!isOf(c, serviceSlug, version)) continue;
      const cs = slugOf(c);
      for (const p of c.ports ?? [])
        if (ports.has(p.portName)) {
          p.portName = ports.get(p.portName)!;
          count++;
        }
      const hc = c.healthcheck as { type?: string; portName?: string } | null;
      if (hc && (hc.type === 'http' || hc.type === 'tcp') && hc.portName && ports.has(hc.portName)) {
        hc.portName = ports.get(hc.portName)!;
        count++;
      }
      for (const r of sc.edge?.routes ?? [])
        if (r.action?.type === 'proxy' && r.action.containerSlug === cs && ports.has(r.action.portName)) {
          r.action.portName = ports.get(r.action.portName)!;
          count++;
        }
      const names = new Set((c.env ?? []).map((e) => e.name));
      for (const e of c.env ?? [])
        if (envs.has(e.name)) {
          const to = envs.get(e.name)!;
          if (names.has(to) && !envs.has(to)) {
            issues.push({ path: `sites[${site.siteSlug}].config.containers[${cs}].env[${to}]`, code: 'V_DUPLICATE', message: `container ${cs} on site ${site.siteSlug} already sets ${to}: rename ${e.name} there first`, severity: 'error' });
            continue;
          }
          e.name = to;
          count++;
        }
      for (const m of c.volumes ?? [])
        if (m.imageVolume && vols.has(m.imageVolume)) {
          m.imageVolume = vols.get(m.imageVolume)!;
          count++;
        }
      // container.env.<OLD> in this container's env values + mount paths (one pass over each string)
      if (envs.size) {
        const re = new RegExp(`(?<![A-Za-z0-9_.-])container\\.env\\.(${[...envs.keys()].map(esc).join('|')})(?![A-Za-z0-9_-])`, 'g');
        const fix = (t: string) => mapBlocks(t, (b) => b.replace(re, (_m, n: string) => `container.env.${envs.get(n)}`));
        for (const e of c.env ?? [])
          if (typeof e.value === 'string' && e.value.includes('{{')) {
            const v = fix(e.value);
            if (v !== e.value) {
              e.value = v;
              count++;
            }
          }
        for (const m of c.volumes ?? [])
          if (typeof m.mountPath === 'string' && m.mountPath.includes('{{')) {
            const v = fix(m.mountPath);
            if (v !== m.mountPath) {
              m.mountPath = v;
              count++;
            }
          }
      }
    }
  }
  // site.<site>.port.<containerSlug>.<OLD> from any container env value
  if (ports.size) {
    const targets: { site: string; cs: string }[] = [];
    for (const site of sites) for (const c of site.config?.containers ?? []) if (isOf(c, serviceSlug, version)) targets.push({ site: site.siteSlug, cs: slugOf(c) });
    if (targets.length) {
      const alt = targets.map((t) => `site\\.${esc(t.site)}\\.port\\.${esc(t.cs)}`).join('|');
      const re = new RegExp(`(?<![A-Za-z0-9_.-])(${alt})\\.(${[...ports.keys()].map(esc).join('|')})(?![A-Za-z0-9_-])`, 'g');
      for (const s2 of sites)
        for (const c2 of s2.config?.containers ?? [])
          for (const e of c2.env ?? [])
            if (typeof e.value === 'string' && e.value.includes('{{')) {
              const v = mapBlocks(e.value, (b) => b.replace(re, (_m, pre: string, p: string) => `${pre}.${ports.get(p)}`));
              if (v !== e.value) {
                e.value = v;
                count++;
              }
            }
    }
  }
  return { config: next, count, issues };
}

/** The image's own expressions: rename `container.env.<OLD>` in its env defaults + volume mount paths (pure). */
export function renameImageSelfRefs<T extends { env: ImageEnv[]; volumes: ImageVolume[] }>(img: T, renames: ImageRename[]): T {
  const envs = new Map(renames.filter((r) => r.kind === 'env').map((r) => [r.from, r.to]));
  if (!envs.size) return img;
  const re = new RegExp(`(?<![A-Za-z0-9_.-])container\\.env\\.(${[...envs.keys()].map(esc).join('|')})(?![A-Za-z0-9_-])`, 'g');
  const fix = (t: string) => (t.includes('{{') ? mapBlocks(t, (b) => b.replace(re, (_m, n: string) => `container.env.${envs.get(n)}`)) : t);
  return { ...img, env: img.env.map((e) => (typeof e.value === 'string' ? { ...e, value: fix(e.value) } : e)), volumes: img.volumes.map((v) => ({ ...v, mountPath: fix(v.mountPath) })) };
}

/** Image → the parts imageNameChanges needs (helper for callers holding a full Image). */
export const imageNames = (i: Pick<Image, 'ports' | 'env' | 'volumes'>) => ({ ports: i.ports, env: i.env, volumes: i.volumes });
