<script lang="ts">
  /**
   * Accounts (avatar + name + e-mail), multi-select by default; `single` = one account (value [] or [uid]).
   * `extra` adds pseudo options first (e.g. { value: 'system', label: 'system' } in the audit-log filter).
   * Needs accounts.read for the list (catalog cache); unknown uids still show as their uid.
   */
  import { onMount } from 'svelte';
  import { catalog } from '../stores/catalog.svelte';
  import MultiSelect, { type MsOption } from './MultiSelect.svelte';
  type Props = { value: string[]; single?: boolean; id?: string; testid?: string; disabled?: boolean; invalid?: boolean; placeholder?: string; extra?: MsOption[]; onchange?: (v: string[]) => void; exclude?: string[] };
  let { value = $bindable([]), single = false, id, testid, disabled = false, invalid = false, placeholder = 'Choose people', extra = [], onchange, exclude = [] }: Props = $props();
  onMount(() => catalog.ensure());
  const options = $derived<MsOption[]>([
    ...extra,
    ...catalog.accounts.filter((a) => !exclude.includes(a.uid)).map((a) => ({ value: a.uid, label: a.name, hint: a.email + (a.disabled ? ' · disabled' : ''), avatar: { name: a.name, img: a.img } })),
  ]);
</script>

<MultiSelect {options} bind:value {single} {id} {testid} {disabled} {invalid} {placeholder} {onchange} emptyText={catalog.loading ? 'Loading accounts…' : 'No accounts'} />
