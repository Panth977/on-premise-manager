/**
 * Files with expressions (round D part 2): a file is rendered ONCE PER SITE (all containers of the site share that
 * copy) with field "file" roots var, secret, site, self, deployment (shared siteFileExprContext + renderSiteFile;
 * secrets always masked). Which sites / containers mount a file decides where it is rendered.
 */
import type { Config, ExprContext, FileDef, RenderedFile, RenderFileError, ValidationContext } from '@opm/shared';
import { containerMounts, imageKey, isJsonMime, jsonError, normalizeConfig, renderSiteFile, siteExprMap, siteFileExprContext } from '@opm/shared';

export interface SiteTarget {
  siteSlug: string;
  siteLabel: string;
  /** containers of the site that mount the file (with the unresolved mount path); empty = hypothetical preview */
  mounts: { containerSlug: string; mountPath: string; enabled: boolean }[];
}

/** Preview context: the scope's validation context; templates mask every secret (no per-deployment values). */
export function previewVctx(vctx: ValidationContext, config: Pick<Config, 'secrets'>, template: boolean): ValidationContext {
  return template ? { ...vctx, secretsSet: config.secrets.map((s) => s.secretSlug) } : { ...vctx, secretsSet: vctx.secretsSet ?? [] };
}

/** Every site, the ones where a container mounts `fileSlug` first. */
export function fileSites(config: Config, vctx: ValidationContext, fileSlug: string): { mounting: SiteTarget[]; all: SiteTarget[] } {
  const cfg = normalizeConfig(config, vctx.images);
  const all: SiteTarget[] = cfg.sites.map((s) => {
    const mounts: SiteTarget['mounts'] = [];
    for (const c of s.config.containers ?? []) {
      if (!c || typeof c.containerSlug !== 'string') continue;
      const img = vctx.images[imageKey(c.serviceSlug, c.version)];
      for (const m of containerMounts(c, img ?? { volumes: [] })) if (m.source.kind === 'file' && m.source.fileSlug === fileSlug) mounts.push({ containerSlug: c.containerSlug, mountPath: m.mountPath, enabled: c.enabled !== false });
    }
    return { siteSlug: s.siteSlug, siteLabel: s.label, mounts };
  });
  all.sort((a, b) => a.siteSlug.localeCompare(b.siteSlug));
  return { mounting: all.filter((t) => t.mounts.length), all };
}

export interface FilePreview {
  target: SiteTarget;
  out: RenderedFile | RenderFileError;
  /** V_FILE_JSON: the rendered output of a JSON mime does not parse */
  jsonError: string | null;
  /** expression context of the site (autocomplete) */
  ectx: ExprContext;
}

/** Render `src` once for a site (or hypothetically for a site where nothing mounts it). */
export function renderForSite(config: Config, vctx: ValidationContext, file: Pick<FileDef, 'fileSlug' | 'mime'>, target: SiteTarget, src: string, siteMap?: NonNullable<ExprContext['site']>): FilePreview | null {
  const cfg = normalizeConfig(config, vctx.images);
  if (!cfg.sites.some((s) => s.siteSlug === target.siteSlug)) return null;
  const ectx = siteFileExprContext(cfg, vctx, target.siteSlug, siteMap ?? siteExprMap(cfg, vctx));
  const out = renderSiteFile(src, ectx);
  const je = out.ok && isJsonMime(file.mime) ? jsonError(out.value) : null;
  return { target, out, jsonError: je, ectx };
}

/** The expression context of a site for autocomplete (no site yet: config roots only). */
export function siteCtx(config: Config, vctx: ValidationContext, siteSlug: string | null, siteMap?: NonNullable<ExprContext['site']>): ExprContext {
  const cfg = normalizeConfig(config, vctx.images);
  const slug = siteSlug ?? cfg.sites[0]?.siteSlug ?? '';
  return siteFileExprContext(cfg, vctx, slug, siteMap ?? siteExprMap(cfg, vctx));
}

/** Split rendered text into plain runs and auto-host placeholders `<auto:slug>` (shown as "auto · slug" chips). */
export function autoHostRuns(text: string): { t: string; auto: string | null }[] {
  const out: { t: string; auto: string | null }[] = [];
  const re = /<auto:([a-z][a-z0-9-]{1,39})>/g;
  let last = 0;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    if (m.index > last) out.push({ t: text.slice(last, m.index), auto: null });
    out.push({ t: m[0], auto: m[1]! });
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push({ t: text.slice(last), auto: null });
  return out;
}
