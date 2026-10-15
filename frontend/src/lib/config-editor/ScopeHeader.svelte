<script lang="ts">
  import type { Snippet } from 'svelte';
  import PageHeader from '../ui/PageHeader.svelte';
  import type { IconNode } from '../ui/icons';
  /** Page header inside a scope (round D: template and deployment pages alike — the breadcrumb above names the scope). */
  type Props = { kind: 'template' | 'deployment'; title: string; subtitle?: string; icon?: IconNode; readOnly?: boolean; actions?: Snippet; meta?: Snippet; back?: { href: string; label: string } };
  let { title, subtitle, icon, readOnly = false, actions, meta, back }: Props = $props();
</script>

<PageHeader {title} {subtitle} {icon} {readOnly} {actions} {meta} {back} />
