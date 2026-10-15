/**
 * The ONE adapter between the shared entity forms (SPEC §21 #102) and an app. The cloud console builds it from its
 * ConfigScope (template / deployment editor), the agent GUI from its local-edit draft. Forms read it from Svelte
 * context (`useConfigHost()`), so the same component renders in cloud create / edit, agent read and agent local edit.
 */
import { getContext, setContext } from 'svelte';
import type { Config, Icon, ImageSpec, NotFoundPage, ValidationIssue } from '@opm/shared/types';
import type { ValidationContext } from '@opm/shared/validate';

/** What a form needs of an image (cloud Image and revision-catalog ImageSpec both satisfy it). */
export type ImageLike = Pick<ImageSpec, 'serviceSlug' | 'version' | 'kind' | 'entrypoint' | 'command' | 'workdir' | 'user' | 'ports' | 'env' | 'volumes' | 'healthcheck' | 'stopTimeoutSec'> & {
  /** shown as the image hint (docker reference in the cloud, digest on the agent) */
  reference?: string;
  platform?: string;
  description?: string;
};
export type FormScope = 'template' | 'deployment' | 'agent';
export type FormMode = 'create' | 'edit' | 'read';

export interface ConfigHost {
  scope: FormScope;
  /** the working config (reactive in the app's store) */
  config(): Config;
  deploymentId: string;
  /** the image of a container (service + version) */
  image(serviceSlug: string, version: string): ImageLike | undefined;
  service(serviceSlug: string): { label: string; icon: Icon } | undefined;
  /** all service slugs + versions the cloud catalog offers (cloud create only; agent: none) */
  services?(): { serviceSlug: string; label: string; icon: Icon }[];
  /** versions of a service, newest first; `status` = import status (cloud: only 'ready' may be picked) */
  versions?(serviceSlug: string): { version: string; reference?: string; status?: string; kind?: string }[];
  /** the ValidationContext of this place (catalog, deployment id, secrets set, detected hosts): container resolution + depends */
  vctx?(): ValidationContext;
  /** fileSlug → known text of files (implicit / suggested depends in mounted files); agent only */
  fileContents?(): Record<string, string>;
  /** validate a candidate config (shared validateConfig with the app's mode + local-edit rules) */
  validate(cfg: Config): ValidationIssue[];
  /** a secret has a value (deployment / agent); undefined in a template */
  secretIsSet?(secretSlug: string): boolean | undefined;
  /** not-found pages that may be picked (cloud: library; agent: those in the head catalog) */
  notFoundPages?(): Pick<NotFoundPage, 'notFoundPageId' | 'label'>[];
  /** the HTML of a not-found page (thumbnails); null = unknown */
  notFoundPageHtml?(notFoundPageId: string): string | null | Promise<string | null>;
  /** siteSlug → detected / advertised address of enrolled sites (auto hosts); empty in a template */
  detectedHosts?(): Record<string, string>;
  /**
   * Why a config path cannot change HERE (null = editable): e.g. agent "Cloud only — change it in the cloud console"
   * for a container's service / version / name. Path = validator path (`sites[app].config.containers[api].serviceSlug`).
   */
  lock(path: string): string | null;
  /** the whole editor is read-only here (no write permission, update running, no head …) */
  readOnly: boolean;
  /** text of a file (template preview / starter content); undefined = not loaded */
  fileText?(fileSlug: string): string | undefined;
  /** app routes the forms link to */
  href?: { container?(siteSlug: string, containerSlug: string): string; secrets?(): string; files?(): string; volumes?(siteSlug: string): string };
}

const KEY = Symbol('opm-config-host');
export function setConfigHost(h: ConfigHost): ConfigHost {
  return setContext(KEY, h);
}
export function useConfigHost(): ConfigHost {
  const h = getContext<ConfigHost | undefined>(KEY);
  if (!h) throw new Error('shared form used outside a ConfigHost context (setConfigHost)');
  return h;
}

/** issues of a candidate under `prefix` (exact path, `.child`, `[i]`) */
export function issuesUnder(all: readonly ValidationIssue[], prefix: string): ValidationIssue[] {
  return all.filter((i) => i.path === prefix || i.path.startsWith(prefix + '.') || i.path.startsWith(prefix + '['));
}
/** issues exactly at a path (or its direct children when `deep`) */
export function issuesAt(all: readonly ValidationIssue[], path: string, deep = false): ValidationIssue[] {
  return all.filter((i) => i.path === path || (deep && (i.path.startsWith(path + '.') || i.path.startsWith(path + '['))));
}
export const errorsOf = (l: readonly ValidationIssue[]) => l.filter((i) => i.severity === 'error');
