/**
 * Catalog cache (services, images of every service, not-found pages, tags, registries) for selects and
 * client-side validation (ValidationContext). Every account can read services / images / pages / tags.
 */
import type { Image, NotFoundPageListItem, RegistryView, ServiceListItem, TagListItem, ValidationContext, Account } from '@opm/shared';
import { api, errMsg } from '../api';
import { session } from '../session.svelte';

const TTL = 20_000;

class Catalog {
  services: ServiceListItem[] = $state([]);
  images: Record<string, Image[]> = $state({});
  pages: NotFoundPageListItem[] = $state([]);
  tags: TagListItem[] = $state([]);
  registries: RegistryView[] = $state([]);
  accounts: Account[] = $state([]);
  loaded = $state(false);
  loading = $state(false);
  error: string | null = $state(null);
  #at = 0;
  #p: Promise<void> | null = null;
  #hadAccounts = false;
  #hadRegistries = false;
  async #loadPeople() {
    try {
      if (!this.#hadRegistries && session.can('registries', 'read')) ((this.registries = (await api.listRegistries({})).items), (this.#hadRegistries = true));
      if (!this.#hadAccounts && session.can('accounts', 'read')) ((this.accounts = (await api.listAccounts({})).items), (this.#hadAccounts = true));
    } catch {
      /* optional lists */
    }
  }

  ensure(force = false): Promise<void> {
    if (!force && this.loaded && Date.now() - this.#at < TTL) {
      // the first load may have run before the session knew the permissions (accounts / registries skipped)
      if (!this.#p && ((!this.#hadAccounts && session.can('accounts', 'read')) || (!this.#hadRegistries && session.can('registries', 'read')))) {
        this.#p = this.#loadPeople().finally(() => (this.#p = null));
        return this.#p;
      }
      return Promise.resolve();
    }
    if (this.#p) return this.#p;
    this.#p = this.#load().finally(() => (this.#p = null));
    return this.#p;
  }
  async #load() {
    this.loading = true;
    try {
      const [svcs, pages, tags] = await Promise.all([api.listServices({}), api.listNotFoundPages({}), api.listTags({})]);
      const imgs = await Promise.all(svcs.items.map((s) => api.listImages({ serviceSlug: s.serviceSlug }).then((r) => [s.serviceSlug, r.items] as const)));
      this.services = svcs.items;
      this.images = Object.fromEntries(imgs);
      this.pages = pages.items;
      this.tags = tags.items;
      this.#hadRegistries = this.#hadAccounts = false;
      await this.#loadPeople();
      this.loaded = true;
      this.error = null;
      this.#at = Date.now();
    } catch (e) {
      this.error = errMsg(e);
    } finally {
      this.loading = false;
    }
  }
  invalidate() {
    this.#at = 0;
  }
  /** a service by its slug (THE id) */
  service(slug: string) {
    return this.services.find((s) => s.serviceSlug === slug) ?? null;
  }
  /** kept for older call sites: the slug is the id */
  slugOf(slug: string) {
    return slug;
  }
  image(serviceSlug: string, version: string) {
    return this.images[serviceSlug]?.find((i) => i.version === version) ?? null;
  }
  /** images of a service, newest first */
  imagesOf(serviceSlug: string) {
    return [...(this.images[serviceSlug] ?? [])].sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  }
  /** `<serviceSlug>:<version>` → image volumes (normalizeConfig / normalizeSiteConfig of legacy containers) */
  imageVolumes(): Record<string, Pick<Image, 'volumes'>> {
    const out: Record<string, Pick<Image, 'volumes'>> = {};
    for (const [sid, list] of Object.entries(this.images)) for (const i of list) out[`${sid}:${i.version}`] = { volumes: i.volumes };
    return out;
  }
  page(id: string | null) {
    return id ? (this.pages.find((p) => p.notFoundPageId === id) ?? null) : null;
  }
  /** ValidationContext for validateConfig (cloud catalog, import status included). */
  vctx(mode: ValidationContext['mode'], deploymentId: string, secretsSet?: string[], detectedHosts?: Record<string, string>): ValidationContext {
    const services: ValidationContext['services'] = {};
    const images: ValidationContext['images'] = {};
    for (const s of this.services) services[s.serviceSlug] = { label: s.label };
    for (const [sid, list] of Object.entries(this.images)) for (const i of list) images[`${sid}:${i.version}`] = { kind: i.kind, ports: i.ports, env: i.env, volumes: i.volumes, healthcheck: i.healthcheck, importStatus: i.import.status };
    return { mode, deploymentId, services, images, notFoundPageIds: this.pages.map((p) => p.notFoundPageId), secretsSet, detectedHosts };
  }
}
export const catalog = new Catalog();
