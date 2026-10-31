/** Normalisation (defaults applied on write, §11) + safe validation of config sections. */
import { normalizeConfig, normalizeSiteConfig, validateConfig, type CatalogImage, type Config, type FileDef, type Issues, type SecretDef, type Site, type StorageLocation, type ValidationContext, type ValidationIssue, type Variable } from '@opm/shared';
import { E } from './errors.js';

const isObj = (x: unknown): x is Record<string, unknown> => !!x && typeof x === 'object' && !Array.isArray(x);

function listOf(section: string, items: unknown): Record<string, unknown>[] {
  if (!Array.isArray(items)) throw E.bad(section, 'V_TYPE', `${section} must be a list`);
  items.forEach((x, i) => {
    if (!isObj(x)) throw E.bad(`${section}[${i}]`, 'V_TYPE', `${section}[${i}] must be an object`);
  });
  return items as Record<string, unknown>[];
}
export function normVariables(items: unknown): Variable[] {
  return listOf('variables', items).map((v) => ({
    ...(v as unknown as Variable),
    description: (v.description as string) ?? '',
    value: v.value === undefined ? null : (v.value as Variable['value']),
    required: (v.required as boolean) ?? false,
    options: (v.options as string[] | null) ?? null,
    validation: v.validation === undefined ? null : (v.validation as Variable['validation']),
  }));
}
export function normSecretDefs(items: unknown): SecretDef[] {
  return listOf('secrets', items).map((s) => ({
    ...(s as unknown as SecretDef),
    description: (s.description as string) ?? '',
    required: (s.required as boolean) ?? true,
    generate: (s.generate as SecretDef['generate']) ?? null,
    value: (s.value as string | null) ?? null,
    rotatedAt: (s.rotatedAt as string | null) ?? null,
  }));
}
/**
 * Round D part 2: a storage location (FileDef.location / SiteVolume.location). Missing / null → auto (normalized by the
 * shared normalizers); a well-formed value is reduced to its fields; a malformed one (unknown kind, path not a string) is
 * refused with 422 V_LOCATION instead of being coerced to auto (the shared normalizeLocation would silently do that).
 * Path rules (./ segments, reserved dirs, absolute, collisions) are validateConfig's V_LOCATION.
 */
function normLocation(l: unknown, at: string): StorageLocation {
  if (l === undefined || l === null) return { kind: 'auto' };
  if (isObj(l) && l.kind === 'auto') return { kind: 'auto' };
  if (isObj(l) && l.kind === 'path' && typeof l.path === 'string') return { kind: 'path', path: l.path };
  throw E.bad(at, 'V_LOCATION', 'location must be { kind: "auto" } or { kind: "path", path: "./…" | "/abs/path" }');
}
export function normFiles(items: unknown): FileDef[] {
  return listOf('files', items).map((f, i) => ({
    ...(f as unknown as FileDef),
    description: (f.description as string) ?? '',
    editor: (f.editor as FileDef['editor']) ?? 'text',
    maxBytes: (f.maxBytes as number) ?? 1048576,
    content: (f.content as FileDef['content']) ?? null,
    // round C: default by editor (text → expressions, binary → none); an explicit value is kept so that validation
    // refuses binary + expressions (V_ENUM) instead of silently coercing it
    render: (f.render as FileDef['render'] | undefined) ?? ((f.editor ?? 'text') === 'binary' ? 'none' : 'expressions'),
    location: normLocation(f.location, `files[${String(f.fileSlug ?? i)}].location`),
  }));
}
/** Image volumes by `<serviceSlug>:<version>` (legacy containers get their implicit image volumes as explicit rows). */
export type ImageVolumes = Record<string, Pick<CatalogImage, 'volumes'>>;
export function normSites(items: unknown, images?: ImageVolumes): Site[] {
  return listOf('sites', items).map((s, i) => {
    if (s.config !== undefined && !isObj(s.config)) throw E.bad(`sites[${i}].config`, 'V_TYPE', 'config must be an object');
    const vols = isObj(s.config) && Array.isArray(s.config.volumes) ? (s.config.volumes as unknown[]) : [];
    for (const v of vols) if (isObj(v)) normLocation(v.location, `sites[${String(s.siteSlug ?? i)}].config.volumes[${String(v.volumeSlug)}].location`);
    try {
      return { siteSlug: s.siteSlug as string, label: s.label as string, config: normalizeSiteConfig((s.config ?? {}) as Partial<Site['config']>, images) };
    } catch (e) {
      throw E.bad(`sites[${String(s.siteSlug ?? i)}].config`, 'V_TYPE', `malformed site config (${(e as Error).message})`);
    }
  });
}

/** validateConfig that never throws on malformed input (reports V_TYPE instead). */
export function safeValidate(config: Config, ctx: ValidationContext): { issues: ValidationIssue[]; errors: ValidationIssue[]; warnings: ValidationIssue[] } {
  let is: Issues;
  try {
    is = validateConfig(config, ctx);
  } catch (e) {
    const issue: ValidationIssue = { path: '', code: 'V_TYPE', message: `malformed config (${(e as Error).message})`, severity: 'error' };
    return { issues: [issue], errors: [issue], warnings: [] };
  }
  return { issues: is.list, errors: is.list.filter((x) => x.severity === 'error'), warnings: is.list.filter((x) => x.severity === 'warning') };
}

/**
 * Image volumes of the catalog, cached per instance for normalize-on-read (round D): a pre-D site config (no `volumes`)
 * mounted every image volume implicitly; normalizeSiteConfig makes those rows explicit `volume` sources only when it knows
 * the image volumes, otherwise a read → write round trip would silently unmount them. Filled by loadCatalog() and
 * primeImageVolumes() (router, before every request; TTL below).
 */
let cachedImageVolumes: ImageVolumes | undefined;
let cachedAt = 0;
export const IMAGE_VOLUMES_TTL_MS = 15_000;
export function setImageVolumes(images: ImageVolumes): void {
  cachedImageVolumes = images;
  cachedAt = Date.now();
}
export const imageVolumesFresh = () => cachedImageVolumes !== undefined && Date.now() - cachedAt < IMAGE_VOLUMES_TTL_MS;

/**
 * Normalize a STORED config-like on read (round C: legacy one-per-service containers, proxy serviceSlug, host "" → null,
 * files without render; round D: site volumes declared, `data` sources → `volume`, implicit image volumes explicit with the
 * cached image volumes). Never throws: a malformed document is returned as it is (validation reports it).
 */
export function normalizeStored<C extends Pick<Config, 'files' | 'sites'>>(config: C, images: ImageVolumes | undefined = cachedImageVolumes): C {
  try {
    if (!config || !Array.isArray(config.files) || !Array.isArray(config.sites)) return config;
    return normalizeConfig(config, images);
  } catch {
    return config;
  }
}
