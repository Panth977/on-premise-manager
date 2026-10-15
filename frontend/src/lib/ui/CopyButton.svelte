<script lang="ts">
  import Icon from './Icon.svelte';
  import { Copy, Check } from './icons';
  import { toast } from '../toast.svelte';
  let { text, label = '', size = 'sm', tip = 'Copy', testid }: { text: string; label?: string; size?: 'sm' | 'md'; tip?: string; testid?: string } = $props();
  let done = $state(false);
  async function copy(e: Event) {
    e.stopPropagation();
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const ta = document.createElement('textarea');
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      ta.remove();
    }
    done = true;
    toast.success('Copied to the clipboard');
    setTimeout(() => (done = false), 1600);
  }
</script>

<button type="button" class="btn {size === 'sm' ? 'sm' : ''} {label ? '' : 'icon ghost'}" onclick={copy} data-tip={label ? undefined : tip} aria-label={label || tip} data-testid={testid}>
  <Icon icon={done ? Check : Copy} size={size === 'sm' ? 13 : 15} />
  {#if label}{label}{/if}
</button>
