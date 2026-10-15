/** Option lists of the shared form pickers (§17.0 pickers rule) built from the ConfigHost — same in both GUIs. */
import type { ConfigHost } from './host';
import type { SelectOption } from '../primitives/listbox';

export function secretOptions(host: ConfigHost): { value: string; label?: string; hint?: string }[] {
  return host.config().secrets.map((s) => {
    const set = host.secretIsSet?.(s.secretSlug);
    return { value: s.secretSlug, hint: [s.label !== s.secretSlug ? s.label : '', set === undefined ? '' : set ? 'has a value' : 'no value yet'].filter(Boolean).join(' · ') || undefined };
  });
}
export function fileOptions(host: ConfigHost): { value: string; label?: string; hint?: string }[] {
  return host.config().files.map((f) => ({ value: f.fileSlug, hint: [f.label !== f.fileSlug ? f.label : '', f.mime].filter(Boolean).join(' · ') }));
}
export function containerOptions(host: ConfigHost, siteSlug: string): SelectOption[] {
  const site = host.config().sites.find((s) => s.siteSlug === siteSlug);
  return (site?.config.containers ?? []).map((c) => ({ value: c.containerSlug, label: c.containerSlug, hint: `${c.serviceSlug}:${c.version}${c.enabled ? '' : ' · disabled'}` }));
}
