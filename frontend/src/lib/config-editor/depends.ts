import { inferDepends, startOrder, type Config, type ContainerDepends, type StartOrder, type ValidationContext, type ValidationIssue } from '@opm/shared';

/**
 * §21 #93 helpers: implicit / suggested depends of a site (shared inferDepends over env + image env defaults; file contents
 * only when the caller already has them) and the V_DEPENDS_SUGGESTED issue → { site, container, target } for the "Add" fix.
 */
export function siteDepends(config: Config, siteSlug: string, vctx: Pick<ValidationContext, 'images'>, fileContents?: Record<string, string>): { deps: ContainerDepends[]; order: StartOrder } | null {
  const site = config.sites.find((s) => s.siteSlug === siteSlug);
  if (!site) return null;
  try {
    const deps = inferDepends({ site: site.config, images: vctx.images, files: config.files, fileContents });
    return { deps, order: startOrder(site.config, deps) };
  } catch {
    return null;
  }
}

export interface DependSuggestion {
  siteSlug: string;
  containerSlug: string;
  target: string;
}
/** A V_DEPENDS_SUGGESTED issue (path `sites[S].config.containers[C].depends`, message "C mentions `X` (…) — add it to Depends on?"). */
export function parseDependSuggestion(i: Pick<ValidationIssue, 'code' | 'path' | 'message'>): DependSuggestion | null {
  if (i.code !== 'V_DEPENDS_SUGGESTED') return null;
  const p = /^sites\[([^\]]+)\]\.config\.containers\[([^\]]+)\]\.depends$/.exec(i.path ?? '');
  const t = /`([^`]+)`/.exec(i.message ?? '');
  return p && t ? { siteSlug: p[1]!, containerSlug: p[2]!, target: t[1]! } : null;
}
