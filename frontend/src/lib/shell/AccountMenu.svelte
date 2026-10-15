<script lang="ts">
  /**
   * Sidebar footer (every scope, round D): account button (avatar, name, e-mail) → menu (My profile, Log out) and the
   * theme toggle. The menu and the profile modal are portaled to <body> (the sidebar is a transformed drawer on phones).
   */
  import { goto } from '$app/navigation';
  import { getContext } from 'svelte';
  import { session } from '../session.svelte';
  import { theme } from '../util/theme.svelte';
  import Avatar from '../ui/Avatar.svelte';
  import Icon from '../ui/Icon.svelte';
  import { ChevronsUpDown, LogOut, User, Sun, Moon, Monitor } from '../ui/icons';
  import ProfileModal from './ProfileModal.svelte';
  import { MOCK } from '../env';
  import { portal, floating } from '../ui/portal';

  const shell = getContext<{ closeNav: () => void } | undefined>('opm.shell');
  let open = $state(false);
  let profileOpen = $state(false);
  let root = $state<HTMLElement | null>(null);
  let btn = $state<HTMLElement | null>(null);
  let popEl = $state<HTMLElement | null>(null);
  const acc = $derived(session.account!);
  async function logout() {
    open = false;
    await session.logout();
    goto('/login', { replaceState: true });
  }
  function onkey(e: KeyboardEvent) {
    if (e.key === 'Escape' && open) {
      e.stopPropagation();
      open = false;
      btn?.focus();
    }
  }
  const themeLabel = $derived(theme.mode === 'dark' ? 'Dark' : theme.mode === 'light' ? 'Light' : 'System');
</script>

<svelte:window onclick={(e) => open && root && !root.contains(e.target as Node) && !popEl?.contains(e.target as Node) && (open = false)} />

<div class="am" bind:this={root} onkeydown={onkey} role="presentation">
  <button bind:this={btn} class="who-btn" aria-haspopup="menu" aria-expanded={open} onclick={() => (open = !open)} data-testid="account-chip">
    <Avatar name={acc.name} img={acc.img} size={28} />
    <span class="txt">
      <span class="nm">{acc.name}</span>
      <span class="em">{acc.email}</span>
    </span>
    <Icon icon={ChevronsUpDown} size={14} />
  </button>
  <button class="btn ghost icon th" aria-label="Theme: {themeLabel} (click to change)" data-tip="Theme: {themeLabel}" onclick={() => theme.cycle()} data-testid="theme-toggle">
    <Icon icon={theme.mode === 'dark' ? Moon : theme.mode === 'light' ? Sun : Monitor} size={16} />
  </button>
  {#if open}
    <div class="pop" role="menu" bind:this={popEl} onkeydown={onkey} tabindex="-1" use:portal use:floating={{ anchor: root, placement: 'bottom-start', offset: 6, minWidth: 240 }} data-testid="account-menu">
      <div class="who">
        <Avatar name={acc.name} img={acc.img} size={34} />
        <div class="grow" style="min-width:0">
          <div class="strong ellipsis">{acc.name}</div>
          <div class="muted small ellipsis">{acc.email}</div>
          {#if session.isAdmin}<span class="badge accent" style="margin-top:4px">admin</span>{/if}
          {#if MOCK}<span class="badge amber" style="margin-top:4px">mock API</span>{/if}
        </div>
      </div>
      <button role="menuitem" class="it" onclick={() => ((open = false), shell?.closeNav(), (profileOpen = true))} data-testid="menu-profile"><Icon icon={User} size={15} />My profile</button>
      <button role="menuitem" class="it" onclick={logout} data-testid="menu-logout"><Icon icon={LogOut} size={15} />Log out</button>
    </div>
  {/if}
</div>

<ProfileModal bind:open={profileOpen} />

<style>
  .am {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .who-btn {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 9px;
    border: 1px solid transparent;
    background: transparent;
    padding: 5px 6px;
    border-radius: var(--r-md);
    cursor: pointer;
    color: var(--text-2);
    text-align: left;
  }
  .who-btn:hover,
  .who-btn[aria-expanded='true'] {
    background: var(--surface-hover);
    color: var(--text);
  }
  .txt {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    line-height: 1.25;
  }
  .nm {
    font-weight: var(--fw-medium);
    color: var(--text);
    font-size: var(--fs-md);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .em {
    font-size: var(--fs-xs);
    color: var(--text-3);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .th {
    flex: none;
  }
  .pop {
    width: 260px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    box-shadow: var(--shadow-lg);
    padding: 6px;
  }
  .who {
    display: flex;
    gap: 10px;
    padding: 8px 8px 10px;
    border-bottom: 1px solid var(--border);
    margin-bottom: 4px;
  }
  .it {
    display: flex;
    align-items: center;
    gap: 9px;
    width: 100%;
    padding: 8px 9px;
    border: 0;
    background: transparent;
    border-radius: var(--r-sm);
    cursor: pointer;
    color: var(--text);
    font-size: var(--fs-md);
  }
  .it:hover {
    background: var(--surface-hover);
  }
</style>
