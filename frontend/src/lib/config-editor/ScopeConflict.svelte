<script lang="ts">
  import ConflictModal from '../ui/ConflictModal.svelte';
  import type { ConfigScope } from './scope.svelte';
  /** The "Someone else changed this" modal of a scope (template ifVersion / draft ifRevision). Place once per scope layout. */
  let { scope }: { scope: ConfigScope } = $props();
  let open = $state(false);
  $effect(() => {
    open = !!scope.conflict;
  });
</script>

<ConflictModal
  bind:open
  current={scope.conflict?.current ?? null}
  what={scope.kind === 'template' ? 'version' : 'draft version'}
  onreload={async () => {
    await scope.conflict?.reload();
  }}
  onkeep={async () => {
    await scope.conflict?.keep();
  }}
/>
