<script lang="ts">
  import { onDestroy, onMount, tick } from 'svelte';
  import { page } from '$app/state';
  import { AUTO_HOST_RE, type InstallTokenView, type SetupResponse } from '@opm/shared';
  import { api } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { getDeploymentScope } from '$lib/config-editor/context';
  import { toast } from '$lib/toast.svelte';
  import { personName } from '$lib/util/people';
  import { catalog } from '$lib/stores/catalog.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import RevealValue, { type RevealResult } from '$lib/ui/RevealValue.svelte';
  import Toggle from '$lib/ui/Toggle.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import { takeAccessKey } from '$lib/stores/handoff';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import StatusBadge from '$lib/ui/StatusBadge.svelte';
  import Countdown from '$lib/ui/Countdown.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import { KeyRound, Terminal, Ban, Unplug, Package, Server, Info, ExternalLink, Globe, TriangleAlert, RefreshCw } from '$lib/ui/icons';
  import CopyButton from '$lib/ui/CopyButton.svelte';
  import ClockSkew from '$lib/ui/ClockSkew.svelte';
  import BrowserClockNote from '$lib/ui/BrowserClockNote.svelte';
  import AgentRelease from '$lib/agent-upgrade/AgentRelease.svelte';
  import SiteUpgrade from '$lib/agent-upgrade/SiteUpgrade.svelte';
  import { upgradeBusy } from '$lib/agent-upgrade/upgrade';

  const scope = getDeploymentScope();
  const id = scope.id;
  const canRead = $derived(scope.can('deployment.setup', 'read'));
  const canWrite = $derived(scope.can('deployment.setup', 'write'));
  const setup = new Loader(() => api.getSetup({ deploymentId: id }));
  // §21 #90 poll every 3 s while an agent upgrade is queued / running (or an "Upgrade all" run is going), else every 30 s
  const FAST_MS = 3000;
  const IDLE_MS = 30_000;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let destroyed = false;
  const busy = $derived(upgradeBusy((setup.data?.sites ?? []).map((s) => s.upgrade), setup.data?.agentUpgradeRun));
  function schedule() {
    if (timer) clearTimeout(timer);
    if (destroyed || !canRead) return;
    timer = setTimeout(async () => {
      if (!document.hidden) await setup.load(true);
      schedule();
    }, busy ? FAST_MS : IDLE_MS);
  }
  /** reload now (after a write) and restart the poll cadence */
  async function refresh() {
    await setup.load(true);
    schedule();
  }
  $effect(() => {
    void busy; // cadence follows the state
    schedule();
  });
  onDestroy(() => {
    destroyed = true;
    if (timer) clearTimeout(timer);
  });
  onMount(() => {
    if (canRead) setup.load().then(() => scrollToSite());
    // "Rotated … by <name>" / token "By": names come from the catalog accounts
    void catalog.ensure();
  });
  /** ?site=<slug> (from the Status page): scroll to that site's card (its upgrade path when it needs the install command) */
  async function scrollToSite() {
    const slug = page.url.searchParams.get('site');
    if (!slug) return;
    await tick();
    const el = document.getElementById(`upgrade-path-${slug}`) ?? document.querySelector(`[data-testid="setup-site-${CSS.escape(slug)}"]`);
    el?.scrollIntoView({ block: 'start' });
  }

  const hosts = $derived(scope.hosts(scope.config));
  function guiUrl(slug: string, inHead: boolean): string | null {
    const sc = scope.config.sites.find((s) => s.siteSlug === slug)?.config;
    // auto host not reported yet → a readable stand-in (the agent prints its real address at install)
    const host = hosts[slug]?.host?.replace(AUTO_HOST_RE, '<machine-ip>');
    if (!sc || !host) return null;
    const port = inHead && sc.agentGuiAliasPort ? sc.agentGuiAliasPort : sc.agentPort;
    return `http://${host}:${port}/_opm/`;
  }

  // the access key handed over by "Create deployment" (in memory only) shows at once for its 60 s
  const createdKey = takeAccessKey(id);

  // install commands: every active stored token can be viewed again (RevealValue); a just-created one shows at once
  let commands = $state<Record<string, RevealResult>>({});
  let creating = $state<Record<string, boolean>>({});
  async function createCommand(slug: string) {
    creating[slug] = true;
    try {
      const r = await api.createInstallToken({ deploymentId: id, body: { siteSlug: slug } });
      commands[r.tokenHash] = r;
      toast.success(`Install command for ${slug} created`, 'Valid for 24 h, single use. View it again any time while it is active.');
      setup.load(true);
    } catch (e) {
      toast.error(e, 'Could not create the install command');
    } finally {
      creating[slug] = false;
    }
  }

  // revoke token
  let tok = $state<InstallTokenView | null>(null);
  let tokOpen = $state(false);
  async function revokeToken() {
    await api.revokeInstallToken({ deploymentId: id, tokenHash: tok!.tokenHash });
    delete commands[tok!.tokenHash];
    toast.success('Install command revoked');
    setup.load(true);
  }

  // revoke enrollment
  let enr = $state<SetupResponse['sites'][number] | null>(null);
  let enrOpen = $state(false);
  async function revokeAgent() {
    await api.revokeAgent({ deploymentId: id, siteSlug: enr!.siteSlug, body: { confirm: enr!.siteSlug } });
    toast.success(`Enrollment of ${enr!.siteSlug} revoked`, 'Create a new install command to install the site again.');
    setup.load(true);
  }

  // Regenerate… of an install command = a new one for the site (the active ones are revoked)
  async function regenerateCommand(slug: string): Promise<RevealResult> {
    const r = await api.createInstallToken({ deploymentId: id, body: { siteSlug: slug, replaceActive: true } });
    commands[r.tokenHash] = r;
    return r;
  }
  // §21 #91 "Unattended install": no-TTY answers as env AFTER sudo (a display transformation of the API command, shown + copied).
  // OPM_SKIP_DOCKER_INSTALL=1 (round G) stays an alias of OPM_INSTALL_DOCKER=0 — a command that already has it is left alone.
  let unattended = $state(false);
  let dockerChoice = $state<'ask' | 'install' | 'skip'>('ask');
  function withFlags(cmd: string): string {
    const env: string[] = [];
    if (unattended && !cmd.includes('OPM_YES=')) env.push('OPM_YES=1');
    if (dockerChoice !== 'ask' && !/OPM_INSTALL_DOCKER=|OPM_SKIP_DOCKER_INSTALL=/.test(cmd)) env.push(`OPM_INSTALL_DOCKER=${dockerChoice === 'install' ? 1 : 0}`);
    if (!env.length) return cmd;
    const e = env.join(' ');
    return /\bsudo\s+/.test(cmd) ? cmd.replace(/\bsudo\s+/, `sudo ${e} `) : `${e} ${cmd}`;
  }
  /** Docker Engine API version → the first Docker release that speaks it (installDeps.minDockerApi) */
  const DOCKER_OF_API: Record<string, string> = { '1.41': '20.10', '1.42': '23.0', '1.43': '24.0', '1.44': '25.0', '1.45': '26.0', '1.46': '27.0', '1.47': '27.2', '1.48': '28.0', '1.49': '28.1', '1.50': '28.2', '1.51': '28.3' };
  const minDocker = (api: string) => (DOCKER_OF_API[api] ? `Docker ≥ ${DOCKER_OF_API[api]}` : `Docker with Engine API ≥ ${api}`);
  const ROTATE_MSG =
    'A new key replaces the current one. The old key stops working at each site as soon as it receives the new hash (sites switch one by one). Open sessions stay valid until their 6 h end.';
  const sites = $derived(setup.data?.sites ?? []);

  // §21 #86: agent GUI URLs of an enrolled site = what its last heartbeat reported (port 80 first, as the cloud orders
  // them); agents < 0.8 report nothing → the default ports on the first IPv4 address
  type SetupSite = SetupResponse['sites'][number];
  const KIND_LABEL: Record<string, string> = { edge: 'edge', 'gui-alias': 'alias port', 'agent-port': 'agent port' };
  function agentUrls(s: SetupSite): { url: string; label: string }[] {
    if (s.guiUrls?.length) return s.guiUrls.map((u) => ({ url: u.url, label: KIND_LABEL[u.kind] ?? u.kind }));
    const a = s.agent!;
    const host = (a.detectedAddresses ?? []).find((x) => /^\d+\.\d+\.\d+\.\d+$/.test(x)) || a.ip;
    if (!host) return [];
    return [
      { url: `http://${host}:8080/_opm/`, label: 'alias port' },
      { url: `http://${host}:7080/_opm/`, label: 'agent port' },
    ];
  }

</script>

<svelte:head><title>Setup · {scope.label} · OPM</title></svelte:head>

<div class="page">
  <PageHeader title="Setup" subtitle="Install the agent on every site and manage the agent GUI access key." icon={KeyRound} readOnly={canRead && !canWrite}>
    {#snippet actions()}
      <Button icon={Package} disabled tip="Offline install bundle — later" data-testid="offline-bundle">Offline install bundle</Button>
    {/snippet}
  </PageHeader>

  {#if !canRead}
    <EmptyState icon={KeyRound} title="No access" text="You need deployment.setup.read to see the setup of this deployment." />
  {:else if setup.loading && !setup.data}
    <TableSkeleton cols={5} rows={3} />
  {:else if setup.error}
    <ErrorState message={setup.error} onretry={() => setup.load()} />
  {:else if setup.data}
    {@const sd = setup.data}
    <BrowserClockNote />
    <AgentRelease deploymentId={id} data={sd} {canWrite} onchanged={refresh} />
    <section class="card key" data-testid="access-key-panel">
      <div class="ki"><Icon icon={KeyRound} size={20} /></div>
      <div class="grow">
        <h3>Agent GUI access key</h3>
        <p class="muted small">One key for every site of the deployment. People log in to the agent GUI with it. View it any time (shown for 60 s, logged); Regenerate makes a new one.</p>
        <dl class="kv small kvm">
          <dt>Key</dt>
          <dd>
            <RevealValue
              testid="access-key"
              stored={sd.accessKey.stored}
              lengthHint={29}
              initial={createdKey}
              canReveal={canWrite}
              canRegenerate={canWrite}
              reveal={() => api.revealAccessKey({ deploymentId: id })}
              regenerate={() => api.rotateAccessKey({ deploymentId: id })}
              confirm={{ title: 'Regenerate the access key?', message: ROTATE_MSG, typed: 'ACCEPT', confirmLabel: 'Regenerate key', danger: true }}
              onregenerated={() => setup.load(true)}
            />
            {#if !canWrite}<span class="xs faint">needs deployment.setup.write</span>{/if}
          </dd>
          <dt>Version</dt><dd><b data-testid="key-version">v{sd.accessKey.version}</b></dd>
          <dt>Rotated</dt><dd><RelTime ts={sd.accessKey.rotatedAt} /> by {personName(sd.accessKey.rotatedBy)}</dd>
          <dt>Sites</dt>
          <dd class="row wrap">
            {#each sd.sites.filter((s) => s.agent) as s (s.siteSlug)}
              <span class="badge {s.agent!.keyVersion === sd.accessKey.version ? 'green' : 'amber'}" data-tip={s.agent!.keyVersion === sd.accessKey.version ? 'has the current key' : 'still on an older key; it switches when it receives the new hash'}>{s.siteSlug} · v{s.agent!.keyVersion}</span>
            {:else}<span class="faint">no site enrolled</span>{/each}
          </dd>
        </dl>
      </div>
    </section>

    <div class="section-title section"><h3>Sites</h3><span class="muted small">every site of the draft, the head and enrolled agents</span></div>
    {#if !sites.length}
      <EmptyState icon={Server} title="No sites yet" text="Add a site to the draft first.">
        {#snippet action()}<Button href="/app/deployments/{id}/sites" variant="primary">Go to Sites</Button>{/snippet}
      </EmptyState>
    {:else}
      <div class="card help small" data-testid="install-help">
        <div class="hint"><Icon icon={RefreshCw} size={14} /><span><b>Upgrade an installed site:</b> agent 0.10+ → <b>Upgrade agent</b> on its card below. Older agents, once: run the site's <b>Upgrade / reinstall command</b> (on its card — always viewable, or Regenerate) on that server, or <code>sudo opm-agent upgrade</code> (agent 0.8+). That command is also the hard fallback for any up- or downgrade.</span></div>
        <div class="hint"><Icon icon={TriangleAlert} size={14} /><span><b>Firewall:</b> open TCP 80 (and 443, 8080, 7080) in the server firewall AND any cloud firewall (e.g. DigitalOcean Cloud Firewall).</span></div>
      </div>
      <div class="sites">
        {#each sites as s (s.siteSlug)}
          {@const url = guiUrl(s.siteSlug, s.inHead)}
          <article class="card site" data-testid="setup-site-{s.siteSlug}">
            <header class="sh">
              <span class="dot {s.agent ? (s.agent.online ? 'green' : 'red') : 'gray'}"></span>
              <div class="grow" style="min-width:0">
                <div class="row wrap"><span class="strong mono">{s.siteSlug}</span><span class="muted">{s.label}</span>
                  {#if s.inDraft}<span class="badge blue">in draft</span>{:else}<span class="badge">not in draft</span>{/if}
                  {#if s.inHead}<span class="badge violet">in head</span>{/if}
                </div>
              </div>
              {#if s.agent}
                <span class="badge green" data-testid="enrolled">enrolled</span>
                {#if canWrite}<Button size="sm" variant="danger-outline" icon={Unplug} onclick={() => ((enr = s), (enrOpen = true))} data-testid="revoke-enrollment">Revoke enrollment</Button>{/if}
              {:else}
                <span class="badge amber">not installed</span>
                {#if canWrite && s.inDraft && !s.tokens.some((t) => t.kind === 'install' && t.state === 'active')}<Button size="sm" variant="primary" icon={Terminal} loading={creating[s.siteSlug]} onclick={() => createCommand(s.siteSlug)} data-testid="create-install">Create install command</Button>{/if}
              {/if}
            </header>
            <div class="sb">
              {#if s.agent}
                <dl class="kv small">
                  <dt>Agent</dt><dd>v{s.agent.agentVersion} · {s.agent.os}/{s.agent.arch}</dd>
                  <dt>Host</dt><dd class="mono">{s.agent.hostname} · {s.agent.ip}</dd>
                  <dt>Last seen</dt><dd data-testid="last-seen"><RelTime ts={s.agent.lastSeenAt} /> {#if s.agent.online}<span class="badge green">online</span>{:else}<span class="badge red">offline</span>{/if}</dd>
                  <dt>Clock</dt><dd><ClockSkew skew={s.agent.clockSkew} /></dd>
                  <dt>Enrolled</dt><dd><RelTime ts={s.agent.enrolledAt} /></dd>
                  <dt>Key version</dt><dd>v{s.agent.keyVersion}</dd>
                </dl>
                {@const urls = agentUrls(s)}
                <div class="gui" data-testid="gui-urls">
                  <span class="small strong row"><Icon icon={Globe} size={14} />Agent GUI</span>
                  {#each s.portAlerts ?? [] as pa (pa.purpose + pa.port)}
                    <div class="alert" data-testid="port-alert" data-port={pa.port}><Icon icon={TriangleAlert} size={14} /><span>{pa.message}</span></div>
                  {/each}
                  {#if s.defaultEdge}<div class="xs faint" data-testid="default-edge">Port 80: default edge (no configuration applied yet) — GUI at <code>/_opm/</code></div>{/if}
                  {#if urls.length}
                    <ul class="urls">
                      {#each urls as u (u.url)}
                        <li data-testid="gui-url">
                          <a href={u.url} target="_blank" rel="noreferrer" class="mono">{u.url} <Icon icon={ExternalLink} size={11} /></a>
                          <span class="xs faint">{u.label}</span>
                          <CopyButton text={u.url} tip="Copy URL" />
                        </li>
                      {/each}
                    </ul>
                    {#if !s.guiUrls?.length}<div class="xs faint" data-testid="gui-urls-fallback">(default ports — agent did not report its listeners)</div>{/if}
                  {:else}
                    <div class="xs faint">No address reported yet.</div>
                  {/if}
                </div>
                <SiteUpgrade deploymentId={id} site={s} release={sd.agentRelease} run={sd.agentUpgradeRun} {canWrite} onchanged={refresh} />
              {/if}
              {#if !s.agent}
                {#each s.tokens.filter((t) => t.kind === 'install' && t.state === 'active') as t (t.tokenHash)}
                  <div class="cmd" data-testid="install-command" data-token={t.tokenHash}>
                    <div class="row between wrap"><span class="small strong">Run on the machine as root</span><span class="row xs"><span class="faint">single use ·</span>{#if t.expiresAt}<Countdown until={t.expiresAt} />{/if}</span></div>
                    <RevealValue
                      testid="install-cmd"
                      variant="command"
                      stored={t.stored}
                      initial={commands[t.tokenHash] ?? null}
                      canReveal={canWrite}
                      canRegenerate={canWrite && s.inDraft}
                      display={withFlags}
                      reveal={() => api.revealInstallCommand({ deploymentId: id, tokenHash: t.tokenHash })}
                      regenerate={() => regenerateCommand(s.siteSlug)}
                      confirm={{ title: `Regenerate the install command for ${s.siteSlug}?`, message: 'A new command replaces this one; this one stops working at once.', confirmLabel: 'Regenerate command' }}
                      onregenerated={() => setup.load(true)}
                      ongone={() => setup.load(true)}
                    />
                    <div class="stack tight deps">
                      <span class="xs muted" data-testid="install-deps">{#if sd.installDeps}{`Asks before installing Docker ${sd.installDeps.dockerVersion} when it is missing (uses your existing ${minDocker(sd.installDeps.minDockerApi)} without asking)`}{#if sd.installDeps.agentVersion}{` · agent ${sd.installDeps.agentVersion}`}{/if}{:else}Asks before installing Docker when it is missing (uses your existing Docker if recent enough){/if}</span>
                      <details class="unatt" data-testid="unattended">
                        <summary class="xs strong">Unattended install (no terminal prompts)</summary>
                        <div class="stack tight ub">
                          <Toggle bind:checked={unattended} label="Answer yes to every prompt (OPM_YES=1)" testid="opt-yes" />
                          <div class="row wrap" style="gap:8px"><span class="xs muted">Docker when missing</span><SegmentedControl size="sm" ariaLabel="Docker when missing" testid="opt-docker" options={[{ value: 'ask', label: 'Ask', title: 'prompt on the server (no terminal → no)' }, { value: 'install', label: 'Install', title: 'OPM_INSTALL_DOCKER=1' }, { value: 'skip', label: 'Skip', title: 'OPM_INSTALL_DOCKER=0' }]} value={dockerChoice} onchange={(v) => (dockerChoice = v as typeof dockerChoice)} /></div>
                          <span class="xs faint">Install = <code>OPM_INSTALL_DOCKER=1</code>, Skip = <code>OPM_INSTALL_DOCKER=0</code> (you install Docker ≥ 20.10 yourself). The variables go after <code>sudo</code> so they survive it. Without a terminal and without a flag the script answers “no” and prints the command to rerun.</span>
                        </div>
                      </details>
                    </div>
                  </div>
                {/each}
              {/if}
              {#if url && !s.agent}
                <div class="hint small"><Icon icon={Info} size={14} /><span>Agent GUI at <a href={url} target="_blank" rel="noreferrer" class="mono">{url} <Icon icon={ExternalLink} size={11} /></a>{#if !s.inHead} (agentPort before the first setup){/if}. Also on every edge host at <code>/_opm/</code> after the first setup.</span></div>
              {:else if hosts[s.siteSlug]?.error}
                <div class="hint small text-amber">The site host does not resolve yet: {hosts[s.siteSlug]?.error?.message}</div>
              {/if}
              {#if s.tokens.length}
                <div class="tokens">
                  <div class="xs faint strong up">Install commands</div>
                  <div class="scroll-x"><table class="table compact">
                    <thead><tr><th>Created</th><th>By</th><th>State</th><th>Expires</th><th class="actions"></th></tr></thead>
                    <tbody>
                      {#each s.tokens as t (t.tokenHash)}
                        <tr data-testid="token-row">
                          <td class="small"><RelTime ts={t.createdAt} /></td>
                          <td class="small">{personName(t.createdBy)}</td>
                          <td><StatusBadge kind="token" value={t.state} />{#if t.kind === 'maintenance'} <span class="badge violet" data-testid="token-maintenance" data-tip="Upgrade-only command of the enrolled site (made by Regenerate); never enrolls; no expiry">upgrade only</span>{/if}{#if t.upgradeCommand} <span class="badge blue" data-testid="token-upgrade" data-tip="The site's current Upgrade / reinstall command (view it on the card above)">upgrade command</span>{/if}</td>
                          <td class="small">{#if t.kind === 'maintenance' && t.state === 'active'}no expiry{:else if t.state === 'active' && t.expiresAt}<Countdown until={t.expiresAt} prefix="" />{:else if t.revokedAt}revoked <RelTime ts={t.revokedAt} />{:else if t.usedAt}used <RelTime ts={t.usedAt} />{:else}<RelTime ts={t.expiresAt} />{/if}</td>
                          <td class="actions">{#if canWrite && t.state === 'active'}<Button size="sm" variant="ghost" icon={Ban} onclick={() => ((tok = t), (tokOpen = true))} data-testid="revoke-token">Revoke</Button>{/if}</td>
                        </tr>
                      {/each}
                    </tbody>
                  </table></div>
                </div>
              {/if}
            </div>
          </article>
        {/each}
      </div>
    {/if}
  {/if}
</div>

<ConfirmDialog bind:open={tokOpen} title="Revoke this install command?" message="The command stops working at once. A site that already enrolled with it stays enrolled." confirmLabel="Revoke" danger onconfirm={revokeToken} />
<ConfirmDialog bind:open={enrOpen} title="Revoke the enrollment of {enr?.siteSlug}?" message="The site key stops working: the agent can no longer reach the cloud or its peers. Needed before a re-install. Containers keep running." confirmLabel="Revoke enrollment" danger typed={enr?.siteSlug ?? ''} typedLabel="Type the site slug" onconfirm={revokeAgent} />

<style>
  .key {
    display: flex;
    gap: 14px;
    padding: 18px 20px;
    align-items: flex-start;
    flex-wrap: wrap;
  }
  .ki {
    width: 40px;
    height: 40px;
    border-radius: 10px;
    background: var(--accent-soft);
    color: var(--accent-text);
    display: grid;
    place-items: center;
    flex: none;
  }
  .kvm {
    margin-top: 10px;
  }
  .sites {
    display: flex;
    flex-direction: column;
    gap: var(--sp-3);
  }
  .sh {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
  }
  .sb {
    padding: 12px 16px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .cmd {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px;
    border: 1px solid color-mix(in srgb, var(--accent) 35%, var(--border));
    background: var(--accent-soft);
    border-radius: var(--r-lg);
  }
  .deps {
    gap: 6px;
  }
  .unatt summary {
    cursor: pointer;
    user-select: none;
    color: var(--text-2);
  }
  .ub {
    margin-top: 6px;
    gap: 8px;
  }
  .hint {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    color: var(--text-2);
  }
  .gui {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding-top: 10px;
    border-top: 1px solid var(--border);
  }
  .alert {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    padding: 8px 10px;
    border-radius: var(--r-md);
    border: 1px solid color-mix(in srgb, var(--amber) 40%, var(--border));
    background: var(--amber-soft);
    color: var(--amber-text);
    font-size: 13px;
  }
  .urls {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .urls li {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    min-width: 0;
  }
  .urls a {
    overflow-wrap: anywhere;
  }
  .help {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 12px 16px;
    margin-bottom: var(--sp-3);
  }
  .up {
    text-transform: uppercase;
    letter-spacing: 0.06em;
    margin-bottom: 6px;
  }
  .tokens .table {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
  }
</style>
