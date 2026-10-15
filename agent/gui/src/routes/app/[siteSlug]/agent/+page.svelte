<script lang="ts">
  /**
   * Agent page (round I, SPEC §21 #90): everything `opm-agent` does on the command line — version / status, verify-release
   * (Check release), upgrade (+ rollback), restart, agent.json (masked view + safe settings), support bundle, uninstall
   * commands, the CLI → GUI map. "*" = the versions of every site + Upgrade all.
   */
  import { onMount, untrack } from 'svelte';
  import { appHref } from '$lib/nav';
  import { page } from '$app/state';
  import type { AgentConfigView, AgentSelfInfo, AgentSelfRelease, PatchAgentConfigResponse } from '@opm/shared/agent-self';
  import { SUPPORT_BUNDLE_SECTIONS } from '@opm/shared/agent-self';
  import type { AgentUpgradeProgress } from '@opm/shared/types';
  import { AGENT_UPGRADE_HEALTH_SEC, GUI_UPGRADE_MIN_VERSION, supportsGuiUpgrade } from '@opm/shared/types';
  import { api, errCode, errMessage } from '$lib/api';
  import { overview } from '$lib/state/app.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { Resource } from '$lib/state/resource.svelte';
  import { fmtDateTime } from '$lib/format';
  import { uptime } from '$lib/tools/fmt';
  import { downloadUrl } from '$lib/tools/api';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Time from '$lib/ui/Time.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import CopyId from '$lib/ui/CopyId.svelte';
  import PageTabs, { tabOf } from '$lib/ui/PageTabs.svelte';
  import ChooseSite from '$lib/ui/ChooseSite.svelte';
  import TerminalTool from '$lib/tools/TerminalTool.svelte';
  import { routeLabel } from '$lib/ui/tones';
  import { agentApi, agentSelfList, fetchHealth, isNetworkError } from '$lib/agent/api';
  import { agentAll } from '$lib/agent/state.svelte';
  import { restart } from '$lib/agent/restart.svelte';
  import { copyText } from '$lib/agent/copy';
  import UpgradeProgress from '$lib/agent/UpgradeProgress.svelte';
  import VerificationCard from '$lib/agent/VerificationCard.svelte';
  import ConfigCard from '$lib/agent/ConfigCard.svelte';
  import ProcessCard from '$lib/agent/ProcessCard.svelte';
  import UpgradeAll from '$lib/agent/UpgradeAll.svelte';

  const slug = $derived(page.params.siteSlug ?? '*');
  const all = $derived(slug === '*');
  const o = $derived(overview.data);
  const isSelf = $derived(o?.self === slug);

  // ── data
  const info = new Resource<AgentSelfInfo>(async () => (await agentSelfList(slug))[0]);
  const ustate = new Resource(() => agentApi.upgradeState({ siteSlug: slug }));
  const config = new Resource<AgentConfigView>(() => agentApi.agentConfig({ siteSlug: slug }));
  /** §21 #91 /_opm/api/health is THIS agent only (public) — shown on this site's page */
  const health = new Resource(() => fetchHealth());
  function loadSite() {
    info.load();
    ustate.load();
    config.load();
    health.load();
  }
  $effect(() => {
    void slug;
    untrack(() => {
      info.reset();
      ustate.reset();
      config.reset();
      checked = null;
      watching = null;
      if (slug === '*') agentAll.load();
      else loadSite();
    });
  });
  // keep the cards fresh (uptime, last heartbeat) while nothing runs
  onMount(() => {
    const t = setInterval(() => {
      if (document.visibilityState !== 'visible' || restart.active || watchingNow) return;
      if (slug === '*') agentAll.load();
      else (info.load(), health.load());
    }, 15_000);
    return () => clearInterval(t);
  });
  const i = $derived(info.data);
  const h = $derived(isSelf ? (health.data ?? null) : null);
  /** DB schema of the running build: /health, else /info (this site only; absent before 0.10.0) */
  const schema = $derived(isSelf ? (h?.schema ?? session.info?.schema ?? null) : null);
  const oldAgent = $derived(!info.loaded && errCode(info.error) === 'not-found');
  const tile = $derived(o?.sites.find((t) => t.siteSlug === slug));
  const multi = $derived((o?.sites.filter((t) => t.enrolled).length ?? 0) >= 2);
  const rel = $derived<AgentSelfRelease | null>(i?.release ?? null);
  const pin = $derived((all ? agentAll.data?.find((x) => x.self)?.release?.version : rel?.version) ?? null);
  const selfInfo = $derived(all ? agentAll.data?.find((x) => x.self) : isSelf ? i : agentAll.data?.find((x) => x.self));

  /** why writes are blocked: only a running agent upgrade (409 conflict) — §21 #110 an update / snapshot never blocks */
  const blocked = $derived.by(() => {
    const b = i?.busy;
    if (!b || b.kind !== 'upgrade') return null;
    return `An agent upgrade is running on ${slug} since ${fmtDateTime(b.since).slice(11, 16)} — try again when it ends (409 conflict)`;
  });
  /** §21 #110 an update / rollback / snapshot / restore runs: a warning only (restart, upgrade and saves still work) */
  const busyNote = $derived.by(() => {
    const b = i?.busy;
    if (!b || b.kind === 'upgrade') return null;
    const what = { update: 'An update', rollback: 'A rollback', snapshot: 'A snapshot', restore: 'A restore' }[b.kind];
    return `${what} is running on ${slug} since ${fmtDateTime(b.since).slice(11, 16)}.`;
  });

  // nav2 (§21 #97): the site comes from the global selector; tabs Agent | Host shell
  const TABS = [
    { k: 'agent', label: 'Agent', icon: 'server' },
    { k: 'shell', label: 'Host shell', icon: 'terminal' },
  ];
  const tab = $derived(tabOf(page.url, ['agent', 'shell']));

  // ── live uptime
  const up = $derived.by(() => {
    void session.tick;
    return i ? uptime((Date.now() - Date.parse(i.startedAt)) / 1000) : '—';
  });

  // ── release check (verify-release parity)
  let checked = $state<AgentSelfRelease | null>(null);
  let checking = $state(false);
  let checkErr = $state<string | null>(null);
  async function checkRelease() {
    checking = true;
    checkErr = null;
    try {
      checked = await agentApi.checkRelease({ siteSlug: slug });
      info.load();
    } catch (e) {
      checkErr = errMessage(e);
    } finally {
      checking = false;
    }
  }

  // ── upgrade
  let upOpen = $state(false);
  let upConfirm = $state('');
  let upBusy = $state(false);
  let upErr = $state<{ code: string | null; msg: string } | null>(null);
  let force = $state(false);
  let watching = $state<AgentUpgradeProgress | null>(null);
  let lastPhase = $state<string | null>(null);
  let siteRestarting = $state(false);
  let watchingNow = false;
  let watchGen = 0;
  const TERM = ['done', 'failed', 'rolled-back'];
  const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

  function openUpgrade(reinstall = false) {
    if (!session.actor) {
      session.askActor = true;
      toasts.info('Say who is working first', 'The upgrade is written to the activity log with your email.');
      return;
    }
    force = reinstall;
    upConfirm = '';
    upErr = null;
    upOpen = true;
  }
  async function startUpgrade() {
    if (!rel || upConfirm !== slug) return;
    upBusy = true;
    upErr = null;
    const prevStartedAt = i?.startedAt ?? null;
    try {
      const p = await agentApi.startUpgrade({ siteSlug: slug, body: { version: rel.version, confirm: slug, ...(force ? { force: true } : {}) } });
      upOpen = false;
      watching = p;
      lastPhase = p.phase;
      toasts.info(`Upgrading ${slug} to ${p.to}`, 'Download → verify → swap → restart → health');
      watch(slug, isSelf, prevStartedAt);
    } catch (e) {
      const c = errCode(e);
      const msg =
        c === 'stale'
          ? `The pinned release moved since this page loaded: ${errMessage(e)}`
          : c === 'conflict'
            ? `Another upgrade is already running on ${slug}: ${errMessage(e)}`
            : c === 'locked'
              ? `${errMessage(e)} (409 locked)`
              : errMessage(e);
      upErr = { code: c, msg };
    } finally {
      upBusy = false;
    }
  }
  /** follow an upgrade until done / failed / rolled-back (restart overlay for this site; "restarting" note for others) */
  async function watch(site: string, self: boolean, prevStartedAt: string | null) {
    const my = ++watchGen;
    watchingNow = true;
    let downSince = 0;
    try {
      while (my === watchGen) {
        try {
          const r = await agentApi.upgradeState({ siteSlug: site });
          siteRestarting = false;
          downSince = 0;
          ustate.data = r;
          if (r.current) {
            watching = r.current;
            if (!TERM.includes(r.current.phase)) lastPhase = r.current.phase;
          }
          if (!r.current || TERM.includes(r.current.phase)) break;
          if (self && r.current.phase === 'restart') {
            const res = await restart.wait({ site, prevStartedAt, title: 'Restarting the agent…', detail: `Upgrading ${site} to ${r.current.to}: the new agent starts now and must report healthy within ${AGENT_UPGRADE_HEALTH_SEC} s.` });
            if (res !== 'ok') return;
            continue;
          }
        } catch (e) {
          if (self && isNetworkError(e)) {
            const res = await restart.wait({ site, prevStartedAt, title: 'Restarting the agent…', detail: watching ? `Upgrading ${site} to ${watching.to} — waiting for the agent to answer again.` : '' });
            if (res !== 'ok') return;
            continue;
          }
          if (!self && (errCode(e) === 'unavailable' || isNetworkError(e))) {
            siteRestarting = true;
            downSince ||= Date.now();
            if (Date.now() - downSince > 120_000) {
              toasts.push('bad', `${site} did not answer for 2 minutes`, 'Open the agent GUI of that site, or check `systemctl status opm-agent` there.');
              return;
            }
          } else {
            toasts.err(e, 'Lost the upgrade state');
            return;
          }
        }
        await sleep(1000);
      }
      if (my !== watchGen) return;
      const p = watching;
      info.load();
      config.load();
      agentAll.load();
      overview.load();
      if (self) (health.load(), refreshSessionInfo());
      if (p?.phase === 'done') toasts.ok(`Now running ${p.to}`, `${site} upgraded from ${p.from}`);
      else if (p?.phase === 'rolled-back') toasts.push('bad', `Rolled back to ${p.from}`, p.error ?? undefined);
      else if (p?.phase === 'failed') toasts.push('bad', 'The upgrade failed', p.error ?? undefined);
    } finally {
      if (my === watchGen) watchingNow = false;
    }
  }
  // an upgrade already running when the page opens (or started from the cloud / CLI): follow it
  let followed = '';
  $effect(() => {
    const u = i?.upgrade;
    if (!u || TERM.includes(u.phase) || watchingNow || followed === u.upgradeId) return;
    followed = u.upgradeId;
    untrack(() => {
      watching = u;
      lastPhase = u.phase;
      watch(slug, isSelf, i?.startedAt ?? null);
    });
  });
  const shown = $derived.by(() => {
    if (watching) return watching;
    const u = i?.upgrade;
    if (!u) return null;
    // a recent failure stays visible (the banner explains it)
    if ((u.phase === 'failed' || u.phase === 'rolled-back') && u.finishedAt && Date.now() - Date.parse(u.finishedAt) < 3600_000) return u;
    return null;
  });
  let dismissed = $state<string | null>(null);

  // ── restart
  let rsOpen = $state(false);
  let rsConfirm = $state('');
  let rsReason = $state('');
  let rsBusy = $state(false);
  let rsErr = $state<string | null>(null);
  let remoteRestart = $state(false);
  function openRestart() {
    if (!session.actor) {
      session.askActor = true;
      return;
    }
    rsConfirm = '';
    rsReason = '';
    rsErr = null;
    rsOpen = true;
  }
  async function doRestart() {
    if (rsConfirm !== slug) return;
    rsBusy = true;
    rsErr = null;
    const prev = i?.startedAt ?? null;
    try {
      const r = await agentApi.restartAgent({ siteSlug: slug, body: { confirm: slug, ...(rsReason.trim() ? { reason: rsReason.trim() } : {}) } });
      rsOpen = false;
      await afterRestart(prev, `Restart via ${r.via === 'systemd' ? 'systemctl restart opm-agent' : 're-exec'}${rsReason.trim() ? ` — ${rsReason.trim()}` : ''}.`);
    } catch (e) {
      const c = errCode(e);
      rsErr = c === 'conflict' ? `${errMessage(e)} (409 ${c})` : errMessage(e);
    } finally {
      rsBusy = false;
    }
  }
  /** the sidebar footer "agent X" + the schema come from /info: refresh it after this agent restarted */
  function refreshSessionInfo() {
    api
      .info({})
      .then((x) => (session.info = x))
      .catch(() => {});
  }
  /** this site → the blocking overlay; another site → wait (≤ 120 s) for its startedAt to change */
  async function afterRestart(prevStartedAt: string | null, detail: string) {
    if (isSelf) {
      const res = await restart.wait({ site: slug, prevStartedAt, title: 'Restarting the agent…', detail });
      if (res === 'ok') {
        info.load();
        config.load();
        ustate.load();
        agentAll.load();
        health.load();
        refreshSessionInfo();
        toasts.ok('The agent is back', restart.version ? `${slug} runs ${restart.version} · same session` : 'same session');
      }
      return;
    }
    remoteRestart = true;
    const t0 = Date.now();
    try {
      while (Date.now() - t0 < 120_000) {
        await sleep(2000);
        try {
          const x = (await agentSelfList(slug))[0];
          if (x.startedAt !== prevStartedAt) {
            info.data = x;
            config.load();
            toasts.ok(`${slug} is back`, `agent ${x.version} · pid ${x.pid}`);
            return;
          }
        } catch {
          /* restarting */
        }
      }
      toasts.push('bad', `${slug} did not come back within 2 minutes`);
    } finally {
      remoteRestart = false;
    }
  }

  // ── agent.json saved
  function onSaved(r: PatchAgentConfigResponse, restartIt: boolean) {
    config.data = r.config;
    if (!r.changed.length) {
      toasts.info('Nothing changed');
      return;
    }
    toasts.ok('agent.json saved', `${r.changed.join(', ')}${r.restarting ? ' · restarting to apply' : ' · applies at the next restart'}`);
    if (r.restarting && restartIt) afterRestart(i?.startedAt ?? null, `Applying agent.json: ${r.changed.join(', ')}.`);
    else info.load();
  }

  // ── support bundle
  let sbBusy = $state(false);
  async function supportBundle() {
    if (!session.actor) {
      session.askActor = true;
      return;
    }
    sbBusy = true;
    try {
      const t = await agentApi.supportBundle({ siteSlug: slug });
      await downloadUrl(t.url, `opm-support-${slug}.tar.gz`);
      toasts.ok('Support bundle downloading', 'Send it to the operations team; secrets are masked.');
    } catch (e) {
      toasts.err(e, 'Support bundle failed');
    } finally {
      sbBusy = false;
    }
  }
  const SECTION_TEXT: Record<string, string> = {
    'version.json': 'version, build, uptime, ports, systemd, enrollment (no keys)',
    'agent.json': 'agent.json with secrets masked',
    'debug/': 'the last 24 h of the debug log (already redacted)',
    'docker-info.json': 'docker info + version (no registry logins)',
    'containers.json': 'the OPM containers (labels, state, ports; env values removed)',
    'activities.jsonl': 'the last 500 activities of this site',
    'status.json': 'the site status (as the Status page)',
    'upgrade/': 'upgrade state + history',
  };

  // ── CLI → GUI map
  const CLI: { cmd: string; what: string; where: string; href?: string; cliOnly?: boolean }[] = $derived([
    { cmd: 'opm-agent version', what: 'version, build, Go version', where: 'This page › Version' },
    { cmd: 'opm-agent status', what: 'process, ports, Docker, systemd, enrollment, cloud link', where: 'This page › the cards above' },
    { cmd: 'opm-agent verify-release', what: 'check the signature of the pinned release', where: 'This page › Check release' },
    { cmd: 'opm-agent upgrade', what: 'download, verify, swap, restart, rollback', where: 'This page › Upgrade to …' },
    { cmd: 'systemctl restart opm-agent', what: 'restart the agent (containers keep running)', where: 'This page › Restart agent' },
    { cmd: 'edit /etc/opm/agent.json', what: 'tools, container names, edge / alias ports', where: 'This page › agent.json' },
    { cmd: 'opm-agent snapshot list', what: 'the snapshots of the deployment', where: 'Backups › Snapshots', href: appHref(slug, 'backups') },
    { cmd: 'opm-agent snapshot export', what: 'download a .opmsnap (with / without images)', where: 'Backups › Download', href: appHref(slug, 'backups') },
    { cmd: 'opm-agent snapshot import', what: 'import a .opmsnap', where: 'Backups › Import .opmsnap', href: appHref(slug, 'backups') },
    { cmd: 'opm-agent keygen', what: 'create the site key', where: 'CLI only (before enrollment) — the fingerprint is shown above', cliOnly: true },
    { cmd: 'opm-agent enroll', what: 'register the site with the cloud', where: 'CLI only (the install command does it) — enrollment date + re-enroll help above', cliOnly: true },
    { cmd: 'opm-agent install', what: 'install the binary + systemd unit', where: 'CLI only (the install command from the cloud Setup page)', cliOnly: true },
  ]);

  const enrollTone = { enrolled: 'ok', 'not-enrolled': 'warn', revoked: 'bad' } as const;
</script>

<PageHead icon="server" eyebrow={all ? 'Agent · all sites' : `Agent · site ${slug}`} title={all ? 'Agent · all sites' : `Agent · ${slug}`} sub={all ? 'The opm-agent of every site: installed version, the pinned release, Upgrade all.' : 'Everything opm-agent does on the command line: version, status, release check, upgrade, restart, agent.json, support bundle.'}>
  {#snippet meta()}
    {#if i}
      <span class="badge outline mono" data-testid="agent-version">{i.version}</span>
      {#if i.release?.upgradeAvailable}<span class="badge accent" data-testid="upgrade-available">Upgrade available · {i.release.version}</span>{/if}
    {/if}
  {/snippet}
  {#snippet actions()}
    <Btn size="sm" icon="refresh" aria-label="Refresh" busy={info.loading || agentAll.loading} onclick={() => (all ? agentAll.load() : loadSite())} />
  {/snippet}
</PageHead>

<PageTabs tabs={TABS} label="Agent sections" testid="agent-tabs" />
{#if tab === 'shell'}
  {#if all}
    <ChooseSite what="a host shell" text={'"All sites" is selected; a host shell runs on the server of one site.'} />
  {:else}
    <TerminalTool site={slug} mode="host" />
  {/if}
{:else if all}
  <!-- ─────────────────────────── all sites -->
  <Load loading={agentAll.loading} loaded={agentAll.loaded} error={agentAll.error} retry={() => agentAll.load()} rows={5}>
    <section class="card" data-testid="agent-sites">
      <div class="card-head"><h2>Installed versions</h2>{#if pin}<span class="small muted">pinned release <span class="mono strong">{pin}</span></span>{/if}</div>
      <div class="table-wrap">
        <table class="table">
          <thead><tr><th>Site</th><th>Installed</th><th>Pinned</th><th>State</th><th>Uptime</th><th>Upgrade</th><th></th></tr></thead>
          <tbody>
            {#each o?.sites.filter((t) => t.enrolled) ?? [] as t (t.siteSlug)}
              {@const a = agentAll.data?.find((x) => x.siteSlug === t.siteSlug)}
              <tr data-testid="agent-row-{t.siteSlug}">
                <td class="mono small">{t.siteSlug}{#if t.self}<span class="badge info sb">this site</span>{/if}</td>
                <td class="mono small">{a?.version ?? t.agentVersion ?? '—'}</td>
                <td class="mono small">{a?.release?.version ?? pin ?? '—'}</td>
                <td class="small">
                  {#if !t.self && !t.reachable}<span class="badge warn">unreachable</span>
                  {:else if a?.upgrade && !TERM.includes(a.upgrade.phase)}<span class="badge accent">upgrading · {a.upgrade.phase}</span>
                  {:else if a}<span class="badge ok">running</span>
                  {:else if !supportsGuiUpgrade(t.agentVersion)}<span class="badge warn" title="rerun the install command of this site once">older than {GUI_UPGRADE_MIN_VERSION}</span>
                  {:else}<span class="faint">—</span>{/if}
                </td>
                <td class="small nowrap">{a ? uptime(a.uptimeSec) : '—'}</td>
                <td class="small">
                  {#if a?.release?.upgradeAvailable}<span class="badge accent">upgrade available</span>
                  {:else if a}<span class="badge ok">up to date</span>
                  {:else if t.reachable && !supportsGuiUpgrade(t.agentVersion)}<span class="muted">rerun the install command once</span>
                  {:else}<span class="faint">—</span>{/if}
                </td>
                <td class="right"><a class="btn xs" href={appHref(t.siteSlug, 'agent')}>Open</a></td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
    {#if multi && o}
      <div class="gap"></div>
      <UpgradeAll tiles={o.sites} infos={agentAll.data ?? []} self={o.self} {pin} selfStartedAt={selfInfo?.startedAt ?? null} blocked={null} onchanged={() => (agentAll.load(), overview.load())} />
    {/if}
  </Load>
{:else if oldAgent}
  <Empty icon="server" title="This site runs an agent older than {GUI_UPGRADE_MIN_VERSION}" text="It has no self-management API, so it cannot upgrade itself from a GUI or the cloud.">
    {#snippet detail()}
      <div class="stack tight small">
        <p style="margin:0">Installed: <span class="mono">{tile?.agentVersion ?? '?'}</span>. Rerun the install command of <strong>{slug}</strong> once on that server (cloud console › Setup › {slug} › View install command — it stays valid for upgrades). After that, upgrades work from this page.</p>
        <p class="muted" style="margin:0">{errMessage(info.error)}</p>
      </div>
    {/snippet}
  </Empty>
{:else}
  <Load loading={info.loading} loaded={info.loaded} error={info.error} retry={() => info.load()} rows={8}>
    {#if i}
      {#if blocked}<div class="banner warn mb" data-testid="agent-busy"><Icon name="lock" /><div class="grow"><strong>Upgrade, restart and agent.json saves are disabled.</strong> {blocked}</div></div>{:else if busyNote}<div class="banner warn mb" data-testid="agent-busy-note"><Icon name="alert" /><div class="grow">{busyNote} Restart, upgrade and agent.json saves still work — a restart or upgrade of the coordinator stops the update (nothing is rolled back unless you stop it with roll back).</div></div>{/if}
      {#if i.enrollment.state !== 'enrolled'}
        <div class="banner bad mb" data-testid="enroll-banner">
          <Icon name="alert" />
          <div class="grow">
            <strong>{i.enrollment.state === 'revoked' ? 'This site was revoked in the cloud.' : 'This site is not enrolled.'}</strong> The agent cannot reach the cloud or upgrade itself. Re-enroll on the server (keygen / enroll / install are CLI only):
            {#if i.enrollment.reenrollHelp?.length}
              <pre class="code lines" data-testid="reenroll-help">{i.enrollment.reenrollHelp.join('\n')}</pre>
              <Btn size="xs" icon="copy" onclick={() => copyText(i.enrollment.reenrollHelp!.join('\n'), 're-enroll steps')} data-testid="reenroll-copy">Copy</Btn>
            {/if}
          </div>
        </div>
      {/if}

      <!-- ─────────────────────────── version / status cards -->
      <div class="cards" data-testid="agent-cards">
        <section class="card card-pad c" data-testid="card-version">
          <h3><Icon name="box" size={15} />Version</h3>
          <dl class="kv small">
            <dt>Version</dt><dd class="mono strong">{i.version}</dd>
            <dt>Build</dt><dd>{i.build.goVersion} · {i.build.os}/{i.build.arch}</dd>
            <dt>Commit</dt><dd class="mono">{i.build.commit ?? 'dev build'}</dd>
            <dt>Built</dt><dd>{i.build.builtAt ? fmtDateTime(i.build.builtAt) : '—'}</dd>
            {#if schema !== null}<dt>DB schema</dt><dd data-testid="db-schema"><span class="mono">{schema}</span> <span class="faint">opm.db user_version</span></dd>{/if}
            <dt>Previous</dt><dd data-testid="prev-binary">{#if i.previousBinary.exists}<span class="mono">{i.previousBinary.version ?? '?'}</span> <span class="faint">kept for rollback</span>{:else}<span class="faint">none</span>{/if}</dd>
          </dl>
        </section>
        <section class="card card-pad c" data-testid="card-process">
          <h3><Icon name="pulse" size={15} />Process</h3>
          <dl class="kv small">
            <dt>PID</dt><dd class="mono">{i.pid}</dd>
            <dt>Uptime</dt><dd data-testid="uptime">{up} <span class="faint">· since <Time ts={i.startedAt} /></span></dd>
            <dt>Supervisor</dt>
            <dd>{#if i.supervisor.kind === 'systemd'}<span class="mono">{i.supervisor.unit}</span> <span class="badge {i.supervisor.activeState === 'active' ? 'ok' : 'warn'}">{i.supervisor.activeState}/{i.supervisor.subState}</span>{:else}<span class="muted">not under systemd</span>{/if}</dd>
            <dt>Boot guard</dt>
            <dd data-testid="boot-guard">{#if i.supervisor.restartGuard.installed}<span class="badge ok">installed</span>{:else}<span class="badge warn">not installed</span> <span class="faint">the watcher still rolls back; rerun the install command to add the guard</span>{/if}</dd>
          </dl>
        </section>
        <section class="card card-pad c" data-testid="card-paths">
          <h3><Icon name="folder" size={15} />Paths</h3>
          <dl class="kv small">
            <dt>Config</dt><dd class="mono">{i.configPath ?? '—'}</dd>
            <dt>Data dir</dt><dd class="mono">{i.dataDir}</dd>
            <dt>Binary</dt><dd class="mono">{i.binaryPath}</dd>
            <dt>Previous</dt><dd class="mono" class:faint={!i.previousBinary.exists}>{i.previousBinary.path}{i.previousBinary.exists ? '' : ' (absent)'}</dd>
          </dl>
        </section>
        <section class="card card-pad c" data-testid="card-ports">
          <h3><Icon name="network" size={15} />Ports</h3>
          <dl class="kv small">
            <dt>Agent</dt><dd class="mono">{i.ports.agentPort}</dd>
            <dt>GUI alias</dt><dd class="mono">{i.ports.guiAliasPort ?? 'none'}</dd>
            <dt>Default edge</dt><dd class="mono">{i.ports.defaultEdgeHttpPort ?? 'none'}</dd>
            <dt>Edge</dt><dd><span class="mono">{i.ports.edge.httpPort ?? '—'} / {i.ports.edge.httpsPort ?? '—'}</span> <span class="faint">http / https</span> <span class="badge {i.ports.edge.running ? 'ok' : 'warn'}">{i.ports.edge.running ? (i.ports.edge.defaultEdge ? 'default edge' : 'running') : 'stopped'}</span></dd>
            <dt>GUI on</dt><dd class="mono">{i.ports.guiPorts.map((g) => `${g.scheme}:${g.port}`).join(' · ') || '—'}</dd>
            {#if i.ports.edge.lastError}<dt>Edge error</dt><dd class="bad-text">{i.ports.edge.lastError}</dd>{/if}
          </dl>
        </section>
        <section class="card card-pad c" data-testid="card-docker">
          <h3><Icon name="container" size={15} />Docker</h3>
          <dl class="kv small">
            <dt>Engine</dt><dd>{#if i.docker.ok}<span class="mono">{i.docker.version ?? '?'}</span> <span class="badge ok">ok</span>{:else}<span class="badge bad">not reachable</span>{/if}</dd>
            <dt>API</dt><dd class="mono">{i.docker.apiVersion ?? '—'}</dd>
            <dt>Host</dt><dd class="mono">{i.docker.host}</dd>
            {#if i.docker.error}<dt>Error</dt><dd class="bad-text">{i.docker.error}</dd>{/if}
          </dl>
        </section>
        <section class="card card-pad c" data-testid="card-enrollment">
          <h3><Icon name="key" size={15} />Enrollment</h3>
          <dl class="kv small">
            <dt>State</dt><dd><span class="badge {enrollTone[i.enrollment.state]}" data-testid="enroll-state">{i.enrollment.state}</span></dd>
            <dt>Enrolled</dt><dd>{i.enrollment.enrolledAt ? fmtDateTime(i.enrollment.enrolledAt) : '—'}</dd>
            <dt>Site key</dt><dd><CopyId value={i.enrollment.siteKeyFingerprint} short={26} label="site key fingerprint" /> <span class="faint">v{i.enrollment.keyVersion}</span></dd>
            <dt>Deployment</dt><dd><CopyId value={i.enrollment.deploymentId} label="deployment id" /></dd>
            <dt>API base</dt><dd class="mono">{i.enrollment.apiBase}</dd>
          </dl>
        </section>
        {#if h}
          <section class="card card-pad c" data-testid="card-health">
            <h3><Icon name="pulse" size={15} />Health <span class="badge {h.ok ? 'ok' : 'bad'}" data-testid="health-ok">{h.ok ? 'ok' : 'not ok'}</span></h3>
            <ul class="checks small" data-testid="health-checks">
              {#each h.checks as c (c.name)}<li><Icon name={c.ok ? 'check' : 'x'} size={13} /><span class="mono">{c.name}</span>{#if c.message}<span class="muted">— {c.message}</span>{/if}</li>{/each}
            </ul>
            <p class="small" data-testid="socket-activated">
              {#if h.socketActivated}<span class="badge ok">socket activation</span> <span class="muted">edge sockets held by systemd — no refused connections during restarts</span>
              {:else}<span class="badge outline">no socket activation</span> <span class="muted">ports close for a moment while the agent restarts</span>{/if}
            </p>
          </section>
        {/if}
        <section class="card card-pad c wide" class:withh={!!h} data-testid="card-cloud">
          <h3><Icon name="globe" size={15} />Cloud</h3>
          <dl class="kv small">
            <dt>Route</dt><dd><span class="badge {i.cloud.route === 'direct' ? 'ok' : i.cloud.route === 'none' ? 'bad' : 'warn'}">{routeLabel(i.cloud.route)}</span> {#if !i.cloud.internet}<span class="faint">no direct internet</span>{/if}</dd>
            <dt>Last fetch</dt><dd><Time ts={i.cloud.lastFetchAt} /></dd>
            <dt>Last heartbeat</dt><dd><Time ts={i.cloud.lastHeartbeatAt} /></dd>
            {#if i.cloud.lastError}<dt>Last error</dt><dd class="warn-text">{i.cloud.lastError}</dd>{/if}
            <dt>Console</dt><dd>{#if i.cloud.consoleUrl}<a href={i.cloud.consoleUrl} target="_blank" rel="noopener" data-testid="console-link">{i.cloud.deploymentLabel ?? 'Open the cloud console'} <Icon name="external" size={12} /></a>{:else}<span class="faint">unknown</span>{/if}</dd>
          </dl>
        </section>
      </div>

      <!-- ─────────────────────────── release + upgrade -->
      <h2 class="sect">Release and upgrade</h2>
      <ProcessCard siteSlug={slug} /><!-- §21 #112 -->
      <section class="card" data-testid="release-card">
        <div class="card-body stack">
          <div class="rel">
            <div class="vers">
              <div><span class="section-title">Installed</span><div class="mono big" data-testid="installed-version">{i.version}</div></div>
              <Icon name="chevronRight" size={18} />
              <div><span class="section-title">Pinned release</span><div class="mono big" data-testid="pinned-version">{rel?.version ?? '—'}</div></div>
              <div class="st">
                {#if !rel}<span class="badge outline">no pinned release known yet</span>
                {:else if rel.upgradeAvailable}<span class="badge accent" data-testid="rel-badge">Upgrade available</span>
                {:else if !rel.newer}<span class="badge warn" data-testid="rel-badge">the pin is older than the installed version</span>
                {:else}<span class="badge ok" data-testid="rel-badge">Up to date</span>{/if}
                {#if rel}<span class="faint small">{rel.source === 'release-check' ? 'checked' : 'from the cloud view'}{#if rel.checkedAt} · <Time ts={rel.checkedAt} />{/if}</span>{/if}
              </div>
            </div>
            <div class="row acts">
              <Btn icon="shield" busy={checking} onclick={checkRelease} disabled={!rel} data-testid="check-release">Check release</Btn>
              {#if rel?.upgradeAvailable}
                <Btn variant="primary" icon="download" disabled={!!blocked || !!(watching && !TERM.includes(watching.phase)) || i.enrollment.state !== 'enrolled'} title={blocked ?? (i.enrollment.state !== 'enrolled' ? 'The site is not enrolled' : '')} onclick={() => openUpgrade(false)} data-testid="upgrade-open">Upgrade to {rel.version}</Btn>
              {:else if rel}
                <Btn variant="ghost" icon="refresh" disabled={!!blocked} title={blocked ?? 'opm-agent upgrade --force'} onclick={() => openUpgrade(true)} data-testid="reinstall-open">Reinstall {rel.version}</Btn>
              {/if}
            </div>
          </div>
          {#if blocked && rel?.upgradeAvailable}<p class="small warn-text" style="margin:0" data-testid="upgrade-blocked">{blocked}</p>{/if}
          {#if rel?.notes}
            <details class="notes" data-testid="release-notes">
              <summary>Release notes · {rel.version}</summary>
              <pre class="nt">{rel.notes}</pre>
            </details>
          {/if}
          {#if checkErr}<div class="banner bad small" data-testid="check-error"><Icon name="alert" /><div class="grow">Release check failed: {checkErr}</div></div>{/if}
          {#if checked?.verification}<VerificationCard v={checked.verification} />{/if}
        </div>
      </section>

      {#if shown && dismissed !== shown.upgradeId}
        <div class="gap"></div>
        <UpgradeProgress p={shown} {lastPhase} {siteRestarting} ondismiss={() => ((dismissed = shown.upgradeId), (watching = null))} />
      {/if}

      {#if (ustate.data?.history.length ?? 0) > 0}
        <section class="card hist" data-testid="upgrade-history">
          <div class="card-head"><h2>Upgrade history</h2><span class="faint small">last 10 · survives restarts</span></div>
          <div class="table-wrap">
            <table class="table">
              <thead><tr><th>When</th><th>Version</th><th>Via</th><th>By</th><th>Result</th><th>Detail</th></tr></thead>
              <tbody>
                {#each ustate.data?.history ?? [] as h (h.upgradeId)}
                  <tr data-testid="hist-{h.phase}">
                    <td class="small nowrap"><Time ts={h.startedAt} /></td>
                    <td class="mono small nowrap">{h.from} → {h.to}</td>
                    <td class="small">{h.via}</td>
                    <td class="small">{h.actor ?? '—'}</td>
                    <td><span class="badge {h.phase === 'done' ? 'ok' : h.phase === 'rolled-back' ? 'warn' : h.phase === 'failed' ? 'bad' : 'accent'}">{h.phase}</span></td>
                    <td class="small err">{h.error ?? (h.finishedAt ? `took ${Math.max(1, Math.round((Date.parse(h.finishedAt) - Date.parse(h.startedAt)) / 1000))} s` : '—')}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </section>
      {/if}

      {#if multi && o}
        <div class="gap"></div>
        <UpgradeAll tiles={o.sites} infos={agentAll.data ?? []} self={o.self} pin={(agentAll.data?.find((x) => x.self)?.release?.version ?? (isSelf ? rel?.version : null)) ?? null} selfStartedAt={selfInfo?.startedAt ?? null} blocked={null} onchanged={() => (agentAll.load(), info.load(), overview.load())} />
      {/if}

      <!-- ─────────────────────────── operations -->
      <h2 class="sect">Operations</h2>
      <div class="ops">
        <section class="card card-pad c" data-testid="restart-card">
          <h3><Icon name="rotate" size={15} />Restart agent</h3>
          <p class="small muted">Stops and starts <span class="mono">opm-agent</span> ({i.supervisor.kind === 'systemd' ? 'systemctl restart' : 're-exec'}). The containers keep running; you stay logged in. A save of agent.json below can restart it for you.</p>
          {#if remoteRestart}<p class="small"><span class="spin" aria-hidden="true"></span> {slug} is restarting…</p>{/if}
          <div class="row"><Btn icon="rotate" disabled={!!blocked || remoteRestart} title={blocked ?? ''} onclick={openRestart} data-testid="restart-open">Restart agent…</Btn></div>
        </section>
        <section class="card card-pad c" data-testid="support-card">
          <h3><Icon name="archive" size={15} />Support bundle</h3>
          <p class="small muted">One <span class="mono">.tar.gz</span> for the operations team. Secrets are masked; the download is logged.</p>
          <ul class="secs small" data-testid="bundle-sections">
            {#each SUPPORT_BUNDLE_SECTIONS as s (s)}<li><span class="mono">{s}</span> <span class="muted">— {SECTION_TEXT[s] ?? ''}</span></li>{/each}
          </ul>
          <div class="row"><Btn icon="download" busy={sbBusy} onclick={supportBundle} data-testid="support-bundle">Download support bundle</Btn></div>
        </section>
      </div>

      <div class="gap"></div>
      {#if !i.configPath && config.error}
        <section class="card" data-testid="config-none">
          <div class="card-head"><h2 class="row tight"><Icon name="file" size={15} />agent.json</h2></div>
          <div class="card-body"><p class="small muted">This agent runs without an agent.json file (no <span class="mono">--config</span>, e.g. a dev mesh), so there is nothing to view or edit here.</p></div>
        </section>
      {:else}
      <Load loading={config.loading} loaded={config.loaded} error={config.error} retry={() => config.load()} rows={4}>
        {#if config.data}<ConfigCard site={slug} agentPort={i.ports.agentPort} view={config.data} {blocked} onsaved={onSaved} onreload={() => (config.load(), info.load())} />{/if}
      </Load>
      {/if}

      <div class="gap"></div>
      <section class="card" data-testid="uninstall-card">
        <div class="card-head"><h2 class="row tight"><Icon name="trash" size={15} />Uninstall</h2><Btn size="xs" icon="copy" onclick={() => copyText(i.uninstall.join('\n'), 'uninstall commands')} data-testid="uninstall-copy">Copy all</Btn></div>
        <div class="card-body stack">
          <div class="banner warn small"><Icon name="alert" /><div class="grow">There is no uninstall button: run these on <strong>{slug}</strong>'s server. They stop and remove the agent; the OPM containers keep running until you remove them. The last line also deletes the site identity, snapshots and volumes — only when you are sure.</div></div>
          <ol class="cmds" data-testid="uninstall-lines">
            {#each i.uninstall as l, k (k)}
              <li class:comment={l.startsWith('#')}><code>{l}</code>{#if !l.startsWith('#')}<button class="btn ghost icon xs" aria-label="Copy line {k + 1}" onclick={() => copyText(l, 'command')}><Icon name="copy" size={12} /></button>{/if}</li>
            {/each}
          </ol>
        </div>
      </section>

      <div class="gap"></div>
      <div class="banner info small" data-testid="hard-fallback">
        <Icon name="info" />
        <div class="grow"><strong>Hard fallback.</strong> If an upgrade cannot run from here or the terminal, rerun this site's original install command on the server: it detects the installed agent, asks upgrade / downgrade / reinstall, keeps everything, and rolls back if the new agent is not healthy in 60 s. <code>OPM_AGENT_VERSION=x</code> for a specific version.</div>
      </div>
      <div class="gap"></div>
      <section class="card" data-testid="cli-map">
        <div class="card-head"><h2 class="row tight"><Icon name="terminal" size={15} />opm-agent commands in this GUI</h2></div>
        <div class="table-wrap">
          <table class="table">
            <thead><tr><th>Command</th><th>What</th><th>In the GUI</th></tr></thead>
            <tbody>
              {#each CLI as c (c.cmd)}
                <tr data-testid="cli-{c.cmd.replace(/[^a-z]+/gi, '-')}">
                  <td class="mono small nowrap">{c.cmd}</td>
                  <td class="small">{c.what}</td>
                  <td class="small">{#if c.href}<a href={c.href}>{c.where}</a>{:else if c.cliOnly}<span class="badge outline">CLI only</span> <span class="muted">{c.where.replace(/^CLI only /, '')}</span>{:else}{c.where}{/if}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </section>
    {/if}
  </Load>
{/if}

<!-- ─────────────────────────── dialogs -->
<Modal bind:open={upOpen} title={force ? `Reinstall ${rel?.version ?? ''} on ${slug}?` : `Upgrade ${slug} to ${rel?.version ?? ''}?`} size="md" testid="upgrade-dialog">
  {#if i && rel}
    <div class="stack">
      <dl class="kv small">
        <dt>Site</dt><dd class="mono">{slug}{isSelf ? ' (this site)' : ''}</dd>
        <dt>Version</dt><dd class="mono" data-testid="upgrade-from-to">{i.version} → {rel.version}</dd>
        <dt>Actor</dt><dd>{session.actor?.email ?? '—'} <button class="btn ghost xs" type="button" onclick={() => ((upOpen = false), (session.askActor = true))}>Change person</button></dd>
      </dl>
      {#if rel.notes}
        <details class="notes"><summary>Release notes</summary><pre class="nt">{rel.notes}</pre></details>
      {/if}
      <div class="what small">
        <div class="section-title">What happens</div>
        <ol>
          <li><strong>Download</strong> the signed release from the cloud (resumable; works via the relay).</li>
          <li><strong>Verify</strong> the release signature, the file sha256 and a self-test of the new binary — a mismatch stops here, nothing changes.</li>
          <li><strong>Swap</strong> the binary; the current one is kept as <code>opm-agent.prev</code>.</li>
          <li><strong>Restart</strong> the agent{isSelf ? ' — this page waits and continues on the same login' : ''}. The containers keep running.</li>
          <li><strong>Health</strong>: the new agent must report healthy within {AGENT_UPGRADE_HEALTH_SEC} s, else <code>opm-agent.prev</code> is restored automatically (rollback).</li>
        </ol>
      </div>
      {#if upErr}
        <div class="banner bad small" data-testid="upgrade-error">
          <Icon name="alert" />
          <div class="grow">{upErr.msg}</div>
          {#if upErr.code === 'stale' || upErr.code === 'conflict' || upErr.code === 'locked'}<Btn size="xs" icon="refresh" onclick={() => ((upOpen = false), loadSite(), agentAll.load())} data-testid="upgrade-reload">Reload</Btn>{/if}
        </div>
      {/if}
      <div class="field">
        <label for="up-confirm">Type the site slug <code>{slug}</code> to confirm</label>
        <input id="up-confirm" class="input mono" bind:value={upConfirm} autocomplete="off" spellcheck="false" onkeydown={(e) => e.key === 'Enter' && startUpgrade()} data-testid="upgrade-confirm" />
      </div>
    </div>
  {/if}
  {#snippet footer()}
    <Btn onclick={() => (upOpen = false)} disabled={upBusy}>Cancel</Btn>
    <Btn variant="primary" icon="download" busy={upBusy} disabled={upConfirm !== slug} onclick={startUpgrade} data-testid="upgrade-go">{force ? 'Reinstall' : `Upgrade to ${rel?.version ?? ''}`}</Btn>
  {/snippet}
</Modal>

<Modal bind:open={rsOpen} title="Restart the agent on {slug}?" size="sm" testid="restart-dialog">
  <div class="stack">
    <p class="small" style="margin:0">The agent stops and starts again (about {isSelf ? 'a few seconds' : 'a few seconds; this page stays on'}). The containers keep running; you stay logged in.</p>
    <div class="field">
      <label for="rs-reason">Reason <span class="faint">(optional, in the activity log)</span></label>
      <input id="rs-reason" class="input" maxlength="200" bind:value={rsReason} placeholder="e.g. apply agent.json edited on the server" data-testid="restart-reason" />
    </div>
    <div class="field">
      <label for="rs-confirm">Type the site slug <code>{slug}</code> to confirm</label>
      <input id="rs-confirm" class="input mono" bind:value={rsConfirm} autocomplete="off" spellcheck="false" onkeydown={(e) => e.key === 'Enter' && doRestart()} data-testid="restart-confirm" />
    </div>
    {#if rsErr}<div class="banner bad small" data-testid="restart-error"><Icon name="alert" /><div class="grow">{rsErr}</div></div>{/if}
  </div>
  {#snippet footer()}
    <Btn onclick={() => (rsOpen = false)} disabled={rsBusy}>Cancel</Btn>
    <Btn variant="primary" icon="rotate" busy={rsBusy} disabled={rsConfirm !== slug} onclick={doRestart} data-testid="restart-go">Restart agent</Btn>
  {/snippet}
</Modal>


<style>
  .mb {
    margin-bottom: 14px;
  }
  .gap {
    height: 16px;
  }
  .sb {
    margin-left: 6px;
  }
  .row.tight {
    gap: 6px;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, 300px), 1fr));
    gap: 12px;
  }
  .c {
    display: flex;
    flex-direction: column;
    gap: 10px;
    min-width: 0;
  }
  .c h3 {
    display: flex;
    align-items: center;
    gap: 7px;
    font-size: var(--fs-md);
    margin: 0;
  }
  .c h3 :global(svg) {
    color: var(--accent-text);
  }
  .c p {
    margin: 0;
  }
  .c .kv {
    gap: 5px 14px;
  }
  /* the 7th card fills its row; its facts flow in two columns */
  .c.wide {
    grid-column: 1 / -1;
  }
  .c.wide .kv {
    grid-template-columns: max-content minmax(0, 1fr) max-content minmax(0, 1fr);
  }
  /* with the health card (this site): Cloud takes the rest of that row on 3-column widths */
  @media (min-width: 1280px) {
    .c.wide.withh {
      grid-column: span 2;
    }
  }
  @media (max-width: 720px) {
    .c.wide .kv {
      grid-template-columns: max-content minmax(0, 1fr);
    }
  }
  .checks {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .checks li {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
  }
  .sect {
    font-size: var(--fs-lg);
    margin: 26px 0 10px;
  }
  .rel {
    display: flex;
    gap: 14px 24px;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
  }
  .vers {
    display: flex;
    gap: 10px 18px;
    align-items: center;
    flex-wrap: wrap;
    min-width: 0;
  }
  .vers :global(svg) {
    color: var(--text-3);
  }
  .big {
    font-size: 17px;
    font-weight: var(--fw-semibold);
    overflow-wrap: anywhere;
  }
  .st {
    display: flex;
    flex-direction: column;
    gap: 3px;
    align-items: flex-start;
  }
  .acts {
    flex-wrap: wrap;
  }
  .notes summary {
    cursor: pointer;
    font-weight: var(--fw-medium);
    font-size: var(--fs-md);
  }
  .nt {
    white-space: pre-wrap;
    font-family: inherit;
    font-size: var(--fs-sm);
    color: var(--text-2);
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 10px 12px;
    margin: 8px 0 0;
    max-height: 280px;
    overflow: auto;
  }
  .hist {
    margin-top: 16px;
  }
  .err {
    overflow-wrap: anywhere;
    max-width: 520px;
  }
  .ops {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 360px), 1fr));
    gap: 12px;
  }
  .secs {
    margin: 0;
    padding-left: 18px;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .cmds {
    margin: 0;
    padding: 10px 12px 10px 34px;
    background: var(--code-bg);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    display: flex;
    flex-direction: column;
    gap: 3px;
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .cmds li {
    overflow-wrap: anywhere;
  }
  .cmds li code {
    border: 0;
    background: none;
    padding: 0;
    white-space: pre-wrap;
    word-break: break-all;
  }
  .cmds li .btn {
    margin-left: 6px;
    vertical-align: middle;
  }
  .cmds .comment code {
    color: var(--text-3);
  }
  .what ol {
    margin: 4px 0 0;
    padding-left: 20px;
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .lines {
    margin: 8px 0;
    white-space: pre-wrap;
    word-break: break-all;
  }
</style>
