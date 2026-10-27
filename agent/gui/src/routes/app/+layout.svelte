<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { goto, afterNavigate } from '$app/navigation';
  import { base } from '$app/paths';
  import { page } from '$app/state';
  import { api, errMessage, isApiError } from '$lib/api';
  import { session } from '$lib/state/session.svelte';
  import { overview, proposals, headConfigFor, unreachableCount, prefs } from '$lib/state/app.svelte';
  import { appHref, moduleOf, parseAppPath, storeSite, storedSite } from '$lib/nav';
  import { poll } from '$lib/state/resource.svelte';
  import { draft, allDrafts, anyDirty, endDraftSessions, pageSite, setDraftUser } from '$lib/state/draft.svelte';
  import { branch } from '$lib/state/branch.svelte';
  import { provideAgentConfigHost } from '$lib/configHost';
  import { tunnel } from '$lib/state/tunnel.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { duration } from '$lib/format';
  import { routeLabel } from '$lib/ui/tones';
  import ActorPopup from '$lib/ui/ActorPopup.svelte';
  import PendingBanner from '$opm/branch/PendingBanner.svelte';
  import RebaseDialog from '$opm/branch/RebaseDialog.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import CopyId from '$lib/ui/CopyId.svelte';
  import Brand from '$lib/ui/Brand.svelte';
  import Avatar from '$lib/ui/Avatar.svelte';
  import SitesTree from '$lib/ui/SitesTree.svelte';
  import SidebarResizer, { loadSidebarWidth } from '$opm/primitives/SidebarResizer.svelte';
  import { deploymentLabel } from '$lib/cloud';
  import { agentAll } from '$lib/agent/state.svelte';
  import UpdateLockBanner from '$lib/ui/UpdateLockBanner.svelte';
  import RestartOverlay from '$lib/agent/RestartOverlay.svelte';
  // the shared entity forms ($opm, SPEC §21 #102) read the local working copy through this adapter
  provideAgentConfigHost();

  let { children } = $props();
  let ready = $state(false);
  let changing = $state(false);
  let navOpen = $state(false);
  let confirmLogout = $state(false);
  // the sidebar width (drag the edge; remembered like in the cloud)
  let sideW = $state<number | null>(loadSidebarWidth());
  let sideEl = $state<HTMLElement | null>(null);

  onMount(async () => {
    try {
      const [s, info] = await Promise.all([api.session({}), api.info({})]);
      session.session = s;
      session.info = info;
      session.setServerTime(info.serverTime);
      // §21 #118 opened through the cloud: the agent made a session for the cloud person — no email prompt
      if (s.remote) session.actor = { email: s.remote.email, token: s.remote.actorToken, expiresAt: s.remote.expiresAt };
      if (!session.actor) session.askActor = true;
      ready = true;
    } catch (e) {
      // 401 → api.ts already redirects to the login page
      if (!(isApiError(e) && e.status === 401)) toasts.err(e, 'Cannot reach the agent');
    }
  });

  // polling: overview 5 s (tiles, header), proposals 10 s (badge)
  $effect(() => {
    if (!ready) return;
    const a = poll(() => overview.load(), 5000);
    const b = poll(() => proposals.load(), 10_000);
    // round I: agent versions of every site (sidebar "upgrade available", Status / Sites hints)
    const c = poll(() => agentAll.load(), 60_000);
    // §21 #104 the branch (badge: uncommitted + behind)
    const d = poll(() => branch.load(), 15_000);
    return () => (a(), b(), c(), d());
  });
  // §21 #107 the drafts owner = the declared person: their drafts session (expiry + heartbeat) + stored draft offer
  $effect(() => {
    const email = session.actor?.email ?? null;
    if (email) untrack(() => void setDraftUser(email));
  });
  // §21 #107 the change set auto-saves as a local draft (IndexedDB, debounced; typed secret values never)
  $effect(() => {
    void JSON.stringify(draft.work);
    void draft.base?.head?.revId;
    untrack(() => draft.persist());
  });
  let offerBusy = $state(false);
  async function restoreOffer() {
    offerBusy = true;
    try {
      if (await draft.restoreOffer()) {
        toasts.ok('Changes restored', 'Still not committed — commit them from the change bar.');
        // §21 #107 file contents are restored from this browser's storage; lost bytes must be uploaded again
        if (draft.lostBytes.length) toasts.err(`The content of ${draft.lostBytes.length} changed file${draft.lostBytes.length === 1 ? '' : 's'} is no longer stored in this browser — upload or edit ${draft.lostBytes.length === 1 ? 'it' : 'them'} again (or Undo) before committing.`, 'Content missing');
      }
    } catch (e) {
      toasts.err(e, 'Could not restore the changes');
    } finally {
      offerBusy = false;
    }
  }
  // the head config follows the head revision (this agent's set)
  let lastHead: string | null | undefined = undefined;
  $effect(() => {
    const h = overview.data?.head?.revId ?? null;
    if (!ready || overview.data === undefined) return;
    if (h !== lastHead) {
      lastHead = h;
      untrack(() => headConfigFor(overview.data!.self).load());
    }
  });
  // §21 #126 another site's page edits THAT site's branch: its head loads when the page opens it and every 20 s
  $effect(() => {
    if (!ready || overview.data === undefined) return;
    const s = pageSite();
    if (!s || s === overview.data.self) return;
    const hc = headConfigFor(s);
    untrack(() => {
      if (!hc.loaded && !hc.loading) void hc.load();
    });
    const t = setInterval(() => void hc.load(), 20_000);
    return () => clearInterval(t);
  });
  // session end (6 h, no sliding): go to login
  $effect(() => {
    if (!ready || !session.session) return;
    if (session.sessionLeftMs() <= 0) {
      untrack(() => {
        session.clear('Your session ended (6 h). Log in again.');
        goto(`${base}/login?next=${encodeURIComponent(page.url.pathname + page.url.search)}`, { replaceState: true });
      });
    }
  });
  afterNavigate(() => (navOpen = false));

  // nav2 (SPEC §21 #97): ONE site selection, part of the URL (/_opm/app/{site}/…), remembered per tab
  const routeSite = $derived(page.params.siteSlug);
  $effect(() => {
    const s = routeSite;
    if (s) untrack(() => storeSite(s));
  });
  const sel = $derived(routeSite ?? (storedSite() || overview.data?.self || '*'));

  function onUnload(e: BeforeUnloadEvent) {
    // §21 #107 pending changes are auto-saved as a local draft (offered again after the reload): no prompt for them
    for (const d of allDrafts()) d.flush();
    if (tunnel.active) {
      e.preventDefault();
      e.returnValue = '';
    }
  }

  async function logout() {
    try {
      await api.logout({});
    } catch (e) {
      toasts.err(e, 'Logout failed');
    }
    if (tunnel.active) await tunnel.stop().catch(() => {});
    for (const d of allDrafts()) d.discard();
    await endDraftSessions(); // §21 #107 this person's local drafts are deleted at logout
    session.clear(null);
    await goto(`${base}/login`, { replaceState: true });
  }
  function cycleTheme() {
    prefs.theme = prefs.theme === 'system' ? 'dark' : prefs.theme === 'dark' ? 'light' : 'system';
    prefs.save();
  }

  const path = $derived(page.url.pathname);
  const loc = $derived(parseAppPath(path));
  const curMod = $derived(moduleOf(loc));
  /** breadcrumb: site › module › sub-pages (deployment-wide modules: Deployment › module) */
  const crumbs = $derived.by(() => {
    if (!loc || !curMod) return [{ label: 'On-Prem Manager' }];
    const s = loc.site;
    const out: { label: string; href?: string; mono?: boolean }[] = curMod.wide
      ? [{ label: curMod.sec === 'agent' ? 'Agent' : 'Deployment' }]
      : [{ label: s === '*' ? 'All sites' : s, mono: s !== '*', href: s === '*' ? undefined : appHref(s, 'status') }];
    // §21 #97 dev tools: site › Dev tools › tool (the connection is in the page)
    if (curMod.sec === 'devtools') {
      out.push({ label: 'Dev tools', href: curMod.k === 'devtools' ? undefined : appHref(s, 'devtools') }, { label: curMod.label });
      return out;
    }
    const names: Record<string, string> = { download: 'Download', containers: 'Containers', edge: 'Edge', volumes: 'Volumes', settings: 'Settings', route: 'Route', import: 'Import nginx.conf' };
    const rest = loc.rest;
    out.push({ label: curMod.label, href: rest.length ? appHref(s, curMod.k) : undefined });
    let acc = curMod.k;
    rest.forEach((seg, i) => {
      acc += `/${seg}`;
      const last = i === rest.length - 1;
      const nm = curMod.k === 'containers' ? undefined : names[seg];
      out.push({ label: nm ?? decodeURIComponent(seg), mono: !nm, href: last || (curMod.k === 'config' && seg === 'edge' && rest[i + 1] === undefined) ? undefined : appHref(s, acc) });
    });
    if (curMod.k === 'config' && rest[1] === 'route') out[out.length - 1] = { label: page.url.searchParams.get('id') ?? 'new route', mono: true };
    return out;
  });

  const o = $derived(overview.data);
  const unreachable = $derived(unreachableCount(o));
  const depLabel = $derived(deploymentLabel(session.info));
  const selfTile = $derived(o?.sites.find((x) => x.self));
  /** deployment-wide pages (the cloud sidebar's top + Config groups); the site part of their URL is only a filter */
  const wideNav = $derived([
    { k: 'activities', label: 'Activities', icon: 'activity' },
    { k: 'backups', label: 'Backups', icon: 'archive' },
    { k: 'internet', label: 'Internet', icon: 'globe' },
  ]);
  const cloudTone = $derived(!o ? '' : o.cloud.route === 'none' ? 'red' : o.cloud.route === 'direct' ? 'green' : 'amber');
  const leftMs = $derived.by(() => {
    void session.tick;
    return session.sessionLeftMs();
  });
</script>


<svelte:window onbeforeunload={onUnload} />

<div class="shell" class:nav-open={navOpen} style={sideW ? `--sidebar-w:${sideW}px` : undefined}>
  <!-- §21 #110 a running update is a warning, never a lock: keep working, or stop it here -->
  <UpdateLockBanner lock={o?.lock ?? null} href={appHref(sel, 'branch')} />
  {#if o?.deleted}
    <div class="banner bad flat"><Icon name="alert" /><div class="grow">This deployment was deleted in the cloud. The containers keep running; the agent stopped polling the cloud.</div></div>
  {/if}
  {#if tunnel.active && loc?.mod !== 'internet'}
    <a class="tunnelbar" href={appHref(sel, 'internet')}><Icon name="globe" size={14} />This tab gives internet to the deployment — keep it open · {tunnel.requests} requests</a>
  {/if}

  <div class="body">
    <nav class="side" aria-label="Main" bind:this={sideEl}>
      <SidebarResizer bind:width={sideW} {sideEl} />
      <div class="brand">
        <a class="brand-link" href="{base}/app" aria-label="On-Prem Manager agent home">
          <Brand />
          <span class="bname"><span class="strong nowrap">On-Prem Manager</span><span class="badge accent">agent</span></span>
        </a>
      </div>
      <div class="scope" data-testid="status-strip">
        <div class="who">
          <span class="dav" aria-hidden="true"><Icon name="server" size={16} /></span>
          <span class="grow">
            <span class="strong ellipsis dname" title={depLabel ?? o?.deploymentId}>{depLabel ?? 'Deployment'}</span>
            {#if o}
              <span class="row tight xs"><span class="chip mono did" title="Deployment id"><CopyId value={o.deploymentId} label="deployment id" short={10} /></span></span>
            {:else}
              <span class="skel" style="width:120px;margin-top:4px"></span>
            {/if}
          </span>
        </div>
        {#if o}
          <div class="status">
            <span class="sv" title="This site (the agent you opened)"><Icon name="server" size={12} /><span class="mono strong" data-testid="this-site">{o.self}</span>{#if selfTile?.agentVersion}<span class="faint">{selfTile.agentVersion}</span>{/if}</span>
            <span class="badge {o.head ? 'accent' : 'outline'}" data-testid="head-seq">{o.head ? `head seq ${o.head.seq}` : 'no head yet'}</span>
            {#if unreachable === 0}
              <span class="sv green" data-testid="reachability" title="every enrolled site answers"><i class="dot"></i>all sites reachable</span>
            {:else}
              <span class="sv amber" data-testid="reachability"><i class="dot"></i>{unreachable} {unreachable === 1 ? 'site' : 'sites'} unreachable</span>
            {/if}
            <span class="sv {cloudTone}" title="How this site reaches the cloud"><i class="dot"></i>cloud {routeLabel(o.cloud.route)}</span>
          </div>
        {:else}
          <span class="skel" style="width:auto;height:40px;margin:0 10px 10px"></span>
        {/if}
      </div>

      <div class="navs">
        {#snippet item(m: { k: string; label: string; icon: string })}
          <a class="nav-item" href={appHref(sel, m.k)} aria-current={curMod?.k === m.k ? 'page' : undefined} data-testid="side-{m.k}">
            <Icon name={m.icon} size={16} /><span class="grow">{m.label}</span>
            {#if m.k === 'internet' && tunnel.active}<span class="nav-cnt blue" title="this tab gives internet to the deployment">on</span>{/if}
          </a>
        {/snippet}
        {#each wideNav as m (m.k)}{@render item(m)}{/each}
        <SitesTree />
      </div>

      <div class="sfoot">
        {#if session.actor}
          <div class="actor" data-testid="actor-chip">
            <div class="am">
              <Avatar name={session.actor.email} size={28} />
              <span class="txt">
                <span class="nm" title={session.actor.email}>{session.actor.email}</span>
                {#if session.session?.remote}<span class="em" data-testid="remote-chip">through the cloud</span>{:else}<span class="em" class:warn-text={leftMs < 15 * 60_000}>session ends in {duration(leftMs)}</span>{/if}
              </span>
              <button class="btn ghost icon sm th" onclick={cycleTheme} aria-label="Theme: {prefs.theme}" title="Theme: {prefs.theme}" data-testid="theme"><Icon name={prefs.theme === 'dark' ? 'moon' : prefs.theme === 'light' ? 'sun' : 'monitor'} size={16} /></button>
            </div>
            {#if !session.session?.remote}
              <div class="acts">
                <button class="btn ghost sm" onclick={() => ((navOpen = false), (changing = true), (session.askActor = true))} data-testid="change-person"><Icon name="user" size={14} />Change person</button>
                <button class="btn ghost sm" onclick={() => (anyDirty() || tunnel.active ? (confirmLogout = true) : logout())} data-testid="logout"><Icon name="logout" size={14} />Log out</button>
              </div>
            {/if}
          </div>
        {:else}
          <div class="am">
            <span class="grow faint small">No person declared yet</span>
            <button class="btn ghost icon sm th" onclick={cycleTheme} aria-label="Theme: {prefs.theme}" title="Theme: {prefs.theme}"><Icon name={prefs.theme === 'dark' ? 'moon' : prefs.theme === 'light' ? 'sun' : 'monitor'} size={16} /></button>
          </div>
        {/if}
        <div class="faint xs ver">{#if session.info}agent {session.info.agentVersion} · via {session.info.surface}{/if}</div>
      </div>
    </nav>
    <button class="scrim" aria-label="Close menu" tabindex="-1" onclick={() => (navOpen = false)}></button>

    <main class="content" id="main">
      <div class="crumbs" data-testid="breadcrumb">
        <button class="btn ghost icon burger" aria-label="Menu" aria-expanded={navOpen} onclick={() => (navOpen = !navOpen)}><Icon name="menu" size={18} /></button>
        <nav aria-label="Breadcrumb" class="bcnav">
          <ol class="bc">
            {#each crumbs as c, i (i)}
              <li class:last={i === crumbs.length - 1}>
                {#if i > 0}<Icon name="chevronRight" size={12} />{/if}
                {#if c.href}<a href={c.href} class:mono={c.mono}>{c.label}</a>{:else}<span aria-current="page" class:mono={c.mono}>{c.label}</span>{/if}
              </li>
            {/each}
          </ol>
        </nav>
      </div>
      <div class="page" class:full={loc?.mod === 'devtools'}>
        {#if ready}
          {#if draft.offer && !draft.dirty}
            <div style="margin-bottom:14px">
              <PendingBanner state={draft.offer.state} count={draft.offer.stored.count} savedAt={draft.offer.stored.savedAt} baseSeq={draft.offer.stored.baseSeq} headSeq={draft.offer.headSeq ?? draft.base?.head?.seq ?? null} droppedValues={draft.offer.stored.droppedValues} busy={offerBusy} onrestore={restoreOffer} ondiscard={() => { const st = draft.offer?.state; draft.dropOffer(); if (st === 'same') toasts.ok('Draft discarded'); }} />
            </div>
          {/if}
          {@render children()}
        {:else}
          <div class="stack" aria-busy="true">
            <span class="skel" style="width:220px;height:22px"></span>
            <span class="skel" style="width:100%;height:120px"></span>
            <span class="skel" style="width:100%;height:120px"></span>
          </div>
        {/if}
      </div>
    </main>
  </div>
</div>

<RebaseDialog open={!!draft.rebaseUi} conflicts={draft.rebaseUi?.conflicts ?? []} clean={draft.rebaseUi?.clean ?? 0} headLabel={draft.rebaseUi?.headLabel ?? 'the head'} onapply={(c) => draft.rebaseUi?.resolve(c)} onclose={() => draft.rebaseUi?.cancel()} />

<ActorPopup
  open={ready && session.askActor}
  cancellable={changing}
  ondone={() => {
    changing = false;
    if (session.actor) toasts.ok(`Working as ${session.actor.email}`);
  }}
/>
<Confirm bind:open={confirmLogout} title="Log out?" confirmLabel="Log out" danger onconfirm={logout}>
  {#if tunnel.active}<p>This tab gives internet to the deployment. Logging out stops the tunnel.</p>{/if}
  {#if anyDirty()}<p>You have {allDrafts().reduce((n, d) => n + d.pendingCount, 0)} changes that are not committed. Logging out deletes them from this browser — commit them to apply.</p>{/if}
</Confirm>

<!-- the agent restart overlay (Agent page restart / upgrade, and "Stop, restart agents" in the update bar) -->
<RestartOverlay />

{#if overview.error && !overview.loaded}
  <div class="offline banner bad"><Icon name="wifiOff" /><div class="grow">The agent does not answer: {errMessage(overview.error)}</div></div>
{/if}

<style>
  .shell {
    min-height: 100dvh;
    display: flex;
    flex-direction: column;
    /* no top bar: sticky page parts stick to the top */
    --topbar-h: 0px;
  }
  .body {
    flex: 1;
    display: grid;
    grid-template-columns: var(--sidebar-w) minmax(0, 1fr);
    min-height: 0;
  }

  /* ── sidebar (cloud Shell + DeploymentShell look) */
  .side {
    position: sticky;
    top: 0;
    align-self: start;
    height: 100dvh;
    display: flex;
    flex-direction: column;
    background: var(--surface-2);
    border-right: 1px solid var(--border);
    z-index: var(--z-sidebar);
  }
  .brand {
    padding: 12px 12px 4px;
  }
  .brand-link {
    display: flex;
    align-items: center;
    gap: 9px;
    color: var(--text);
    padding: 4px;
    font-size: var(--fs-md);
  }
  .brand-link:hover {
    text-decoration: none;
  }
  .bname {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .scope {
    margin: 6px 12px 4px;
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--surface);
    box-shadow: var(--shadow-sm);
    display: flex;
    flex-direction: column;
  }
  .who {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px;
    min-width: 0;
  }
  .who .grow {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .dav {
    width: 32px;
    height: 32px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    background: var(--accent-soft);
    color: var(--accent-text);
    flex: none;
  }
  .dname {
    display: block;
    line-height: 1.3;
  }
  .row.tight {
    gap: 4px;
  }
  .did {
    height: 20px;
    max-width: 100%;
    padding: 0 6px;
  }
  .status {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 5px 10px;
    padding: 2px 10px 9px;
    font-size: var(--fs-xs);
    color: var(--text-2);
  }
  .status .badge {
    height: 18px;
    font-size: 10.5px;
  }
  .sv {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    color: var(--text-2);
    white-space: nowrap;
  }
  .sv :global(svg) {
    color: var(--text-3);
  }
  .sv .dot {
    width: 7px;
    height: 7px;
    background: var(--text-3);
  }
  .sv.green .dot {
    background: var(--green);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--green) 20%, transparent);
  }
  .sv.amber .dot {
    background: var(--amber);
  }
  .sv.red .dot {
    background: var(--red);
  }
  .navs {
    flex: 1;
    overflow-y: auto;
    padding: 6px 8px 12px;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .nav-item :global(svg) {
    flex: none;
  }
  .grp {
    padding: 12px 10px 4px;
    font-size: 11px;
    font-weight: var(--fw-semibold);
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--text-3);
  }
  .sfoot {
    border-top: 1px solid var(--border);
    padding: 8px;
  }
  .actor {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .am {
    display: flex;
    align-items: center;
    gap: 9px;
    padding: 4px 2px 4px 6px;
    min-width: 0;
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
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .em {
    font-size: var(--fs-xs);
    color: var(--text-3);
    white-space: nowrap;
  }
  .th {
    flex: none;
  }
  .acts {
    display: flex;
    gap: 2px;
  }
  .acts .btn {
    flex: 1;
    color: var(--text-2);
  }
  .acts .btn:hover {
    color: var(--text);
  }
  .ver {
    padding: 4px 6px 0;
  }

  /* ── main column: breadcrumb row + page (cloud Shell) */
  .content {
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .crumbs {
    display: flex;
    align-items: center;
    gap: 6px;
    min-height: 44px;
    padding: 8px var(--sp-8) 0;
    max-width: 1320px;
    width: 100%;
    margin: 0 auto;
  }
  .bcnav {
    min-width: 0;
  }
  .bc {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    align-items: center;
    min-width: 0;
    flex-wrap: nowrap;
    overflow: hidden;
    font-size: var(--fs-sm);
    color: var(--text-2);
  }
  .bc li {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    min-width: 0;
    flex: none;
    white-space: nowrap;
  }
  .bc li.last {
    flex: 0 1 auto;
    overflow: hidden;
  }
  .bc li :global(svg) {
    color: var(--text-3);
    margin: 0 2px;
    flex: none;
  }
  .bc a {
    color: var(--text-2);
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .bc a:hover {
    color: var(--text);
    text-decoration: none;
  }
  .bc span {
    color: var(--text);
    font-weight: var(--fw-medium);
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 12.5px;
  }
  .page {
    padding: var(--sp-3) var(--sp-8) var(--sp-10);
    max-width: 1320px;
    margin: 0 auto;
    width: 100%;
    min-width: 0;
  }
  /* §21 #97: the dev tools workbenches use the whole width */
  .page.full {
    max-width: none;
    padding-bottom: var(--sp-4);
  }
  .burger {
    display: none;
    flex: none;
  }
  .tunnelbar {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 9px 16px;
    background: var(--amber-soft);
    color: var(--text);
    border-bottom: 1px solid color-mix(in srgb, var(--amber) 30%, transparent);
    font-size: var(--fs-md);
    text-decoration: none;
  }
  .tunnelbar {
    background: var(--blue-soft);
    border-bottom-color: color-mix(in srgb, var(--blue) 30%, transparent);
    font-weight: var(--fw-medium);
  }
  .flat {
    border-radius: 0;
    border-width: 0 0 1px;
  }
  .scrim {
    display: none;
  }
  .offline {
    position: fixed;
    left: 50%;
    bottom: 16px;
    transform: translateX(-50%);
    z-index: var(--z-banner);
    box-shadow: var(--shadow-lg);
  }
  @media (max-width: 900px) {
    .burger {
      display: inline-flex;
    }
    .body {
      grid-template-columns: minmax(0, 1fr);
    }
    .side {
      position: fixed;
      top: 0;
      left: 0;
      bottom: 0;
      height: 100dvh;
      width: min(290px, 86vw);
      transform: translateX(-100%);
      transition: transform var(--dur) var(--ease);
      box-shadow: none;
    }
    .nav-open .side {
      transform: none;
      box-shadow: var(--shadow-lg);
    }
    .nav-open .scrim {
      display: block;
      position: fixed;
      inset: 0;
      z-index: var(--z-scrim);
      background: var(--overlay);
      border: 0;
    }
  }
  @media (max-width: 720px) {
    .crumbs {
      padding: 6px var(--sp-4) 0 6px;
    }
    .page {
      padding: var(--sp-3) var(--sp-4) var(--sp-8);
    }
    /* phones: only the last two crumbs */
    .bc li:not(:nth-last-child(-n + 2)) {
      display: none;
    }
    .bc li:nth-last-child(2) :global(svg) {
      display: none;
    }
    .bc a {
      max-width: 130px;
    }
  }
</style>
