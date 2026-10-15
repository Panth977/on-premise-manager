<script lang="ts">
  /**
   * The cloud console's field = the shared field of both GUIs ($opm Field, SPEC §21 #102): label (+ required star) ·
   * control · errors (data-testid="field-error") · warnings · hint. Kept as a thin wrapper so every cloud form keeps
   * its import path and props.
   */
  import type { Snippet } from 'svelte';
  import SharedField from '$opm/primitives/Field.svelte';
  type Props = {
    label?: string;
    hint?: string;
    error?: string | string[] | null;
    warning?: string | string[] | null;
    required?: boolean;
    id?: string;
    children: Snippet;
    aside?: Snippet;
    class?: string;
    testid?: string;
    lock?: string | null;
  };
  let { label, hint, error = null, warning = null, required = false, id, children, aside, class: cls = '', testid, lock = null }: Props = $props();
</script>

<SharedField {label} {hint} {error} {warning} {required} {id} {aside} class={cls} {testid} {lock}>{@render children()}</SharedField>
