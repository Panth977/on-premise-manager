<script lang="ts">
  /**
   * §21 #92 the enrolled site's "Upgrade / reinstall command" — always viewable (RevealValue, 60 s, logged as
   * install-command.revealed + audit), Copy, Regenerate (a site-bound maintenance token; the previous command stops
   * working). The same `curl … | sudo sh` command runs the upgrade-only flow on that server (§19.1 / §21 #91).
   */
  import type { SetupResponse } from '@opm/shared';
  import { api } from '$lib/api';
  import { personName } from '$lib/util/people';
  import RevealValue from '$lib/ui/RevealValue.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import Toggle from '$lib/ui/Toggle.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import { Terminal } from '$lib/ui/icons';

  type SetupSite = SetupResponse['sites'][number];
  let { deploymentId, site, canWrite, latest = null, onchanged }: { deploymentId: string; site: SetupSite; canWrite: boolean; latest?: string | null; onchanged: () => void } = $props();

  const uc = $derived(site.upgradeCommand);
  const VERSION_RE = /^[0-9A-Za-z._+-]{1,64}$/;
  let unattended = $state(false);
  let version = $state('');
  let allowDowngrade = $state(false);
  const versionOk = $derived(version.trim() === '' || VERSION_RE.test(version.trim()));
  /** env answers after sudo (shown + copied): OPM_YES=1, OPM_AGENT_VERSION=x, OPM_ALLOW_DOWNGRADE=1 */
  function withFlags(cmd: string): string {
    const env: string[] = [];
    if (unattended) env.push('OPM_YES=1');
    const v = version.trim();
    if (v && VERSION_RE.test(v)) {
      env.push(`OPM_AGENT_VERSION=${v}`);
      if (allowDowngrade) env.push('OPM_ALLOW_DOWNGRADE=1');
    }
    if (!env.length) return cmd;
    const e = env.join(' ');
    return /\bsudo\s+/.test(cmd) ? cmd.replace(/\bsudo\s+/, `sudo ${e} `) : `${e} ${cmd}`;
  }
  const HISTORY_CMD = `sudo grep -h 'v1/install/script' /root/.bash_history "$HOME/.bash_history" 2>/dev/null | tail -n 1`;
</script>

{#if uc}
  <div class="uc" data-testid="upgrade-command" data-kind={uc.kind ?? ''} data-stored={uc.stored ? '1' : '0'}>
    <div class="row between wrap hd">
      <span class="small strong row"><Icon icon={Terminal} size={14} />Upgrade / reinstall command</span>
      <span class="xs faint" data-testid="upgrade-command-meta">
        {#if uc.kind === 'maintenance'}maintenance command · regenerated <RelTime ts={uc.createdAt} /> by {personName(uc.createdBy)}{:else if uc.kind === 'install'}the install command that enrolled this site{:else}no command stored{/if}
      </span>
    </div>
    <RevealValue
      testid="upgrade-cmd"
      variant="command"
      stored={uc.stored}
      notStoredText="not stored — Regenerate to view a new one"
      canReveal={canWrite}
      canRegenerate={canWrite}
      display={withFlags}
      reveal={() => api.revealUpgradeCommand({ deploymentId, siteSlug: site.siteSlug })}
      regenerate={() => api.regenerateUpgradeCommand({ deploymentId, siteSlug: site.siteSlug })}
      confirm={{ title: `Regenerate the upgrade command of ${site.siteSlug}?`, message: 'A new command replaces the current one — the old one (also the original install command of this site) stops working at once. The new command only upgrades / reinstalls / downgrades this enrolled site; it never enrolls a new one and does not expire.', confirmLabel: 'Regenerate command' }}
      onregenerated={() => onchanged()}
      ongone={() => onchanged()}
    />
    {#if !canWrite}<span class="xs faint">needs deployment.setup.write</span>{/if}
    <div class="xs muted" data-testid="upgrade-command-help">
      Run it on <b>{site.agent?.hostname ?? site.siteSlug}</b> as root: it detects the installed agent{site.agent ? ` (v${site.agent.agentVersion})` : ''}, asks <b>UPGRADE / DOWNGRADE / SAME</b>{latest ? ` (pinned ${latest})` : ''}, keeps the setup (enrollment, data, containers keep running) and rolls back by itself if the new agent is not healthy within 60 s. Works only for this enrolled site · no expiry · never enrolls a new site.
    </div>
    {#if !uc.stored}
      <div class="xs faint" data-testid="upgrade-command-history">Or find the original install command in the server's shell history (it works until you Regenerate): <code class="mono">{HISTORY_CMD}</code></div>
    {/if}
    <details class="opts" data-testid="upgrade-options">
      <summary class="xs strong">Options (unattended, specific version)</summary>
      <div class="stack tight ob">
        <Toggle bind:checked={unattended} label="Unattended — answer yes to every prompt (OPM_YES=1)" testid="upg-opt-yes" />
        <label class="row wrap vrow">
          <span class="xs muted">Agent version</span>
          <input class="input sm mono" class:invalid={!versionOk} placeholder={latest ?? 'pinned version'} bind:value={version} data-testid="upg-opt-version" aria-label="Agent version (OPM_AGENT_VERSION)" />
          <span class="xs faint">empty = the pinned version · <code>OPM_AGENT_VERSION</code></span>
        </label>
        {#if !versionOk}<span class="xs text-red">1–64 of 0-9 A-Z a-z . _ + -</span>{/if}
        {#if version.trim() && versionOk}
          <Toggle bind:checked={allowDowngrade} label="Allow a downgrade without a terminal (OPM_ALLOW_DOWNGRADE=1)" testid="upg-opt-downgrade" />
        {/if}
        <span class="xs faint">The variables go after <code>sudo</code> so they survive it. Without a terminal and without a flag the script answers “no” and prints the command to rerun.</span>
      </div>
    </details>
  </div>
{/if}

<style>
  .uc {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px;
    border: 1px solid color-mix(in srgb, var(--accent) 30%, var(--border));
    background: var(--accent-soft);
    border-radius: var(--r-lg);
    min-width: 0;
  }
  .hd {
    gap: 8px;
  }
  .opts summary {
    cursor: pointer;
    user-select: none;
    color: var(--text-2);
  }
  .ob {
    margin-top: 6px;
    gap: 8px;
  }
  .vrow {
    gap: 8px;
    align-items: center;
  }
  .vrow input {
    width: 220px;
    max-width: 100%;
  }
  code {
    overflow-wrap: anywhere;
  }
</style>
