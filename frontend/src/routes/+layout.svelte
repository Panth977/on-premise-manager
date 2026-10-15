<script lang="ts">
  import '@fontsource-variable/inter';
  import '@fontsource-variable/jetbrains-mono';
  import '$lib/styles/app.css';
  import Toaster from '$lib/ui/Toaster.svelte';
  import LeaveModal from '$lib/ui/LeaveModal.svelte';
  let { children } = $props();
</script>

{@render children()}
<Toaster />
<LeaveModal />
