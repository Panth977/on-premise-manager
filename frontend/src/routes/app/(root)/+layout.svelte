<script lang="ts">
  import Shell, { type NavItem } from '$lib/shell/Shell.svelte';
  import { session } from '$lib/session.svelte';
  import { MOCK } from '$lib/env';
  import { Rocket, LayoutTemplate, Boxes, Database, FileCode, Tags, Users, ScrollText, GitBranch } from '$lib/ui/icons';
  let { children } = $props();
  const nav = $derived.by(() => {
    const items: (NavItem & { show: boolean })[] = [
      { href: '/app/deployments', label: 'Deployments', icon: Rocket, show: true },
      { href: '/app/templates', label: 'Templates', icon: LayoutTemplate, show: session.can('templates', 'read'), group: 'Catalog' },
      { href: '/app/services', label: 'Services', icon: Boxes, show: session.can('services', 'read'), group: 'Catalog' },
      { href: '/app/registries', label: 'Registries', icon: Database, show: session.can('registries', 'read'), group: 'Catalog' },
      { href: '/app/not-found-pages', label: 'Not-found pages', icon: FileCode, show: session.can('notFoundPages', 'read'), group: 'Catalog' },
      { href: '/app/tags', label: 'Tags', icon: Tags, show: session.can('tags', 'read'), group: 'Admin' },
      { href: '/app/accounts', label: 'Accounts', icon: Users, show: session.can('accounts', 'read'), group: 'Admin' },
      { href: '/app/audit-log', label: 'Audit log', icon: ScrollText, show: session.isAdmin, group: 'Admin' },
      { href: '/app/github', label: 'GitHub', icon: GitBranch, show: session.isAdmin, group: 'Admin' },
    ];
    return items.filter((i) => i.show);
  });
</script>

<Shell {nav} scope="root">
  {#snippet brand()}
    <a href="/app/deployments" class="brand-link" data-testid="brand">
      <img src="/favicon.svg" alt="" width="26" height="26" />
      <span class="strong nowrap">On-Prem Manager</span>
      {#if MOCK}<span class="badge amber">mock</span>{/if}
    </a>
  {/snippet}
  {@render children()}
</Shell>

<style>
  .brand-link {
    display: flex;
    align-items: center;
    gap: 9px;
    color: var(--text);
    padding: 6px 4px;
    font-size: var(--fs-md);
  }
  .brand-link:hover {
    text-decoration: none;
  }
</style>
