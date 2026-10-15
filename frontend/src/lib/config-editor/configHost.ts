/**
 * The cloud console's ConfigHost (SPEC §21 #102): how the shared entity forms of `$opm` (ui/) see a template or a
 * deployment draft — the working config (site working copies applied), the cloud catalog and the scope's validation.
 * Set once per scope (TemplateShell / DeploymentShell) next to setScope.
 */
import { setConfigHost, type ConfigHost } from '$opm/forms/host';
import type { ConfigScope } from './scope.svelte';
import { catalog } from '../stores/catalog.svelte';
import { pageHtml } from '../pages404/page-html';

export function cloudConfigHost(scope: ConfigScope & { kind: 'template' | 'deployment' }): ConfigHost {
  return {
    get scope() {
      return scope.kind;
    },
    config: () => scope.working(),
    get deploymentId() {
      return scope.deploymentId;
    },
    image: (serviceSlug, version) => {
      const i = catalog.image(serviceSlug, version);
      if (i) return { ...i, reference: i.reference, platform: i.platform, description: i.description };
      // §21 #113 a site image the head runs (added on a site / adopted from its Docker; not in the cloud catalog)
      const si = scope.siteImages?.images[`${serviceSlug}:${version}`];
      return si ? { ...si, reference: si.reference ? `site image ${si.reference}` : 'site image (built on the site)', description: 'An image of the site’s Docker — not in the cloud catalog.' } : undefined;
    },
    service: (slug) => {
      const s = catalog.service(slug) ?? scope.siteImages?.services[slug];
      return s ? { label: s.label, icon: s.icon } : undefined;
    },
    services: () => catalog.services.map((s) => ({ serviceSlug: s.serviceSlug, label: s.label, icon: s.icon })),
    versions: (slug) => catalog.imagesOf(slug).map((i) => ({ version: i.version, reference: i.reference, status: i.import.status, kind: i.kind })),
    vctx: () => scope.vctx(),
    validate: (cfg) => scope.validate(cfg).list,
    secretIsSet: (slug) => scope.secretsSet?.includes(slug),
    notFoundPageHtml: (id) => pageHtml(id),
    detectedHosts: () => scope.detected,
    notFoundPages: () => catalog.pages.map((p) => ({ notFoundPageId: p.notFoundPageId, label: p.label })),
    lock: () => null,
    get readOnly() {
      return !scope.canWrite;
    },
  };
}
export function provideCloudConfigHost(scope: ConfigScope & { kind: 'template' | 'deployment' }) {
  return setConfigHost(cloudConfigHost(scope));
}
