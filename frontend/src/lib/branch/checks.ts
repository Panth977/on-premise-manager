/**
 * §21 #104 checks of a deployment change set before a commit (formerly on Diff / Publish): per-site "your agent is too
 * old for this feature" warnings (dev-tool presets 0.11.0, container env 0.11.1, static values + hash filters 0.13.1,
 * volume content 0.12.0). Rendered File sources (small text files) are read once for the expression scan.
 */
import { fetchContent } from '../config-editor/files';
import { oldAgentSites, devToolsAgentWarning } from '../config-editor/devtools/devtools';
import { oldContentAgentSites, contentAgentWarning } from '../config-editor/volume-content/content';
import type { DeploymentScope } from '../config-editor/scope.svelte';

const texts = new Map<string, string>();
export async function loadFileTexts(scope: DeploymentScope): Promise<Record<string, string>> {
  const out: Record<string, string> = {};
  await Promise.all(
    scope.working().files.map(async (f) => {
      const sha = f.content?.sha256;
      if (f.render !== 'expressions' || !sha || (f.content?.sizeBytes ?? 0) > 1 << 20) return;
      if (!texts.has(sha)) {
        try {
          texts.set(sha, await (await fetchContent(sha)).text());
        } catch {
          return;
        }
      }
      out[f.fileSlug] = texts.get(sha)!;
    }),
  );
  return out;
}
export function featureAgentWarnings(scope: DeploymentScope, fileTexts: Record<string, string>) {
  const sites = scope.status?.sites ?? [];
  return {
    devtools: oldAgentSites(scope.working(), sites, fileTexts).map(devToolsAgentWarning),
    content: oldContentAgentSites(scope.working().sites, sites).map(contentAgentWarning),
  };
}
