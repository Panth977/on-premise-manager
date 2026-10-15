<script lang="ts" module>
  import type { ComboOption } from '../ui/Combobox.svelte';
  import { catalog } from '../stores/catalog.svelte';

  /** Versions of a service as Combobox options: newest first, reference as second line, import status badge. */
  export function versionOptions(serviceSlug: string, current?: string | null): ComboOption[] {
    const list = catalog.imagesOf(serviceSlug);
    const opts: ComboOption[] = list.map((i) => ({
      value: i.version,
      label: i.version,
      sub: i.reference,
      badge: i.import.status,
      detail: i.kind === 'job' ? 'job' : undefined,
      disabled: i.import.status !== 'ready' && i.version !== current,
    }));
    if (current && !list.some((i) => i.version === current)) opts.unshift({ value: current, label: current, sub: 'not in the catalog', badge: 'missing' });
    return opts;
  }
  /** newest ready version (else the newest) */
  export function defaultVersion(serviceSlug: string): string {
    const list = catalog.imagesOf(serviceSlug);
    return list.find((i) => i.import.status === 'ready')?.version ?? list[0]?.version ?? '';
  }
</script>

<script lang="ts">
  import Combobox from '../ui/Combobox.svelte';
  /** Version (image) of one service: dropdown of its registered versions, not free text. */
  type Props = { serviceSlug: string; value: string; id?: string; testid?: string; disabled?: boolean; invalid?: boolean; size?: 'md' | 'sm'; onchange?: (v: string) => void };
  let { serviceSlug, value = $bindable(''), id, testid, disabled = false, invalid = false, size = 'md', onchange }: Props = $props();
  const options = $derived(versionOptions(serviceSlug, value));
</script>

<Combobox
  {id}
  {testid}
  {options}
  {value}
  {disabled}
  {invalid}
  {size}
  mono
  placeholder="choose a version"
  emptyText="No versions registered — register an image first"
  ariaLabel="Version"
  onselect={(o) => {
    value = o.value;
    onchange?.(o.value);
  }}
/>
