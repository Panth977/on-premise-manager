<script lang="ts">
  import '../app.css';
  import Toasts from '$lib/ui/Toasts.svelte';
  let { children } = $props();
</script>

{@render children()}
<Toasts />
