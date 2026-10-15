<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { Tone } from './tones';
  let { tone = '', dot = false, title, children }: { tone?: Tone; dot?: boolean; title?: string; children: Snippet } = $props();
</script>

<span class="badge {tone}" {title}>{#if dot}<i class="dot"></i>{/if}{@render children()}</span>
