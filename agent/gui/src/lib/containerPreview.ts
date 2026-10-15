/**
 * Container resolution helpers for the local working copy (agent mode): the expression context of one container, its
 * env + mount paths resolved together (resolveContainerConfig) and "Preview for site" of a template file (round D:
 * ONE copy per site, renderSiteFile, secrets masked). Same shared functions as the agent's staging.
 */
import type { Config, Container } from '@opm/shared/types';
import type { ContainerResolution, EnvSource, ExprContext, MountSource } from '@opm/shared/expr';
import { containerExprContext, containerMounts, fileRenderMode, imageKey, normalizeImageEnv, renderSiteFile, resolveContainerConfig, resolveSiteHosts, siteExprMap, siteFileExprContext, type CatalogImage, type RenderFileError, type RenderedFile } from '@opm/shared/validate';
import { apiPath, fetchText } from './api';
import { draft } from './state/draft.svelte';

export interface ContainerResolved {
  img: CatalogImage;
  ectx: ExprContext;
  env: EnvSource[];
  mounts: MountSource[];
  resolution: ContainerResolution;
}

/** Validation-context fields for the agent (masked secrets: only "is set" is known). */
export function agentCtx(cfg: Config, deploymentId: string) {
  const { services, images } = draft.catalogCtx;
  return {
    mode: 'agent' as const,
    services,
    images,
    deploymentId: deploymentId || draft.deploymentId || 'deployment',
    secretsSet: cfg.secrets.filter((s) => s.value !== null).map((s) => s.secretSlug),
    detectedHosts: draft.detectedHosts,
  };
}

/** Resolve one container (null when its image is not in the head catalog). Never throws. */
export function resolveContainer(cfg: Config, deploymentId: string, siteSlug: string, c: Container): ContainerResolved | null {
  const vctx = agentCtx(cfg, deploymentId);
  const raw = vctx.images[imageKey(c.serviceSlug, c.version)];
  if (!raw) return null;
  const img: CatalogImage = { ...raw, env: raw.env.map(normalizeImageEnv) };
  try {
    const hosts = resolveSiteHosts(cfg, vctx.deploymentId, { detected: vctx.detectedHosts, placeholder: false });
    const ectx = containerExprContext(cfg, vctx, siteSlug, c, siteExprMap(cfg, vctx, hosts));
    const r = resolveContainerConfig(cfg, vctx, c, img, ectx);
    return { img, ectx, env: r.env, mounts: r.mounts, resolution: r.resolution };
  } catch {
    return null;
  }
}

/** Containers (enabled or not) that mount a file, in config order. */
export function mountersOf(cfg: Config, fileSlug: string): { siteSlug: string; containerSlug: string }[] {
  const out: { siteSlug: string; containerSlug: string }[] = [];
  const { images } = draft.catalogCtx;
  for (const s of cfg.sites)
    for (const c of s.config.containers) {
      const img = images[imageKey(c.serviceSlug, c.version)] ?? { volumes: [] };
      if (containerMounts(c, img).some((m) => m.source.kind === 'file' && m.source.fileSlug === fileSlug)) out.push({ siteSlug: s.siteSlug, containerSlug: c.containerSlug });
    }
  return out;
}

/** true when the file is a template ({{ }} resolved per mounting container). */
export function isTemplateFile(cfg: Config, fileSlug: string): boolean {
  const f = cfg.files.find((x) => x.fileSlug === fileSlug);
  return !!f && fileRenderMode(f) === 'expressions';
}

/** The current text of a file: the local edit when there is one, else the head content from the agent. */
export async function loadFileText(fileSlug: string): Promise<string> {
  const head = draft.base?.config.files.find((f) => f.fileSlug === fileSlug);
  const work = draft.work?.files.find((f) => f.fileSlug === fileSlug);
  const local = draft.fileText[fileSlug];
  if (local !== undefined && JSON.stringify(work?.content ?? null) !== JSON.stringify(head?.content ?? null)) return local;
  if (!head?.content) return '';
  return fetchText('getFileContent', apiPath('getFileContent', { fileSlug }));
}

/** Render a template file for one site (round D: rendered once per site, shared by every container; secrets masked in `masked`). */
export function renderPreview(cfg: Config, deploymentId: string, siteSlug: string, text: string): RenderedFile | RenderFileError | null {
  if (!cfg.sites.some((s) => s.siteSlug === siteSlug)) return null;
  const vctx = agentCtx(cfg, deploymentId);
  try {
    const hosts = resolveSiteHosts(cfg, vctx.deploymentId, { detected: vctx.detectedHosts, placeholder: false });
    return renderSiteFile(text, siteFileExprContext(cfg, vctx, siteSlug, siteExprMap(cfg, vctx, hosts)));
  } catch {
    return null;
  }
}

/** Containers of ONE site that mount a file / secret (for "shared copy · also used by"). */
export function siteMounters(cfg: Config, siteSlug: string, kind: 'file' | 'secret', slug: string): string[] {
  const s = cfg.sites.find((x) => x.siteSlug === siteSlug);
  if (!s) return [];
  const { images } = draft.catalogCtx;
  return s.config.containers
    .filter((c) => containerMounts(c, images[imageKey(c.serviceSlug, c.version)] ?? { volumes: [] }).some((m) => (kind === 'file' ? m.source.kind === 'file' && m.source.fileSlug === slug : m.source.kind === 'secret' && m.source.secretSlug === slug)))
    .map((c) => c.containerSlug);
}
