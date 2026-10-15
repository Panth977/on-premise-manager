<script lang="ts">
  import { session } from '$lib/session.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import { History } from '$lib/ui/icons';
  import AuditHistory from './AuditHistory.svelte';

  /** "History" button + drawer with the audit entries of one target. Rendered only for accounts.admin. */
  let { target, title = 'History', iconOnly = false, size = 'md' }: { target: string; title?: string; iconOnly?: boolean; size?: 'md' | 'sm' } = $props();
  let open = $state(false);
</script>

{#if session.isAdmin}
  {#if iconOnly}
    <Button {size} variant="ghost" icon={History} tip="History" aria-label="History of {title}" onclick={() => (open = true)} data-testid="history-button" />
  {:else}
    <Button {size} icon={History} onclick={() => (open = true)} data-testid="history-button">History</Button>
  {/if}
  <Drawer bind:open {title} subtitle="Audit log of this item" width="wide" testid="history-drawer">
    {#if open}<AuditHistory {target} />{/if}
  </Drawer>
{/if}
