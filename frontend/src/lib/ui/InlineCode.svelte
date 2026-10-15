<script lang="ts">
  /** Text with `backtick` spans rendered as inline code (hints, validation messages). No HTML is interpreted. */
  let { text }: { text: string } = $props();
  const parts = $derived(text.split('`').map((t, i) => ({ t, code: i % 2 === 1 })));
</script>

{#each parts as p, i (i)}{#if p.code}<code class="inline mono">{p.t}</code>{:else}{p.t}{/if}{/each}
