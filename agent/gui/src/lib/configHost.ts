/**
 * The agent GUI's ConfigHost (SPEC §21 #102): how the shared entity forms of `$opm` (ui/) see the local working copy —
 * the draft config, the head revision catalog, the agent validation (local-edit rules + validateConfig mode agent) and
 * the locks of a local edit (§21 #113: only the image is picked instead of typed). Set once in routes/app/+layout.svelte.
 */
import { setConfigHost, type ConfigHost } from '$opm/forms/host';
import { LOCK_IMAGE_PICKER } from '$opm/forms/locks';
import { emptyConfig } from '@opm/shared/validate';
import { draft } from '$lib/state/draft.svelte';
import { agentCtx } from '$lib/containerPreview';

/**
 * §21 #113 a site is standalone: every field is a local edit. Only the image of a container (serviceSlug + version)
 * is not a free-text field — it is picked with "Change image…" (catalog version, docker reference or host image).
 */
export function agentLock(path: string): string | null {
  if (/^sites\[[^\]]+\]\.config\.containers\[[^\]]+\]\.(serviceSlug|version)$/.test(path)) return LOCK_IMAGE_PICKER;
  return null;
}

export function agentConfigHost(): ConfigHost {
  return {
    scope: 'agent',
    config: () => draft.asConfig('work') ?? emptyConfig(),
    get deploymentId() {
      return draft.deploymentId || 'deployment';
    },
    image: (serviceSlug, version) => draft.base?.catalog?.images[`${serviceSlug}:${version}`],
    service: (slug) => {
      const s = draft.base?.catalog?.services[slug];
      return s ? { label: s.label, icon: s.icon } : undefined;
    },
    validate: (cfg) => draft.issuesFor(cfg),
    secretIsSet: (slug) => draft.secretIsSet(slug),
    notFoundPages: () => Object.values(draft.base?.catalog?.notFoundPages ?? {}).map((p) => ({ notFoundPageId: p.notFoundPageId, label: p.label })),
    notFoundPageHtml: (id) => draft.base?.catalog?.notFoundPages[id]?.htmlCode ?? null,
    detectedHosts: () => draft.detectedHosts,
    lock: agentLock,
    get readOnly() {
      return !draft.work;
    },
    fileText: (slug) => draft.fileContents[slug],
    fileContents: () => draft.fileContents,
    vctx: () => ({ ...agentCtx(draft.asConfig('work') ?? emptyConfig(), draft.deploymentId), notFoundPageIds: draft.base?.catalog ? Object.keys(draft.base.catalog.notFoundPages) : undefined }),
  };
}
export function provideAgentConfigHost() {
  return setConfigHost(agentConfigHost());
}
