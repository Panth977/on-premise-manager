<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { session } from '$lib/session.svelte';
  import Spinner from '$lib/ui/Spinner.svelte';
  import { onMount } from 'svelte';
  import { startDrafts } from '$lib/branch/drafts.svelte';
  let { children } = $props();
  // §21 #107 local drafts of this account (expiry purge, account switch, heartbeat) once signed in
  $effect(() => {
    const uid = session.status === 'signed-in' ? session.account?.uid : null;
    if (uid) void startDrafts(uid);
  });
  onMount(() => {
    session.init();
  });
  $effect(() => {
    if (session.status === 'signed-out') {
      const next = page.url.pathname + page.url.search;
      goto(`/login?next=${encodeURIComponent(next)}`, { replaceState: true });
    }
  });
</script>

{#if session.status === 'signed-in' && session.me}
  {@render children()}
{:else}
  <div class="splash" aria-busy="true">
    <div class="logo">OPM</div>
    <Spinner size={18} />
  </div>
{/if}

<style>
  .splash {
    height: 100vh;
    display: grid;
    place-content: center;
    justify-items: center;
    gap: 16px;
    color: var(--text-2);
  }
  .logo {
    font-weight: 700;
    letter-spacing: 0.08em;
    color: var(--text-3);
  }
</style>
