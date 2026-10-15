/**
 * §21 #93 depends in the agent GUI: explicit / implicit / suggested depends of a site's containers and the computed start
 * order, from the shared `inferDepends` / `startOrder` (same functions as the agent's APPLY and the validator), on the
 * local working copy. Suggestions are never applied automatically: `addDepend` is the one-click "Add".
 */
import { inferDepends, startOrder, type ContainerDepends, type StartOrder } from '@opm/shared/depends';
import type { ValidationIssue } from '@opm/shared/types';
import { draft } from './state/draft.svelte';
import { apiPath, fetchText } from './api';

/** explicit / implicit / suggested depends of every enabled container + the start order of one site (working copy). */
export function siteDepends(siteSlug: string): { deps: ContainerDepends[]; order: StartOrder } | null {
  const site = draft.work?.sites.find((s) => s.siteSlug === siteSlug);
  if (!site) return null;
  const deps = inferDepends({ site: site.config, images: draft.catalogCtx.images, files: draft.work!.files, fileContents: draft.fileContents });
  return { deps, order: startOrder(site.config, deps) };
}

/** "where" of an inferred depend for people: "env MQTT_URL", "image env REDIS_HOST", "file broker-conf". */
export const fromText = (from: readonly string[]) => from.join(', ');

/** V_DEPENDS_SUGGESTED → the depend it suggests (path sites[S].config.containers[C].depends, message "… `X` …"). */
export function suggestionOf(i: ValidationIssue): { siteSlug: string; containerSlug: string; target: string } | null {
  if (i.code !== 'V_DEPENDS_SUGGESTED') return null;
  const p = /^sites\[([^\]]+)\]\.config\.containers\[([^\]]+)\]\.depends$/.exec(i.path);
  const t = /`([^`]+)`/.exec(i.message);
  return p && t ? { siteSlug: p[1], containerSlug: p[2], target: t[1] } : null;
}

/** true when the working copy can take the depend (site + both containers exist, not already a depend) */
export function canAddDepend(siteSlug: string, containerSlug: string, target: string): boolean {
  const site = draft.work?.sites.find((s) => s.siteSlug === siteSlug);
  const c = site?.config.containers.find((x) => x.containerSlug === containerSlug);
  return !!c && !!site?.config.containers.some((x) => x.containerSlug === target) && !c.depends.some((d) => d.containerSlug === target);
}

/** One-click "Add": `target` joins the container's Depends on in the local working copy. */
export function addDepend(siteSlug: string, containerSlug: string, target: string): boolean {
  if (!canAddDepend(siteSlug, containerSlug, target)) return false;
  const c = draft.work!.sites.find((s) => s.siteSlug === siteSlug)!.config.containers.find((x) => x.containerSlug === containerSlug)!;
  c.depends.push({ containerSlug: target });
  return true;
}

const loading = new Set<string>();
/**
 * Load the head text of the text files mounted by the site's containers (for implicit / suggested depends in files),
 * once per head content; errors are ignored (the env-based inference still works).
 */
export async function loadSiteFileTexts(siteSlug: string): Promise<void> {
  const site = draft.work?.sites.find((s) => s.siteSlug === siteSlug);
  const files = draft.work?.files ?? [];
  if (!site) return;
  const slugs = new Set<string>();
  for (const c of site.config.containers) {
    if (!c.enabled) continue;
    for (const m of c.volumes) if (m.source.kind === 'file') slugs.add(m.source.fileSlug);
  }
  await Promise.all(
    [...slugs].map(async (slug) => {
      const head = draft.base?.config.files.find((f) => f.fileSlug === slug);
      const f = files.find((x) => x.fileSlug === slug);
      if (!head?.content || !f || f.editor === 'binary') return;
      const key = JSON.stringify(head.content);
      if (draft.headFileText[slug]?.key === key || loading.has(key + slug)) return;
      loading.add(key + slug);
      try {
        const text = await fetchText('getFileContent', apiPath('getFileContent', { fileSlug: slug }));
        draft.headFileText[slug] = { key, text };
      } catch {
        /* not fatal */
      } finally {
        loading.delete(key + slug);
      }
    }),
  );
}
