<script lang="ts">
  /** Multi-select of tags (chips with the tag colour + icon), options from the catalog cache. */
  import { onMount } from 'svelte';
  import { catalog } from '../stores/catalog.svelte';
  import MultiSelect, { type MsOption } from './MultiSelect.svelte';
  type Props = { value: string[]; id?: string; testid?: string; disabled?: boolean; invalid?: boolean; placeholder?: string; onchange?: (v: string[]) => void; only?: string[] | null };
  let { value = $bindable([]), id, testid, disabled = false, invalid = false, placeholder = 'Choose tags', onchange, only = null }: Props = $props();
  onMount(() => catalog.ensure());
  const options = $derived<MsOption[]>(catalog.tags.filter((t) => !only || only.includes(t.tagSlug)).map((t) => ({ value: t.tagSlug, label: t.label, hint: t.tagSlug, color: t.color, icon: t.icon ?? null })));
</script>

<MultiSelect {options} bind:value {id} {testid} {disabled} {invalid} {placeholder} {onchange} emptyText={catalog.loading ? 'Loading tags…' : 'No tags yet — create one on the Tags page'} />
