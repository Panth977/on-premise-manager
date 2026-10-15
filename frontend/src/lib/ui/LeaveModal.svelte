<script lang="ts">
  import { beforeNavigate, goto } from '$app/navigation';
  import { guard } from '../util/guard.svelte';
  import Modal from './Modal.svelte';
  import Button from './Button.svelte';

  let open = $state(false);
  beforeNavigate((nav) => {
    if (guard.bypass) {
      guard.bypass = false;
      return;
    }
    if (nav.willUnload) {
      if (guard.anyDirty()) nav.cancel(); // browser shows its own prompt
      return;
    }
    const to = nav.to?.url ?? null;
    if (to && guard.blocks(to)) {
      nav.cancel();
      guard.pending = to;
      open = true;
    }
  });
  function leave() {
    const u = guard.pending;
    open = false;
    guard.pending = null;
    if (u) {
      guard.bypass = true;
      goto(u.pathname + u.search + u.hash);
    }
  }
  function stay() {
    open = false;
    guard.pending = null;
  }
</script>

<Modal bind:open title="Leave without saving?" size="sm" onclose={stay} testid="leave-modal">
  <p class="muted">You have unsaved changes on this page. They are lost when you leave.</p>
  {#snippet footer()}
    <Button onclick={stay} data-testid="leave-stay">Stay</Button>
    <Button variant="danger" onclick={leave} data-testid="leave-go">Leave</Button>
  {/snippet}
</Modal>
