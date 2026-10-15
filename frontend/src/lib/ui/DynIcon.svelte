<script lang="ts">
  /** Legacy wrapper: renders any Icon value via IconView (kept so older call sites keep working). */
  import IconView, { type IconType } from './IconView.svelte';
  let { name, size = 18, class: cls = '', type = 'service' }: { name: string | null | undefined; size?: number; class?: string; type?: IconType } = $props();
</script>

<IconView value={name} {size} class={cls} {type} />
